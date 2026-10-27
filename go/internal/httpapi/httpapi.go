// Package httpapi 提供 Shade 壹匣 Go 服务的 HTTP 基础接口：chi 路由 + 统一 JSON 响应信封，
// 业务依赖以接口注入（AppCatalog / ConfigAdmin / LogReader），便于上层接 MySQL 实现。
//
// 路由：
//
//	GET  /healthz               健康检查（不依赖数据库）
//	GET  /api/v1/app/list       应用列表
//	GET  /api/v1/admin/config   配置读取（敏感值脱敏，支持 ?key= / ?group=）
//	PUT  /api/v1/admin/config   配置更新（响应仍脱敏，不回显完整敏感值）
//	GET  /api/v1/app-logs       应用日志（筛选 + 分页）
//
// 响应契约：统一 {code,message,data}。成功 code=0、message="ok"；失败 code 为稳定字符串错误码
// （invalid_param / app_not_found / not_found / dependency_unavailable / internal_error），
// message 为中文提示，data 为 nil。依赖未接入（nil）或后端不可用时返回 dependency_unavailable。
// 响应绝不回显完整敏感配置值（见 MaskValue）。
package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
)

// 稳定错误码（对外契约，勿随意改名）。
const (
	// CodeOK 成功（数值 0）。
	CodeOK = 0
	// CodeInvalidParam 参数错误。
	CodeInvalidParam = "invalid_param"
	// CodeAppNotFound 未知应用。
	CodeAppNotFound = "app_not_found"
	// CodeNotFound 其他对象不存在（如未知配置键）。
	CodeNotFound = "not_found"
	// CodeDependencyUnavailable 依赖未接入或不可用（如数据库未配置）。
	CodeDependencyUnavailable = "dependency_unavailable"
	// CodeInternal 其他内部错误。
	CodeInternal = "internal_error"
)

// Envelope 统一 JSON 响应信封 {code,message,data}。
// 成功 Code=0（数值）；失败为稳定字符串错误码（见上方常量）。
type Envelope struct {
	Code    any    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data"`
}

// APIError 结构化接口错误：依赖实现层可返回以指定稳定错误码、提示与 HTTP 状态码。
type APIError struct {
	Code    string // 稳定错误码
	Message string // 面向调用方的中文提示
	Status  int    // HTTP 状态码（0 取 500）
	Err     error  // 底层错误（仅内部使用，不写入响应）
}

func (e *APIError) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("%s: %s: %v", e.Code, e.Message, e.Err)
	}
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

// Unwrap 暴露底层错误。
func (e *APIError) Unwrap() error { return e.Err }

// Is 按错误码判定：同码即视为同一哨兵（配合 errors.Is 使用）。
func (e *APIError) Is(target error) bool {
	t, ok := target.(*APIError)
	return ok && e.Code == t.Code
}

// 预置错误哨兵（errors.Is 按错误码判定；需要带提示时用下方构造函数）。
var (
	// ErrInvalidParam 参数错误。
	ErrInvalidParam = &APIError{Code: CodeInvalidParam, Message: "参数错误", Status: http.StatusBadRequest}
	// ErrNotFound 对象不存在（未知配置键等；未知应用用 CodeAppNotFound）。
	ErrNotFound = &APIError{Code: CodeNotFound, Message: "资源不存在", Status: http.StatusNotFound}
	// ErrDependencyUnavailable 依赖未接入或不可用（如数据库未配置）。
	ErrDependencyUnavailable = &APIError{Code: CodeDependencyUnavailable, Message: "依赖未接入或不可用", Status: http.StatusServiceUnavailable}
)

// InvalidParam 构造参数错误。
func InvalidParam(msg string) *APIError {
	return &APIError{Code: CodeInvalidParam, Message: msg, Status: http.StatusBadRequest}
}

// AppNotFound 构造未知应用错误。
func AppNotFound(msg string) *APIError {
	return &APIError{Code: CodeAppNotFound, Message: msg, Status: http.StatusNotFound}
}

// NotFound 构造对象不存在错误。
func NotFound(msg string) *APIError {
	return &APIError{Code: CodeNotFound, Message: msg, Status: http.StatusNotFound}
}

// DependencyUnavailable 构造依赖未接入/不可用错误。
func DependencyUnavailable(msg string) *APIError {
	return &APIError{Code: CodeDependencyUnavailable, Message: msg, Status: http.StatusServiceUnavailable}
}

// Internal 构造内部错误（底层错误仅内部保留，不写入响应，避免泄漏配置/连接信息）。
func Internal(err error) *APIError {
	return &APIError{Code: CodeInternal, Message: "内部错误", Status: http.StatusInternalServerError, Err: err}
}

// mapError 将错误映射为响应信封与 HTTP 状态码。
func mapError(err error) (Envelope, int) {
	var ae *APIError
	if errors.As(err, &ae) {
		msg := ae.Message
		if msg == "" {
			msg = "内部错误"
		}
		status := ae.Status
		if status == 0 {
			status = http.StatusInternalServerError
		}
		return Envelope{Code: ae.Code, Message: msg, Data: nil}, status
	}
	return Envelope{Code: CodeInternal, Message: "内部错误", Data: nil}, http.StatusInternalServerError
}

// writeJSON 写出 JSON 响应（统一 Content-Type 与 no-store）。
func writeJSON(w http.ResponseWriter, status int, env Envelope) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(env)
}

// writeOK 写出成功信封（code=0）。
func writeOK(w http.ResponseWriter, data any) {
	writeJSON(w, http.StatusOK, Envelope{Code: CodeOK, Message: "ok", Data: data})
}

// writeErr 写出错误信封（稳定错误码 + 对应 HTTP 状态码）。
func writeErr(w http.ResponseWriter, err error) {
	env, status := mapError(err)
	writeJSON(w, status, env)
}
