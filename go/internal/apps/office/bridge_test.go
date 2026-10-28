package office

// 桥与扁平参数契约测试：office.html query 透传口径、五源桥回源、网盘桥与文档 key；
// fake 能力桩，不访问真实 OpenList / OnlyOffice。

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"shade/internal/auth"
)

// fakeSheet wls 能力桩。
type fakeSheet struct {
	name string
	data []byte
	err  error
	got  WlsRef
}

func (f *fakeSheet) Resolve(_ context.Context, _ auth.Identity, ref WlsRef) (string, error) {
	f.got = ref
	if f.err != nil {
		return "", f.err
	}
	return f.name, nil
}

func (f *fakeSheet) Fetch(_ context.Context, ref WlsRef) ([]byte, error) {
	f.got = ref
	if f.err != nil {
		return nil, f.err
	}
	return f.data, nil
}

// fakeSafeday sdr 能力桩。
type fakeSafeday struct {
	name string
	data []byte
	err  error
}

func (f *fakeSafeday) Resolve(context.Context, auth.Identity, SdrRef) (string, error) {
	return f.name, f.err
}
func (f *fakeSafeday) Fetch(context.Context, SdrRef) ([]byte, error) { return f.data, f.err }

// fakeExport thd / crs 能力桩。
type fakeExport struct {
	name string
	data []byte
	got  LineRef
}

func (f *fakeExport) Resolve(_ context.Context, _ auth.Identity, ref LineRef) (string, error) {
	f.got = ref
	return f.name, nil
}
func (f *fakeExport) Fetch(_ context.Context, ref LineRef) ([]byte, error) {
	f.got = ref
	return f.data, nil
}

// fakeNetdiskBridge 网盘能力桩（office.NetdiskBridge）。
type fakeNetdiskBridge struct {
	data    []byte
	ct      string
	key     string
	written []byte
	gotPath string
	readErr error
}

func (f *fakeNetdiskBridge) ReadFileBytes(_ context.Context, _, _ string, _ int64, relPath string) ([]byte, string, error) {
	f.gotPath = relPath
	if f.readErr != nil {
		return nil, "", f.readErr
	}
	return f.data, f.ct, nil
}
func (f *fakeNetdiskBridge) WriteFileBytes(_ context.Context, _, _ string, _ int64, relPath string, data []byte) error {
	f.gotPath = relPath
	f.written = data
	return nil
}
func (f *fakeNetdiskBridge) DocKey(_ context.Context, _, _ string, _ int64, relPath string) (string, error) {
	f.gotPath = relPath
	return f.key, nil
}

