package netdisk

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// ---- 路径防护（normRel/validName/sanitize*）----

func TestNormRel(t *testing.T) {
	cases := []struct {
		in   string
		want string
		ok   bool
	}{
		{"", "/", true},
		{"/", "/", true},
		{"a/b", "/a/b", true},
		{"/a//b/", "/a/b", true},
		{`a\b`, "/a/b", true},
		{"/a/../b", "", false},
		{"/../etc", "", false},
		{"/a/./b", "", false},
		{"/" + strings.Repeat("x", 401), "", false},
	}
	for _, c := range cases {
		got, ok := NormRel(c.in)
		if ok != c.ok || (ok && got != c.want) {
			t.Fatalf("NormRel(%q) = %q,%v want %q,%v", c.in, got, ok, c.want, c.ok)
		}
	}
}

func TestValidName(t *testing.T) {
	if ValidName("a.txt") != "a.txt" {
		t.Fatal("合法名被拒")
	}
	for _, bad := range []string{"", " ", "a/b", `a\b`, "..", ".", "a\nb", strings.Repeat("x", 201)} {
		if ValidName(bad) != "" {
			t.Fatalf("非法名 %q 通过", bad)
		}
	}
}

func TestSanitizeRelDir(t *testing.T) {
	if got, ok := SanitizeRelDir("出工日志/2026-10"); !ok || got != "出工日志/2026-10" {
		t.Fatalf("合法目录被拒: %q %v", got, ok)
	}
	if _, ok := SanitizeRelDir("a/../b"); ok {
		// 「..」被净化为下划线文本段而非穿越；仅当净化后仍含分隔符才非法
		t.Fatal("穿越目录通过")
	}
	if got, ok := SanitizeRelDir(""); !ok || got != "" {
		t.Fatalf("根目录应为空串: %q %v", got, ok)
	}
	if _, ok := SanitizeRelDir("a/b/c/d/e/f/g"); ok {
		t.Fatal("超深目录通过")
	}
	if got, ok := SanitizeRelDir("a:b"); !ok || got != "a_b" {
		// 冒号按共享通道口径净化为下划线（Node sanitizeName 同款），不判非法
		t.Fatalf("冒号段净化错误: %q %v", got, ok)
	}
}

func TestSpaceRootOf(t *testing.T) {
	d := BaseDirs{PersonalRoot: "/nd/my", PublicRoot: "/nd/pub"}
	if got, ok := SpaceRootOf(d, "alice", "检修一班", "my"); !ok || got != "/nd/my/alice" {
		t.Fatalf("my 根错误: %q %v", got, ok)
	}
	if got, ok := SpaceRootOf(d, "alice", "检修一班", "public"); !ok || got != "/nd/pub/检修一班" {
		t.Fatalf("public 根错误: %q %v", got, ok)
	}
	if got, ok := SpaceRootOf(d, "alice", "", "public"); !ok || got != "" {
		t.Fatalf("未分配班组公共区应为空串: %q %v", got, ok)
	}
	if _, ok := SpaceRootOf(d, "alice", "x", "bad"); ok {
		t.Fatal("非法 space 通过")
	}
}

func TestZipSegment(t *testing.T) {
	if zipSegment("a.txt") != "a.txt" {
		t.Fatal("合法段被拒")
	}
	for _, bad := range []string{"", "..", ".", "a/b", "c:x", "a\nb"} {
		if zipSegment(bad) != "" {
			t.Fatalf("非法 ZIP 段 %q 通过", bad)
		}
	}
}

// ---- 假 OpenList（httptest）----

// fakeOpenList 模拟 OpenList 信封（code=200 成功）。
func fakeOpenList(t *testing.T) (*httptest.Server, *int64) {
	t.Helper()
	var loginCount int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/api/auth/login":
			atomic.AddInt64(&loginCount, 1)
			_, _ = w.Write([]byte(`{"code":200,"data":{"token":"tk-1"}}`))
		case r.URL.Path == "/api/fs/list":
			body, _ := io.ReadAll(r.Body)
			if strings.Contains(string(body), "/empty") {
				_, _ = w.Write([]byte(`{"code":200,"data":{"content":[]}}`))
				return
			}
			_, _ = w.Write([]byte(`{"code":200,"data":{"content":[
        {"name":"a.txt","is_dir":false,"size":3,"modified":"2026-10-27 10:00:00"},
        {"name":"sub","is_dir":true,"size":0,"modified":"2026-10-27 10:00:00"}]}}`))
		case r.URL.Path == "/api/fs/get":
			_, _ = w.Write([]byte(`{"code":200,"data":{"name":"a.txt","is_dir":false,"size":3,"modified":"2026-10-27 10:00:00","sign":"s1"}}`))
		case r.URL.Path == "/api/fs/mkdir" || r.URL.Path == "/api/fs/rename" ||
			r.URL.Path == "/api/fs/remove" || r.URL.Path == "/api/fs/move" || r.URL.Path == "/api/fs/copy":
			_, _ = w.Write([]byte(`{"code":200,"data":null}`))
		case strings.HasPrefix(r.URL.Path, "/api/fs/multipart"):
			_, _ = w.Write([]byte(`{"code":200,"data":{"upload_id":"abcd-1234-abcd-1234","state":"in_progress"}}`))
		case r.URL.Path == "/api/fs/put":
			_, _ = w.Write([]byte(`{"code":200,"data":null}`))
		case strings.HasPrefix(r.URL.Path, "/d/"):
			w.Header().Set("Content-Type", "image/jpeg")
			_, _ = w.Write([]byte("JPEGDATA"))
		default:
			_, _ = w.Write([]byte(`{"code":500,"message":"unknown api"}`))
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &loginCount
}

