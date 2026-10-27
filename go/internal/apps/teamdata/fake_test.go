package teamdata

// 本文件是测试专用轻量 database/sql 驱动（fake）：按 SQL 子串匹配返回预置行/错误，
// 并记录执行语句与参数，覆盖关键 SQL / 权限 / 导入导出 / 跳过计数逻辑，
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
	match    string
	cols     []string
	rows     [][]driver.Value
	err      error
	affected int64 // 0 → 按 1 处理
	lastID   int64 // 0 → 按 100 处理
}

type fakeExec struct {
	query string
	args  []driver.Value
}

type fakeDB struct {
	mu      sync.Mutex
	rules   []fakeRule
	queries []fakeExec
	execs   []fakeExec
}

// on 注册查询规则（返回预置行）。
func (f *fakeDB) on(match string, cols []string, rows [][]driver.Value) *fakeDB {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rules = append(f.rules, fakeRule{match: match, cols: cols, rows: rows})
	return f
}

// onErr 注册错误规则（查询/执行均返回该错误）。
func (f *fakeDB) onErr(match string, err error) *fakeDB {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rules = append(f.rules, fakeRule{match: match, err: err})
	return f
}

// onExec 注册执行规则（指定影响行数与 insertId）。
func (f *fakeDB) onExec(match string, affected, lastID int64) *fakeDB {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rules = append(f.rules, fakeRule{match: match, affected: affected, lastID: lastID})
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

// lastExec 最近一次匹配的执行（含参数）。
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

// allExecs 全部执行（含参数）。
func (f *fakeDB) allExecs() []fakeExec {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]fakeExec{}, f.execs...)
}

// lastQuery 最近一次匹配的查询（含参数）。
func (f *fakeDB) lastQuery(substr string) (fakeExec, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := len(f.queries) - 1; i >= 0; i-- {
		if strings.Contains(f.queries[i].query, substr) {
			return f.queries[i], true
		}
	}
	return fakeExec{}, false
}

// find 按最后注册优先匹配（便于测试中重注册规则覆盖旧行为）。
func (f *fakeDB) find(query string) *fakeRule {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := len(f.rules) - 1; i >= 0; i-- {
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
	name := fmt.Sprintf("teamdata-fake-%d", fakeSeq.Add(1))
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
	c.db.mu.Lock()
	c.db.queries = append(c.db.queries, fakeExec{query: q, args: namedValues(args)})
	c.db.mu.Unlock()
	rule := c.db.find(q)
	if rule == nil {
		return &fakeRows{}, nil
	}
	if rule.err != nil {
		return nil, rule.err
	}
	return &fakeRows{cols: rule.cols, data: rule.rows}, nil
}

func (c *fakeConn) ExecContext(_ context.Context, q string, args []driver.NamedValue) (driver.Result, error) {
	c.db.mu.Lock()
	c.db.execs = append(c.db.execs, fakeExec{query: q, args: namedValues(args)})
	c.db.mu.Unlock()
	rule := c.db.find(q)
	if rule != nil && rule.err != nil {
		return nil, rule.err
	}
	res := fakeResult{affected: 1, lastID: 100}
	if rule != nil {
		if rule.affected > 0 {
			res.affected = rule.affected
		}
		if rule.lastID > 0 {
			res.lastID = rule.lastID
		}
	}
	return res, nil
}

func namedValues(args []driver.NamedValue) []driver.Value {
	vals := make([]driver.Value, 0, len(args))
	for _, a := range args {
		vals = append(vals, a.Value)
	}
	return vals
}

type fakeTx struct{}

func (fakeTx) Commit() error   { return nil }
func (fakeTx) Rollback() error { return nil }

type fakeStmt struct {
	db    *fakeDB
	query string
}

func (s *fakeStmt) Close() error  { return nil }
func (s *fakeStmt) NumInput() int { return -1 }

func (s *fakeStmt) Exec([]driver.Value) (driver.Result, error) {
	return nil, fmt.Errorf("fake: 走 ExecerContext，不应 Prepare")
}

func (s *fakeStmt) Query([]driver.Value) (driver.Rows, error) {
	return nil, fmt.Errorf("fake: 走 QueryerContext，不应 Prepare")
}

type fakeResult struct{ affected, lastID int64 }

func (r fakeResult) LastInsertId() (int64, error) { return r.lastID, nil }
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

// 测试用应用权限桩。
type fakePermit struct {
	allow map[string]bool // app_key → 是否放行
	err   error
}

func (p fakePermit) Allow(_ context.Context, _ int64, appKey string) (bool, error) {
	if p.err != nil {
		return false, p.err
	}
	return p.allow[appKey], nil
}

var (
	_ driver.Conn           = (*fakeConn)(nil)
	_ driver.QueryerContext = (*fakeConn)(nil)
	_ driver.ExecerContext  = (*fakeConn)(nil)
	_ driver.ConnBeginTx    = (*fakeConn)(nil)
	_ driver.Rows           = (*fakeRows)(nil)
	_ AppPermit             = fakePermit{}
)
