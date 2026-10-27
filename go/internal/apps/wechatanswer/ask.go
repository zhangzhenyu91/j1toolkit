// 群内提问 WF3（移植 crane-answer src/ask/groupAsk.js）：
// 群成员 @监听账号 且非「报送/汇总」指令时，以该成员 wxid 走 Call Me 提问，
// 回答经 Dify WF3 命名后写 md 文件发回群。调用 callme ingest 逻辑（AskIngest 接口由 bridge
// 接 callme 模块，直调不走 HTTP 自循环）；未启用（开关关闭或命名 Key 未配置）静默忽略。
// 提问六形态：纯文字 / 引用文字 / 引用图片 / 引用文件 / 引用视频 / 引用语音。
package wechatanswer

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"
)

// askTimeout 单次提问超时（Call Me 含模型思考）；非幂等，不重试。
const askTimeout = 320 * time.Second

// askNameInputLen WF3 的 answer 输入截取长度。
const askNameInputLen = 4000

// askFallbackLen 文本兜底的回答截取长度。
const askFallbackLen = 500

// askFileNameMax 文件名截断长度。
const askFileNameMax = 40

// AskFile 提问/归档附件。
type AskFile struct {
	Name       string `json:"name"`
	DataBase64 string `json:"data_base64"`
}

// AskRequest 群内提问载荷（callme ingest-chat 契约）。
type AskRequest struct {
	Wxid         string   `json:"wxid"`
	Query        string   `json:"query"`
	SessionTitle string   `json:"session_title"`
	WebSearch    bool     `json:"web_search"`
	Image        string   `json:"image,omitempty"`
	File         *AskFile `json:"file,omitempty"`
}

// AskError 壹匣业务失败（code 语义与 callme 响应对齐，用于群内回执文案）。
type AskError struct {
	Code    string
	Message string
	Models  []string
}

func (e *AskError) Error() string {
	return "callme ingest 失败：" + e.Code + " " + e.Message
}

// AskIngest 群内提问出口：bridge 直调 callme ingest 逻辑（禁止 HTTP 自循环）。
// 返回回答正文；业务失败以 *AskError 表达。
type AskIngest interface {
	IngestChat(ctx context.Context, req AskRequest) (string, error)
}

// FileSender 群内文件发送（回答 md 发回群；Sender 未实现时回退文本摘要）。
type FileSender interface {
	SendFile(ctx context.Context, groupID string, data []byte, fileName string) error
}

// FileDownloader 媒体本体下载（文件/视频/语音；语音需已转码可发送格式）。
type FileDownloader interface {
	// DownloadMedia 按缓存参数下载本体；ext 为建议扩展名（mp4/mp3/wav/pdf…）。
	DownloadMedia(ctx context.Context, e MediaEntry) (data []byte, ext string, err error)
}

// isAskEnabled 群内提问启用条件：开关打开且 WF3 命名 Key 已配置。
func isAskEnabled(rule Rule) bool {
	return rule.AskEnabled && strings.TrimSpace(rule.AskNameKey) != ""
}

// sanitizeFileName 文件名净化：去 \/:*?"<>| 与换行，截 40 字。
func sanitizeFileName(name string) string {
	repl := strings.NewReplacer(`/`, ``, `\`, ``, `:`, ``, `*`, ``, `?`, ``, `"`, ``, `<`, ``, `>`, ``, `|`, ``, "\r", ``, "\n", ``)
	return truncateRunes(strings.TrimSpace(repl.Replace(name)), askFileNameMax)
}

// truncateRunes 按 rune 截断。
func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}

// askStamp 时间戳文件名后缀 yyyyMMdd-HHmm。
func (m *Module) askStamp() string {
	return m.now().Format("20060102-1504")
}

