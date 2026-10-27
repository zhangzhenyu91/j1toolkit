// 兼容性库存测试（Go/Vue 重写最终装配审计产物，只读审计不改业务代码）。
//
// 对照 server/src 的 238 条 Node 路由（teamdata/worklog admin 的 dictRoutes ${path}
// 已展开为 vehicles/destinations/members）逐一验证：
//   - 全模块装配无路由注册冲突（chi 同模式重复注册会 panic）；
//   - Node 端点在 Go 路由上确实注册（区分 chi 404 与业务层 404）；
//   - 公开接口/受保护接口的统一鉴权门控与 direct token fail-closed；
//   - app/list、user/profile、notice/list、admin/teams 的响应形状（当前 Go/Vue 契约）；
//   - 旧客户端双契约字段与滑块页运行时 AppID 注入。
//
// 已知情况：审计过程中 internal/apps/worklog 曾出现 isDupEntry 重复声明
// （membersync.go / dict.go:593）导致 worklog/bridge/sgccclockin/integrations/cmd 构建失败，
// 审计上报后上游已移除重复声明；当前全部 251 条 Node 端点（worklog/sgcc 已补入）均在装配范围。
package httpapi_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	"shade/internal/apps/adminapi"
	"shade/internal/apps/authwx"
	"shade/internal/apps/callme"
	"shade/internal/apps/chatlog"
	"shade/internal/apps/crossing"
	"shade/internal/apps/kvm"
	"shade/internal/apps/netdisk"
	"shade/internal/apps/office"
	"shade/internal/apps/quiz"
	"shade/internal/apps/safeday"
	"shade/internal/apps/sgccclockin"
	"shade/internal/apps/teamdata"
	"shade/internal/apps/treehazard"
	"shade/internal/apps/wechatanswer"
	"shade/internal/apps/wmadd"
	"shade/internal/apps/worklog"
	"shade/internal/auth"
	"shade/internal/httpapi"
)

// ---- 测试替身（fake/httptest，不接真实云/MySQL/Redis） ----

type testAuth struct{ ident auth.Identity }

func (a testAuth) Login(context.Context, string, string, string) (auth.LoginResult, error) {
	return auth.LoginResult{Token: "good-token", ExpiresIn: 3600, User: a.ident}, nil
}

func (a testAuth) Authenticate(_ context.Context, token string) (auth.Identity, error) {
	if token == "good-token" {
		return a.ident, nil
	}
	return auth.Identity{}, errors.New("invalid token")
}

func (a testAuth) Logout(context.Context, string, time.Time) error { return nil }

type shapeApps struct{}

func (shapeApps) ListApps(context.Context) ([]httpapi.App, error) {
	return []httpapi.App{{Code: "work-log", AppKey: "work-log", Name: "出工日志", Description: "派车/巡视/打卡", Icon: "work", Path: "/worklog", Terminal: "both", Sort: 2, Enabled: true}}, nil
}

type shapeAccounts struct{}

func (shapeAccounts) Profile(context.Context, int64) (httpapi.UserProfile, error) {
	return httpapi.UserProfile{
		ID: 1, Username: "u1", Nickname: "昵称", Avatar: "",
		Team: "检修一班", TeamID: 7, Role: "user", WXBound: true,
		CreatedAt: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC),
	}, nil
}

func (shapeAccounts) Teams(context.Context) ([]httpapi.TeamItem, error) {
	return []httpapi.TeamItem{{ID: 1, Name: "检修一班", WXID: "group@chatroom", KVMGroupName: "检修一班", Sort: 1, Status: 1, UserCount: 9, CreatedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}}, nil
}

type shapeNotices struct{}

type legacyCfg struct{}

func (legacyCfg) Get(_ context.Context, key string) (string, error) {
	if key == "SGCC_DX_APPID" {
		return "fixture-app-id", nil
	}
	return "", errors.New("not found")
}

func (shapeNotices) ListNotices(context.Context, int64, string, int) (httpapi.NoticePage, error) {
	return httpapi.NoticePage{
		Items: []httpapi.NoticeItem{{
			ID: 1, Title: "标题", Content: "内容", Targets: []string{"user"},
			CreatedBy: "系统", CreatedAt: "2026-01-02 03:04:05", Read: 0,
		}},
		Unread: 3,
	}, nil
}

