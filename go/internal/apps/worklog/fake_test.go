package worklog

// 测试专用轻量 database/sql 驱动（fake）：按 SQL 子串匹配返回预置行/错误，
// 并记录执行语句与参数，覆盖关键 SQL / 权限 / 核验 / 字典逻辑，
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
	name := fmt.Sprintf("worklog-fake-%d", fakeSeq.Add(1))
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
func (c *fakeConn) Begin() (driver.Tx, error)             { return fakeTx{}, nil }

func (c *fakeConn) BeginTx(context.Context, driver.TxOptions) (driver.Tx, error) {
	return fakeTx{}, nil
}

func (c *fakeConn) QueryContext(_ context.Context, q string, args []driver.NamedValue) (driver.Rows, error) {
	return c.db.queryRows(q)
}

func (c *fakeConn) ExecContext(_ context.Context, q string, args []driver.NamedValue) (driver.Result, error) {
	vals := make([]driver.Value, 0, len(args))
	for _, a := range args {
		vals = append(vals, a.Value)
	}
	return c.db.exec(q, vals)
}

type fakeTx struct{}

func (fakeTx) Commit() error   { return nil }
func (fakeTx) Rollback() error { return nil }

type fakeStmt struct {
	db    *fakeDB
	query string
}

func (s *fakeStmt) Close() error                                    { return nil }
func (s *fakeStmt) NumInput() int                                   { return -1 }
func (s *fakeStmt) Exec(args []driver.Value) (driver.Result, error) { return s.db.exec(s.query, args) }
func (s *fakeStmt) Query(args []driver.Value) (driver.Rows, error)  { return s.db.queryRows(s.query) }

func (s *fakeStmt) ExecContext(_ context.Context, args []driver.NamedValue) (driver.Result, error) {
	vals := make([]driver.Value, 0, len(args))
	for _, a := range args {
		vals = append(vals, a.Value)
	}
	return s.db.exec(s.query, vals)
}

func (s *fakeStmt) QueryContext(_ context.Context, args []driver.NamedValue) (driver.Rows, error) {
	return s.db.queryRows(s.query)
}

func (f *fakeDB) queryRows(query string) (driver.Rows, error) {
	f.mu.Lock()
	f.queries = append(f.queries, query)
	f.mu.Unlock()
	rule := f.find(query)
	if rule == nil {
		return &fakeRows{}, nil
	}
	if rule.err != nil {
		return nil, rule.err
	}
	return &fakeRows{cols: rule.cols, rows: rule.rows}, nil
}

func (f *fakeDB) exec(query string, args []driver.Value) (driver.Result, error) {
	f.mu.Lock()
	f.execs = append(f.execs, fakeExec{query: query, args: args})
	f.mu.Unlock()
	rule := f.find(query)
	if rule != nil && rule.err != nil {
		return nil, rule.err
	}
	return fakeResult(fakeSeq.Add(1)), nil
}

type fakeResult int64

func (r fakeResult) LastInsertId() (int64, error) { return int64(r), nil }
func (r fakeResult) RowsAffected() (int64, error) { return 1, nil }

type fakeRows struct {
	cols []string
	rows [][]driver.Value
	i    int
}

func (r *fakeRows) Columns() []string { return r.cols }
func (r *fakeRows) Close() error      { return nil }

func (r *fakeRows) Next(dest []driver.Value) error {
	if r.i >= len(r.rows) {
		return io.EOF
	}
	copy(dest, r.rows[r.i])
	r.i++
	return nil
}
