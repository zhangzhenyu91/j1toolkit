package integrations

// 出站观测：只记 method/service/host/path/status/duration/attempt/bytes/error_class，
// 不读 body（SSE/大文件不缓冲）；4xx/5xx/网络错误每次尝试一条，2xx 不重复成功事件；
// 四个 Transport 工厂统一包装并保留独立 base 的代理/拨号参数。

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"shade/internal/logging"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// fieldText 把事件字段拼成可检索文本。
func fieldText(fields map[string]any) string {
	return fmt.Sprint(fields)
}

func TestObservedTransportRecordsFailuresOnly(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ok":
			_, _ = io.WriteString(w, "ok")
		case "/missing":
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, "nope")
		case "/boom":
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = io.WriteString(w, "boom")
		}
	}))
	defer srv.Close()

	rec := &fakeRecorder{}
	rt := NewSharedTransport()
	rt.Recorder = rec
	client := &http.Client{Transport: rt}

	for _, path := range []string{"/ok", "/missing", "/boom?token=sekrit"} {
		resp, err := client.Get(srv.URL + path)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}

	events := rec.snapshot()
	if len(events) != 3 {
		t.Fatalf("事件 %d 条，期望每次尝试一条（2xx/404/500）：%+v", len(events), events)
	}
	okEvent := events[0]
	if okEvent.event != "http.outbound" || okEvent.level != "info" || okEvent.fields["status"] != 200 {
		t.Fatalf("2xx 轻量事件形态不符：%+v", okEvent)
	}
	notFound := events[1]
	if notFound.event != "http.outbound.failed" || notFound.level != "warn" || notFound.category != externalCategory {
		t.Fatalf("404 事件形态不符：%+v", notFound)
	}
	wantFields := map[string]any{
		"method": "GET", "service": "http", "path": "/missing", "status": 404,
		"attempt": 1, "error_class": classHTTPStatus,
	}
	for key, want := range wantFields {
		if notFound.fields[key] != want {
			t.Fatalf("404 字段 %s = %v，期望 %v：%v", key, notFound.fields[key], want, notFound.fields)
		}
	}
	if notFound.fields["host"] == "" {
		t.Fatalf("404 缺 host：%v", notFound.fields)
	}
	if _, ok := notFound.fields["duration_ms"].(float64); !ok {
		t.Fatalf("404 缺 duration_ms：%v", notFound.fields)
	}
	if strings.Contains(fieldText(notFound.fields)+notFound.raw, "sekrit") {
		t.Fatalf("query 秘密泄漏：%v / %s", notFound.fields, notFound.raw)
	}
	if strings.Contains(notFound.raw, "?") {
		t.Fatalf("raw 不应包含 query：%s", notFound.raw)
	}

	boom := events[2]
	if boom.event != "http.outbound.failed" || boom.level != "error" || boom.fields["status"] != 500 {
		t.Fatalf("500 事件形态不符：%+v", boom)
	}
	if boom.fields["bytes"] != int64(4) {
		t.Fatalf("500 bytes = %v，期望 4", boom.fields["bytes"])
	}
}

func TestObservedTransport2xxCarriesTrace(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "ok")
	}))
	defer srv.Close()
	rec := &fakeRecorder{}
	rt := NewSharedTransport()
	rt.Recorder = rec
	req, _ := http.NewRequestWithContext(logging.WithTraceID(context.Background(), "trace-1"), http.MethodGet, srv.URL+"/ok", nil)
	resp, err := rt.RoundTrip(req)
	if err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	_ = resp.Body.Close()
	events := rec.snapshot()
	if len(events) != 1 {
		t.Fatalf("2xx 事件 = %d", len(events))
	}
	if events[0].fields["trace_id"] != "trace-1" || events[0].fields["status"] != 200 {
		t.Fatalf("2xx 事件缺 trace/status：%v", events[0].fields)
	}
}

func TestObservedTransportNetworkError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	addr := srv.URL
	srv.Close()

	rec := &fakeRecorder{}
	rt := NewSharedTransport()
	rt.Recorder = rec
	client := &http.Client{Transport: rt, Timeout: 2 * time.Second}
	if _, err := client.Get(addr + "/x?token=sekrit"); err == nil {
		t.Fatal("期望网络错误")
	}
	events := rec.snapshot()
	if len(events) != 1 {
		t.Fatalf("事件 %d 条，期望 1：%+v", len(events), events)
	}
	e := events[0]
	if e.event != "http.outbound.error" || e.level != "error" {
		t.Fatalf("网络错误事件形态不符：%+v", e)
	}
	if e.fields["status"] != 0 || e.fields["error_class"] != classNetwork {
		t.Fatalf("网络错误字段不符：%v", e.fields)
	}
	if strings.Contains(fieldText(e.fields)+e.raw, "sekrit") {
		t.Fatalf("query 秘密泄漏：%v / %s", e.fields, e.raw)
	}
}

