package httpapi

import (
	"context"
	"database/sql"
	"database/sql/driver"
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

	"github.com/golang-jwt/jwt/v5"

	"shade/internal/auth"
	"shade/internal/logging"
)

// ---- database/sql 假驱动（纯标准库；不连真实库）----

type fakeResult int64

func (r fakeResult) LastInsertId() (int64, error) { return int64(r), nil }
func (r fakeResult) RowsAffected() (int64, error) { return 1, nil }

type fakeCall struct {
	query string
	args  []driver.Value
}

func (c fakeCall) arg(i int) driver.Value {
	if i < 0 || i >= len(c.args) {
		return nil
	}
	return c.args[i]
}

func (c fakeCall) text() string {
	parts := make([]string, len(c.args))
	for i, a := range c.args {
		parts[i] = fmt.Sprint(a)
	}
	return strings.Join(parts, "|")
}

type fakeQueryRoute struct {
	match string
	cols  []string
	rows  [][]driver.Value
}

type fakeDB struct {
	mu      sync.Mutex
	calls   []fakeCall
	queries []fakeQueryRoute
	execErr error
}

// onRows 注册 query 响应（match 为 SQL 子串，可多次调用排队）。
func (d *fakeDB) onRows(match string, cols []string, rows ...[]driver.Value) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.queries = append(d.queries, fakeQueryRoute{match: match, cols: cols, rows: rows})
}

// inserts 取 app_log 写入记录。
func (d *fakeDB) inserts() []fakeCall {
	d.mu.Lock()
	defer d.mu.Unlock()
	var out []fakeCall
	for _, c := range d.calls {
		if strings.Contains(c.query, "INSERT INTO app_log") {
			out = append(out, c)
		}
	}
	return out
}

type fakeConn struct{ db *fakeDB }

func (c *fakeConn) Prepare(string) (driver.Stmt, error) { return nil, errors.New("unsupported") }
func (c *fakeConn) Close() error                        { return nil }
func (c *fakeConn) Begin() (driver.Tx, error)           { return nil, errors.New("unsupported") }

func (c *fakeConn) ExecContext(_ context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	vals := make([]driver.Value, len(args))
	for i, a := range args {
		vals[i] = a.Value
	}
	c.db.mu.Lock()
	defer c.db.mu.Unlock()
	c.db.calls = append(c.db.calls, fakeCall{query: query, args: vals})
	if c.db.execErr != nil {
		return nil, c.db.execErr
	}
	return fakeResult(1), nil
}

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

func (c *fakeConn) QueryContext(_ context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	vals := make([]driver.Value, len(args))
	for i, a := range args {
		vals[i] = a.Value
	}
	c.db.mu.Lock()
	defer c.db.mu.Unlock()
	c.db.calls = append(c.db.calls, fakeCall{query: query, args: vals})
	for i := range c.db.queries {
		if strings.Contains(query, c.db.queries[i].match) {
			return &fakeRows{cols: c.db.queries[i].cols, data: c.db.queries[i].rows}, nil
		}
	}
	return &fakeRows{cols: []string{"id"}, data: nil}, nil
}

type fakeDriver struct{ db *fakeDB }

func (d fakeDriver) Open(string) (driver.Conn, error) { return &fakeConn{db: d.db}, nil }

var fakeSeq atomic.Int64

func newFakeDB(t *testing.T) (*sql.DB, *fakeDB) {
	t.Helper()
	f := &fakeDB{}
	name := fmt.Sprintf("accesslog-fake-%d", fakeSeq.Add(1))
	sql.Register(name, fakeDriver{db: f})
	db, err := sql.Open(name, "")
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db, f
}

// okHandler 返回固定状态码的测试处理器。
func okHandler(status int, body string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	})
}

// ---- 纯函数 ----