func TestOpenListLoginAndCall(t *testing.T) {
	srv, logins := fakeOpenList(t)
	ol := &OpenListHTTP{BaseURL: srv.URL, Username: "u", Password: "p"}
	data, err := ol.Call(t.Context(), http.MethodPost, "/api/fs/list", CallOpts{
		Body: map[string]any{"path": "/x", "password": ""},
	})
	if err != nil {
		t.Fatalf("Call 失败: %v", err)
	}
	if !strings.Contains(string(data), "a.txt") {
		t.Fatalf("data 错误: %s", data)
	}
	// 第二次调用应复用 token（登录仅一次）。
	if _, err := ol.Call(t.Context(), http.MethodPost, "/api/fs/get", CallOpts{Body: map[string]any{"path": "/x/a.txt"}}); err != nil {
		t.Fatalf("第二次 Call 失败: %v", err)
	}
	if n := atomic.LoadInt64(logins); n != 1 {
		t.Fatalf("登录次数 = %d want 1", n)
	}
}

func TestOpenListNotConfigured(t *testing.T) {
	ol := &OpenListHTTP{BaseURL: "http://127.0.0.1:0"}
	if err := ol.EnsureConfigured(); err != ErrNotConfigured {
		t.Fatalf("未配置应报 ErrNotConfigured, got %v", err)
	}
}

func TestOpenListBusinessError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/auth/login" {
			_, _ = w.Write([]byte(`{"code":200,"data":{"token":"tk"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"code":500,"message":"object not found"}`))
	}))
	defer srv.Close()
	ol := &OpenListHTTP{BaseURL: srv.URL, Username: "u", Password: "p"}
	_, err := ol.Call(t.Context(), http.MethodPost, "/api/fs/get", CallOpts{Body: map[string]any{"path": "/gone"}})
	ue, ok := err.(*UpstreamError)
	if !ok || !ue.Exposable || ue.Message != "object not found" || ue.UpstreamCode != 500 {
		t.Fatalf("业务错误未归一: %v", err)
	}
}

func TestOpenListDownloadStream(t *testing.T) {
	srv, _ := fakeOpenList(t)
	ol := &OpenListHTTP{BaseURL: srv.URL, Username: "u", Password: "p"}
	rc, header, err := ol.DownloadStream(t.Context(), "/x/a.txt")
	if err != nil {
		t.Fatalf("DownloadStream 失败: %v", err)
	}
	defer rc.Close()
	buf, _ := io.ReadAll(rc)
	if string(buf) != "JPEGDATA" || header.Get("Content-Type") != "image/jpeg" {
		t.Fatalf("下载内容/类型错误: %q %q", buf, header.Get("Content-Type"))
	}
}

// ---- 分享票据 / 越界校验 ----

func TestResolveShareAbs(t *testing.T) {
	row := &ShareRow{BasePath: "/nd/pub/检修一班", Paths: []string{"/docs", "/a.txt"}}
	if got := resolveShareAbs(row, "/docs"); got != "/nd/pub/检修一班/docs" {
		t.Fatalf("自身路径: %q", got)
	}
	if got := resolveShareAbs(row, "/docs/x/y.txt"); got != "/nd/pub/检修一班/docs/x/y.txt" {
		t.Fatalf("子路径: %q", got)
	}
	if got := resolveShareAbs(row, "/other"); got != "" {
		t.Fatalf("越界路径应为空: %q", got)
	}
	if got := resolveShareAbs(row, "/docsx"); got != "" {
		t.Fatalf("前缀伪装越界应为空: %q", got)
	}
}

func TestShareState(t *testing.T) {
	if shareState(&ShareRow{Status: 0}) != "disabled" {
		t.Fatal("停用判定错误")
	}
	if shareState(&ShareRow{Status: 1}) != "active" {
		t.Fatal("生效判定错误")
	}
}

func TestShareTicketRoundTrip(t *testing.T) {
	m := New(nil)
	m.JWTSecret = "test-secret"
	tok := m.signTicket(map[string]any{"sid": "abc123def456"}, 3600e9) // 1h
	claims := m.verifyTicket(tok)
	if claims == nil || claimsString(claims, "sid") != "abc123def456" {
		t.Fatal("票据签发/验签失败")
	}
	if m.verifyTicket("bad-token") != nil {
		t.Fatal("非法票据应验签失败")
	}
	// 他人密钥签发的票据应失败。
	m2 := New(nil)
	m2.JWTSecret = "other-secret"
	if m2.verifyTicket(tok) != nil {
		t.Fatal("异密钥票据应验签失败")
	}
}