func TestObservedTransportDoesNotBufferLargeBody(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	var writes atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Length", "1000000")
		w.WriteHeader(http.StatusOK)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		close(started)
		<-release
		for i := 0; i < 1000; i++ {
			if _, err := io.WriteString(w, strings.Repeat("x", 1000)); err != nil {
				return
			}
			writes.Add(1)
		}
	}))
	defer srv.Close()

	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/big", nil)
	rt := NewSharedTransport()
	done := make(chan *http.Response, 1)
	go func() {
		resp, err := rt.RoundTrip(req)
		if err != nil {
			t.Errorf("RoundTrip: %v", err)
			close(done)
			return
		}
		done <- resp
	}()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("请求未到达服务端")
	}
	var resp *http.Response
	select {
	case resp = <-done:
	case <-time.After(2 * time.Second):
		close(release)
		t.Fatal("RoundTrip 阻塞在响应 body（observer 不得缓冲/读取 body）")
	}
	if resp == nil {
		t.Fatal("RoundTrip 返回空响应")
	}
	close(release)
	buf, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err != nil || len(buf) != 1000000 {
		t.Fatalf("body = %d, %v，期望 1000000", len(buf), err)
	}
	if got := writes.Load(); got != 1000 {
		t.Fatalf("服务端写入次数 = %d，期望 1000", got)
	}
}

func TestObservedTransportSSEStreamsWithoutBuffering(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "data: first\n\n")
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		select {
		case <-r.Context().Done():
			return
		case <-time.After(2 * time.Second):
		}
		_, _ = io.WriteString(w, "data: second\n\n")
	}))
	defer srv.Close()

	rt := NewSharedTransport()
	client := &http.Client{Transport: rt, Timeout: 3 * time.Second}
	resp, err := client.Get(srv.URL + "/sse")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	reader := bufio.NewReader(resp.Body)
	line, err := reader.ReadString('\n')
	if err != nil || !strings.Contains(line, "first") {
		t.Fatalf("首帧 = %q, %v（不应等待流结束）", line, err)
	}
}

func TestObservedTransportServiceAndAttemptFromContext(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer srv.Close()
	rec := &fakeRecorder{}
	rt := NewSharedTransport()
	rt.Recorder = rec
	req, _ := http.NewRequestWithContext(WithAttempt(WithServiceName(context.Background(), "dify"), 3), http.MethodPost, srv.URL+"/v1", nil)
	resp, err := rt.RoundTrip(req)
	if err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	_ = resp.Body.Close()
	e := rec.snapshot()[0]
	if e.fields["service"] != "dify" || e.fields["attempt"] != 3 || e.fields["method"] != "POST" {
		t.Fatalf("service/attempt 未从 ctx 取：%v", e.fields)
	}
}

func TestObservedTransportGlobalRecorder(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()
	rec := &fakeRecorder{}
	SetObservedRecorder(rec)
	t.Cleanup(func() { SetObservedRecorder(nil) })
	rt := NewSharedTransport() // 不显式注入 Recorder，取全局
	resp, err := (&http.Client{Transport: rt}).Get(srv.URL + "/x")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	_ = resp.Body.Close()
	if len(rec.snapshot()) != 1 {
		t.Fatal("全局 Recorder 未生效")
	}
}

func TestRetryPolicyPropagatesAttempt(t *testing.T) {
	var got []int
	policy := RetryPolicy{
		MaxAttempts: 3,
		Sleep:       func(context.Context, time.Duration) error { return nil },
	}
	err := policy.Do(context.Background(), func(error) bool { return true }, func(ctx context.Context, attempt int) error {
		got = append(got, attempt)
		if n := AttemptFromContext(ctx); n != attempt {
			t.Fatalf("ctx attempt = %d，期望 %d", n, attempt)
		}
		return &statusError{Op: "x", StatusCode: 500}
	})
	if err == nil || len(got) != 3 || got[0] != 1 || got[1] != 2 || got[2] != 3 {
		t.Fatalf("attempt 序列 = %v，err=%v", got, err)
	}
}

