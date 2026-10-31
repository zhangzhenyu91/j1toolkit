package treehazard

// 本文件是测试专用轻量 database/sql 驱动（fake）：按 SQL 子串匹配返回预置行/错误，
// 并记录执行语句与参数，覆盖关键 SQL / 权限 / 导入导出 / 幂等与 skipped 逻辑，
// 不依赖外部数据库、不调用真实外部服务。

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type fakeRule struct {
	match string
	cols  []string
	rows  [][]driver.Value
	err   error
}

type fakeExec struct {
	query string
	args  []driver.Value
}

type fakeDB struct {
	mu      sync.Mutex
	rules   []fakeRule
	queries []string
	execs   []fakeExec
	events  []string
}

// addEvent 记录事务/外部副作用事件（提交后 COS 删除顺序断言用）。
func (f *fakeDB) addEvent(event string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.events = append(f.events, event)
}

// eventLog 取事件快照（保序）。
func (f *fakeDB) eventLog() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string{}, f.events...)
}

func (f *fakeDB) on(match string, cols []string, rows [][]driver.Value) *fakeDB {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rules = append(f.rules, fakeRule{match: match, cols: cols, rows: rows})
	return f
}

func (f *fakeDB) onErr(match string, err error) *fakeDB {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rules = append(f.rules, fakeRule{match: match, err: err})
	return f
}

func (f *fakeDB) execCount(substr string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, e := range f.execs {
		if strings.Contains(e.query, substr) {
			n++
		}
	}
	return n
}

func (f *fakeDB) lastExec(substr string) (fakeExec, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := len(f.execs) - 1; i >= 0; i-- {
		if strings.Contains(f.execs[i].query, substr) {
			return f.execs[i], true
		}
	}
	return fakeExec{}, false
}

// allExec 按子串取全部执行记录（保序）。
func (f *fakeDB) allExec(substr string) []fakeExec {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]fakeExec, 0)
	for _, e := range f.execs {
		if strings.Contains(e.query, substr) {
			out = append(out, e)
		}
	}
	return out
}

func (f *fakeDB) allQueries() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := append([]string{}, f.queries...)
	for _, e := range f.execs {
		out = append(out, e.query)
	}
	return out
}

func (f *fakeDB) find(query string) *fakeRule {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := range f.rules {
		if strings.Contains(query, f.rules[i].match) {
			return &f.rules[i]
		}
	}
	return nil
}

var fakeSeq atomic.Int64

// openFake 打开一个 fake *sql.DB。
func openFake(t interface{ Fatalf(string, ...any) }) (*sql.DB, *fakeDB) {
	f := &fakeDB{}
	name := fmt.Sprintf("treehazard-fake-%d", fakeSeq.Add(1))
	sql.Register(name, &fakeDriver{db: f})
	db, err := sql.Open(name, "fake")
	if err != nil {
		t.Fatalf("打开 fake db 失败: %v", err)
	}
	return db, f
}

type fakeDriver struct{ db *fakeDB }

func (d *fakeDriver) Open(string) (driver.Conn, error) { return &fakeConn{db: d.db}, nil }

type fakeConn struct{ db *fakeDB }

func (c *fakeConn) Prepare(q string) (driver.Stmt, error) { return &fakeStmt{db: c.db, query: q}, nil }
func (c *fakeConn) Close() error                          { return nil }
func (c *fakeConn) Begin() (driver.Tx, error)             { return fakeTx{db: c.db}, nil }

func (c *fakeConn) BeginTx(context.Context, driver.TxOptions) (driver.Tx, error) {
	return fakeTx{db: c.db}, nil
}

func (c *fakeConn) QueryContext(_ context.Context, q string, args []driver.NamedValue) (driver.Rows, error) {
	vals := make([]driver.Value, 0, len(args))
	for _, a := range args {
		vals = append(vals, a.Value)
	}
	_ = vals
	return c.db.queryRows(q)
}

func (c *fakeConn) ExecContext(_ context.Context, q string, args []driver.NamedValue) (driver.Result, error) {
	vals := make([]driver.Value, 0, len(args))
	for _, a := range args {
		vals = append(vals, a.Value)
	}
	return c.db.exec(q, vals)
}

