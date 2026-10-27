// Package chatlog 聊天日志归档（迁移 server/src/chatlog/index.js）：crane-answer treeWatch 兜底
// 子能力每日把群兜底消息纪要 md 与当天文档直推本端点，写入 WeKnora 知识库「检修一班历史聊天记录」
// 供 Call Me 问答引用群历史讨论。纯对接端点：无 sys_app 登记、无前端页面、无权限应用。
//
// 路由基路径 /api/v1/chatlog：
//
//	POST /ingest   每日归档直推（免登录，凭 X-Ingest-Token，挂 auth 门控之前）
//
// 对接契约（与 crane-answer 侧约定）：一次调用只带一件——
//
//	kind=digest：{ date:'yyyy-MM-dd', group_name, kind, digest_md:'<markdown 正文>' }——存「{群名}_{date}.md」
//	kind=file：{ date, group_name, kind, file:{ name, data_base64 } }——用原名，base64 纯内容无前缀、解码后 ≤30MB
//
// 成功 {ok:true, data:{date, group_name, kind, folder, file_name, knowledge_id, replaced}}；
// 失败 {ok:false, code, message}（业务拒绝口径同 callme /ingest-chat：参数非法 400、文档类型
// unsupported_file 为 HTTP 200 + ok:false；密钥未配 503、密钥不等 401、WeKnora 未配 503、上游 502）。
// 落库：WEKNORA_CHATLOG_KB_ID 知识库 /{yyyy}/{M.d}/ 两级文件夹（月.日不带前导零），同文件夹同名
// 先删后传（幂等重跑/补跑覆盖当天内容），仍 409 取响应已有 id 删除后重试一次、再冲突追加「-1」后缀；
// 摄取渠道 channel='chatlog'，与树障/交跨既有渠道清理互不影响。
//
// 装配示例（主程序 main.go，cfg 为 *config.Store）：
//
//	cl := chatlog.New(cfg, callme.NewHTTPClient(cfg)) // 知识库客户端复用 callme WeKnora 客户端
//	cl.RegisterRoutes(r) // 挂载在 /api/v1 之下；/ingest 须在鉴权门控之前放行（JSON 上限 ≥50MB）
//
// 密钥 fail-closed：CHATLOG_INGEST_TOKEN / WEKNORA_API_KEY 未配置一律 503 拒绝，不带空密钥外呼。
package chatlog

import (
	"context"
	"crypto/subtle"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"shade/internal/apps/callme"
)

// KBClient WeKnora 知识库内容客户端（由 callme 的 WeKnora HTTP 客户端满足；测试注入 fake）。
type KBClient = callme.KnowledgeAPI

// ConfigReader 运行时配置读取（CHATLOG_INGEST_TOKEN / WEKNORA_API_KEY / WEKNORA_CHATLOG_KB_ID；
// *config.Store 满足；测试可注入桩）。
type ConfigReader interface {
	Get(ctx context.Context, key string) (string, error)
}

// Module 聊天日志归档模块。
type Module struct {
	// Cfg 运行时配置（nil 时对接密钥视为未配置）。
	Cfg ConfigReader
	// KB WeKnora 知识库客户端；nil 时按 WeKnora 未配置拒绝。
	KB KBClient
	// IngestToken 直推密钥（优先于 Cfg 读取；测试注入）。
	IngestToken string
}

// New 构造聊天日志归档模块。
func New(cfg ConfigReader, kb KBClient) *Module {
	return &Module{Cfg: cfg, KB: kb}
}

// RegisterRoutes 注册 /api/v1/chatlog 路由（挂载在 /api/v1 之下）。
// /ingest 免登录（主程序须放行该路径免鉴权，同 treehazard /ingest 模式）。
func (m *Module) RegisterRoutes(r chi.Router) {
	r.Route("/chatlog", func(r chi.Router) {
		r.Post("/ingest", m.handleIngest)
	})
}

// configValue 读运行时配置（未接入/键不存在返回空串）。
func (m *Module) configValue(ctx context.Context, key string) string {
	if m.Cfg == nil {
		return ""
	}
	v, err := m.Cfg.Get(ctx, key)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(v)
}

// ingestToken 对接密钥：字段优先，否则配置读取。
func (m *Module) ingestToken(ctx context.Context) string {
	if m.IngestToken != "" {
		return m.IngestToken
	}
	return m.configValue(ctx, "CHATLOG_INGEST_TOKEN")
}

// tokenEqual 对接密钥比对（先比长度再恒定时间比较，防时序侧信道，同 treehazard /ingest 口径）。
func tokenEqual(provided, expected string) bool {
	pb, eb := []byte(provided), []byte(expected)
	if len(pb) != len(eb) {
		return false
	}
	return subtle.ConstantTimeCompare(pb, eb) == 1
}

// checkIngestToken 校验 X-Ingest-Token：true 放行；false 已写出错误响应。
func (m *Module) checkIngestToken(w http.ResponseWriter, r *http.Request) bool {
	token := m.ingestToken(r.Context())
	if token == "" {
		writeIngest(w, http.StatusServiceUnavailable,
			ingestResponse{OK: false, Code: "ingest_token_missing", Message: "未配置对接密钥"})
		return false
	}
	if !tokenEqual(r.Header.Get("X-Ingest-Token"), token) {
		writeIngest(w, http.StatusUnauthorized,
			ingestResponse{OK: false, Code: "ingest_token_invalid", Message: "对接密钥错误"})
		return false
	}
	return true
}
