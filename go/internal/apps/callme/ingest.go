package callme

// POST /api/v1/callme/ingest-chat 群内提问直推（crane-answer 用，凭 X-Ingest-Token 免登录）。
// 契约（与 crane-answer 侧约定）：body { wxid, query, session_title, web_search?, image?, file? }；
// 成功 { ok:true, data:{ session_id, session_title, answer, model_used, file_name? } }；
// 失败 { ok:false, code, message, models? }，code：user_not_found / no_permission / model_not_found /
// unsupported_file / file_too_large / param_invalid / upstream_error / ingest_token_missing /
// ingest_token_invalid（业务失败统一 HTTP 200 + ok:false；对接密钥未配置 503、密钥不等 401）。
// wxid 唯一启用账号 + call-me 权限校验；Dify WF3 文件名生成器可注入（回答 md 命名）。

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"regexp"
	"strings"
)

// Ingest 响应契约（与统一信封不同）。
type ingestResponse struct {
	OK   bool           `json:"ok"`
	Code string         `json:"code,omitempty"`
	Data map[string]any `json:"data,omitempty"`
	// Models model_not_found 时附带可选模型。
	Models  []map[string]any `json:"models,omitempty"`
	Message string           `json:"message,omitempty"`
}

func writeIngest(w http.ResponseWriter, status int, resp ingestResponse) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(resp)
}

func ingestFail(w http.ResponseWriter, code, message string) {
	writeIngest(w, http.StatusOK, ingestResponse{OK: false, Code: code, Message: message})
}

func ingestFailModels(w http.ResponseWriter, code, message string, models []map[string]any) {
	writeIngest(w, http.StatusOK, ingestResponse{OK: false, Code: code, Message: message, Models: models})
}

// FileNamer Dify WF3 文件名生成器（群回答 md 命名，可注入；nil 时回退 session_title）。
type FileNamer interface {
	// NameFile 生成回答 md 文件名（不含扩展名或含 .md 皆可）。
	NameFile(ctx context.Context, username, sessionTitle, query, answer string) string
}

// imageDataURLRe 图片 dataURL 校验：jpeg/png/gif/webp base64、解码后 ≤8MB。
var imageDataURLRe = regexp.MustCompile(`^data:image/(jpeg|png|gif|webp);base64,([A-Za-z0-9+/=\s]*)$`)

// checkImageDataUrl 通过返回 ""，否则 'format' / 'size'。
func checkImageDataUrl(dataURL string) string {
	m := imageDataURLRe.FindStringSubmatch(dataURL)
	if m == nil {
		return "format"
	}
	raw, err := base64.StdEncoding.DecodeString(strings.Join(strings.Fields(m[2]), ""))
	if err != nil {
		return "format"
	}
	if len(raw) > 8*1024*1024 {
		return "size"
	}
	return ""
}

// attachmentExts 附件扩展名白名单（不含点、小写）。
var attachmentExts = map[string]bool{
	"pdf": true, "docx": true, "doc": true, "pptx": true, "ppt": true,
	"xlsx": true, "xls": true, "epub": true, "mhtml": true, "csv": true,
	"md": true, "markdown": true, "txt": true, "json": true, "html": true,
	"mp3": true, "wav": true, "m4a": true, "flac": true, "ogg": true, "aac": true,
	"bmp": true, "tiff": true, "tif": true, "mp4": true,
}

// fileExt 扩展名提取（不含点、小写）。
func fileExt(name string) string {
	i := strings.LastIndexByte(name, '.')
	if i < 0 || i == len(name)-1 {
		return ""
	}
	return strings.ToLower(name[i+1:])
}