// mapAskError 壹匣失败 → 群内回执文案。
func mapAskError(err error) string {
	var ae *AskError
	if !errors.As(err, &ae) {
		return ""
	}
	switch ae.Code {
	case "user_not_found":
		return "你的微信尚未绑定壹匣账号，请联系超管录入"
	case "no_permission":
		return "你暂无 Call Me 使用权限"
	case "model_not_found":
		if len(ae.Models) > 0 {
			return "未添加该模型，当前可调用模型：\n" + strings.Join(ae.Models, "\n")
		}
		return "未添加该模型"
	case "unsupported_file":
		return "该文件类型暂不支持"
	case "file_too_large":
		return "文件超过 50MB 上限"
	}
	if ae.Message != "" {
		return "提问失败：" + ae.Message
	}
	return ""
}

// detectQuoteKind 引用消息被引用内容形态：text / image / file / video / voice / other。
func (m *Module) detectQuoteKind(ctx context.Context, quote *Quote, content string) string {
	if quote == nil {
		return "text"
	}
	if quote.HasMsgType && quote.MsgType == 3 {
		return "image"
	}
	if quote.SvrID != "" {
		if img, _ := m.recordStore().Image(ctx, quote.SvrID); img != nil {
			return "image"
		}
	}
	xml := strings.Join([]string{quote.Content, content}, "\n")
	if (quote.HasMsgType && quote.MsgType == 6) || mediaFileRe.MatchString(xml) {
		return "file"
	}
	if (quote.HasMsgType && quote.MsgType == 43) || mediaVideoRe.MatchString(xml) {
		return "video"
	}
	if (quote.HasMsgType && quote.MsgType == 34) || mediaVoiceRe.MatchString(xml) {
		return "voice"
	}
	if (quote.HasMsgType && quote.MsgType == 1) || !quote.HasMsgType {
		return "text"
	}
	return "other"
}

// askPayload 提问装配结果。
type askPayload struct {
	Query string
	Image string // dataURL（图片形态）
	File  *AskFile
	Err   string // 非空 → 回执该文案
}

