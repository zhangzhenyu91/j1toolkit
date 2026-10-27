package callme

// SSE 流式问答：WeKnora 事件归一化（answer/thinking/preamble/tool_start/tool_end/references/
// title/error/done）+ 最终回答拼接（工具调用前的过渡语 preamble 不计入）+ 300s 无字节静默看门狗。
// 对应 callme.js collectAnswer 与 utils/sse.js 解析器。

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"
)

// ChatEvent 归一化后下发端侧的事件（data: {type, ...}）。
type ChatEvent struct {
	Type       string           `json:"type"`
	Content    string           `json:"content,omitempty"`
	Done       bool             `json:"done,omitempty"`
	ID         string           `json:"id,omitempty"`
	Name       string           `json:"name,omitempty"`
	Title      string           `json:"title,omitempty"`
	Summary    string           `json:"summary,omitempty"`
	Success    *bool            `json:"success,omitempty"`
	DurationMs int64            `json:"durationMs,omitempty"`
	Update     bool             `json:"update,omitempty"`
	List       []map[string]any `json:"list,omitempty"`
}

// ErrEmitted 已向端侧下发 error 事件的失败（调用方只收尾不重复下发）。
var ErrEmitted = errors.New("callme: error already emitted")

// ErrClientGone 客户端断连（已静默）。
var ErrClientGone = errors.New("callme: client gone")

// sseParser 兼容 WeKnora 非标准 SSE（data: 后无空格），事件以 \n\n 分隔。
type sseParser struct {
	buf     string
	onEvent func(map[string]any)
}

func (p *sseParser) push(chunk string) {
	p.buf += chunk
	parts := strings.Split(p.buf, "\n\n")
	p.buf = parts[len(parts)-1]
	for _, ev := range parts[:len(parts)-1] {
		p.handle(ev)
	}
}

func (p *sseParser) end() {
	if strings.TrimSpace(p.buf) != "" {
		rest := p.buf
		p.buf = ""
		p.handle(rest)
	}
}

func (p *sseParser) handle(eventStr string) {
	for _, line := range strings.Split(eventStr, "\n") {
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		jsonStr := strings.TrimPrefix(line, "data:")
		jsonStr = strings.TrimPrefix(jsonStr, " ")
		if jsonStr == "" {
			continue
		}
		var obj map[string]any
		if err := json.Unmarshal([]byte(jsonStr), &obj); err == nil {
			p.onEvent(obj)
		}
	}
}

// CollectResult 收流结果。
type CollectResult struct {
	Answer    string
	RequestID string
}

// toolCallInfo tool_call_id → {name,args}（tool_result 生成完成态标题需要入参）。
type toolCallInfo struct {
	name string
	args map[string]any
}

