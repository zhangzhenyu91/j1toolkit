package authwx

// 小程序登录兼容回归（经 httpapi.NewRouter 全链路，Node routes/auth.js + pages/login 契约）：
// 数字信封（40313/40302/50301 等）、wx_bound/bind_message 字段、原小程序用户形状、
// openid / session_key / WX_SECRET 不外泄；假 code2session，绝不连微信。

import (
	"context"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"

	"shade/internal/auth"
	"shade/internal/httpapi"
)

var _ Authenticator = (*auth.Service)(nil)

// resp 统一信封解码形状。
type resp struct {
	Code    any             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data"`
}

func doReq(t *testing.T, h http.Handler, method, path, body string) (int, resp, string) {
	t.Helper()
	var rd *strings.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	var req *http.Request
	if rd != nil {
		req = httptest.NewRequest(method, path, rd)
	} else {
		req = httptest.NewRequest(method, path, nil)
	}
	req.RemoteAddr = "10.0.0.9:1234"
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	raw := rec.Body.String()
	var r resp
	if err := json.Unmarshal(rec.Body.Bytes(), &r); err != nil {
		t.Fatalf("响应不是 JSON 信封: %v body=%s", err, raw)
	}
	return rec.Code, r, raw
}

func assertCode(t *testing.T, status, wantStatus int, r resp, wantCode int) {
	t.Helper()
	if status != wantStatus {
		t.Fatalf("status = %d, want %d (body=%+v)", status, wantStatus, r)
	}
	if r.Code != float64(wantCode) {
		t.Fatalf("code = %v (%T), want %d", r.Code, r.Code, wantCode)
	}
}

func bcryptHash(t *testing.T, password string) string {
	t.Helper()
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	return string(hash)
}

// userVals userSelect 10 列行值（nickname/avatar/team/hash/openid 可为 nil）。
func userVals(id int64, username, nickname, avatar string, teamID any, teamName any, role string, status int64, hash, openid any) []driver.Value {
	return []driver.Value{int64(id), username, nickname, avatar, teamID, teamName, role, int64(status), hash, openid}
}

var userCols = []string{"id", "username", "nickname", "avatar", "team_id", "team_name", "role", "status", "password_hash", "openid"}

// ---- POST /auth/login ----

func TestLoginWithWXCodeBindsWeChat(t *testing.T) {
	db, f := openFake(t)
	f.onRows("alice", userCols, userVals(7, "alice", "阿丽", "a.png", int64(1), "检修一班", "user", 1, bcryptHash(t, "pw"), nil))
	m := New(db, fakeCfg{"WX_APPID": "wx-app", "WX_SECRET": "wx-secret"}, &fakeCore{})
	m.WX = &fakeWX{sess: WXSession{OpenID: "wx-open-9", UnionID: "u-9", SessionKey: "sk-9"}}
	h := httpapi.NewRouter(httpapi.Deps{}, m)

	status, r, raw := doReq(t, h, http.MethodPost, "/api/v1/auth/login",
		`{"username":"alice","password":"pw","wx_code":"code-1"}`)
	assertCode(t, status, 200, r, 0)

	var data httpapi.WXLoginData
	if err := json.Unmarshal(r.Data, &data); err != nil {
		t.Fatalf("data 解码失败: %v", err)
	}
	if data.Token != "jwt-alice" || data.ExpiresIn != 3600 {
		t.Fatalf("令牌不符: %+v", data)
	}
	if !data.WXBound || data.BindMessage != "" {
		t.Fatalf("本次应完成绑定且无 bind_message: %+v", data)
	}
	u := data.User
	if u.ID != 7 || u.Username != "alice" || u.Nickname != "阿丽" || u.Avatar != "a.png" ||
		u.Team != "检修一班" || u.Role != "user" || !u.WXBound {
		t.Fatalf("小程序用户形状不符: %+v", u)
	}
	if u.TeamID == nil || *u.TeamID != 1 {
		t.Fatalf("team_id 不符: %+v", u.TeamID)
	}

	execs := strings.Join(f.execQueries(), "\n")
	if !strings.Contains(execs, "UPDATE sys_user SET openid = NULL") || !strings.Contains(execs, "UPDATE sys_user SET openid = ?") {
		t.Fatalf("缺少 openid 绑定 SQL: %s", execs)
	}
	found := false
	for _, v := range f.execArgs() {
		if v == "wx-open-9" {
			found = true
		}
	}
	if !found {
		t.Fatalf("绑定 SQL 未携带 openid: %v", f.execArgs())
	}

	for _, leak := range []string{"wx-open-9", "sk-9", "wx-secret", "openid", "session_key"} {
		if strings.Contains(raw, leak) {
			t.Fatalf("响应泄漏 %q: %s", leak, raw)
		}
	}
}