// rawGet 免登录回源（票据即鉴权）。
func rawGet(t *testing.T, h http.Handler, ticket string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/office/raw/"+ticket, nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestFlatParamsContract(t *testing.T) {
	m, _ := newTestModule(t)
	sheet := &fakeSheet{name: "工作任务单-2026-10-01至2026-10-31.docx", data: []byte("sheet")}
	m.Sources = NewSources(sheet, nil, nil, nil, nil)
	h := newRouter(m)

	// office.html 口径：query 值全字符串（team_id/force/split 含在内）。
	rec := doJSON(t, h, "/api/v1/office/config", map[string]any{
		"src": "worklog-sheet", "sheet": "task",
		"from": "2026-10-01", "to": "2026-10-31",
		"team_id": "5", "force": "1",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("config 失败: %d %s", rec.Code, rec.Body)
	}
	cfg := extractConfig(t, rec)
	doc := cfg["document"].(map[string]any)
	if doc["title"] != "工作任务单-2026-10-01至2026-10-31.docx" {
		t.Fatalf("定名应取桥 Resolve: %v", doc["title"])
	}
	if sheet.got.Sheet != "task" || sheet.got.Team != 5 || !sheet.got.Force {
		t.Fatalf("扁平参数未正确组装 ref: %+v", sheet.got)
	}
	editor := cfg["editorConfig"].(map[string]any)
	if editor["mode"] != "view" {
		t.Fatalf("wls 应只读: %v", editor["mode"])
	}

	// 日期范围不合法 → 400。
	rec = doJSON(t, h, "/api/v1/office/config", map[string]any{
		"src": "worklog-sheet", "sheet": "task", "from": "2026-10-31", "to": "2026-10-01",
	})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("范围错误应 400: %d %s", rec.Code, rec.Body)
	}
	// sheet 非法 → 400。
	rec = doJSON(t, h, "/api/v1/office/config", map[string]any{
		"src": "worklog-sheet", "sheet": "bad", "from": "2026-10-01", "to": "2026-10-02",
	})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("sheet 非法应 400: %d %s", rec.Code, rec.Body)
	}
	// 桥拒绝（汇总前核验 40901）直出。
	sheet.err = &RefError{Status: 409, Code: 40901, Message: "所选范围内有 2 条记录存在未通过项"}
	rec = doJSON(t, h, "/api/v1/office/config", map[string]any{
		"src": "worklog-sheet", "sheet": "task", "from": "2026-10-01", "to": "2026-10-02",
	})
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "40901") {
		t.Fatalf("桥业务错误应直出: %d %s", rec.Code, rec.Body)
	}
	sheet.err = nil

	// ref 直传（结构化口径）：JSON 引用不经扁平组装（非 JSON 引用走自定义 DocSource，
	// 见 TestConfigViewOnlySource 的 fakeSource 不透明 ref 兼容口径）。
	rec = doJSON(t, h, "/api/v1/office/config", map[string]any{
		"src":  "wls",
		"ref":  EncodeRef(WlsRef{Sheet: "dispatch", From: "2026-10-01", To: "2026-10-02", Team: 3}),
		"name": "派车汇总.xlsx",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("ref 直传失败: %d %s", rec.Code, rec.Body)
	}
	if sheet.got.Sheet != "dispatch" || sheet.got.Team != 3 {
		t.Fatalf("ref 直传解析错误: %+v", sheet.got)
	}
}

func TestBridgeRawFetch(t *testing.T) {
	m, _ := newTestModule(t)
	sheet := &fakeSheet{name: "任务单.docx", data: []byte("sheet-bytes")}
	safe := &fakeSafeday{name: "安全日.docx", data: []byte("safeday-bytes")}
	m.Sources = NewSources(sheet, safe, nil, nil, nil)
	h := newRouter(m)

	// wls 回源。
	tk, _ := m.SignTicket(context.Background(), TicketPayload{
		Src: "wls", Ref: EncodeRef(WlsRef{Sheet: "fee", From: "2026-10-01", To: "2026-10-02", Team: 5}), Name: "任务单.docx",
	}, 60)
	rec := rawGet(t, h, tk)
	if rec.Code != http.StatusOK || rec.Body.String() != "sheet-bytes" {
		t.Fatalf("wls 回源失败: %d %q", rec.Code, rec.Body)
	}
	if sheet.got.Sheet != "fee" || sheet.got.Team != 5 {
		t.Fatalf("wls ref 解析错误: %+v", sheet.got)
	}

	// sdr 回源（扁平参数组装后的 ref 等价）。
	tk, _ = m.SignTicket(context.Background(), TicketPayload{
		Src: "sdr", Ref: EncodeRef(SdrRef{ID: "r1"}), Name: "安全日.docx",
	}, 60)
	rec = rawGet(t, h, tk)
	if rec.Code != http.StatusOK || rec.Body.String() != "safeday-bytes" {
		t.Fatalf("sdr 回源失败: %d %q", rec.Code, rec.Body)
	}

	// 桥业务错误直出（RefError，纯文本应答）。
	safe.err = &RefError{Status: 403, Code: 40304, Message: "仅管理员可执行此操作"}
	tk, _ = m.SignTicket(context.Background(), TicketPayload{Src: "sdr", Ref: EncodeRef(SdrRef{ID: "r2"})}, 60)
	rec = rawGet(t, h, tk)
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "仅管理员可执行此操作") {
		t.Fatalf("RefError 应直出: %d %s", rec.Code, rec.Body)
	}

	// 不存在归一 404。
	safe.err = errors.New("记录不存在")
	tk, _ = m.SignTicket(context.Background(), TicketPayload{Src: "sdr", Ref: EncodeRef(SdrRef{ID: "r3"})}, 60)
	rec = rawGet(t, h, tk)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("不存在应 404: %d %s", rec.Code, rec.Body)
	}
}

