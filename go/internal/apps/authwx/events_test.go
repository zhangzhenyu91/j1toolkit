package authwx

// 登录安全事件：成功登录/微信绑定/失败均写 category=security，不泄漏密码、openid、session_key。

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"

	"shade/internal/httpapi"
	"shade/internal/logging"
)

type secEvent struct {
	level, category, event, messageCN, raw string
	fields                                 map[string]any
	userID, teamID                         int64
}

type recordingEvents struct {
	mu     sync.Mutex
	events []secEvent
}

func (r *recordingEvents) Record(ctx context.Context, level, category, event, messageCN, raw string, fields map[string]any) error {
	userID, teamID := logging.ActorFromContext(ctx)
	if v, ok := fields["user_id"].(int64); ok && userID == 0 {
		userID = v
	}
	if v, ok := fields["team_id"].(int64); ok && teamID == 0 {
		teamID = v
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, secEvent{level, category, event, messageCN, raw, fields, userID, teamID})
	return nil
}

func (r *recordingEvents) find(event string) (secEvent, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, e := range r.events {
		if e.event == event {
			return e, true
		}
	}
	return secEvent{}, false
}

func TestLoginSecurityEvents(t *testing.T) {
	db, f := openFake(t)
	f.onRows("alice", userCols, userVals(7, "alice", "阿丽", "a.png", int64(1), "检修一班", "user", 1, bcryptHash(t, "pw"), nil))
	m := New(db, fakeCfg{"WX_APPID": "wx-app", "WX_SECRET": "wx-secret"}, &fakeCore{})
	m.WX = &fakeWX{sess: WXSession{OpenID: "wx-open-9", UnionID: "u-9", SessionKey: "sk-9"}}
	rec := &recordingEvents{}
	m.Events = rec
	h := httpapi.NewRouter(httpapi.Deps{}, m)

	status, r, raw := doReq(t, h, http.MethodPost, "/api/v1/auth/login",
		`{"username":"alice","password":"pw","wx_code":"code-1"}`)
	assertCode(t, status, 200, r, 0)
	login, ok := rec.find("security.login.succeeded")
	if !ok {
		t.Fatalf("缺登录成功事件：%+v", rec.events)
	}
	if login.category != "security" || login.level != "info" || login.userID != 7 || login.teamID != 1 {
		t.Fatalf("登录事件形态/归属不符：%+v", login)
	}
	if _, ok := rec.find("security.wx.bound"); !ok {
		t.Fatalf("缺微信绑定事件：%+v", rec.events)
	}
	for _, e := range rec.events {
		for _, leak := range []string{"pw", "wx-open-9", "sk-9", "wx-secret"} {
			if strings.Contains(e.raw+e.messageCN, leak) {
				t.Fatalf("安全事件泄漏 %q：%+v", leak, e)
			}
		}
	}
	if strings.Contains(raw, "wx-open-9") {
		t.Fatalf("响应泄漏 openid：%s", raw)
	}
}

func TestLoginFailureSecurityEvent(t *testing.T) {
	db, f := openFake(t)
	f.onRows("alice", userCols, userVals(7, "alice", "阿丽", "a.png", int64(1), "检修一班", "user", 1, bcryptHash(t, "pw"), nil))
	m := New(db, fakeCfg{}, &fakeCore{})
	rec := &recordingEvents{}
	m.Events = rec
	h := httpapi.NewRouter(httpapi.Deps{}, m)
	status, _, _ := doReq(t, h, http.MethodPost, "/api/v1/auth/login", `{"username":"alice","password":"wrong"}`)
	if status != http.StatusUnauthorized {
		t.Fatalf("status = %d", status)
	}
	e, ok := rec.find("security.login.failed")
	if !ok {
		t.Fatalf("缺登录失败事件：%+v", rec.events)
	}
	if e.level != "warn" || e.category != "security" || strings.Contains(e.raw+e.messageCN, "wrong") {
		t.Fatalf("失败事件形态不符：%+v", e)
	}
}

