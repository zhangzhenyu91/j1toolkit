package course

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/chromedp/cdproto"
	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/cdproto/target"
	"github.com/chromedp/chromedp/remote"
)

// cdpHub 浏览器级 CDP 连接（chromedp/remote 同版本传输层）：目标自动附加 + 学习侧接口
// 响应零丢失捕获。与 chromedp 驱动连接并存——这里只做监视与轻量探测：
//   - Target.setAutoAttach(waitForDebuggerOnStart)：新目标（含「进入学习」弹窗）先冻结，
//     Network.enable 后再放行，弹窗首屏的 listRes/上报响应必被看到（Playwright 语义）；
//   - Network.responseReceived 同步取响应体（仅 APICapturePaths），按序喂 ResponseMonitor；
//   - 目标元数据/可见性/正文文本探测（find_list_page 用），免 chromedp 逐标签附加。
type cdpHub struct {
	conn    *remote.Conn
	monitor *ResponseMonitor
	logf    func(string)

	writeMu sync.Mutex
	nextID  int64
	pending map[int64]chan *cdproto.Message

	mu       sync.Mutex
	sessions map[target.SessionID]*target.Info // 自动附加的会话→目标
	targets  map[target.ID]target.SessionID
	bodies   map[string]MonitoredResponse // 响应体待取登记（loadingFinished 后取）
	closed   bool

	eventCh chan *cdproto.Message
	done    chan struct{}
}

// newCDPHub 连接本机浏览器 CDP 并开启目标自动附加（cdpURL 仅本机回环，调用方已校验）。
func newCDPHub(ctx context.Context, cdpURL string, monitor *ResponseMonitor, logf func(string)) (*cdpHub, error) {
	wsURL, err := browserWSURL(ctx, cdpURL)
	if err != nil {
		return nil, err
	}
	conn, err := remote.DialContext(ctx, wsURL)
	if err != nil {
		return nil, fmt.Errorf("course: 连接浏览器 CDP 失败: %w", err)
	}
	h := &cdpHub{
		conn:     conn,
		monitor:  monitor,
		logf:     logf,
		pending:  map[int64]chan *cdproto.Message{},
		sessions: map[target.SessionID]*target.Info{},
		targets:  map[target.ID]target.SessionID{},
		bodies:   map[string]MonitoredResponse{},
		eventCh:  make(chan *cdproto.Message, 1024),
		done:     make(chan struct{}),
	}
	go h.readLoop()
	go h.eventLoop()
	if err := h.call(ctx, "", "Target.setAutoAttach", map[string]any{
		"autoAttach":             true,
		"waitForDebuggerOnStart": true,
		"flatten":                true,
	}, nil); err != nil {
		h.Close()
		return nil, fmt.Errorf("course: 开启目标自动附加失败: %w", err)
	}
	return h, nil
}

