package office

// 网盘路径口径测试：normNDPPath 规范化（无前导斜杠/反斜杠/重复斜杠/中文多级目录）与越界拦截；
// /config 签发把规范化路径写入票据（fake 网盘桥记录载荷），/raw 回源与错误码走 httptest。
// 网盘桥经 NewNetdiskDoc 生产口径装配（含错误归一）；不访问真实 OpenList / OnlyOffice。

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// fakeBridge 网盘桥桩（office.NetdiskBridge 方法集）：经 NewNetdiskDoc 适配注入，
// 覆盖真实错误归一路径；记录签发期/回源期收到的相对路径。
type fakeBridge struct {
	data     []byte
	docKey   string
	docErr   error
	fetchErr error
	gotPath  []string // DocKey 收到的 relPath（签发期）
	fetchGot []string // ReadFileBytes 回源时收到的 relPath（经票据回放）
}

func (b *fakeBridge) ReadFileBytes(_ context.Context, _, _ string, _ int64, relPath string) ([]byte, string, error) {
	if b.fetchErr != nil {
		return nil, "", b.fetchErr
	}
	b.fetchGot = append(b.fetchGot, relPath)
	return b.data, "text/plain", nil
}

func (b *fakeBridge) WriteFileBytes(context.Context, string, string, int64, string, []byte) error {
	return nil
}

func (b *fakeBridge) DocKey(_ context.Context, _, _ string, _ int64, relPath string) (string, error) {
	b.gotPath = append(b.gotPath, relPath)
	return b.docKey, b.docErr
}

// newNDPModule 网盘源按生产口径装配（NewNetdiskDoc 桥 + NetdiskKeyer）。
func newNDPModule(t *testing.T) (*Module, *fakeBridge) {
	t.Helper()
	m, _ := newTestModule(t)
	fb := &fakeBridge{data: []byte("doc-bytes"), docKey: "k1"}
	m.Netdisk = NewNetdiskDoc(fb)
	return m, fb
}

func TestNormNDPPath(t *testing.T) {
	okCases := []struct{ in, want string }{
		{"docs/a.docx", "/docs/a.docx"},                // 网盘相对路径（无前导斜杠，前端 _rel 口径）
		{"/docs/a.docx", "/docs/a.docx"},               // 已带前导斜杠
		{`\docs\a.docx`, "/docs/a.docx"},               // 反斜杠
		{"//a///b.docx", "/a/b.docx"},                  // 重复斜杠压缩
		{"/报表/2026 年度/汇总.xlsx", "/报表/2026 年度/汇总.xlsx"}, // 中文路径/多级目录不破坏
		{" a.docx ", "/a.docx"},                        // 首尾空白
	}
	for _, c := range okCases {
		got, ok := normNDPPath(c.in)
		if !ok || got != c.want {
			t.Fatalf("normNDPPath(%q) = %q,%v want %q,true", c.in, got, ok, c.want)
		}
	}
	badCases := []string{
		"", "   ", "/", "\\",
		"..", "/../etc/passwd", "/a/../b.docx", "/a/./b.docx", "docs/../../x",
		"/" + strings.Repeat("a", 401),
	}
	for _, in := range badCases {
		if got, ok := normNDPPath(in); ok {
			t.Fatalf("normNDPPath(%q) = %q 应拒绝", in, got)
		}
	}
}

