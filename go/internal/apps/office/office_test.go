package office

// 测试：票据签发/校验、config 签发（多源只读口径）、raw 回源、回调双校验与写回。
// fake 文档来源 / fake 网盘，不访问真实外部服务。

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"shade/internal/auth"
	"shade/internal/httpapi"
)

// fakeSource 只读文档来源桩。
type fakeSource struct {
	data []byte
	name string
	err  error
}

func (f *fakeSource) Fetch(context.Context, string) ([]byte, string, error) {
	return f.data, f.name, f.err
}

// fakeNetdisk 网盘来源桩（回源 + 写回记录）。
type fakeNetdisk struct {
	data     []byte
	written  [][]byte
	writeErr error
}

func (f *fakeNetdisk) Fetch(context.Context, NDP, string) ([]byte, string, error) {
	return f.data, "text/plain", nil
}

func (f *fakeNetdisk) WriteBack(_ context.Context, _ NDP, data []byte) error {
	f.written = append(f.written, data)
	return f.writeErr
}

// fixedCfg 配置桩。
type fixedCfg map[string]string

func (c fixedCfg) Get(_ context.Context, key string) (string, error) { return c[key], nil }

type fixedPermit map[string]bool

func (p fixedPermit) Allow(_ context.Context, _ int64, appKey string) (bool, error) {
	return p[appKey], nil
}

func newTestModule(t *testing.T) (*Module, *fakeNetdisk) {
	t.Helper()
	nd := &fakeNetdisk{data: []byte("doc-bytes")}
	m := &Module{
		Cfg: fixedCfg{
			"OO_PUBLIC_URL":    "https://oo.example.com",
			"OO_CALLBACK_BASE": "https://tool.example.com",
		},
		Sources: map[string]DocSource{
			"wls": &fakeSource{data: []byte("sheet-bytes"), name: "任务单.docx"},
		},
		Netdisk:      nd,
		Permit:       fixedPermit{"netdisk": true, "work-log": true, "safeday": true, "tree-hazard": true, "crossing": true},
		TicketSecret: "tk-secret",
		OOJWTSecret:  "oo-secret",
	}
	return m, nd
}

// fakeAuth 把固定身份注入 httpapi.AuthMiddleware 的上下文（与生产同一条装配路径）。
type fakeAuth struct{ ident auth.Identity }

func (f fakeAuth) Login(context.Context, string, string, string) (auth.LoginResult, error) {
	return auth.LoginResult{User: f.ident}, nil
}
func (f fakeAuth) Authenticate(context.Context, string) (auth.Identity, error) { return f.ident, nil }
func (f fakeAuth) Logout(context.Context, string, time.Time) error             { return nil }

func newRouter(m *Module) http.Handler {
	r := chi.NewRouter()
	r.Route("/api/v1", func(r chi.Router) { m.RegisterRoutes(r) })
	return httpapi.AuthMiddleware(r, fakeAuth{ident: auth.Identity{
		ID: 1, Username: "zhang", Nickname: "小张", Role: "user", TeamID: 1,
	}})
}

func TestDocTypeOf(t *testing.T) {
	if dt, edit := DocTypeOf("a.docx"); dt != "word" || !edit {
		t.Fatalf("docx 应可编辑 word: %q %v", dt, edit)
	}
	if dt, edit := DocTypeOf("b.wps"); dt != "word" || edit {
		t.Fatalf("wps 应仅预览: %q %v", dt, edit)
	}
	if dt, _ := DocTypeOf("c.xyz"); dt != "" {
		t.Fatalf("未知扩展应空: %q", dt)
	}
}

func TestTicketSignVerify(t *testing.T) {
	m, _ := newTestModule(t)
	p := TicketPayload{Src: "ndp", Name: "a.docx", NDP: &NDP{Space: "my", User: "u1", Path: "/a.docx"}}
	token, err := m.SignTicket(context.Background(), p, 60)
	if err != nil {
		t.Fatalf("签发: %v", err)
	}
	got, err := m.VerifyTicket(context.Background(), token)
	if err != nil || got.Src != "ndp" || got.NDP == nil || got.NDP.Path != "/a.docx" {
		t.Fatalf("校验: %+v %v", got, err)
	}
	if _, err := m.VerifyTicket(context.Background(), token[:len(token)-2]+"xx"); err == nil {
		t.Fatalf("篡改应拒绝")
	}
	m.TicketSecret = "other"
	if _, err := m.VerifyTicket(context.Background(), token); err == nil {
		t.Fatalf("换密钥应拒绝")
	}
}

