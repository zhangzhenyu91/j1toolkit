package sgccclockin

// 本文件是测试专用轻量 database/sql 驱动（fake）：按 SQL 子串匹配返回预置行/错误，
// 并记录执行语句与参数，不依赖外部数据库、不调用真实外部服务。

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/go-chi/chi/v5"
	"shade/internal/auth"
	"shade/internal/httpapi"
)

type fakeResult int64

func (r fakeResult) LastInsertId() (int64, error) { return int64(r), nil }
func (r fakeResult) RowsAffected() (int64, error) { return 1, nil }

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

type fakeCall struct {
	query string
	args  []driver.Value
}

type fakeRoute struct {
	match  string
	queues []func() (driver.Rows, driver.Result, error)
	always bool
}

type fakeDB struct {
	mu    sync.Mutex
	calls []fakeCall
	rout  []fakeRoute
}

func (d *fakeDB) onRows(match string, cols []string, rows ...[]driver.Value) {
	d.mu.Lock()
	defer d.mu.Unlock()
	q := append([][]driver.Value(nil), rows...)
	d.rout = append(d.rout, fakeRoute{match: match, queues: []func() (driver.Rows, driver.Result, error){
		func() (driver.Rows, driver.Result, error) {
			data := q
			q = nil
			return &fakeRows{cols: cols, data: data}, nil, nil
		},
	}})
}

func (d *fakeDB) onRowsAlways(match string, cols []string, rows ...[]driver.Value) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.rout = append(d.rout, fakeRoute{match: match, always: true, queues: []func() (driver.Rows, driver.Result, error){
		func() (driver.Rows, driver.Result, error) {
			return &fakeRows{cols: cols, data: append([][]driver.Value(nil), rows...)}, nil, nil
		},
	}})
}

// onRowsSeq 顺序应答：每次查询消费一批行（首批 → 次批 … 依次；耗尽后该路由回空行）。
// 同一 SQL 须按调用次序返回不同行（如按名查成员、人级解析账号）时用它。
func (d *fakeDB) onRowsSeq(match string, cols []string, batches ...[]driver.Value) {
	d.mu.Lock()
	defer d.mu.Unlock()
	qs := append([][]driver.Value(nil), batches...)
	route := fakeRoute{match: match}
	for range qs {
		batch := qs[0]
		qs = qs[1:]
		route.queues = append(route.queues, func() (driver.Rows, driver.Result, error) {
			return &fakeRows{cols: cols, data: [][]driver.Value{batch}}, nil, nil
		})
	}
	d.rout = append(d.rout, route)
}

func (d *fakeDB) onExec(match string, id int64) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.rout = append(d.rout, fakeRoute{match: match, queues: []func() (driver.Rows, driver.Result, error){
		func() (driver.Rows, driver.Result, error) { return nil, fakeResult(id), nil },
	}})
}

func (d *fakeDB) onErr(match string, err error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.rout = append(d.rout, fakeRoute{match: match, queues: []func() (driver.Rows, driver.Result, error){
		func() (driver.Rows, driver.Result, error) { return nil, nil, err },
	}})
}

func (d *fakeDB) dispatch(query string, args []driver.Value, isExec bool) (driver.Rows, driver.Result, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.calls = append(d.calls, fakeCall{query: query, args: append([]driver.Value(nil), args...)})
	for i := range d.rout {
		route := &d.rout[i]
		if !strings.Contains(query, route.match) {
			continue
		}
		if route.always && len(route.queues) > 0 {
			return route.queues[0]()
		}
		if len(route.queues) > 0 {
			fn := route.queues[0]
			route.queues = route.queues[1:]
			return fn()
		}
		if isExec {
			return nil, fakeResult(1), nil
		}
		return &fakeRows{cols: []string{"v"}}, nil, nil
	}
	if isExec {
		return nil, fakeResult(1), nil
	}
	return &fakeRows{cols: []string{"v"}}, nil, nil
}

func (d *fakeDB) callsWith(sub string) []fakeCall {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := []fakeCall{}
	for _, c := range d.calls {
		if strings.Contains(c.query, sub) {
			out = append(out, c)
		}
	}
	return out
}

type fakeStmt struct {
	db *fakeDB
	q  string
}

func (s *fakeStmt) Close() error  { return nil }
func (s *fakeStmt) NumInput() int { return -1 }

func (s *fakeStmt) Exec(args []driver.Value) (driver.Result, error) {
	_, r, err := s.db.dispatch(s.q, args, true)
	return r, err
}

func (s *fakeStmt) Query(args []driver.Value) (driver.Rows, error) {
	r, _, err := s.db.dispatch(s.q, args, false)
	return r, err
}

type fakeTx struct{ db *fakeDB }

func (t *fakeTx) Commit() error   { return nil }
func (t *fakeTx) Rollback() error { return nil }

type fakeConn struct{ db *fakeDB }

func (c *fakeConn) Prepare(q string) (driver.Stmt, error) { return &fakeStmt{c.db, q}, nil }
func (c *fakeConn) Close() error                          { return nil }
func (c *fakeConn) Begin() (driver.Tx, error)             { return &fakeTx{c.db}, nil }

type fakeDriver struct{ db *fakeDB }

func (d *fakeDriver) Open(string) (driver.Conn, error) { return &fakeConn{d.db}, nil }

// openFake 打开 fake 数据库连接（驱动名用进程内自增计数保证唯一）。
var fakeSeq int64

func openFake(t interface{ Fatalf(string, ...any) }) (*sql.DB, *fakeDB) {
	fdb := &fakeDB{}
	seq := atomic.AddInt64(&fakeSeq, 1)
	name := fmt.Sprintf("sgcc-fake-%d-%d", time.Now().UnixNano(), seq)
	sql.Register(name, &fakeDriver{fdb})
	db, err := sql.Open(name, "")
	if err != nil {
		t.Fatalf("open fake: %v", err)
	}
	return db, fdb
}

// ---- 路由测试脚手架 ----

type stubAuth struct{ ident auth.Identity }

func (s stubAuth) Login(context.Context, string, string, string) (auth.LoginResult, error) {
	return auth.LoginResult{}, nil
}
func (s stubAuth) Authenticate(_ context.Context, token string) (auth.Identity, error) {
	if token == "t" {
		return s.ident, nil
	}
	return auth.Identity{}, sql.ErrNoRows
}
func (s stubAuth) Logout(context.Context, string, time.Time) error { return nil }

// newTestRouter 构建带身份的测试路由（Authorization: Bearer t）。
func newTestRouter(m *Module, ident auth.Identity) http.Handler {
	r := chi.NewRouter()
	r.Route("/api/v1", func(r chi.Router) {
		m.RegisterRoutes(r)
	})
	return httpapi.AuthMiddleware(r, stubAuth{ident: ident})
}
