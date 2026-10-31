package httpapi

// 审计与运行事件：2xx 写操作 audit、panic runtime 脱敏、登录/登出 security、配置修改 config。

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"shade/internal/auth"
	"shade/internal/config"
	"shade/internal/logging"
)

// captureSink 记录应用日志条目（access/audit/security 等）。
type captureSink struct {
	mu      sync.Mutex
	entries []config.AppLogEntry
}

func (s *captureSink) Write(_ context.Context, e config.AppLogEntry) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.entries = append(s.entries, e)
	return nil
}

func (s *captureSink) snapshot() []config.AppLogEntry {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]config.AppLogEntry(nil), s.entries...)
}

func (s *captureSink) byCategory(category string) []config.AppLogEntry {
	var out []config.AppLogEntry
	for _, e := range s.snapshot() {
		if e.Category == category {
			out = append(out, e)
		}
	}
	return out
}

// fakeEvents 记录事件写入口（Deps.Events）；归属从 ctx actor 取，fields 序列化进 Detail。
type fakeEvents struct {
	mu     sync.Mutex
	events []config.AppLogEntry
}

func (f *fakeEvents) Record(ctx context.Context, level, category, event, messageCN, raw string, fields map[string]any) error {
	detail := ""
	if len(fields) > 0 {
		if b, err := json.Marshal(fields); err == nil {
			detail = string(b)
		}
	}
	userID, teamID := logging.ActorFromContext(ctx)
	f.mu.Lock()
	defer f.mu.Unlock()
	f.events = append(f.events, config.AppLogEntry{
		AppCode: "system", Level: level, Category: category, Event: event,
		Message: raw, MessageCN: messageCN, RawMessage: raw, Detail: detail,
		UserID: userID, TeamID: teamID,
	})
	return nil
}

func (f *fakeEvents) snapshot() []config.AppLogEntry {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]config.AppLogEntry(nil), f.events...)
}

func (f *fakeEvents) find(event string) (config.AppLogEntry, bool) {
	for _, e := range f.snapshot() {
		if e.Event == event {
			return e, true
		}
	}
	return config.AppLogEntry{}, false
}

func TestAccessLogMutationAudit(t *testing.T) {
	sink := &captureSink{}
	h := WithAccessLogOptions(okHandler(http.StatusOK, "ok"), AccessLogOptions{Sink: sink})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/worklog/logs?token=SECRET", strings.NewReader("{}"))
	h.ServeHTTP(httptest.NewRecorder(), req)

	entries := sink.snapshot()
	if len(entries) != 2 {
		t.Fatalf("条目 %d 条，期望 access + audit：%+v", len(entries), entries)
	}
	if entries[0].Category != "access" || entries[1].Category != "audit" {
		t.Fatalf("类别不符：%+v", entries)
	}
	audit := entries[1]
	if audit.Event != "http.mutation" || audit.Level != "info" {
		t.Fatalf("audit 形态不符：%+v", audit)
	}
	for _, want := range []string{"POST", "/api/v1/worklog/logs", "200"} {
		if !strings.Contains(audit.Detail, want) {
			t.Fatalf("audit detail 缺 %q：%s", want, audit.Detail)
		}
	}
	if strings.Contains(audit.Detail+audit.Message+audit.MessageCN, "SECRET") {
		t.Fatalf("audit 泄漏 query 秘密：%+v", audit)
	}
}

func TestAccessLogNoAuditForReadOrFailure(t *testing.T) {
	cases := []struct {
		method string
		status int
	}{
		{http.MethodGet, http.StatusOK},
		{http.MethodPost, http.StatusInternalServerError},
		{http.MethodPut, http.StatusBadRequest},
		{http.MethodDelete, http.StatusUnauthorized},
	}
	for _, c := range cases {
		sink := &captureSink{}
		h := WithAccessLogOptions(okHandler(c.status, ""), AccessLogOptions{Sink: sink})
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(c.method, "/api/v1/worklog/logs", nil))
		entries := sink.snapshot()
		if len(entries) != 1 || entries[0].Category != "access" {
			t.Fatalf("%s %d 产生 %d 条（期望仅 access）：%+v", c.method, c.status, len(entries), entries)
		}
	}
}

func TestAccessLogMasksTicketPaths(t *testing.T) {
	sink := &captureSink{}
	h := WithAccessLogOptions(okHandler(http.StatusOK, ""), AccessLogOptions{Sink: sink})
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet,
		"/api/v1/office/raw/TICKET123/file.docx?token=SECRET", nil))
	entries := sink.snapshot()
	if len(entries) != 1 {
		t.Fatalf("条目 = %d", len(entries))
	}
	e := entries[0]
	if !strings.Contains(e.Detail, "/api/v1/office/raw/****/file.docx") {
		t.Fatalf("office raw 票据未掩码：%s", e.Detail)
	}
	if strings.Contains(e.Detail+e.Message+e.MessageCN, "TICKET123") || strings.Contains(e.Detail, "SECRET") {
		t.Fatalf("票据/秘密泄漏：%+v", e)
	}
}

// panicWith 触发指定 panic 文本的应用目录桩。
type panicWith string