// TestLoginBindOpenIDFailureSecurityEvent bindOpenID 写库失败必须写 security.wx.bind_failed（warn），
// 不阻断登录，也不记录 openid/session_key。
func TestLoginBindOpenIDFailureSecurityEvent(t *testing.T) {
	db, f := openFake(t)
	f.onRows("alice", userCols, userVals(7, "alice", "阿丽", "a.png", int64(1), "检修一班", "user", 1, bcryptHash(t, "pw"), nil))
	f.onErr("UPDATE sys_user SET openid = ?", errors.New("db write failed"))
	m := New(db, fakeCfg{"WX_APPID": "wx-app", "WX_SECRET": "wx-secret"}, &fakeCore{})
	m.WX = &fakeWX{sess: WXSession{OpenID: "wx-open-9", SessionKey: "sk-9"}}
	rec := &recordingEvents{}
	m.Events = rec
	h := httpapi.NewRouter(httpapi.Deps{}, m)

	status, r, _ := doReq(t, h, http.MethodPost, "/api/v1/auth/login",
		`{"username":"alice","password":"pw","wx_code":"code-1"}`)
	assertCode(t, status, 200, r, 0)
	e, ok := rec.find("security.wx.bind_failed")
	if !ok {
		t.Fatalf("缺绑定失败安全事件：%+v", rec.events)
	}
	if e.level != "warn" || e.category != "security" {
		t.Fatalf("绑定失败事件形态不符：%+v", e)
	}
	if e.userID != 7 || e.teamID != 1 {
		t.Fatalf("绑定失败事件归属不符：%+v", e)
	}
	for _, secret := range []string{"wx-open-9", "sk-9", "wx-secret"} {
		if strings.Contains(e.raw+e.messageCN+fmt.Sprint(e.fields), secret) {
			t.Fatalf("绑定失败事件泄漏 %q：%+v", secret, e)
		}
	}
}

// TestWXLoginFailureRedactsRawError code2session 报错含完整 URL/secret 时：
// 匿名调用方只拿固定文案；安全事件只落脱敏后的原文（secret 以 **** 呈现）。
func TestWXLoginFailureRedactsRawError(t *testing.T) {
	m := New(nil, fakeCfg{"WX_APPID": "wx-app", "WX_SECRET": "wx-secret"}, &fakeCore{})
	m.WX = &fakeWX{err: errors.New(`Get "https://api.weixin.qq.com/sns/jscode2session?appid=wx-app&secret=wx-secret&js_code=code-1&grant_type=authorization_code": dial tcp 10.0.0.1:443: connect: connection refused`)}
	rec := &recordingEvents{}
	m.Events = rec
	h := httpapi.NewRouter(httpapi.Deps{}, m)

	status, r, raw := doReq(t, h, http.MethodPost, "/api/v1/auth/wx-login", `{"code":"code-1"}`)
	assertCode(t, status, 400, r, codeWXFailed)
	if r.Message != "微信登录失败，请稍后重试" {
		t.Fatalf("匿名调用方应只拿固定文案: %q", r.Message)
	}
	for _, leak := range []string{"wx-secret", "secret=wx-secret", "api.weixin.qq.com", "jscode2session"} {
		if strings.Contains(raw, leak) {
			t.Fatalf("响应泄漏 %q: %s", leak, raw)
		}
	}

	e, ok := rec.find("security.login.failed")
	if !ok {
		t.Fatalf("缺微信登录失败安全事件：%+v", rec.events)
	}
	if e.level != "warn" || e.category != "security" {
		t.Fatalf("事件形态不符：%+v", e)
	}
	detail := e.raw + e.messageCN + fmt.Sprint(e.fields)
	if strings.Contains(detail, "wx-secret") {
		t.Fatalf("安全事件泄漏 secret：%+v", e)
	}
	if !strings.Contains(detail, "****") {
		t.Fatalf("安全事件应保留脱敏后的原始错误（****）：%+v", e)
	}
}
