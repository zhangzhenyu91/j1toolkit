package crossing

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"shade/internal/auth"
	"shade/internal/httpapi"
)

// ---- database/sql 假驱动（纯标准库；不连真实库，不引 go-sqlmock）----

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

// fakeCall 一次执行记录。
type fakeCall struct {
	query string
	args  []driver.Value
}

// fakeRoute 按子串匹配的脚本响应队列；队列耗尽后 query 返回空集、exec 返回 affected=1。
type fakeRoute struct {
	match  string
	queues []func() (driver.Rows, driver.Result, error)
	always bool // true = 可重复命中（不弹出队列）
}

type fakeDB struct {
	mu    sync.Mutex
	calls []fakeCall
	rout  []fakeRoute
}

// onRows 注册一次 query 响应（可多次调用排队；cols 为列名，rows 为行值）。
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

// onRowsAlways 注册一个可重复命中的 query 响应（每次调用都返回同样的行）。
func (d *fakeDB) onRowsAlways(match string, cols []string, rows ...[]driver.Value) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.rout = append(d.rout, fakeRoute{match: match, always: true, queues: []func() (driver.Rows, driver.Result, error){
		func() (driver.Rows, driver.Result, error) {
			return &fakeRows{cols: cols, data: append([][]driver.Value(nil), rows...)}, nil, nil
		},
	}})
}

// onExec 注册一次 exec 响应（LastInsertId=id，affected=1）。
func (d *fakeDB) onExec(match string, id int64) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.rout = append(d.rout, fakeRoute{match: match, queues: []func() (driver.Rows, driver.Result, error){
		func() (driver.Rows, driver.Result, error) { return nil, fakeResult(id), nil },
	}})
}

// onErr 注册一次错误响应（query/exec 通用）。
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
		if len(route.queues) > 0 {
			fn := route.queues[0]
			if !route.always {
				route.queues = route.queues[1:]
			}
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

// callsWith 返回匹配子串的调用记录。
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
	db    *fakeDB
	query string
}

func (s *fakeStmt) Close() error  { return nil }
func (s *fakeStmt) NumInput() int { return -1 }

func (s *fakeStmt) Exec(args []driver.Value) (driver.Result, error) {
	_, res, err := s.db.dispatch(s.query, args, true)
	return res, err
}

func (s *fakeStmt) Query(args []driver.Value) (driver.Rows, error) {
	rows, _, err := s.db.dispatch(s.query, args, false)
	return rows, err
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

var fakeSeq int64

// openFake 注册并打开一个假数据库连接（每个测试独立）。
func openFake(t *testing.T) (*sql.DB, *fakeDB) {
	t.Helper()
	fdb := &fakeDB{}
	name := fmt.Sprintf("crossing-fake-%d", atomic.AddInt64(&fakeSeq, 1))
	sql.Register(name, &fakeDriver{db: fdb})
	sdb, err := sql.Open(name, "")
	if err != nil {
		t.Fatalf("打开假库失败: %v", err)
	}
	sdb.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = sdb.Close() })
	return sdb, fdb
}

// ---- 测试用假配置 / 假通知 / 假鉴权 ----

type fakeCfg struct{ kv map[string]string }

func (f fakeCfg) Get(_ context.Context, key string) (string, error) {
	if v, ok := f.kv[key]; ok {
		return v, nil
	}
	return "", sql.ErrNoRows
}

type fakeNotifier struct {
	mu     sync.Mutex
	pushes []string
}

func (n *fakeNotifier) Push(_ context.Context, targets []string, title, content string) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.pushes = append(n.pushes, title+"|"+strings.Join(targets, ",")+"|"+content)
	return nil
}

type stubAuth struct{ ident auth.Identity }

func (s stubAuth) Login(context.Context, string, string, string) (auth.LoginResult, error) {
	return auth.LoginResult{Token: "t", User: s.ident}, nil
}

func (s stubAuth) Authenticate(_ context.Context, token string) (auth.Identity, error) {
	if token == "t" {
		return s.ident, nil
	}
	return auth.Identity{}, errors.New("bad token")
}

func (s stubAuth) Logout(context.Context, string, time.Time) error { return nil }

// newTestRouter 构造带鉴权的路由（身份走 stubAuth 的 Bearer t）。
func newTestRouter(m *Module, ident auth.Identity) http.Handler {
	r := chi.NewRouter()
	m.RegisterRoutes(r)
	return httpapi.AuthMiddleware(r, stubAuth{ident: ident})
}

// newRawRouter 构造不带 JWT 鉴权的路由（直推 /ingest 自鉴权专用）。
func newRawRouter(m *Module) http.Handler {
	r := chi.NewRouter()
	m.RegisterRoutes(r)
	return r
}

// doJSON 发送带 Bearer 的 JSON 请求。
func doJSON(t *testing.T, h http.Handler, method, path, token, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// decodeEnvelope 解出 {code,message,data} 信封。
func decodeEnvelope(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("响应不是 JSON: %v（body=%s）", err, rec.Body.String())
	}
	return out
}