func TestLoginMissingCredentials(t *testing.T) {
	m := New(nil, fakeCfg{}, &fakeCore{})
	h := httpapi.NewRouter(httpapi.Deps{}, m)
	status, r, _ := doReq(t, h, http.MethodPost, "/api/v1/auth/login", `{"username":"","password":""}`)
	assertCode(t, status, 400, r, codeBadParam)
	if r.Message != "请输入账号和密码" {
		t.Fatalf("message = %q", r.Message)
	}
}

func TestLoginBadPassword(t *testing.T) {
	db, f := openFake(t)
	f.onRows("alice", userCols, userVals(7, "alice", "阿丽", "a.png", int64(1), "检修一班", "user", 1, bcryptHash(t, "pw"), nil))
	m := New(db, fakeCfg{}, &fakeCore{})
	h := httpapi.NewRouter(httpapi.Deps{}, m)
	status, r, raw := doReq(t, h, http.MethodPost, "/api/v1/auth/login", `{"username":"alice","password":"wrong"}`)
	assertCode(t, status, 401, r, codeBadCreds)
	if r.Message != "账号或密码错误" {
		t.Fatalf("message = %q", r.Message)
	}
	if strings.Contains(raw, "token") {
		t.Fatalf("失败响应不得含令牌: %s", raw)
	}
}

func TestLoginBindFailureKeepsLogin(t *testing.T) {
	db, f := openFake(t)
	f.onRows("alice", userCols, userVals(7, "alice", "阿丽", "a.png", int64(1), "检修一班", "user", 1, bcryptHash(t, "pw"), nil))
	m := New(db, fakeCfg{"WX_APPID": "wx-app", "WX_SECRET": "wx-secret"}, &fakeCore{})
	m.WX = &fakeWX{err: errors.New("40029 invalid code")}
	h := httpapi.NewRouter(httpapi.Deps{}, m)

	status, r, _ := doReq(t, h, http.MethodPost, "/api/v1/auth/login",
		`{"username":"alice","password":"pw","wx_code":"code-1"}`)
	assertCode(t, status, 200, r, 0)
	var data httpapi.WXLoginData
	if err := json.Unmarshal(r.Data, &data); err != nil {
		t.Fatal(err)
	}
	if data.WXBound || data.BindMessage != "微信号绑定失败，可稍后在登录时重试" {
		t.Fatalf("绑定失败应仅提示不阻断: %+v", data)
	}
	if data.Token != "jwt-alice" || data.User.WXBound {
		t.Fatalf("登录应成功且未绑定: %+v", data)
	}
}

func TestLoginSkipsBindWhenWXUnconfigured(t *testing.T) {
	db, f := openFake(t)
	f.onRows("alice", userCols, userVals(7, "alice", "阿丽", "a.png", int64(1), "检修一班", "user", 1, bcryptHash(t, "pw"), nil))
	wx := &fakeWX{sess: WXSession{OpenID: "wx-open-9"}}
	m := New(db, fakeCfg{}, &fakeCore{}) // 无 WX_APPID / WX_SECRET
	m.WX = wx
	h := httpapi.NewRouter(httpapi.Deps{}, m)

	status, r, raw := doReq(t, h, http.MethodPost, "/api/v1/auth/login",
		`{"username":"alice","password":"pw","wx_code":"code-1"}`)
	assertCode(t, status, 200, r, 0)
	var data httpapi.WXLoginData
	if err := json.Unmarshal(r.Data, &data); err != nil {
		t.Fatal(err)
	}
	if data.WXBound || data.BindMessage != "" || data.User.WXBound {
		t.Fatalf("未配置微信应静默跳过绑定: %+v", data)
	}
	if strings.Contains(raw, "bind_message") {
		t.Fatalf("未配置时不应下发 bind_message: %s", raw)
	}
	if wx.calls != 0 {
		t.Fatalf("未配置时不得调用 code2session: %d", wx.calls)
	}
}

