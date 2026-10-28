package sgccclockin

import (
	"context"
	"os"
	"testing"
	"time"
)

// TestLiveSGCCProxyTransport 仅在显式设置 SGCC_LIVE_PROXY_URL 时运行，用于生产代理差异排障。
// 不读取数据库、不发送账号或加密业务数据，只验证 Go 协议传输能否取得网关 HTTP 响应；常规测试始终跳过。
func TestLiveSGCCProxyTransport(t *testing.T) {
	proxyURL := os.Getenv("SGCC_LIVE_PROXY_URL")
	if proxyURL == "" {
		t.Skip("未设置 SGCC_LIVE_PROXY_URL，跳过实网传输探测")
	}
	p := NewProtocol(Keys{})
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	status, _, err := p.post(ctx, proxyURL, p.hostH5(), "ads", map[string]string{
		"Content-Type": "application/jsonm",
	}, "{}")
	if err != nil {
		t.Fatalf("Go 协议传输失败: %v", err)
	}
	if status != 200 {
		t.Fatalf("网关 HTTP 状态异常: %d", status)
	}
}
