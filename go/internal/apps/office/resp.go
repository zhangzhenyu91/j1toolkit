// Package office 迁移 server/src/office：ONLYOFFICE Docs 在线编辑对接（社区版自托管）。
//
//	GET  /api/v1/office/status            启用状态
//	POST /api/v1/office/config            签发编辑器配置（多源：netdisk 可编辑；其余源只读）
//	GET  /api/v1/office/raw/:ticket/:name? 免登录回源（票据即鉴权，只读源统一走此路由）
//	POST /api/v1/office/callback/:ticket  保存回调（OnlyOffice JWT + 回调票据双校验，应答 {error:0}）
//
// 安全口径：回源票据短时效绑定单来源单文件；回调须同时通过 OnlyOffice JWT（OO_JWT_SECRET）与
// 本服务回调票据双校验；后五个 view-only 来源强制 mode=view、不配 callbackUrl（只读不回写）。
// 网盘来源保存产物经 OpenList 写回原路径。文档来源与 OpenList 客户端均可注入，测试不实连。
package office

import (
	"context"
	"encoding/json"
	"net/http"
)

// 响应信封（数字业务码）。
const (
	codeOK            = 0
	codeParam         = 40001
	codeBadFormat     = 40030
	codeUnknownSrc    = 40010
	codeUnsupported   = 40011
	codeAppDenied     = 40301
	codeUnauthorized  = 40101
	codeBadTicket     = 40311
	codeNotFound      = 40400
	codeUpstream      = 50201
	codeNotConfigured = 50301
)

// Envelope 统一响应信封。
type Envelope struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

func writeJSON(w http.ResponseWriter, status int, env Envelope) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(env)
}

func ok(w http.ResponseWriter, data any, message string) {
	if message == "" {
		message = "ok"
	}
	writeJSON(w, http.StatusOK, Envelope{Code: codeOK, Message: message, Data: data})
}

func fail(w http.ResponseWriter, status, code int, message string) {
	writeJSON(w, status, Envelope{Code: code, Message: message})
}

// writeOO OnlyOffice 约定应答（不走统一信封）：{"error":0} 或 {"error":1,"message":"..."}。
func writeOO(w http.ResponseWriter, status, errCode int, message string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if errCode == 0 {
		_, _ = w.Write([]byte(`{"error":0}`))
		return
	}
	msg, _ := json.Marshal(message)
	_, _ = w.Write([]byte(`{"error":1,"message":` + string(msg) + `}`))
}

// writeText 纯文本应答（/raw 回源错误；OO 不消费 {code,message,data} 信封，Node .type('text') 同口径）。
func writeText(w http.ResponseWriter, status int, text string) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(text))
}

// AppPermit 应用权限检查（/config 按 src 分派来源应用）。
type AppPermit interface {
	Allow(ctx context.Context, userID int64, appKey string) (bool, error)
}

// AllowAllPermit 放行全部用户（本地开发/测试用）。
type AllowAllPermit struct{}

func (AllowAllPermit) Allow(context.Context, int64, string) (bool, error) { return true, nil }

// ConfigReader 运行时配置（OO_*）。
type ConfigReader interface {
	Get(ctx context.Context, key string) (string, error)
}
