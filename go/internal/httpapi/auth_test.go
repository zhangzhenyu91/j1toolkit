package httpapi

// 鉴权回归测试：公开接口免登录语义不变、非公开接口不得匿名、
// netdisk preview/sign 不放行、token query 仅文件下载路径有效、app/list 权限过滤。

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"shade/internal/auth"
)

// fakeAuth 测试用认证器：仅接受 good-token。
type fakeAuth struct {
	ident    auth.Identity
	gotToken string
	logins   int
}

func (f *fakeAuth) Login(_ context.Context, _, _, _ string) (auth.LoginResult, error) {
	f.logins++
	return auth.LoginResult{Token: "good-token", User: f.ident}, nil
}

func (f *fakeAuth) Authenticate(_ context.Context, token string) (auth.Identity, error) {
	f.gotToken = token
	if token != "good-token" {
		return auth.Identity{}, errors.New("unauthorized")
	}
	return f.ident, nil
}

func (f *fakeAuth) Logout(_ context.Context, _ string, _ time.Time) error { return nil }

// echoModule 在 /api/v1 之下挂回显 handler（记录是否到达与拿到的身份）。
type echoModule struct {
	method  string
	pattern string
	hit     bool
	gotID   int64
	hasID   bool
}

func (m *echoModule) RegisterRoutes(r chi.Router) {
	method, pattern := m.method, m.pattern
	if method == "" {
		method = http.MethodGet
	}
	r.Method(method, pattern, http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		m.hit = true
		if ident, ok := IdentityFromContext(req.Context()); ok {
			m.gotID, m.hasID = ident.ID, true
		}
		writeOK(w, map[string]any{"reached": true})
	}))
}

// doReqAuth 带 Authorization 头的请求辅助（fakeAuth 只认 good-token）。
func doReqAuth(t *testing.T, h http.Handler, method, target, body string) (int, resp) {
	t.Helper()
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, target, rd)
	req.Header.Set("Authorization", "Bearer good-token")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	var r resp
	if err := json.Unmarshal(rec.Body.Bytes(), &r); err != nil {
		t.Fatalf("响应不是 JSON 信封: %v, body=%s", err, rec.Body.String())
	}
	return rec.Code, r
}

// ---- 公开接口免登录语义回归（不能因收紧鉴权而失效） ----

func TestPublicPathsStayReachableWithoutToken(t *testing.T) {
	cases := []struct {
		pattern string
		path    string
	}{
		{"/treehazard/ingest", "/api/v1/treehazard/ingest"},
		{"/crossing/ingest", "/api/v1/crossing/ingest"},
		{"/callme/ingest-chat", "/api/v1/callme/ingest-chat"},
		{"/chatlog/ingest", "/api/v1/chatlog/ingest"},
		{"/safeday/callback", "/api/v1/safeday/callback"},
		{"/sgcc/callback", "/api/v1/sgcc/callback"},
		{"/share/{id}", "/api/v1/share/abc"},
		{"/office/raw/{ticket}", "/api/v1/office/raw/ticket1"},
		{"/office/callback/{ticket}", "/api/v1/office/callback/ticket1"},
		{"/netdisk/public/share/{id}", "/api/v1/netdisk/public/share/abc"},
		{"/netdisk/preview/{token}", "/api/v1/netdisk/preview/ticketjwt"},
		{"/safeday/records/{id}/download", "/api/v1/safeday/records/1/download"},
		{"/safeday/records/{id}/download/{name}", "/api/v1/safeday/records/1/download/name.pdf"},
	}
	fa := &fakeAuth{ident: auth.Identity{ID: 7, Role: "user"}}
	for _, c := range cases {
		m := &echoModule{pattern: c.pattern}
		h := NewRouter(Deps{Auth: fa}, m)
		status, r := doReq(t, h, http.MethodGet, c.path, "")
		if status != http.StatusOK || !m.hit {
			t.Fatalf("公开路径 %s 应免登录可达: status=%d hit=%v body=%+v", c.path, status, m.hit, r)
		}
	}
	// healthz 不经业务模块
	status, _ := doReq(t, NewRouter(Deps{Auth: fa}), http.MethodGet, "/healthz", "")
	if status != http.StatusOK {
		t.Fatalf("healthz 应免登录可达: %d", status)
	}
}