func (p panicWith) ListApps(context.Context) ([]App, error) { panic(string(p)) }

func TestRecovererLogsSanitizedPanic(t *testing.T) {
	events := &fakeEvents{}
	h := NewRouter(Deps{Apps: panicWith("token=SECRET boom"), Events: events})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/app/list", nil))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "SECRET") {
		t.Fatalf("panic 秘密写入响应：%s", rec.Body.String())
	}
	e, ok := events.find("runtime.panic")
	if !ok {
		t.Fatalf("缺 runtime.panic 事件：%+v", events.snapshot())
	}
	if e.Category != "runtime" || e.Level != "error" {
		t.Fatalf("panic 事件形态不符：%+v", e)
	}
	if strings.Contains(e.Message+e.MessageCN, "SECRET") {
		t.Fatalf("panic 未脱敏：%+v", e)
	}
}

func TestLoginSecurityEvent(t *testing.T) {
	events := &fakeEvents{}
	ident := auth.Identity{ID: 7, Username: "u7", Role: "user", TeamID: 42}
	h := NewRouter(Deps{Auth: stubAuth{ident: ident}, Events: events})
	body := `{"username":"u7","password":"sekrit-pass","client":"web"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", strings.NewReader(body))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	e, ok := events.find("security.login.succeeded")
	if !ok {
		t.Fatalf("缺登录安全事件：%+v", events.snapshot())
	}
	if e.Category != "security" || e.Level != "info" {
		t.Fatalf("安全事件形态不符：%+v", e)
	}
	if strings.Contains(e.Message+e.MessageCN+e.Detail, "sekrit-pass") {
		t.Fatalf("密码泄漏：%+v", e)
	}
	if !strings.Contains(e.Message+e.Detail, "u7") {
		t.Fatalf("登录事件缺用户名：%+v", e)
	}
}

func TestLogoutSecurityEvent(t *testing.T) {
	events := &fakeEvents{}
	ident := auth.Identity{ID: 7, Username: "u7", Role: "user", TeamID: 42}
	h := NewRouter(Deps{Auth: stubAuth{ident: ident}, Events: events})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/logout", nil)
	req.Header.Set("Authorization", "Bearer tok")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	e, ok := events.find("security.logout")
	if !ok {
		t.Fatalf("缺登出安全事件：%+v", events.snapshot())
	}
	if e.Category != "security" || e.UserID != 7 || e.TeamID != 42 {
		t.Fatalf("登出事件归属不符：%+v", e)
	}
}

func TestConfigUpdatedEvent(t *testing.T) {
	events := &fakeEvents{}
	cfg := &fakeCfg{items: map[string]ConfigItem{
		"DEMO_KEY": {Key: "DEMO_KEY", Value: "old", Sensitive: false},
	}}
	h := NewRouter(Deps{Cfg: cfg, Events: events})
	body := `{"items":[{"key":"DEMO_KEY","value":"new-secret-value"}]}`
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPut, "/api/v1/admin/config", strings.NewReader(body)))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	e, ok := events.find("config.updated")
	if !ok {
		t.Fatalf("缺配置修改事件：%+v", events.snapshot())
	}
	if e.Category != "config" || e.Level != "info" {
		t.Fatalf("配置事件形态不符：%+v", e)
	}
	if !strings.Contains(e.Message+e.Detail, "DEMO_KEY") {
		t.Fatalf("配置事件缺键名：%+v", e)
	}
	if strings.Contains(e.Message+e.Detail+e.MessageCN, "new-secret-value") {
		t.Fatalf("配置值泄漏：%+v", e)
	}
}

func TestAccessAuditCarriesIdentityActor(t *testing.T) {
	sink := &captureSink{}
	ident := auth.Identity{ID: 7, Username: "u7", Role: "user", TeamID: 42}
	inner := WithAccessLogOptions(okHandler(http.StatusOK, ""), AccessLogOptions{Sink: sink})
	h := AuthMiddleware(inner, stubAuth{ident: ident})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/worklog/logs", strings.NewReader("{}"))
	req.Header.Set("Authorization", "Bearer tok")
	h.ServeHTTP(httptest.NewRecorder(), req)
	audits := sink.byCategory("audit")
	if len(audits) != 1 {
		t.Fatalf("audit = %d", len(audits))
	}
	if audits[0].UserID != 7 || audits[0].TeamID != 42 {
		t.Fatalf("audit 归属未回填：%+v", audits[0])
	}
}

func TestAccessLogMiddlewarePanicRuntimeEvent(t *testing.T) {
	sink := &captureSink{}
	events := &fakeEvents{}
	h := WithAccessLogOptions(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("token=SECRET boom")
	}), AccessLogOptions{Sink: sink, Events: events})
	func() {
		defer func() { _ = recover() }()
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/api/v1/worklog/logs", nil))
	}()
	e, ok := events.find("runtime.panic")
	if !ok {
		t.Fatalf("缺中间件 panic runtime 事件：%+v", events.snapshot())
	}
	if strings.Contains(e.Message+e.MessageCN, "SECRET") {
		t.Fatalf("中间件 panic 未脱敏：%+v", e)
	}
}
