package main

// 启动装配测试：MySQL DSN 自动拼装参数（MYSQL_DSN 直填原样）与连接池上限解析/应用。
// 不连真实 MySQL（sql.Open 惰性、不 Ping）。

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"shade/internal/logging"
)

// TestMysqlDSNPassthrough MYSQL_DSN 直填时保持原样，不追加任何参数。
func TestMysqlDSNPassthrough(t *testing.T) {
	const raw = "user:pass@tcp(db.example:3307)/shade?timeout=9s&readTimeout=3s"
	t.Setenv("MYSQL_DSN", raw)
	t.Setenv("MYSQL_USER", "ignored")
	t.Setenv("MYSQL_DATABASE", "ignored")
	if got := mysqlDSN(); got != raw {
		t.Fatalf("MYSQL_DSN 应原样返回，得到 %q", got)
	}
}

// TestMysqlDSNAutoAssembly 自动拼装分支带 timeout=5s 与 interpolateParams=true，并保留既有参数。
func TestMysqlDSNAutoAssembly(t *testing.T) {
	t.Setenv("MYSQL_DSN", "")
	t.Setenv("MYSQL_USER", "shade")
	t.Setenv("MYSQL_PASSWORD", "secret")
	t.Setenv("MYSQL_HOST", "mysql.internal")
	t.Setenv("MYSQL_PORT", "3306")
	t.Setenv("MYSQL_DATABASE", "shade_db")
	got := mysqlDSN()
	if !strings.HasPrefix(got, "shade:secret@tcp(mysql.internal:3306)/shade_db?") {
		t.Fatalf("DSN 前缀不符: %q", got)
	}
	for _, want := range []string{"parseTime=true", "charset=utf8mb4", "loc=Local", "timeout=5s", "interpolateParams=true"} {
		if !strings.Contains(got, want) {
			t.Fatalf("DSN 缺少 %s: %q", want, got)
		}
	}
}

// TestMysqlPoolLimits 池上限默认 32/16，env 可覆盖，非法/非正值回退默认。
func TestMysqlPoolLimits(t *testing.T) {
	t.Setenv("MYSQL_MAX_OPEN_CONNS", "")
	t.Setenv("MYSQL_MAX_IDLE_CONNS", "")
	if open, idle := mysqlPoolLimits(); open != 32 || idle != 16 {
		t.Fatalf("默认池上限应为 32/16，实际 %d/%d", open, idle)
	}
	t.Setenv("MYSQL_MAX_OPEN_CONNS", "48")
	t.Setenv("MYSQL_MAX_IDLE_CONNS", "24")
	if open, idle := mysqlPoolLimits(); open != 48 || idle != 24 {
		t.Fatalf("env 覆盖后应为 48/24，实际 %d/%d", open, idle)
	}
	for _, bad := range []string{"0", "-3", "abc"} {
		t.Setenv("MYSQL_MAX_OPEN_CONNS", bad)
		t.Setenv("MYSQL_MAX_IDLE_CONNS", bad)
		if open, idle := mysqlPoolLimits(); open != 32 || idle != 16 {
			t.Fatalf("非法值 %q 应回退 32/16，实际 %d/%d", bad, open, idle)
		}
	}
}

// TestConfigurePoolAppliesLimits 池配置真实落到 *sql.DB（MaxOpenConns 可从 Stats 读回）。
func TestConfigurePoolAppliesLimits(t *testing.T) {
	t.Setenv("MYSQL_MAX_OPEN_CONNS", "24")
	t.Setenv("MYSQL_MAX_IDLE_CONNS", "12")
	db, err := sql.Open("mysql", "user:pass@tcp(127.0.0.1:1)/shade")
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	defer db.Close()
	configurePool(db)
	if got := db.Stats().MaxOpenConnections; got != 24 {
		t.Fatalf("MaxOpenConns 应为 24，实际 %d", got)
	}

	t.Setenv("MYSQL_MAX_OPEN_CONNS", "")
	t.Setenv("MYSQL_MAX_IDLE_CONNS", "")
	configurePool(db)
	if got := db.Stats().MaxOpenConnections; got != 32 {
		t.Fatalf("默认 MaxOpenConns 应为 32，实际 %d", got)
	}
}