var (
	adminIdent = auth.Identity{ID: 1, Username: "admin", Nickname: "超管", Role: "admin", TeamID: 7}
	userIdent  = auth.Identity{ID: 2, Username: "u1", Nickname: "昵称", Role: "user", TeamID: 7}
)

// newCompatRouter 装配全部业务模块；chi 对重复模式注册会 panic，
// 一旦出现路由注册冲突本 helper 以 t.Fatalf 暴露。
func newCompatRouter(t *testing.T, d httpapi.Deps, ident auth.Identity) http.Handler {
	t.Helper()
	d.Auth = testAuth{ident: ident}
	var handler http.Handler
	func() {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("路由注册冲突或装配失败: %v", r)
			}
		}()
		handler = httpapi.NewRouter(d,
			authwx.New(nil, nil, nil),
			adminapi.New(nil),
			callme.New(nil, nil),
			chatlog.New(nil, nil),
			crossing.New(nil, nil),
			kvm.New(nil, nil),
			netdisk.New(nil),
			office.New(nil),
			quiz.New(nil),
			safeday.New(nil, nil),
			sgccclockin.New(nil),
			teamdata.New(nil, nil),
			treehazard.New(nil),
			wechatanswer.New(nil, nil),
			wmadd.New(nil),
			worklog.New(nil),
		)
	}()
	return handler
}

var paramSeg = regexp.MustCompile(`:\w+`)

func fillParams(path string) string { return paramSeg.ReplaceAllString(path, "1") }

func doReq(t *testing.T, h http.Handler, method, path, token string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w
}

// isRouterNotFound 判定 chi 路由未注册（chi 默认 404 纯文本），
// 与业务层 JSON 404（如票据不存在）区分。
func isRouterNotFound(w *httptest.ResponseRecorder) bool {
	return w.Code == http.StatusNotFound && strings.TrimSpace(w.Body.String()) == "404 page not found"
}

// isMiddlewareAuthFail 判定统一鉴权中间件的 01（code 字符串 "unauthorized"），
// 与登录域/直推域的业务 401（数字码 40111 / 40104 等）区分。
func isMiddlewareAuthFail(w *httptest.ResponseRecorder) bool {
	return w.Code == http.StatusUnauthorized && strings.Contains(w.Body.String(), `"unauthorized"`)
}

// ---- 1. 路由注册冲突 ----

func TestCompatNoRouteRegistrationConflict(t *testing.T) {
	// newCompatRouter 内部装配全部模块；chi 重复注册会 panic 并被转为 t.Fatalf。
	h := newCompatRouter(t, httpapi.Deps{Apps: shapeApps{}}, adminIdent)
	if h == nil {
		t.Fatal("路由未构建")
	}
}

// ---- 2. Node 端点存在性（251 条，含 worklog/sgcc） ----