// ---- POST /auth/wx-login ----

func TestWXLoginSuccess(t *testing.T) {
	db, f := openFake(t)
	f.onRows("wx-open-9", userCols, userVals(7, "alice", "阿丽", "a.png", int64(1), "检修一班", "user", 1, nil, "wx-open-9"))
	m := New(db, fakeCfg{"WX_APPID": "wx-app", "WX_SECRET": "wx-secret"}, &fakeCore{})
	m.WX = &fakeWX{sess: WXSession{OpenID: "wx-open-9", SessionKey: "sk-9"}}
	h := httpapi.NewRouter(httpapi.Deps{}, m)

	status, r, raw := doReq(t, h, http.MethodPost, "/api/v1/auth/wx-login", `{"code":"code-1"}`)
	assertCode(t, status, 200, r, 0)

	var keys map[string]json.RawMessage
	if err := json.Unmarshal(r.Data, &keys); err != nil {
		t.Fatal(err)
	}
	if _, ok := keys["wx_bound"]; ok {
		t.Fatalf("wx-login 载荷不应含 wx_bound: %s", raw)
	}
	if len(keys) != 3 {
		t.Fatalf("wx-login 载荷应仅 token/expires_in/user: %s", raw)
	}
	var data httpapi.WXSessionData
	if err := json.Unmarshal(r.Data, &data); err != nil {
		t.Fatal(err)
	}
	if data.Token != "jwt-alice" || data.ExpiresIn != 3600 || !data.User.WXBound || data.User.ID != 7 {
		t.Fatalf("wx-login 载荷不符: %+v", data)
	}
	for _, leak := range []string{"wx-open-9", "sk-9", "openid", "session_key"} {
		if strings.Contains(raw, leak) {
			t.Fatalf("响应泄漏 %q: %s", leak, raw)
		}
	}
}

func TestWXLoginUnboundAndDisabled(t *testing.T) {
	// 未绑定任何账号 → 40313
	db, _ := openFake(t)
	m := New(db, fakeCfg{"WX_APPID": "wx-app", "WX_SECRET": "wx-secret"}, &fakeCore{})
	m.WX = &fakeWX{sess: WXSession{OpenID: "wx-open-9"}}
	h := httpapi.NewRouter(httpapi.Deps{}, m)
	status, r, _ := doReq(t, h, http.MethodPost, "/api/v1/auth/wx-login", `{"code":"code-1"}`)
	assertCode(t, status, 403, r, 40313)
	if r.Message != "该微信号尚未绑定账号，请先使用账号密码登录一次后再使用微信登录" {
		t.Fatalf("message = %q", r.Message)
	}

	// 绑定账号被禁用 → 40302
	db2, f2 := openFake(t)
	f2.onRows("wx-open-9", userCols, userVals(7, "alice", "阿丽", "a.png", int64(1), "检修一班", "user", 2, nil, "wx-open-9"))
	m2 := New(db2, fakeCfg{"WX_APPID": "wx-app", "WX_SECRET": "wx-secret"}, &fakeCore{})
	m2.WX = &fakeWX{sess: WXSession{OpenID: "wx-open-9"}}
	h2 := httpapi.NewRouter(httpapi.Deps{}, m2)
	status, r, _ = doReq(t, h2, http.MethodPost, "/api/v1/auth/wx-login", `{"code":"code-1"}`)
	assertCode(t, status, 403, r, 40302)
	if r.Message != "账号已被禁用，请联系管理员" {
		t.Fatalf("message = %q", r.Message)
	}
}