// browserWSURL 解析浏览器 websocket 调试地址（/json/version）。
func browserWSURL(ctx context.Context, cdpURL string) (string, error) {
	client := &http.Client{Timeout: 5 * time.Second}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimSuffix(cdpURL, "/")+"/json/version", nil)
	if err != nil {
		return "", err
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("course: 读取浏览器调试版本失败: %w", err)
	}
	defer resp.Body.Close()
	var version struct {
		WebSocketDebuggerURL string `json:"webSocketDebuggerUrl"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&version); err != nil {
		return "", fmt.Errorf("course: 解析浏览器调试版本失败: %w", err)
	}
	if version.WebSocketDebuggerURL == "" {
		return "", fmt.Errorf("course: 浏览器未通告 websocket 调试地址")
	}
	return version.WebSocketDebuggerURL, nil
}

// call 发送一条 CDP 命令并等结果（sessionID 空=浏览器会话）。result 为 nil 丢弃。
func (h *cdpHub) call(ctx context.Context, sessionID target.SessionID, method string, params any, result any) error {
	raw, err := json.Marshal(params)
	if err != nil {
		return err
	}
	h.writeMu.Lock()
	h.nextID++
	id := h.nextID
	ch := make(chan *cdproto.Message, 1)
	h.pending[id] = ch
	h.writeMu.Unlock()
	defer func() {
		h.writeMu.Lock()
		delete(h.pending, id)
		h.writeMu.Unlock()
	}()
	msg := &cdproto.Message{ID: id, SessionID: sessionID, Method: cdproto.MethodType(method), Params: raw}
	if err := h.conn.Write(ctx, msg); err != nil {
		return err
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-h.done:
		return fmt.Errorf("course: CDP 连接已断开")
	case res := <-ch:
		if res.Error != nil {
			return fmt.Errorf("%s: %s (%d)", method, res.Error.Message, res.Error.Code)
		}
		if result != nil && len(res.Result) > 0 {
			return json.Unmarshal(res.Result, result)
		}
		return nil
	}
}

// readLoop 读消息泵：命令应答交等待方，事件进 eventCh（事件处理可在其内再发命令）。
func (h *cdpHub) readLoop() {
	defer close(h.done)
	for {
		msg := new(cdproto.Message)
		if err := h.conn.Read(context.Background(), msg); err != nil {
			return
		}
		switch {
		case msg.ID != 0:
			h.writeMu.Lock()
			ch := h.pending[msg.ID]
			h.writeMu.Unlock()
			if ch != nil {
				ch <- msg
			}
		case msg.Method != "":
			select {
			case h.eventCh <- msg:
			default:
				// 事件缓冲满：丢弃而非阻塞读泵（监视窗口内的关键接口响应极少积压）
			}
		}
	}
}

// eventLoop 事件处理：自动附加会话启用 Network 后放行；API 响应取体喂监视器（保序）。
func (h *cdpHub) eventLoop() {
	for {
		select {
		case <-h.done:
			return
		case msg := <-h.eventCh:
			h.handleEvent(msg)
		}
	}
}

func (h *cdpHub) handleEvent(msg *cdproto.Message) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	switch msg.Method {
	case "Target.attachedToTarget":
		var ev target.EventAttachedToTarget
		if err := json.Unmarshal(msg.Params, &ev); err != nil {
			return
		}
		info := ev.TargetInfo
		h.mu.Lock()
		h.sessions[ev.SessionID] = info
		if info != nil {
			h.targets[info.TargetID] = ev.SessionID
		}
		h.mu.Unlock()
		// 先启用 Network 再放行（命令按序处理，事件零丢失）；失败也放行，避免页面卡死
		_ = h.call(ctx, ev.SessionID, "Network.enable", map[string]any{}, nil)
		_ = h.call(ctx, ev.SessionID, "Runtime.runIfWaitingForDebugger", map[string]any{}, nil)
	case "Target.detachedFromTarget":
		var ev target.EventDetachedFromTarget
		if err := json.Unmarshal(msg.Params, &ev); err != nil {
			return
		}
		h.mu.Lock()
		if info := h.sessions[ev.SessionID]; info != nil {
			if h.targets[info.TargetID] == ev.SessionID {
				delete(h.targets, info.TargetID)
			}
			delete(h.sessions, ev.SessionID)
		}
		h.mu.Unlock()
	case "Target.targetDestroyed":
		var ev target.EventTargetDestroyed
		if err := json.Unmarshal(msg.Params, &ev); err != nil {
			return
		}
		h.mu.Lock()
		sid := h.targets[ev.TargetID]
		delete(h.targets, ev.TargetID)
		delete(h.sessions, sid)
		h.mu.Unlock()
		if h.monitor != nil {
			h.monitor.MarkClosed(string(ev.TargetID))
		}
	case "Network.responseReceived":
		var ev network.EventResponseReceived
		if err := json.Unmarshal(msg.Params, &ev); err != nil {
			return
		}
		url := ""
		status := 0
		if ev.Response != nil {
			url = ev.Response.URL
			status = int(ev.Response.Status)
		}
		if !captureBody(url) {
			return
		}
		scope := ""
		h.mu.Lock()
		if info := h.sessions[msg.SessionID]; info != nil {
			scope = string(info.TargetID)
		}
		// 响应头到达即登记；响应体等 loadingFinished 后再取（过早取只会拿到
		// "No data found"——体还没到，与 Playwright response.json() 惰性取体同口径）
		h.bodies[bodyKey(msg.SessionID, ev.RequestID)] = MonitoredResponse{
			Scope:  scope,
			URL:    url,
			Status: status,
		}
		h.mu.Unlock()
	case "Network.loadingFinished":
		var ev network.EventLoadingFinished
		if err := json.Unmarshal(msg.Params, &ev); err != nil {
			return
		}
		h.deliverBody(ctx, msg.SessionID, ev.RequestID)
	case "Network.loadingFailed":
		var ev network.EventLoadingFailed
		if err := json.Unmarshal(msg.Params, &ev); err != nil {
			return
		}
		// 失败也入账（空体），让等待侧给出「响应读取失败」而非干等超时
		h.mu.Lock()
		rec, ok := h.bodies[bodyKey(msg.SessionID, ev.RequestID)]
		delete(h.bodies, bodyKey(msg.SessionID, ev.RequestID))
		h.mu.Unlock()
		if ok && h.monitor != nil {
			h.monitor.Add(rec)
		}
	}
}

// deliverBody loadingFinished 后取响应体并入账监视器（保序：事件泵内同步调用）。
func (h *cdpHub) deliverBody(ctx context.Context, sessionID target.SessionID, requestID network.RequestID) {
	key := bodyKey(sessionID, requestID)
	h.mu.Lock()
	rec, ok := h.bodies[key]
	delete(h.bodies, key)
	h.mu.Unlock()
	if !ok {
		return
	}
	var body struct {
		Body          string `json:"body"`
		Base64Encoded bool   `json:"base64Encoded"`
	}
	err := h.call(ctx, sessionID, "Network.getResponseBody", map[string]any{
		"requestId": requestID,
	}, &body)
	if err != nil {
		if h.logf != nil {
			h.logf(fmt.Sprintf("[cdp] 取响应体失败 %s: %s", rec.URL, trimErr(err)))
		}
	} else if body.Base64Encoded {
		rec.Body = decodeB64(body.Body)
	} else {
		rec.Body = []byte(body.Body)
	}
	if h.monitor != nil {
		h.monitor.Add(rec)
	}
}

// bodyKey 响应体登记键（会话+请求）。
func bodyKey(sessionID target.SessionID, requestID network.RequestID) string {
	return string(sessionID) + "|" + string(requestID)
}

// captureBody URL 是否为需捕获响应体的学习侧接口。
func captureBody(rawURL string) bool {
	for _, p := range APICapturePaths {
		if IsAPI(rawURL, p) {
			return true
		}
	}
	return false
}

// sessionOf 取目标当前自动附加会话（无则空）。
func (h *cdpHub) sessionOf(id string) (target.SessionID, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	sid, ok := h.targets[target.ID(id)]
	return sid, ok
}

// evalString 在目标上求值并取字符串（可见性/正文文本探测用）。
func (h *cdpHub) evalString(ctx context.Context, id, expr string) (string, error) {
	sid, ok := h.sessionOf(id)
	if !ok {
		return "", fmt.Errorf("course: 目标 %s 未附加", id)
	}
	var res struct {
		Result struct {
			Type  string `json:"type"`
			Value any    `json:"value"`
		} `json:"result"`
		ExceptionDetails json.RawMessage `json:"exceptionDetails"`
	}
	if err := h.call(ctx, sid, "Runtime.evaluate", map[string]any{
		"expression":    expr,
		"returnByValue": true,
	}, &res); err != nil {
		return "", err
	}
	if len(res.ExceptionDetails) > 0 {
		return "", fmt.Errorf("course: 页面脚本异常: %s", string(res.ExceptionDetails))
	}
	if s, ok := res.Result.Value.(string); ok {
		return s, nil
	}
	return fmt.Sprintf("%v", res.Result.Value), nil
}

// waitNewPage 等待新出现的 page 目标（「进入学习」弹窗），返回目标 ID。
// existing 为点击前 /json/list 的目标 ID 基线（含未完成自动附加的目标，避免误判）。
func (h *cdpHub) waitNewPage(ctx context.Context, existing map[string]bool, timeout time.Duration) (string, error) {
	deadline := time.Now().Add(timeout)
	for {
		h.mu.Lock()
		var found string
		for id, sid := range h.targets {
			if info := h.sessions[sid]; info != nil && info.Type != "page" {
				continue
			}
			if existing[string(id)] {
				continue
			}
			found = string(id)
			break
		}
		h.mu.Unlock()
		if found != "" {
			return found, nil
		}
		if time.Now().After(deadline) {
			return "", fmt.Errorf("course: 等待学习弹窗超时（%.0f 秒）", timeout.Seconds())
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-h.done:
			return "", fmt.Errorf("course: CDP 连接已断开")
		case <-time.After(150 * time.Millisecond):
		}
	}
}

// decodeB64 base64 解码（失败返回 nil，与「响应读取失败」口径一致由上层判定）。
func decodeB64(s string) []byte {
	data, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return nil
	}
	return data
}

// Close 断开监视连接（不关浏览器）。
func (h *cdpHub) Close() {
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return
	}
	h.closed = true
	h.mu.Unlock()
	_ = h.conn.Close()
}
