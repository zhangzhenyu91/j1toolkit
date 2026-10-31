// 消息处理管线（移植 crane-answer src/wechatpadpro/messageHandler.js）：
// 树障/交跨双漏斗初筛 → Dify 终审提取 → CaptureSink 上送；引用更正、撤回启发式、
// 图片辅证挂接（仅「先引用图片、再发送记录文字」路径）、未消费聊天归档。
// 严禁汇总 xlsx 与群内「报送」指令（@指令仅静默消费）。
// 消息串行处理（单条处理完再下一条），提取中撤回/更正竞态不出现。
// 撤回定位一律以撤回人为准（同 sender 时间线），任何兜底不跨 sender，绝不误删他人记录。
package wechatanswer

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// RECALL_WINDOW_SEC 撤回匹配时间窗（秒）。
const RECALL_WINDOW_SEC = 180

// Message 解析后的入站消息（字段兼容 WeChatPadPro 多种键名）。
type Message struct {
	Kind           string
	Type           int
	ConversationID string
	IsGroup        bool
	SenderID       string // extractSender 后（含 wxid: 前缀解析）
	Content        string // extractSender 后
	Direction      string
	CreatedAt      int64
	SvrID          string
	Location       *MessageLocation
	Image          *MessageImage
	App            *MessageApp
	SystemCategory string
	Raw            map[string]any
}

// MessageLocation 位置消息。
type MessageLocation struct {
	Longitude float64
	Latitude  float64
	Label     string
	POIName   string
}

// MessageImage 图片消息 CDN 下载参数。
type MessageImage struct {
	FileNo  string
	AESKey  string
	DataLen int64
}

// MessageApp app 形态消息（引用等）。
type MessageApp struct {
	Category  string
	Title     string
	Reference *Quote
}

// Quote 引用消息信息。
type Quote struct {
	SvrID      string
	Content    string
	Text       string // 引用者新发的可见文字
	MsgType    int    // 被引用消息类型（3=图片）；0 表示未知
	HasMsgType bool
	CreateTime string
}

// toStr JSON 值转字符串。
func toStr(v any) string {
	switch s := v.(type) {
	case nil:
		return ""
	case string:
		return s
	case float64:
		return strconv.FormatFloat(s, 'f', -1, 64)
	default:
		return ""
	}
}

func toFloat(v any) (float64, bool) {
	f, ok := v.(float64)
	return f, ok
}

func toInt64(v any) int64 {
	f, ok := v.(float64)
	if !ok {
		return 0
	}
	return int64(f)
}

// ParseMessage 报文 map → Message；非群聊也会解析（入口再过滤）。
func ParseMessage(raw map[string]any) *Message {
	if raw == nil {
		return nil
	}
	msg := &Message{Raw: raw}
	msg.Kind = toStr(raw["kind"])
	msg.Type = int(toInt64(raw["type"]))
	msg.ConversationID = toStr(raw["conversation_id"])
	msg.Direction = toStr(raw["direction"])
	msg.CreatedAt = toInt64(raw["created_at"])
	for _, key := range []string{"svr_id", "new_msg_id", "id", "svrid", "srvid", "msg_id"} {
		if s := toStr(raw[key]); s != "" {
			msg.SvrID = s
			break
		}
	}
	if b, ok := raw["is_group"].(bool); ok {
		msg.IsGroup = b
	}
	if msg.ConversationID != "" && strings.HasSuffix(msg.ConversationID, "@chatroom") {
		msg.IsGroup = true
	}
	if loc, ok := raw["location"].(map[string]any); ok && loc != nil {
		l := &MessageLocation{Label: toStr(loc["label"]), POIName: toStr(loc["poi_name"])}
		l.Longitude, _ = toFloat(loc["longitude"])
		l.Latitude, _ = toFloat(loc["latitude"])
		msg.Location = l
	}
	if img, ok := raw["image"].(map[string]any); ok && img != nil {
		msg.Image = parseImageMeta(img)
	}
	if app, ok := raw["app"].(map[string]any); ok && app != nil {
		a := &MessageApp{Category: toStr(app["category"]), Title: toStr(app["title"])}
		if ref, ok := app["reference"].(map[string]any); ok && ref != nil {
			q := &Quote{
				SvrID:      firstNonEmpty(toStr(ref["svr_id"]), toStr(ref["new_msg_id"]), toStr(ref["msg_id"])),
				Content:    toStr(ref["content"]),
				CreateTime: firstNonEmpty(toStr(ref["createtime"]), toStr(ref["create_time"])),
			}
			if t, ok := ref["msg_type"].(float64); ok {
				q.MsgType = int(t)
				q.HasMsgType = true
			}
			a.Reference = q
		}
		msg.App = a
	}
	if sys, ok := raw["system"].(map[string]any); ok && sys != nil {
		msg.SystemCategory = toStr(sys["category"])
	}
	msg.SenderID = toStr(raw["sender_id"])
	msg.Content = toStr(raw["content"])
	sender, content := extractSender(msg)
	msg.SenderID = sender
	msg.Content = content
	return msg
}