func TestRouteAppCode(t *testing.T) {
	cases := map[string]string{
		"/api/v1/auth/login":          "system",
		"/api/v1/app/list":            "system",
		"/api/v1/user/profile":        "system",
		"/api/v1/admin/config":        "system",
		"/api/v1/notice/list":         "system",
		"/api/v1/app-logs":            "system",
		"/api/v1/worklog/logs/12":     "work-log",
		"/api/v1/teamdata/towers":     "team-data",
		"/api/v1/treehazard/ingest":   "tree-hazard",
		"/api/v1/crossing/records":    "crossing",
		"/api/v1/netdisk/download":    "netdisk",
		"/api/v1/quiz/practice":       "quiz",
		"/api/v1/callme/chat":         "call-me",
		"/api/v1/safeday/records":     "safe-day",
		"/api/v1/kvm/devices":         "kvm",
		"/api/v1/wmadd/render":        "wm-add",
		"/api/v1/sgcc/tasks":          "sgcc-clockin",
		"/api/v1/wechatanswer/rules":  "wechat-answer",
		"/api/v1/wechat-answer/rules": "wechat-answer",
		"/api/v1/office/config":       "office",
		"/api/v1/chatlog/ingest":      "chat-log",
		"/api/v1/unknown/thing":       "system",
	}
	for path, want := range cases {
		if got := routeAppCode(path); got != want {
			t.Fatalf("routeAppCode(%q) = %q，期望 %q", path, got, want)
		}
	}
}

func TestAccessLevel(t *testing.T) {
	cases := map[int]string{
		200: "info", 302: "info",
		401: "info", // 未授权（未登录/登录过期）属正常噪声
		400: "warn", 403: "warn", 404: "warn", 429: "warn",
		500: "error", 502: "error",
	}
	for status, want := range cases {
		if got := accessLevel(status); got != want {
			t.Fatalf("accessLevel(%d) = %q，期望 %q", status, got, want)
		}
	}
}

func TestRedactQuery(t *testing.T) {
	q := map[string][]string{
		"token":   {"sekrit-tok"},
		"key":     {"sekrit-key"},
		"apikey":  {"sekrit-ak"},
		"keyword": {"hello"},
		"page":    {"2"},
	}
	out := redactQuery(q)
	if out["token"] != "****" || out["key"] != "****" || out["apikey"] != "****" {
		t.Fatalf("敏感参数未脱敏：%v", out)
	}
	if out["keyword"] != "hello" || out["page"] != "2" {
		t.Fatalf("普通参数被误伤：%v", out)
	}
}

// ---- 中间件 ----

func TestWithAccessLogRecordsAPIRequest(t *testing.T) {
	db, fake := newFakeDB(t)
	h := WithAccessLog(okHandler(http.StatusOK, "ok"), db)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/worklog/logs?page=1", nil))

	if rec.Code != http.StatusOK || rec.Body.String() != "ok" {
		t.Fatalf("响应被改动：code=%d body=%q", rec.Code, rec.Body.String())
	}
	rows := fake.inserts()
	if len(rows) != 1 {
		t.Fatalf("app_log 写入 %d 条，期望 1 条", len(rows))
	}
	row := rows[0]
	if got := fmt.Sprint(row.arg(0)); got != "work-log" {
		t.Fatalf("app_code = %q", got)
	}
	if got := fmt.Sprint(row.arg(1)); got != "info" {
		t.Fatalf("level = %q", got)
	}
	trace := fmt.Sprint(row.arg(3))
	if len(trace) != 32 {
		t.Fatalf("trace_id = %q", trace)
	}
	if got := rec.Header().Get("X-Trace-Id"); got != trace {
		t.Fatalf("响应头 trace = %q，行内 = %q", got, trace)
	}
	text := row.text()
	for _, want := range []string{"GET", "/api/v1/worklog/logs", "http.request", "access", "访问成功"} {
		if !strings.Contains(text, want) {
			t.Fatalf("日志行缺 %q：%s", want, text)
		}
	}
}

func TestWithAccessLogLevelByStatus(t *testing.T) {
	cases := map[int]string{
		200: "info", 401: "info", 400: "warn", 404: "warn", 500: "error", 503: "error",
	}
	for status, want := range cases {
		db, fake := newFakeDB(t)
		h := WithAccessLog(okHandler(status, ""), db)
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/api/v1/quiz/meta", nil))
		rows := fake.inserts()
		if len(rows) != 1 {
			t.Fatalf("status=%d 写入 %d 条", status, len(rows))
		}
		if got := fmt.Sprint(rows[0].arg(1)); got != want {
			t.Fatalf("status=%d level=%q，期望 %q", status, got, want)
		}
	}
}