// handleIngestChat 群内提问直推（薄适配：密钥校验 + 编解码，业务核 ingestChatCore 与直调共用）。
func (m *Module) handleIngestChat(w http.ResponseWriter, r *http.Request) {
	// 对接密钥：未配置 503、不等 401。
	token := m.ingestToken(r.Context())
	if token == "" {
		writeIngest(w, http.StatusServiceUnavailable,
			ingestResponse{OK: false, Code: "ingest_token_missing", Message: "未配置对接密钥"})
		return
	}
	if subtle.ConstantTimeCompare([]byte(r.Header.Get("X-Ingest-Token")), []byte(token)) != 1 {
		writeIngest(w, http.StatusUnauthorized,
			ingestResponse{OK: false, Code: "ingest_token_invalid", Message: "对接密钥错误"})
		return
	}
	rawBody, _ := io.ReadAll(io.LimitReader(r.Body, 120<<20))
	var req IngestChatRequest
	if err := json.Unmarshal(rawBody, &req); err != nil {
		ingestFail(w, "param_invalid", "wxid / query / session_title 均为必填")
		return
	}
	res, ierr := m.ingestChatCore(r.Context(), req)
	if ierr != nil {
		if len(ierr.Models) > 0 {
			ingestFailModels(w, ierr.Code, ierr.Message, ierr.Models)
			return
		}
		ingestFail(w, ierr.Code, ierr.Message)
		return
	}
	writeIngest(w, http.StatusOK, ingestResponse{OK: true, Data: map[string]any{
		"session_id":    res.SessionID,
		"session_title": res.SessionTitle,
		"answer":        res.Answer,
		"model_used":    res.ModelUsed,
		"file_name":     res.FileName,
	}})
}

var base64Re = regexp.MustCompile(`^[A-Za-z0-9+/=\s]*$`)

var errWxidAmbiguous = errors.New("wxid 绑定了多个启用账号")

func hasControlChars(s string) bool {
	for _, r := range s {
		if r < 0x20 {
			return true
		}
	}
	return false
}

// userByWxid wxid → 唯一启用账号（0 命中 → sql.ErrNoRows；多绑 → errWxidAmbiguous）。
func (m *Module) userByWxid(ctx context.Context, wxid string) (int64, string, error) {
	rows, err := m.DB.QueryContext(ctx,
		`SELECT id, username FROM sys_user WHERE wxid = ? AND status = 1`, wxid)
	if err != nil {
		return 0, "", err
	}
	defer rows.Close()
	var ids []int64
	var names []string
	for rows.Next() {
		var id int64
		var name string
		if rows.Scan(&id, &name) == nil {
			ids = append(ids, id)
			names = append(names, name)
		}
	}
	switch len(ids) {
	case 0:
		return 0, "", sql.ErrNoRows
	case 1:
		return ids[0], names[0], nil
	default:
		return 0, "", errWxidAmbiguous
	}
}

// ensureSession 会话按名找或建（同名多个取 updated_at 最新）。
func (m *Module) ensureSession(ctx context.Context, username, title string) (string, error) {
	resp, err := m.WeKnora.ListSessions(ctx, username)
	if err != nil {
		return "", err
	}
	var bestID string
	var bestUpdated string
	for _, s := range unwrapList(resp) {
		if StringOf(s, "title") != title {
			continue
		}
		upd := StringOf(s, "updated_at")
		if bestID == "" || upd > bestUpdated {
			bestID, bestUpdated = StringOf(s, "id"), upd
		}
	}
	if bestID != "" {
		return bestID, nil
	}
	created, err := m.WeKnora.CreateSession(ctx, username, title)
	if err != nil {
		return "", err
	}
	id := StringOf(unwrapData(created), "id")
	if id == "" {
		return "", errors.New("会话创建失败")
	}
	return id, nil
}

// weknoraUserMessage 上游错误 → 面向用户的中文提示。
func weknoraUserMessage(err error) string {
	var wk *WkError
	if errors.As(err, &wk) {
		if wk.Expose && wk.Message != "" {
			return wk.Message
		}
	}
	return "AI 服务暂时不可用，请稍后再试"
}
