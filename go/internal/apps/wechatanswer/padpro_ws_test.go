// padpro WebSocket 通道测试：httptest 本地假网关验证握手与控制帧口径——
// 不发送 Origin、ticket 查询参数换升级、文本帧 Read（跳过非文本帧）、
// Ping 控制帧、401 握手失败返回状态与正文且不泄露票据。不连真实 WeChatPadPro。
package wechatanswer

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

const wsTestTicket = "secret-ticket-do-not-leak"

// wsTestUpgrader 测试用升级器：放行任意 Origin（本用例断言客户端根本不发 Origin）。
var wsTestUpgrader = websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}

// wsProbe 假网关记录的握手与控制帧观测。
type wsProbe struct {
	mu       sync.Mutex
	origin   string
	ticket   string
	pinged   bool
	pingBody string
}

func (p *wsProbe) record(fn func()) {
	p.mu.Lock()
	defer p.mu.Unlock()
	fn()
}

func (p *wsProbe) snapshot() (origin, ticket string, pinged bool, pingBody string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.origin, p.ticket, p.pinged, p.pingBody
}

// newWSGW 起本地假网关：/cloud/v1/Runtime/WebSocketTicket 换票 + /ws/sync 升级。
// onConn 为升级成功后的服务端协程体（自行写帧/读回执）。
func newWSGW(t *testing.T, onConn func(conn *websocket.Conn, probe *wsProbe)) (*httptest.Server, *wsProbe) {
	t.Helper()
	probe := &wsProbe{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/cloud/v1/Runtime/WebSocketTicket" {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"Success": true,
				"Code":    0,
				"Data":    map[string]any{"ticket": wsTestTicket, "expires_in": 3600},
			})
			return
		}
		probe.record(func() {
			probe.origin = r.Header.Get("Origin")
			probe.ticket = r.URL.Query().Get("ticket")
		})
		conn, err := wsTestUpgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		if onConn != nil {
			onConn(conn, probe)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, probe
}

// wsClient 构造绑定假网关的 PadProClient（WS 地址由 http:// 换 ws://）。
func wsClient(srv *httptest.Server) *PadProClient {
	return &PadProClient{Cfg: fakeCfg{values: map[string]string{
		"WECHATPADPRO_TOKEN":    "test-token",
		"WECHATPADPRO_API_BASE": srv.URL,
		"WECHATPADPRO_WS_URL":   "ws" + strings.TrimPrefix(srv.URL, "http") + "/ws/sync",
	}}}
}

