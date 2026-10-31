// 出站观测：包装 http.RoundTripper，只观察 method/service/host/path/status/duration/
// attempt/bytes/error_class；不读取/缓冲响应体（SSE、大文件流式透传不受影响）。
// 每次尝试一条：2xx 写轻量 http.outbound，4xx/5xx/网络错误写失败事件；不读 body。
package integrations

import (
	"context"
	"fmt"
	"math"
	"net/http"
	"sync"
	"time"

	"shade/internal/logging"
	"shade/internal/logsafe"
	"shade/internal/outbound"
)

// WithServiceName / WithAttempt 兼容入口（实现下沉 outbound，避免业务包循环依赖）。
func WithServiceName(ctx context.Context, name string) context.Context {
	return outbound.WithServiceName(ctx, name)
}

// WithAttempt 标注当前重试序号。
func WithAttempt(ctx context.Context, attempt int) context.Context {
	return outbound.WithAttempt(ctx, attempt)
}

// AttemptFromContext 取重试序号（缺失为 1）。
func AttemptFromContext(ctx context.Context) int { return outbound.Attempt(ctx) }

// 全局观测 Recorder：生产 main 在创建 DurableSink 后立即安装，供已构建的 client 使用。
var (
	observedMu       sync.RWMutex
	observedRecorder Recorder
)

// SetObservedRecorder 安装全局出站观测写入器（nil 关闭）。
func SetObservedRecorder(rec Recorder) {
	observedMu.Lock()
	observedRecorder = rec
	observedMu.Unlock()
}

// currentObservedRecorder 取全局观测写入器。
func currentObservedRecorder() Recorder {
	observedMu.RLock()
	rec := observedRecorder
	observedMu.RUnlock()
	return rec
}

// 编译期契约：观测层直接用于 http.Client.Transport。
var _ http.RoundTripper = (*ObservedTransport)(nil)

// ObservedTransport 观测传输层：Base 保留独立 *http.Transport（代理/拨号/池参数不丢），
// 包装层只做计时与失败事件，不修改请求头、不消费响应体。
type ObservedTransport struct {
	// *http.Transport 同时内嵌：保留旧调用方对 Proxy/MaxIdleConns/DialContext 等字段的直接访问，
	// Base 指向同一独立实例（禁止把包装层再当 base）。
	*http.Transport
	Base     *http.Transport
	Recorder Recorder // nil 时用全局 Recorder
	Service  string   // 默认服务名；ctx 标注优先
}

// WrapObserved 包装 base（nil 时使用标准库默认参数）。
func WrapObserved(base *http.Transport) *ObservedTransport {
	if base == nil {
		base = cloneBaseTransport() // 继承标准默认参数（代理/拨号/TLS/HTTP2），不创建空壳
	}
	return &ObservedTransport{Transport: base, Base: base}
}

// BaseTransport 返回独立 base（供需要 CloseIdleConnections/代理断言的调用方使用）。
func (t *ObservedTransport) BaseTransport() *http.Transport {
	if t == nil {
		return nil
	}
	if t.Base != nil {
		return t.Base
	}
	return t.Transport
}

// SetDisableCompression 动态设置 base 的压缩协商（商旅通道要求关闭）。
func (t *ObservedTransport) SetDisableCompression(v bool) {
	if base := t.BaseTransport(); base != nil {
		base.DisableCompression = v
	}
}

// CloseIdleConnections 关闭 base 空闲连接。
func (t *ObservedTransport) CloseIdleConnections() {
	if base := t.BaseTransport(); base != nil {
		base.CloseIdleConnections()
	}
}

// RoundTrip 实现 http.RoundTripper；响应体原样返回，绝不读取。
func (t *ObservedTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	base := t.base()
	start := time.Now()
	resp, err := base.RoundTrip(req)
	dur := time.Since(start)
	ctx := req.Context()

	method := req.Method
	host := ""
	path := "/"
	if req.URL != nil {
		host = req.URL.Host
		if escaped := req.URL.EscapedPath(); escaped != "" {
			path = logsafe.PathForLog(escaped)
		}
	}
	service := t.Service
	if fromCtx := outbound.ServiceName(ctx); fromCtx != "" {
		service = fromCtx
	}
	if service == "" {
		service = "http"
	}
	status := 0
	bytes := int64(0)
	if resp != nil {
		status = resp.StatusCode
		bytes = resp.ContentLength // chunked/未知为 -1，不能写成误导性的 0
	}
	fields := map[string]any{
		"method":      method,
		"service":     service,
		"host":        host,
		"path":        path,
		"status":      status,
		"duration_ms": math.Round(float64(dur)/float64(time.Millisecond)*10) / 10,
		"attempt":     outbound.Attempt(ctx),
		"bytes":       bytes,
		"trace_id":    logging.TraceIDFromContext(ctx),
	}
	rec := t.Recorder
	if rec == nil {
		rec = currentObservedRecorder()
	}
	if err != nil {
		class := errorClass(err)
		fields["error_class"] = class
		level := "error"
		if class == classCanceled {
			level = "warn"
		}
		messageCN := fmt.Sprintf("外部请求网络异常（%s %s%s，%s）", method, host, path, class)
		raw := fmt.Sprintf("%s %s%s status=0 duration_ms=%.1f attempt=%d error_class=%s",
			method, host, path, fields["duration_ms"], fields["attempt"], class)
		record(rec, ctx, level, "http.outbound.error", messageCN, raw, fields)
		return resp, err
	}
	if status >= 400 {
		fields["error_class"] = classHTTPStatus
		level := "warn"
		if status >= 500 {
			level = "error"
		}
		messageCN := fmt.Sprintf("外部请求失败（%s %s%s，HTTP %d）", method, host, path, status)
		raw := fmt.Sprintf("%s %s%s status=%d duration_ms=%.1f attempt=%d bytes=%d",
			method, host, path, status, fields["duration_ms"], fields["attempt"], bytes)
		record(rec, ctx, level, "http.outbound.failed", messageCN, raw, fields)
		return resp, nil
	}
	if status >= 200 && status < 300 {
		messageCN := fmt.Sprintf("外部请求成功（%s %s%s，HTTP %d）", method, host, path, status)
		raw := fmt.Sprintf("%s %s%s status=%d duration_ms=%.1f attempt=%d bytes=%d",
			method, host, path, status, fields["duration_ms"], fields["attempt"], bytes)
		record(rec, ctx, "info", "http.outbound", messageCN, raw, fields)
	}
	return resp, nil
}

// base 返回底层传输层（构造后不再变化）。
func (t *ObservedTransport) base() *http.Transport {
	if t == nil {
		return &http.Transport{}
	}
	if t.Base != nil {
		return t.Base
	}
	if t.Transport != nil {
		return t.Transport
	}
	return &http.Transport{}
}
