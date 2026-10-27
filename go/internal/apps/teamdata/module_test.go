package teamdata

// 模块装配、鉴权/应用权限/班组隔离与管理门控测试（走 httpapi.NewRouter 全链路）。

import (
	"bytes"
	"context"
	"database/sql/driver"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"shade/internal/auth"
	"shade/internal/httpapi"
)

// fakeAuthenticator 按固定令牌返回指定身份（不接真实 JWT/数据库）。
type fakeAuthenticator struct{ identity auth.Identity }

func (f fakeAuthenticator) Login(context.Context, string, string, string) (auth.LoginResult, error) {
	return auth.LoginResult{Token: "test-token", User: f.identity}, nil
}

func (f fakeAuthenticator) Authenticate(_ context.Context, token string) (auth.Identity, error) {
	if token != "test-token" {
		return auth.Identity{}, auth.ErrUnauthorized
	}
	return f.identity, nil
}

func (f fakeAuthenticator) Logout(context.Context, string, time.Time) error { return nil }

// newTestEnv 组装 fake 环境：fake DB + 模块（team-data 权限放行）+ 全链路路由。
// 默认生效班组查询回填 (2, "检修二班")。
func newTestEnv(t *testing.T, identity auth.Identity) (*fakeDB, *httptest.Server) {
	t.Helper()
	sqlDB, fake := openFake(t)
	mod := New(sqlDB, nil)
	mod.Permit = fakePermit{allow: map[string]bool{"team-data": true}}
	fake.on("FROM sys_team WHERE id = ?", []string{"id", "name"},
		[][]driver.Value{{int64(2), "检修二班"}})
	srv := httptest.NewServer(httpapi.NewRouter(
		httpapi.Deps{Auth: fakeAuthenticator{identity: identity}}, mod))
	t.Cleanup(func() {
		srv.Close()
		_ = sqlDB.Close()
	})
	return fake, srv
}

// do 发起带令牌的 JSON 请求并解码信封。
func do(t *testing.T, srv *httptest.Server, method, path string, body any) (int, map[string]any) {
	t.Helper()
	var reqBody *bytes.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("编码请求体失败: %v", err)
		}
		reqBody = bytes.NewReader(raw)
	} else {
		reqBody = bytes.NewReader(nil)
	}
	req, err := http.NewRequest(method, srv.URL+path, reqBody)
	if err != nil {
		t.Fatalf("构造请求失败: %v", err)
	}
	req.Header.Set("Authorization", "Bearer test-token")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("请求失败: %v", err)
	}
	defer resp.Body.Close()
	var env map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&env); err != nil {
		t.Fatalf("解码响应失败: %v", err)
	}
	return resp.StatusCode, env
}

// newRouterWith 以管理员身份装配全链路路由（供测试注入钩子后复用）。
func newRouterWith(mod *Module) http.Handler {
	return httpapi.NewRouter(httpapi.Deps{
		Auth: fakeAuthenticator{identity: auth.Identity{ID: 1, Role: "admin", TeamID: 2}},
	}, mod)
}

// codeOf 取信封业务码（JSON 数值 → float64）。
func codeOf(env map[string]any) float64 {
	c, _ := env["code"].(float64)
	return c
}

// 门控：普通用户全部 40304。
func TestGateRejectsPlainUser(t *testing.T) {
	_, srv := newTestEnv(t, auth.Identity{ID: 7, Role: "user", TeamID: 2})
	status, env := do(t, srv, http.MethodGet, "/api/v1/teamdata/vehicles", nil)
	if status != http.StatusForbidden {
		t.Fatalf("普通用户应 403，got %d %v", status, env)
	}
	if codeOf(env) != codeNotAdmin {
		t.Fatalf("业务码应 %d，got %v", codeNotAdmin, env["code"])
	}
}