// compatInventory Node 侧既有端点（来源 server/src，dictRoutes ${path} 已展开）。
// 断言在 Go 路由上注册（chi 404 即缺口）；业务层 404/400/500 不算缺口。
var compatInventory = [][2]string{
	{"GET", "/api/v1/admin/apps"}, {"GET", "/api/v1/admin/teams"}, {"POST", "/api/v1/admin/teams"},
	{"DELETE", "/api/v1/admin/teams/:id"}, {"PUT", "/api/v1/admin/teams/:id"}, {"GET", "/api/v1/admin/users"},
	{"POST", "/api/v1/admin/users"}, {"PUT", "/api/v1/admin/users/:id"}, {"GET", "/api/v1/admin/users/:id/apps"},
	{"PUT", "/api/v1/admin/users/:id/apps"}, {"GET", "/api/v1/app/list"}, {"POST", "/api/v1/auth/app-login"},
	{"POST", "/api/v1/auth/login"}, {"POST", "/api/v1/auth/logout"}, {"POST", "/api/v1/auth/wx-login"},
	{"GET", "/api/v1/callme/capabilities"}, {"POST", "/api/v1/callme/chat"}, {"POST", "/api/v1/callme/ingest-chat"},
	{"GET", "/api/v1/callme/sessions"}, {"POST", "/api/v1/callme/sessions"}, {"DELETE", "/api/v1/callme/sessions/:id"},
	{"GET", "/api/v1/callme/sessions/:id"}, {"PUT", "/api/v1/callme/sessions/:id"},
	{"GET", "/api/v1/callme/sessions/:id/attachments"}, {"POST", "/api/v1/callme/sessions/:id/attachments"},
	{"DELETE", "/api/v1/callme/sessions/:id/messages/:msgId"}, {"POST", "/api/v1/chatlog/ingest"},
	{"GET", "/api/v1/crossing/conflicts"}, {"POST", "/api/v1/crossing/conflicts/:id/resolve"},
	{"GET", "/api/v1/crossing/dashboard"}, {"GET", "/api/v1/crossing/export/preview"}, {"POST", "/api/v1/crossing/ingest"},
	{"GET", "/api/v1/crossing/records"}, {"DELETE", "/api/v1/crossing/records/:id"}, {"GET", "/api/v1/crossing/records/:id"},
	{"GET", "/api/v1/crossing/staging/batches"}, {"DELETE", "/api/v1/crossing/staging/batches/:id"},
	{"GET", "/api/v1/crossing/staging/batches/:id"}, {"POST", "/api/v1/crossing/staging/batches/:id/import"},
	{"PUT", "/api/v1/crossing/staging/records/:id"}, {"POST", "/api/v1/crossing/sync/push-now"},
	{"GET", "/api/v1/crossing/sync/status"}, {"POST", "/api/v1/crossing/weknora-sync/push-now"},
	{"GET", "/api/v1/kvm/devices"}, {"POST", "/api/v1/kvm/devices/:id/delete"}, {"GET", "/api/v1/kvm/devices/:id/download"},
	{"GET", "/api/v1/kvm/devices/:id/files"}, {"POST", "/api/v1/kvm/devices/:id/mount"},
	{"POST", "/api/v1/kvm/devices/:id/pull-from-netdisk"}, {"POST", "/api/v1/kvm/devices/:id/push"},
	{"POST", "/api/v1/kvm/devices/:id/save-to-netdisk"}, {"GET", "/api/v1/kvm/devices/:id/status"}, {"POST", "/api/v1/kvm/jump"},
	{"POST", "/api/v1/netdisk/archive/decompress"}, {"GET", "/api/v1/netdisk/archive/download"},
	{"POST", "/api/v1/netdisk/archive/list"}, {"POST", "/api/v1/netdisk/archive/meta"}, {"POST", "/api/v1/netdisk/copy"},
	{"GET", "/api/v1/netdisk/dirsize"}, {"GET", "/api/v1/netdisk/download"}, {"GET", "/api/v1/netdisk/download/dir"},
	{"POST", "/api/v1/netdisk/list"}, {"POST", "/api/v1/netdisk/mkdir"}, {"POST", "/api/v1/netdisk/move"},
	{"GET", "/api/v1/netdisk/preview/:token/:name"}, {"POST", "/api/v1/netdisk/preview/sign"},
	{"GET", "/api/v1/netdisk/public/share/:id"}, {"GET", "/api/v1/netdisk/public/share/:id/download"},
	{"POST", "/api/v1/netdisk/public/share/:id/list"}, {"POST", "/api/v1/netdisk/public/share/:id/verify"},
	{"POST", "/api/v1/netdisk/remove"}, {"POST", "/api/v1/netdisk/rename"}, {"POST", "/api/v1/netdisk/rename/batch"},
	{"POST", "/api/v1/netdisk/search"}, {"GET", "/api/v1/netdisk/shares"}, {"POST", "/api/v1/netdisk/shares"},
	{"DELETE", "/api/v1/netdisk/shares/:id"}, {"POST", "/api/v1/netdisk/shares/:id/disable"},
	{"POST", "/api/v1/netdisk/shares/:id/enable"}, {"GET", "/api/v1/netdisk/spaces"}, {"GET", "/api/v1/netdisk/thumb"},
	{"POST", "/api/v1/netdisk/upload/abort"}, {"PUT", "/api/v1/netdisk/upload/chunk"},
	{"POST", "/api/v1/netdisk/upload/complete"}, {"POST", "/api/v1/netdisk/upload/init"}, {"GET", "/api/v1/netdisk/upload/status"},
	{"DELETE", "/api/v1/notice/:id"}, {"POST", "/api/v1/notice/:id/read"}, {"POST", "/api/v1/notice/del-read"},
	{"GET", "/api/v1/notice/list"}, {"POST", "/api/v1/notice/push"}, {"POST", "/api/v1/notice/read-all"},
	{"POST", "/api/v1/office/callback/:ticket"}, {"POST", "/api/v1/office/config"},
	{"GET", "/api/v1/office/raw/:ticket/:name"}, {"GET", "/api/v1/office/status"},
	{"GET", "/api/v1/quiz/banks"}, {"POST", "/api/v1/quiz/banks"}, {"DELETE", "/api/v1/quiz/banks/:id"},
	{"PUT", "/api/v1/quiz/banks/:id"}, {"POST", "/api/v1/quiz/banks/:id/analyze-retry"},
	{"GET", "/api/v1/quiz/banks/:id/home"}, {"POST", "/api/v1/quiz/banks/:id/import"},
	{"DELETE", "/api/v1/quiz/banks/:id/join"}, {"POST", "/api/v1/quiz/banks/:id/join"},
	{"GET", "/api/v1/quiz/banks/:id/questions"}, {"POST", "/api/v1/quiz/banks/:id/questions"},
	{"GET", "/api/v1/quiz/banks/manage"}, {"GET", "/api/v1/quiz/banks/pool"}, {"GET", "/api/v1/quiz/banks/template"},
	{"DELETE", "/api/v1/quiz/favorites/:questionId"}, {"POST", "/api/v1/quiz/favorites/:questionId"},
	{"GET", "/api/v1/quiz/overview"}, {"PUT", "/api/v1/quiz/practice/analysis"}, {"POST", "/api/v1/quiz/practice/answer"},
	{"POST", "/api/v1/quiz/practice/progress"}, {"GET", "/api/v1/quiz/practice/questions"},
	{"POST", "/api/v1/quiz/practice/reset"}, {"DELETE", "/api/v1/quiz/questions/:id"}, {"PUT", "/api/v1/quiz/questions/:id"},
	{"GET", "/api/v1/quiz/wrongs"}, {"DELETE", "/api/v1/quiz/wrongs/:questionId"},
	{"POST", "/api/v1/safeday/callback"}, {"GET", "/api/v1/safeday/form-meta"}, {"POST", "/api/v1/safeday/generate"},
	{"GET", "/api/v1/safeday/records"}, {"DELETE", "/api/v1/safeday/records/:id"},
	{"GET", "/api/v1/safeday/records/:id/preview"}, {"POST", "/api/v1/safeday/records/:id/save-netdisk"},
	{"GET", "/api/v1/teamdata/destinations"}, {"POST", "/api/v1/teamdata/destinations"},
	{"DELETE", "/api/v1/teamdata/destinations/:id"}, {"PUT", "/api/v1/teamdata/destinations/:id"},
	{"GET", "/api/v1/teamdata/lines"}, {"GET", "/api/v1/teamdata/members"}, {"POST", "/api/v1/teamdata/members"},
	{"DELETE", "/api/v1/teamdata/members/:id"}, {"PUT", "/api/v1/teamdata/members/:id"},
	{"PUT", "/api/v1/teamdata/members/:id/move"}, {"GET", "/api/v1/teamdata/oplines"}, {"POST", "/api/v1/teamdata/oplines"},
	{"DELETE", "/api/v1/teamdata/oplines/:id"}, {"PUT", "/api/v1/teamdata/oplines/:id"},
	{"PUT", "/api/v1/teamdata/oplines/:id/move"}, {"POST", "/api/v1/teamdata/oplines/import"},
	{"GET", "/api/v1/teamdata/oplines/template"}, {"GET", "/api/v1/teamdata/towers"},
	{"POST", "/api/v1/teamdata/towers/import"}, {"GET", "/api/v1/teamdata/towers/template"},
	{"GET", "/api/v1/teamdata/vehicles"}, {"POST", "/api/v1/teamdata/vehicles"},
	{"DELETE", "/api/v1/teamdata/vehicles/:id"}, {"PUT", "/api/v1/teamdata/vehicles/:id"},
	{"GET", "/api/v1/treehazard/conflicts"}, {"POST", "/api/v1/treehazard/conflicts/:id/resolve"},
	{"GET", "/api/v1/treehazard/dashboard"}, {"GET", "/api/v1/treehazard/export/preview"}, {"POST", "/api/v1/treehazard/ingest"},
	{"GET", "/api/v1/treehazard/records"}, {"DELETE", "/api/v1/treehazard/records/:id"},
	{"GET", "/api/v1/treehazard/records/:id"}, {"POST", "/api/v1/treehazard/records/:id/handle"},
	{"POST", "/api/v1/treehazard/records/:id/photos"}, {"DELETE", "/api/v1/treehazard/records/:id/photos/:photoId"},
	{"POST", "/api/v1/treehazard/records/handle-batch"}, {"GET", "/api/v1/treehazard/settings"},
	{"PUT", "/api/v1/treehazard/settings"}, {"GET", "/api/v1/treehazard/staging/batches"},
	{"DELETE", "/api/v1/treehazard/staging/batches/:id"}, {"GET", "/api/v1/treehazard/staging/batches/:id"},
	{"POST", "/api/v1/treehazard/staging/batches/:id/import"}, {"PUT", "/api/v1/treehazard/staging/records/:id"},
	{"POST", "/api/v1/treehazard/sync/push-now"}, {"GET", "/api/v1/treehazard/sync/status"},
	{"POST", "/api/v1/treehazard/weknora-sync/push-now"},
	{"GET", "/api/v1/user/profile"}, {"GET", "/api/v1/wmadd/geo"}, {"POST", "/api/v1/wmadd/render"},
	{"GET", "/api/v1/wmadd/towers"}, {"GET", "/healthz"},
	{"DELETE", "/api/v1/sgcc/account"}, {"GET", "/api/v1/sgcc/account"}, {"PUT", "/api/v1/sgcc/account/device"},
	{"GET", "/api/v1/sgcc/accounts"}, {"PUT", "/api/v1/sgcc/accounts/:id/device"}, {"POST", "/api/v1/sgcc/accounts/bind"},
	{"POST", "/api/v1/sgcc/clockin"}, {"GET", "/api/v1/sgcc/day"}, {"GET", "/api/v1/sgcc/expired"},
	{"GET", "/api/v1/sgcc/fee"}, {"POST", "/api/v1/sgcc/fee"}, {"GET", "/api/v1/sgcc/geo"}, {"POST", "/api/v1/sgcc/login/bind"},
	{"POST", "/api/v1/sgcc/login/captcha"}, {"POST", "/api/v1/sgcc/login/sms"}, {"GET", "/api/v1/sgcc/op/status"},
	{"POST", "/api/v1/sgcc/photos/:id/resync"}, {"GET", "/api/v1/sgcc/sync/active"}, {"DELETE", "/api/v1/sgcc/sync/logs"},
	{"GET", "/api/v1/sgcc/sync/logs"}, {"GET", "/api/v1/sgcc/sync/progress"}, {"POST", "/api/v1/sgcc/sync/pull"},
	{"GET", "/api/v1/worklog/admin/destinations"}, {"POST", "/api/v1/worklog/admin/destinations"},
	{"DELETE", "/api/v1/worklog/admin/destinations/:id"}, {"PUT", "/api/v1/worklog/admin/destinations/:id"},
	{"GET", "/api/v1/worklog/admin/members"}, {"POST", "/api/v1/worklog/admin/members"},
	{"DELETE", "/api/v1/worklog/admin/members/:id"}, {"PUT", "/api/v1/worklog/admin/members/:id"},
	{"PUT", "/api/v1/worklog/admin/members/:id/move"},
	{"GET", "/api/v1/worklog/admin/vehicles"}, {"POST", "/api/v1/worklog/admin/vehicles"},
	{"DELETE", "/api/v1/worklog/admin/vehicles/:id"}, {"PUT", "/api/v1/worklog/admin/vehicles/:id"},
	{"GET", "/api/v1/worklog/day-status"}, {"GET", "/api/v1/worklog/dispatch-sheet/preview"},
	{"POST", "/api/v1/worklog/dispatch/align"}, {"POST", "/api/v1/worklog/dispatch/sync-now"},
	{"GET", "/api/v1/worklog/dispatch/sync-switch"}, {"PUT", "/api/v1/worklog/dispatch/sync-switch"},
	{"GET", "/api/v1/worklog/fee-sheet/preview"}, {"GET", "/api/v1/worklog/geo"},
	{"GET", "/api/v1/worklog/logs"}, {"POST", "/api/v1/worklog/logs"}, {"DELETE", "/api/v1/worklog/logs/:id"},
	{"PUT", "/api/v1/worklog/logs/:id"}, {"PUT", "/api/v1/worklog/logs/:id/members/:mid/check"},
	{"POST", "/api/v1/worklog/logs/:id/photos"}, {"GET", "/api/v1/worklog/logs/:id/remark-download"},
	{"POST", "/api/v1/worklog/logs/:id/remark-files"}, {"GET", "/api/v1/worklog/logs/:id/remark-preview"},
	{"POST", "/api/v1/worklog/logs/cross"}, {"PUT", "/api/v1/worklog/logs/cross/:id"}, {"GET", "/api/v1/worklog/logs/cross/meta"},
	{"GET", "/api/v1/worklog/meta"}, {"GET", "/api/v1/worklog/photos"},
	{"POST", "/api/v1/worklog/photos-pdf/save-netdisk"}, {"GET", "/api/v1/worklog/photos.pdf"},
	{"DELETE", "/api/v1/worklog/photos/:id"}, {"GET", "/api/v1/worklog/photos/:id/download"},
	{"PUT", "/api/v1/worklog/photos/:id/members"}, {"POST", "/api/v1/worklog/photos/:id/verify"},
	{"PUT", "/api/v1/worklog/photos/:id/wm"}, {"GET", "/api/v1/worklog/report"},
	{"POST", "/api/v1/worklog/sheet/save-netdisk"}, {"GET", "/api/v1/worklog/stats"},
	{"GET", "/api/v1/worklog/task-sheet/preview"}, {"GET", "/api/v1/worklog/towers"},
	{"POST", "/api/v1/worklog/towers/import"}, {"GET", "/api/v1/worklog/towers/template"},
	{"POST", "/api/v1/worklog/zip"},
}

