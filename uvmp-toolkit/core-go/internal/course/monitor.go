package course

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"
)

// MonitoredResponse 一条被监视到的学习侧接口响应。
type MonitoredResponse struct {
	Seq    int    // 序号（从 1 起，save_mark 口径）
	Scope  string // 来源页面标识（ListPage.ID / LearnPage.ID）
	URL    string
	Status int
	Body   []byte // 已捕获的响应体（未捕获为空）
}

// ResponseMonitor 学习侧接口响应监视器（trial.py ResponseMonitor 同口径）：
// 记录 ware 清单/学习上报/课件切换/课程分页接口响应，按序号窗口等待并校验成功响应。
// 响应捕获由驱动层（CDP Network 事件或测试 fake）喂入；等待/校验/解析在此完成。
type ResponseMonitor struct {
	mu        sync.Mutex
	responses []MonitoredResponse
	closed    map[string]bool
	notify    chan struct{}
}

// NewResponseMonitor 创建空监视器。
func NewResponseMonitor() *ResponseMonitor {
	return &ResponseMonitor{closed: map[string]bool{}, notify: make(chan struct{})}
}

// Add 追加一条响应（驱动层捕获后调用）。
func (m *ResponseMonitor) Add(resp MonitoredResponse) {
	m.mu.Lock()
	m.responses = append(m.responses, resp)
	close(m.notify)
	m.notify = make(chan struct{})
	m.mu.Unlock()
}

// MarkClosed 标记页面已关闭（等待中的「学习页已关闭」判定）。
func (m *ResponseMonitor) MarkClosed(scope string) {
	m.mu.Lock()
	m.closed[scope] = true
	close(m.notify)
	m.notify = make(chan struct{})
	m.mu.Unlock()
}

// Seq 当前响应序号（作为等待窗口起点，save_mark 口径）。
func (m *ResponseMonitor) Seq() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.responses)
}

// Responses 响应快照（诊断用）。
func (m *ResponseMonitor) Responses() []MonitoredResponse {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]MonitoredResponse, len(m.responses))
	copy(out, m.responses)
	return out
}

// WaitSuccess 从序号 start 起等待 scope 页面上 apiPath 的成功响应并解析 JSON
// （trial.py ResponseMonitor.wait_success 同口径：HTTP 非 200/读取失败/业务失败立
// 即报错；页面已关闭报错；超时报「等待%s超时（%.0f 秒）」）。desc 为等待描述。
func (m *ResponseMonitor) WaitSuccess(ctx context.Context, scope, apiPath string, start int, timeout time.Duration, desc string) (map[string]any, error) {
	if timeout <= 0 {
		timeout = 120 * time.Second
	}
	deadline := time.Now().Add(timeout)
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		m.mu.Lock()
		waitCh := m.notify
		closed := m.closed[scope]
		found := -1
		var hit MonitoredResponse
		for i := start; i < len(m.responses); i++ {
			r := m.responses[i]
			if scope != "" && r.Scope != scope {
				continue
			}
			if !IsAPI(r.URL, apiPath) {
				continue
			}
			if r.Status != 200 {
				m.mu.Unlock()
				return nil, fmt.Errorf("%s HTTP 状态为 %d", desc, r.Status)
			}
			found = i
			hit = r
			break
		}
		m.mu.Unlock()
		if found >= 0 {
			if len(hit.Body) == 0 {
				return nil, fmt.Errorf("%s响应读取失败：未捕获响应体", desc)
			}
			var payload map[string]any
			if err := json.Unmarshal(hit.Body, &payload); err != nil {
				return nil, fmt.Errorf("%s响应读取失败：%s", desc, err)
			}
			if _, err := apiValue(payload, desc); err != nil {
				return nil, err
			}
			return payload, nil
		}
		if closed {
			return nil, fmt.Errorf("等待%s时学习页已关闭", desc)
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("等待%s超时（%.0f 秒）", desc, timeout.Seconds())
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-waitCh:
		case <-time.After(200 * time.Millisecond):
		}
	}
}
