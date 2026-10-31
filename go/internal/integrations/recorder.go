// 外部服务调用结构化事件：Dify / COS / AMap 经可注入 Recorder 写中文结构化事件
// （中文说明与技术原文分离，category=external，trace_id 由 logging 从 ctx 自动带出）；
// 异常带稳定分类（fields.error_class）且落库前脱敏，绝不泄漏 API Key / token / 签名；
// Recorder 默认 nil，不额外写日志。写日志失败与异常一律吞掉，不影响业务返回。
package integrations

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/url"
	"time"

	"shade/internal/logging"
	"shade/internal/logsafe"
)

// Recorder 外部服务调用的结构化事件写入口（*logging.Logger 直接满足；nil 不写日志）。
type Recorder interface {
	Record(ctx context.Context, level, category, event, messageCN, raw string, fields map[string]any) error
}

// 编译期契约：生产装配注入 *logging.Logger。
var _ Recorder = (*logging.Logger)(nil)

// externalCategory 外部调用事件统一类别。
const externalCategory = "external"

// recordWriteTimeout 单条外部事件落库超时。
const recordWriteTimeout = 5 * time.Second

// error_class 稳定分类标识（落 fields.error_class，勿随意改名）。
const (
	classNotConfigured  = "not_configured"  // 必需配置/密钥缺失
	classInvalidInput   = "invalid_input"   // 入参非法（坐标越界等）
	classTimeout        = "timeout"         // 调用超时
	classNetwork        = "network"         // 网络传输异常
	classCanceled       = "canceled"        // 调用方取消
	classHTTPStatus     = "http_status"     // 上游接口非 2xx
	classAPIError       = "api_error"       // 上游业务失败（如 status != 1）
	classWorkflowFailed = "workflow_failed" // Dify 工作流 failed/stopped
	classBadResponse    = "bad_response"    // 响应不是合法 JSON
	classEmptyResult    = "empty_result"    // 调用成功但结果为空
	classUnknown        = "unknown"         // 其他未归类
)

// errorClass 将错误归类为稳定分类标识（nil 返回空串）。
func errorClass(err error) string {
	if err == nil {
		return ""
	}
	switch {
	case errors.Is(err, ErrNotConfigured), errors.Is(err, ErrCOSNotConfigured):
		return classNotConfigured
	case errors.Is(err, context.Canceled):
		return classCanceled
	case errors.Is(err, context.DeadlineExceeded):
		return classTimeout
	}
	var we *WorkflowError
	if errors.As(err, &we) {
		return classWorkflowFailed
	}
	var he *HTTPError
	if errors.As(err, &he) {
		return classHTTPStatus
	}
	var se *statusError
	if errors.As(err, &se) {
		return classHTTPStatus
	}
	if errors.Is(err, ErrBadEnvelope) {
		return classBadResponse
	}
	var syn *json.SyntaxError
	var utype *json.UnmarshalTypeError
	if errors.As(err, &syn) || errors.As(err, &utype) {
		return classBadResponse
	}
	var ne net.Error
	if errors.As(err, &ne) {
		if ne.Timeout() {
			return classTimeout
		}
		return classNetwork
	}
	var ue *url.Error
	if errors.As(err, &ue) {
		return classNetwork
	}
	return classUnknown
}

// errorClassCN 分类的中文说明（拼进 messageCN）。
func errorClassCN(class string) string {
	switch class {
	case classNotConfigured:
		return "未配置"
	case classInvalidInput:
		return "入参非法"
	case classTimeout:
		return "调用超时"
	case classNetwork:
		return "网络异常"
	case classCanceled:
		return "调用取消"
	case classHTTPStatus:
		return "接口状态异常"
	case classAPIError:
		return "接口返回失败"
	case classWorkflowFailed:
		return "工作流执行失败"
	case classBadResponse:
		return "响应解析失败"
	case classEmptyResult:
		return "结果为空"
	default:
		return "未知异常"
	}
}

// sensitiveName 敏感参数名判定（与 logsafe 查询参数同口径）。
func sensitiveName(name string) bool { return logsafe.SensitiveKey(name) }

// scrubSecrets 脱敏自由文本：name=value 敏感参数、Bearer 令牌、JSON 密钥字段值一律 ****。
func scrubSecrets(s string) string { return logsafe.TextForLog(s) }

// scrubFields 脱敏结构化字段：敏感字段名整值 ****，字符串值再过 scrubSecrets；
// 路径类 key（path/key_name/object_key）不过度掩码。
func scrubFields(fields map[string]any) map[string]any { return logsafe.FieldsForLog(fields) }

// record 写外部调用结构化事件：nil 静默；脱敏后落库；写失败与异常一律吞掉。
// 脱离请求取消并限时写入（trace_id 等 context 值保留），超时/取消不丢事件、也不拖业务。
func record(rec Recorder, ctx context.Context, level, event, messageCN, raw string, fields map[string]any) {
	if rec == nil {
		return
	}
	defer func() { _ = recover() }()
	rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), recordWriteTimeout)
	defer cancel()
	_ = rec.Record(rctx, level, externalCategory, event, scrubSecrets(messageCN), scrubSecrets(raw), scrubFields(fields))
}

// failureLevel 失败事件级别：未配置与调用取消记 warn，其余记 error。
func failureLevel(class string) string {
	if class == classNotConfigured || class == classCanceled {
		return "warn"
	}
	return "error"
}