func TestCompatEndpointInventory(t *testing.T) {
	h := newCompatRouter(t, httpapi.Deps{Apps: shapeApps{}}, adminIdent)
	for _, ep := range compatInventory {
		method, path := ep[0], ep[1]
		w := doReq(t, h, method, fillParams(path), "good-token")
		if isRouterNotFound(w) {
			t.Errorf("%s %s 未在 Go 路由注册（chi 404）", method, path)
			continue
		}
		if w.Code == http.StatusMethodNotAllowed {
			t.Errorf("%s %s 已注册但方法不匹配（405）", method, path)
		}
		if isMiddlewareAuthFail(w) {
			t.Errorf("%s %s 带有效令牌仍被统一鉴权拒绝", method, path)
		}
	}
}

// ---- 3. 鉴权门控（公开接口 / 保护接口 / 超管门控 / direct token） ----

func TestCompatAuthGating(t *testing.T) {
	h := newCompatRouter(t, httpapi.Deps{Apps: shapeApps{}}, userIdent)

	publicEndpoints := [][2]string{
		{"GET", "/healthz"},
		{"POST", "/api/v1/auth/login"},
		{"POST", "/api/v1/auth/wx-login"},
		{"POST", "/api/v1/auth/app-login"},
		{"POST", "/api/v1/safeday/callback"},
		{"POST", "/api/v1/treehazard/ingest"},
		{"POST", "/api/v1/crossing/ingest"},
		{"POST", "/api/v1/callme/ingest-chat"},
		{"POST", "/api/v1/chatlog/ingest"},
		{"GET", "/api/v1/netdisk/public/share/1"},
		{"POST", "/api/v1/netdisk/public/share/1/verify"},
		{"GET", "/api/v1/netdisk/preview/tok1"},
		{"GET", "/api/v1/office/raw/tok1"},
		{"POST", "/api/v1/office/callback/tok1"},
		{"GET", "/api/v1/safeday/records/1/download"},
	}
	for _, ep := range publicEndpoints {
		w := doReq(t, h, ep[0], ep[1], "")
		if isMiddlewareAuthFail(w) {
			t.Errorf("公开接口 %s %s 被统一鉴权拦截（应放行自鉴权）", ep[0], ep[1])
		}
	}

	protectedEndpoints := [][2]string{
		{"GET", "/api/v1/app/list"},
		{"GET", "/api/v1/user/profile"},
		{"GET", "/api/v1/notice/list"},
		{"POST", "/api/v1/notice/push"},
		{"POST", "/api/v1/auth/logout"},
		{"GET", "/api/v1/admin/users"},
		{"GET", "/api/v1/quiz/banks"},
		{"GET", "/api/v1/treehazard/records"},
		{"GET", "/api/v1/crossing/records"},
		{"GET", "/api/v1/netdisk/spaces"},
		{"GET", "/api/v1/teamdata/lines"},
		{"GET", "/api/v1/kvm/devices"},
		{"GET", "/api/v1/callme/sessions"},
		{"GET", "/api/v1/safeday/records"},
		{"GET", "/api/v1/wmadd/geo"},
		{"GET", "/api/v1/netdisk/preview/sign"},
	}
	for _, ep := range protectedEndpoints {
		w := doReq(t, h, ep[0], ep[1], "")
		if !isMiddlewareAuthFail(w) {
			t.Errorf("受保护接口 %s %s 未带令牌未被统一鉴权拒绝（got %d）", ep[0], ep[1], w.Code)
		}
	}

	// 超管门控：普通用户 403，放行到业务层（非 401/403）。
	for _, ep := range [][2]string{
		{"GET", "/api/v1/admin/users"},
		{"GET", "/api/v1/wechat-answer/status"},
		{"GET", "/api/v1/wechatanswer/status"},
	} {
		w := doReq(t, h, ep[0], ep[1], "good-token")
		if w.Code != http.StatusForbidden {
			t.Errorf("普通用户访问管理路径 %s %s 应 403，got %d", ep[0], ep[1], w.Code)
		}
	}
	hAdmin := newCompatRouter(t, httpapi.Deps{Apps: shapeApps{}}, adminIdent)
	for _, ep := range [][2]string{
		{"GET", "/api/v1/wechat-answer/status"},
	} {
		w := doReq(t, hAdmin, ep[0], ep[1], "good-token")
		if w.Code == http.StatusForbidden || isMiddlewareAuthFail(w) {
			t.Errorf("超管访问 %s %s 不应被门控拦截，got %d", ep[0], ep[1], w.Code)
		}
	}

	// direct token fail-closed：未配置密钥（nil 配置源）时直推接口必须 503，不得免密放行。
	for _, ep := range [][2]string{
		{"POST", "/api/v1/chatlog/ingest"},
		{"POST", "/api/v1/callme/ingest-chat"},
	} {
		w := doReq(t, h, ep[0], ep[1], "")
		if w.Code != http.StatusServiceUnavailable {
			t.Errorf("direct token 未配置时 %s %s 应 503 拒绝，got %d", ep[0], ep[1], w.Code)
		}
	}
}