func TestWXLoginNotConfiguredAndBadCode(t *testing.T) {
	// 未配置微信 → 50301 明确错误，且不触达 code2session
	wx := &fakeWX{sess: WXSession{OpenID: "wx-open-9"}}
	m := New(nil, fakeCfg{}, &fakeCore{})
	m.WX = wx
	h := httpapi.NewRouter(httpapi.Deps{}, m)
	status, r, _ := doReq(t, h, http.MethodPost, "/api/v1/auth/wx-login", `{"code":"code-1"}`)
	assertCode(t, status, 503, r, 50301)
	if r.Message != "微信登录未配置（WX_APPID / WX_SECRET）" {
		t.Fatalf("message = %q", r.Message)
	}
	if wx.calls != 0 {
		t.Fatalf("未配置时不得调用 code2session: %d", wx.calls)
	}

	// 缺 code → 40002
	m2 := New(nil, fakeCfg{"WX_APPID": "wx-app", "WX_SECRET": "wx-secret"}, &fakeCore{})
	m2.WX = &fakeWX{}
	h2 := httpapi.NewRouter(httpapi.Deps{}, m2)
	status, r, _ = doReq(t, h2, http.MethodPost, "/api/v1/auth/wx-login", `{"code":""}`)
	assertCode(t, status, 400, r, 40002)

	// code2session 报错 → 40012
	m3 := New(nil, fakeCfg{"WX_APPID": "wx-app", "WX_SECRET": "wx-secret"}, &fakeCore{})
	m3.WX = &fakeWX{err: errors.New("40029 invalid code")}
	h3 := httpapi.NewRouter(httpapi.Deps{}, m3)
	status, r, _ = doReq(t, h3, http.MethodPost, "/api/v1/auth/wx-login", `{"code":"code-1"}`)
	assertCode(t, status, 400, r, 40012)
	if r.Message != "微信登录失败：40029 invalid code" {
		t.Fatalf("message = %q", r.Message)
	}
}

// ---- POST /auth/app-login ----

func TestAppLogin(t *testing.T) {
	db, f := openFake(t)
	f.onRows("alice", userCols, userVals(7, "alice", "阿丽", "a.png", int64(1), "检修一班", "user", 1, bcryptHash(t, "pw"), nil))
	f.onRows("sys_user_app", []string{"id"}, []driver.Value{int64(1)})
	m := New(db, fakeCfg{}, &fakeCore{})
	m.WX = &fakeWX{}
	h := httpapi.NewRouter(httpapi.Deps{}, m)

	status, r, raw := doReq(t, h, http.MethodPost, "/api/v1/auth/app-login",
		`{"username":"alice","password":"pw","app_key":"work-log"}`)
	assertCode(t, status, 200, r, 0)
	var keys map[string]json.RawMessage
	if err := json.Unmarshal(r.Data, &keys); err != nil {
		t.Fatal(err)
	}
	if len(keys) != 1 {
		t.Fatalf("app-login 载荷应仅 user: %s", raw)
	}
	if _, ok := keys["token"]; ok {
		t.Fatalf("app-login 不得签发本平台 JWT: %s", raw)
	}
	var data httpapi.AppLoginData
	if err := json.Unmarshal(r.Data, &data); err != nil {
		t.Fatal(err)
	}
	if data.User.ID != 7 || data.User.Team != "检修一班" {
		t.Fatalf("user 形状不符: %+v", data.User)
	}

	// 无应用权限 → 40301（COUNT(*) 恒回一行，0 行值）
	f.onRows("sys_user_app", []string{"id"}, []driver.Value{int64(0)})
	status, r, _ = doReq(t, h, http.MethodPost, "/api/v1/auth/app-login",
		`{"username":"alice","password":"pw","app_key":"work-log"}`)
	assertCode(t, status, 403, r, 40301)
	if r.Message != "暂无该应用的使用权限" {
		t.Fatalf("message = %q", r.Message)
	}

	// 缺 app_key → 40001（同 Node 提示）
	status, r, _ = doReq(t, h, http.MethodPost, "/api/v1/auth/app-login",
		`{"username":"alice","password":"pw"}`)
	assertCode(t, status, 400, r, codeBadParam)
	if r.Message != "请输入账号和密码" {
		t.Fatalf("message = %q", r.Message)
	}
}

// ---- 限流（同 Node：窗口内失败 20 次拒绝，成功清零） ----

