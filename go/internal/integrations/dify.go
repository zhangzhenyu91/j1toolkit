// Dify 工作流 HTTP 客户端（协议对齐 server/src/utils/dify.js 与各应用 dify.js）：
// POST {DIFY_API_URL}/v1/workflows/run（配置已带 /v1 不重复拼接）。
// blocking 模式解析 data.outputs；streaming 模式触发后后台消费 SSE，
// workflow_finished failed/stopped 或流读取异常回调 OnFailed。
// 瞬时传输故障的有界重试集中在 RetryPolicy 一处；工作流业务失败、4xx、配置缺失不重试。
package integrations

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// DefaultDifyAPIURLKey Dify 基地址配置键（DIFY_API_URL）。
const DefaultDifyAPIURLKey = "DIFY_API_URL"

// workflowRunPath 工作流运行接口相对路径。
const workflowRunPath = "/workflows/run"

// defaultWorkflowTimeout 单次尝试超时（对齐 Node worklog/dify.js timeout: 90000）。
const defaultWorkflowTimeout = 90 * time.Second

// ErrNotConfigured Dify 未配置（DIFY_API_URL 或应用 API Key 为空）。
var ErrNotConfigured = errors.New("未配置 DIFY_API_URL 或 API Key")

// ErrBadEnvelope 工作流响应不是合法 JSON（不重试）。
var ErrBadEnvelope = errors.New("dify: 工作流响应不是合法 JSON")

// HTTPError Dify 工作流接口非 2xx 响应。
type HTTPError struct {
	StatusCode int
	Body       string // 截断后的响应体片段
}

// Error 错误文案。
func (e *HTTPError) Error() string {
	return fmt.Sprintf("dify: 工作流接口异常（HTTP %d）：%s", e.StatusCode, e.Body)
}

// HTTPStatus 状态码（瞬时重试判定用）。
func (e *HTTPError) HTTPStatus() int { return e.StatusCode }

// WorkflowError 工作流执行失败（data.status = failed / stopped）。
type WorkflowError struct {
	Status  string
	Message string
}

// Error 错误文案。
func (e *WorkflowError) Error() string {
	if e.Message != "" {
		return e.Message
	}
	if e.Status == "stopped" {
		return "Dify 工作流被停止"
	}
	return "Dify 工作流执行失败"
}

// Outputs 工作流输出变量集合（值为原始 JSON 类型）。
type Outputs map[string]any

// String 取键的字符串值（缺失/nil 为空串，其余 fmt.Sprint，与 Node String(v) 同口径）。
func (o Outputs) String(key string) string {
	if o == nil {
		return ""
	}
	v, ok := o[key]
	if !ok || v == nil {
		return ""
	}
	return fmt.Sprint(v)
}

// RunRequest 工作流触发入参。
type RunRequest struct {
	Inputs map[string]any // 工作流输入变量
	User   string         // 调用方标识（Dify user）
}

// RunResult 工作流运行结果（blocking 模式）。
type RunResult struct {
	WorkflowRunID  string
	TaskID         string
	Status         string  // succeeded / failed / stopped
	Outputs        Outputs // 工作流输出变量
	Error          string  // 失败原因（data.error，成功为空）
	ElapsedSeconds float64
}

// StreamInput 工作流触发入参（streaming 模式）。
type StreamInput struct {
	Inputs   map[string]any
	User     string
	OnFailed func(error) // 工作流 failed/stopped 或流读取异常回调（可 nil）
}