// ---- 4. 响应形状（当前 Go/Vue 契约；与 legacy 客户端差异见审计报告） ----

func TestCompatResponseShapes(t *testing.T) {
	h := newCompatRouter(t, httpapi.Deps{
		Apps: shapeApps{}, Accounts: shapeAccounts{}, Notices: shapeNotices{},
	}, userIdent)

	decode := func(t *testing.T, w *httptest.ResponseRecorder) map[string]any {
		t.Helper()
		var env struct {
			Code int            `json:"code"`
			Data map[string]any `json:"data"`
		}
		if err := jsonUnmarshal(w.Body.Bytes(), &env); err != nil {
			t.Fatalf("响应不是 JSON 信封: %v (%s)", err, w.Body.String())
		}
		if env.Code != 0 {
			t.Fatalf("业务 code 非 0: %s", w.Body.String())
		}
		return env.Data
	}

	// GET /app/list → data.apps（Vue）+ data.list（小程序/旧网页），字段双契约兼容。
	w := doReq(t, h, "GET", "/api/v1/app/list", "good-token")
	if w.Code != 200 {
		t.Fatalf("/app/list got %d", w.Code)
	}
	data := decode(t, w)
	if _, ok := data["apps"]; !ok {
		t.Errorf("/app/list data 缺少 apps 键: %v", data)
	}
	legacyApps, ok := data["list"].([]any)
	if !ok || len(legacyApps) == 0 {
		t.Fatalf("/app/list data.list 形状不对: %v", data)
	}
	legacyApp, _ := legacyApps[0].(map[string]any)
	for _, key := range []string{"app_key", "name", "icon", "path", "terminal", "sort"} {
		if _, ok := legacyApp[key]; !ok {
			t.Errorf("/app/list legacy 条目缺少 %s: %v", key, legacyApp)
		}
	}

	// GET /user/profile → 平铺资料（无 user 包装）。
	w = doReq(t, h, "GET", "/api/v1/user/profile", "good-token")
	if w.Code != 200 {
		t.Fatalf("/user/profile got %d", w.Code)
	}
	data = decode(t, w)
	for _, key := range []string{"id", "username", "nickname", "avatar", "team", "team_id", "role", "wx_bound", "created_at"} {
		if _, ok := data[key]; !ok {
			t.Errorf("/user/profile data 缺少 %s 键: %v", key, data)
		}
	}

	// GET /notice/list → data.items + data.unread，条目 createdByName/createdAt/read。
	w = doReq(t, h, "GET", "/api/v1/notice/list", "good-token")
	if w.Code != 200 {
		t.Fatalf("/notice/list got %d", w.Code)
	}
	data = decode(t, w)
	if _, ok := data["unread"]; !ok {
		t.Errorf("/notice/list data 缺少 unread 键")
	}
	items, ok := data["items"].([]any)
	if !ok || len(items) == 0 {
		t.Fatalf("/notice/list data.items 形状不对: %v", data)
	}
	item, _ := items[0].(map[string]any)
	for _, key := range []string{"id", "title", "content", "targets", "createdByName", "createdAt", "read"} {
		if _, ok := item[key]; !ok {
			t.Errorf("/notice/list item 缺少 %s 键: %v", key, item)
		}
	}

	// GET /admin/teams → data.list 保留完整管理字段，供小程序和旧网页继续使用。
	// 管理路径需超管身份（isAdminPath 门控），换 admin 身份的路由实例。
	hAdmin := newCompatRouter(t, httpapi.Deps{
		Apps: shapeApps{}, Accounts: shapeAccounts{}, Notices: shapeNotices{},
	}, adminIdent)
	w = doReq(t, hAdmin, "GET", "/api/v1/admin/teams", "good-token")
	if w.Code != 200 {
		t.Fatalf("/admin/teams got %d", w.Code)
	}
	data = decode(t, w)
	if _, ok := data["list"]; !ok {
		t.Errorf("/admin/teams data 缺少 list 键: %v", data)
	}
	teams, _ := data["list"].([]any)
	team, _ := teams[0].(map[string]any)
	for _, key := range []string{"id", "name", "wxid", "kvm_group_name", "sort", "status", "created_at", "user_count"} {
		if _, ok := team[key]; !ok {
			t.Errorf("/admin/teams 条目缺少 %s: %v", key, team)
		}
	}
}

