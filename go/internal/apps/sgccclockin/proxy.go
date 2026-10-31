// 出口代理传输层：SGCC_PROXY_URL 支持 socks5://（x/net/proxy 拨号）与 http://、https://（CONNECT 代理）。
// 代理不可用/地址无效即报错，不静默降级直连——同一账号多链路 IP 属地若直连/代理混用会更不一致，
// 风控口径反而更差（Node 侧 socks-proxy-agent 同口径）。传输层按代理地址缓存，地址变更即重建（热生效）。
package sgccclockin

import (
	"context"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/url"
	"sync"

	"golang.org/x/net/proxy"

	"shade/internal/integrations"
)

// directTransport 未显式配置 SGCC_PROXY_URL 时直连：Node 商旅通道不继承进程 HTTP_PROXY，
// Go 也不能让同名环境变量把请求悄悄带去其它代理。复用 integrations 共享传输层工厂
// （池 128/32、空闲 90s），强制 Proxy=nil 并关闭 gzip 协商。
func directTransport() http.RoundTripper {
	return integrations.NewDirectTransport()
}

// transportCache 传输层缓存（按代理地址重建；空代理缓存直连实例，并发安全）。
type transportCache struct {
	mu  sync.Mutex
	url string
	rt  http.RoundTripper
}

// get 取当前代理地址对应的传输层；地址变化时重建。proxyURL 为空返回缓存的直连传输层
// （模块内复用同一实例，避免每张商旅图片都新建连接池；Proxy=nil/关闭压缩语义不变）。
func (c *transportCache) get(proxyURL string) (http.RoundTripper, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if proxyURL == "" {
		if c.url == "" && c.rt != nil {
			return c.rt, nil
		}
		c.url, c.rt = "", directTransport()
		return c.rt, nil
	}
	if c.url == proxyURL && c.rt != nil {
		return c.rt, nil
	}
	rt, err := buildTransport(proxyURL)
	if err != nil {
		return nil, err
	}
	log.Printf("[商旅打卡] API 出口代理已启用：%s", maskProxyURL(proxyURL))
	c.url, c.rt = proxyURL, rt
	return rt, nil
}

// buildTransport 按代理地址构造传输层：socks5://走 SOCKS5 拨号（账密写在 URL 里），
// http://、https://走 HTTP CONNECT 代理；其余 scheme 报错。
func buildTransport(proxyURL string) (http.RoundTripper, error) {
	u, err := url.Parse(proxyURL)
	if err != nil {
		return nil, fmt.Errorf("SGCC_PROXY_URL 代理地址无效：%w", err)
	}
	switch u.Scheme {
	case "socks5", "socks5h":
		d, err := proxy.FromURL(u, proxy.Direct)
		if err != nil {
			return nil, fmt.Errorf("SGCC_PROXY_URL 代理地址无效：%w", err)
		}
		rt := integrations.NewDialTransport(dialerContext(d)) // Proxy=nil，池 64/16
		rt.SetDisableCompression(true)
		return rt, nil
	case "http", "https":
		rt := integrations.NewProxyTransport(u) // 仅显式代理地址，不读环境代理；池 64/16
		rt.SetDisableCompression(true)
		return rt, nil
	default:
		return nil, fmt.Errorf("SGCC_PROXY_URL 代理地址无效：不支持的代理协议 %q（仅支持 socks5/http/https）", u.Scheme)
	}
}

// dialerContext 把 x/net/proxy.Dialer 适配为 DialContext（实现 ContextDialer 时透传）。
func dialerContext(d proxy.Dialer) func(ctx context.Context, network, addr string) (net.Conn, error) {
	if cd, ok := d.(proxy.ContextDialer); ok {
		return cd.DialContext
	}
	return func(_ context.Context, network, addr string) (net.Conn, error) {
		return d.Dial(network, addr)
	}
}