// WorkflowClient Dify 工作流客户端。密钥经 Provider 热读取或构造注入，不硬编码。
type WorkflowClient struct {
	// Cfg 配置热读取（读 APIURLKey / APIKeyKey）；nil 时仅用 APIKey 字段。
	Cfg Provider
	// APIURLKey Dify 基地址配置键（默认 DIFY_API_URL）。
	APIURLKey string
	// APIKey 应用 API Key 明文（构造注入；与 APIKeyKey 同时存在时 APIKeyKey 热读取优先）。
	APIKey string
	// APIKeyKey 应用 API Key 配置键（如 DIFY_WORKLOG_API_KEY / DIFY_SAFEDAY_API_KEY）。
	APIKeyKey string
	// HTTP 传输客户端（nil 用默认；streaming 响应不受 Timeout 约束）。
	HTTP *http.Client
	// Timeout blocking 单次尝试超时（默认 90s）。
	Timeout time.Duration
	// Retry 有界重试（缺省 MaxAttempts=3，仅瞬时传输故障；显式 MaxAttempts=1 关闭重试）。
	Retry RetryPolicy
	// Recorder 外部调用结构化事件写入口（可注入 *logging.Logger；nil 不写日志）。
	Recorder Recorder
}

// record 写本客户端的外部调用事件（Recorder nil 静默）。
func (c *WorkflowClient) record(ctx context.Context, level, event, messageCN, raw string, fields map[string]any) {
	if c == nil {
		return
	}
	record(c.Recorder, ctx, level, event, messageCN, raw, fields)
}

// httpc 取传输客户端。
func (c *WorkflowClient) httpc() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return &http.Client{}
}

// timeout 取单次尝试超时。
func (c *WorkflowClient) timeout() time.Duration {
	if c.Timeout > 0 {
		return c.Timeout
	}
	return defaultWorkflowTimeout
}

// retryPolicy 取重试策略（缺省 3 次总尝试）。
func (c *WorkflowClient) retryPolicy() RetryPolicy {
	if c.Retry.MaxAttempts == 0 {
		return RetryPolicy{MaxAttempts: 3, BaseWait: c.Retry.BaseWait, MaxWait: c.Retry.MaxWait, Sleep: c.Retry.Sleep}
	}
	return c.Retry
}

// creds 热读取基地址与 API Key。
func (c *WorkflowClient) creds(ctx context.Context) (baseURL, apiKey string, err error) {
	urlKey := c.APIURLKey
	if urlKey == "" {
		urlKey = DefaultDifyAPIURLKey
	}
	baseURL = Lookup(ctx, c.Cfg, urlKey)
	apiKey = Lookup(ctx, c.Cfg, c.APIKeyKey)
	if apiKey == "" {
		apiKey = c.APIKey
	}
	if strings.TrimSpace(baseURL) == "" || strings.TrimSpace(apiKey) == "" {
		if c.APIKeyKey != "" {
			return "", "", fmt.Errorf("%w（%s / %s）", ErrNotConfigured, urlKey, c.APIKeyKey)
		}
		return "", "", ErrNotConfigured
	}
	return baseURL, apiKey, nil
}

// runURL 拼工作流运行地址（base 已带 /v1 不重复拼接，对齐 utils/dify.js workflowRunUrl）。
func runURL(base string) string {
	b := strings.TrimRight(strings.TrimSpace(base), "/")
	if strings.HasSuffix(b, "/v1") {
		return b + workflowRunPath
	}
	return b + "/v1" + workflowRunPath
}

// runEnvelope blocking 响应信封。
type runEnvelope struct {
	WorkflowRunID string `json:"workflow_run_id"`
	TaskID        string `json:"task_id"`
	Data          struct {
		ID             string         `json:"id"`
		Status         string         `json:"status"`
		Outputs        map[string]any `json:"outputs"`
		Error          string         `json:"error"`
		ElapsedTime    float64        `json:"elapsed_time"`
		TotalTokens    int            `json:"total_tokens"`
		ExecutionSteps int            `json:"execution_steps"`
	} `json:"data"`
}

// newRequest 构造工作流运行请求。
func (c *WorkflowClient) newRequest(ctx context.Context, baseURL, apiKey string, payload []byte) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, runURL(baseURL), bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Content-Type", "application/json")
	return req, nil
}

