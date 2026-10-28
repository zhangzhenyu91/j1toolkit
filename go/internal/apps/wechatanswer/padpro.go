// WeChatPadPro 对接：HTTP（换票/群成员/图片下载/文本与引用回执）+ WebSocket 实时消息通道。
// 全局连接配置（Token/API Base/WS URL 等）每次调用从 config.Store 解析；Token 未配置时不发起任何网络请求。
package wechatanswer

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
)

// PadProClient WeChatPadPro 云端网关客户端（MemberSource + ImageDownloader + MessageSender + SyncSource）。
type PadProClient struct {
	Cfg  ConfigGet
	HTTP *http.Client
	// DialWS WebSocket 拨号（测试可注入）；nil 用 gorilla/websocket。
	DialWS func(ctx context.Context, wsURL string) (WSConn, error)

	pingInterval       time.Duration
	staleCheckInterval time.Duration
	staleTimeout       time.Duration
}

var (
	_ MemberSource    = (*PadProClient)(nil)
	_ ImageDownloader = (*PadProClient)(nil)
	_ MessageSender   = (*PadProClient)(nil)
	_ SyncSource      = (*PadProClient)(nil)
)

// WSConn WebSocket 连接最小面（可注入假实现）。
type WSConn interface {
	Read() ([]byte, error)
	Ping() error
	Close() error
}

// wechatCfg WeChatPadPro 连接配置（每次调用解析，支持热更）。
type wechatCfg struct {
	Token      string
	APIBase    string
	TicketPath string
	WSURL      string
	BotWxid    string
	FileAppID  string
	LogRaw     bool
}

func (c *PadProClient) loadCfg(ctx context.Context) wechatCfg {
	get := func(key, def string) string {
		if c.Cfg == nil {
			return def
		}
		v, err := c.Cfg.Get(ctx, key)
		if err != nil || v == "" {
			return def
		}
		return v
	}
	return wechatCfg{
		Token:      get("WECHATPADPRO_TOKEN", ""),
		APIBase:    strings.TrimRight(get("WECHATPADPRO_API_BASE", "https://api.wechatpadpro.com"), "/"),
		TicketPath: get("WECHATPADPRO_TICKET_PATH", "/cloud/v1/Runtime/WebSocketTicket"),
		WSURL:      get("WECHATPADPRO_WS_URL", "wss://api.wechatpadpro.com/ws/sync"),
		BotWxid:    get("WECHATPADPRO_BOT_WXID", ""),
		FileAppID:  get("WECHATPADPRO_FILE_APP_ID", "ID_10001"),
		LogRaw:     get("LOG_RAW_MESSAGE", "0") == "1",
	}
}

func (c *PadProClient) httpc() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return &http.Client{Timeout: 60 * time.Second}
}