func TestWithAccessLogSkipsHealthzAndNonAPI(t *testing.T) {
	db, fake := newFakeDB(t)
	h := WithAccessLog(okHandler(http.StatusOK, ""), db)
	for _, path := range []string{"/healthz", "/index.html", "/assets/theme.css", "/api/v1/healthz"} {
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, path, nil))
	}
	if n := len(fake.inserts()); n != 0 {
		t.Fatalf("非业务路径写入 %d 条，期望 0", n)
	}
}

func TestWithAccessLogRedactsSecrets(t *testing.T) {
	db, fake := newFakeDB(t)
	h := WithAccessLog(okHandler(http.StatusOK, ""), db)
	req := httptest.NewRequest(http.MethodGet,
		"/api/v1/netdisk/download?token=sekrit-tok&key=sekrit-key&keyword=hello", nil)
	req.Header.Set("Authorization", "Bearer sekrit-bearer")
	h.ServeHTTP(httptest.NewRecorder(), req)

	rows := fake.inserts()
	if len(rows) != 1 {
		t.Fatalf("写入 %d 条，期望 1 条", len(rows))
	}
	text := rows[0].text()
	for _, secret := range []string{"sekrit-tok", "sekrit-key", "sekrit-bearer"} {
		if strings.Contains(text, secret) {
			t.Fatalf("秘密 %q 落库：%s", secret, text)
		}
	}
	detail := fmt.Sprint(rows[0].arg(5))
	if !strings.Contains(detail, `"token":"****"`) || !strings.Contains(detail, `"key":"****"`) {
		t.Fatalf("敏感 query 未脱敏：%s", detail)
	}
	if !strings.Contains(detail, `"keyword":"hello"`) {
		t.Fatalf("普通 query 被误伤：%s", detail)
	}
}

func TestWithAccessLogWriteFailureDoesNotAffectResponse(t *testing.T) {
	db, fake := newFakeDB(t)
	fake.mu.Lock()
	fake.execErr = errors.New("db down")
	fake.mu.Unlock()
	h := WithAccessLog(okHandler(http.StatusOK, "payload"), db)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/worklog/logs", strings.NewReader("{}")))
	if rec.Code != http.StatusOK || rec.Body.String() != "payload" {
		t.Fatalf("落库失败影响了响应：code=%d body=%q", rec.Code, rec.Body.String())
	}
}

// stubAuth 测试鉴权桩：恒返回固定身份。
type stubAuth struct{ ident auth.Identity }

func (s stubAuth) Login(context.Context, string, string, string) (auth.LoginResult, error) {
	return auth.LoginResult{User: s.ident}, nil
}

func (s stubAuth) Authenticate(context.Context, string) (auth.Identity, error) { return s.ident, nil }

func (s stubAuth) Logout(context.Context, string, time.Time) error { return nil }

func TestWithAccessLogIdentityFromContext(t *testing.T) {
	db, fake := newFakeDB(t)
	ident := auth.Identity{ID: 7, Username: "u7", Role: "user", TeamID: 42}
	// 内层挂载（AuthMiddleware 注入 identity 后经过中间件）。
	h := AuthMiddleware(WithAccessLog(okHandler(http.StatusOK, ""), db), stubAuth{ident: ident})
	req := httptest.NewRequest(http.MethodGet, "/api/v1/worklog/logs", nil)
	req.Header.Set("Authorization", "Bearer whatever")
	h.ServeHTTP(httptest.NewRecorder(), req)

	rows := fake.inserts()
	if len(rows) != 1 {
		t.Fatalf("写入 %d 条", len(rows))
	}
	row := rows[0]
	if got := fmt.Sprint(row.arg(4)); got != "7" {
		t.Fatalf("user_id = %q", got)
	}
	if got := fmt.Sprint(row.arg(11)); got != "42" {
		t.Fatalf("team_id = %q", got)
	}
	if !strings.Contains(row.text(), "uid=7") || !strings.Contains(row.text(), "team=42") {
		t.Fatalf("原文缺用户/班组：%s", row.text())
	}
	if !strings.Contains(fmt.Sprint(row.arg(5)), `"username":"u7"`) {
		t.Fatalf("detail 缺 username：%s", row.arg(5))
	}
}

