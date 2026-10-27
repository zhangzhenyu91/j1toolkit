// 群媒体消息（文件/视频/语音）下载参数解析与缓存（移植 crane-answer src/wechatpadpro/fileStore.js）：
// 消息到达时只缓存下载参数不下载本体（被引用提问/文档归档时才下载）；条目 3 天过期清理。
// 三类媒体报文字段形态待真机首验对齐——解析不全时在 LOG_RAW_MESSAGE 开关下落脱敏原始报文。
package wechatanswer

import (
	"context"
	"regexp"
	"strings"
)

// mediaCacheTTLSec 媒体参数缓存保留时长（秒）：3 天。
const mediaCacheTTLSec = 3 * 24 * 3600

var (
	mediaFileRe  = regexp.MustCompile(`(?s)<appattach[\s>]|<appmsg[^>]*type="6"|<appmsg[^>]*>[\s\S]*?<type>\s*6\s*</type>`)
	mediaVideoRe = regexp.MustCompile(`<videomsg[\s>]|<videomsg\s`)
	mediaVoiceRe = regexp.MustCompile(`<voicemsg[\s>]|<voicemsg\s`)
)

// xmlTag 取标签内文本（含 XML 反转义）。
func xmlTag(raw, name string) string {
	re := regexp.MustCompile(`(?s)<` + name + `>(.*?)</` + name + `>`)
	if m := re.FindStringSubmatch(raw); m != nil {
		return strings.TrimSpace(unescapeXML(m[1]))
	}
	return ""
}

// xmlAttr 取属性值（左边界限定，避免 msgid 误匹配 newmsgid）。
func xmlAttr(raw, name string) string {
	re := regexp.MustCompile(`(?:^|[\s"'\]])` + name + `\s*=\s*"([^"]*)"`)
	if m := re.FindStringSubmatch(raw); m != nil {
		return strings.TrimSpace(unescapeXML(m[1]))
	}
	return ""
}

func xmlNum(s string) int64 {
	n := toInt64Str(s)
	if n > 0 {
		return n
	}
	return 0
}

// toInt64Str 数字串转 int64（非数字/非正返回 0）。
func toInt64Str(s string) int64 {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}
	var n int64
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0
		}
		n = n*10 + int64(c-'0')
	}
	return n
}

// ParseMediaParams 从 XML 报文解析媒体下载参数；非媒体返回 nil。
// 字段按报文实际携带情况部分为空，缺失由缓存或调用方兜底。
func ParseMediaParams(xmlText string) *MediaEntry {
	raw := xmlText
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	kind := ""
	switch {
	case mediaFileRe.MatchString(raw):
		kind = "file"
	case mediaVideoRe.MatchString(raw):
		kind = "video"
	case mediaVoiceRe.MatchString(raw):
		kind = "voice"
	}
	if kind == "" {
		return nil
	}
	firstNonEmptyNum := func(vals ...string) int64 {
		for _, v := range vals {
			if n := xmlNum(v); n > 0 {
				return n
			}
		}
		return 0
	}
	return &MediaEntry{
		Kind:     kind,
		FileName: firstNonEmpty(xmlTag(raw, "title"), xmlAttr(raw, "file_name")),
		AppID:    firstNonEmpty(xmlAttr(raw, "appid"), xmlAttr(raw, "app_id")),
		AttachID: firstNonEmpty(xmlTag(raw, "attachid"), xmlAttr(raw, "attachid"), xmlAttr(raw, "attach_id")),
		DataLen: firstNonEmptyNum(
			xmlTag(raw, "totallen"), xmlAttr(raw, "totallen"),
			xmlTag(raw, "data_len"), xmlAttr(raw, "data_len"), xmlAttr(raw, "length"), xmlTag(raw, "length")),
		MsgID: firstNonEmpty(
			xmlTag(raw, "msg_id"), xmlAttr(raw, "msg_id"), xmlAttr(raw, "msgid"), xmlAttr(raw, "clientmsgid")),
		NewMsgID: firstNonEmpty(
			xmlTag(raw, "new_msg_id"), xmlAttr(raw, "newmsgid"), xmlAttr(raw, "new_msg_id")),
		MasterBufID: firstNonEmpty(
			xmlTag(raw, "master_buf_id"), xmlAttr(raw, "masterbufid"), xmlAttr(raw, "master_buf_id"), xmlAttr(raw, "bufid")),
		Length: xmlNum(firstNonEmpty(xmlAttr(raw, "length"), xmlTag(raw, "length"))),
		FromUserName: firstNonEmpty(
			xmlTag(raw, "from_user_name"), xmlTag(raw, "fromusername"),
			xmlAttr(raw, "from_user_name"), xmlAttr(raw, "fromusername")),
	}
}