func TestLoginRateLimit(t *testing.T) {
	db, f := openFake(t)
	f.onRows("alice", userCols, userVals(7, "alice", "阿丽", "a.png", int64(1), "检修一班", "user", 1, bcryptHash(t, "pw"), nil))
	f.onRows("ghost", userCols) // 不存在的账号：走快速失败路径
	m := New(db, fakeCfg{}, &fakeCore{})
	h := httpapi.NewRouter(httpapi.Deps{}, m)

	// 窗口内失败满 20 次 → 拒绝（同 Node：限流期间正确口令也 429）
	for i := 0; i < loginFailMax; i++ {
		status, r, _ := doReq(t, h, http.MethodPost, "/api/v1/auth/login", `{"username":"ghost","password":"x"}`)
		assertCode(t, status, 401, r, codeBadCreds)
	}
	status, r, _ := doReq(t, h, http.MethodPost, "/api/v1/auth/login", `{"username":"ghost","password":"x"}`)
	assertCode(t, status, 429, r, codeRateLimit)
	if r.Message != "尝试过于频繁，请稍后再试" {
		t.Fatalf("message = %q", r.Message)
	}
	status, r, _ = doReq(t, h, http.MethodPost, "/api/v1/auth/login", `{"username":"alice","password":"pw"}`)
	assertCode(t, status, 429, r, codeRateLimit)

	// 成功登录清零计数：19 次失败 + 1 次成功后重新累计（未限流时不拒绝）
	db2, f2 := openFake(t)
	f2.onRows("alice", userCols, userVals(7, "alice", "阿丽", "a.png", int64(1), "检修一班", "user", 1, bcryptHash(t, "pw"), nil))
	f2.onRows("ghost", userCols)
	m2 := New(db2, fakeCfg{}, &fakeCore{})
	h2 := httpapi.NewRouter(httpapi.Deps{}, m2)
	for i := 0; i < loginFailMax-1; i++ {
		status, r, _ = doReq(t, h2, http.MethodPost, "/api/v1/auth/login", `{"username":"ghost","password":"x"}`)
		assertCode(t, status, 401, r, codeBadCreds)
	}
	status, r, _ = doReq(t, h2, http.MethodPost, "/api/v1/auth/login", `{"username":"alice","password":"pw"}`)
	assertCode(t, status, 200, r, 0)
	for i := 0; i < loginFailMax-1; i++ {
		status, r, _ = doReq(t, h2, http.MethodPost, "/api/v1/auth/login", `{"username":"ghost","password":"x"}`)
		assertCode(t, status, 401, r, codeBadCreds)
	}
}

// ---- HTTP code2session 适配（httptest 本地回环，不连微信） ----

func TestHTTPCode2Session(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("appid") != "wx-app" || q.Get("secret") != "wx-secret" ||
			q.Get("js_code") != "code-1" || q.Get("grant_type") != "authorization_code" {
			t.Errorf("请求参数不符: %s", r.URL.RawQuery)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"openid": "o1", "unionid": "u1", "session_key": "s1"})
	}))
	defer srv.Close()

	c := &HTTPCode2Session{Client: srv.Client(), BaseURL: srv.URL}
	sess, err := c.Code2Session(context.Background(), "wx-app", "wx-secret", "code-1")
	if err != nil {
		t.Fatal(err)
	}
	if sess.OpenID != "o1" || sess.UnionID != "u1" || sess.SessionKey != "s1" {
		t.Fatalf("会话映射不符: %+v", sess)
	}

	errSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"errcode": 40029, "errmsg": "invalid code"})
	}))
	defer errSrv.Close()
	c2 := &HTTPCode2Session{Client: errSrv.Client(), BaseURL: errSrv.URL}
	if _, err := c2.Code2Session(context.Background(), "wx-app", "wx-secret", "bad"); err == nil || err.Error() != "invalid code" {
		t.Fatalf("errcode 应转为错误: %v", err)
	}

	noMsgSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"errcode": 40029})
	}))
	defer noMsgSrv.Close()
	c3 := &HTTPCode2Session{Client: noMsgSrv.Client(), BaseURL: noMsgSrv.URL}
	if _, err := c3.Code2Session(context.Background(), "wx-app", "wx-secret", "bad"); err == nil || err.Error() != "微信接口错误（40029）" {
		t.Fatalf("缺 errmsg 应兜底文案: %v", err)
	}
}

// ---- 公开路径纪律：仅登录接口免登录，admin / logout 不放行 ----