// ---- 非公开接口：不得匿名 ----

func TestNonPublicPathsRejectAnonymous(t *testing.T) {
	paths := []string{
		"/api/v1/app/list",
		"/api/v1/user/profile",
		"/api/v1/notice/list",
		"/api/v1/admin/teams",
		"/api/v1/admin/config",
		"/api/v1/app-logs",
		"/api/v1/netdisk/preview/sign",
		"/api/v1/worklog/logs",
		"/api/v1/wechat-answer/status",
		"/api/v1/wechatanswer/status",
	}
	fa := &fakeAuth{ident: auth.Identity{ID: 7, Role: "admin"}}
	for _, path := range paths {
		m := &echoModule{pattern: strings.TrimPrefix(path, "/api/v1")}
		h := NewRouter(Deps{Auth: fa}, m)
		status, r := doReq(t, h, http.MethodGet, path, "")
		if status != http.StatusUnauthorized {
			t.Fatalf("路径 %s 无 token 必须 401（不得匿名）: status=%d body=%+v", path, status, r)
		}
		if m.hit {
			t.Fatalf("路径 %s 无 token 不应到达业务 handler", path)
		}
	}
}

// ---- netdisk preview/sign 不放行（票据取流仍免登录） ----

func TestNetdiskPreviewSignNotExempt(t *testing.T) {
	if publicAuthPath("/api/v1/netdisk/preview/sign") {
		t.Fatal("/api/v1/netdisk/preview/sign 不得列入免登录白名单")
	}
	if !publicAuthPath("/api/v1/netdisk/preview/some-ticket") {
		t.Fatal("/api/v1/netdisk/preview/{ticket} 票据取流应保持免登录（票据即鉴权）")
	}
	fa := &fakeAuth{ident: auth.Identity{ID: 7, Role: "user"}}
	m := &echoModule{method: http.MethodPost, pattern: "/netdisk/preview/sign"}
	h := NewRouter(Deps{Auth: fa}, m)
	status, _ := doReq(t, h, http.MethodPost, "/api/v1/netdisk/preview/sign", "")
	if status != http.StatusUnauthorized {
		t.Fatalf("preview/sign 无 token 应 401，got %d", status)
	}
}

// ---- token query 仅文件下载路径有效 ----

func TestTokenQueryOnlyOnFileDownloadPaths(t *testing.T) {
	// 下载路径：无 Authorization 头时 ?token= 兜底生效
	fa := &fakeAuth{ident: auth.Identity{ID: 9, Role: "user"}}
	dl := &echoModule{pattern: "/worklog/photos/{id}/download"}
	h := NewRouter(Deps{Auth: fa}, dl)
	status, r := doReq(t, h, http.MethodGet, "/api/v1/worklog/photos/1/download?token=good-token", "")
	assertOK(t, status, r)
	if !dl.hit || !dl.hasID || dl.gotID != 9 {
		t.Fatalf("下载路径 ?token= 应注入身份: hit=%v id=%d", dl.hit, dl.gotID)
	}

	// 非下载路径：?token= 不生效，仍 401
	fa2 := &fakeAuth{ident: auth.Identity{ID: 9, Role: "user"}}
	h2 := NewRouter(Deps{Auth: fa2}, &echoModule{pattern: "/app/list"})
	status, _ = doReq(t, h2, http.MethodGet, "/api/v1/app/list?token=good-token", "")
	if status != http.StatusUnauthorized {
		t.Fatalf("app/list 不得接受 query token，got %d", status)
	}

	// 非 GET/HEAD：?token= 不生效
	fa3 := &fakeAuth{ident: auth.Identity{ID: 9, Role: "user"}}
	post := &echoModule{method: http.MethodPost, pattern: "/netdisk/preview/sign"}
	h3 := NewRouter(Deps{Auth: fa3}, post)
	status, _ = doReq(t, h3, http.MethodPost, "/api/v1/netdisk/preview/sign?token=good-token", "")
	if status != http.StatusUnauthorized {
		t.Fatalf("POST 不得接受 query token，got %d", status)
	}

	// 有 Authorization 头时以头为准（不回落 query）
	fa4 := &fakeAuth{ident: auth.Identity{ID: 9, Role: "user"}}
	h4 := NewRouter(Deps{Auth: fa4}, &echoModule{pattern: "/worklog/photos/{id}/download"})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/worklog/photos/1/download?token=good-token", nil)
	req.Header.Set("Authorization", "Bearer other")
	h4.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("Authorization 头优先且无效时应 401，got %d", rec.Code)
	}
	if fa4.gotToken != "other" {
		t.Fatalf("应以 Authorization 头为准，got token=%q", fa4.gotToken)
	}
}

