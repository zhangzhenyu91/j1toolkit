package callme

// 群内提问进程内直调入口（wechatanswer 经 bridge 复用 /ingest-chat 同一套业务，不走 HTTP 自循环）：
// 校验对接密钥 → wxid 唯一启用账号 + call-me 权限 → 会话按名找或建 → WeKnora 收流 → WF3 命名。
// 业务核 ingestChatCore 与 handleIngestChat 共用（direct.go 为唯一实现，HTTP 入口为薄适配）。
// 直调仍校验 CALLME_INGEST_TOKEN（未配置 ingest_token_missing / 不等 ingest_token_invalid，
// 与 HTTP 入口同 fail-closed，不因进程内调用免密钥）。

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"errors"
	"strings"
	"unicode/utf8"
)

// IngestChatFile 提问附件（/ingest-chat file 字段同形）。
type IngestChatFile struct {
	Name       string `json:"name"`
	DataBase64 string `json:"data_base64"`
}

// IngestChatRequest 群内提问载荷（/ingest-chat 请求体同形；WebSearch 缺省 true）。
type IngestChatRequest struct {
	Wxid         string          `json:"wxid"`
	Query        string          `json:"query"`
	SessionTitle string          `json:"session_title"`
	WebSearch    *bool           `json:"web_search"`
	Image        string          `json:"image"`
	File         *IngestChatFile `json:"file"`
}

// IngestChatResult 直调成功结果（/ingest-chat data 同形）。
type IngestChatResult struct {
	SessionID    string
	SessionTitle string
	Answer       string
	ModelUsed    map[string]any
	FileName     string
}

// IngestChatError 直调失败（code/message/models 与 /ingest-chat 失败信封同语义；
// Status 为对接密钥类失败的 HTTP 同口径状态 503/401，业务失败记 200）。
type IngestChatError struct {
	Status  int
	Code    string
	Message string
	Models  []map[string]any
}

func (e *IngestChatError) Error() string {
	return "callme ingest 失败：" + e.Code + " " + e.Message
}

// IngestChat 群内提问直调：token 必须与 CALLME_INGEST_TOKEN（或 IngestToken 字段）一致，
// 校验通过后走与 /ingest-chat 完全相同的业务核。
func (m *Module) IngestChat(ctx context.Context, token string, req IngestChatRequest) (IngestChatResult, error) {
	expected := m.ingestToken(ctx)
	if expected == "" {
		return IngestChatResult{}, &IngestChatError{Status: 503, Code: "ingest_token_missing", Message: "未配置对接密钥"}
	}
	if subtle.ConstantTimeCompare([]byte(token), []byte(expected)) != 1 {
		return IngestChatResult{}, &IngestChatError{Status: 401, Code: "ingest_token_invalid", Message: "对接密钥错误"}
	}
	res, ierr := m.ingestChatCore(ctx, req)
	if ierr != nil {
		return IngestChatResult{}, ierr
	}
	return res, nil
}