// ---- 路由层：路径穿越拦截 ----

func TestListRejectsTraversal(t *testing.T) {
	srv, _ := fakeOpenList(t)
	m := New(nil)
	m.OL = &OpenListHTTP{BaseURL: srv.URL, Username: "u", Password: "p"}
	m.Base = BaseDirs{PersonalRoot: "/nd/my", PublicRoot: "/nd/pub"}
	m.JWTSecret = "s"

	r := newTestRouter(t, m)
	body := `{"space":"my","path":"/../etc"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/netdisk/list", strings.NewReader(body))
	req = withFakeAuth(req)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("穿越路径未被拒: status=%d body=%s", rec.Code, rec.Body.String())
	}
	var env Envelope
	_ = json.Unmarshal(rec.Body.Bytes(), &env)
	if env.Code != codeParam {
		t.Fatalf("业务码 = %d want %d", env.Code, codeParam)
	}
}

func TestPreviewSignAndStream(t *testing.T) {
	srv, _ := fakeOpenList(t)
	m := New(nil)
	m.OL = &OpenListHTTP{BaseURL: srv.URL, Username: "u", Password: "p"}
	m.Base = BaseDirs{PersonalRoot: "/nd/my", PublicRoot: "/nd/pub"}
	m.JWTSecret = "s"

	r := newTestRouter(t, m)
	// 签发。
	req := withFakeAuth(httptest.NewRequest(http.MethodPost, "/api/v1/netdisk/preview/sign",
		strings.NewReader(`{"space":"my","path":"/report.docx"}`)))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("签发失败: %d %s", rec.Code, rec.Body.String())
	}
	var env struct {
		Data struct {
			Token string `json:"token"`
		} `json:"data"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &env)
	if env.Data.Token == "" {
		t.Fatal("未签发票据")
	}
	// 回源（免登录）。
	req2 := httptest.NewRequest(http.MethodGet, "/api/v1/netdisk/preview/"+env.Data.Token+"/report.docx", nil)
	rec2 := httptest.NewRecorder()
	r.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusOK {
		t.Fatalf("回源失败: %d %s", rec2.Code, rec2.Body.String())
	}
	if rec2.Body.String() != "JPEGDATA" {
		t.Fatalf("回源内容错误: %q", rec2.Body.String())
	}
	// 非法票据。
	req3 := httptest.NewRequest(http.MethodGet, "/api/v1/netdisk/preview/bogus/x.docx", nil)
	rec3 := httptest.NewRecorder()
	r.ServeHTTP(rec3, req3)
	if rec3.Code != http.StatusForbidden {
		t.Fatalf("非法票据未拒绝: %d", rec3.Code)
	}
}

func TestUploadInitRejectsTooLarge(t *testing.T) {
	srv, _ := fakeOpenList(t)
	m := New(nil)
	m.OL = &OpenListHTTP{BaseURL: srv.URL, Username: "u", Password: "p"}
	m.Base = BaseDirs{PersonalRoot: "/nd/my", PublicRoot: "/nd/pub"}
	m.JWTSecret = "s"
	m.MaxUploadMB = 1

	r := newTestRouter(t, m)
	req := withFakeAuth(httptest.NewRequest(http.MethodPost, "/api/v1/netdisk/upload/init",
		strings.NewReader(`{"space":"my","path":"/","name":"big.bin","size":1048577}`)))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("超限未拒绝: %d %s", rec.Code, rec.Body.String())
	}
}

func TestUploadInitEmptyFile(t *testing.T) {
	srv, _ := fakeOpenList(t)
	m := New(nil)
	m.OL = &OpenListHTTP{BaseURL: srv.URL, Username: "u", Password: "p"}
	m.Base = BaseDirs{PersonalRoot: "/nd/my", PublicRoot: "/nd/pub"}
	m.JWTSecret = "s"

	r := newTestRouter(t, m)
	req := withFakeAuth(httptest.NewRequest(http.MethodPost, "/api/v1/netdisk/upload/init",
		strings.NewReader(`{"space":"my","path":"/","name":"empty.txt","size":0}`)))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"instant":true`) {
		t.Fatalf("空文件直传失败: %d %s", rec.Code, rec.Body.String())
	}
}

func TestSpacesListsTeamPublic(t *testing.T) {
	m := New(nil)
	m.Base = BaseDirs{PersonalRoot: "/nd/my", PublicRoot: "/nd/pub"}
	m.JWTSecret = "s"
	r := newTestRouter(t, m)
	req := withFakeAuth(httptest.NewRequest(http.MethodGet, "/api/v1/netdisk/spaces", nil))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("spaces 失败: %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "公共区") {
		t.Fatalf("公共区缺失: %s", rec.Body.String())
	}
}