func TestConfigViewOnlySource(t *testing.T) {
	m, _ := newTestModule(t)
	h := newRouter(m)

	// 只读源（wls）：mode=view、无 callbackUrl。
	rec := doJSON(t, h, "/api/v1/office/config", map[string]any{
		"src": "wls", "ref": "2026-10", "name": "任务单.docx",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("config 失败: %d %s", rec.Code, rec.Body)
	}
	cfg := extractConfig(t, rec)
	editor := cfg["editorConfig"].(map[string]any)
	if editor["mode"] != "view" {
		t.Fatalf("只读源应 mode=view: %+v", editor)
	}
	if _, has := editor["callbackUrl"]; has {
		t.Fatalf("只读源不应有 callbackUrl")
	}

	// 网盘源：mode=edit + callbackUrl + 回源 URL 带票据。
	rec = doJSON(t, h, "/api/v1/office/config", map[string]any{
		"src": "ndp", "space": "my", "path": "/docs/a.docx", "name": "a.docx",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("config 失败: %d %s", rec.Code, rec.Body)
	}
	cfg = extractConfig(t, rec)
	editor = cfg["editorConfig"].(map[string]any)
	if editor["mode"] != "edit" {
		t.Fatalf("网盘源应 mode=edit: %+v", editor)
	}
	if _, has := editor["callbackUrl"]; !has {
		t.Fatalf("网盘源应有 callbackUrl")
	}
	doc := cfg["document"].(map[string]any)
	url, _ := doc["url"].(string)
	if !strings.Contains(url, "/api/v1/office/raw/") {
		t.Fatalf("回源 URL 错误: %q", url)
	}

	// 未知来源。
	rec = doJSON(t, h, "/api/v1/office/config", map[string]any{"src": "xxx", "name": "a.docx"})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("未知来源应 400: %d", rec.Code)
	}

	// 无 netdisk 权限。
	m.Permit = fixedPermit{"netdisk": false}
	rec = doJSON(t, h, "/api/v1/office/config", map[string]any{
		"src": "ndp", "space": "my", "path": "/a.docx", "name": "a.docx",
	})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("无权限应 403: %d", rec.Code)
	}
}

