// 链路 ID：HTTP 访问日志中间件生成后经请求 context 向下贯穿，
// 各处结构化日志（Logger.Record / WriteEntry）写库时自动带出，实现 trace_id 贯穿。
package logging

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"strings"
	"time"
)

// traceContextKey 请求 context 中的链路 ID 键。
type traceContextKey struct{}

// maxTraceIDLen app_log.trace_id 列上限（VARCHAR(64)）。
const maxTraceIDLen = 64

// NewTraceID 生成新链路 ID（32 位十六进制随机数）；随机源不可用时退化为时间戳十六进制。
func NewTraceID() string {
	var buf [16]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return hex.EncodeToString([]byte(time.Now().UTC().Format("20060102150405.000000000")))
	}
	return hex.EncodeToString(buf[:])
}

// WithTraceID 把链路 ID 写入 context（向下游贯穿）；非法或超长 ID 视为空。
func WithTraceID(ctx context.Context, id string) context.Context {
	id = strings.TrimSpace(id)
	if id == "" || len(id) > maxTraceIDLen {
		return ctx
	}
	for _, r := range id {
		switch {
		case r >= '0' && r <= '9', r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z',
			r == '-', r == '_', r == '.':
		default:
			return ctx
		}
	}
	return context.WithValue(ctx, traceContextKey{}, id)
}

// TraceIDFromContext 取 context 中的链路 ID（缺失为空串）。
func TraceIDFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	id, _ := ctx.Value(traceContextKey{}).(string)
	return id
}