// Run 触发工作流（blocking）：解析 outputs；失败/超时分类返回，瞬时故障有界重试。
func (c *WorkflowClient) Run(ctx context.Context, in RunRequest) (RunResult, error) {
	baseURL, apiKey, err := c.creds(ctx)
	if err != nil {
		c.recordRunFailed(ctx, in.User, errorClass(err), err)
		return RunResult{}, err
	}
	payload, err := json.Marshal(map[string]any{
		"inputs":        in.Inputs,
		"response_mode": "blocking",
		"user":          in.User,
	})
	if err != nil {
		return RunResult{}, err
	}
	var out RunResult
	err = c.retryPolicy().Do(ctx, transientError, func(pctx context.Context, _ int) error {
		actx, cancel := context.WithTimeout(pctx, c.timeout())
		defer cancel()
		req, err := c.newRequest(actx, baseURL, apiKey, payload)
		if err != nil {
			return err
		}
		res, err := c.httpc().Do(req)
		if err != nil {
			if actx.Err() != nil && pctx.Err() == nil {
				return fmt.Errorf("dify: 工作流调用超时（%s）：%w", c.timeout(), actx.Err())
			}
			return fmt.Errorf("dify: 工作流请求失败：%w", err)
		}
		defer res.Body.Close()
		raw, _ := io.ReadAll(io.LimitReader(res.Body, 8<<20))
		if res.StatusCode < 200 || res.StatusCode >= 300 {
			return &HTTPError{StatusCode: res.StatusCode, Body: truncateRunes(string(raw), 200)}
		}
		var env runEnvelope
		if err := json.Unmarshal(raw, &env); err != nil {
			return fmt.Errorf("%w：%v", ErrBadEnvelope, err)
		}
		status := strings.TrimSpace(env.Data.Status)
		msg := strings.TrimSpace(env.Data.Error)
		if status == "failed" || status == "stopped" || (msg != "" && status != "succeeded") {
			return &WorkflowError{Status: status, Message: msg}
		}
		out = RunResult{
			WorkflowRunID:  env.WorkflowRunID,
			TaskID:         env.TaskID,
			Status:         status,
			Outputs:        Outputs(env.Data.Outputs),
			ElapsedSeconds: env.Data.ElapsedTime,
		}
		return nil
	})
	if err != nil {
		c.recordRunFailed(ctx, in.User, errorClass(err), err)
		return RunResult{}, err
	}
	c.record(ctx, "info", "dify.workflow.run", "Dify 工作流调用成功",
		fmt.Sprintf("[dify] workflow run ok run_id=%s status=%s", out.WorkflowRunID, out.Status),
		map[string]any{"key_name": c.APIKeyKey, "user": in.User,
			"workflow_run_id": out.WorkflowRunID, "task_id": out.TaskID,
			"status": out.Status, "elapsed_seconds": out.ElapsedSeconds})
	return out, nil
}