func TestRawAndCallback(t *testing.T) {
	m, nd := newTestModule(t)
	h := newRouter(m)

	// 只读源回源（票据免登录）。
	tk, _ := m.SignTicket(context.Background(), TicketPayload{Src: "wls", Ref: "r1", Name: "任务单.docx"}, 60)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/office/raw/"+tk, nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || rec.Body.String() != "sheet-bytes" {
		t.Fatalf("raw 回源失败: %d %q", rec.Code, rec.Body)
	}

	// 坏票据拒。
	req = httptest.NewRequest(http.MethodGet, "/api/v1/office/raw/bad-ticket", nil)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("坏票据应 401: %d", rec.Code)
	}

	// 回调：OnlyOffice JWT + 票据双校验（载荷须含数值型 status），status=4 关闭无改动 → {"error":0}。
	p := TicketPayload{Src: "ndp", Name: "a.docx", NDP: &NDP{Space: "my", User: "u1", Path: "/a.docx"}}
	cbTk, _ := m.SignTicket(context.Background(), p, 60)
	ooToken, err := m.SignOOJWT(context.Background(), map[string]any{"status": 4, "url": ""})
	if err != nil {
		t.Fatalf("OO JWT 签发: %v", err)
	}
	req = httptest.NewRequest(http.MethodPost, "/api/v1/office/callback/"+cbTk, bytes.NewReader([]byte(`{}`)))
	req.Header.Set("Authorization", "Bearer "+ooToken)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Body.String() != `{"error":0}` {
		t.Fatalf("无改动回调应 {"+"error"+":0}: %s", rec.Body)
	}

	// status=2 保存：产物地址不通 → 写回失败 200 {"error":1,"message":"写回网盘失败"}（OO 约定格式）。
	ooToken2, _ := m.SignOOJWT(context.Background(), map[string]any{"status": 2, "url": "https://oo.internal/save/a.docx"})
	req = httptest.NewRequest(http.MethodPost, "/api/v1/office/callback/"+cbTk, bytes.NewReader([]byte(`{}`)))
	req.Header.Set("Authorization", "Bearer "+ooToken2)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if !strings.Contains(rec.Body.String(), `"error":1`) || !strings.Contains(rec.Body.String(), "写回网盘失败") {
		t.Fatalf("写回失败应 {"+"error"+":1}+message: %s", rec.Body)
	}
	if len(nd.written) != 0 {
		t.Fatalf("产物地址不通不应写回")
	}

	// 只读源回调不写回、应答 error:0。
	vTk, _ := m.SignTicket(context.Background(), TicketPayload{Src: "wls", Ref: "r1"}, 60)
	req = httptest.NewRequest(http.MethodPost, "/api/v1/office/callback/"+vTk, bytes.NewReader([]byte(`{}`)))
	req.Header.Set("Authorization", "Bearer "+ooToken)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Body.String() != `{"error":0}` {
		t.Fatalf("只读源回调应 {"+"error"+":0}: %s", rec.Body)
	}
	if len(nd.written) != 0 {
		t.Fatalf("只读源不应写回")
	}

	// JWT 载荷缺数值型 status → 401 签名校验失败（Node 口径：回调体必须在 JWT 内）。
	ooBad, _ := m.SignOOJWT(context.Background(), map[string]any{"payload": "save"})
	req = httptest.NewRequest(http.MethodPost, "/api/v1/office/callback/"+cbTk, bytes.NewReader([]byte(`{"status":2}`)))
	req.Header.Set("Authorization", "Bearer "+ooBad)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized || !strings.Contains(rec.Body.String(), "回调签名校验失败") {
		t.Fatalf("缺 status 应 401: %d %s", rec.Code, rec.Body)
	}

	// 无 JWT（双校验不过）→ 401，即便 body 自带 status 也不采信。
	req = httptest.NewRequest(http.MethodPost, "/api/v1/office/callback/"+cbTk, bytes.NewReader([]byte(`{"status":2}`)))
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("无 JWT 应 401: %d %s", rec.Code, rec.Body)
	}

	// 坏票据 + 合法 JWT → 401 票据无效。
	req = httptest.NewRequest(http.MethodPost, "/api/v1/office/callback/bad-ticket", bytes.NewReader([]byte(`{}`)))
	req.Header.Set("Authorization", "Bearer "+ooToken)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized || !strings.Contains(rec.Body.String(), "回调票据无效") {
		t.Fatalf("坏票据应 401: %d %s", rec.Code, rec.Body)
	}

	// OO_JWT_SECRET 未配置 → 回调一律拒（Node：无密钥时 body 恒空，401）。
	m.OOJWTSecret = ""
	vTk2, _ := m.SignTicket(context.Background(), TicketPayload{Src: "wls", Ref: "r1"}, 60)
	req = httptest.NewRequest(http.MethodPost, "/api/v1/office/callback/"+vTk2, bytes.NewReader([]byte(`{"status":1}`)))
	req.Header.Set("Authorization", "Bearer "+ooToken)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("未配置 OO_JWT_SECRET 应 401: %d %s", rec.Code, rec.Body)
	}
}

