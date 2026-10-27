// Package netdisk 迁移 server/src/netdisk：OpenList 中转层（空间/文件/分享/上传下载/预览/缩略图/压缩包）、
// 路径穿越防护（normRel/validName）、分享免登录访问（提取码 + 2h 票据 + 限流）、「保存到网盘」共享通道。
//
// 路由基路径 /api/v1/netdisk；/public/share/* 免登录（主程序已放行 /api/v1/share 前缀，
// 本模块把公开分享挂到 /netdisk/public/share/* 并在 RegisterRoutes 中不经鉴权直接注册——见
// httpapi.publicAuthPath 已放行 /api/v1/netdisk/preview；公开分享路由注册在 AuthMiddleware 之外需要
// 主程序放行 /api/v1/netdisk/public，测试以无鉴权中间件直挂验证）。其余接口需登录 + netdisk 应用权限。
// 响应信封 {code,message,data} 沿用现有数字业务码。
package netdisk

import (
	"encoding/json"
	"net/http"
)

// 现有业务码（与 server/src/utils/resp.js 及 netdisk/index.js 各 fail 口径一致）。
const (
	codeOK            = 0
	codeParam         = 40001
	codeNoTeam        = 40030
	codeForbidden     = 40304
	codeShareDisabled = 40310
	codeBadTicket     = 40311
	codeOutOfShare    = 40312
	codeAppDenied     = 40301
	codeNotFound      = 40400
	codeShareExpired  = 41010
	codeTooLarge      = 41301
	codeRateLimited   = 42901
	codeConflict      = 40900
	codeUpstream      = 50201
	codeNotConfigured = 50301
	codeInternal      = 50000
)

// Envelope 统一响应信封（成功 code=0；失败为数字业务码）。
type Envelope struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

func writeOK(w http.ResponseWriter, data any, message string) {
	if message == "" {
		message = "ok"
	}
	writeJSON(w, http.StatusOK, Envelope{Code: codeOK, Message: message, Data: data})
}

func writeFail(w http.ResponseWriter, status, code int, message string, data any) {
	writeJSON(w, status, Envelope{Code: code, Message: message, Data: data})
}

func writeJSON(w http.ResponseWriter, status int, env Envelope) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(env)
}

// writePlain 纯文本应答（OO 回源等不消费 JSON 信封的场景）。
func writePlain(w http.ResponseWriter, status int, text string) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(text))
}