// TestConfigNDPPathNormalized /config 网盘源：无前导斜杠/反斜杠/中文多级路径均放行，
// 票据载荷与定名取规范化路径；回源 URL 经 /raw 回放同一路径。
func TestConfigNDPPathNormalized(t *testing.T) {
	m, fb := newNDPModule(t)
	h := newRouter(m)

	for _, p := range []string{
		"报表/2026 年度/汇总.xlsx",  // 前端 _rel 形态（无前导斜杠）
		`\报表\2026 年度\汇总.xlsx`, // 反斜杠形态
		"/报表/2026 年度/汇总.xlsx",
	} {
		rec := doJSON(t, h, "/api/v1/office/config", map[string]any{
			"src": "ndp", "space": "my", "path": p,
		})
		if rec.Code != http.StatusOK {
			t.Fatalf("config(%q) 失败: %d %s", p, rec.Code, rec.Body)
		}
		if n := len(fb.gotPath); n == 0 || fb.gotPath[n-1] != "/报表/2026 年度/汇总.xlsx" {
			t.Fatalf("签发期路径未规范化: %v", fb.gotPath)
		}
		cfg := extractConfig(t, rec)
		doc := cfg["document"].(map[string]any)
		if doc["title"] != "汇总.xlsx" {
			t.Fatalf("定名应取规范化路径末段: %+v", doc)
		}
		// 回源：票据 URL 直取 /raw，载荷路径应为规范化值。
		url, _ := doc["url"].(string)
		tk := url[strings.LastIndex(url, "/raw/")+len("/raw/"):]
		if i := strings.IndexByte(tk, '/'); i > 0 {
			tk = tk[:i]
		}
		req := httptest.NewRequest(http.MethodGet, "/api/v1/office/raw/"+tk, nil)
		rec2 := httptest.NewRecorder()
		h.ServeHTTP(rec2, req)
		if rec2.Code != http.StatusOK || rec2.Body.String() != "doc-bytes" {
			t.Fatalf("回源失败: %d %q", rec2.Code, rec2.Body)
		}
		if n := len(fb.fetchGot); n == 0 || fb.fetchGot[n-1] != "/报表/2026 年度/汇总.xlsx" {
			t.Fatalf("回源路径错误: %v", fb.fetchGot)
		}
	}
}

// TestConfigNDPPathRejected 越界/根路径/空路径一律 400 40001 路径不合法（继续拦越界）。
func TestConfigNDPPathRejected(t *testing.T) {
	m, _ := newNDPModule(t)
	h := newRouter(m)
	for _, p := range []string{"", "/", "..", "/../etc/passwd", "a/../../b.docx", "/a/./b.docx"} {
		rec := doJSON(t, h, "/api/v1/office/config", map[string]any{
			"src": "ndp", "space": "my", "path": p, "name": "a.docx",
		})
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("path(%q) 应 400: %d %s", p, rec.Code, rec.Body)
		}
		var env Envelope
		if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
			t.Fatalf("解析信封: %v", err)
		}
		if env.Code != codeParam || env.Message != "路径不合法" {
			t.Fatalf("path(%q) 错误码口径: %d %s", p, env.Code, env.Message)
		}
	}
}

// TestConfigNDPSpaces 空间/班组载荷核对：my 带签发人用户名；public 带班组；team_id 缺省回落签发人班组。
func TestConfigNDPSpaces(t *testing.T) {
	m, fb := newNDPModule(t)
	h := newRouter(m)

	for _, body := range []map[string]any{
		{"src": "ndp", "space": "my", "path": "a.docx"},
		{"src": "ndp", "space": "public", "path": "a.docx", "team_id": 5},
		{"src": "ndp", "space": "public", "path": "a.docx"},
	} {
		if rec := doJSON(t, h, "/api/v1/office/config", body); rec.Code != http.StatusOK {
			t.Fatalf("config %v 失败: %d %s", body, rec.Code, rec.Body)
		}
	}

	// 回源票据回放：按空间/班组核对载荷（my 个人、public 显式班组、public 缺省回落签发人班组=1）。
	want := []NDP{
		{Space: "my", User: "zhang", Path: "/a.docx", Team: 1},
		{Space: "public", User: "zhang", Path: "/a.docx", Team: 5},
		{Space: "public", User: "zhang", Path: "/a.docx", Team: 1},
	}
	caught := &captureBridge{NetdiskDoc: NewNetdiskDoc(fb), docKey: "k1"}
	m.Netdisk = caught
	for _, body := range []map[string]any{
		{"src": "ndp", "space": "my", "path": "a.docx"},
		{"src": "ndp", "space": "public", "path": "a.docx", "team_id": 5},
		{"src": "ndp", "space": "public", "path": "a.docx"},
	} {
		if rec := doJSON(t, h, "/api/v1/office/config", body); rec.Code != http.StatusOK {
			t.Fatalf("config %v 失败: %d %s", body, rec.Code, rec.Body)
		}
	}
	if len(caught.ndps) != len(want) {
		t.Fatalf("载荷数错误: %d", len(caught.ndps))
	}
	for i, w := range want {
		g := caught.ndps[i]
		if g.Space != w.Space || g.User != w.User || g.Path != w.Path || g.Team != w.Team {
			t.Fatalf("载荷[%d] 错误: %+v want %+v", i, g, w)
		}
	}
}

