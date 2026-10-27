package callme

// Call Me 模块装配：/api/v1/callme 路由（会话 / 消息 / 附件 / 模型 / 能力 / SSE 问答）与
// 群内提问直推 /ingest-chat（免登录，凭 X-Ingest-Token，挂 auth 门控之前）。
// WeKnora 客户端与 Dify WF3 文件名生成器均可注入。
//
// 装配示例（主程序 main.go，cfg 为 *config.Store）：
//
//	cm := callme.New(db, cfg)
//	cm.WeKnora = callme.NewHTTPClient(cfg) // 连接参数（URL/API key/agent）运行时读取
//	cm.Namer = <Dify WF3 文件名生成器，可选，nil 回退会话标题>
//	cm.Permit = <应用权限实现，可选，nil 放行>
//	cm.RegisterRoutes(r) // 挂载在 /api/v1 之下；/ingest-chat 须在鉴权门控之前放行

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"shade/internal/auth"
	"shade/internal/httpapi"
)

// Module Call Me 模块。
type Module struct {
	DB *sql.DB
	// Cfg 运行时配置（CALLME_INGEST_TOKEN / WEKNORA_*）；nil 时 ingest 密钥视为未配置。
	Cfg ConfigReader
	// WeKnora WeKnora 客户端（可注入，生产装配 callme.NewHTTPClient(cfg) 运行时读配置；nil 时业务接口返回未配置）。
	WeKnora WeKnoraClient
	// Namer Dify WF3 文件名生成器（群回答 md 命名；nil 回退会话标题）。
	Namer FileNamer
	// Permit call-me 应用权限检查；nil 放行。
	Permit AppPermit
	// IngestToken 直推密钥（优先于 Cfg 读取；测试注入）。
	IngestToken string
}

// New 构造 Call Me 模块。
func New(db *sql.DB, cfg ConfigReader) *Module {
	return &Module{DB: db, Cfg: cfg, Permit: AllowAllPermit{}}
}

// ingestToken 对接密钥：字段优先，否则配置读取。
func (m *Module) ingestToken(ctx context.Context) string {
	if m.IngestToken != "" {
		return m.IngestToken
	}
	if m.Cfg == nil {
		return ""
	}
	v, err := m.Cfg.Get(ctx, "CALLME_INGEST_TOKEN")
	if err != nil {
		return ""
	}
	return v
}

// RegisterRoutes 注册 /api/v1/callme 路由（挂载在 /api/v1 之下）。
// /ingest-chat 在鉴权组之外（主程序需放行该路径免登录，同 treehazard /ingest 模式）。
func (m *Module) RegisterRoutes(r chi.Router) {
	r.Route("/callme", func(r chi.Router) {
		r.Post("/ingest-chat", m.handleIngestChat)

		r.Group(func(r chi.Router) {
			r.Use(m.withContext)
			r.Post("/sessions", m.handleCreateSession)
			r.Get("/sessions", m.handleListSessions)
			r.Get("/sessions/{id}", m.handleGetSession)
			r.Put("/sessions/{id}", m.handleUpdateSession)
			r.Delete("/sessions/{id}", m.handleDeleteSession)
			r.Delete("/sessions/{id}/messages/{msgId}", m.handleDeleteMessage)
			r.Post("/sessions/{id}/attachments", m.handleUploadAttachment)
			r.Get("/sessions/{id}/attachments", m.handleListAttachments)
			r.Get("/capabilities", m.handleCapabilities)
			r.Post("/chat", m.handleChat)
		})
	})
}

type moduleContextKey string

const identityContextKey moduleContextKey = "callme.identity"