type fakeTx struct{ db *fakeDB }

func (t fakeTx) Commit() error {
	if t.db != nil {
		t.db.addEvent("commit")
	}
	return nil
}

func (t fakeTx) Rollback() error {
	if t.db != nil {
		t.db.addEvent("rollback")
	}
	return nil
}

type fakeStmt struct {
	db    *fakeDB
	query string
}

func (s *fakeStmt) Close() error  { return nil }
func (s *fakeStmt) NumInput() int { return -1 }

func (s *fakeStmt) Exec(args []driver.Value) (driver.Result, error) {
	return s.db.exec(s.query, args)
}

func (s *fakeStmt) Query(args []driver.Value) (driver.Rows, error) {
	return s.db.queryRows(s.query)
}

func (f *fakeDB) queryRows(q string) (driver.Rows, error) {
	f.mu.Lock()
	f.queries = append(f.queries, q)
	f.mu.Unlock()
	rule := f.find(q)
	if rule == nil {
		return &fakeRows{}, nil
	}
	if rule.err != nil {
		return nil, rule.err
	}
	return &fakeRows{cols: rule.cols, data: rule.rows}, nil
}

func (f *fakeDB) exec(q string, args []driver.Value) (driver.Result, error) {
	f.mu.Lock()
	f.execs = append(f.execs, fakeExec{query: q, args: args})
	f.mu.Unlock()
	rule := f.find(q)
	if rule != nil && rule.err != nil {
		return nil, rule.err
	}
	return fakeResult{affected: 1}, nil
}

type fakeResult struct{ affected int64 }

func (r fakeResult) LastInsertId() (int64, error) { return 100, nil }
func (r fakeResult) RowsAffected() (int64, error) { return r.affected, nil }

type fakeRows struct {
	cols []string
	data [][]driver.Value
	pos  int
}

func (r *fakeRows) Columns() []string { return r.cols }
func (r *fakeRows) Close() error      { return nil }

func (r *fakeRows) Next(dest []driver.Value) error {
	if r.pos >= len(r.data) {
		return io.EOF
	}
	copy(dest, r.data[r.pos])
	r.pos++
	return nil
}

// 测试用对象存储（记录调用，不联网）。
type memObjects struct {
	mu         sync.Mutex
	puts       []string
	deletes    []string
	failPut    bool
	failDelete bool
	putHook    func(key string) error
	deleteHook func(key string)
}

func (s *memObjects) Put(_ context.Context, key string, _ []byte, _ string) (string, error) {
	s.mu.Lock()
	hook := s.putHook
	s.mu.Unlock()
	if hook != nil {
		if err := hook(key); err != nil {
			return "", err
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failPut {
		return "", fmt.Errorf("对象存储不可用")
	}
	s.puts = append(s.puts, key)
	return "https://cdn.example/" + key, nil
}

func (s *memObjects) PublicURL(key string) string { return "https://cdn.example/" + key }

func (s *memObjects) Delete(_ context.Context, key string) error {
	s.mu.Lock()
	s.deletes = append(s.deletes, key)
	hook, fail := s.deleteHook, s.failDelete
	s.mu.Unlock()
	if hook != nil {
		hook(key)
	}
	if fail {
		return fmt.Errorf("对象存储删除失败")
	}
	return nil
}

// 测试用配置（map 桩）。
type memConfig map[string]string

func (c memConfig) Get(_ context.Context, key string) (string, error) {
	v, ok := c[key]
	if !ok {
		return "", fmt.Errorf("配置键不存在: %s", key)
	}
	return v, nil
}

var (
	_ driver.Conn           = (*fakeConn)(nil)
	_ driver.QueryerContext = (*fakeConn)(nil)
	_ driver.ExecerContext  = (*fakeConn)(nil)
	_ driver.ConnBeginTx    = (*fakeConn)(nil)
	_ driver.Rows           = (*fakeRows)(nil)
	_ ObjectStore           = (*memObjects)(nil)
	_ ConfigReader          = memConfig{}
	_                       = time.Now
)