// TestAppLogSettingsFromEnv 启动期日志 env：目录默认 ./data/logs，批量 100、fsync batch；
// 非法值回退默认（APP_LOG_RETENTION_DAYS 等运行配置不在此处读取）。
func TestAppLogSettingsFromEnv(t *testing.T) {
	t.Setenv("APP_LOG_DIR", "")
	t.Setenv("APP_LOG_BATCH_SIZE", "")
	t.Setenv("APP_LOG_FSYNC", "")
	got := appLogSettingsFromEnv()
	if got.Dir != "./data/logs" || got.Options.BatchSize != 100 || got.Options.FsyncMode != logging.FsyncBatch {
		t.Fatalf("默认设置 = %+v", got)
	}
	t.Setenv("APP_LOG_DIR", "/var/log/shade")
	t.Setenv("APP_LOG_BATCH_SIZE", "250")
	t.Setenv("APP_LOG_FSYNC", "always")
	got = appLogSettingsFromEnv()
	if got.Dir != "/var/log/shade" || got.Options.BatchSize != 250 || got.Options.FsyncMode != logging.FsyncAlways {
		t.Fatalf("env 覆盖 = %+v", got)
	}
	t.Setenv("APP_LOG_BATCH_SIZE", "abc")
	t.Setenv("APP_LOG_FSYNC", "sometimes")
	got = appLogSettingsFromEnv()
	if got.Options.BatchSize != 100 || got.Options.FsyncMode != logging.FsyncBatch {
		t.Fatalf("非法值应回退 = %+v", got)
	}
}

// ---- 保留清理 log_id 对账测试（假驱动；不连真实 MySQL）----

func testDay(y int, m time.Month, d int) time.Time {
	return time.Date(y, m, d, 0, 0, 0, 0, time.Local)
}

type mainLogFake struct {
	mu         sync.Mutex
	days       []time.Time
	ids        []string
	deletes    int
	deleteRows int64
}

func (f *mainLogFake) setRows(days []time.Time, ids []string) {
	f.mu.Lock()
	f.days, f.ids = days, ids
	f.mu.Unlock()
}

type mainLogRows struct {
	days []time.Time
	ids  []string
	i    int
}

func (r *mainLogRows) Columns() []string {
	if r.ids != nil {
		return []string{"log_id"}
	}
	return []string{"d"}
}
func (r *mainLogRows) Close() error { return nil }
func (r *mainLogRows) Next(dest []driver.Value) error {
	if r.ids != nil {
		if r.i >= len(r.ids) {
			return io.EOF
		}
		dest[0] = r.ids[r.i]
		r.i++
		return nil
	}
	if r.i >= len(r.days) {
		return io.EOF
	}
	dest[0] = r.days[r.i]
	r.i++
	return nil
}

type mainLogResult int64

func (r mainLogResult) LastInsertId() (int64, error) { return 0, nil }
func (r mainLogResult) RowsAffected() (int64, error) { return int64(r), nil }

type mainLogConn struct{ db *mainLogFake }

func (c *mainLogConn) Prepare(string) (driver.Stmt, error) { return nil, errors.New("unsupported") }
func (c *mainLogConn) Close() error                        { return nil }
func (c *mainLogConn) Begin() (driver.Tx, error)           { return nil, errors.New("unsupported") }

func (c *mainLogConn) QueryContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Rows, error) {
	if strings.Contains(query, "log_id") {
		c.db.mu.Lock()
		ids := append([]string(nil), c.db.ids...)
		c.db.mu.Unlock()
		return &mainLogRows{ids: ids}, nil
	}
	if strings.Contains(query, "DATE(occurred_at)") {
		c.db.mu.Lock()
		days := append([]time.Time(nil), c.db.days...)
		c.db.mu.Unlock()
		return &mainLogRows{days: days}, nil
	}
	return &mainLogRows{}, nil
}

func (c *mainLogConn) ExecContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Result, error) {
	if strings.Contains(query, "DELETE FROM app_log") {
		c.db.mu.Lock()
		c.db.deletes++
		rows := c.db.deleteRows
		c.db.mu.Unlock()
		return mainLogResult(rows), nil
	}
	return mainLogResult(0), nil
}

type mainLogDriver struct{ db *mainLogFake }

func (d mainLogDriver) Open(string) (driver.Conn, error) { return &mainLogConn{db: d.db}, nil }

var mainLogSeq atomic.Int64

func newMainLogDB(t *testing.T, days []time.Time) (*sql.DB, *mainLogFake) {
	t.Helper()
	f := &mainLogFake{days: days}
	name := fmt.Sprintf("main-log-fake-%d", mainLogSeq.Add(1))
	sql.Register(name, mainLogDriver{db: f})
	db, err := sql.Open(name, "")
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db, f
}

// newCleanupSink 构造日志持久层，并按需写入历史段（app-20200101）。
func newCleanupSink(t *testing.T, lines ...string) (*logging.DurableSink, string) {
	t.Helper()
	dir := t.TempDir()
	if len(lines) > 0 {
		if err := os.WriteFile(filepath.Join(dir, "app-20200101-000001.log"),
			[]byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
			t.Fatalf("写段文件: %v", err)
		}
	}
	sink, err := logging.NewDurableSinkWithOptions(dir, logging.DefaultDurableOptions())
	if err != nil {
		t.Fatalf("NewDurableSink: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = sink.CloseContext(ctx)
	})
	return sink, dir
}

func logLineID(id string) string {
	return `{"log_id":"` + id + `","occurred_at":"2020-01-01T00:00:00Z","app_code":"system","category":"runtime"}`
}

