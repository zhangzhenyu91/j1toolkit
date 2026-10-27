// Package callme 迁移 server/src/routes/callme.js 与 services/weknora.js：WeKnora 知识库对话代理。
//
// 路由基路径 /api/v1/callme：
//
//	POST /ingest-chat                    群内提问直推（crane-answer 用，凭 X-Ingest-Token 免登录）
//	POST /sessions  GET /sessions        会话新建 / 列表（按登录账号 X-External-User-ID 隔离）
//	GET/PUT/DELETE /sessions/{id}        会话详情（含消息）/ 改名 / 删除
//	DELETE /sessions/{id}/messages/{mid} 问答成对删除（同 request_id）
//	POST/GET /sessions/{id}/attachments  会话附件上传 / 列表
//	GET /capabilities                    模型列表 + 联网搜索能力探测
//	POST /chat                           SSE 流式问答（归一化事件）
//
// 全部登录接口需 call-me 应用权限；WeKnora / Dify WF3 文件命名客户端均可注入，测试不实连。
package callme

import (
	"context"
	"encoding/json"
	"net/http"
)

// 响应信封 {code,message,data}（与 server/src/utils/resp.js 一致；/ingest-chat 用 {ok,...} 契约另见 ingest.go）。
const (
	codeOK              = 0
	codeParam           = 40003
	codeTitleEmpty      = 40016
	codeAttachSize      = 40027
	codeAttachType      = 40028
	codeAttachMissing   = 40029
	codeAttachIDs       = 40043
	codeImageCount      = 40044
	codeImageFormat     = 40045
	codeImageSize       = 40046
	codeWebSearch       = 40047
	codeModelID         = 40048
	codeReasoningEffort = 40049
	codeUnauthorized    = 40101
	codeNoApp           = 40301
	codeSessionNotFound = 40402
	codeUpstreamAuth    = 50201
	codeNotConfigured   = 50302
	codeUpstreamTimeout = 50401
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

// AppPermit 应用权限检查（对应 requireApp('call-me') / hasAppPermission）。
type AppPermit interface {
	Allow(ctx context.Context, userID int64, appKey string) (bool, error)
}

// AllowAllPermit 放行全部用户（本地开发/测试用）。
type AllowAllPermit struct{}

func (AllowAllPermit) Allow(context.Context, int64, string) (bool, error) { return true, nil }

// ConfigReader 运行时配置读取（CALLME_INGEST_TOKEN / WeKnora 配置）。
type ConfigReader interface {
	Get(ctx context.Context, key string) (string, error)
}
