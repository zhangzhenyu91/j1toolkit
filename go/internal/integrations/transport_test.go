package integrations

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"
	"time"
)

// roundTripFunc 测试用 RoundTripper（模拟被替换的 http.DefaultTransport）。
type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestNewSharedTransportPoolSettings(t *testing.T) {
	rt := NewSharedTransport()
	if rt.Base.MaxIdleConns != 128 {
		t.Fatalf("MaxIdleConns = %d，期望 128", rt.Base.MaxIdleConns)
	}
	if rt.Base.MaxIdleConnsPerHost != 32 {
		t.Fatalf("MaxIdleConnsPerHost = %d，期望 32", rt.Base.MaxIdleConnsPerHost)
	}
	if rt.Base.IdleConnTimeout != 90*time.Second {
		t.Fatalf("IdleConnTimeout = %v，期望 90s", rt.Base.IdleConnTimeout)
	}
	if rt.Base.Proxy == nil {
		t.Fatal("共享传输层应保留环境代理探测")
	}
	if rt.Base.DialContext == nil {
		t.Fatal("共享传输层应保留默认拨号器")
	}
	if rt.Base.DisableCompression {
		t.Fatal("共享传输层不应关闭压缩")
	}
	if rt == http.DefaultTransport {
		t.Fatal("工厂不应直接返回全局默认传输层")
	}
	// 导出常量与实现保持一致（Task 11 装配/文档引用）
	if TransportMaxIdleConns != 128 || TransportMaxIdleConnsPerHost != 32 ||
		TransportIdleConnTimeout != 90*time.Second {
		t.Fatalf("共享传输层常量不一致: %d/%d/%v",
			TransportMaxIdleConns, TransportMaxIdleConnsPerHost, TransportIdleConnTimeout)
	}
	if ProxyTransportMaxIdleConns != 64 || ProxyTransportMaxIdleConnsPerHost != 16 {
		t.Fatalf("代理传输层常量不一致: %d/%d",
			ProxyTransportMaxIdleConns, ProxyTransportMaxIdleConnsPerHost)
	}
}

func TestSetDefaultTransportConnReuse(t *testing.T) {
	old := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = old })

	var newConns int32
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "ok")
	}))
	srv.Config.ConnState = func(_ net.Conn, state http.ConnState) {
		if state == http.StateNew {
			atomic.AddInt32(&newConns, 1)
		}
	}
	srv.Start()
	t.Cleanup(srv.Close)

	rt := SetDefaultTransport()
	if http.DefaultTransport != rt {
		t.Fatal("SetDefaultTransport 未安装为 http.DefaultTransport")
	}
	t.Cleanup(rt.CloseIdleConnections)

	// 现有默认 client（空 Transport）应全部复用共享传输层与长连接
	client := &http.Client{}
	for i := 0; i < 3; i++ {
		resp, err := client.Get(srv.URL)
		if err != nil {
			t.Fatalf("第 %d 次请求: %v", i+1, err)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}
	if got := atomic.LoadInt32(&newConns); got != 1 {
		t.Fatalf("新建连接数 = %d，期望 1（连接复用）", got)
	}
}

func TestSharedTransportFallbackWhenDefaultNotTransport(t *testing.T) {
	old := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = old })
	http.DefaultTransport = roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, nil
	})

	rt := NewSharedTransport()
	if rt == nil {
		t.Fatal("默认传输层非 *http.Transport 时应回退到内置默认参数")
	}
	if rt.Base.Proxy == nil || rt.Base.DialContext == nil {
		t.Fatal("回退传输层应保留代理与拨号器")
	}
	if rt.Base.MaxIdleConns != 128 || rt.Base.MaxIdleConnsPerHost != 32 || rt.Base.IdleConnTimeout != 90*time.Second {
		t.Fatalf("回退传输层池参数 = %d/%d/%v", rt.Base.MaxIdleConns, rt.Base.MaxIdleConnsPerHost, rt.Base.IdleConnTimeout)
	}
}

func TestDirectTransport(t *testing.T) {
	rt := NewDirectTransport()
	if rt.Base.Proxy != nil {
		t.Fatal("直连传输层必须 Proxy=nil（不继承进程 HTTP_PROXY）")
	}
	if !rt.Base.DisableCompression {
		t.Fatal("直连传输层应关闭压缩协商")
	}
	if rt.Base.MaxIdleConns != TransportMaxIdleConns ||
		rt.Base.MaxIdleConnsPerHost != TransportMaxIdleConnsPerHost ||
		rt.Base.IdleConnTimeout != TransportIdleConnTimeout {
		t.Fatalf("直连传输层池参数 = %d/%d/%v", rt.Base.MaxIdleConns, rt.Base.MaxIdleConnsPerHost, rt.Base.IdleConnTimeout)
	}
	if rt.Base.DialContext == nil || rt.Base.TLSHandshakeTimeout <= 0 {
		t.Fatal("直连传输层应保留拨号器与 TLS 握手超时")
	}
}

func TestProxyTransport(t *testing.T) {
	proxyURL, err := url.Parse("http://127.0.0.1:3128")
	if err != nil {
		t.Fatalf("解析代理地址: %v", err)
	}
	rt := NewProxyTransport(proxyURL)
	if rt.Base.MaxIdleConns != 64 || rt.Base.MaxIdleConnsPerHost != 16 {
		t.Fatalf("代理传输层池参数 = %d/%d，期望 64/16", rt.Base.MaxIdleConns, rt.Base.MaxIdleConnsPerHost)
	}
	if rt.Base.IdleConnTimeout != TransportIdleConnTimeout {
		t.Fatalf("代理传输层 IdleConnTimeout = %v", rt.Base.IdleConnTimeout)
	}
	req, err := http.NewRequest(http.MethodGet, "http://example.invalid/x", nil)
	if err != nil {
		t.Fatalf("构造请求: %v", err)
	}
	pu, err := rt.Base.Proxy(req)
	if err != nil || pu == nil {
		t.Fatalf("Proxy(req) = %v %v", pu, err)
	}
	if pu.String() != proxyURL.String() {
		t.Fatalf("代理地址 = %s，期望 %s", pu, proxyURL)
	}
}

func TestDialTransport(t *testing.T) {
	var called bool
	rt := NewDialTransport(func(context.Context, string, string) (net.Conn, error) {
		called = true
		return nil, context.Canceled
	})
	if rt.Base.Proxy != nil {
		t.Fatal("自定义拨号传输层必须 Proxy=nil")
	}
	if rt.Base.MaxIdleConns != 64 || rt.Base.MaxIdleConnsPerHost != 16 {
		t.Fatalf("自定义拨号传输层池参数 = %d/%d，期望 64/16", rt.Base.MaxIdleConns, rt.Base.MaxIdleConnsPerHost)
	}
	if rt.Base.DialContext == nil {
		t.Fatal("自定义拨号传输层缺少 DialContext")
	}
	_, _ = rt.Base.DialContext(context.Background(), "tcp", "example.invalid:80")
	if !called {
		t.Fatal("DialContext 未使用注入的拨号函数")
	}
}