// ingestChatCore 群内提问业务核（HTTP 与直调共用；失败以 *IngestChatError 表达，不抛其他错误）。
// 流程与原 handler 一致：参数校验 → 图片/文件校验 → wxid 唯一启用账号 → 权限 → 会话 →
// 模型识别 → 附件预上传 → WeKnora 收流 → WF3 命名（未接 Namer 回退会话标题）。
func (m *Module) ingestChatCore(ctx context.Context, body IngestChatRequest) (IngestChatResult, *IngestChatError) {
	fail := func(code, message string) (IngestChatResult, *IngestChatError) {
		return IngestChatResult{}, &IngestChatError{Status: 200, Code: code, Message: message}
	}
	wxid := strings.TrimSpace(body.Wxid)
	query := strings.TrimSpace(body.Query)
	sessionTitle := strings.TrimSpace(body.SessionTitle)
	if wxid == "" || query == "" || sessionTitle == "" {
		return fail("param_invalid", "wxid / query / session_title 均为必填")
	}

	// 群内提问内部契约：≤1 张、jpeg/png/gif/webp dataURL、解码后 ≤50MB。
	var images []Image
	if body.Image != "" {
		switch checkImageDataUrl(body.Image) {
		case "format":
			return fail("unsupported_file", "图片格式应为 jpeg/png/gif/webp（base64 dataURL）")
		case "size":
			return fail("file_too_large", "图片大小应在 50MB 以内")
		}
		images = []Image{{Data: body.Image}}
	}

	// 文件：{name, data_base64} 单个 ≤50MB，扩展名白名单。
	var attachName string
	var attachBuf []byte
	if body.File != nil {
		name := body.File.Name
		b64 := body.File.DataBase64
		nameOK := name != "" && utf8.ValidString(name) && !hasControlChars(name)
		b64OK := b64 != "" && base64Re.MatchString(b64)
		if !nameOK || !b64OK {
			return fail("unsupported_file", "附件名称或内容无效")
		}
		if base64EncodedTooLarge(b64, maxIngestImageBytes) {
			return fail("file_too_large", "附件大小应在 50MB 以内")
		}
		buf, err := base64.StdEncoding.DecodeString(strings.Join(strings.Fields(b64), ""))
		if err != nil || len(buf) == 0 {
			return fail("unsupported_file", "附件内容为空")
		}
		if len(buf) > maxIngestImageBytes {
			return fail("file_too_large", "附件大小应在 50MB 以内")
		}
		ext := fileExt(name)
		if !attachmentExts[ext] {
			return fail("unsupported_file", "不支持的附件格式："+firstNonEmpty(ext, "无扩展名"))
		}
		attachName, attachBuf = name, buf
	}

	// wxid → 壹匣账号（0 命中或多绑 → user_not_found；仅启用账号唯一）。
	userID, username, err := m.userByWxid(ctx, wxid)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fail("user_not_found", "该 wxid 未绑定壹匣账号")
		}
		if errors.Is(err, errWxidAmbiguous) {
			return fail("user_not_found", "该 wxid 绑定了多个壹匣账号")
		}
		return fail("upstream_error", "AI 服务暂时不可用，请稍后再试")
	}
	// Call Me 应用权限（与 requireApp 同一 SQL 口径）。
	if m.Permit != nil {
		allow, err := m.Permit.Allow(ctx, userID, "call-me")
		if err != nil {
			return fail("upstream_error", "AI 服务暂时不可用，请稍后再试")
		}
		if !allow {
			return fail("no_permission", "该用户未开通 Call Me 权限")
		}
	}

	// 会话按名找或建（同名多个取 updated_at 最新；创建显式传 title）。
	sessionID, err := m.ensureSession(ctx, username, sessionTitle)
	if err != nil {
		return fail("upstream_error", weknoraUserMessage(err))
	}

	// 模型识别：抽到候选才查模型表；未抽到默认不传。
	var modelUsed map[string]any
	chatReq := ChatRequest{Query: query, Images: images, WebSearch: true}
	if body.WebSearch != nil {
		chatReq.WebSearch = *body.WebSearch
	}
	if hint := ExtractModelHint(query); hint != "" {
		models, err := m.WeKnora.ListModels(ctx, username)
		if err != nil {
			return fail("upstream_error", weknoraUserMessage(err))
		}
		models = KnowledgeQAModels(models)
		hit := MatchModelByHint(hint, models)
		if hit == nil {
			var opts []map[string]any
			for _, mm := range models {
				opts = append(opts, map[string]any{"id": mm.ID, "name": mm.NameOf()})
			}
			return IngestChatResult{}, &IngestChatError{
				Status: 200, Code: "model_not_found", Message: "未识别到可用模型", Models: opts,
			}
		}
		chatReq.ModelID = hit.ID
		chatReq.ReasoningEffort = hit.ReasoningEffort
		modelUsed = map[string]any{"id": hit.ID, "name": hit.Name}
	}

	// 有文件先预上传取附件 id，随对话发送。
	if len(attachBuf) > 0 {
		doc, err := m.WeKnora.UploadAttachment(ctx, username, sessionID, attachName, "application/octet-stream", attachBuf)
		if err != nil || doc.ID == "" {
			return fail("upstream_error", "附件上传失败")
		}
		chatReq.AttachmentIDs = []string{doc.ID}
	}

	res, err := CollectAnswer(ctx, m.WeKnora, username, sessionID, chatReq, nil)
	if err != nil {
		return fail("upstream_error", weknoraUserMessage(err))
	}
	// Dify WF3 文件名（可注入）：回答 md 命名；未接入回退会话标题。
	fileName := sessionTitle
	if m.Namer != nil {
		if n := strings.TrimSpace(m.Namer.NameFile(ctx, username, sessionTitle, query, res.Answer)); n != "" {
			fileName = n
		}
	}
	return IngestChatResult{
		SessionID:    sessionID,
		SessionTitle: sessionTitle,
		Answer:       res.Answer,
		ModelUsed:    modelUsed,
		FileName:     fileName,
	}, nil
}
