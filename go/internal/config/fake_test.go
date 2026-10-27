package config

import (
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// ---- database/sql 假驱动（纯标准库；不引入 go-sqlmock，测试不连真实库）----

type fakeResult int64

func (r fakeResult) LastInsertId() (int64, error) { return 0, nil }
func (r fakeResult) RowsAffected() (int64, error) { return int64(r), nil }

type fakeRows struct {
	cols []string
	data [][]driver.Value
	i    int
}

func (r *fakeRows) Columns() []string { return r.cols }
func (r *fakeRows) Close() error      { return nil }

func (r *fakeRows) Next(dest []driver.Value) error {
	if r.i >= len(r.data) {
		return io.EOF
	}
	copy(dest, r.data[r.i])
	r.i++
	return nil
}

// fakeCall 一次执行记录
type fakeCall struct {
	query string
	args  []driver.Value
}

// fakeDB 假数据库状态：记录调用 + 可注入响应
type fakeDB struct {
	mu      sync.Mutex
	execs   []fakeCall
	queries []fakeCall
	// onExec 自定义 Exec 响应（nil 时默认 RowsAffected=1）
	onExec func(query string, args []driver.Value) (driver.Result, error)
	// onQuery 自定义 Query 响应（nil 时空结果集 → sql.ErrNoRows）
	onQuery func(query string, args []driver.Value) (*fakeRows, error)
}

func (d *fakeDB) exec(query string, args []driver.Value) (driver.Result, error) {
	d.mu.Lock()
	d.execs = append(d.execs, fakeCall{query, append([]driver.Value(nil), args...)})
	h := d.onExec
	d.mu.Unlock()
	if h != nil {
		return h(query, args)
	}
	return fakeResult(1), nil
}

func (d *fakeDB) query(query string, args []driver.Value) (driver.Rows, error) {
	d.mu.Lock()
	d.queries = append(d.queries, fakeCall{query, append([]driver.Value(nil), args...)})
	h := d.onQuery
	d.mu.Unlock()
	if h != nil {
		return h(query, args)
	}
	return &fakeRows{cols: []string{"v"}}, nil
}

func (d *fakeDB) execsWith(prefix string) []fakeCall {
	d.mu.Lock()
	defer d.mu.Unlock()
	var out []fakeCall
	for _, c := range d.execs {
		if len(c.query) >= len(prefix) && (prefix == "" || containsStr(c.query, prefix)) {
			out = append(out, c)
		}
	}
	return out
}

func containsStr(s, sub string) bool {
	return len(sub) == 0 || (len(s) >= len(sub) && indexOfStr(s, sub) >= 0)
}

func indexOfStr(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
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
	return s.db.query(s.query, args)
}

type fakeConn struct{ db *fakeDB }

func (c *fakeConn) Prepare(q string) (driver.Stmt, error) { return &fakeStmt{c.db, q}, nil }
func (c *fakeConn) Close() error                          { return nil }
func (c *fakeConn) Begin() (driver.Tx, error) {
	return nil, errors.New("fake: 事务未实现")
}

type fakeDriver struct{ db *fakeDB }

func (d *fakeDriver) Open(string) (driver.Conn, error) { return &fakeConn{d.db}, nil }

var fakeSeq int64

// openFake 注册并打开一个假数据库连接（每个测试独立）
func openFake(t *testing.T) (*sql.DB, *fakeDB) {
	t.Helper()
	fdb := &fakeDB{}
	name := fmt.Sprintf("fake-%d", atomic.AddInt64(&fakeSeq, 1))
	sql.Register(name, &fakeDriver{db: fdb})
	sdb, err := sql.Open(name, "")
	if err != nil {
		t.Fatalf("打开假库失败: %v", err)
	}
	t.Cleanup(func() { sdb.Close() })
	return sdb, fdb
}

// rowsOf 组装返回行
func rowsOf(cols []string, vals ...[]driver.Value) *fakeRows {
	return &fakeRows{cols: cols, data: vals}
}

// approxTime 断言时间接近（±窗口）
func approxTime(got, want time.Time, window time.Duration) bool {
	d := got.Sub(want)
	if d < 0 {
		d = -d
	}
	return d <= window
}
