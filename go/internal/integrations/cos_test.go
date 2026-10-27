package integrations

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// 金标准向量由 server/node_modules/cos-nodejs-sdk-v5 sdk/util.js getAuth 生成（Node 侧真实协议实现），
// 与本包 cosSign 交叉对拍，防止签名细节漂移。
const (
	goldenSecretID  = "test-cos-secret-id"
	goldenSecretKey = "test-cos-secret-key"
)

// TestCOSSignGolden 三组签名金标准：基础 PUT、DELETE、中文键 + 查询参数。
func TestCOSSignGolden(t *testing.T) {
	cases := []struct {
		name string
		p    cosSignParams
		want string
	}{
		{
			name: "put-basic",
			p: cosSignParams{
				SecretID:  goldenSecretID,
				SecretKey: goldenSecretKey,
				KeyTime:   "1550039204;1550042804",
				Method:    "PUT",
				Path:      "/worklog/2026/10/27/test.jpg",
				Headers: map[string]string{
					"Host":          "examplebucket-1250000000.cos.ap-beijing.myqcloud.com",
					"Content-Type":  "image/jpeg",
					"Cache-Control": "private, no-cache",
				},
			},
			want: "q-sign-algorithm=sha1&q-ak=test-cos-secret-id" +
				"&q-sign-time=1550039204;1550042804&q-key-time=1550039204;1550042804" +
				"&q-header-list=cache-control;content-type;host&q-url-param-list=" +
				"&q-signature=152b9eeb95582a704c53ae969a3a2550ea1f1ff8",
		},
		{
			name: "delete-basic",
			p: cosSignParams{
				SecretID:  goldenSecretID,
				SecretKey: goldenSecretKey,
				KeyTime:   "1550039204;1550042804",
				Method:    "DELETE",
				Path:      "/worklog/2026/10/27/test.jpg",
				Headers: map[string]string{
					"Host": "examplebucket-1250000000.cos.ap-beijing.myqcloud.com",
				},
			},
			want: "q-sign-algorithm=sha1&q-ak=test-cos-secret-id" +
				"&q-sign-time=1550039204;1550042804&q-key-time=1550039204;1550042804" +
				"&q-header-list=host&q-url-param-list=" +
				"&q-signature=7907956f202666c7c3770aea94edd698bfc5c63a",
		},
		{
			name: "put-cjk-query",
			p: cosSignParams{
				SecretID:  goldenSecretID,
				SecretKey: goldenSecretKey,
				KeyTime:   "1761552000;1761552900",
				Method:    "put",
				Path:      "/safeday/检修一班/2026.10.27-1761552000000.pdf",
				Query:     map[string]string{"response-content-type": "application/pdf", "versionId": "3"},
				Headers: map[string]string{
					"Host":          "mybucket-1250000000.cos.ap-guangzhou.myqcloud.com",
					"Content-Type":  "application/pdf",
					"Cache-Control": "private, no-cache",
				},
			},
			want: "q-sign-algorithm=sha1&q-ak=test-cos-secret-id" +
				"&q-sign-time=1761552000;1761552900&q-key-time=1761552000;1761552900" +
				"&q-header-list=cache-control;content-type;host&q-url-param-list=response-content-type;versionid" +
				"&q-signature=59b5ee1e9915758207201c7db0d3e02f6b6c6c05",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := cosSign(c.p)
			if err != nil {
				t.Fatalf("cosSign = %v", err)
			}
			if got != c.want {
				t.Fatalf("cosSign 不匹配金标准：\n got %s\nwant %s", got, c.want)
			}
		})
	}
}

// cosTestStore 构造指向 httptest 的 COSStore（固定签名时间）。
func cosTestStore(t *testing.T, endpoint string) *COSStore {
	t.Helper()
	return &COSStore{
		Cfg: NewMapProvider(map[string]string{
			"COS_SECRET_ID":  "test-cos-id",
			"COS_SECRET_KEY": "SecretKeyTEST",
			"COS_BUCKET":     "examplebucket-1250000000",
			"COS_REGION":     "ap-beijing",
		}),
		Endpoint: endpoint,
		Now:      func() time.Time { return time.Unix(1550039204, 0) },
	}
}