// buildAskPayload 按六形态装配提问载荷；null=空提问静默忽略。
func (m *Module) buildAskPayload(ctx context.Context, quote *Quote, content, commandText, senderID string) *askPayload {
	cmd := strings.TrimSpace(commandText)
	if quote == nil {
		if cmd == "" {
			return nil
		}
		return &askPayload{Query: cmd}
	}
	kind := m.detectQuoteKind(ctx, quote, content)
	switch kind {
	case "text":
		quotedText := strings.TrimSpace(quote.Content)
		if quotedText == "" {
			quotedText = strings.TrimSpace(quote.Text)
		}
		query := cmd
		if query != "" && quotedText != "" {
			query = cmd + "\n\n" + quotedText
		} else if query == "" {
			query = quotedText
		}
		if query == "" {
			return nil
		}
		return &askPayload{Query: query}
	case "image":
		img, _ := m.recordStore().Image(ctx, quote.SvrID)
		if img == nil {
			return &askPayload{Err: "图片已过期，请重新发送后引用"}
		}
		buf, err := m.downloadImageBytes(ctx, img)
		if err != nil {
			return &askPayload{Err: "图片已过期，请重新发送后引用"}
		}
		mime := "image/jpeg"
		if strings.HasSuffix(strings.ToLower(img.File), ".png") {
			mime = "image/png"
		}
		q := cmd
		if q == "" {
			q = "请回答图片中的问题"
		}
		return &askPayload{Query: q, Image: "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(buf)}
	case "file", "video", "voice":
		return m.buildAskMediaPayload(ctx, kind, quote, content, cmd, senderID)
	}
	return &askPayload{Err: "暂支持文字、图片、文件、视频、语音提问"}
}

// buildAskMediaPayload 文件/视频/语音形态装配（下载参数来自缓存或被引用 XML）。
func (m *Module) buildAskMediaPayload(ctx context.Context, kind string, quote *Quote, content, cmd, senderID string) *askPayload {
	xml := strings.Join([]string{quote.Content, content}, "\n")
	parsed := ParseMediaParams(xml)
	cached := m.cachedMedia(ctx, quote.SvrID)
	merge := func(get func(*MediaEntry) string) string {
		if parsed != nil {
			if v := get(parsed); v != "" {
				return v
			}
		}
		if cached != nil {
			return get(cached)
		}
		return ""
	}
	if m.Files == nil {
		return &askPayload{Err: "媒体下载未接入"}
	}
	entry := &MediaEntry{
		Kind:         kind,
		FileName:     merge(func(e *MediaEntry) string { return e.FileName }),
		AppID:        merge(func(e *MediaEntry) string { return e.AppID }),
		AttachID:     merge(func(e *MediaEntry) string { return e.AttachID }),
		DataLen:      0,
		MsgID:        merge(func(e *MediaEntry) string { return e.MsgID }),
		NewMsgID:     merge(func(e *MediaEntry) string { return e.NewMsgID }),
		MasterBufID:  merge(func(e *MediaEntry) string { return e.MasterBufID }),
		FromUserName: firstNonEmpty(merge(func(e *MediaEntry) string { return e.FromUserName }), senderID),
	}
	num := func(get func(*MediaEntry) int64) int64 {
		if parsed != nil {
			if v := get(parsed); v > 0 {
				return v
			}
		}
		if cached != nil {
			return get(cached)
		}
		return 0
	}
	entry.DataLen = num(func(e *MediaEntry) int64 { return e.DataLen })
	entry.Length = num(func(e *MediaEntry) int64 { return e.Length })
	if entry.MsgID == "" {
		entry.MsgID = quote.SvrID
	}
	if entry.NewMsgID == "" {
		entry.NewMsgID = quote.SvrID
	}
	switch kind {
	case "file":
		if entry.FileName == "" || entry.AttachID == "" || entry.DataLen <= 0 {
			return &askPayload{Err: "文件信息解析失败，请重新发送文件后引用"}
		}
		buf, _, err := m.Files.DownloadMedia(ctx, *entry)
		if err != nil {
			return &askPayload{Err: "提问失败：" + err.Error()}
		}
		q := cmd
		if q == "" {
			q = "请分析这个文件"
		}
		name := sanitizeFileName(entry.FileName)
		if name == "" {
			name = "文件"
		}
		return &askPayload{Query: q, File: &AskFile{Name: name, DataBase64: base64.StdEncoding.EncodeToString(buf)}}
	case "video":
		if entry.MsgID == "" || entry.DataLen <= 0 {
			return &askPayload{Err: "文件信息解析失败，请重新发送文件后引用"}
		}
		buf, ext, err := m.Files.DownloadMedia(ctx, *entry)
		if err != nil {
			return &askPayload{Err: "提问失败：" + err.Error()}
		}
		if ext == "" {
			ext = "mp4"
		}
		q := cmd
		if q == "" {
			q = "请分析这段视频"
		}
		return &askPayload{Query: q, File: &AskFile{Name: fmt.Sprintf("视频-%s.%s", m.askStamp(), ext), DataBase64: base64.StdEncoding.EncodeToString(buf)}}
	case "voice":
		if (entry.MsgID == "" && entry.NewMsgID == "") || entry.MasterBufID == "" || entry.Length <= 0 {
			return &askPayload{Err: "语音解析失败，请改用文字提问"}
		}
		buf, ext, err := m.Files.DownloadMedia(ctx, *entry)
		if err != nil {
			return &askPayload{Err: "语音解析失败，请改用文字提问"}
		}
		if ext == "" {
			ext = "wav"
		}
		q := cmd
		if q == "" {
			q = "请回答语音中的问题"
		} else {
			q = cmd + "\n请回答语音中的问题"
		}
		return &askPayload{Query: q, File: &AskFile{Name: fmt.Sprintf("语音-%s.%s", m.askStamp(), ext), DataBase64: base64.StdEncoding.EncodeToString(buf)}}
	}
	return &askPayload{Err: "暂支持文字、图片、文件、视频、语音提问"}
}

// downloadImageBytes 图片本体下载（已下载缓存直接读盘；否则走网关）。
func (m *Module) downloadImageBytes(ctx context.Context, img *ImageEntry) ([]byte, error) {
	if img.File != "" {
		if buf, err := os.ReadFile(m.photoPath(img.File)); err == nil {
			return buf, nil
		}
	}
	if m.Images == nil {
		return nil, errors.New("未接入图片下载客户端")
	}
	return m.Images.DownloadImage(ctx, img.FileNo, img.AESKey)
}

// handleGroupAsk 群内提问主流程：六形态装配 → 受理回执 → 调 Call Me → WF3 命名 → md 文件发回群。
// 不抛错，所有失败回执到群（发送失败仅记日志）。
func (m *Module) handleGroupAsk(ctx context.Context, msg *Message, rule Rule, senderID, content, commandText string, quote *Quote) {
	if !isAskEnabled(rule) {
		return // 未启用静默忽略
	}
	if m.Ask == nil {
		m.receiptQuote(ctx, msg, "未配置壹匣对接")
		return
	}
	payload := m.buildAskPayload(ctx, quote, content, commandText, senderID)
	if payload == nil {
		return
	}
	if payload.Err != "" {
		m.receiptQuote(ctx, msg, payload.Err)
		return
	}
	// 受理回执（失败仅记日志，不打断提问）
	m.receiptQuote(ctx, msg, "正在思考，请稍候")

	req := AskRequest{
		Wxid:         senderID,
		Query:        payload.Query,
		SessionTitle: strings.TrimSpace(rule.TeamName) + "群内提问",
		WebSearch:    true,
		Image:        payload.Image,
		File:         payload.File,
	}
	askCtx, cancel := context.WithTimeout(ctx, askTimeout)
	defer cancel()
	answer, err := m.Ask.IngestChat(askCtx, req)
	if err != nil {
		m.appLogErr(ctx, "wechat.ask.failed", "群内提问调用 Call Me 失败", err, map[string]any{
			"group_id": msg.ConversationID, "sender": senderID,
		})
		m.receiptQuote(ctx, msg, firstNonEmpty(mapAskError(err), "提问失败："+err.Error()))
		return
	}
	if strings.TrimSpace(answer) == "" {
		m.receiptQuote(ctx, msg, "提问失败：壹匣未返回回答")
		return
	}

	// WF3 文件名生成；失败/空名用「群内问答-时间戳」兜底，仍把 md 发回群
	filename := ""
	if outputs, err := m.dify().RunWorkflow(ctx, rule.AskNameKey, map[string]string{
		"question": payload.Query,
		"answer":   truncateRunes(answer, askNameInputLen),
	}); err == nil {
		if data := parseWorkflowOutputs(outputs); data != nil {
			filename = sanitizeFileName(textOf(data["filename"]))
		}
	} else {
		m.appLog(ctx, "warn", "business", "wechat.ask.name_failed", "WF3 文件名生成失败（改用兜底文件名）", err.Error(), map[string]any{"group_id": msg.ConversationID})
	}
	if filename == "" {
		filename = "群内问答-" + m.askStamp()
	}
	senderName, _ := m.names().DisplayName(ctx, msg.ConversationID, senderID)
	md := fmt.Sprintf("# %s\n\n提问人：%s\n提问：%s\n时间：%s\n\n---\n\n%s",
		filename, senderName, payload.Query,
		time.Unix(m.msgTime(msg), 0).Format("2006-01-02 15:04"), answer)
	if fs, ok := m.Sender.(FileSender); ok && m.Sender != nil {
		if err := fs.SendFile(ctx, msg.ConversationID, []byte(md), filename+".md"); err == nil {
			m.appLog(ctx, "info", "business", "wechat.ask.replied", "群内提问已回复回答文件", "", map[string]any{
				"group_id": msg.ConversationID, "sender": senderName, "file": filename + ".md", "answer_len": len(answer),
			})
			return
		} else {
			m.appLogErr(ctx, "wechat.ask.send_failed", "回答文件发送失败", err, map[string]any{"group_id": msg.ConversationID})
		}
	}
	// 无文件发送能力或发送失败：文本兜底摘要
	m.receiptQuote(ctx, msg, "回答已生成但文件发送失败\n"+truncateRunes(answer, askFallbackLen))
}