// withContext 中间件：身份 → call-me 应用权限。
func (m *Module) withContext(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ident, okIdent := httpapi.IdentityFromContext(r.Context())
		if !okIdent {
			fail(w, http.StatusUnauthorized, codeUnauthorized, "未登录或登录已过期")
			return
		}
		if m.Permit != nil {
			allow, err := m.Permit.Allow(r.Context(), ident.ID, "call-me")
			if err != nil {
				fail(w, http.StatusInternalServerError, codeNotConfigured, "应用权限校验失败")
				return
			}
			if !allow {
				fail(w, http.StatusForbidden, codeNoApp, "暂无该应用的使用权限，请联系管理员开通")
				return
			}
		}
		ctx := context.WithValue(r.Context(), identityContextKey, ident)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func identityFrom(ctx context.Context) (auth.Identity, bool) {
	v, ok := ctx.Value(identityContextKey).(auth.Identity)
	return v, ok
}

// requireWeKnora 取客户端；未接入时写出错误并返回 false。
func (m *Module) requireWeKnora(w http.ResponseWriter) bool {
	if m.WeKnora == nil {
		fail(w, http.StatusServiceUnavailable, codeNotConfigured, "WeKnora 未配置（WEKNORA_API_KEY / WEKNORA_AGENT_ID）")
		return false
	}
	return true
}

// weknoraError 统一处理 WeKnora 调用错误（避免把内部细节抛给客户端）。
func weknoraError(w http.ResponseWriter, err error) bool {
	var wk *WkError
	if errors.As(err, &wk) {
		if wk.Expose {
			fail(w, http.StatusServiceUnavailable, codeNotConfigured, wk.Message)
			return true
		}
		switch wk.Status {
		case 401:
			fail(w, http.StatusBadGateway, codeUpstreamAuth, "WeKnora 认证失败，请检查 WEKNORA_API_KEY")
			return true
		case 404:
			fail(w, http.StatusNotFound, codeSessionNotFound, "会话不存在或已被删除")
			return true
		}
		if wk.Timeout {
			fail(w, http.StatusGatewayTimeout, codeUpstreamTimeout, "WeKnora 响应超时，请稍后再试")
			return true
		}
	}
	fail(w, http.StatusBadGateway, codeUpstreamAuth, "AI 服务暂时不可用，请稍后再试")
	return true
}

// ===== 会话 =====

// handleCreateSession POST /sessions：新建会话（标题留空时 WeKnora 首轮后自动命名）。
func (m *Module) handleCreateSession(w http.ResponseWriter, r *http.Request) {
	if !m.requireWeKnora(w) {
		return
	}
	ident, _ := identityFrom(r.Context())
	var body struct {
		Title string `json:"title"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	data, err := m.WeKnora.CreateSession(r.Context(), ident.Username, body.Title)
	if err != nil {
		weknoraError(w, err)
		return
	}
	ok(w, unwrapData(data), "")
}

// handleListSessions GET /sessions：会话列表（按用户隔离）。
func (m *Module) handleListSessions(w http.ResponseWriter, r *http.Request) {
	if !m.requireWeKnora(w) {
		return
	}
	ident, _ := identityFrom(r.Context())
	data, err := m.WeKnora.ListSessions(r.Context(), ident.Username)
	if err != nil {
		weknoraError(w, err)
		return
	}
	if _, has := data["data"]; has {
		ok(w, data["data"], "")
	} else {
		ok(w, data, "")
	}
}

// handleGetSession GET /sessions/{id}：会话详情 + 历史消息（升序），支持 limit / before_time。
func (m *Module) handleGetSession(w http.ResponseWriter, r *http.Request) {
	if !m.requireWeKnora(w) {
		return
	}
	ident, _ := identityFrom(r.Context())
	sessionID := chi.URLParam(r, "id")
	limit := 50
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			limit = min(max(n, 1), 200)
		}
	}
	beforeTime := r.URL.Query().Get("before_time")

	detail, err := m.WeKnora.GetSession(r.Context(), ident.Username, sessionID)
	if err != nil {
		weknoraError(w, err)
		return
	}
	messagesRaw, err := m.WeKnora.ListMessages(r.Context(), ident.Username, sessionID, limit, beforeTime)
	if err != nil {
		weknoraError(w, err)
		return
	}
	session := unwrapData(detail)
	list := unwrapList(messagesRaw)
	// 按创建时间升序；assistant 消息附带步骤树。
	var messages []map[string]any
	for _, msg := range list {
		messages = append(messages, msg)
	}
	sortByCreatedAt(messages)
	for _, msg := range messages {
		role := StringOf(msg, "role")
		if role != "assistant" && role != "ai" {
			continue
		}
		steps, okSteps := msg["agent_steps"].([]any)
		if !okSteps || len(steps) == 0 {
			continue
		}
		var stepMaps []map[string]any
		for _, s := range steps {
			if sm, ok := s.(map[string]any); ok {
				stepMaps = append(stepMaps, sm)
			}
		}
		built, meta := BuildSteps(stepMaps)
		metaMap := map[string]any{
			"toolCalls":  meta.ToolCalls,
			"durationMs": anyToInt64(msg["agent_duration_ms"]),
		}
		msg["steps"] = built
		msg["steps_meta"] = metaMap
	}
	out := map[string]any{}
	for k, v := range session {
		out[k] = v
	}
	out["messages"] = messages
	out["has_more"] = len(list) >= limit
	ok(w, out, "")
}

func sortByCreatedAt(list []map[string]any) {
	for i := 1; i < len(list); i++ {
		for j := i; j > 0 && StringOf(list[j], "created_at") < StringOf(list[j-1], "created_at"); j-- {
			list[j], list[j-1] = list[j-1], list[j]
		}
	}
}

// handleUpdateSession PUT /sessions/{id}：改名。
func (m *Module) handleUpdateSession(w http.ResponseWriter, r *http.Request) {
	if !m.requireWeKnora(w) {
		return
	}
	ident, _ := identityFrom(r.Context())
	var body struct {
		Title string `json:"title"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	title := strings.TrimSpace(body.Title)
	if title == "" {
		fail(w, http.StatusBadRequest, codeTitleEmpty, "会话名称不能为空")
		return
	}
	data, err := m.WeKnora.UpdateSession(r.Context(), ident.Username, chi.URLParam(r, "id"), title)
	if err != nil {
		weknoraError(w, err)
		return
	}
	ok(w, unwrapData(data), "已保存")
}

// handleDeleteSession DELETE /sessions/{id}：删除会话。
func (m *Module) handleDeleteSession(w http.ResponseWriter, r *http.Request) {
	if !m.requireWeKnora(w) {
		return
	}
	ident, _ := identityFrom(r.Context())
	if err := m.WeKnora.DeleteSession(r.Context(), ident.Username, chi.URLParam(r, "id")); err != nil {
		weknoraError(w, err)
		return
	}
	ok(w, nil, "已删除")
}

// handleDeleteMessage DELETE /sessions/{id}/messages/{msgId}：问答成对删除（同 request_id）。
func (m *Module) handleDeleteMessage(w http.ResponseWriter, r *http.Request) {
	if !m.requireWeKnora(w) {
		return
	}
	ident, _ := identityFrom(r.Context())
	sessionID := chi.URLParam(r, "id")
	msgID := chi.URLParam(r, "msgId")

	ids := []string{msgID}
	// 找出该消息所在问答对的全部消息 id（列表失败退化为仅删本条）。
	if data, err := m.WeKnora.ListMessages(r.Context(), ident.Username, sessionID, 0, ""); err == nil {
		list := unwrapList(data)
		var targetRID string
		for _, msg := range list {
			if StringOf(msg, "id") == msgID {
				targetRID = StringOf(msg, "request_id")
				break
			}
		}
		if targetRID != "" {
			seen := map[string]bool{msgID: true}
			for _, msg := range list {
				id := StringOf(msg, "id")
				if StringOf(msg, "request_id") == targetRID && id != "" && !seen[id] {
					seen[id] = true
					ids = append(ids, id)
				}
			}
		}
	}
	for _, id := range ids {
		if err := m.WeKnora.DeleteMessage(r.Context(), ident.Username, sessionID, id); err != nil {
			weknoraError(w, err)
			return
		}
	}
	ok(w, map[string]any{"deleted": len(ids)}, "已删除")
}

// ===== 附件 =====

// handleUploadAttachment POST /sessions/{id}/attachments：上传会话附件（multipart 字段 file，单文件 ≤50MB）。
func (m *Module) handleUploadAttachment(w http.ResponseWriter, r *http.Request) {
	if !m.requireWeKnora(w) {
		return
	}
	ident, _ := identityFrom(r.Context())
	if err := r.ParseMultipartForm(51 << 20); err != nil {
		fail(w, http.StatusBadRequest, codeAttachSize, "附件大小应在 50MB 以内")
		return
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		fail(w, http.StatusBadRequest, codeAttachMissing, "请选择要上传的附件")
		return
	}
	defer file.Close()
	buf, err := readAllLimit(file, 50*1024*1024+1)
	if err != nil || len(buf) > 50*1024*1024 {
		fail(w, http.StatusBadRequest, codeAttachSize, "附件大小应在 50MB 以内")
		return
	}
	filename := fixLatin1Name(header.Filename)
	ext := fileExt(filename)
	if !attachmentExts[ext] {
		fail(w, http.StatusBadRequest, codeAttachType, "不支持的附件格式："+firstNonEmpty(ext, "无扩展名"))
		return
	}
	mimetype := header.Header.Get("Content-Type")
	if mimetype == "" {
		mimetype = "application/octet-stream"
	}
	doc, err := m.WeKnora.UploadAttachment(r.Context(), ident.Username, chi.URLParam(r, "id"), filename, mimetype, buf)
	if err != nil {
		weknoraError(w, err)
		return
	}
	ok(w, map[string]any{"id": doc.ID, "name": doc.FileName, "size": doc.FileSize, "status": doc.Status}, "")
}

// handleListAttachments GET /sessions/{id}/attachments：附件列表。
func (m *Module) handleListAttachments(w http.ResponseWriter, r *http.Request) {
	if !m.requireWeKnora(w) {
		return
	}
	ident, _ := identityFrom(r.Context())
	list, err := m.WeKnora.ListAttachments(r.Context(), ident.Username, chi.URLParam(r, "id"))
	if err != nil {
		weknoraError(w, err)
		return
	}
	out := make([]map[string]any, 0, len(list))
	for _, d := range list {
		out = append(out, map[string]any{
			"id": d.ID, "name": d.FileName, "size": d.FileSize,
			"status": d.Status, "error_message": d.Error,
		})
	}
	ok(w, out, "")
}

// handleCapabilities GET /capabilities：模型列表 + 联网搜索能力（探测失败降级空能力）。
func (m *Module) handleCapabilities(w http.ResponseWriter, r *http.Request) {
	capabilities := map[string]any{"models": []any{}, "web_search": false}
	if m.WeKnora != nil {
		ident, _ := identityFrom(r.Context())
		models, errM := m.WeKnora.ListModels(r.Context(), ident.Username)
		if errM == nil {
			var out []map[string]any
			for _, mm := range KnowledgeQAModels(models) {
				out = append(out, map[string]any{
					"id":              mm.ID,
					"name":            mm.NameOf(),
					"supports_vision": mm.SupportsVision(),
					"thinking_levels": mm.ThinkingLevels(),
				})
			}
			capabilities["models"] = out
		}
		agent, errA := m.WeKnora.GetAgent(r.Context(), ident.Username)
		providers, errP := m.WeKnora.ListWebSearchProviders(r.Context(), ident.Username)
		if errA == nil {
			agentData := unwrapData(agent)
			cfg, _ := agentData["config"].(map[string]any)
			if cfg == nil {
				cfg = map[string]any{}
			}
			agentWeb := anyToBool(agentData["web_search_enabled"]) || anyToBool(cfg["web_search_enabled"])
			providerID := firstNonEmpty(StringOf(agentData, "web_search_provider_id"), StringOf(cfg, "web_search_provider_id"))
			hasDefault := false
			if errP == nil {
				for _, p := range providers {
					if anyToBool(p["is_default"]) {
						hasDefault = true
					}
				}
			}
			capabilities["web_search"] = agentWeb && (providerID != "" || hasDefault)
		}
	}
	ok(w, capabilities, "")
}

// ===== SSE 问答 =====

// handleChat POST /chat：发送消息（SSE 流式转发归一化事件）。
func (m *Module) handleChat(w http.ResponseWriter, r *http.Request) {
	ident, okIdent := identityFrom(r.Context())
	if !okIdent {
		fail(w, http.StatusUnauthorized, codeUnauthorized, "未登录或登录已过期")
		return
	}
	var body struct {
		SessionID       string   `json:"session_id"`
		Query           string   `json:"query"`
		Images          []Image  `json:"images"`
		AttachmentIDs   []string `json:"attachment_ids"`
		WebSearch       *bool    `json:"web_search"`
		ModelID         *string  `json:"model_id"`
		ReasoningEffort *string  `json:"reasoning_effort"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		fail(w, http.StatusBadRequest, codeParam, "缺少 session_id 或 query")
		return
	}
	if body.SessionID == "" || body.Query == "" {
		fail(w, http.StatusBadRequest, codeParam, "缺少 session_id 或 query")
		return
	}
	if body.AttachmentIDs != nil {
		if len(body.AttachmentIDs) > 5 {
			fail(w, http.StatusBadRequest, codeAttachIDs, "attachment_ids 参数无效（最多 5 个附件）")
			return
		}
		for _, id := range body.AttachmentIDs {
			if strings.TrimSpace(id) == "" {
				fail(w, http.StatusBadRequest, codeAttachIDs, "attachment_ids 参数无效（最多 5 个附件）")
				return
			}
		}
	}
	modelID := ""
	if body.ModelID != nil {
		modelID = strings.TrimSpace(*body.ModelID)
	}
	if body.ReasoningEffort != nil && !validReasoningEffort(*body.ReasoningEffort) {
		fail(w, http.StatusBadRequest, codeReasoningEffort, "reasoning_effort 参数无效")
		return
	}
	if body.Images != nil {
		if len(body.Images) > 1 {
			fail(w, http.StatusBadRequest, codeImageCount, "图片最多 1 张")
			return
		}
		if len(body.Images) == 1 {
			switch checkImageDataUrl(body.Images[0].Data) {
			case "format":
				fail(w, http.StatusBadRequest, codeImageFormat, "图片格式应为 jpeg/png/gif/webp（base64 dataURL）")
				return
			case "size":
				fail(w, http.StatusBadRequest, codeImageSize, "图片大小应在 8MB 以内")
				return
			}
		}
	}
	if m.WeKnora == nil {
		fail(w, http.StatusServiceUnavailable, codeNotConfigured, "WeKnora 未配置（WEKNORA_API_KEY / WEKNORA_AGENT_ID）")
		return
	}

	// SSE 响应头（X-Accel-Buffering 关闭 Nginx 缓冲）。
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache, no-transform")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	flusher, _ := w.(http.Flusher)
	if flusher != nil {
		flusher.Flush()
	}
	send := func(ev ChatEvent) {
		raw, _ := json.Marshal(ev)
		_, _ = w.Write([]byte("data: " + string(raw) + "\n\n"))
		if flusher != nil {
			flusher.Flush()
		}
	}
	// 心跳：防代理长生成断连（客户端忽略非 data 行）。
	done := make(chan struct{})
	defer close(done)
	go func() {
		t := newTicker(15)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-t.C:
				_, _ = w.Write([]byte(": ping\n\n"))
				if flusher != nil {
					flusher.Flush()
				}
			}
		}
	}()
	// 客户端断连 → ctx 取消销毁上游。
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	chatReq := ChatRequest{
		Query: body.Query, Images: body.Images,
		AttachmentIDs: body.AttachmentIDs, ModelID: modelID,
	}
	if body.WebSearch != nil {
		chatReq.WebSearch = *body.WebSearch
	} else {
		chatReq.WebSearch = true
	}
	if body.ReasoningEffort != nil {
		chatReq.ReasoningEffort = *body.ReasoningEffort
	}
	_, err := CollectAnswer(ctx, m.WeKnora, ident.Username, body.SessionID, chatReq, send)
	if err != nil {
		if errors.Is(err, ErrEmitted) || errors.Is(err, ErrClientGone) {
			return
		}
		var wk *WkError
		switch {
		case errors.As(err, &wk) && wk.Expose:
			send(ChatEvent{Type: "error", Content: wk.Message})
		case errors.As(err, &wk) && wk.Status == 401:
			send(ChatEvent{Type: "error", Content: "WeKnora 认证失败，请检查 WEKNORA_API_KEY"})
		case errors.As(err, &wk) && wk.Status == 404:
			send(ChatEvent{Type: "error", Content: "会话不存在或已被删除"})
		default:
			send(ChatEvent{Type: "error", Content: "AI 服务暂时不可用，请稍后再试"})
		}
	}
}

// reasoningEfforts 思考强度固定档位。
var reasoningEfforts = map[string]bool{
	"off": true, "auto": true, "minimal": true, "low": true,
	"medium": true, "high": true, "xhigh": true, "max": true,
}

func validReasoningEffort(s string) bool { return reasoningEfforts[s] }

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