func TestNetdiskBridgeAdapter(t *testing.T) {
	m, _ := newTestModule(t)
	b := &fakeNetdiskBridge{data: []byte("doc"), ct: "text/plain", key: "key-123"}
	m.Netdisk = NewNetdiskDoc(b)
	h := newRouter(m)

	// ndp 签发：文档 key 走 NetdiskKeyer（sha1 内容指纹口径由网盘侧给出）。
	rec := doJSON(t, h, "/api/v1/office/config", map[string]any{
		"src": "ndp", "space": "my", "path": "/docs/a.docx", "name": "a.docx",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("config 失败: %d %s", rec.Code, rec.Body)
	}
	cfg := extractConfig(t, rec)
	doc := cfg["document"].(map[string]any)
	if doc["key"] != "key-123" {
		t.Fatalf("文档 key 应取 DocKeyOf: %v", doc["key"])
	}

	// 回源 + 写回。
	ndp := &NDP{Space: "my", User: "zhang", Path: "/docs/a.docx"}
	tk, _ := m.SignTicket(context.Background(), TicketPayload{Src: "ndp", NDP: ndp, Name: "a.docx"}, 60)
	rec = rawGet(t, h, tk)
	if rec.Code != http.StatusOK || rec.Body.String() != "doc" {
		t.Fatalf("ndp 回源失败: %d %q", rec.Code, rec.Body)
	}
	if b.gotPath != "/docs/a.docx" {
		t.Fatalf("路径透传错误: %q", b.gotPath)
	}
	doc2 := NewNetdiskDoc(b)
	if err := doc2.WriteBack(context.Background(), *ndp, []byte("saved")); err != nil {
		t.Fatalf("写回: %v", err)
	}
	if string(b.written) != "saved" {
		t.Fatalf("写回内容错误: %q", b.written)
	}

	// 「不存在」语义归一 ErrDocNotFound（桥错误 → 404）。
	b.readErr = errors.New("文件或目录不存在")
	tk, _ = m.SignTicket(context.Background(), TicketPayload{Src: "ndp", NDP: ndp, Name: "a.docx"}, 60)
	rec = rawGet(t, h, tk)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("不存在应 404: %d %s", rec.Code, rec.Body)
	}
}

func TestTTLAndTicketSecretFallback(t *testing.T) {
	// TTL 走运行时配置；非法/缺省回落默认 600 / 43200。
	m := New(fixedCfg{"OO_FILE_TICKET_TTL": "300", "OO_CALLBACK_TICKET_TTL": "bad"})
	if n := m.ttlSeconds(context.Background(), "OO_FILE_TICKET_TTL", 600); n != 300 {
		t.Fatalf("TTL 读取错误: %d", n)
	}
	if n := m.ttlSeconds(context.Background(), "OO_CALLBACK_TICKET_TTL", 43200); n != 43200 {
		t.Fatalf("非法 TTL 应回落: %d", n)
	}
	// 票据密钥：OO_TICKET_SECRET 缺省回落 OO_JWT_SECRET（单密钥部署）。
	m2 := New(fixedCfg{"OO_JWT_SECRET": "oo-secret"})
	secret, err := m2.ticketSecret(context.Background())
	if err != nil || secret != "oo-secret" {
		t.Fatalf("票据密钥回落错误: %q %v", secret, err)
	}
	tk, err := m2.SignTicket(context.Background(), TicketPayload{Src: "wls", Ref: "r"}, 60)
	if err != nil {
		t.Fatalf("签发: %v", err)
	}
	if _, err := m2.VerifyTicket(context.Background(), tk); err != nil {
		t.Fatalf("校验: %v", err)
	}
}

// TestRawRejectsBadRef 引用 JSON 损坏时按无效票据处理（不泄内部错误）。
func TestRawRejectsBadRef(t *testing.T) {
	m, _ := newTestModule(t)
	m.Sources = NewSources(&fakeSheet{name: "a.docx", data: []byte("x")}, nil, nil, nil, nil)
	h := newRouter(m)
	tk, _ := m.SignTicket(context.Background(), TicketPayload{Src: "wls", Ref: "not-json", Name: "a.docx"}, 60)
	rec := rawGet(t, h, tk)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("坏引用应 401: %d %s", rec.Code, rec.Body)
	}
}
