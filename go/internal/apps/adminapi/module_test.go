package adminapi

// 模块装配与门控测试（走 httpapi.NewRouter 全链路）：路由不与 httpapi 已有
// GET /admin/teams、GET /notice/list 冲突，/admin/* 超管门控与通知推送自检超管。

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

// newTestEnv 组装 fake 环境：fake DB + adminapi 模块 + 全链路路由。
func newTestEnv(t *testing.T, identity auth.Identity) (*fakeDB, *Module, *httptest.Server) {
	t.Helper()
	sqlDB, fake := openFake(t)
	mod := New(sqlDB)
	srv := httptest.NewServer(httpapi.NewRouter(
		httpapi.Deps{Auth: fakeAuthenticator{identity: identity}}, mod))
	t.Cleanup(func() {
		srv.Close()
		_ = sqlDB.Close()
	})
	return fake, mod, srv
}

// adminIdent 超管测试身份。
func adminIdent() auth.Identity {
	return auth.Identity{ID: 1, Username: "root", Nickname: "超管", Role: "admin", TeamID: 1}
}

// userIdent 普通用户测试身份。
func userIdent() auth.Identity {
	return auth.Identity{ID: 2, Username: "alice", Nickname: "Alice", Role: "user", TeamID: 1}
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

// wantCode 断言业务码（成功 0；失败为数值码）。
func wantCode(t *testing.T, env map[string]any, code float64) {
	t.Helper()
	if got, ok := env["code"].(float64); !ok || got != code {
		t.Fatalf("code = %v, want %v (message=%v)", env["code"], code, env["message"])
	}
}

// wantMessage 断言提示文案。
func wantMessage(t *testing.T, env map[string]any, msg string) {
	t.Helper()
	if got, _ := env["message"].(string); got != msg {
		t.Fatalf("message = %q, want %q", got, msg)
	}
}

// TestRouteNoConflict 已有 httpapi 路由不受影响：GET /admin/teams 仍是窄列表（未注入
// Accounts → 503），GET /admin/teams/full 归本模块；GET /notice/list 仍归 httpapi。
func TestRouteNoConflict(t *testing.T) {
	fake, _, srv := newTestEnv(t, adminIdent())

	// 班组全量列表归 adminapi：预置 fixture 行
	fake.on("FROM sys_team t", []string{"id", "name", "wxid", "kvm_group_name", "sort", "status", "created_at", "user_count"},
		[][]driver.Value{{int64(1), "检修一班", "", "", 1, 1, "2026-10-27 09:00:00", int64(3)}})
	status, env := do(t, srv, "GET", "/api/v1/admin/teams/full", nil)
	if status != http.StatusOK {
		t.Fatalf("GET /admin/teams/full status = %d", status)
	}
	wantCode(t, env, 0)
	data := env["data"].(map[string]any)
	list := data["list"].([]any)
	if len(list) != 1 {
		t.Fatalf("full list len = %d, want 1", len(list))
	}
	row := list[0].(map[string]any)
	if row["name"] != "检修一班" || row["user_count"].(float64) != 3 {
		t.Fatalf("row = %v", row)
	}

	// httpapi 窄列表仍由 Deps.Accounts 处理（nil → dependency_unavailable）
	status, env = do(t, srv, "GET", "/api/v1/admin/teams", nil)
	if status != http.StatusServiceUnavailable {
		t.Fatalf("GET /admin/teams status = %d, want 503（httpapi 处理器仍占位）", status)
	}
	if env["code"] != "dependency_unavailable" {
		t.Fatalf("GET /admin/teams code = %v", env["code"])
	}

	// GET /notice/list 仍归 httpapi（Notices nil → 503）
	status, env = do(t, srv, "GET", "/api/v1/notice/list", nil)
	if status != http.StatusServiceUnavailable {
		t.Fatalf("GET /notice/list status = %d, want 503（httpapi 处理器仍占位）", status)
	}
}

// TestAdminGate /api/v1/admin/* 非超管 403；通知推送处理器自检超管（40304）。
func TestAdminGate(t *testing.T) {
	_, _, srv := newTestEnv(t, userIdent())

	status, env := do(t, srv, "GET", "/api/v1/admin/users", nil)
	if status != http.StatusForbidden {
		t.Fatalf("普通用户 GET /admin/users status = %d, want 403", status)
	}
	if env["code"] != "forbidden" {
		t.Fatalf("code = %v, want forbidden", env["code"])
	}

	// /api/v1/notice/* 仅需登录（Node 同口径），推送在处理器内自检超管
	status, env = do(t, srv, "POST", "/api/v1/notice/push",
		map[string]any{"title": "t", "content": "c", "targets": []string{"user"}})
	if status != http.StatusForbidden {
		t.Fatalf("普通用户 POST /notice/push status = %d, want 403", status)
	}
	wantCode(t, env, float64(codeNotAdmin))
}

// TestUnauthenticated 未登录 401（unauthorized）。
func TestUnauthenticated(t *testing.T) {
	sqlDB, _ := openFake(t)
	defer sqlDB.Close()
	srv := httptest.NewServer(httpapi.NewRouter(httpapi.Deps{}, New(sqlDB)))
	defer srv.Close()

	req, err := http.NewRequest("GET", srv.URL+"/api/v1/admin/users", nil)
	if err != nil {
		t.Fatalf("构造请求失败: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("请求失败: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", resp.StatusCode)
	}
}
