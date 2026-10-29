package uvmpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/zhangzhenyu91/j1toolkit/uvmp-toolkit/core-go/internal/auth"
)

// newTestManager 手动凭证模式 Manager（不触发登录流量），登录一次落定凭证。
func newTestManager(t *testing.T, gateway string) *auth.Manager {
	t.Helper()
	mgr := auth.NewManager(auth.Config{
		Gateway:      gateway,
		Mode:         "token",
		Token:        "tok1",
		RefreshToken: "rt1",
		Mtk:          goldenMtk,
	})
	if _, err := mgr.Login(context.Background()); err != nil {
		t.Fatal(err)
	}
	return mgr
}

// newTestClient 零延迟重试 + 固定 rnd/ts 的测试客户端。
func newTestClient(mgr *auth.Manager) *Client {
	c := NewClient(mgr, WithRetryDelay(func(int) time.Duration { return 0 }))
	c.rnd = func() string { return "0123456789abcdef" }
	c.nowMs = func() string { return "1690000000001" }
	return c
}

// TestClientRequestShape 请求头/URL 形态断言（Accept-Encoding: identity、三头、rnd/cl_u_id/_）。
func TestClientRequestShape(t *testing.T) {
	var gotReq *http.Request
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotReq = r
		fmt.Fprint(w, `{"successful":true,"resultValue":{"items":[]}}`)
	}))
	defer srv.Close()

	c := newTestClient(newTestManager(t, srv.URL+"/"))
	_, err := c.Get(context.Background(), "running-service-provider/pcdmanager/",
		map[string]string{"params": `{"pageIndex":1}`}, "测试")
	if err != nil {
		t.Fatal(err)
	}
	h := gotReq.Header
	if h.Get("Accept-Encoding") != "identity" {
		t.Fatalf("Accept-Encoding = %s", h.Get("Accept-Encoding"))
	}
	if h.Get("Auth_Tk") != "tok1" || h.Get("Refresh_Tk") != "rt1" {
		t.Fatalf("凭证头错: %s / %s", h.Get("Auth_Tk"), h.Get("Refresh_Tk"))
	}
	if !strings.HasPrefix(h.Get("X-Content-Token"), "MAC ") {
		t.Fatalf("X-Content-Token = %s", h.Get("X-Content-Token"))
	}
	if h.Get("Origin") != webOrigin || h.Get("Referer") != webOrigin+"/" {
		t.Fatalf("Origin/Referer 错: %s / %s", h.Get("Origin"), h.Get("Referer"))
	}
	if !strings.Contains(gotReq.URL.RawQuery, "rnd=0123456789abcdef") ||
		!strings.Contains(gotReq.URL.RawQuery, "cl_u_id=null") ||
		!strings.Contains(gotReq.URL.RawQuery, "_=1690000000001") {
		t.Fatalf("query 错: %s", gotReq.URL.RawQuery)
	}
	if !strings.Contains(gotReq.URL.RawQuery, "params=") {
		t.Fatalf("params 缺失: %s", gotReq.URL.RawQuery)
	}
}

// TestClient401RefreshRetry 401 + Token-Refresh:true → singleflight 刷新 → 重签重试成功。
func TestClient401RefreshRetry(t *testing.T) {
	var refreshHits atomic.Int32
	var bizHits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/tokenRefresh":
			refreshHits.Add(1)
			fmt.Fprint(w, `{"accessToken":"tok2"}`)
		default:
			bizHits.Add(1)
			if r.Header.Get("Auth_Tk") != "tok2" {
				w.Header().Set("Token-Refresh", "true")
				w.WriteHeader(http.StatusUnauthorized)
				fmt.Fprint(w, `{"msg":"expired"}`)
				return
			}
			fmt.Fprint(w, `{"successful":true,"resultValue":{"ok":1}}`)
		}
	}))
	defer srv.Close()

	c := newTestClient(newTestManager(t, srv.URL+"/"))
	data, err := c.Get(context.Background(), "svc/x", nil, "")
	if err != nil {
		t.Fatal(err)
	}
	rv, _ := data["resultValue"].(Dict)
	if dictInt(rv, "ok") != 1 {
		t.Fatalf("data = %v", data)
	}
	if bizHits.Load() != 2 || refreshHits.Load() != 1 {
		t.Fatalf("biz=%d refresh=%d, want 2/1", bizHits.Load(), refreshHits.Load())
	}
}

// TestClient401NoRefreshHeader 401 无 Token-Refresh 头：立即 AuthError，不刷新不重试。
func TestClient401NoRefreshHeader(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprint(w, `{"msg":"no"}`)
	}))
	defer srv.Close()
	c := newTestClient(newTestManager(t, srv.URL+"/"))
	_, err := c.Get(context.Background(), "svc/x", nil, "desc-x")
	if !auth.IsAuthError(err) {
		t.Fatalf("err = %v, want AuthError", err)
	}
	if !strings.Contains(err.Error(), "凭证校验失败（HTTP 401）") || !strings.Contains(err.Error(), "请求URL：") {
		t.Fatalf("err 文案错: %v", err)
	}
	if hits.Load() != 1 {
		t.Fatalf("hits = %d, want 1", hits.Load())
	}
}

// TestClientRetryThenSuccess 5xx/业务失败退避重试共 retries 次后成功。
func TestClientRetryThenSuccess(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hits.Add(1) <= 2 {
			if hits.Load() == 1 {
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			fmt.Fprint(w, `{"successful":false,"msg":"biz fail"}`)
			return
		}
		fmt.Fprint(w, `{"successful":true}`)
	}))
	defer srv.Close()
	c := newTestClient(newTestManager(t, srv.URL+"/"))
	if _, err := c.Get(context.Background(), "svc/x", nil, ""); err != nil {
		t.Fatal(err)
	}
	if hits.Load() != 3 {
		t.Fatalf("hits = %d, want 3", hits.Load())
	}
}

