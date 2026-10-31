package httpapi

import (
	"context"
	"database/sql/driver"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"shade/internal/auth"
	"shade/internal/config"
)

// accessCaptureSink 测试用日志 Sink：按真实 Sink 语义从 ctx 补 trace_id 后记录。
type accessCaptureSink struct {
	mu      sync.Mutex
	entries []config.AppLogEntry
}

func (s *accessCaptureSink) Write(_ context.Context, e config.AppLogEntry) error {
	s.mu.Lock()
	s.entries = append(s.entries, e)
	s.mu.Unlock()
	return nil
}

func (s *accessCaptureSink) all() []config.AppLogEntry {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]config.AppLogEntry, len(s.entries))
	copy(out, s.entries)
	return out
}

func singleEntry(t *testing.T, sink *accessCaptureSink) config.AppLogEntry {
	t.Helper()
	entries := sink.all()
	if len(entries) != 1 {
		t.Fatalf("访问日志写入 %d 条，期望 1 条", len(entries))
	}
	return entries[0]
}

// panicSink 模拟落库侧 panic：访问日志必须只吞不抛。
type panicSink struct{}

func (panicSink) Write(context.Context, config.AppLogEntry) error { panic("sink boom") }

// countingAuth 记录验签调用次数的鉴权桩（身份复用断言：一次请求只验签一次）。
type countingAuth struct {
	mu    sync.Mutex
	calls int
	ident auth.Identity
	err   error
}

func (c *countingAuth) Login(context.Context, string, string, string) (auth.LoginResult, error) {
	return auth.LoginResult{User: c.ident}, nil
}

func (c *countingAuth) Authenticate(context.Context, string) (auth.Identity, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls++
	return c.ident, c.err
}

func (c *countingAuth) Logout(context.Context, string, time.Time) error { return nil }

func (c *countingAuth) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls
}

// countCalls 统计假库中匹配子串的 SQL 次数（验签回查 sys_user 的探针）。
func countCalls(fake *fakeDB, match string) int {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	n := 0
	for _, c := range fake.calls {
		if strings.Contains(c.query, match) {
			n++
		}
	}
	return n
}

// ---- WithAccessLogOptions：可注入 Sink / Auth ----

func TestWithAccessLogOptionsUsesInjectedSink(t *testing.T) {
	sink := &accessCaptureSink{}
	h := WithAccessLogOptions(okHandler(http.StatusOK, "ok"), AccessLogOptions{Sink: sink})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/quiz/meta?page=2", nil))

	if rec.Code != http.StatusOK || rec.Body.String() != "ok" {
		t.Fatalf("响应被改动：code=%d body=%q", rec.Code, rec.Body.String())
	}
	e := singleEntry(t, sink)
	if e.AppCode != "quiz" || e.Level != "info" || e.Category != "access" || e.Event != "http.request" {
		t.Fatalf("条目字段异常：%+v", e)
	}
	if e.TraceID == "" || rec.Header().Get("X-Trace-Id") != e.TraceID {
		t.Fatalf("trace 未贯穿：行内 %q，响应头 %q", e.TraceID, rec.Header().Get("X-Trace-Id"))
	}
	if !strings.Contains(e.Detail, `"status":200`) {
		t.Fatalf("detail 缺状态：%s", e.Detail)
	}
}

func TestAccessLogReusesAuthMiddlewareIdentity(t *testing.T) {
	sink := &accessCaptureSink{}
	authn := &countingAuth{ident: auth.Identity{ID: 7, Username: "u7", Role: "admin", TeamID: 42}}
	// 最外层访问日志 + 内层鉴权：身份经共享 carrier 回传，只验签一次。
	h := WithAccessLogOptions(
		AuthMiddleware(okHandler(http.StatusOK, ""), authn),
		AccessLogOptions{Sink: sink, Auth: authn},
	)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/worklog/logs", nil)
	req.Header.Set("Authorization", "Bearer whatever")
	h.ServeHTTP(httptest.NewRecorder(), req)

	if got := authn.count(); got != 1 {
		t.Fatalf("Authenticate 调用 %d 次，期望 1 次（身份应复用）", got)
	}
	e := singleEntry(t, sink)
	if e.UserID != 7 || e.TeamID != 42 {
		t.Fatalf("访问日志归属 user=%d team=%d，期望 7/42", e.UserID, e.TeamID)
	}
	if !strings.Contains(e.Detail, `"username":"u7"`) {
		t.Fatalf("detail 缺 username：%s", e.Detail)
	}
}

func TestAccessLogForbiddenReusesIdentity(t *testing.T) {
	sink := &accessCaptureSink{}
	authn := &countingAuth{ident: auth.Identity{ID: 7, Username: "u7", Role: "user", TeamID: 42}}
	h := WithAccessLogOptions(
		AuthMiddleware(okHandler(http.StatusOK, "不应到达"), authn),
		AccessLogOptions{Sink: sink, Auth: authn},
	)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/config", nil)
	req.Header.Set("Authorization", "Bearer whatever")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("状态 = %d，期望 403", rec.Code)
	}
	if got := authn.count(); got != 1 {
		t.Fatalf("403 场景 Authenticate 调用 %d 次，期望 1 次（admin gate 前已回填身份）", got)
	}
	e := singleEntry(t, sink)
	if e.Level != "warn" || e.UserID != 7 || e.TeamID != 42 {
		t.Fatalf("403 归属异常：level=%s user=%d team=%d", e.Level, e.UserID, e.TeamID)
	}
}