// 门控：班管仅本班（?team_id= 指定他班也收敛回本班，不越权）。
func TestTeamAdminCannotCrossTeam(t *testing.T) {
	fake, srv := newTestEnv(t, auth.Identity{ID: 7, Role: "team_admin", TeamID: 2})
	fake.on("SELECT COALESCE(MAX(sort), 0) FROM worklog_vehicle", []string{"maxSort"},
		[][]driver.Value{{int64(0)}})
	fake.onExec("INSERT INTO worklog_vehicle", 1, 11)
	status, body := do(t, srv, http.MethodPost, "/api/v1/teamdata/vehicles?team_id=3",
		map[string]any{"name": "京A12345"})
	if status != http.StatusOK {
		t.Fatalf("班管本班操作应 200，got %d %v", status, body)
	}
	q, ok := fake.lastQuery("FROM sys_team WHERE id = ?")
	if !ok {
		t.Fatalf("应有班组查询")
	}
	if len(q.args) == 0 || q.args[0] != int64(2) {
		t.Fatalf("班管生效班组应固定本班 2，got %v", q.args)
	}
}

// 超管可指定班组。
func TestAdminSelectsTeam(t *testing.T) {
	fake, srv := newTestEnv(t, auth.Identity{ID: 1, Role: "admin", TeamID: 2})
	status, body := do(t, srv, http.MethodGet, "/api/v1/teamdata/vehicles?team_id=5", nil)
	if status != http.StatusOK {
		t.Fatalf("超管指定班组应 200，got %d %v", status, body)
	}
	q, ok := fake.lastQuery("FROM sys_team WHERE id = ?")
	if !ok {
		t.Fatalf("应有班组查询")
	}
	if len(q.args) == 0 || q.args[0] != int64(5) {
		t.Fatalf("超管生效班组应 5，got %v", q.args)
	}
}

// 应用权限缺失 → 40301。
func TestPermitDenied(t *testing.T) {
	sqlDB, _ := openFake(t)
	defer sqlDB.Close()
	mod := New(sqlDB, nil)
	mod.Permit = fakePermit{allow: map[string]bool{"team-data": false}}
	srv := httptest.NewServer(httpapi.NewRouter(
		httpapi.Deps{Auth: fakeAuthenticator{identity: auth.Identity{ID: 1, Role: "admin"}}}, mod))
	defer srv.Close()
	status, body := do(t, srv, http.MethodGet, "/api/v1/teamdata/vehicles", nil)
	if status != http.StatusForbidden {
		t.Fatalf("无应用权限应 403，got %d %v", status, body)
	}
	if codeOf(body) != codeNoApp {
		t.Fatalf("业务码应 %d，got %v", codeNoApp, body["code"])
	}
}

// 无班组：列表空态、写操作 40010（超管亦无默认班组时按空态处理）。
func TestNoTeamBehaviors(t *testing.T) {
	sqlDB, fake := openFake(t)
	defer sqlDB.Close()
	fake.on("FROM sys_team WHERE id = ?", []string{"id", "name"}, nil) // 查无班组
	mod := New(sqlDB, nil)
	srv := httptest.NewServer(httpapi.NewRouter(
		httpapi.Deps{Auth: fakeAuthenticator{identity: auth.Identity{ID: 3, Role: "admin", TeamID: 9}}}, mod))
	defer srv.Close()

	status, body := do(t, srv, http.MethodGet, "/api/v1/teamdata/vehicles", nil)
	if status != http.StatusOK {
		t.Fatalf("无班组列表应 200 空态，got %d %v", status, body)
	}
	data, _ := body["data"].(map[string]any)
	if list, ok := data["list"].([]any); !ok || len(list) != 0 {
		t.Fatalf("无班组列表应为空，got %v", body)
	}

	status, body = do(t, srv, http.MethodPost, "/api/v1/teamdata/vehicles", map[string]any{"name": "x"})
	if status != http.StatusBadRequest {
		t.Fatalf("无班组写操作应 400，got %d %v", status, body)
	}
	if codeOf(body) != codeNoTeam {
		t.Fatalf("业务码应 %d，got %v", codeNoTeam, body["code"])
	}
}

// EnsureSchema 幂等建表 team_line。
func TestEnsureSchema(t *testing.T) {
	sqlDB, fake := openFake(t)
	defer sqlDB.Close()
	mod := New(sqlDB, nil)
	if err := mod.EnsureSchema(context.Background()); err != nil {
		t.Fatalf("EnsureSchema 失败: %v", err)
	}
	if fake.execCount("CREATE TABLE IF NOT EXISTS team_line") != 1 {
		t.Fatalf("应执行 team_line 建表，execs=%v", fake.allExecs())
	}
}
