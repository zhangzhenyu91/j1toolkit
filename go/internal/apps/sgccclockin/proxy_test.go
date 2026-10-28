package sgccclockin

// 出口代理测试：socks5/http 代理双方案、传输层缓存热重建、非法地址报错不静默降级。
// 极简 SOCKS5 桩服务本地转发，不触真实商旅。

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// socks5Stub 极简 SOCKS5 桩（no-auth；CONNECT 后固定转发到 target 并双向拷贝）。
func socks5Stub(t *testing.T, target string) (addr string, closeFn func()) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("监听: %v", err)
	}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go handleSocks5(c, target)
		}
	}()
	return ln.Addr().String(), func() {
		// 只关监听不等连接收尾：http keep-alive 下管道拷贝协程会挂到连接复用结束
		_ = ln.Close()
	}
}

func handleSocks5(c net.Conn, target string) {
	defer c.Close()
	buf := make([]byte, 512)
	// 问候：VER NMETHODS METHODS…
	if _, err := io.ReadFull(c, buf[:2]); err != nil {
		return
	}
	n := int(buf[1])
	if _, err := io.ReadFull(c, buf[:n]); err != nil {
		return
	}
	_, _ = c.Write([]byte{0x05, 0x00}) // 选 no-auth
	// 请求：VER CMD RSV ATYP ADDR PORT
	if _, err := io.ReadFull(c, buf[:4]); err != nil {
		return
	}
	switch buf[3] {
	case 0x01:
		if _, err := io.ReadFull(c, buf[:4]); err != nil {
			return
		}
	case 0x03:
		if _, err := io.ReadFull(c, buf[:1]); err != nil {
			return
		}
		l := int(buf[0])
		if _, err := io.ReadFull(c, buf[:l]); err != nil {
			return
		}
	case 0x04:
		if _, err := io.ReadFull(c, buf[:16]); err != nil {
			return
		}
	}
	if _, err := io.ReadFull(c, buf[:2]); err != nil {
		return
	}
	up, err := net.Dial("tcp", target)
	if err != nil {
		_, _ = c.Write([]byte{0x05, 0x05, 0x00, 0x01, 0, 0, 0, 0, 0, 0})
		return
	}
	defer up.Close()
	_, _ = c.Write([]byte{0x05, 0x00, 0x00, 0x01, 0, 0, 0, 0, 0, 0})
	go func() { _, _ = io.Copy(up, c) }()
	_, _ = io.Copy(c, up)
}

// TestBuildTransportSchemes 代理 scheme 分流：socks5/http/https 可构造，其余报错。
func TestBuildTransportSchemes(t *testing.T) {
	for _, u := range []string{
		"socks5://127.0.0.1:1080",
		"socks5h://user:pass@127.0.0.1:1080",
		"http://127.0.0.1:8080",
		"https://127.0.0.1:8443",
	} {
		if rt, err := buildTransport(u); err != nil || rt == nil {
			t.Fatalf("%s 应可构造: %v", u, err)
		}
	}
	if _, err := buildTransport("ftp://127.0.0.1"); err == nil {
		t.Fatalf("不支持的 scheme 应报错")
	}
	if _, err := buildTransport("://bad"); err == nil {
		t.Fatalf("非法地址应报错")
	}
}

// TestPostViaSOCKS5 经 SOCKS5 代理发出的请求抵达目标（x/net/proxy 拨号链路端到端）。
func TestPostViaSOCKS5(t *testing.T) {
	var gotBody string
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		_, _ = w.Write([]byte("ok"))
	}))
	defer target.Close()
	addr, closeStub := socks5Stub(t, strings.TrimPrefix(target.URL, "http://"))
	defer closeStub()

	p := NewProtocol(testKeys(t))
	p.Scheme = "http"
	status, raw, err := p.post(context.Background(), "socks5://"+addr,
		strings.TrimPrefix(target.URL, "http://"), "ping", map[string]string{"X-Test": "1"}, "hello")
	if err != nil {
		t.Fatalf("经 SOCKS5 代理请求: %v", err)
	}
	if status != 200 || raw != "ok" || gotBody != "hello" {
		t.Fatalf("代理链路结果不符: status=%d raw=%q body=%q", status, raw, gotBody)
	}
}

// TestPostViaHTTPProxy http 代理：请求以绝对 URI 抵达代理（CONNECT/转发口径）。
func TestPostViaHTTPProxy(t *testing.T) {
	var gotReqURI string
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotReqURI = r.RequestURI
		_, _ = w.Write([]byte("proxied"))
	}))
	defer proxy.Close()

	p := NewProtocol(testKeys(t))
	p.Scheme = "http"
	status, raw, err := p.post(context.Background(), proxy.URL,
		"gwslapi.esgcc.com.cn", "ads", map[string]string{}, "x")
	if err != nil {
		t.Fatalf("经 http 代理请求: %v", err)
	}
	if status != 200 || raw != "proxied" {
		t.Fatalf("http 代理链路结果不符: status=%d raw=%q", status, raw)
	}
	if !strings.Contains(gotReqURI, "http://gwslapi.esgcc.com.cn/api/ads") {
		t.Fatalf("代理应收到绝对 URI: %q", gotReqURI)
	}
}