func TestPublicOnlyLoginRoutes(t *testing.T) {
	stub := plainAuth{ident: auth.Identity{ID: 3, Username: "u", Role: "admin"}}
	h := httpapi.NewRouter(httpapi.Deps{Auth: stub})

	status, _, _ := doReq(t, h, http.MethodPost, "/api/v1/auth/logout", "")
	if status != http.StatusUnauthorized {
		t.Fatalf("/auth/logout 不得匿名放行: %d", status)
	}
	status, _, _ = doReq(t, h, http.MethodGet, "/api/v1/admin/config", "")
	if status != http.StatusUnauthorized {
		t.Fatalf("admin 路径不得匿名放行: %d", status)
	}

	// 带 token 的 logout：数字信封 {code:0,message:"已退出登录",data:null}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/logout", nil)
	req.Header.Set("Authorization", "Bearer good-token")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	var r resp
	if err := json.Unmarshal(rec.Body.Bytes(), &r); err != nil {
		t.Fatal(err)
	}
	if rec.Code != 200 || r.Code != float64(0) || r.Message != "已退出登录" {
		t.Fatalf("logout 形状不符: %d %+v", rec.Code, r)
	}
}

// ---- 核心回退登录（无 authwx 模块：纯账号密码 + 数字信封） ----

func TestFallbackLoginWithoutModule(t *testing.T) {
	h := httpapi.NewRouter(httpapi.Deps{Auth: plainAuth{ident: auth.Identity{ID: 3, Username: "u", Nickname: "昵称", Role: "user", TeamID: 5}}})

	status, r, _ := doReq(t, h, http.MethodPost, "/api/v1/auth/login", `{"username":"","password":""}`)
	assertCode(t, status, 400, r, codeBadParam)

	status, r, _ = doReq(t, h, http.MethodPost, "/api/v1/auth/login", `{"username":"u","password":"bad"}`)
	assertCode(t, status, 401, r, codeBadCreds)

	status, r, _ = doReq(t, h, http.MethodPost, "/api/v1/auth/login", `{"username":"u","password":"ok","client":"web"}`)
	assertCode(t, status, 200, r, 0)
	var data httpapi.WXLoginData
	if err := json.Unmarshal(r.Data, &data); err != nil {
		t.Fatal(err)
	}
	if data.Token != "plain-token" || data.ExpiresIn != 60 || data.WXBound {
		t.Fatalf("回退登录载荷不符: %+v", data)
	}
	if data.User.ID != 3 || data.User.WXBound || data.User.TeamID == nil || *data.User.TeamID != 5 {
		t.Fatalf("回退登录用户形状不符: %+v", data.User)
	}
}

// ---- 模块兼作 httpapi.Authenticator（与 auth.Service 复用） ----

func TestModuleAsAuthenticator(t *testing.T) {
	db, f := openFake(t)
	f.onRows("alice", userCols, userVals(7, "alice", "阿丽", "a.png", int64(1), "检修一班", "user", 1, bcryptHash(t, "pw"), nil))
	users := auth.StaticUserStore{Users: map[string]struct {
		Identity auth.Identity
		Hash     string
	}{
		"alice": {Identity: auth.Identity{ID: 7, Username: "alice", Nickname: "阿丽", Role: "user", TeamID: 1}},
	}}
	core := auth.NewService(users, nil, "test-secret", time.Hour, time.Minute)
	m := New(db, nil, core)

	result, err := m.Login(context.Background(), "alice", "pw", "")
	if err != nil || result.Token == "" || result.ExpiresIn != 3600 {
		t.Fatalf("登录签发失败: %+v %v", result, err)
	}
	ident, err := m.Authenticate(context.Background(), result.Token)
	if err != nil || ident.ID != 7 {
		t.Fatalf("委托鉴权失败: %+v %v", ident, err)
	}
	if err := m.Logout(context.Background(), result.Token, time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Login(context.Background(), "alice", "bad", ""); !errors.Is(err, auth.ErrInvalidCredentials) {
		t.Fatalf("错误密码应报凭证错误: %v", err)
	}

	// 核心仅提供签发适配时：Authenticate 明确拒绝、Logout 降级为空操作
	m2 := New(nil, nil, &fakeCore{})
	if _, err := m2.Authenticate(context.Background(), "any"); !errors.Is(err, auth.ErrUnauthorized) {
		t.Fatalf("缺鉴权能力应报未登录: %v", err)
	}
	if err := m2.Logout(context.Background(), "any", time.Now()); err != nil {
		t.Fatalf("缺拉黑能力应降级: %v", err)
	}
}
