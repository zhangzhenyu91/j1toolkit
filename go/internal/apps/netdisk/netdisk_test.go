package netdisk

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
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
	if zipSegment("a-b:c") != "a-b:c" {
		t.Fatal("非盘符式冒号段被误拒")
	}
	for _, bad := range []string{"", "..", ".", "a/b", "c:x", "abcd:x", "a\nb"} {
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
			body, _ := io.ReadAll(r.Body)
			size := 3
			if strings.Contains(string(body), "empty.txt") {
				size = 0 // 空文件原子写：staging 校验必须看到 0 字节
			}
			_, _ = fmt.Fprintf(w, `{"code":200,"data":{"name":"a.txt","is_dir":false,"size":%d,"modified":"2026-10-27 10:00:00","sign":"s1"}}`, size)
		case r.URL.Path == "/api/fs/mkdir" || r.URL.Path == "/api/fs/rename" ||
			r.URL.Path == "/api/fs/remove" || r.URL.Path == "/api/fs/move" || r.URL.Path == "/api/fs/copy":
			_, _ = w.Write([]byte(`{"code":200,"data":null}`))
		case r.URL.Path == "/api/fs/archive/meta":
			_, _ = w.Write([]byte(`{"code":200,"data":{"encrypted":false,"comment":"cmt","sign":"s1","content":[
        {"name":"a.txt","is_dir":false,"size":3,"modified":"2026-10-27 10:00:00"},
        {"name":"d","is_dir":true,"size":0,"modified":"2026-10-27 10:00:00","children":[
          {"name":"y.txt","is_dir":false,"size":1,"modified":"2026-10-27 10:00:00"}]}]}}`))
		case r.URL.Path == "/api/fs/archive/list":
			_, _ = w.Write([]byte(`{"code":200,"data":{"content":[
        {"name":"f.txt","is_dir":false,"size":2,"modified":"2026-10-27 10:00:00"}]}}`))
		case r.URL.Path == "/api/fs/archive/decompress":
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

// ---- 分享公开访问（Node 契约回归）----

// newShareTestModule 构造分享/压缩包测试模块（fake OpenList + 假库分享行）。
func newShareTestModule(t *testing.T, srvURL string) (*Module, http.Handler) {
	t.Helper()
	m := New(nil)
	m.OL = &OpenListHTTP{BaseURL: srvURL, Username: "u", Password: "p"}
	m.Base = BaseDirs{PersonalRoot: "/nd/my", PublicRoot: "/nd/pub"}
	m.JWTSecret = "s"
	return m, newTestRouter(t, m)
}

func TestShareListBodyTicketPath(t *testing.T) {
	srv, _ := fakeOpenList(t)
	m, r := newShareTestModule(t, srv.URL)
	tk := m.signTicket(jwt.MapClaims{"sid": "abcd1234ef"}, time.Hour)
	// 票据与 path 同放 body：不得因 body 被 shareAuth 提前消费而丢 path（双读回归）。
	req := httptest.NewRequest(http.MethodPost, "/api/v1/netdisk/public/share/abcd1234ef/list",
		strings.NewReader(`{"ticket":"`+tk+`","path":"/docs"}`))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("list 失败: %d %s", rec.Code, rec.Body.String())
	}
	var env struct {
		Data struct {
			Path  string `json:"path"`
			Items []any  `json:"items"`
		} `json:"data"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &env)
	if env.Data.Path != "/docs" {
		t.Fatalf("body 内 path 丢失（双读 bug 回归）: %s", rec.Body.String())
	}
	if len(env.Data.Items) != 2 {
		t.Fatalf("下钻 items 错误: %s", rec.Body.String())
	}
}

func TestShareInfoRouteForms(t *testing.T) {
	srv, _ := fakeOpenList(t)
	_, r := newShareTestModule(t, srv.URL)
	for _, p := range []string{
		"/api/v1/netdisk/public/share/abcd1234ef",
		"/api/v1/netdisk/public/share/abcd1234ef/",
	} {
		req := httptest.NewRequest(http.MethodGet, p, nil)
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "阿狸") {
			t.Fatalf("分享信息 %s 失败: %d %s", p, rec.Code, rec.Body.String())
		}
	}
}

// ---- 压缩包契约（Node 映射口径）----

func TestArchiveMetaListShape(t *testing.T) {
	srv, _ := fakeOpenList(t)
	_, r := newShareTestModule(t, srv.URL)

	// meta：{encrypted, comment, content} 映射 rel/children；上游 sign 不外发。
	req := withFakeAuth(httptest.NewRequest(http.MethodPost, "/api/v1/netdisk/archive/meta",
		strings.NewReader(`{"space":"my","path":"/pack.zip"}`)))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("meta 失败: %d %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), `"sign"`) {
		t.Fatalf("sign 不应外发: %s", rec.Body.String())
	}
	var env struct {
		Data struct {
			Encrypted bool   `json:"encrypted"`
			Comment   string `json:"comment"`
			Content   []struct {
				Name     string `json:"name"`
				Rel      string `json:"rel"`
				Children []struct {
					Rel string `json:"rel"`
				} `json:"children"`
			} `json:"content"`
		} `json:"data"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &env)
	if env.Data.Comment != "cmt" || len(env.Data.Content) != 2 {
		t.Fatalf("meta 映射错误: %s", rec.Body.String())
	}
	if env.Data.Content[0].Rel != "/a.txt" {
		t.Fatalf("rel 映射错误: %s", rec.Body.String())
	}
	if len(env.Data.Content[1].Children) != 1 || env.Data.Content[1].Children[0].Rel != "/d/y.txt" {
		t.Fatalf("children rel 映射错误: %s", rec.Body.String())
	}

	// list：{inner_path, items} 包装 + rel 映射。
	req = withFakeAuth(httptest.NewRequest(http.MethodPost, "/api/v1/netdisk/archive/list",
		strings.NewReader(`{"space":"my","path":"/pack.zip","inner_path":"/d"}`)))
	rec = httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("list 失败: %d %s", rec.Code, rec.Body.String())
	}
	var env2 struct {
		Data struct {
			InnerPath string `json:"inner_path"`
			Items     []struct {
				Name string `json:"name"`
				Rel  string `json:"rel"`
			} `json:"items"`
		} `json:"data"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &env2)
	if env2.Data.InnerPath != "/d" || len(env2.Data.Items) != 1 || env2.Data.Items[0].Rel != "/d/f.txt" {
		t.Fatalf("list 映射错误: %s", rec.Body.String())
	}
}

func TestArchiveDecompressNodeContract(t *testing.T) {
	var gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/auth/login":
			_, _ = w.Write([]byte(`{"code":200,"data":{"token":"tk-1"}}`))
		case "/api/fs/mkdir":
			_, _ = w.Write([]byte(`{"code":200,"data":null}`))
		case "/api/fs/archive/decompress":
			b, _ := io.ReadAll(r.Body)
			gotBody = string(b)
			_, _ = w.Write([]byte(`{"code":200,"data":null}`))
		default:
			_, _ = w.Write([]byte(`{"code":500,"message":"unknown"}`))
		}
	}))
	t.Cleanup(srv.Close)
	_, r := newShareTestModule(t, srv.URL)

	// Node 口径：{space, path, dst_dir, put_into_new_dir}（miniprogram / server/public 同款）。
	req := withFakeAuth(httptest.NewRequest(http.MethodPost, "/api/v1/netdisk/archive/decompress",
		strings.NewReader(`{"space":"my","path":"/pack.zip","dst_dir":"/out","put_into_new_dir":true}`)))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("Node 口径解压失败: %d %s", rec.Code, rec.Body.String())
	}
	for _, want := range []string{
		`"src_dir":"/nd/my/alice"`, `"dst_dir":"/nd/my/alice/out"`, `"name":["pack.zip"]`,
	} {
		if !strings.Contains(gotBody, want) {
			t.Fatalf("解压载荷缺 %s: %s", want, gotBody)
		}
	}

	// web 批量口径仍可用（src_dir/names 形态）。
	req = withFakeAuth(httptest.NewRequest(http.MethodPost, "/api/v1/netdisk/archive/decompress",
		strings.NewReader(`{"src_space":"my","src_dir":"/zips","names":["a.zip","b.zip"],"dst_space":"my","dst_dir":"/out2","put_into_new_dir":"false"}`)))
	rec = httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("批量口径解压失败: %d %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(gotBody, `"name":["a.zip","b.zip"]`) || !strings.Contains(gotBody, `"dst_dir":"/nd/my/alice/out2"`) {
		t.Fatalf("批量载荷错误: %s", gotBody)
	}
}

func TestArchiveInnerPathValidation(t *testing.T) {
	srv, _ := fakeOpenList(t)
	_, r := newShareTestModule(t, srv.URL)

	// list：内层路径穿越 → 400 内层路径不合法。
	req := withFakeAuth(httptest.NewRequest(http.MethodPost, "/api/v1/netdisk/archive/list",
		strings.NewReader(`{"space":"my","path":"/pack.zip","inner_path":"/../x"}`)))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("穿越 inner 未拒绝: %d %s", rec.Code, rec.Body.String())
	}

	// download：inner 为根 → 400 参数错误：path/inner。
	req = withFakeAuth(httptest.NewRequest(http.MethodGet,
		"/api/v1/netdisk/archive/download?space=my&path=/pack.zip&inner=/", nil))
	rec = httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("inner 根未拒绝: %d %s", rec.Code, rec.Body.String())
	}
}

// ---- 上传分片与预览票据契约 ----

func TestUploadChunkContract(t *testing.T) {
	srv, _ := fakeOpenList(t)
	_, r := newShareTestModule(t, srv.URL)

	// 大写 upload_id（Node /i 口径）+ octet-stream → 200。
	req := withFakeAuth(httptest.NewRequest(http.MethodPut, "/api/v1/netdisk/upload/chunk", strings.NewReader("BYTES")))
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set("X-Upload-Id", "ABCD1234ABCD1234")
	req.Header.Set("X-Chunk-Index", "0")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("大写 upload_id 被拒: %d %s", rec.Code, rec.Body.String())
	}

	// Content-Type 非 octet-stream → 400（Node express.raw 口径）。
	req2 := withFakeAuth(httptest.NewRequest(http.MethodPut, "/api/v1/netdisk/upload/chunk", strings.NewReader("BYTES")))
	req2.Header.Set("Content-Type", "application/json")
	req2.Header.Set("X-Upload-Id", "abcd1234abcd1234")
	req2.Header.Set("X-Chunk-Index", "0")
	rec2 := httptest.NewRecorder()
	r.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusBadRequest {
		t.Fatalf("非 octet-stream 未拒绝: %d %s", rec2.Code, rec2.Body.String())
	}
}

func TestPreviewSignRejectsPdf(t *testing.T) {
	srv, _ := fakeOpenList(t)
	_, r := newShareTestModule(t, srv.URL)
	// Node PREVIEW_MIME 白名单不含 pdf（pdf 走 ONLYOFFICE 承载页）。
	req := withFakeAuth(httptest.NewRequest(http.MethodPost, "/api/v1/netdisk/preview/sign",
		strings.NewReader(`{"space":"my","path":"/a.pdf"}`)))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("pdf 不应签预览票据: %d %s", rec.Code, rec.Body.String())
	}
	var env Envelope
	_ = json.Unmarshal(rec.Body.Bytes(), &env)
	if env.Code != codeNoTeam {
		t.Fatalf("业务码 = %d want %d", env.Code, codeNoTeam)
	}
}

func TestClientIPLoopbackForwarded(t *testing.T) {
	// 回环反代：信任 X-Forwarded-For 首项（Node trust proxy 'loopback' 口径）。
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "127.0.0.1:5678"
	req.Header.Set("X-Forwarded-For", "203.0.113.9, 127.0.0.1")
	if got := clientIP(req); got != "203.0.113.9" {
		t.Fatalf("回环反代应取 XFF 首项: %q", got)
	}
	// 非回环来源不信任转发头。
	req2 := httptest.NewRequest(http.MethodGet, "/", nil)
	req2.RemoteAddr = "198.51.100.7:1234"
	req2.Header.Set("X-Forwarded-For", "203.0.113.9")
	if got := clientIP(req2); got != "198.51.100.7" {
		t.Fatalf("非回环不应采信 XFF: %q", got)
	}
}