// post 网关 POST：X-Cloud-Access-Token 鉴权，解析 {Success,Code,Data} 信封。
func (c *PadProClient) post(ctx context.Context, path string, body any) (map[string]any, error) {
	cfg := c.loadCfg(ctx)
	if cfg.Token == "" {
		return nil, errors.New("wechatpadpro: 未配置 WECHATPADPRO_TOKEN，调用禁用")
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, cfg.APIBase+path, bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-Cloud-Access-Token", cfg.Token)
	req.Header.Set("Content-Type", "application/json")
	res, err := c.httpc().Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	// 文件接口把二进制放在 base64 JSON 中，须容纳 50MB 原文件的展开。
	text, _ := io.ReadAll(io.LimitReader(res.Body, 80<<20))
	var parsed map[string]any
	if err := json.Unmarshal(text, &parsed); err != nil {
		return nil, fmt.Errorf("wechatpadpro %s 返回非 JSON（HTTP %d）：%s", path, res.StatusCode, truncate(string(text), 200))
	}
	success, _ := parsed["Success"].(bool)
	code, hasCode := parsed["Code"].(float64)
	if res.StatusCode < 200 || res.StatusCode >= 300 || !success || (hasCode && code != 0) {
		return nil, fmt.Errorf("wechatpadpro %s 调用失败（HTTP %d）：%s", path, res.StatusCode, truncate(string(text), 300))
	}
	return parsed, nil
}

// GroupMembers 获取群成员（UserName → DisplayName/NickName），10 分钟内存缓存。
func (c *PadProClient) GroupMembers(ctx context.Context, groupID string) (map[string]string, error) {
	body, err := c.post(ctx, "/v1/Group/GetChatRoomMemberDetail", map[string]any{"QID": groupID})
	if err != nil {
		return nil, err
	}
	data, _ := body["Data"].(map[string]any)
	room, _ := data["NewChatroomData"].(map[string]any)
	list, _ := room["ChatRoomMember"].([]any)
	if list == nil {
		return nil, fmt.Errorf("wechatpadpro: 群 %s 成员数据缺失", groupID)
	}
	out := map[string]string{}
	for _, item := range list {
		m, _ := item.(map[string]any)
		if m == nil {
			continue
		}
		user, _ := m["UserName"].(string)
		if user == "" {
			continue
		}
		name, _ := m["DisplayName"].(string)
		if name == "" {
			name, _ = m["NickName"].(string)
		}
		out[user] = name
	}
	return out, nil
}

// DownloadImage 下载图片本体（返回服务端已解密的原图字节）；网关仅认小写 file_no/file_aes_key。
func (c *PadProClient) DownloadImage(ctx context.Context, fileNo, aesKey string) ([]byte, error) {
	cfg := c.loadCfg(ctx)
	if cfg.Token == "" {
		return nil, errors.New("wechatpadpro: 未配置 WECHATPADPRO_TOKEN，调用禁用")
	}
	// 大原图（10M 级）解密 + base64 回传耗时可观，超时单独放宽
	reqCtx, cancel := context.WithTimeout(ctx, 180*time.Second)
	defer cancel()
	body, err := c.post(reqCtx, "/v1/Tools/CdnDownloadImage", map[string]any{
		"file_no":      fileNo,
		"file_aes_key": aesKey,
	})
	if err != nil {
		return nil, err
	}
	data, _ := body["Data"].(map[string]any)
	b64, _ := data["Image"].(string)
	if b64 == "" {
		return nil, errors.New("wechatpadpro: 图片下载接口未返回图像数据")
	}
	buf, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return nil, fmt.Errorf("wechatpadpro: 图像 base64 解码失败: %w", err)
	}
	isJpeg := len(buf) > 3 && buf[0] == 0xff && buf[1] == 0xd8
	isPng := len(buf) > 8 && buf[0] == 0x89 && buf[1] == 0x50 && buf[2] == 0x4e && buf[3] == 0x47
	if !isJpeg && !isPng {
		return nil, errors.New("wechatpadpro: 下载内容不是 JPEG/PNG 图片")
	}
	return buf, nil
}

// SendText 发送群文本回执。
func (c *PadProClient) SendText(ctx context.Context, groupID, text string) error {
	_, err := c.post(ctx, "/v1/Msg/SendTxt", map[string]any{"Content": text, "ToWxid": groupID})
	return err
}

// SendQuote 发送引用回执（锚定被引用消息）。
func (c *PadProClient) SendQuote(ctx context.Context, groupID, text string, quote QuoteRef) error {
	reply := map[string]any{
		"chat_user_id":    groupID,
		"conversation_id": groupID,
		"from_user_id":    "",
		"msg_id":          0,
		"msg_type":        1,
		"new_msg_id":      quote.SvrID,
		"quote_content":   quote.Content,
		"sequence":        0,
		"svr_id":          quote.SvrID,
		"to_wxid":         groupID,
	}
	_, err := c.post(ctx, "/v1/Msg/Quote", map[string]any{
		"chat_user_id":  groupID,
		"content":       text,
		"display_name":  "",
		"from_user_id":  "",
		"msg_type":      1,
		"new_msg_id":    quote.SvrID,
		"quote_content": quote.Content,
		"reply_context": reply,
		"sequence":      quote.CreateTime,
		"svr_id":        quote.SvrID,
		"to_wxid":       groupID,
	})
	return err
}

// fetchTicket 换取 WebSocket 票据。
func (c *PadProClient) fetchTicket(ctx context.Context) (string, error) {
	cfg := c.loadCfg(ctx)
	if cfg.Token == "" {
		return "", errors.New("wechatpadpro: 未配置 WECHATPADPRO_TOKEN，消息通道禁用")
	}
	raw, err := json.Marshal(map[string]any{})
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, cfg.APIBase+cfg.TicketPath, bytes.NewReader(raw))
	if err != nil {
		return "", err
	}
	req.Header.Set("X-Cloud-Access-Token", cfg.Token)
	req.Header.Set("Content-Type", "application/json")
	res, err := c.httpc().Do(req)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	text, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	var body struct {
		Success bool `json:"Success"`
		Data    struct {
			Ticket    string `json:"ticket"`
			ExpiresIn int64  `json:"expires_in"`
		} `json:"Data"`
	}
	if err := json.Unmarshal(text, &body); err != nil || !body.Success || body.Data.Ticket == "" {
		return "", fmt.Errorf("wechatpadpro: 换票失败：%s", truncate(string(text), 300))
	}
	return body.Data.Ticket, nil
}

