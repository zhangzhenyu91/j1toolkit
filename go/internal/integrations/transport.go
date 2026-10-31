// 共享 HTTP 传输层工厂：全进程复用一套调大后的连接池，替代各客户端零散新建的默认
// 传输层。生产装配在 main 启动最早处调用 SetDefaultTransport，现有 http.Client{}
// （Transport 为 nil）即自动复用；需要独立口径的通道（商旅直连/出口代理）用下列工厂派生，
// 各实例连接池互不共享。
//
// 所有工厂返回 *ObservedTransport：包装层只观测失败调用，Base 保留独立 *http.Transport，
// 代理/拨号/池参数不因包装丢失；cloneBaseTransport 识别包装类型，包装后再派生仍取真实 base。
package integrations

import (
	"context"
	"net"
	"net/http"
	"net/url"
	"time"
)

// 共享传输层连接池参数：提升默认每主机空闲连接数（标准库默认仅 2），
// 避免高并发下反复建连/断连（Dify、COS、高德、WeKnora 等默认 client 共用）。
const (
	TransportMaxIdleConns        = 128
	TransportMaxIdleConnsPerHost = 32
	TransportIdleConnTimeout     = 90 * time.Second

	// ProxyTransportMaxIdleConns / ProxyTransportMaxIdleConnsPerHost 单出口代理连接池上限：
	// 代理链路并发与出口稳定性有限，池子刻意小于共享传输层。
	ProxyTransportMaxIdleConns        = 64
	ProxyTransportMaxIdleConnsPerHost = 16
)

// NewSharedTransport 构造共享传输层：连接池 128/32、空闲连接 90s，其余参数（环境代理、
// 拨号器、TLS 握手超时、HTTP/2）沿用 http.DefaultTransport。返回独立实例，不修改全局默认。
func NewSharedTransport() *ObservedTransport {
	return WrapObserved(newSharedBase())
}

// SetDefaultTransport 用共享传输层替换 http.DefaultTransport 并返回该实例
// （main 启动装配调用；返回值供退出时 CloseIdleConnections）。现有 http.Client{} 立即复用。
func SetDefaultTransport() *ObservedTransport {
	rt := NewSharedTransport()
	http.DefaultTransport = rt
	return rt
}

// NewDirectTransport 直连传输层：在共享参数上取消环境代理探测（Proxy=nil）并关闭压缩协商。
// 商旅打卡 Node 链路同样不继承进程 HTTP_PROXY、不协商 gzip，避免网关按压缩头分流应答。
func NewDirectTransport() *ObservedTransport {
	base := newSharedBase()
	base.Proxy = nil
	base.DisableCompression = true
	return WrapObserved(base)
}

// NewProxyTransport HTTP/HTTPS CONNECT 代理传输层：在共享参数上套用代理，
// 连接池收敛为 64/16；proxyURL 为 nil 时保留环境代理探测。
func NewProxyTransport(proxyURL *url.URL) *ObservedTransport {
	base := newSharedBase()
	if proxyURL != nil {
		base.Proxy = http.ProxyURL(proxyURL)
	}
	base.MaxIdleConns = ProxyTransportMaxIdleConns
	base.MaxIdleConnsPerHost = ProxyTransportMaxIdleConnsPerHost
	return WrapObserved(base)
}

// NewDialTransport 自定义拨号传输层（如 SOCKS5）：Proxy=nil、拨号交给 dial，连接池 64/16。
// 需要「不协商压缩」的通道（商旅代理）由调用方 SetDisableCompression(true)。
func NewDialTransport(dial func(ctx context.Context, network, addr string) (net.Conn, error)) *ObservedTransport {
	base := newSharedBase()
	base.Proxy = nil
	if dial != nil {
		base.DialContext = dial
	}
	base.MaxIdleConns = ProxyTransportMaxIdleConns
	base.MaxIdleConnsPerHost = ProxyTransportMaxIdleConnsPerHost
	return WrapObserved(base)
}

// newSharedBase 构造共享传输层裸 base（未包装）。
func newSharedBase() *http.Transport {
	rt := cloneBaseTransport()
	rt.MaxIdleConns = TransportMaxIdleConns
	rt.MaxIdleConnsPerHost = TransportMaxIdleConnsPerHost
	rt.IdleConnTimeout = TransportIdleConnTimeout
	return rt
}

// cloneBaseTransport 复制当前全局传输层参数；默认传输层为 *ObservedTransport 时取真实 Base，
// 被替换为其他 RoundTripper 时回退标准库默认参数，保证工厂始终可用且代理参数不丢。
func cloneBaseTransport() *http.Transport {
	switch base := http.DefaultTransport.(type) {
	case *ObservedTransport:
		if base != nil {
			if raw := base.BaseTransport(); raw != nil {
				return raw.Clone()
			}
		}
	case *http.Transport:
		if base != nil {
			return base.Clone()
		}
	}
	return &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           (&net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          100,
		MaxIdleConnsPerHost:   2,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
	}
}
