package wechatanswer

// WeChatPadPro WS 连接/断开日志：连接成功与断开各一条，票据不入日志。

import (
	"context"
	"net"
	"strings"
	"testing"
	"time"
)

// immediateCloseConn 连接后立即可读失败（模拟对端断开）。
type immediateCloseConn struct{}

func (immediateCloseConn) Read() ([]byte, error) { return nil, net.ErrClosed }
func (immediateCloseConn) Ping() error           { return nil }
func (immediateCloseConn) Close() error          { return nil }

func TestPadProRunLogsConnectAndDisconnect(t *testing.T) {
	srv, _ := newWSGW(t, nil)
	cli := wsClient(srv)
	cli.DialWS = func(context.Context, string) (WSConn, error) { return immediateCloseConn{}, nil }
	fl := &fakeAppLog{}
	cli.Log = fl
	cli.pingInterval = 10 * time.Millisecond
	cli.staleCheckInterval = 5 * time.Millisecond
	cli.staleTimeout = 20 * time.Millisecond
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	err := cli.Run(ctx, func(map[string]any) {})
	if err == nil {
		t.Fatal("对端立即关闭应返回断开错误")
	}
	events := fl.events()
	joined := strings.Join(events, "\n")
	for _, want := range []string{"info/wechat.ws.connected", "warn/wechat.ws.disconnected"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("缺事件 %q：%s", want, joined)
		}
	}
	if strings.Contains(joined, wsTestTicket) || strings.Contains(joined, "test-token") {
		t.Fatalf("票据/token 泄漏：%s", joined)
	}
}