// TestCOSPutBufferHTTP httptest 验证上传请求形态：路径编码、签名头、CacheControl 与 body。
func TestCOSPutBufferHTTP(t *testing.T) {
	var gotMethod, gotEscapedPath, gotAuth, gotCT, gotCC string
	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotEscapedPath = r.URL.EscapedPath()
		gotAuth = r.Header.Get("Authorization")
		gotCT = r.Header.Get("Content-Type")
		gotCC = r.Header.Get("Cache-Control")
		gotBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	store := cosTestStore(t, srv.URL)
	key := "safeday/检修一班/2026.10.27-1761552000000.pdf"
	if err := store.PutBuffer(context.Background(), key, []byte("pdf-bytes"), "application/pdf"); err != nil {
		t.Fatalf("PutBuffer = %v", err)
	}
	if gotMethod != http.MethodPut {
		t.Fatalf("method = %s", gotMethod)
	}
	// 请求路径：UTF-8 百分号编码、保留 /（对齐 SDK getUrl）
	wantPath := "/safeday/%E6%A3%80%E4%BF%AE%E4%B8%80%E7%8F%AD/2026.10.27-1761552000000.pdf"
	if gotEscapedPath != wantPath {
		t.Fatalf("escaped path = %s，期望 %s", gotEscapedPath, wantPath)
	}
	if gotCT != "application/pdf" || gotCC != "private, no-cache" {
		t.Fatalf("headers = %q / %q", gotCT, gotCC)
	}
	if string(gotBody) != "pdf-bytes" {
		t.Fatalf("body = %q", gotBody)
	}
	// Authorization 与独立签名一致（Host 为 httptest 实际 host）
	host := strings.TrimPrefix(srv.URL, "http://")
	wantAuth, err := cosSign(cosSignParams{
		SecretID:  "test-cos-id",
		SecretKey: "SecretKeyTEST",
		Method:    "PUT",
		Path:      "/" + key, // 签名用未编码路径
		Headers: map[string]string{
			"Host":          host,
			"Content-Type":  "application/pdf",
			"Cache-Control": "private, no-cache",
		},
		Now:     func() time.Time { return time.Unix(1550039204, 0) },
		Expires: defaultCOSExpires,
	})
	if err != nil {
		t.Fatal(err)
	}
	if gotAuth != wantAuth {
		t.Fatalf("Authorization 不匹配：\n got %s\nwant %s", gotAuth, wantAuth)
	}
	if !strings.HasPrefix(gotAuth, "q-sign-algorithm=sha1&q-ak=test-cos-id&q-sign-time=1550039204;1550040104") {
		t.Fatalf("Authorization 缺签名时间等要素：%s", gotAuth)
	}
}

// TestCOSDeleteObjectHTTP 删除：204 成功、404 视为成功（不存在即达成）。
func TestCOSDeleteObjectHTTP(t *testing.T) {
	codes := make(chan int, 4)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete {
			t.Errorf("method = %s", r.Method)
		}
		code := <-codes
		w.WriteHeader(code)
	}))
	defer srv.Close()
	store := cosTestStore(t, srv.URL)

	codes <- http.StatusNoContent
	if err := store.DeleteObject(context.Background(), "a/b.jpg"); err != nil {
		t.Fatalf("DeleteObject(204) = %v", err)
	}
	codes <- http.StatusNotFound
	if err := store.DeleteObject(context.Background(), "a/b.jpg"); err != nil {
		t.Fatalf("DeleteObject(404) 应视为成功：%v", err)
	}
	codes <- http.StatusInternalServerError
	if err := store.DeleteObject(context.Background(), "a/b.jpg"); err == nil {
		t.Fatal("DeleteObject(500) 应报错")
	}
}