// TestCallbackPayloadInJWT 回调体在 JWT 内（v10 头位 token 包一层 {payload}）时也应取到 status。
func TestCallbackPayloadInJWT(t *testing.T) {
	m := New(fixedCfg{
		"OO_PUBLIC_URL":    "https://oo.example",
		"OO_CALLBACK_BASE": "http://shade",
		"OO_JWT_SECRET":    "oo-secret",
	})
	m.OOJWTSecret = "oo-secret"
	nd := &fakeNetdisk{}
	m.Netdisk = nd
	r := chi.NewRouter()
	r.Route("/api/v1", func(r chi.Router) { m.RegisterRoutes(r) })
	p := TicketPayload{Src: "ndp", Name: "a.docx", NDP: &NDP{Space: "my", User: "u1", Path: "/a.docx"}}
	cbTk, _ := m.SignTicket(context.Background(), p, 60)
	// 头位 token：载荷包一层 {payload: 回调体}（DocsCoServer.js sendServerRequest 口径）。
	ooToken, err := m.SignOOJWT(context.Background(), map[string]any{
		"payload": map[string]any{"status": 4, "url": ""},
	})
	if err != nil {
		t.Fatalf("OO JWT 签发: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/office/callback/"+cbTk, bytes.NewReader([]byte(`{}`)))
	req.Header.Set("Authorization", "Bearer "+ooToken)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	// status=4 关闭无改动：不应写回、应答 {"error":0}（若 status 取不到会走 rawBody 解析得 0，
	// 同样不写回；本用例锁定「JWT 载荷被消费」——用 status=2 + 假 url 验证取数路径）。
	if rec.Body.String() != `{"error":0}` {
		t.Fatalf("包层载荷回调应答错误: %s", rec.Body)
	}

	ooToken2, _ := m.SignOOJWT(context.Background(), map[string]any{
		"payload": map[string]any{"status": 2, "url": "http://127.0.0.1:1/save"},
	})
	req2 := httptest.NewRequest(http.MethodPost, "/api/v1/office/callback/"+cbTk, bytes.NewReader([]byte(`{}`)))
	req2.Header.Set("Authorization", "Bearer "+ooToken2)
	rec2 := httptest.NewRecorder()
	r.ServeHTTP(rec2, req2)
	// status=2 从 JWT 载荷取到（url 不通 → error:1）；若仍解 rawBody 会得 status=0 → error:0。
	if !strings.Contains(rec2.Body.String(), `"error":1`) {
		t.Fatalf("JWT 内 status=2 未被消费（写回路径未触发）: %s", rec2.Body)
	}
	if len(nd.written) == 0 {
		// http.Get 失败时 written 为空也算通过（本用例只验证 status 取数路径）。
		t.Log("保存产物地址不通，写回未发生（预期于离线测试环境）")
	}
}

// doJSON 发 JSON 请求（带固定身份头：模块鉴权走 httpapi 上下文，未注入时 /config 需要登录 —— 此处
// 直接绕过 AuthMiddleware 由路由处理，身份为空时 Permit 放行仍可测签发主流程）。
func doJSON(t *testing.T, h http.Handler, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	raw, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func extractConfig(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var env Envelope
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("解析信封: %v", err)
	}
	data := env.Data.(map[string]any)
	return data["config"].(map[string]any)
}

// ---- 契约补齐：/config 校验与 docCfg 形状、/raw 响应头、回调写回成功路径 ----

// TestConfigRequiresOOJWTSecret OO_JWT_SECRET 缺失时拒发配置（Node 口径 503）。
func TestConfigRequiresOOJWTSecret(t *testing.T) {
	m, _ := newTestModule(t)
	m.OOJWTSecret = ""
	m.Cfg = fixedCfg{"OO_PUBLIC_URL": "https://oo.example.com", "OO_CALLBACK_BASE": "https://tool.example.com"}
	h := newRouter(m)
	rec := doJSON(t, h, "/api/v1/office/config", map[string]any{
		"src": "ndp", "space": "my", "path": "/a.docx", "name": "a.docx",
	})
	if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), "OO_JWT_SECRET") {
		t.Fatalf("缺 OO_JWT_SECRET 应 503: %d %s", rec.Code, rec.Body)
	}
}

// TestConfigDocShape docCfg 形状对齐 Node：编辑态 permissions review/comment、forcesave；
// 只读态无 forcesave/无 callbackUrl。
func TestConfigDocShape(t *testing.T) {
	m, _ := newTestModule(t)
	h := newRouter(m)

	// 网盘编辑态。
	rec := doJSON(t, h, "/api/v1/office/config", map[string]any{
		"src": "ndp", "space": "my", "path": "/docs/a.docx", "name": "a.docx",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("config 失败: %d %s", rec.Code, rec.Body)
	}
	cfg := extractConfig(t, rec)
	doc := cfg["document"].(map[string]any)
	perm := doc["permissions"].(map[string]any)
	if perm["edit"] != true || perm["review"] != true || perm["comment"] != true {
		t.Fatalf("编辑态权限错误: %+v", perm)
	}
	editor := cfg["editorConfig"].(map[string]any)
	cust := editor["customization"].(map[string]any)
	if cust["forcesave"] != true {
		t.Fatalf("编辑态应 forcesave: %+v", cust)
	}
	if _, has := cust["chat"]; has {
		t.Fatalf("customization 不应有 chat 键: %+v", cust)
	}

	// view 模式：只读、无 callbackUrl、无 forcesave。
	rec = doJSON(t, h, "/api/v1/office/config", map[string]any{
		"src": "ndp", "space": "my", "path": "/docs/a.docx", "name": "a.docx", "mode": "view",
	})
	cfg = extractConfig(t, rec)
	doc = cfg["document"].(map[string]any)
	perm = doc["permissions"].(map[string]any)
	if perm["edit"] != false || perm["review"] != false || perm["comment"] != false {
		t.Fatalf("只读态权限错误: %+v", perm)
	}
	editor = cfg["editorConfig"].(map[string]any)
	cust = editor["customization"].(map[string]any)
	if _, has := cust["forcesave"]; has {
		t.Fatalf("只读态不应有 forcesave: %+v", cust)
	}
	if _, has := editor["callbackUrl"]; has {
		t.Fatalf("只读态不应有 callbackUrl")
	}

	// 未知扩展 → 400 40030 该格式不支持在线打开。
	rec = doJSON(t, h, "/api/v1/office/config", map[string]any{
		"src": "ndp", "space": "my", "path": "/a.xyz", "name": "a.xyz",
	})
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "该格式不支持在线打开") {
		t.Fatalf("未知格式应 400: %d %s", rec.Code, rec.Body)
	}
}

