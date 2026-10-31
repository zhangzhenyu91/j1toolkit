// Package outbound 提供出站调用上下文标注（service / attempt），不依赖任何业务包，
// 供 integrations 观测层与各业务包（包括被 integrations 反向依赖的包）共用，避免循环依赖。
package outbound

import (
	"context"
	"strings"
)

type serviceKey struct{}
type attemptKey struct{}

// WithServiceName 标注出站调用所属服务（dify/cos/weknora/sgcc 等）。
func WithServiceName(ctx context.Context, name string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return ctx
	}
	return context.WithValue(ctx, serviceKey{}, name)
}

// ServiceName 取出站服务标注（缺失为空）。
func ServiceName(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	name, _ := ctx.Value(serviceKey{}).(string)
	return name
}

// WithAttempt 标注当前重试序号（从 1 起）。
func WithAttempt(ctx context.Context, attempt int) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if attempt < 1 {
		attempt = 1
	}
	return context.WithValue(ctx, attemptKey{}, attempt)
}

// Attempt 取重试序号（缺失为 1）。
func Attempt(ctx context.Context) int {
	if ctx == nil {
		return 1
	}
	if n, ok := ctx.Value(attemptKey{}).(int); ok && n > 0 {
		return n
	}
	return 1
}
