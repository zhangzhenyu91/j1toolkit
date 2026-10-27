package logging

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"shade/internal/config"
)

// ---- database/sql 假驱动（纯标准库；不连真实库）----

type fakeResult int64

func (r fakeResult) LastInsertId() (int64, error) { return int64(r), nil }
func (r fakeResult) RowsAffected() (int64, error) { return 1, nil }

type fakeCall struct {
	query string
	args  []driver.Value
}

type fakeDB struct {
	mu      sync.Mutex
	calls   []fakeCall
	execErr error
}

// inserts 取全部 app_log 写入记录。
func (d *fakeDB) inserts() []fakeCall {
	d.mu.Lock()
	defer d.mu.Unlock()
	var out []fakeCall
	for _, c := range d.calls {
		if strings.Contains(c.query, "INSERT INTO app_log") {
			out = append(out, c)
		}
	}
	return out
}

type fakeConn struct{ db *fakeDB }

func (c *fakeConn) Prepare(string) (driver.Stmt, error) { return nil, errors.New("unsupported") }
func (c *fakeConn) Close() error                        { return nil }
func (c *fakeConn) Begin() (driver.Tx, error)           { return nil, errors.New("unsupported") }

func (c *fakeConn) ExecContext(_ context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	vals := make([]driver.Value, len(args))
	for i, a := range args {
		vals[i] = a.Value
	}
	c.db.mu.Lock()
	defer c.db.mu.Unlock()
	c.db.calls = append(c.db.calls, fakeCall{query: query, args: vals})
	if c.db.execErr != nil {
		return nil, c.db.execErr
	}
	return fakeResult(1), nil
}

type fakeDriver struct{ db *fakeDB }

func (d fakeDriver) Open(string) (driver.Conn, error) { return &fakeConn{db: d.db}, nil }

var fakeSeq atomic.Int64

func newFakeDB(t *testing.T) (*sql.DB, *fakeDB) {
	t.Helper()
	f := &fakeDB{}
	name := fmt.Sprintf("logging-fake-%d", fakeSeq.Add(1))
	sql.Register(name, fakeDriver{db: f})
	db, err := sql.Open(name, "")
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db, f
}

// argsText 把插入参数拼成可检索文本。
func argsText(args []driver.Value) string {
	parts := make([]string, len(args))
	for i, a := range args {
		parts[i] = fmt.Sprint(a)
	}
	return strings.Join(parts, "|")
}

// ---- 链路 ID ----

func TestNewTraceID(t *testing.T) {
	a, b := NewTraceID(), NewTraceID()
	if len(a) != 32 {
		t.Fatalf("trace id 长度 = %d，期望 32", len(a))
	}
	if a == b {
		t.Fatal("两次生成的 trace id 不应相同")
	}
	for _, r := range a {
		if !strings.ContainsRune("0123456789abcdef", r) {
			t.Fatalf("trace id 含非法字符: %q", a)
		}
	}
}

func TestTraceContext(t *testing.T) {
	if got := TraceIDFromContext(context.Background()); got != "" {
		t.Fatalf("空 context 应无 trace id，得到 %q", got)
	}
	ctx := WithTraceID(context.Background(), "trace-abc.1")
	if got := TraceIDFromContext(ctx); got != "trace-abc.1" {
		t.Fatalf("trace id = %q", got)
	}
	// 非法字符与超长拒绝（保持原 ctx 不变）
	if got := TraceIDFromContext(WithTraceID(context.Background(), "bad id!")); got != "" {
		t.Fatalf("非法 trace id 应被拒绝，得到 %q", got)
	}
	if got := TraceIDFromContext(WithTraceID(context.Background(), strings.Repeat("a", 65))); got != "" {
		t.Fatalf("超长 trace id 应被拒绝，得到 %q", got)
	}
	if got := TraceIDFromContext(WithTraceID(ctx, "next")); got != "next" {
		t.Fatalf("覆盖后 trace id = %q", got)
	}
}

// ---- 落库与 trace_id 贯穿 ----

func TestWriteEntryFillsTraceFromContext(t *testing.T) {
	db, fake := newFakeDB(t)
	ctx := WithTraceID(context.Background(), "trace-xyz")
	err := WriteEntry(ctx, db, config.AppLogEntry{
		AppCode: "work-log", Level: "info", Category: "access", Event: "http.request",
		Message: "GET /x 200", MessageCN: "访问成功", UserID: 7, TeamID: 42,
	})
	if err != nil {
		t.Fatalf("WriteEntry: %v", err)
	}
	rows := fake.inserts()
	if len(rows) != 1 {
		t.Fatalf("app_log 写入 %d 条，期望 1 条", len(rows))
	}
	text := argsText(rows[0].args)
	for _, want := range []string{"work-log", "trace-xyz", "访问成功", "http.request"} {
		if !strings.Contains(text, want) {
			t.Fatalf("插入参数缺 %q：%s", want, text)
		}
	}
}

func TestWriteEntryKeepsExplicitTrace(t *testing.T) {
	db, fake := newFakeDB(t)
	ctx := WithTraceID(context.Background(), "ctx-trace")
	if err := WriteEntry(ctx, db, config.AppLogEntry{AppCode: "system", Message: "m", TraceID: "row-trace"}); err != nil {
		t.Fatalf("WriteEntry: %v", err)
	}
	text := argsText(fake.inserts()[0].args)
	if !strings.Contains(text, "row-trace") || strings.Contains(text, "ctx-trace") {
		t.Fatalf("显式 trace id 应优先：%s", text)
	}
}

func TestLoggerRecordFillsTraceFromContext(t *testing.T) {
	db, fake := newFakeDB(t)
	l := New(db, "quiz")
	ctx := WithTraceID(context.Background(), "trace-rec")
	if err := l.Info(ctx, "business", "ev.done", "完成", "done", map[string]any{"n": 1}); err != nil {
		t.Fatalf("Record: %v", err)
	}
	text := argsText(fake.inserts()[0].args)
	if !strings.Contains(text, "trace-rec") || !strings.Contains(text, "quiz") {
		t.Fatalf("插入参数缺 trace/app：%s", text)
	}
}

func TestWriteEntryNilDB(t *testing.T) {
	if err := WriteEntry(context.Background(), nil, config.AppLogEntry{}); err == nil {
		t.Fatal("db 为空应报错")
	}
	var l *Logger
	if err := l.Record(context.Background(), "info", "", "", "", "x", nil); err == nil {
		t.Fatal("nil Logger 应报错")
	}
	if err := New(nil, "x").Record(context.Background(), "info", "", "", "", "x", nil); err == nil {
		t.Fatal("未配置数据库应报错")
	}
}

func TestWriteEntrySwallowsDBError(t *testing.T) {
	db, fake := newFakeDB(t)
	fake.mu.Lock()
	fake.execErr = errors.New("db down")
	fake.mu.Unlock()
	if err := WriteEntry(context.Background(), db, config.AppLogEntry{Message: "m"}); err == nil {
		t.Fatal("落库失败应返回错误（由调用方决定是否吞掉）")
	}
}