// TestCleanupAppLogDBSkipsDBOnlyHistoryRows 待删 DB 行仅存在于数据库（历史回退/文件缺失）
// 时必须 fail-closed，不执行 DELETE。
func TestCleanupAppLogDBSkipsDBOnlyHistoryRows(t *testing.T) {
	sink, _ := newCleanupSink(t, logLineID("id-1"))
	db, fake := newMainLogDB(t, nil)
	fake.setRows([]time.Time{testDay(2020, 1, 1)}, []string{"id-1", "db-only"})
	n, err := cleanupAppLogDB(context.Background(), db, sink, 30)
	if err == nil || n != -1 {
		t.Fatalf("DB-only 历史行应跳过（-1/error），得到 %d/%v", n, err)
	}
	if fake.deletes != 0 {
		t.Fatalf("DB-only 行存在时不得 DELETE，实际 %d 次", fake.deletes)
	}
}

// TestCleanupAppLogDBSkipsEmptyLogID 待删 DB 行 log_id 为空时无法对账，必须跳过。
func TestCleanupAppLogDBSkipsEmptyLogID(t *testing.T) {
	sink, _ := newCleanupSink(t, logLineID("id-1"))
	db, fake := newMainLogDB(t, nil)
	fake.setRows(nil, []string{"id-1", ""})
	if n, err := cleanupAppLogDB(context.Background(), db, sink, 30); err == nil || n != -1 {
		t.Fatalf("空 log_id 应 fail-closed，得到 %d/%v", n, err)
	}
	if fake.deletes != 0 {
		t.Fatalf("空 log_id 时不得 DELETE")
	}
}

// TestCleanupAppLogDBSkipsCorruptFile 文件存在坏行/半行时无法证明覆盖，必须跳过。
func TestCleanupAppLogDBSkipsCorruptFile(t *testing.T) {
	sink, _ := newCleanupSink(t, logLineID("id-1"), "{bad json}")
	db, fake := newMainLogDB(t, nil)
	fake.setRows(nil, []string{"id-1"})
	if n, err := cleanupAppLogDB(context.Background(), db, sink, 30); err == nil || n != -1 {
		t.Fatalf("坏行应 fail-closed，得到 %d/%v", n, err)
	}
	if fake.deletes != 0 {
		t.Fatalf("坏行时不得 DELETE")
	}
}

// TestCleanupAppLogDBDeletesWhenEveryIDCovered 每个待删 log_id 都在文件中时才允许清理。
func TestCleanupAppLogDBDeletesWhenEveryIDCovered(t *testing.T) {
	sink, _ := newCleanupSink(t, logLineID("id-1"), logLineID("id-3"))
	db, fake := newMainLogDB(t, nil)
	fake.setRows([]time.Time{testDay(2020, 1, 1), testDay(2020, 1, 3)}, []string{"id-1", "id-3"})
	n, err := cleanupAppLogDB(context.Background(), db, sink, 30)
	if err != nil || n != 0 {
		t.Fatalf("覆盖完整应清理，得到 %d/%v", n, err)
	}
	if fake.deletes != 1 {
		t.Fatalf("覆盖完整时 DELETE 次数 = %d，期望 1", fake.deletes)
	}
}

// TestCleanupAppLogDBRetentionZeroNoop 保留 0（永久）时不得读取/删除 DB 行。
func TestCleanupAppLogDBRetentionZeroNoop(t *testing.T) {
	sink, _ := newCleanupSink(t, logLineID("id-1"))
	db, fake := newMainLogDB(t, nil)
	fake.setRows(nil, []string{"id-missing"})
	n, err := cleanupAppLogDB(context.Background(), db, sink, 0)
	if err != nil || n != 0 {
		t.Fatalf("保留 0 应直接返回 0，得到 %d/%v", n, err)
	}
	if fake.deletes != 0 {
		t.Fatalf("保留 0 不得 DELETE")
	}
}

// TestRecordFatalWritesErrorAndFsync 致命日志以 error 级别写 DurableSink（urgent fsync），且脱敏。
func TestRecordFatalWritesErrorAndFsync(t *testing.T) {
	dir := t.TempDir()
	sink, err := logging.NewDurableSink(dir)
	if err != nil {
		t.Fatalf("NewDurableSink: %v", err)
	}
	recordFatal(sink, "fatal token=SECRET boom")
	if got := sink.Health().Syncs; got < 1 {
		t.Fatalf("fatal 日志未触发 fsync：Syncs=%d", got)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = sink.CloseContext(ctx)
	files, _ := filepath.Glob(filepath.Join(dir, "app-*.log"))
	if len(files) == 0 {
		t.Fatal("未生成日志段")
	}
	data, _ := os.ReadFile(files[0])
	text := string(data)
	for _, want := range []string{`"level":"error"`, `"event":"runtime.fatal"`, "fatal"} {
		if !strings.Contains(text, want) {
			t.Fatalf("fatal 段落缺 %q：%s", want, text)
		}
	}
	if strings.Contains(text, "SECRET") {
		t.Fatalf("fatal 日志未脱敏：%s", text)
	}
}