func TestTransportFactoriesWrapAndPreserveSettings(t *testing.T) {
	shared := NewSharedTransport()
	if shared.Base == nil {
		t.Fatal("共享传输层缺 base")
	}
	if shared.Base.MaxIdleConns != TransportMaxIdleConns || shared.Base.MaxIdleConnsPerHost != TransportMaxIdleConnsPerHost ||
		shared.Base.IdleConnTimeout != TransportIdleConnTimeout {
		t.Fatalf("共享池参数丢失：%+v", shared.Base)
	}
	if shared.Base.Proxy == nil || shared.Base.DialContext == nil {
		t.Fatal("共享传输层应保留环境代理与拨号器")
	}

	proxyURL, _ := url.Parse("http://127.0.0.1:3128")
	proxied := NewProxyTransport(proxyURL)
	if proxied.Base == nil || proxied.Base.Proxy == nil {
		t.Fatal("代理工厂丢失代理参数")
	}
	req, _ := http.NewRequest(http.MethodGet, "http://example.invalid/x", nil)
	pu, err := proxied.Base.Proxy(req)
	if err != nil || pu == nil || pu.String() != proxyURL.String() {
		t.Fatalf("代理地址 = %v, %v", pu, err)
	}
	if proxied.Base.MaxIdleConns != ProxyTransportMaxIdleConns || proxied.Base.MaxIdleConnsPerHost != ProxyTransportMaxIdleConnsPerHost {
		t.Fatalf("代理池参数丢失：%+v", proxied.Base)
	}

	direct := NewDirectTransport()
	if direct.Base == nil || direct.Base.Proxy != nil || !direct.Base.DisableCompression {
		t.Fatal("直连工厂丢失 Proxy=nil/压缩设置")
	}

	var dialed bool
	dial := func(context.Context, string, string) (net.Conn, error) {
		dialed = true
		return nil, context.Canceled
	}
	dialRT := NewDialTransport(dial)
	if dialRT.Base == nil || dialRT.Base.DialContext == nil || dialRT.Base.Proxy != nil {
		t.Fatal("拨号工厂丢失 DialContext/Proxy 设置")
	}
	_, _ = dialRT.Base.DialContext(context.Background(), "tcp", "example.invalid:80")
	if !dialed {
		t.Fatal("DialContext 未使用注入拨号函数")
	}
}

func TestSetDefaultTransportKeepsBaseAfterWrapping(t *testing.T) {
	old := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = old })
	_ = SetDefaultTransport()
	observed, ok := http.DefaultTransport.(*ObservedTransport)
	if !ok || observed.Base == nil {
		t.Fatalf("默认传输层未包装：%T", http.DefaultTransport)
	}
	// 包装后再建工厂不得把包装层当 base（否则再次类型断言失败会丢代理参数）。
	next := NewSharedTransport()
	if next.Base == nil || next.Base.Proxy == nil || next.Base.DialContext == nil {
		t.Fatalf("包装后派生丢失代理/拨号：%+v", next.Base)
	}
	if next.Base.MaxIdleConns != TransportMaxIdleConns {
		t.Fatalf("派生池参数 = %d", next.Base.MaxIdleConns)
	}
}

func TestObservedTransportNilRecorderNoop(t *testing.T) {
	rt := &ObservedTransport{Base: &http.Transport{}}
	req, _ := http.NewRequest(http.MethodGet, "http://example.invalid/x", nil)
	if _, err := rt.RoundTrip(req); err == nil {
		t.Fatal("期望连接错误")
	}
}

func TestObservedTransportChunkedBytesUnknown(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		_, _ = io.WriteString(w, "x")
	}))
	defer srv.Close()
	rec := &fakeRecorder{}
	rt := NewSharedTransport()
	rt.Recorder = rec
	resp, err := (&http.Client{Transport: rt}).Get(srv.URL + "/chunked")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	events := rec.snapshot()
	if len(events) != 1 {
		t.Fatalf("事件 = %d", len(events))
	}
	if events[0].fields["bytes"] != int64(-1) {
		t.Fatalf("chunked bytes = %v，期望 -1（未知）", events[0].fields["bytes"])
	}
}

func TestWrapObservedNilInheritsDefaults(t *testing.T) {
	old := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = old })
	std := http.DefaultTransport.(*http.Transport).Clone()
	std.Proxy = http.ProxyFromEnvironment
	std.DialContext = (&net.Dialer{}).DialContext
	http.DefaultTransport = std
	rt := WrapObserved(nil)
	if rt.Base == nil || rt.Base.Proxy == nil || rt.Base.DialContext == nil {
		t.Fatalf("WrapObserved(nil) 必须继承标准默认参数：%+v", rt.Base)
	}
	if rt.Base.MaxIdleConns != 100 || rt.Base.IdleConnTimeout != 90*time.Second {
		t.Fatalf("默认池参数不符：%+v", rt.Base)
	}
}
