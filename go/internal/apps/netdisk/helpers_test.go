package netdisk

// 测试基础设施：database/sql 假驱动（纯标准库；不连真实库）+ 桩鉴权（httpapi.AuthMiddleware）。

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
	"testing"
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

type fakeDB struct {
	mu sync.Mutex
}

// dispatch 默认响应：sys_team 名称查询返回班组名（公共区口径），nd_share 返回一条分享行（分享路由用），
// 其余空集/affected=1。
func (d *fakeDB) dispatch(query string, isExec bool) (driver.Rows, driver.Result, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if strings.Contains(query, "SELECT name FROM sys_team") {
		return &fakeRows{cols: []string{"name"}, data: [][]driver.Value{{"检修一班"}}}, nil, nil
	}
	if strings.Contains(query, "FROM nd_share WHERE share_id") {
		return &fakeRows{cols: []string{"id", "share_id", "user_id", "creator", "base_path", "paths", "password", "expire_at", "status", "created_at"},
			data: [][]driver.Value{{
				int64(1), "abcd1234ef", int64(1), "阿狸", "/nd/my/alice",
				`["/docs","/file.txt"]`, "1234", nil, int64(1), time.Now(),
			}}}, nil, nil
	}
	if isExec {
		return nil, fakeResult(1), nil
	}
	return &fakeRows{cols: []string{"v"}}, nil, nil
}

type fakeStmt struct {
	db    *fakeDB
	query string
}

func (s *fakeStmt) Close() error  { return nil }
func (s *fakeStmt) NumInput() int { return -1 }

func (s *fakeStmt) Exec([]driver.Value) (driver.Result, error) {
	_, res, err := s.db.dispatch(s.query, true)
	return res, err
}

func (s *fakeStmt) Query([]driver.Value) (driver.Rows, error) {
	rows, _, err := s.db.dispatch(s.query, false)
	return rows, err
}

type fakeTx struct{}

func (t *fakeTx) Commit() error   { return nil }
func (t *fakeTx) Rollback() error { return nil }

type fakeConn struct{ db *fakeDB }

func (c *fakeConn) Prepare(q string) (driver.Stmt, error) {
	return &fakeStmt{db: c.db, query: q}, nil
}
func (c *fakeConn) Close() error              { return nil }
func (c *fakeConn) Begin() (driver.Tx, error) { return &fakeTx{}, nil }

type fakeDriver struct{ db *fakeDB }

func (d *fakeDriver) Open(string) (driver.Conn, error) { return &fakeConn{db: d.db}, nil }

var fakeSeq int64

func openFakeDB(t *testing.T) *sql.DB {
	t.Helper()
	name := fmt.Sprintf("netdisk-fake-%d", atomic.AddInt64(&fakeSeq, 1))
	sql.Register(name, &fakeDriver{db: &fakeDB{}})
	sdb, err := sql.Open(name, "")
	if err != nil {
		t.Fatalf("打开假库失败: %v", err)
	}
	sdb.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = sdb.Close() })
	return sdb
}

// stubAuth 桩鉴权：任何 token 都返回固定身份。
type stubAuth struct{ ident auth.Identity }

func (s stubAuth) Login(context.Context, string, string, string) (auth.LoginResult, error) {
	return auth.LoginResult{User: s.ident}, nil
}
func (s stubAuth) Authenticate(context.Context, string) (auth.Identity, error) { return s.ident, nil }
func (s stubAuth) Logout(context.Context, string, time.Time) error             { return nil }

// newTestRouter 构建测试路由：无 DB 时补假库；AuthMiddleware 注入桩身份。
func newTestRouter(t *testing.T, m *Module) http.Handler {
	t.Helper()
	if m.DB == nil {
		m.DB = openFakeDB(t)
	}
	r := chi.NewRouter()
	r.Route("/api/v1", func(r chi.Router) { m.RegisterRoutes(r) })
	ident := auth.Identity{ID: 1, Username: "alice", Nickname: "阿狸", Role: "admin", TeamID: 5}
	m.Auth = stubAuth{ident: ident}
	return httpapi.AuthMiddleware(r, stubAuth{ident: ident})
}

// withFakeAuth 保持测试可读（身份由 stubAuth 注入，无需改写请求）。
func withFakeAuth(req *http.Request) *http.Request { return req }