// TestRawHeaders /raw 成功响应头：Content-Type 按 RAW_MIME 给准、filename* 定名取票据载荷。
func TestRawHeaders(t *testing.T) {
	m, _ := newTestModule(t)
	h := newRouter(m)
	tk, _ := m.SignTicket(context.Background(), TicketPayload{Src: "wls", Ref: "r1", Name: "任务单.docx"}, 60)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/office/raw/"+tk+"/other-name.bin", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("raw 失败: %d %s", rec.Code, rec.Body)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/vnd.openxmlformats-officedocument.wordprocessingml.document" {
		t.Fatalf("Content-Type 错误: %s", ct)
	}
	if cd := rec.Header().Get("Content-Disposition"); !strings.Contains(cd, "filename*=UTF-8''") {
		t.Fatalf("Content-Disposition 错误: %s", cd)
	}

	// 未知来源票据 → 401 纯文本。
	bad, _ := m.SignTicket(context.Background(), TicketPayload{Src: "xxx", Name: "a.docx"}, 60)
	req = httptest.NewRequest(http.MethodGet, "/api/v1/office/raw/"+bad, nil)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized || !strings.Contains(rec.Body.String(), "回源票据无效") {
		t.Fatalf("未知来源应 401: %d %s", rec.Code, rec.Body)
	}
}

// TestCallbackWriteBackSuccess 回调写回成功路径：OO_INTERNAL_URL 改写拉回产物 → 写回 → {"error":0}。
func TestCallbackWriteBackSuccess(t *testing.T) {
	ooInternal := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("SAVED-DOC"))
	}))
	t.Cleanup(ooInternal.Close)

	m, nd := newTestModule(t)
	m.Cfg = fixedCfg{
		"OO_PUBLIC_URL":    "https://oo.example.com",
		"OO_CALLBACK_BASE": "https://tool.example.com",
		"OO_INTERNAL_URL":  ooInternal.URL,
	}
	h := newRouter(m)

	p := TicketPayload{Src: "ndp", Name: "a.docx", NDP: &NDP{Space: "my", User: "u1", Path: "/a.docx"}}
	cbTk, _ := m.SignTicket(context.Background(), p, 60)
	ooToken, _ := m.SignOOJWT(context.Background(), map[string]any{"status": 2, "url": "http://oo-container/internal/save/a.docx"})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/office/callback/"+cbTk, bytes.NewReader([]byte(`{}`)))
	req.Header.Set("Authorization", "Bearer "+ooToken)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Body.String() != `{"error":0}` {
		t.Fatalf("写回成功应 {"+"error"+":0}: %s", rec.Body)
	}
	if len(nd.written) != 1 || string(nd.written[0]) != "SAVED-DOC" {
		t.Fatalf("写回内容错误: %q", nd.written)
	}

	// status=2 但缺 url → 写回失败 {"error":1,"message"}。
	ooToken2, _ := m.SignOOJWT(context.Background(), map[string]any{"status": 2})
	req = httptest.NewRequest(http.MethodPost, "/api/v1/office/callback/"+cbTk, bytes.NewReader([]byte(`{}`)))
	req.Header.Set("Authorization", "Bearer "+ooToken2)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if !strings.Contains(rec.Body.String(), `"error":1`) || !strings.Contains(rec.Body.String(), "写回网盘失败") {
		t.Fatalf("缺 url 应写回失败: %s", rec.Body)
	}
}
