package bridge

// 测试用迷你 fake DB driver（按 SQL 子串匹配应答）与通用桩：桥内 SQL 真实现的单测铺底。
// 与 worklog/fake_test.go 同模式，仅测试文件内使用。

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

// fakeRule 单条查询应答规则（match 为 SQL 子串）。
type fakeRule struct {
	match string
	cols  []string
	rows  [][]driver.Value
	err   error
}

// fakeDB 迷你查询桩。
type fakeDB struct {
	mu    sync.Mutex
	rules []fakeRule
	execs []string
}

// on 登记查询应答。
func (f *fakeDB) on(match string, cols []string, rows [][]driver.Value) *fakeDB {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rules = append(f.rules, fakeRule{match: match, cols: cols, rows: rows})
	return f
}

// find 按子串找首个命中规则。
func (f *fakeDB) find(q string) *fakeRule {
	for i := range f.rules {
		if strings.Contains(q, f.rules[i].match) {
			return &f.rules[i]
		}
	}
	return nil
}

// execCount 记录 Exec 次数（子串匹配）。
func (f *fakeDB) execCount(substr string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, q := range f.execs {
		if strings.Contains(q, substr) {
			n++
		}
	}
	return n
}

// openFake 打开内存 fake 库（驱动名按调用序去重）。
var fakeSeq atomic.Int64

func openFake(t *testing.T) (*sql.DB, *fakeDB) {
	t.Helper()
	fb := &fakeDB{}
	name := fmt.Sprintf("bridgefake-%s-%d", t.Name(), fakeSeq.Add(1))
	sql.Register(name, &fakeDriver{db: fb})
	db, err := sql.Open(name, "")
	if err != nil {
		t.Fatalf("open fake db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db, fb
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

func (c *fakeConn) QueryContext(_ context.Context, q string, _ []driver.NamedValue) (driver.Rows, error) {
	return c.db.query(q)
}

func (c *fakeConn) ExecContext(_ context.Context, q string, _ []driver.NamedValue) (driver.Result, error) {
	c.db.mu.Lock()
	defer c.db.mu.Unlock()
	c.db.execs = append(c.db.execs, q)
	return driver.RowsAffected(1), nil
}

func (f *fakeDB) query(q string) (driver.Rows, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	r := f.find(q)
	if r == nil {
		return nil, errors.New("fake db: 未登记的查询: " + q)
	}
	if r.err != nil {
		return nil, r.err
	}
	return &fakeRows{cols: r.cols, rows: r.rows}, nil
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

func (s *fakeStmt) Exec(args []driver.Value) (driver.Result, error) {
	s.db.mu.Lock()
	defer s.db.mu.Unlock()
	s.db.execs = append(s.db.execs, s.query)
	return driver.RowsAffected(1), nil
}

func (s *fakeStmt) Query(args []driver.Value) (driver.Rows, error) { return s.db.query(s.query) }

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