// Run 实时消息通道：换票 → WS 订阅 → 逐条回调；连接断开返回错误（由调用方退避重连）。
func (c *PadProClient) Run(ctx context.Context, onMessage func(raw map[string]any)) error {
	ticket, err := c.fetchTicket(ctx)
	if err != nil {
		return err
	}
	cfg := c.loadCfg(ctx)
	wsURL := cfg.WSURL + "?ticket=" + url.QueryEscape(ticket)
	conn, err := c.dial(ctx, wsURL)
	if err != nil {
		return fmt.Errorf("wechatpadpro: WebSocket 连接失败: %w", err)
	}
	defer conn.Close()
	var lastSeen atomic.Int64
	lastSeen.Store(time.Now().UnixNano())
	// Pong 与业务消息一样是连接活性证据；只看消息会把正常空闲连接误判为死连接。
	if pc, ok := conn.(interface{ SetPongHandler(func(string) error) }); ok {
		pc.SetPongHandler(func(string) error {
			lastSeen.Store(time.Now().UnixNano())
			return nil
		})
	}
	pingInterval, checkInterval, staleTimeout := c.pingInterval, c.staleCheckInterval, c.staleTimeout
	if pingInterval <= 0 {
		pingInterval = 30 * time.Second
	}
	if checkInterval <= 0 {
		checkInterval = 30 * time.Second
	}
	if staleTimeout <= 0 {
		staleTimeout = 90 * time.Second
	}
	staleCh := make(chan struct{})
	go func() {
		defer close(staleCh)
		ticker := time.NewTicker(checkInterval)
		defer ticker.Stop()
		lastPing := time.Time{}
		for {
			select {
			case <-ctx.Done():
				_ = conn.Close()
				return
			case now := <-ticker.C:
				if time.Since(time.Unix(0, lastSeen.Load())) > staleTimeout {
					_ = conn.Close()
					return
				}
				if now.Sub(lastPing) >= pingInterval {
					if err := conn.Ping(); err != nil {
						_ = conn.Close()
						return
					}
					lastPing = now
				}
			}
		}
	}()
	for {
		data, err := conn.Read()
		if err != nil {
			<-staleCh
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		lastSeen.Store(time.Now().UnixNano())
		var raw map[string]any
		if err := json.Unmarshal(data, &raw); err != nil {
			continue
		}
		if cfg.LogRaw {
			// 原始报文仅在开启 LOG_RAW_MESSAGE 时落日志，且脱敏（token/aes_key 等敏感值不落盘）
			var dump map[string]any
			if json.Unmarshal(data, &dump) == nil {
				if b, err := json.Marshal(RedactRaw(dump)); err == nil {
					fmt.Printf("[wechatpadpro] RAW: %s\n", truncate(string(b), 2000))
				}
			}
		}
		for _, msg := range extractPushedMessages(raw) {
			onMessage(msg)
		}
	}
}

// dial 与旧 Node 一样不发送 Origin；握手错误只保留状态和正文，不输出票据 URL。
func (c *PadProClient) dial(ctx context.Context, wsURL string) (WSConn, error) {
	if c.DialWS != nil {
		return c.DialWS(ctx, wsURL)
	}
	dialer := *websocket.DefaultDialer
	ws, resp, err := dialer.DialContext(ctx, wsURL, nil)
	if err != nil {
		if resp != nil {
			defer resp.Body.Close()
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
			if len(body) > 0 {
				return nil, fmt.Errorf("HTTP %d %s：%s", resp.StatusCode, resp.Status, truncate(string(body), 500))
			}
			return nil, fmt.Errorf("HTTP %d %s", resp.StatusCode, resp.Status)
		}
		return nil, err
	}
	return &netWSConn{ws: ws}, nil
}

// extractPushedMessages 从推送报文提取消息数组（Data.data.messages）；connection_ready 等非消息推送返回空。
func extractPushedMessages(raw map[string]any) []map[string]any {
	payload, _ := raw["Data"].(map[string]any)
	if payload == nil {
		return nil
	}
	if t, _ := payload["type"].(string); t == "connection_ready" {
		return nil
	}
	data, _ := payload["data"].(map[string]any)
	list, _ := data["messages"].([]any)
	var out []map[string]any
	for _, item := range list {
		if m, ok := item.(map[string]any); ok {
			out = append(out, m)
		}
	}
	return out
}

// netWSConn gorilla/websocket 适配。
type netWSConn struct {
	ws *websocket.Conn
}

func (c *netWSConn) Read() ([]byte, error) {
	for {
		kind, msg, err := c.ws.ReadMessage()
		if err != nil {
			return nil, err
		}
		if kind == websocket.TextMessage {
			return msg, nil
		}
	}
}

func (c *netWSConn) Ping() error {
	return c.ws.WriteControl(websocket.PingMessage, []byte("ping"), time.Now().Add(10*time.Second))
}

func (c *netWSConn) SetPongHandler(h func(string) error) {
	c.ws.SetPongHandler(h)
}

func (c *netWSConn) Close() error {
	return c.ws.Close()
}