// ---- 管理路径超管门控（含 /wechat-answer 统一入口） ----

func TestAdminPathsRequireSuperAdmin(t *testing.T) {
	adminPaths := []string{
		"/api/v1/admin/teams",
		"/api/v1/app-logs",
		"/api/v1/app-logs/health",
		"/api/v1/app-logs/detail",
		"/api/v1/app-logs/export",
		"/api/v1/app-logs/export/ticket",
		"/api/v1/app-logs/anything",
		"/api/v1/wechat-answer/status",
		"/api/v1/wechatanswer/status",
	}
	for _, path := range adminPaths {
		fa := &fakeAuth{ident: auth.Identity{ID: 7, Role: "user"}}
		m := &echoModule{pattern: strings.TrimPrefix(path, "/api/v1")}
		h := NewRouter(Deps{Auth: fa}, m)
		status, r := doReqAuth(t, h, http.MethodGet, path, "")
		if status != http.StatusForbidden {
			t.Fatalf("路径 %s 普通用户应 403: status=%d body=%+v", path, status, r)
		}
	}
}

func TestWechatAnswerAliasRewritesToModuleRoute(t *testing.T) {
	fa := &fakeAuth{ident: auth.Identity{ID: 7, Role: "admin"}}
	// 模块挂在 /wechatanswer/status（wechatanswer 模块口径）
	m := &echoModule{pattern: "/wechatanswer/status"}
	h := NewRouter(Deps{Auth: fa}, m)
	status, r := doReqAuth(t, h, http.MethodGet, "/api/v1/wechat-answer/status", "")
	assertOK(t, status, r)
	if !m.hit {
		t.Fatalf("管理入口 /api/v1/wechat-answer/status 应重写命中模块路由，body=%+v", r)
	}
}

// ---- app/list 权限过滤 ----

// fakeUserApps 实现 UserAppCatalog：按预置授权过滤，admin 追加管理应用。
type fakeUserApps struct {
	all    []App
	grants map[int64][]string
}

func (f *fakeUserApps) ListApps(context.Context) ([]App, error) { return f.all, nil }

func (f *fakeUserApps) ListAppsForUser(_ context.Context, userID int64, role string) ([]App, error) {
	grantSet := map[string]bool{}
	for _, code := range f.grants[userID] {
		grantSet[code] = true
	}
	out := make([]App, 0)
	for _, a := range f.all {
		if grantSet[a.Code] {
			out = append(out, a)
		}
	}
	if role == "admin" {
		for _, code := range []string{"wechat-answer", "app-log"} {
			if !grantSet[code] {
				out = append(out, App{Code: code, Name: code, Enabled: true})
			}
		}
	}
	return out, nil
}

func TestAppListFilteredByPermission(t *testing.T) {
	apps := &fakeUserApps{
		all: []App{
			{Code: "work-log", Name: "出工日志", Enabled: true},
			{Code: "call-me", Name: "Call Me", Enabled: true},
			{Code: "wechat-answer", Name: "微信智能应答", Enabled: true},
			{Code: "app-log", Name: "应用日志", Enabled: true},
		},
		grants: map[int64][]string{7: {"work-log"}, 8: {"work-log", "call-me"}},
	}

	// 普通用户：只回授权应用，不含管理应用
	fa := &fakeAuth{ident: auth.Identity{ID: 7, Role: "user"}}
	h := NewRouter(Deps{Apps: apps, Auth: fa})
	status, r := doReqAuth(t, h, http.MethodGet, "/api/v1/app/list", "")
	assertOK(t, status, r)
	var data struct {
		Apps []App `json:"apps"`
	}
	if err := json.Unmarshal(r.Data, &data); err != nil {
		t.Fatalf("data 解码失败: %v", err)
	}
	if len(data.Apps) != 1 || data.Apps[0].Code != "work-log" {
		t.Fatalf("普通用户应用列表不符: %+v", data.Apps)
	}

	// admin：授权应用 + wechat-answer / app-log
	fa2 := &fakeAuth{ident: auth.Identity{ID: 8, Role: "admin"}}
	h2 := NewRouter(Deps{Apps: apps, Auth: fa2})
	status, r = doReqAuth(t, h2, http.MethodGet, "/api/v1/app/list", "")
	assertOK(t, status, r)
	if err := json.Unmarshal(r.Data, &data); err != nil {
		t.Fatalf("data 解码失败: %v", err)
	}
	codes := map[string]bool{}
	for _, a := range data.Apps {
		codes[a.Code] = true
	}
	if !codes["work-log"] || !codes["call-me"] || !codes["wechat-answer"] || !codes["app-log"] {
		t.Fatalf("admin 应用列表应含授权 + 管理应用: %+v", data.Apps)
	}
	if len(data.Apps) != 4 {
		t.Fatalf("admin 应用列表条数不符: %+v", data.Apps)
	}
}