// ---- 5. 旧客户端补充接口与滑块页 ----

func TestCompatLegacyEndpointsAndCaptcha(t *testing.T) {
	h := newCompatRouter(t, httpapi.Deps{Apps: shapeApps{}}, adminIdent)
	for _, ep := range [][2]string{{"POST", "/api/v1/notice/upload-image"}, {"GET", "/api/v1/sgcc/geo"}} {
		w := doReq(t, h, ep[0], ep[1], "good-token")
		if isRouterNotFound(w) || w.Code == http.StatusMethodNotAllowed || isMiddlewareAuthFail(w) {
			t.Errorf("兼容接口 %s %s 未正确注册: %d %s", ep[0], ep[1], w.Code, w.Body.String())
		}
	}
	pages := httpapi.WithLegacyPages(h, "../../../server/public", legacyCfg{})
	w := doReq(t, pages, "GET", "/sgcc-captcha.html", "")
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "fixture-app-id") || strings.Contains(w.Body.String(), "__SGCC_DX_APPID__") {
		t.Errorf("滑块页未正确注入运行时 AppID: %d", w.Code)
	}
}

// jsonUnmarshal 小封装，避免各用例重复处理错误。
func jsonUnmarshal(b []byte, v any) error { return json.Unmarshal(b, v) }