// TestClientRetryExhausted 连续失败：重试耗尽后报「请求失败（已重试 N 次）」。
func TestClientRetryExhausted(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer srv.Close()
	c := newTestClient(newTestManager(t, srv.URL+"/"))
	_, err := c.Get(context.Background(), "svc/x", nil, "zzz")
	if err == nil || !strings.Contains(err.Error(), "请求失败（已重试 3 次）zzz") {
		t.Fatalf("err = %v", err)
	}
}

// TestClientConcurrent401Singleflight 并发 401：tokenRefresh 只打一次（Manager singleflight），
// 全部请求用新 token 重试成功。
func TestClientConcurrent401Singleflight(t *testing.T) {
	var refreshHits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/tokenRefresh" {
			refreshHits.Add(1)
			time.Sleep(50 * time.Millisecond)
			fmt.Fprint(w, `{"accessToken":"tok2"}`)
			return
		}
		if r.Header.Get("Auth_Tk") != "tok2" {
			w.Header().Set("Token-Refresh", "true")
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		fmt.Fprint(w, `{"successful":true}`)
	}))
	defer srv.Close()

	mgr := newTestManager(t, srv.URL+"/")
	const n = 8
	var wg sync.WaitGroup
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			c := newTestClient(mgr)
			_, errs[i] = c.Get(context.Background(), "svc/x", nil, "")
		}(i)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("goroutine %d: %v", i, err)
		}
	}
	if refreshHits.Load() != 1 {
		t.Fatalf("tokenRefresh 命中 %d 次, want 1", refreshHits.Load())
	}
}

// TestClipStrRune clipStr 按 rune 截断（Python s[:n] 字符口径），不截裂 UTF-8。
func TestClipStrRune(t *testing.T) {
	if got := clipStr([]byte("派车单管理abc"), 3); got != "派车单" {
		t.Fatalf("clipStr 3 = %q, want 派车单", got)
	}
	if got := clipStr([]byte("派车单管理"), 10); got != "派车单管理" {
		t.Fatalf("clipStr 10 = %q, want 原文", got)
	}
	if got := clipStr([]byte("abcdef"), 3); got != "abc" {
		t.Fatalf("clipStr ASCII = %q, want abc", got)
	}
}

// TestClientPostSignGolden POST 签名对 body 按 Python `str(v) if v else v` 口径转换后，
// 与 Python 离线复算的黄金向量逐字节一致（false/null/0/空容器原类型、1.0→"1.0"、
// true→"True"、Unicode 原文）。body 的 HTTP 序列化仍走 marshalNoHTMLEscape，与签名口径分离。
func TestClientPostSignGolden(t *testing.T) {
	var gotHeader string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeader = r.Header.Get("X-Content-Token")
		fmt.Fprint(w, `{"successful":true}`)
	}))
	defer srv.Close()
	mgr := auth.NewManager(auth.Config{
		Gateway:      srv.URL + "/",
		Mode:         "token",
		Token:        "py.token-01",
		RefreshToken: "rt1",
		Mtk:          goldenMtk,
	})
	if _, err := mgr.Login(context.Background()); err != nil {
		t.Fatal(err)
	}
	c := newTestClient(mgr)
	body := map[string]any{
		"b":     false,
		"n":     nil,
		"z":     0,
		"f":     1.0,
		"t":     true,
		"elist": []any{},
		"edict": map[string]any{},
		"u":     "派车单管理",
	}
	if _, err := c.Post(context.Background(), "zuul/common-service-provider/save", body, "POST 黄金"); err != nil {
		t.Fatal(err)
	}
	// 与 TestMakeXContentTokenPythonSemantics 同一向量（temp_url=/zuul/common-service-provider/save）
	const want = "MAC fw0PiPkV2hiqRKNgv8vU1pRc-Ow8X31FNl5JXb_fP3I"
	if gotHeader != want {
		t.Fatalf("X-Content-Token = %s, want %s", gotHeader, want)
	}
}

// TestClientJSONUseNumber JSON 响应数字用 json.Number 解码：大整数单据 ID
// 过 float64 会丢精度（1234567890123456789 → 1234567890123456768），
// UseNumber 后原样保留；dictInt 对 json.Number 同样可用。
func TestClientJSONUseNumber(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"successful":true,"resultValue":{"items":[`+
			`{"id":1234567890123456789,"big":9007199254740993,"pageSize":50}]}}`)
	}))
	defer srv.Close()

	c := newTestClient(newTestManager(t, srv.URL+"/"))
	data, err := c.Get(context.Background(), "running-service-provider/pcdmanager/",
		map[string]string{"params": `{"pageIndex":1}`}, "大整数")
	if err != nil {
		t.Fatal(err)
	}
	rv, _ := data["resultValue"].(map[string]any)
	items, _ := rv["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("items = %v", items)
	}
	it, _ := items[0].(map[string]any)
	id, ok := it["id"].(json.Number)
	if !ok {
		t.Fatalf("id 应为 json.Number: %T", it["id"])
	}
	if id.String() != "1234567890123456789" {
		t.Fatalf("大整数 ID 丢精度: %s", id)
	}
	big, ok := it["big"].(json.Number)
	if !ok || big.String() != "9007199254740993" {
		t.Fatalf("2^53+1 应原样保留: %T %v", it["big"], it["big"])
	}
	if n := dictInt(it, "pageSize"); n != 50 {
		t.Fatalf("dictInt(json.Number) = %d, want 50", n)
	}
}