// Start 触发工作流（streaming）：触发成功立即返回，SSE 流在后台消费；
// 工作流 failed/stopped 或流读取异常时调用 OnFailed。触发请求单次不重试（避免重复触发工作流）。
// 触发与流消费经 Recorder 写结构化事件：触发成功/流完成记 info，触发失败/流失败记分类异常。
func (c *WorkflowClient) Start(ctx context.Context, in StreamInput) error {
	baseURL, apiKey, err := c.creds(ctx)
	if err != nil {
		c.recordStartFailed(ctx, in.User, errorClass(err), err)
		return err
	}
	payload, err := json.Marshal(map[string]any{
		"inputs":        in.Inputs,
		"response_mode": "streaming",
		"user":          in.User,
	})
	if err != nil {
		c.recordStartFailed(ctx, in.User, errorClass(err), err)
		return err
	}
	// 流式响应在触发后长期存续，不能随请求 ctx 取消：脱离取消保留取值
	bctx := context.WithoutCancel(ctx)
	req, err := c.newRequest(bctx, baseURL, apiKey, payload)
	if err != nil {
		c.recordStartFailed(ctx, in.User, errorClass(err), err)
		return err
	}
	res, err := c.httpc().Do(req)
	if err != nil {
		err = fmt.Errorf("Dify 工作流触发失败：%w", err)
		c.recordStartFailed(ctx, in.User, errorClass(err), err)
		return err
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		text, _ := io.ReadAll(io.LimitReader(res.Body, 200))
		res.Body.Close()
		err = fmt.Errorf("Dify 工作流触发失败（HTTP %d）：%s", res.StatusCode, truncateRunes(string(text), 200))
		c.recordStartFailed(ctx, in.User, classHTTPStatus, err)
		return err
	}
	c.record(ctx, "info", "dify.workflow.start", "Dify 工作流已触发",
		"[dify] workflow streaming started",
		map[string]any{"key_name": c.APIKeyKey, "user": in.User})
	go func() {
		defer res.Body.Close()
		failed := false
		onFailed := func(err error) {
			failed = true
			class := errorClass(err)
			c.record(bctx, failureLevel(class), "dify.workflow.stream.failed",
				"Dify 工作流执行异常（"+errorClassCN(class)+"）",
				fmt.Sprintf("[dify] workflow stream failed: %v", err),
				map[string]any{"key_name": c.APIKeyKey, "user": in.User, "error_class": class})
			if in.OnFailed != nil {
				in.OnFailed(err)
			}
		}
		if err := consumeWorkflowSSE(res.Body, onFailed); err != nil {
			onFailed(fmt.Errorf("工作流流读取异常：%w", err))
			return
		}
		if !failed {
			c.record(bctx, "info", "dify.workflow.stream.finished", "Dify 工作流流式执行完成",
				"[dify] workflow stream finished",
				map[string]any{"key_name": c.APIKeyKey, "user": in.User})
		}
	}()
	return nil
}

// recordStartFailed 写触发失败事件。
func (c *WorkflowClient) recordStartFailed(ctx context.Context, user, class string, err error) {
	if class == "" {
		class = classUnknown
	}
	c.record(ctx, failureLevel(class), "dify.workflow.start.failed",
		"Dify 工作流触发失败（"+errorClassCN(class)+"）",
		fmt.Sprintf("[dify] workflow start failed: %v", err),
		map[string]any{"key_name": c.APIKeyKey, "user": user, "error_class": class})
}

// recordRunFailed 写 blocking 调用失败事件。
func (c *WorkflowClient) recordRunFailed(ctx context.Context, user, class string, err error) {
	if class == "" {
		class = classUnknown
	}
	c.record(ctx, failureLevel(class), "dify.workflow.run.failed",
		"Dify 工作流调用失败（"+errorClassCN(class)+"）",
		fmt.Sprintf("[dify] workflow run failed: %v", err),
		map[string]any{"key_name": c.APIKeyKey, "user": user, "error_class": class})
}

// sseEvent Dify SSE 流事件。
type sseEvent struct {
	Event string `json:"event"`
	Data  struct {
		Status  string `json:"status"`
		Error   string `json:"error"`
		Message string `json:"message"`
	} `json:"data"`
}

// consumeWorkflowSSE 消费工作流 SSE 流（对齐 safeday/dify.js consumeStream）：
// workflow_finished 且 failed/stopped 时回调 onFailed；succeeded 不回调（完成标记由调用方负责）；
// 读到 workflow_finished 或 EOF 即结束，其余读取错误向上返回（由调用方转 onFailed）。
func consumeWorkflowSSE(body io.Reader, onFailed func(error)) error {
	reader := bufio.NewReader(body)
	for {
		line, err := reader.ReadString('\n')
		if line != "" {
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "data:") {
				payload := strings.TrimSpace(strings.TrimPrefix(trimmed, "data:"))
				if payload != "" {
					var evt sseEvent
					if json.Unmarshal([]byte(payload), &evt) == nil && evt.Event == "workflow_finished" {
						status := evt.Data.Status
						if status == "failed" || status == "stopped" {
							msg := evt.Data.Error
							if msg == "" {
								msg = evt.Data.Message
							}
							we := &WorkflowError{Status: status, Message: msg}
							if onFailed != nil {
								onFailed(we)
							}
						}
						return nil
					}
				}
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
	}
}

// truncateRunes 按 rune 截断。
func truncateRunes(s string, n int) string {
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return string(runes[:n])
}