func TestWithAccessLogTokenFallback(t *testing.T) {
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
	// 最外层挂载（AuthMiddleware 在内层，identity 不在本中间件 ctx）→ token 验签回查。
	h := WithAccessLog(okHandler(http.StatusOK, ""), db)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/worklog/logs", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	h.ServeHTTP(httptest.NewRecorder(), req)

	rows := fake.inserts()
	if len(rows) != 1 {
		t.Fatalf("写入 %d 条", len(rows))
	}
	row := rows[0]
	if got := fmt.Sprint(row.arg(4)); got != "5" {
		t.Fatalf("user_id = %q", got)
	}
	if got := fmt.Sprint(row.arg(11)); got != "42" {
		t.Fatalf("team_id = %q", got)
	}
}

func TestWithAccessLogInvalidTokenAnonymous(t *testing.T) {
	t.Setenv("JWT_SECRET", "test-secret")
	db, fake := newFakeDB(t)
	h := WithAccessLog(okHandler(http.StatusOK, ""), db)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/worklog/logs", nil)
	req.Header.Set("Authorization", "Bearer not-a-jwt")
	h.ServeHTTP(httptest.NewRecorder(), req)

	rows := fake.inserts()
	if len(rows) != 1 {
		t.Fatalf("写入 %d 条", len(rows))
	}
	if got := fmt.Sprint(rows[0].arg(4)); got != "0" {
		t.Fatalf("无效 token 应匿名，user_id = %q", got)
	}
}

func TestWithAccessLogTracePropagation(t *testing.T) {
	// 入站 X-Trace-Id 合法则沿用
	db, fake := newFakeDB(t)
	h := WithAccessLog(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := traceFromRequest(r); got != "in-trace.1" {
			t.Errorf("请求 ctx trace = %q", got)
		}
		w.WriteHeader(http.StatusOK)
	}), db)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/app/list", nil)
	req.Header.Set("X-Trace-Id", "in-trace.1")
	h.ServeHTTP(rec, req)
	if got := rec.Header().Get("X-Trace-Id"); got != "in-trace.1" {
		t.Fatalf("响应头 trace = %q", got)
	}
	if got := fmt.Sprint(fake.inserts()[0].arg(3)); got != "in-trace.1" {
		t.Fatalf("行内 trace = %q", got)
	}

	// 非法入站值回退为新生成
	db2, fake2 := newFakeDB(t)
	h2 := WithAccessLog(okHandler(http.StatusOK, ""), db2)
	req2 := httptest.NewRequest(http.MethodGet, "/api/v1/app/list", nil)
	req2.Header.Set("X-Trace-Id", "bad id!")
	h2.ServeHTTP(httptest.NewRecorder(), req2)
	got := fmt.Sprint(fake2.inserts()[0].arg(3))
	if len(got) != 32 {
		t.Fatalf("回退 trace = %q", got)
	}
}

// traceFromRequest 测试探针：读请求 ctx 中的链路 ID。
func traceFromRequest(r *http.Request) string {
	return logging.TraceIDFromContext(r.Context())
}

func TestWithAccessLogPanicLogsAndRethrows(t *testing.T) {
	db, fake := newFakeDB(t)
	h := WithAccessLog(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("boom")
	}), db)
	defer func() {
		rec := recover()
		if rec != "boom" {
			t.Fatalf("panic 未上抛：%v", rec)
		}
		rows := fake.inserts()
		if len(rows) != 1 {
			t.Fatalf("panic 场景写入 %d 条", len(rows))
		}
		if got := fmt.Sprint(rows[0].arg(1)); got != "error" {
			t.Fatalf("panic level = %q", got)
		}
		if !strings.Contains(rows[0].text(), "500") {
			t.Fatalf("panic 状态应记 500：%s", rows[0].text())
		}
	}()
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/api/v1/app/list", nil))
	t.Fatal("panic 应继续上抛")
}

func TestWithAccessLogNilDBPassthrough(t *testing.T) {
	inner := okHandler(http.StatusTeapot, "spout")
	h := WithAccessLog(inner, nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/app/list", nil))
	if rec.Code != http.StatusTeapot || rec.Body.String() != "spout" {
		t.Fatalf("nil db 透传异常：code=%d body=%q", rec.Code, rec.Body.String())
	}
}