// parseImageMeta 提取 CDN 下载参数（variant=standard 优先，退化顶层字段）。
func parseImageMeta(img map[string]any) *MessageImage {
	var fileNo, aesKey string
	var dataLen int64
	if list, ok := img["cdn_download_contexts"].([]any); ok {
		var std map[string]any
		for _, item := range list {
			m, _ := item.(map[string]any)
			if m == nil {
				continue
			}
			if toStr(m["variant"]) == "standard" {
				std = m
				break
			}
			if std == nil {
				std = m
			}
		}
		if std != nil {
			fileNo = toStr(std["file_no"])
			aesKey = toStr(std["file_aes_key"])
		}
	}
	if fileNo == "" {
		fileNo = toStr(img["standard_file_no"])
	}
	if aesKey == "" {
		aesKey = toStr(img["aes_key"])
	}
	dataLen = toInt64(img["data_len"])
	if fileNo == "" || aesKey == "" {
		return nil
	}
	return &MessageImage{FileNo: fileNo, AESKey: aesKey, DataLen: dataLen}
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

var senderPrefixRe = regexp.MustCompile(`(?s)^([^\s:]{1,64}):\n(.*)$`)

// extractSender 群消息发送者：sender_id / reply_context.from_user_id，或 content 前缀 `wxid:\n正文`。
func extractSender(msg *Message) (senderID, content string) {
	senderID = msg.SenderID
	if rc, ok := msg.Raw["reply_context"].(map[string]any); ok && rc != nil && senderID == "" {
		senderID = toStr(rc["from_user_id"])
	}
	content = msg.Content
	if msg.IsGroup && content != "" {
		if m := senderPrefixRe.FindStringSubmatch(content); m != nil {
			if senderID == "" {
				senderID = m[1]
			}
			content = m[2]
		}
	}
	return senderID, content
}

/* ------------------------- 初筛闸（交跨/树障语义） ------------------------- */

var (
	// 杆号允许字母后缀（12A#/15B#、12A号）与连字符标签（12-A#），区间两侧同样允许（12A#-15B#），不剥离字母。
	poleRangeRe    = regexp.MustCompile(`\d+[A-Za-z-]*\s*#?\s*[-—~－至]\s*#?\s*\d+[A-Za-z-]*`)
	poleSingleRe   = regexp.MustCompile(`\d+[A-Za-z-]*\s*(?:#|号)(?:塔底)?`)
	featureWordRe  = regexp.MustCompile(`棵|垂距|垂直|超高|水平`)
	treeWordRe     = regexp.MustCompile(`树|棵`)
	measureWordRe  = regexp.MustCompile(`水平|垂距|垂直|超高`)
	crossExcludeRe = regexp.MustCompile(`交叉|跨越`)
	crossIncludeRe = regexp.MustCompile(`交叉|跨越|交跨`)
)

// passesCaptureGate 树障初筛（宽进严出，终审 Dify is_tree_hazard）：杆号+特征词，或树种词+量测词；纯交叉跨越排除。
func passesCaptureGate(text string) bool {
	if crossExcludeRe.MatchString(text) && !strings.Contains(text, "棵") {
		return false
	}
	hasPole := poleRangeRe.MatchString(text) || poleSingleRe.MatchString(text)
	if hasPole && featureWordRe.MatchString(text) {
		return true
	}
	return treeWordRe.MatchString(text) && measureWordRe.MatchString(text)
}

// passesCrossingGate 交跨初筛（treeWatch 子能力）：命中交叉/跨越/交跨且含杆号区间或单杆号。
func passesCrossingGate(text string) bool {
	if !crossIncludeRe.MatchString(text) {
		return false
	}
	return poleRangeRe.MatchString(text) || poleSingleRe.MatchString(text)
}

/* ------------------------- 引用识别 ------------------------- */

func unescapeXML(text string) string {
	r := strings.NewReplacer("&lt;", "<", "&gt;", ">", "&quot;", `"`, "&amp;", "&")
	return r.Replace(text)
}

var referBlockRe = regexp.MustCompile(`(?s)<refermsg[^>]*>(.*?)</refermsg>`)

// parseReferXML 解析 refermsg 块（兼容 &lt;refermsg&gt; 实体转义形态，最多反转义两轮）。
func parseReferXML(raw string) *Quote {
	text := raw
	for i := 0; i < 2 && !regexp.MustCompile(`<refermsg[\s>]`).MatchString(text); i++ {
		un := unescapeXML(text)
		if un == text {
			break
		}
		text = un
	}
	block := referBlockRe.FindStringSubmatch(text)
	if block == nil {
		return nil
	}
	body := block[1]
	pick := func(tag string) string {
		re := regexp.MustCompile(`(?s)<` + tag + `>(.*?)</` + tag + `>`)
		if m := re.FindStringSubmatch(body); m != nil {
			return strings.TrimSpace(unescapeXML(m[1]))
		}
		return ""
	}
	q := &Quote{
		SvrID:      pick("svrid"),
		Content:    pick("content"),
		CreateTime: pick("createtime"),
	}
	if t := pick("type"); t != "" {
		if n, err := strconv.Atoi(t); err == nil {
			q.MsgType = n
			q.HasMsgType = true
		}
	}
	return q
}

var titleRe = regexp.MustCompile(`(?s)<title>(.*?)</title>`)

func quoteTitle(content string) string {
	if m := titleRe.FindStringSubmatch(content); m != nil {
		return strings.TrimSpace(unescapeXML(m[1]))
	}
	return strings.TrimSpace(content)
}

var referMsgKeyRe = regexp.MustCompile(`(?i)^refer_?msg$`)

// findReferSource refermsg 候选源收集：一级字符串字段 + 深扫（限深 6、跳过 reply_context），取首个带 svrid 的。
func findReferSource(msg *Message, content string) *Quote {
	direct := []any{content}
	for _, key := range []string{"content", "raw", "msg_source", "source"} {
		direct = append(direct, msg.Raw[key])
	}
	if gf, ok := msg.Raw["go_fields"].(map[string]any); ok && gf != nil {
		direct = append(direct, gf["Content"], gf["content"])
	}
	for _, item := range direct {
		s, ok := item.(string)
		if !ok || strings.TrimSpace(s) == "" {
			continue
		}
		if q := parseReferXML(s); q != nil && q.SvrID != "" {
			return q
		}
	}
	var found *Quote
	var walk func(node any, depth int)
	walk = func(node any, depth int) {
		if found != nil || depth > 6 || node == nil {
			return
		}
		switch v := node.(type) {
		case string:
			if len(v) > 65536 || !regexp.MustCompile(`(?i)refermsg|refer_msg`).MatchString(v) {
				return
			}
			if q := parseReferXML(v); q != nil && q.SvrID != "" {
				found = q
			}
		case map[string]any:
			for key, value := range v {
				if found != nil {
					return
				}
				if key == "reply_context" {
					continue
				}
				if referMsgKeyRe.MatchString(key) {
					if obj, ok := value.(map[string]any); ok && obj != nil {
						svrid := strings.TrimSpace(firstNonEmpty(toStr(obj["svrid"]), toStr(obj["svr_id"]), toStr(obj["new_msg_id"])))
						if svrid != "" {
							q := &Quote{
								SvrID:      svrid,
								Content:    toStr(obj["content"]),
								CreateTime: firstNonEmpty(toStr(obj["createtime"]), toStr(obj["create_time"])),
							}
							if t := toStr(obj["type"]); t != "" {
								if n, err := strconv.Atoi(t); err == nil {
									q.MsgType = n
									q.HasMsgType = true
								}
							}
							found = q
							return
						}
					}
				}
				walk(value, depth+1)
			}
		case []any:
			for _, item := range v {
				if found != nil {
					return
				}
				walk(item, depth+1)
			}
		}
	}
	walk(msg.Raw, 0)
	return found
}

// extractQuote 引用识别：app.category=quote 优先（reference.msg_type=3 表示被引用是图片）；
// 兜底解析 refermsg 块。text 为引用者新发的可见文字。
func extractQuote(msg *Message, content string) *Quote {
	if msg.App != nil && msg.App.Category == "quote" && msg.App.Reference != nil {
		ref := msg.App.Reference
		text := strings.TrimSpace(msg.App.Title)
		if text == "" {
			text = strings.TrimSpace(content)
		}
		return &Quote{SvrID: ref.SvrID, Content: ref.Content, Text: text, MsgType: ref.MsgType, HasMsgType: ref.HasMsgType, CreateTime: ref.CreateTime}
	}
	if refer := findReferSource(msg, content); refer != nil && (refer.SvrID != "" || refer.Content != "") {
		return &Quote{SvrID: refer.SvrID, Content: refer.Content, Text: quoteTitle(content), MsgType: refer.MsgType, HasMsgType: refer.HasMsgType, CreateTime: refer.CreateTime}
	}
	return nil
}

/* ------------------------- 撤回竞态暂存（可靠持久化） ------------------------- */

// stashRecall 暂存未命中记录的撤回事件（RecallLog 持久化，跨重启可靠；按群保留时间窗内）。
func (m *Module) stashRecall(groupID, senderID string, at int64) {
	if senderID == "" {
		return
	}
	if err := m.recallLog().StashRecall(context.Background(), groupID, senderID, at); err != nil {
		log.Printf("[微信智能应答] 撤回暂存失败（%s）：%v", groupID, err)
	}
}

// popRecall 记录落库时补判撤回（方向性：撤回时间 ≥ 报送时间且在时间窗内）。
func (m *Module) popRecall(groupID string, rec *Record) bool {
	hit, err := m.recallLog().PopRecall(context.Background(), groupID, rec.SenderID, rec.CapturedAt, RECALL_WINDOW_SEC)
	if err != nil {
		log.Printf("[微信智能应答] 撤回补判失败（%s）：%v", groupID, err)
		return false
	}
	return hit
}

// isRecallEvent 撤回系统消息判定。
func isRecallEvent(msg *Message) bool {
	return msg.Kind == "system" &&
		(msg.Type == 10002 || msg.SystemCategory == "message_recalled")
}

/* ------------------------- 入口与分发 ------------------------- */

// HandleMessage 单条消息入口：返回是否被规则消费（消费=不进聊天兜底归档）。
// 幂等：同 svr_id 重复投递（断线重连重放）只处理一次，重复报已消费。
func (m *Module) HandleMessage(ctx context.Context, msg *Message) bool {
	if msg == nil || !msg.IsGroup {
		return false
	}
	m.rawLogMessage(ctx, msg)
	if msg.SvrID != "" {
		first, err := m.eventLog().ClaimEvent(ctx, msg.SvrID, msg.ConversationID, "message")
		if err == nil && !first {
			m.rawLogf(ctx, "重复投递消息已跳过（svr_id=%s）", msg.SvrID)
			return true
		}
	}
	m.touchGroup(ctx, msg.ConversationID, m.msgTime(msg))
	rules, err := m.rulesProvider().RulesForGroup(ctx, msg.ConversationID)
	if err != nil {
		log.Printf("[微信智能应答] 规则读取失败（%s）：%v", msg.ConversationID, err)
		m.appLogErr(ctx, "wechat.rule.load_failed", "监听规则读取失败", err, map[string]any{"group_id": msg.ConversationID})
		rules = nil
	}
	consumed := m.dispatchRules(ctx, msg, rules)
	if !consumed {
		m.collectUnconsumed(ctx, msg, rules)
	}
	return consumed
}

// touchGroup 回写群最近消息时间（DB 未注入时静默）。
func (m *Module) touchGroup(ctx context.Context, groupID string, at int64) {
	if m.DB == nil || groupID == "" {
		return
	}
	_, _ = m.DB.ExecContext(ctx, `UPDATE wa_group SET last_message_at=? WHERE group_id=?`, at, groupID)
}

// dispatchRules 规则分发：消费取并集；处理异常视为已消费（防异常消息进归档）。
func (m *Module) dispatchRules(ctx context.Context, msg *Message, rules []Rule) bool {
	consumed := false
	senderID, content := msg.SenderID, msg.Content
	for _, rule := range rules {
		if !rule.Enabled {
			continue
		}
		switch rule.Trigger {
		case TriggerLocation:
			if firstNonEmpty(rule.LocationKey, rule.ExtractKey) == "" {
				continue
			}
			if msg.Kind != "location" || msg.Location == nil {
				continue
			}
			consumed = true
			if err := m.handleLocation(ctx, msg, rule); err != nil {
				log.Printf("[微信智能应答] 位置规则「%s」处理失败：%v", rule.GroupName, err)
			}
		case TriggerTreeWatch:
			if rule.ExtractKey == "" {
				continue
			}
			if isRecallEvent(msg) {
				consumed = true
				if err := m.handleRecall(ctx, msg, rule); err != nil {
					log.Printf("[微信智能应答] 撤回处理失败：%v", err)
				}
				continue
			}
			hit, err := m.handleTreeWatch(ctx, msg, rule, senderID, content)
			if err != nil {
				consumed = true
				log.Printf("[微信智能应答] treeWatch 处理失败：%v", err)
				continue
			}
			if hit {
				consumed = true
			}
		}
	}
	return consumed
}

// handleLocation 位置消息触发：提取经纬度后调用该规则的 Dify workflow（只触发不入库）。
func (m *Module) handleLocation(ctx context.Context, msg *Message, rule Rule) error {
	senderName, _ := m.names().DisplayName(ctx, msg.ConversationID, msg.SenderID)
	outputs, err := m.dify().RunWorkflow(ctx, firstNonEmpty(rule.LocationKey, rule.ExtractKey), map[string]string{
		"lng":      strconv.FormatFloat(msg.Location.Longitude, 'f', -1, 64),
		"lat":      strconv.FormatFloat(msg.Location.Latitude, 'f', -1, 64),
		"nickname": senderName,
		"wxid":     msg.SenderID,
	})
	if err != nil {
		return err
	}
	log.Printf("[微信智能应答] 位置触发完成（群 %s，发送者 %s）输出=%v", msg.ConversationID, senderName, outputs)
	return nil
}

var atListKeys = []string{"mention_list", "at_list", "atUserList", "at_users", "mentions", "atUserIds", "at_user_ids"}
var atUserListRe = regexp.MustCompile(`(?s)<atuserlist>(.*?)</atuserlist>`)

// botMentioned @检测：at 列表 / msgsource atuserlist / 文本 @群昵称。
func (m *Module) botMentioned(ctx context.Context, msg *Message, content string) bool {
	botWxid := strings.TrimSpace(m.cfgValue(ctx, "WECHATPADPRO_BOT_WXID", ""))
	if botWxid == "" {
		return false
	}
	var listed []string
	appendList := func(v any) {
		if arr, ok := v.([]any); ok {
			for _, item := range arr {
				listed = append(listed, toStr(item))
			}
		}
	}
	for _, key := range atListKeys {
		appendList(msg.Raw[key])
	}
	if src, ok := msg.Raw["msg_source"].(map[string]any); ok && src != nil {
		for _, key := range []string{"at_user_ids", "atUserList", "mentions"} {
			appendList(src[key])
		}
	}
	rawText := strings.Join([]string{msg.Content, toStr(msg.Raw["raw"]), toStr(msg.Raw["msg_source"])}, "\n")
	if m := atUserListRe.FindStringSubmatch(rawText); m != nil {
		listed = append(listed, strings.Split(m[1], ",")...)
	}
	for _, item := range listed {
		if strings.TrimSpace(item) == botWxid {
			return true
		}
	}
	botName, _ := m.names().DisplayName(ctx, msg.ConversationID, botWxid)
	if botName != "" && botName != botWxid && strings.Contains(content, "@"+botName) {
		return true
	}
	return false
}

// stripMentions 去掉 @提及 文本。
func stripMentions(content string) string {
	re := regexp.MustCompile(`@[^\s@]+`)
	return strings.Join(strings.Fields(re.ReplaceAllString(content, " ")), " ")
}

// handleTreeWatch 输电班组运维入口：树障/交跨双漏斗 + 引用更正/图片辅证 + @指令静默消费。
// 返回是否消费；新增分支保持该约定（被认领含忽略类一律 true）。
func (m *Module) handleTreeWatch(ctx context.Context, msg *Message, rule Rule, senderID, content string) (bool, error) {
	botWxid := strings.TrimSpace(m.cfgValue(ctx, "WECHATPADPRO_BOT_WXID", ""))
	if msg.Direction == "outgoing" {
		return true, nil
	}
	if botWxid != "" && senderID == botWxid {
		return true, nil
	}

	if m.botMentioned(ctx, msg, content) {
		// 「报送」/「汇总」严禁：@ 监听账号不触发任何群内报表或重推。
		// 群内提问（WF3）启用时走 Call Me 问答，否则静默消费（提问未启用也算消费，不进聊天归档）。
		quote := extractQuote(msg, content)
		cmd := stripMentions(content)
		if quote != nil && quote.Text != "" {
			cmd = stripMentions(quote.Text) // 引用形态：可见提问文字取 title，避免 XML 入问
		}
		if isAskEnabled(rule) && m.Ask != nil {
			if m.AskSync {
				m.handleGroupAsk(ctx, msg, rule, senderID, content, cmd, quote)
			} else {
				go m.handleGroupAsk(context.WithoutCancel(ctx), msg, rule, senderID, content, cmd, quote)
			}
		} else {
			log.Printf("[微信智能应答] @指令已消费（不支持报送/汇总，指令=%q）", cmd)
		}
		return true, nil
	}

	// 图片消息先行分流：一律只缓存 CDN 参数备查（辅证挂接仅「先引用图片、再发送记录文字」路径）
	if msg.Kind == "image" || msg.Image != nil {
		if err := m.handleImageMessage(ctx, msg, senderID); err != nil {
			log.Printf("[微信智能应答] 图片消息处理失败：%v", err)
		}
		return true, nil
	}

	quote := extractQuote(msg, content)
	if quote != nil {
		dayStart, dayEnd, _ := beijingDayRange(beijingDay(m.msgTime(msg)))
		located, err := m.recordStore().FindTodayRecord(ctx, msg.ConversationID, quote.SvrID, quote.Content, dayStart, dayEnd)
		if err != nil {
			return true, err
		}
		imageHit := (*ImageEntry)(nil)
		if quote.SvrID != "" {
			imageHit, _ = m.recordStore().Image(ctx, quote.SvrID)
		}
		quotingImage := located == nil && ((quote.HasMsgType && quote.MsgType == 3) || imageHit != nil)
		if quotingImage {
			if strings.TrimSpace(quote.Text) == "" {
				log.Printf("[微信智能应答] 引用图片但未附文字（svr_id=%s），忽略", quote.SvrID)
				return true, nil
			}
			if passesCrossingGate(quote.Text) {
				if rule.CrossingKey == "" {
					log.Printf("[微信智能应答] 引用图片的交跨报送未捕获（群 %s 未配置交跨提取 Key）：%s", rule.GroupName, truncate(quote.Text, 60))
					return true, nil
				}
				if err := m.captureCrossing(ctx, msg, rule, senderID, quote.Text, ""); err != nil {
					return true, err
				}
				return true, nil
			}
			if !passesCaptureGate(quote.Text) {
				log.Printf("[微信智能应答] 引用图片的文字未过初筛，忽略：%s", truncate(quote.Text, 60))
				return true, nil
			}
			if err := m.captureRecord(ctx, msg, rule, senderID, quote.Text, quote.SvrID); err != nil {
				return true, err
			}
			return true, nil
		}
		if err := m.handleCorrection(ctx, msg, rule, senderID, quote); err != nil {
			return true, err
		}
		return true, nil
	}

	// 文件类消息：缓存下载参数（引用提问/文档归档用）；file 登记未消费文档归档，其余媒体备查算消费
	if msg.Kind != "" && msg.Kind != "text" && msg.Kind != "quote" && msg.Kind != "app" {
		m.cacheMedia(ctx, msg, senderID)
		if msg.Kind == "file" {
			m.collectDocument(ctx, msg, rule, senderID)
			return false, nil
		}
		return true, nil
	}
	if content == "" || strings.HasPrefix(strings.TrimSpace(content), "<") {
		return true, nil
	}

	// 双漏斗：交跨闸优先（未配交跨 Key 记日志忽略，算消费）
	if passesCrossingGate(content) {
		if rule.CrossingKey == "" {
			log.Printf("[微信智能应答] 交跨报送未捕获（群 %s 未配置交跨提取 Key）：%s", rule.GroupName, truncate(content, 60))
			return true, nil
		}
		if err := m.captureCrossing(ctx, msg, rule, senderID, strings.TrimSpace(content), ""); err != nil {
			return true, err
		}
		return true, nil
	}
	if !passesCaptureGate(content) {
		return false, nil
	}
	if err := m.captureRecord(ctx, msg, rule, senderID, strings.TrimSpace(content), ""); err != nil {
		return true, err
	}
	return true, nil
}

// msgTime 消息时间（缺省取当前）。
func (m *Module) msgTime(msg *Message) int64 {
	if msg.CreatedAt > 0 {
		return msg.CreatedAt
	}
	return m.now().Unix()
}

// newRecordKey 生成记录技术主键。
func newRecordKey() string {
	var buf [16]byte
	_, _ = rand.Read(buf[:])
	return "rec_" + hex.EncodeToString(buf[:])
}

/* ------------------------- 捕获（树障/交跨） ------------------------- */

// captureRecord 树障捕获：落 pending → Dify 提取 → is_tree_hazard 终审 → 辅证挂接 → 上送。
// photoSvrID 可选（引用图片发文字场景被引用图片的 svr_id）。
func (m *Module) captureRecord(ctx context.Context, msg *Message, rule Rule, senderID, content, photoSvrID string) error {
	senderName, _ := m.names().DisplayName(ctx, msg.ConversationID, senderID)
	rec := &Record{
		Key:        newRecordKey(),
		GroupID:    msg.ConversationID,
		GroupName:  rule.GroupName,
		Kind:       KindTree,
		MsgID:      msg.SvrID,
		SenderID:   senderID,
		SenderName: senderName,
		Content:    content,
		CapturedAt: m.msgTime(msg),
		Status:     "pending",
		Extracted:  map[string]any{},
	}
	if err := m.recordStore().InsertRecord(ctx, rec); err != nil {
		return err
	}
	if m.popRecall(msg.ConversationID, rec) {
		_ = m.recordStore().DeleteRecord(ctx, rec.Key)
		log.Printf("[树木排查] 记录落库前检测到对应撤回，已删除且不再提取：%s", truncate(content, 40))
		m.receiptText(ctx, msg.ConversationID, "已删除")
		return nil
	}
	outputs, err := m.dify().RunWorkflow(ctx, rule.ExtractKey, map[string]string{
		"text": content, "nickname": senderName, "wxid": senderID, "group": rule.GroupName,
	})
	if err != nil {
		_ = m.recordStore().DeleteRecord(ctx, rec.Key)
		log.Printf("[树木排查] 提取调用失败，记录已丢弃：%v，原文：%s", err, truncate(content, 60))
		m.receiptQuote(ctx, msg, "记录失败")
		return nil
	}
	ex := parseWorkflowOutputs(outputs)
	if ex == nil {
		_ = m.recordStore().DeleteRecord(ctx, rec.Key)
		log.Printf("[树木排查] Dify 提取结果无法解析，原文：%s", truncate(content, 60))
		return nil
	}
	if !verdictAccept(ex["is_tree_hazard"]) {
		_ = m.recordStore().DeleteRecord(ctx, rec.Key)
		log.Printf("[树木排查] 判定为非树障消息，已丢弃：%s", truncate(content, 60))
		return nil
	}
	if cur, _ := m.recordStore().GetRecord(ctx, rec.Key); cur == nil {
		log.Printf("[树木排查] 记录 %s 在提取期间已被撤回删除，跳过落库与「已记录」反馈", rec.Key)
		return nil
	}
	lineNameRaw, autoFilled := m.resolveLineNameRaw(ctx, rec, ex, KindTree, rule)
	rec.Extracted = ex
	rec.LineNameRaw = lineNameRaw
	rec.Status = "active"
	if err := m.recordStore().SaveRecord(ctx, rec); err != nil {
		return err
	}
	photoNote := ""
	if photoSvrID != "" {
		note, err := m.attachQuotedImage(ctx, rec, photoSvrID, senderID)
		if err != nil {
			log.Printf("[树木排查] 记录 %s 辅证照片下载失败（记录保留）：%v", rec.Key, err)
			photoNote = "，但辅证照片下载失败"
		} else {
			photoNote = note
		}
	}
	lineNote := ""
	if autoFilled != "" {
		lineNote = "（线路按你上次记录补为：" + autoFilled + "）"
	}
	m.receiptQuote(ctx, msg, "已记录"+photoNote+lineNote)
	m.pushWithReceipt(ctx, rec, rule, msg)
	return nil
}

// captureCrossing 交跨捕获（treeWatch 子能力）：无辅证照片，终审 is_crossing，上送 crossing 载荷。
func (m *Module) captureCrossing(ctx context.Context, msg *Message, rule Rule, senderID, content, photoSvrID string) error {
	senderName, _ := m.names().DisplayName(ctx, msg.ConversationID, senderID)
	rec := &Record{
		Key:        newRecordKey(),
		GroupID:    msg.ConversationID,
		GroupName:  rule.GroupName,
		Kind:       KindCrossing,
		MsgID:      msg.SvrID,
		SenderID:   senderID,
		SenderName: senderName,
		Content:    content,
		CapturedAt: m.msgTime(msg),
		Status:     "pending",
		Extracted:  map[string]any{},
	}
	if err := m.recordStore().InsertRecord(ctx, rec); err != nil {
		return err
	}
	if m.popRecall(msg.ConversationID, rec) {
		_ = m.recordStore().DeleteRecord(ctx, rec.Key)
		log.Printf("[交跨排查] 记录落库前检测到对应撤回，已删除且不再提取：%s", truncate(content, 40))
		m.receiptText(ctx, msg.ConversationID, "已删除")
		return nil
	}
	outputs, err := m.dify().RunWorkflow(ctx, rule.CrossingKey, map[string]string{
		"text": content, "nickname": senderName, "wxid": senderID, "group": rule.GroupName,
	})
	if err != nil {
		_ = m.recordStore().DeleteRecord(ctx, rec.Key)
		log.Printf("[交跨排查] 提取调用失败，记录已丢弃：%v，原文：%s", err, truncate(content, 60))
		m.receiptQuote(ctx, msg, "记录失败")
		return nil
	}
	ex := parseWorkflowOutputs(outputs)
	if ex == nil {
		_ = m.recordStore().DeleteRecord(ctx, rec.Key)
		log.Printf("[交跨排查] Dify 提取结果无法解析，原文：%s", truncate(content, 60))
		return nil
	}
	if !verdictAccept(ex["is_crossing"]) {
		_ = m.recordStore().DeleteRecord(ctx, rec.Key)
		log.Printf("[交跨排查] 判定为非交跨消息，已丢弃：%s", truncate(content, 60))
		return nil
	}
	if cur, _ := m.recordStore().GetRecord(ctx, rec.Key); cur == nil {
		log.Printf("[交跨排查] 记录 %s 在提取期间已被撤回删除，跳过落库与「已记录」反馈", rec.Key)
		return nil
	}
	lineNameRaw, autoFilled := m.resolveLineNameRaw(ctx, rec, ex, KindCrossing, rule)
	rec.Extracted = ex
	rec.LineNameRaw = lineNameRaw
	rec.Status = "active"
	if err := m.recordStore().SaveRecord(ctx, rec); err != nil {
		return err
	}
	lineNote := ""
	if autoFilled != "" {
		lineNote = "（线路按你上次记录补为：" + autoFilled + "）"
	}
	m.receiptQuote(ctx, msg, "已记录"+lineNote)
	m.pushWithReceipt(ctx, rec, rule, msg)
	return nil
}

// resolveLineNameRaw 线路名：提取结果优先，省略时按该成员最近一次同类记录自动补全；
// 随后按 team_line 归一匹配（去电压前缀/罗马数字归一），命中写规范线路名与电压等级。
func (m *Module) resolveLineNameRaw(ctx context.Context, rec *Record, ex map[string]any, kind string, rule Rule) (string, string) {
	lineNameRaw := textOf(ex["line_name"])
	if lineNameRaw == "" {
		lineNameRaw = textOf(ex["line_name_raw"])
	}
	autoFilled := ""
	if lineNameRaw == "" {
		recent, err := m.recordStore().FindRecentLine(ctx, rec.SenderID, kind)
		if err != nil || recent == "" {
			return "", ""
		}
		log.Printf("[微信智能应答] 记录 %s 省略线路名称，按 %s 上次记录自动补全为「%s」", rec.Key, rec.SenderName, recent)
		lineNameRaw = recent
		autoFilled = recent
	}
	// 线路名归一 + team_line 匹配：命中取规范名与电压，未命中保留归一原文
	match := m.resolveLine(ctx, rule.TeamID, lineNameRaw)
	if match.Status == "ok" {
		ex["line_name"] = match.Name
		if match.Voltage != "" {
			ex["voltage_level"] = match.Voltage
		}
		if match.Name != NormalizeLineName(lineNameRaw) {
			log.Printf("[微信智能应答] 记录 %s 线路名归一：%q → %q（电压 %s）", rec.Key, lineNameRaw, match.Name, match.Voltage)
		}
	} else if textOf(ex["line_name"]) == "" {
		ex["line_name"] = NormalizeLineName(lineNameRaw)
	}
	return lineNameRaw, autoFilled
}

// pushWithReceipt 上送 CaptureSink；失败引用报送回复「报送失败，请私聊报送班管及超管」（记录保留，队列补推）。
func (m *Module) pushWithReceipt(ctx context.Context, rec *Record, rule Rule, msg *Message) {
	teamName := m.teamLabel(ctx, rule)
	if teamName == "" {
		rec.PushStatus = "failed"
		rec.PushError = "该规则未配置归属班组"
		_ = m.recordStore().SaveRecord(ctx, rec)
		log.Printf("[微信智能应答] 记录 %s 未上送：%s", rec.Key, rec.PushError)
		m.receiptQuote(ctx, msg, "报送失败，请私聊报送班管及超管")
		return
	}
	if err := m.pushRecordToSink(ctx, rec, teamName); err != nil {
		log.Printf("[微信智能应答] 记录 %s 上送失败（已入补推队列）：%v", rec.Key, err)
		m.appLogErr(ctx, "wechat.push.failed", "记录上送失败（已入补推队列）", err, map[string]any{"record_key": rec.Key})
		m.receiptQuote(ctx, msg, "报送失败，请私聊报送班管及超管")
	}
}

// teamLabel 归属班组名（wa_group.team_id → sys_team 优先；legacy 规则 team_name 兜底）。
func (m *Module) teamLabel(ctx context.Context, rule Rule) string {
	if name := m.teamNameByID(ctx, rule.TeamID); name != "" {
		return name
	}
	return rule.TeamName
}

/* ------------------------- 引用更正 ------------------------- */

// handleCorrection 引用更正：记录定位后按 kind 路由更正工作流（树障 correct_key / 交跨 crossing_correct_key）。
func (m *Module) handleCorrection(ctx context.Context, msg *Message, rule Rule, senderID string, quote *Quote) error {
	dayStart, dayEnd, _ := beijingDayRange(beijingDay(m.msgTime(msg)))
	located, err := m.recordStore().FindTodayRecord(ctx, msg.ConversationID, quote.SvrID, quote.Content, dayStart, dayEnd)
	if err != nil {
		return err
	}
	if located == nil {
		log.Printf("[微信智能应答] 引用消息未命中当日记录（svr_id=%s），忽略", quote.SvrID)
		return nil
	}
	logPrefix := "[树木排查]"
	correctKey := rule.CorrectKey
	if located.Kind == KindCrossing {
		logPrefix = "[交跨排查]"
		correctKey = rule.CrossingCorrectKey
	}
	correctionText := strings.TrimSpace(quote.Text)
	if correctionText == "" {
		log.Printf("%s 引用消息无更正内容，忽略", logPrefix)
		return nil
	}
	if correctKey == "" {
		log.Printf("%s 更正被忽略（群 %s 未配置更正 API Key）：%s", logPrefix, rule.GroupName, truncate(correctionText, 60))
		return nil
	}
	senderName, _ := m.names().DisplayName(ctx, msg.ConversationID, senderID)
	currentJSON, _ := json.Marshal(located.Extracted)
	outputs, err := m.dify().RunWorkflow(ctx, correctKey, map[string]string{
		"correction_text": correctionText,
		"quoted_text":     firstNonEmpty(quote.Content, located.Content),
		"current_record":  string(currentJSON),
		"nickname":        senderName,
		"wxid":            senderID,
	})
	if err != nil {
		log.Printf("%s 更正工作流调用失败：%v", logPrefix, err)
		m.receiptQuote(ctx, msg, "更正失败")
		return nil
	}
	result := parseWorkflowOutputs(outputs)
	if result == nil {
		log.Printf("%s 更正工作流返回无法解析", logPrefix)
		return nil
	}
	action := textOf(result["action"])
	switch action {
	case "correct":
		patches := toPatchMap(result["patches"])
		if patches == nil {
			log.Printf("%s 更正补丁无法解析：%v", logPrefix, result["patches"])
			return nil
		}
		for k, v := range patches {
			located.Extracted[k] = v
		}
		// 更正重推前装配照片表：文字更正不含照片键，重推必须携带现有辅证，避免空数组清除。
		m.loadRecordPhotos(ctx, located)
		if err := m.recordStore().SaveRecord(ctx, located); err != nil {
			return err
		}
		log.Printf("%s 记录 %s 已更正：%v（by %s）", logPrefix, located.Key, patches, senderName)
		m.receiptQuote(ctx, msg, "已更正")
		if err := m.pushRecordToSink(ctx, located, m.teamLabel(ctx, rule)); err != nil {
			log.Printf("%s 记录 %s 更正后重推失败：%v", logPrefix, located.Key, err)
		}
	case "status":
		note := textOf(result["status"])
		if note == "" {
			note = correctionText
		}
		located.HandledNote = note
		if err := m.recordStore().SaveRecord(ctx, located); err != nil {
			return err
		}
		log.Printf("%s 记录 %s 标记状态：%s（by %s）", logPrefix, located.Key, note, senderName)
	default:
		log.Printf("%s 更正工作流判定无关（action=%s）：%s", logPrefix, action, truncate(correctionText, 60))
	}
	return nil
}

// toPatchMap 更正补丁：对象或 JSON 字符串。
func toPatchMap(v any) map[string]any {
	switch p := v.(type) {
	case map[string]any:
		if p == nil {
			return nil
		}
		return p
	case string:
		var parsed map[string]any
		if err := json.Unmarshal([]byte(p), &parsed); err == nil && parsed != nil {
			return parsed
		}
	}
	return nil
}

/* ------------------------- 撤回 ------------------------- */

var recallNameRe = regexp.MustCompile(`^["“](.+?)["”]\s*撤回了一条消息`)

// handleRecall 撤回事件：撤回人时间线内定位（绝不跨 sender，撤回人无法确定时不删任何记录）——
// 撤到图片 → 摘删辅证照片（记录保留）并重推；撤到记录 → 先推删除再本地连带删照片；皆无 → 暂存补判。
func (m *Module) handleRecall(ctx context.Context, msg *Message, rule Rule) error {
	now := m.msgTime(msg)
	recallerWxid := msg.SenderID
	if recallerWxid == "" || strings.HasSuffix(recallerWxid, "@chatroom") {
		recallerWxid = ""
		if match := recallNameRe.FindStringSubmatch(msg.Content); match != nil {
			name, _ := m.names().WxidByName(ctx, msg.ConversationID, match[1])
			recallerWxid = name
		}
	}
	var recalledImage *ImageEntry
	if recallerWxid != "" {
		recalledImage, _ = m.recordStore().FindRecallImage(ctx, msg.ConversationID, recallerWxid, RECALL_WINDOW_SEC, now)
	}
	treeTarget, treeBy, _ := m.recordStore().FindRecallTarget(ctx, msg.ConversationID, recallerWxid, RECALL_WINDOW_SEC, KindTree, now)
	crossTarget, crossBy, _ := m.recordStore().FindRecallTarget(ctx, msg.ConversationID, recallerWxid, RECALL_WINDOW_SEC, KindCrossing, now)
	var target *Record
	matchedBy := ""
	switch {
	case treeTarget == nil && crossTarget == nil:
	case treeTarget == nil:
		target, matchedBy = crossTarget, crossBy
	case crossTarget == nil:
		target, matchedBy = treeTarget, treeBy
	case crossTarget.CapturedAt > treeTarget.CapturedAt:
		target, matchedBy = crossTarget, crossBy
	default:
		target, matchedBy = treeTarget, treeBy
	}
	logPrefix := "[树木排查]"
	if target != nil && target.Kind == KindCrossing {
		logPrefix = "[交跨排查]"
	}

	// 同一时间线取较新者：图片 ≥ 记录 → 按「撤回图片」处理
	if recalledImage != nil && (target == nil || recalledImage.CapturedAt >= target.CapturedAt) {
		if recalledImage.RecordKey != "" {
			found, _ := m.recordStore().GetRecord(ctx, recalledImage.RecordKey)
			removed, _ := m.recordStore().RemovePhoto(ctx, recalledImage.RecordKey, recalledImage.SvrID)
			_ = m.recordStore().RemoveImage(ctx, recalledImage.SvrID)
			if found != nil {
				if removed != nil && removed.File != "" {
					_ = os.Remove(m.photoPath(removed.File))
				}
				if found.CoSenderID != "" && !m.hasPhotoBy(found.Key, found.CoSenderID) {
					found.CoSenderID = ""
					found.CoSenderName = ""
				}
				// 撤图后重推前装配剩余照片（空数组=权威清除，缺键=不动）。
				m.loadRecordPhotos(ctx, found)
				_ = m.recordStore().SaveRecord(ctx, found)
				if err := m.pushRecordToSink(ctx, found, m.teamLabel(ctx, rule)); err != nil {
					log.Printf("%s 记录 %s 辅证变更重推失败：%v", logPrefix, found.Key, err)
				}
			}
			log.Printf("%s 辅证照片因图片撤回已删除（记录 %s，撤回人=%s）", logPrefix, recalledImage.RecordKey, recallerWxid)
			m.receiptText(ctx, msg.ConversationID, "已删除辅证照片")
		} else {
			_ = m.recordStore().RemoveImage(ctx, recalledImage.SvrID)
			log.Printf("%s 未挂接的缓存图片因撤回已清理（撤回人=%s）", logPrefix, recallerWxid)
		}
		return nil
	}

	if target == nil {
		if recallerWxid == "" {
			log.Printf("%s 撤回事件无法确定撤回人，不删除任何记录", logPrefix)
			return nil
		}
		log.Printf("%s 撤回事件未命中撤回人 %s 的记录，暂存待补判（同 sender 方向性窗口内生效）", logPrefix, recallerWxid)
		m.stashRecall(msg.ConversationID, recallerWxid, now)
		return nil
	}

	// 整条删除：先推删除（失败入补推队列不丢），再本地连带删辅证
	if err := m.pushDeleteToSink(ctx, target, m.teamLabel(ctx, rule)); err != nil {
		log.Printf("%s 记录 %s 删除推送失败，已入补推队列（本地仍删除）：%v", logPrefix, target.Key, err)
	}
	photoCount := m.purgeRecordPhotos(ctx, target)
	if err := m.recordStore().DeleteRecord(ctx, target.Key); err != nil {
		return err
	}
	log.Printf("%s 记录 %s 因消息撤回已删除（匹配方式=%s，连带辅证照片 %d 张）：%s",
		logPrefix, target.Key, matchedBy, photoCount, truncate(target.Content, 40))
	m.receiptText(ctx, msg.ConversationID, "已删除")
	return nil
}

// hasPhotoBy 该记录是否还有指定供图人的照片。
func (m *Module) hasPhotoBy(recordKey, senderID string) bool {
	photos, err := m.recordStore().PhotosByRecord(context.Background(), recordKey)
	if err != nil {
		return false
	}
	for _, p := range photos {
		if p.SenderID == senderID {
			return true
		}
	}
	return false
}

// loadRecordPhotos 装配记录辅证照片并标记已加载；读取失败保持未加载（载荷省略照片键，绝不当作空清除）。
func (m *Module) loadRecordPhotos(ctx context.Context, rec *Record) {
	photos, err := m.recordStore().PhotosByRecord(ctx, rec.Key)
	if err != nil {
		log.Printf("[微信智能应答] 记录 %s 辅证照片读取失败（重推省略照片键）：%v", rec.Key, err)
		return
	}
	rec.Photos = photos
	rec.PhotosLoaded = true
}

// purgeRecordPhotos 记录删除时连带清理辅证照片（缓存条目 + 文件），返回清理张数。
func (m *Module) purgeRecordPhotos(ctx context.Context, rec *Record) int {
	photos, _ := m.recordStore().PhotosByRecord(ctx, rec.Key)
	images, _ := m.recordStore().ImagesByRecord(ctx, rec.Key)
	for _, e := range images {
		if e.File != "" {
			_ = os.Remove(m.photoPath(e.File))
		}
		_ = m.recordStore().RemoveImage(ctx, e.SvrID)
	}
	for _, p := range photos {
		if p.File != "" {
			_ = os.Remove(m.photoPath(p.File))
		}
		_, _ = m.recordStore().RemovePhoto(ctx, rec.Key, strconv.FormatInt(p.ID, 10))
	}
	rec.Photos = nil
	return len(photos)
}

/* ------------------------- 图片辅证 ------------------------- */

// handleImageMessage 图片消息：只缓存 CDN 参数备查（供「先引用图片、再发送记录文字」挂辅证与撤回清理）。
// 「引用记录文字再发图片」形态按无法识别处理：不挂接、不误删——图片报文 extcommoninfo 简形的
// svrid/createtime 无法可靠锚定记录，按内容/时间兜底曾把普通群图误挂他人记录，该路径已停用。
func (m *Module) handleImageMessage(ctx context.Context, msg *Message, senderID string) error {
	entry := m.cacheImage(ctx, msg, senderID)
	if extractQuote(msg, msg.Content) != nil {
		log.Printf("[树木排查] 引用记录发图形态不识别，仅缓存不挂接（svr_id=%s）", msg.SvrID)
		return nil
	}
	if entry != nil {
		log.Printf("[树木排查] 缓存群图片 %s（群 %s），待引用挂接", entry.SvrID, msg.ConversationID)
	}
	return nil
}

// cacheImage 缓存图片 CDN 下载参数（幂等）。
func (m *Module) cacheImage(ctx context.Context, msg *Message, senderID string) *ImageEntry {
	if msg.SvrID == "" || msg.Image == nil {
		return nil
	}
	entry := &ImageEntry{
		SvrID:      msg.SvrID,
		GroupID:    msg.ConversationID,
		SenderID:   senderID,
		CapturedAt: m.msgTime(msg),
		FileNo:     msg.Image.FileNo,
		AESKey:     msg.Image.AESKey,
		DataLen:    msg.Image.DataLen,
	}
	if err := m.recordStore().CacheImage(ctx, entry); err != nil {
		log.Printf("[微信智能应答] 图片缓存写入失败：%v", err)
		return nil
	}
	return entry
}

// attachQuotedImage 引用图片发文字场景：下载被引用图片挂为辅证；返回回执附加文案。
func (m *Module) attachQuotedImage(ctx context.Context, rec *Record, photoSvrID, senderID string) (string, error) {
	cached, _ := m.recordStore().Image(ctx, photoSvrID)
	if cached == nil {
		log.Printf("[树木排查] 记录 %s 引用的图片 %s 不在缓存中，辅证缺失", rec.Key, photoSvrID)
		return "，但未找到引用的图片", nil
	}
	if err := m.downloadAndAttach(ctx, rec, cached, firstNonEmpty(cached.SenderID, senderID)); err != nil {
		return "", err
	}
	return "", nil
}

// downloadAndAttach 下载图片本体并挂接辅证（含协作供图人）。
func (m *Module) downloadAndAttach(ctx context.Context, rec *Record, entry *ImageEntry, senderID string) error {
	if m.Images == nil {
		return errors.New("未接入图片下载客户端")
	}
	rel := "data/wechatanswer/attachments/evi-" + entry.SvrID
	buf, err := m.Images.DownloadImage(ctx, entry.FileNo, entry.AESKey)
	if err != nil {
		return err
	}
	ext := ".jpg"
	if len(buf) > 8 && buf[0] == 0x89 && buf[1] == 0x50 && buf[2] == 0x4e && buf[3] == 0x47 {
		ext = ".png"
	}
	rel += ext
	abs := m.photoPath(rel)
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(abs, buf, 0o600); err != nil {
		return err
	}
	_ = m.recordStore().SetImageFile(ctx, entry.SvrID, rel)
	if err := m.recordStore().AddPhoto(ctx, rec.Key, Photo{
		MsgID: entry.SvrID, SenderID: senderID, CapturedAt: entry.CapturedAt, File: rel, Bytes: len(buf),
	}); err != nil {
		return err
	}
	_ = m.recordStore().AttachImage(ctx, entry.SvrID, rec.Key)
	if senderID != "" && senderID != rec.SenderID && rec.CoSenderID == "" {
		rec.CoSenderID = senderID
		rec.CoSenderName, _ = m.names().DisplayName(ctx, rec.GroupID, senderID)
	}
	photos, _ := m.recordStore().PhotosByRecord(ctx, rec.Key)
	rec.Photos = photos
	rec.PhotosLoaded = true
	return m.recordStore().SaveRecord(ctx, rec)
}

/* ------------------------- 回执与归档 ------------------------- */

// receiptText 群内文本回执（Sender 未注入时静默）。
func (m *Module) receiptText(ctx context.Context, groupID, text string) {
	if m.Sender == nil {
		return
	}
	if err := m.Sender.SendText(ctx, groupID, text); err != nil {
		log.Printf("[微信智能应答] 「%s」回执发送失败：%v", text, err)
	}
}

// receiptQuote 群内引用回执（锚定报送消息）。
func (m *Module) receiptQuote(ctx context.Context, msg *Message, text string) {
	if m.Sender == nil {
		return
	}
	quote := QuoteRef{
		SvrID: msg.SvrID, Content: msg.Content, CreateTime: strconv.FormatInt(m.msgTime(msg), 10),
		FromUserID: msg.SenderID,
	}
	if reply, ok := msg.Raw["reply_context"].(map[string]any); ok && len(reply) > 0 {
		quote.ReplyContext = reply
	}
	if err := m.Sender.SendQuote(ctx, msg.ConversationID, text, quote); err != nil {
		log.Printf("[微信智能应答] 「%s」引用回执发送失败：%v", text, err)
	}
}

// supportedDocRe WeKnora 可入库文档扩展名白名单（与 callme ATTACHMENT_EXTS 对齐）。
var supportedDocRe = regexp.MustCompile(`(?i)\.(pdf|doc|docx|ppt|pptx|xlsx|xls|epub|mhtml|csv|md|markdown|txt|json|html)$`)

// collectUnconsumed 未消费消息兜底归档（绑定 treeWatch 规则的群才收集）。
func (m *Module) collectUnconsumed(ctx context.Context, msg *Message, rules []Rule) {
	if m.Archiver == nil {
		return
	}
	var treeRule *Rule
	for i := range rules {
		if rules[i].Enabled && rules[i].Trigger == TriggerTreeWatch {
			treeRule = &rules[i]
			break
		}
	}
	if treeRule == nil {
		return
	}
	if msg.Direction == "outgoing" {
		return
	}
	botWxid := strings.TrimSpace(m.cfgValue(ctx, "WECHATPADPRO_BOT_WXID", ""))
	if botWxid != "" && msg.SenderID == botWxid {
		return
	}
	kind := msg.Kind
	if kind == "" {
		kind = "text"
	}
	if kind != "text" && kind != "quote" && kind != "app" {
		return
	}
	text := strings.TrimSpace(msg.Content)
	if text == "" && msg.App != nil {
		text = strings.TrimSpace(msg.App.Title)
	}
	if text == "" || strings.HasPrefix(text, "<") {
		return
	}
	senderName, _ := m.names().DisplayName(ctx, msg.ConversationID, msg.SenderID)
	rec := ChatMessage{
		Day:        beijingDay(m.msgTime(msg)),
		GroupID:    msg.ConversationID,
		GroupName:  treeRule.GroupName,
		SvrID:      msg.SvrID,
		SenderID:   msg.SenderID,
		SenderName: senderName,
		Content:    text,
		Time:       m.msgTime(msg),
	}
	if err := m.Archiver.ArchiveMessage(ctx, rec); err != nil {
		log.Printf("[聊天归档] 消息登记失败（%s）：%v", treeRule.GroupName, err)
	}
}

// collectDocument 未消费文档类文件登记（白名单外打 unsupported 标记）。
func (m *Module) collectDocument(ctx context.Context, msg *Message, rule Rule, senderID string) {
	if m.Archiver == nil {
		return
	}
	fileName := toStr(msg.Raw["file_name"])
	if fileName == "" {
		fileName = toStr(msg.Raw["fileName"])
	}
	if fileName == "" {
		fileName = "未命名文件"
	}
	senderName, _ := m.names().DisplayName(ctx, msg.ConversationID, senderID)
	rec := ChatDocument{
		Day:         beijingDay(m.msgTime(msg)),
		GroupID:     msg.ConversationID,
		GroupName:   rule.GroupName,
		SvrID:       msg.SvrID,
		SenderID:    senderID,
		SenderName:  senderName,
		FileName:    fileName,
		Time:        m.msgTime(msg),
		Unsupported: !supportedDocRe.MatchString(fileName),
	}
	if err := m.Archiver.ArchiveDocument(ctx, rec); err != nil {
		log.Printf("[聊天归档] 文档登记失败（%s）：%v", rule.GroupName, err)
	}
}