// TestCOSPutRetryTransient 上传遇 500 有界重试（显式策略），4xx 不重试。
func TestCOSPutRetryTransient(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls < 3 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	store := cosTestStore(t, srv.URL)
	store.Retry = RetryPolicy{
		MaxAttempts: 3,
		BaseWait:    time.Millisecond,
		MaxWait:     time.Millisecond,
		Sleep:       func(context.Context, time.Duration) error { return nil },
	}
	if err := store.PutBuffer(context.Background(), "k.jpg", []byte("b"), "image/jpeg"); err != nil {
		t.Fatalf("PutBuffer = %v", err)
	}
	if calls != 3 {
		t.Fatalf("调用次数 = %d，期望 3", calls)
	}

	calls = 0
	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv2.Close()
	store2 := cosTestStore(t, srv2.URL)
	store2.Retry = store.Retry
	if err := store2.PutBuffer(context.Background(), "k.jpg", []byte("b"), ""); err == nil {
		t.Fatal("403 应报错")
	}
	if calls != 1 {
		t.Fatalf("4xx 调用次数 = %d，期望 1（不重试）", calls)
	}
}

// TestCOSPublicURL 访问地址：自定义域名优先、缺省标准域名、域名热更新即时生效。
func TestCOSPublicURL(t *testing.T) {
	cfg := NewMapProvider(map[string]string{
		"COS_BUCKET":           "examplebucket-1250000000",
		"COS_REGION":           "ap-beijing",
		"COS_WORKLOG_BASE_URL": "https://j1toolkit.cos.j1net.com/",
	})
	store := &COSStore{Cfg: cfg}
	if got := store.PublicURL("worklog/a.jpg"); got != "https://j1toolkit.cos.j1net.com/worklog/a.jpg" {
		t.Fatalf("PublicURL = %s", got)
	}
	// 域名配置变更即时生效
	cfg.Set("COS_WORKLOG_BASE_URL", "https://new.example.com")
	if got := store.PublicURL("worklog/a.jpg"); got != "https://new.example.com/worklog/a.jpg" {
		t.Fatalf("热更新后 PublicURL = %s", got)
	}
	// 清除自定义域名回退标准域名
	cfg.Delete("COS_WORKLOG_BASE_URL")
	if got := store.PublicURL("worklog/a.jpg"); got != "https://examplebucket-1250000000.cos.ap-beijing.myqcloud.com/worklog/a.jpg" {
		t.Fatalf("标准域名 PublicURL = %s", got)
	}
	// 全未配置返回空串
	empty := &COSStore{Cfg: NewMapProvider(nil)}
	if got := empty.PublicURL("k"); got != "" {
		t.Fatalf("未配置 PublicURL = %q，期望空串", got)
	}
}

// TestCOSPutReturnsURL safeday 口径 Put：返回公开地址。
func TestCOSPutReturnsURL(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	store := cosTestStore(t, srv.URL)
	store.Cfg.(*MapProvider).Set("COS_WORKLOG_BASE_URL", "https://files.example.com")
	u, err := store.Put(context.Background(), "safeday/1.pdf", []byte("x"), "application/pdf")
	if err != nil {
		t.Fatalf("Put = %v", err)
	}
	if u != "https://files.example.com/safeday/1.pdf" {
		t.Fatalf("Put url = %s", u)
	}
}

// TestCOSNotConfigured 缺配置直接报错，不发请求。
func TestCOSNotConfigured(t *testing.T) {
	store := &COSStore{Cfg: NewMapProvider(nil)}
	if err := store.PutBuffer(context.Background(), "k", []byte("b"), ""); err == nil {
		t.Fatal("缺配置应报错")
	}
	if err := store.DeleteObject(context.Background(), "k"); err == nil {
		t.Fatal("缺配置应报错")
	}
}

// TestCamSafeURLPath 编码口径对齐 SDK（UTF-8 字节百分号编码、保留 /）。
func TestCamSafeURLPath(t *testing.T) {
	if got := camSafeURLPath("a b/c!d"); got != "a%20b/c%21d" {
		t.Fatalf("camSafeURLPath = %s", got)
	}
	if got := camSafeURLEncode("image/jpeg"); got != "image%2Fjpeg" {
		t.Fatalf("camSafeURLEncode = %s", got)
	}
}