func TestAccessLogUnauthorizedDoesNotRequeryUser(t *testing.T) {
	sink := &accessCaptureSink{}
	authn := &countingAuth{err: errors.New("bad token")}
	h := WithAccessLogOptions(
		AuthMiddleware(okHandler(http.StatusOK, "不应到达"), authn),
		AccessLogOptions{Sink: sink, Auth: authn},
	)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/worklog/logs", nil)
	req.Header.Set("Authorization", "Bearer bad")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("状态 = %d，期望 401", rec.Code)
	}
	if got := authn.count(); got != 1 {
		t.Fatalf("401 场景 Authenticate 调用 %d 次，期望 1 次（不重复验签查用户）", got)
	}
	e := singleEntry(t, sink)
	if e.Level != "info" || e.UserID != 0 || e.TeamID != 0 {
		t.Fatalf("401 应记匿名 info：level=%s user=%d team=%d", e.Level, e.UserID, e.TeamID)
	}
}

func TestWithAccessLogOptionsInjectedAuthWithoutDB(t *testing.T) {
	sink := &accessCaptureSink{}
	authn := &countingAuth{ident: auth.Identity{ID: 5, Username: "zhang", Role: "user", TeamID: 42}}
	h := WithAccessLogOptions(okHandler(http.StatusOK, ""), AccessLogOptions{Sink: sink, Auth: authn})
	req := httptest.NewRequest(http.MethodGet, "/api/v1/worklog/logs", nil)
	req.Header.Set("Authorization", "Bearer whatever")
	h.ServeHTTP(httptest.NewRecorder(), req)

	if got := authn.count(); got != 1 {
		t.Fatalf("注入的 Auth 应承担验签，实际调用 %d 次", got)
	}
	e := singleEntry(t, sink)
	if e.UserID != 5 || e.TeamID != 42 {
		t.Fatalf("归属 user=%d team=%d，期望 5/42", e.UserID, e.TeamID)
	}
}

func TestAccessLogDoesNotRequerySysUser(t *testing.T) {
	t.Setenv("JWT_SECRET", "test-secret")
	db, fake := newFakeDB(t)
	fake.onRows("FROM sys_user",
		[]string{"id", "username", "nickname", "role", "team_id"},
		[]driver.Value{int64(5), "zhang", "张三", "user", int64(42)})
	token, err := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"uid": 5, "username": "zhang", "exp": time.Now().Add(time.Hour).Unix(),
	}).SignedString([]byte("test-secret"))
	if err != nil {
		t.Fatalf("签发测试 JWT: %v", err)
	}
	authn := auth.NewService(auth.SQLUserStore{DB: db}, nil, "test-secret", 0, 0)
	// 最外层访问日志按旧签名构造，内层 AuthMiddleware 验签并回查一次 sys_user。
	h := WithAccessLog(AuthMiddleware(okHandler(http.StatusOK, ""), authn), db)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/worklog/logs", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	h.ServeHTTP(httptest.NewRecorder(), req)

	if n := countCalls(fake, "FROM sys_user"); n != 1 {
		t.Fatalf("sys_user 查询 %d 次，期望 1 次（访问日志不应重复回查）", n)
	}
	rows := fake.inserts()
	if len(rows) != 1 {
		t.Fatalf("app_log 写入 %d 条，期望 1 条", len(rows))
	}
	if got := rows[0].arg(4); got != int64(5) {
		t.Fatalf("user_id = %v，期望 5", got)
	}
	if got := rows[0].arg(11); got != int64(42) {
		t.Fatalf("team_id = %v，期望 42", got)
	}
}

func TestWithAccessLogOptionsPanicSinkKeepsResponse(t *testing.T) {
	h := WithAccessLogOptions(okHandler(http.StatusOK, "payload"), AccessLogOptions{Sink: panicSink{}})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/app/list", nil))
	if rec.Code != http.StatusOK || rec.Body.String() != "payload" {
		t.Fatalf("Sink panic 影响了响应：code=%d body=%q", rec.Code, rec.Body.String())
	}
}

func TestWithAccessLogOptionsHandlerPanicRethrows(t *testing.T) {
	sink := &accessCaptureSink{}
	h := WithAccessLogOptions(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("boom")
	}), AccessLogOptions{Sink: sink})
	defer func() {
		if rec := recover(); rec != "boom" {
			t.Fatalf("panic 未原样上抛：%v", rec)
		}
		e := singleEntry(t, sink)
		if e.Level != "error" {
			t.Fatalf("panic level = %q，期望 error", e.Level)
		}
		if !strings.Contains(e.Message, "500") {
			t.Fatalf("panic 状态应记 500：%s", e.Message)
		}
	}()
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/api/v1/app/list", nil))
	t.Fatal("panic 应继续上抛")
}

func TestWithAccessLogOptionsNilPassthrough(t *testing.T) {
	inner := okHandler(http.StatusTeapot, "spout")
	h := WithAccessLogOptions(inner, AccessLogOptions{})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/app/list", nil))
	if rec.Code != http.StatusTeapot || rec.Body.String() != "spout" {
		t.Fatalf("无 Sink/DB 时应原样透传：code=%d body=%q", rec.Code, rec.Body.String())
	}
}