// ---- user/profile 与 admin/teams 形状 ----

type fakeAccounts struct {
	profile UserProfile
	teams   []TeamItem
}

func (f *fakeAccounts) Profile(context.Context, int64) (UserProfile, error) { return f.profile, nil }
func (f *fakeAccounts) Teams(context.Context) ([]TeamItem, error)           { return f.teams, nil }

func TestUserProfileAndAdminTeams(t *testing.T) {
	fa := &fakeAuth{ident: auth.Identity{ID: 7, Role: "admin"}}
	acc := &fakeAccounts{
		profile: UserProfile{ID: 7, Username: "u", Nickname: "昵称", Team: "检修一班", TeamID: 1, Role: "admin", WXBound: true, CreatedAt: time.Now()},
		teams:   []TeamItem{{ID: 1, Name: "检修一班"}, {ID: 2, Name: "运维二班"}},
	}
	h := NewRouter(Deps{Auth: fa, Accounts: acc})

	status, r := doReqAuth(t, h, http.MethodGet, "/api/v1/user/profile", "")
	assertOK(t, status, r)
	var p UserProfile
	if err := json.Unmarshal(r.Data, &p); err != nil {
		t.Fatalf("profile 解码失败: %v", err)
	}
	if p.ID != 7 || !p.WXBound || p.Team != "检修一班" {
		t.Fatalf("profile 不符: %+v", p)
	}

	status, r = doReqAuth(t, h, http.MethodGet, "/api/v1/admin/teams", "")
	assertOK(t, status, r)
	var teams struct {
		List []TeamItem `json:"list"`
	}
	if err := json.Unmarshal(r.Data, &teams); err != nil {
		t.Fatalf("teams 解码失败: %v", err)
	}
	if len(teams.List) != 2 || teams.List[0].ID != 1 {
		t.Fatalf("teams 不符: %+v", teams.List)
	}
}

// ---- notice/list 形状 ----

type fakeNotices struct {
	gotLimit int
	page     NoticePage
}

func (f *fakeNotices) ListNotices(_ context.Context, _ int64, _ string, limit int) (NoticePage, error) {
	f.gotLimit = limit
	return f.page, nil
}

func TestNoticeListShape(t *testing.T) {
	fa := &fakeAuth{ident: auth.Identity{ID: 7, Role: "user"}}
	n := &fakeNotices{page: NoticePage{
		Items:  []NoticeItem{{ID: 1, Title: "t", Content: "c", Targets: []string{"user"}, CreatedBy: "系统", CreatedAt: "2026-10-27 08:00:00", Read: 0}},
		Unread: 3,
	}}
	h := NewRouter(Deps{Auth: fa, Notices: n})
	status, r := doReqAuth(t, h, http.MethodGet, "/api/v1/notice/list?limit=10", "")
	assertOK(t, status, r)
	var page NoticePage
	if err := json.Unmarshal(r.Data, &page); err != nil {
		t.Fatalf("notice 解码失败: %v", err)
	}
	if page.Unread != 3 || len(page.Items) != 1 || n.gotLimit != 10 {
		t.Fatalf("notice 不符: %+v limit=%d", page, n.gotLimit)
	}
}