// collectXmlSources 消息 XML 候选源（与 crane fileStore.collectXmlSources 同口径）。
func collectXmlSources(msg *Message) string {
	if msg == nil {
		return ""
	}
	parts := []string{msg.Content}
	if msg.App != nil {
		parts = append(parts, msg.App.Title)
	}
	for _, key := range []string{"raw", "msg_source", "source", "content"} {
		parts = append(parts, toStr(msg.Raw[key]))
	}
	if gf, ok := msg.Raw["go_fields"].(map[string]any); ok && gf != nil {
		parts = append(parts, toStr(gf["Content"]), toStr(gf["content"]))
	}
	var kept []string
	for _, p := range parts {
		if strings.TrimSpace(p) != "" {
			kept = append(kept, p)
		}
	}
	return strings.Join(kept, "\n")
}

// cacheMedia 解析并缓存一条文件/视频/语音消息的下载参数（不下载本体）。
// kind 非媒体静默忽略；识别为媒体但关键字段不全时缓存已有参数并按开关落脱敏原始报文。
func (m *Module) cacheMedia(ctx context.Context, msg *Message, senderID string) *MediaEntry {
	if msg == nil {
		return nil
	}
	parsed := ParseMediaParams(collectXmlSources(msg))
	kind := ""
	if parsed != nil {
		kind = parsed.Kind
	}
	if kind == "" {
		switch strings.ToLower(msg.Kind) {
		case "file", "video", "voice":
			kind = msg.Kind
		default:
			return nil
		}
	}
	entry := &MediaEntry{Kind: kind}
	if parsed != nil {
		*entry = MediaEntry{
			Kind:         kind,
			FileName:     parsed.FileName,
			AppID:        parsed.AppID,
			AttachID:     parsed.AttachID,
			DataLen:      parsed.DataLen,
			MsgID:        parsed.MsgID,
			NewMsgID:     parsed.NewMsgID,
			MasterBufID:  parsed.MasterBufID,
			Length:       parsed.Length,
			FromUserName: parsed.FromUserName,
		}
	}
	key := firstNonEmpty(msg.SvrID, entry.NewMsgID)
	if key == "" {
		m.rawLogf(ctx, "媒体消息缺少 svr_id/new_msg_id，无法缓存（kind=%s）", kind)
		return nil
	}
	entry.SvrID = key
	entry.GroupID = msg.ConversationID
	entry.SenderID = senderID
	entry.CapturedAt = m.msgTime(msg)
	if entry.MsgID == "" {
		entry.MsgID = firstNonEmpty(toStr(msg.Raw["msg_id"]), toStr(msg.Raw["new_msg_id"]), msg.SvrID)
	}
	if entry.NewMsgID == "" {
		entry.NewMsgID = firstNonEmpty(toStr(msg.Raw["new_msg_id"]), msg.SvrID)
	}
	if entry.FromUserName == "" {
		entry.FromUserName = senderID
	}
	complete := false
	switch kind {
	case "file":
		complete = entry.AttachID != "" && entry.DataLen > 0 && entry.FileName != ""
	case "video":
		complete = entry.MsgID != "" && entry.DataLen > 0
	case "voice":
		complete = (entry.MsgID != "" || entry.NewMsgID != "") && entry.MasterBufID != "" && entry.Length > 0
	}
	if !complete {
		m.rawLogf(ctx, "媒体消息 %s 关键字段不全（kind=%s），原始报文已脱敏留档", key, kind)
	}
	if err := m.mediaCache().PutMedia(ctx, entry); err != nil {
		m.appLogErr(ctx, "wechat.media.cache_failed", "媒体下载参数缓存失败", err, map[string]any{"svr_id": key})
		return nil
	}
	return entry
}

// cachedMedia 按被引用消息 svr_id 查媒体参数缓存。
func (m *Module) cachedMedia(ctx context.Context, svrID string) *MediaEntry {
	if svrID == "" {
		return nil
	}
	e, err := m.mediaCache().Media(ctx, svrID)
	if err != nil {
		return nil
	}
	return e
}