// captureBridge 包装网盘桥适配，记录 DocKeyOf 收到的 NDP 载荷（空间/用户/班组/路径核对）。
type captureBridge struct {
	NetdiskDoc
	docKey string
	ndps   []NDP
}

// DocKeyOf NetdiskKeyer：记录票据载荷（覆盖内嵌桥同名方法）。
func (c *captureBridge) DocKeyOf(_ context.Context, p NDP) (string, error) {
	c.ndps = append(c.ndps, p)
	return c.docKey, nil
}

// TestConfigNDPNoDocKeyer 网盘桥未实现 NetdiskKeyer 时也可签发（文档 key 回落随机票据 id）。
func TestConfigNDPNoDocKeyer(t *testing.T) {
	m, _ := newTestModule(t) // fakeNetdisk 只实现 NetdiskDoc
	h := newRouter(m)
	rec := doJSON(t, h, "/api/v1/office/config", map[string]any{
		"src": "ndp", "space": "my", "path": "docs/a.docx",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("config 失败: %d %s", rec.Code, rec.Body)
	}
	cfg := extractConfig(t, rec)
	doc := cfg["document"].(map[string]any)
	if !strings.HasPrefix(doc["key"].(string), "k-") {
		t.Fatalf("文档 key 应回落票据 id: %v", doc["key"])
	}
}

// TestConfigNDPDocKeyError 签发期文档 key 校验失败（文件不存在等）按桥错误口径应答 404。
func TestConfigNDPDocKeyError(t *testing.T) {
	m, fb := newNDPModule(t)
	fb.docErr = &bridgeNF{msg: "文件或目录不存在"}
	h := newRouter(m)
	rec := doJSON(t, h, "/api/v1/office/config", map[string]any{
		"src": "ndp", "space": "my", "path": "gone.docx",
	})
	if rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), "文件或目录不存在") {
		t.Fatalf("不存在应 404: %d %s", rec.Code, rec.Body)
	}
}

// TestRawNDPErrorCodes /raw 回源错误码：票据无效 401 纯文本；网盘桥「路径不合法」400；
// 「不存在」404；未预期错误 500。
func TestRawNDPErrorCodes(t *testing.T) {
	m, fb := newNDPModule(t)
	h := newRouter(m)

	// 路径不合法（桥哨兵语义）→ 400 纯文本。
	fb.fetchErr = &bridgeNF{msg: "路径不合法"}
	tk, _ := m.SignTicket(context.Background(), TicketPayload{Src: "ndp", Name: "a.docx",
		NDP: &NDP{Space: "my", User: "zhang", Path: "/a.docx"}}, 60)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/office/raw/"+tk, nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest || rec.Body.String() != "路径不合法" {
		t.Fatalf("路径不合法应 400: %d %q", rec.Code, rec.Body)
	}

	// 不存在 → 404（ErrDocNotFound 口径）。
	fb.fetchErr = ErrDocNotFound
	req = httptest.NewRequest(http.MethodGet, "/api/v1/office/raw/"+tk, nil)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("不存在应 404: %d %q", rec.Code, rec.Body)
	}

	// 未预期错误 → 500 通用文案。
	fb.fetchErr = context.DeadlineExceeded
	req = httptest.NewRequest(http.MethodGet, "/api/v1/office/raw/"+tk, nil)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusInternalServerError || !strings.Contains(rec.Body.String(), "回源失败") {
		t.Fatalf("上游异常应 500: %d %q", rec.Code, rec.Body)
	}

	// 票据无效 → 401。
	req = httptest.NewRequest(http.MethodGet, "/api/v1/office/raw/bad-ticket", nil)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("坏票据应 401: %d", rec.Code)
	}
}

// bridgeNF 「不存在」哨兵桩（IsNotFound；message 可为「路径不合法」等）。
type bridgeNF struct{ msg string }

func (e *bridgeNF) Error() string    { return e.msg }
func (e *bridgeNF) IsNotFound() bool { return true }