// CollectAnswer 发起 agentChatStream 后把 WeKnora 事件归一化经 onEvent 实时转发、拼接最终回答。
// 失败返回 error：errors.Is(err, ErrEmitted) 表示 error 事件已下发，调用方只收尾。
// ctx 取消（客户端断连）时销毁上游并返回 ErrClientGone。
func CollectAnswer(ctx context.Context, client WeKnoraClient, username, sessionID string, req ChatRequest,
	onEvent func(ChatEvent)) (CollectResult, error) {
	upstream, err := client.AgentChatStream(ctx, username, sessionID, req)
	if err != nil {
		return CollectResult{}, err
	}
	defer upstream.Close()

	var (
		mu              sync.Mutex
		answer          string
		requestID       string
		answerForwarded bool
		answerFailed    bool
		toolCalls       = map[string]toolCallInfo{}
	)

	emit := func(ev ChatEvent) {
		if onEvent != nil {
			onEvent(ev)
		}
	}

	emitToolEnd := func(frame map[string]any) {
		meta, _ := frame["data"].(map[string]any)
		if meta == nil {
			meta = map[string]any{}
		}
		id := StringOf(meta, "tool_call_id")
		name := StringOf(meta, "tool_name")
		start := toolCalls[id]
		delete(toolCalls, id)
		if name == "" {
			name = start.name
		}
		args := start.args
		if args == nil {
			args = map[string]any{}
		}
		success := StringOf(frame, "response_type") != "error" && !isFalse(meta["success"])
		title := DoneTitle(name, args, meta, success)
		summary := ""
		if success {
			summary = ToolSummary(name, meta)
		} else {
			summary = truncateRunes(firstNonEmpty(StringOf(meta, "error"), StringOf(frame, "content")), 120)
		}
		emit(ChatEvent{
			Type: "tool_end", ID: id, Name: name, Success: &success,
			Title: title, Summary: summary, DurationMs: anyToInt64(meta["duration_ms"]),
		})
	}

	// 上游静默看门狗：300s 无任何字节判定 WeKnora 卡死；不设总时长上限。
	silence := 300 * time.Second
	var silenceTimer *time.Timer
	var silenceMu sync.Mutex
	silenceCh := make(chan struct{}, 1)
	armSilence := func() {
		silenceMu.Lock()
		defer silenceMu.Unlock()
		if silenceTimer != nil {
			silenceTimer.Stop()
		}
		silenceTimer = time.AfterFunc(silence, func() {
			select {
			case silenceCh <- struct{}{}:
			default:
			}
		})
	}
	defer func() {
		silenceMu.Lock()
		if silenceTimer != nil {
			silenceTimer.Stop()
		}
		silenceMu.Unlock()
	}()

	parser := &sseParser{onEvent: func(data map[string]any) {
		switch StringOf(data, "response_type") {
		case "answer":
			content := StringOf(data, "content")
			mu.Lock()
			if content != "" {
				answerForwarded = true
			}
			answer += content
			mu.Unlock()
			done := boolOf(data, "done")
			emit(ChatEvent{Type: "answer", Content: content, Done: done})
		case "thinking":
			emit(ChatEvent{Type: "thinking", Content: StringOf(data, "content")})
		case "tool_call":
			mu.Lock()
			if answerForwarded {
				emit(ChatEvent{Type: "preamble"})
				answerForwarded = false
				answer = ""
			}
			meta, _ := data["data"].(map[string]any)
			if meta == nil {
				meta = map[string]any{}
			}
			id := StringOf(meta, "tool_call_id")
			prev := toolCalls[id]
			var args map[string]any
			if a, ok := meta["arguments"].(map[string]any); ok && len(a) > 0 {
				args = a
			} else if prev.args != nil {
				args = prev.args
			}
			name := StringOf(meta, "tool_name")
			if name == "" {
				name = prev.name
			}
			if id != "" {
				toolCalls[id] = toolCallInfo{name: name, args: args}
			}
			update := prev.name != "" || prev.args != nil
			mu.Unlock()
			emit(ChatEvent{Type: "tool_start", ID: id, Name: name, Title: PendingTitle(name), Update: update})
		case "tool_result":
			mu.Lock()
			emitToolEnd(data)
			mu.Unlock()
		case "references":
			refs, _ := data["knowledge_references"].([]any)
			seen := map[string]bool{}
			var list []map[string]any
			for _, it := range refs {
				r, _ := it.(map[string]any)
				if r == nil {
					continue
				}
				id := firstNonEmpty(StringOf(r, "knowledge_id"), StringOf(r, "id"))
				if id == "" || seen[id] {
					continue
				}
				seen[id] = true
				title := firstNonEmpty(StringOf(r, "knowledge_title"), StringOf(r, "knowledge_filename"), "未命名资料")
				list = append(list, map[string]any{"id": id, "title": title})
			}
			if len(list) > 0 {
				emit(ChatEvent{Type: "references", List: list})
			}
		case "session_title":
			if c := StringOf(data, "content"); c != "" {
				emit(ChatEvent{Type: "title", Content: c})
			}
		case "error":
			meta, _ := data["data"].(map[string]any)
			if meta != nil && StringOf(meta, "tool_call_id") != "" {
				mu.Lock()
				emitToolEnd(data)
				mu.Unlock()
			} else {
				mu.Lock()
				answerFailed = true
				mu.Unlock()
				emit(ChatEvent{Type: "error", Content: firstNonEmpty(StringOf(data, "content"), "AI 服务返回错误")})
			}
		}
		rid := StringOf(data, "request_id")
		if rid == "" {
			if meta, ok := data["data"].(map[string]any); ok {
				rid = StringOf(meta, "request_id")
			}
		}
		if rid != "" {
			mu.Lock()
			requestID = rid
			mu.Unlock()
		}
	}}

	reader := bufio.NewReader(upstream)
	armSilence()
	readErr := make(chan error, 1)
	go func() {
		for {
			chunk, err := reader.ReadString('\n')
			if chunk != "" {
				armSilence()
				parser.push(chunk)
			}
			if err != nil {
				if err == io.EOF {
					readErr <- nil
				} else {
					readErr <- err
				}
				return
			}
		}
	}()

	select {
	case err := <-readErr:
		parser.end()
		emit(ChatEvent{Type: "done"})
		mu.Lock()
		defer mu.Unlock()
		if err != nil {
			emit(ChatEvent{Type: "error", Content: "AI 服务连接中断"})
			return CollectResult{}, fmt.Errorf("%w: AI 服务连接中断", ErrEmitted)
		}
		if answerFailed {
			return CollectResult{}, fmt.Errorf("%w: AI 服务返回错误", ErrEmitted)
		}
		return CollectResult{Answer: answer, RequestID: requestID}, nil
	case <-silenceCh:
		upstream.Close()
		emit(ChatEvent{Type: "error", Content: "AI 服务响应超时"})
		return CollectResult{}, fmt.Errorf("%w: AI 服务响应超时", ErrEmitted)
	case <-ctx.Done():
		upstream.Close()
		return CollectResult{}, ErrClientGone
	}
}

func boolOf(m map[string]any, key string) bool {
	b, ok := m[key].(bool)
	return ok && b
}

func isFalse(v any) bool {
	b, ok := v.(bool)
	return ok && !b
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