// TestPadProWSTicketUpgradeTextReadNoOrigin 换票升级后文本帧可 Read：
// 客户端不发送 Origin、升级请求带 ticket、跳过非文本帧只回调文本消息。
func TestPadProWSTicketUpgradeTextReadNoOrigin(t *testing.T) {
	srv, probe := newWSGW(t, func(conn *websocket.Conn, _ *wsProbe) {
		// 先发二进制帧（应被 Read 跳过），再发文本推送帧
		_ = conn.WriteMessage(websocket.BinaryMessage, []byte{0x00, 0x01})
		push := map[string]any{"Data": map[string]any{
			"type": "new_message",
			"data": map[string]any{"messages": []any{map[string]any{
				"kind": "text", "content": "hello-ws", "conversation_id": "g@chatroom", "svr_id": "1",
			}}},
		}}
		_ = conn.WriteJSON(push)
		// 读回执直至客户端关闭（观察帧即可，不消费文本）
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	})
	cli := wsClient(srv)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	got := make(chan map[string]any, 4)
	done := make(chan error, 1)
	go func() { done <- cli.Run(ctx, func(raw map[string]any) { got <- raw }) }()

	select {
	case msg := <-got:
		if toStr(msg["content"]) != "hello-ws" {
			t.Fatalf("文本帧内容不符：%v", msg)
		}
	case err := <-done:
		t.Fatalf("Run 提前结束：%v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("等待文本帧超时")
	}
	// 只应收到文本帧里的 1 条消息（二进制帧被跳过）
	select {
	case extra := <-got:
		t.Fatalf("非文本帧不应产生回调：%v", extra)
	case <-time.After(200 * time.Millisecond):
	}

	origin, ticket, _, _ := probe.snapshot()
	if origin != "" {
		t.Fatalf("握手不应发送 Origin，实际=%q", origin)
	}
	if ticket != wsTestTicket {
		t.Fatalf("升级请求未携带 ticket：%q", ticket)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("ctx 取消后 Run 应返回 nil：%v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run 未随 ctx 取消退出")
	}
}

// TestPadProWSPingControlFrame netWSConn.Ping 发出 Ping 控制帧并带载荷。
func TestPadProWSPingControlFrame(t *testing.T) {
	pongSeen := make(chan struct{}, 1)
	srv, probe := newWSGW(t, func(conn *websocket.Conn, p *wsProbe) {
		conn.SetPingHandler(func(appData string) error {
			p.record(func() {
				p.pinged = true
				p.pingBody = appData
			})
			select {
			case pongSeen <- struct{}{}:
			default:
			}
			return nil
		})
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	})
	cli := wsClient(srv)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ticket, err := cli.fetchTicket(ctx)
	if err != nil {
		t.Fatalf("换票失败：%v", err)
	}
	if ticket != wsTestTicket {
		t.Fatalf("换票结果不符：%q", ticket)
	}
	cfg := cli.loadCfg(ctx)
	conn, err := cli.dial(ctx, cfg.WSURL+"?ticket="+ticket)
	if err != nil {
		t.Fatalf("WebSocket 升级失败：%v", err)
	}
	defer conn.Close()
	if err := conn.Ping(); err != nil {
		t.Fatalf("Ping 控制帧发送失败：%v", err)
	}
	select {
	case <-pongSeen:
	case <-time.After(3 * time.Second):
		t.Fatal("服务端未收到 Ping 控制帧")
	}
	_, _, pinged, pingBody := probe.snapshot()
	if !pinged || pingBody != "ping" {
		t.Fatalf("Ping 控制帧观测不符：pinged=%v body=%q", pinged, pingBody)
	}
}

// TestPadProWSHandshake401BodyNoTicketLeak 401 握手失败：错误含状态与正文、不含票据。
func TestPadProWSHandshake401BodyNoTicketLeak(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/cloud/v1/Runtime/WebSocketTicket", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"Success": true,
			"Code":    0,
			"Data":    map[string]any{"ticket": wsTestTicket, "expires_in": 3600},
		})
	})
	mux.HandleFunc("/ws/sync", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"gateway unauthorized"}`))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	cli := wsClient(srv)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err := cli.Run(ctx, func(map[string]any) {})
	if err == nil {
		t.Fatal("401 握手失败应返回错误")
	}
	msg := err.Error()
	if !strings.Contains(msg, "401") || !strings.Contains(msg, "gateway unauthorized") {
		t.Fatalf("错误应含状态与正文：%s", msg)
	}
	if strings.Contains(msg, wsTestTicket) {
		t.Fatalf("错误不得泄露票据：%s", msg)
	}
	if strings.Contains(msg, "ticket=") || strings.Contains(msg, "ws/sync?") {
		t.Fatalf("错误不得泄露票据 URL：%s", msg)
	}
}

// wsPongConn 只回 Pong、不发业务消息的假连接：用于复现空闲连接被看门狗误杀。
type wsPongConn struct {
	closed    chan struct{}
	closeOnce sync.Once
	replyPong bool
	mu        sync.Mutex
	handler   func(string) error
}

func (c *wsPongConn) Read() ([]byte, error) {
	<-c.closed
	return nil, net.ErrClosed
}

func (c *wsPongConn) Ping() error {
	if !c.replyPong {
		return nil
	}
	c.mu.Lock()
	h := c.handler
	c.mu.Unlock()
	if h != nil {
		return h("ping")
	}
	return nil
}

func (c *wsPongConn) Close() error {
	c.closeOnce.Do(func() { close(c.closed) })
	return nil
}

func (c *wsPongConn) SetPongHandler(h func(string) error) {
	c.mu.Lock()
	c.handler = h
	c.mu.Unlock()
}

func runPongWatchdog(t *testing.T, replyPong bool) {
	t.Helper()
	srv, _ := newWSGW(t, nil)
	conn := &wsPongConn{closed: make(chan struct{}), replyPong: replyPong}
	cli := wsClient(srv)
	cli.DialWS = func(context.Context, string) (WSConn, error) { return conn, nil }
	cli.pingInterval = 10 * time.Millisecond
	cli.staleCheckInterval = 5 * time.Millisecond
	cli.staleTimeout = 40 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- cli.Run(ctx, func(map[string]any) {}) }()

	if replyPong {
		select {
		case err := <-done:
			t.Fatalf("只有 Pong、无业务消息时不应断线: %v", err)
		case <-time.After(180 * time.Millisecond):
		}
	} else {
		select {
		case err := <-done:
			if err == nil {
				t.Fatal("无 Pong 时看门狗应返回断线错误")
			}
			return
		case <-time.After(2 * time.Second):
			t.Fatal("无 Pong 时看门狗未及时断线")
		}
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("主动停止不应返回断线错误: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run 未随主动停止退出")
	}
}

func TestPadProWSPongKeepsIdleConnectionAlive(t *testing.T) {
	runPongWatchdog(t, true)
}

func TestPadProWSWatchdogWithoutPong(t *testing.T) {
	runPongWatchdog(t, false)
}