// TestPostSuppressesDefaultUserAgent 对齐 Node：不得让 Go 默认 User-Agent 触发商旅网关 302。
func TestPostSuppressesDefaultUserAgent(t *testing.T) {
	var userAgent string
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		userAgent = r.Header.Get("User-Agent")
		_, _ = w.Write([]byte("ok"))
	}))
	defer target.Close()

	p := NewProtocol(testKeys(t))
	p.Scheme = "http"
	status, _, err := p.post(context.Background(), "", strings.TrimPrefix(target.URL, "http://"), "ping", nil, "x")
	if err != nil || status != 200 {
		t.Fatalf("请求失败: status=%d err=%v", status, err)
	}
	if userAgent != "" {
		t.Fatalf("不应发送默认 User-Agent，实际=%q", userAgent)
	}

	status, _, err = p.post(context.Background(), "", strings.TrimPrefix(target.URL, "http://"), "ping",
		map[string]string{"User-Agent": "guo wang shang lu yun/3.3.7"}, "x")
	if err != nil || status != 200 {
		t.Fatalf("显式 App UA 请求失败: status=%d err=%v", status, err)
	}
	if userAgent != "guo wang shang lu yun/3.3.7" {
		t.Fatalf("调用方显式 App UA 必须保留，实际=%q", userAgent)
	}
}

// TestPostDirectIgnoresEnvironmentProxy 未配置 SGCC_PROXY_URL 时不得被进程环境变量带去其它代理。
func TestPostDirectIgnoresEnvironmentProxy(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("direct"))
	}))
	defer target.Close()
	envProxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte("unexpected environment proxy"))
	}))
	defer envProxy.Close()

	oldTransport := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = oldTransport })
	targetAddr := strings.TrimPrefix(target.URL, "http://")
	proxyAddr := strings.TrimPrefix(envProxy.URL, "http://")
	http.DefaultTransport = &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: func(_ context.Context, network, addr string) (net.Conn, error) {
			if addr == proxyAddr {
				return net.Dial(network, proxyAddr)
			}
			return net.Dial(network, targetAddr)
		},
	}
	t.Setenv("HTTP_PROXY", envProxy.URL)
	t.Setenv("HTTPS_PROXY", envProxy.URL)

	p := NewProtocol(testKeys(t))
	p.Scheme = "http"
	status, raw, err := p.post(context.Background(), "", "sgcc-direct.test", "ping", nil, "x")
	if err != nil || status != 200 || raw != "direct" {
		t.Fatalf("未显式配置代理应直连且忽略环境代理: status=%d raw=%q err=%v", status, raw, err)
	}
}

// TestPostDoesNotFollowRedirect 与 Node https.request 一致：不得自动跳往 Location 的第三地址。
func TestPostDoesNotFollowRedirect(t *testing.T) {
	hits := 0
	final := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits++
		_, _ = w.Write([]byte("unexpected redirect target"))
	}))
	defer final.Close()
	first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, final.URL+"/?ticket=do-not-log", http.StatusFound)
	}))
	defer first.Close()

	p := NewProtocol(testKeys(t))
	p.Scheme = "http"
	_, _, err := p.post(context.Background(), "", strings.TrimPrefix(first.URL, "http://"), "ping", nil, "x")
	if err == nil || !strings.Contains(err.Error(), "HTTP 302") {
		t.Fatalf("重定向应作为明确错误停在原响应: %v", err)
	}
	if hits != 0 {
		t.Fatalf("不得请求 Location 的第三地址，实际访问 %d 次", hits)
	}
	if strings.Contains(err.Error(), "do-not-log") {
		t.Fatalf("重定向错误不得携带 query 敏感值: %v", err)
	}
}

// TestProxyHotSwap 代理地址变更后传输层热重建（先走代理 A，再切代理 B，各自收到流量）。
func TestProxyHotSwap(t *testing.T) {
	var hitsA, hitsB int
	var mu sync.Mutex
	proxyA := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hitsA++
		mu.Unlock()
		_, _ = w.Write([]byte("a"))
	}))
	defer proxyA.Close()
	proxyB := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hitsB++
		mu.Unlock()
		_, _ = w.Write([]byte("b"))
	}))
	defer proxyB.Close()

	p := NewProtocol(testKeys(t))
	p.Scheme = "http"
	ctx := context.Background()
	if _, _, err := p.post(ctx, proxyA.URL, "gwslapi.esgcc.com.cn", "x", nil, "1"); err != nil {
		t.Fatalf("代理A: %v", err)
	}
	if _, _, err := p.post(ctx, proxyA.URL, "gwslapi.esgcc.com.cn", "x", nil, "2"); err != nil {
		t.Fatalf("代理A 复用: %v", err)
	}
	// 热切换到代理 B
	if _, _, err := p.post(ctx, proxyB.URL, "gwslapi.esgcc.com.cn", "x", nil, "3"); err != nil {
		t.Fatalf("代理B: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if hitsA != 2 || hitsB != 1 {
		t.Fatalf("代理热切换计数不符: A=%d B=%d", hitsA, hitsB)
	}
}

// TestProbeProxyResolved 探测读运行时配置代理（未配置 skipped；配置后 TCP 建连判活）。
func TestProbeProxyResolved(t *testing.T) {
	p := NewProtocol(testKeys(t))
	ok, skipped, _ := p.ProbeProxy()
	if !ok || !skipped {
		t.Fatalf("未配置代理应 skipped: ok=%v skipped=%v", ok, skipped)
	}
	// 配一个真实 TCP 端口（httptest 监听即存活）
	probe := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer probe.Close()
	cfg := newMapCfg()
	cfg.set(CfgProxyURL, probe.URL) // http://127.0.0.1:port
	p.Cfg = cfg
	ok, skipped, errMsg := p.ProbeProxy()
	if !ok || skipped {
		t.Fatalf("代理存活应 ok: ok=%v skipped=%v err=%s", ok, skipped, errMsg)
	}
	// 关端口后应判异常
	probe.Close()
	time.Sleep(10 * time.Millisecond)
	ok, skipped, _ = p.ProbeProxy()
	if ok || skipped {
		t.Fatalf("代理不可达应判异常: ok=%v skipped=%v", ok, skipped)
	}
}
