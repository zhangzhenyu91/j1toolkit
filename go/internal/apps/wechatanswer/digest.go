// 聊天日志兜底归档 WF4（移植 crane-answer src/inspection/chatlog.js）：
// 收集未消费群消息/文档，每天由 WF4（Dify）总结成当日纪要，连同文档推送 WeKnora
// （ChatlogSink 由 bridge 接 chatlog 模块 ingest 逻辑，直调不走 HTTP 自循环）。
// 失败口径：整组失败 attempts+1 保留（下次补跑），attempts ≥3 丢弃并 error 日志；
// 单个文档参数缺失或超 30MB 只跳过该文档，不阻断整批；未配 Key 的群过期天丢弃。
package wechatanswer

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"
)

// ChatlogItem chatlog/ingest 契约项（kind=digest | file）。
type ChatlogItem struct {
	Date      string   `json:"date"`
	GroupName string   `json:"group_name"`
	Kind      string   `json:"kind"`
	DigestMD  string   `json:"digest_md,omitempty"`
	File      *AskFile `json:"file,omitempty"`
}

// ChatlogSink 聊天归档出口（bridge 接 chatlog 模块 ingest 逻辑或 WeKnora 客户端）。
type ChatlogSink interface {
	Ingest(ctx context.Context, item ChatlogItem) error
}

const (
	digestMaxMessages = 400              // WF4 输入消息条数上限
	digestMaxChars    = 20000            // WF4 输入总字符上限
	digestDocMaxBytes = 30 << 20         // 单文档归档上限（契约 ≤30MB）
	digestInterval    = 30 * time.Minute // 归档巡检间隔
)

// buildDigestInput 拼装 WF4 输入文本：每行「HH:mm 昵称：内容」，最多 400 条、总字符 ≤20000，
// 截断时末尾补一行「（其余 N 条从略）」。
func buildDigestInput(messages []ChatMessage) string {
	var lines []string
	used, total := 0, 0
	for _, msg := range messages {
		if used >= digestMaxMessages {
			break
		}
		content := strings.Join(strings.Fields(msg.Content), " ")
		name := msg.SenderName
		if name == "" {
			name = msg.SenderID
		}
		line := fmt.Sprintf("%s %s：%s", formatHHmmBeijing(msg.Time), name, content)
		if used > 0 && total+len([]rune(line)) > digestMaxChars {
			break
		}
		lines = append(lines, line)
		used++
		total += len([]rune(line)) + 1
	}
	if used < len(messages) {
		lines = append(lines, fmt.Sprintf("（其余 %d 条从略）", len(messages)-used))
	}
	return strings.Join(lines, "\n")
}

// formatHHmmBeijing Unix 秒 → 北京时间 HH:mm（WF4 输入行的时刻前缀）。
func formatHHmmBeijing(unixSec int64) string {
	t := time.Unix(unixSec, 0).In(time.FixedZone("CST", 8*3600))
	return t.Format("15:04")
}

// extractDigestSummary WF4 输出解析：outputs.summary，兼容 result 包裹 JSON 或直接字符串。
func extractDigestSummary(outputs map[string]any) (string, error) {
	if outputs == nil {
		return "", errors.New("WF4 未返回 summary")
	}
	if s := textOf(outputs["summary"]); s != "" {
		return s, nil
	}
	if result, ok := outputs["result"].(map[string]any); ok {
		if s := textOf(result["summary"]); s != "" {
			return s, nil
		}
	}
	if raw := textOf(outputs["result"]); raw != "" {
		if data := toPatchMap(outputs["result"]); data != nil {
			if s := textOf(data["summary"]); s != "" {
				return s, nil
			}
		}
		return raw, nil
	}
	return "", errors.New("WF4 未返回 summary")
}

// digestRule 找该群启用且配了 chat_digest Key 的 treeWatch 规则（多规则取首个）。
func (m *Module) digestRule(ctx context.Context, groupID string) *Rule {
	rules, err := m.rulesProvider().RulesForGroup(ctx, groupID)
	if err != nil {
		return nil
	}
	for i := range rules {
		r := &rules[i]
		if r.Enabled && r.Trigger == TriggerTreeWatch && strings.TrimSpace(r.ChatDigestKey) != "" {
			return r
		}
	}
	return nil
}

// archiveGroupDay 归档某群某天：WF4 总结 → digest 先行 → 逐个文档下载推送。
// 单个文档参数缺失或超 30MB 跳过（记日志不阻断）；其余失败返回错误交调用方计 attempts。
func (m *Module) archiveGroupDay(ctx context.Context, g ChatDayGroup, rule Rule) error {
	messages, err := m.chatDays().DayMessages(ctx, g.Day, g.GroupID)
	if err != nil {
		return err
	}
	documents, err := m.chatDays().DayDocuments(ctx, g.Day, g.GroupID)
	if err != nil {
		return err
	}
	digestInput := buildDigestInput(messages)
	summary := "当日无未消费文本消息，仅归档文档。"
	if digestInput != "" {
		outputs, err := m.dify().RunWorkflow(ctx, rule.ChatDigestKey, map[string]string{
			"date":     g.Day,
			"group":    g.GroupName,
			"messages": digestInput,
			"data":     digestInput,
		})
		if err != nil {
			return fmt.Errorf("WF4 聊天总结失败：%w", err)
		}
		if s, err := extractDigestSummary(parseWorkflowOutputs(outputs)); err == nil {
			summary = s
		} else {
			return err
		}
	}
	var archived, skipped []string
	for _, doc := range documents {
		if doc.Unsupported {
			skipped = append(skipped, doc.FileName)
			continue
		}
		archived = append(archived, doc.FileName)
	}
	docTail := fmt.Sprintf("原始消息 %d 条 / 归档文档 %d 个：%s", len(messages), len(archived),
		firstNonEmpty(strings.Join(archived, "、"), "无"))
	if len(skipped) > 0 {
		docTail += fmt.Sprintf(" / 未归档文档 %d 个（类型不支持入库）：%s", len(skipped), strings.Join(skipped, "、"))
	}
	digestMD := fmt.Sprintf("# %s %s 聊天纪要\n\n%s\n\n---\n\n%s\n", g.GroupName, g.Day, summary, docTail)
	// 纪要先行（chatlog 侧同名先删后传，重跑幂等）
	if m.Chatlog == nil {
		return errors.New("未接入聊天归档出口（ChatlogSink）")
	}
	if err := m.Chatlog.Ingest(ctx, ChatlogItem{Date: g.Day, GroupName: g.GroupName, Kind: "digest", DigestMD: digestMD}); err != nil {
		return fmt.Errorf("纪要推送失败：%w", err)
	}
	m.appLog(ctx, "info", "business", "wechat.chatlog.digest", "当日聊天纪要已推送知识库", "", map[string]any{
		"day": g.Day, "group_id": g.GroupID, "messages": len(messages),
	})
	for _, doc := range documents {
		if doc.Unsupported {
			continue
		}
		if err := m.archiveDocument(ctx, g, doc); err != nil {
			if isSkipDocErr(err) {
				m.appLog(ctx, "warn", "business", "wechat.chatlog.doc_skipped", "文档跳过归档："+doc.FileName, err.Error(), map[string]any{
					"day": g.Day, "group_id": g.GroupID,
				})
				continue
			}
			return fmt.Errorf("文档归档失败（%s）：%w", doc.FileName, err)
		}
	}
	return nil
}

// skipDocErr 单文档跳过（不计失败）标记错误。
type skipDocErr struct{ error }

func isSkipDocErr(err error) bool {
	var s *skipDocErr
	return errors.As(err, &s)
}

// archiveDocument 单文档下载并推送；参数缺失/超限/被拒收 → 跳过类错误。
func (m *Module) archiveDocument(ctx context.Context, g ChatDayGroup, doc ChatDocument) error {
	media := m.cachedMedia(ctx, doc.SvrID)
	if media == nil || media.Kind != "file" {
		return &skipDocErr{errors.New("文档下载参数缺失")}
	}
	if m.Files == nil {
		return &skipDocErr{errors.New("媒体下载未接入")}
	}
	buf, _, err := m.Files.DownloadMedia(ctx, *media)
	if err != nil {
		var ae *AskError
		if errors.As(err, &ae) && ae.Code == "file_too_large" {
			return &skipDocErr{err}
		}
		return err
	}
	if len(buf) > digestDocMaxBytes {
		return &skipDocErr{errors.New("文档超过 30MB 上限")}
	}
	item := ChatlogItem{
		Date: g.Day, GroupName: g.GroupName, Kind: "file",
		File: &AskFile{Name: doc.FileName, DataBase64: base64.StdEncoding.EncodeToString(buf)},
	}
	if err := m.Chatlog.Ingest(ctx, item); err != nil {
		var ae *AskError
		if errors.As(err, &ae) && (ae.Code == "unsupported_file" || ae.Code == "file_too_large") {
			return &skipDocErr{err}
		}
		return err
	}
	m.appLog(ctx, "info", "business", "wechat.chatlog.doc", "文档已推送知识库："+doc.FileName, "", map[string]any{
		"day": g.Day, "group_id": g.GroupID, "bytes": len(buf),
	})
	return nil
}

// runDailyDigests 处理所有存档天数（仅 day < today；当天留给次日归档，避免过早归档半成品）。
// 成功清理该群当天数据；失败 attempts+1 保留，≥3 丢弃并 error 日志；未配 Key 的群过期数据丢弃。
func (m *Module) runDailyDigests(ctx context.Context) {
	today := beijingDay(m.now().Unix())
	pending, err := m.chatDays().PendingDays(ctx, today)
	if err != nil {
		m.appLogErr(ctx, "wechat.chatlog.list_failed", "聊天归档待处理清单读取失败", err, nil)
		return
	}
	for _, g := range pending {
		rule := m.digestRule(ctx, g.GroupID)
		if rule == nil {
			m.appLog(ctx, "warn", "business", "wechat.chatlog.no_rule",
				fmt.Sprintf("%s %s 未配置聊天总结 Key，过期数据已丢弃", g.Day, g.GroupName), "",
				map[string]any{"day": g.Day, "group_id": g.GroupID})
			_ = m.chatDays().DiscardDay(ctx, g.Day, g.GroupID)
			continue
		}
		if err := m.archiveGroupDay(ctx, g, *rule); err != nil {
			attempts, discarded, ferr := m.chatDays().FinishDay(ctx, g.Day, g.GroupID, false, err.Error())
			if ferr == nil && discarded {
				m.appLogErr(ctx, "wechat.chatlog.dropped",
					fmt.Sprintf("%s %s 归档连续失败 %d 次，丢弃当天数据", g.Day, g.GroupName, attempts), err,
					map[string]any{"day": g.Day, "group_id": g.GroupID})
			} else {
				m.appLogErr(ctx, "wechat.chatlog.failed",
					fmt.Sprintf("%s %s 归档失败（第 %d 次），保留待补跑", g.Day, g.GroupName, attempts), err,
					map[string]any{"day": g.Day, "group_id": g.GroupID})
			}
			continue
		}
		if _, _, ferr := m.chatDays().FinishDay(ctx, g.Day, g.GroupID, true, ""); ferr != nil {
			m.appLogErr(ctx, "wechat.chatlog.cleanup_failed", "归档完成后清理当日数据失败", ferr,
				map[string]any{"day": g.Day, "group_id": g.GroupID})
		}
	}
}

// runDigestLoop 归档巡检：启动先补跑存量，此后每 30 分钟处理前一天及更早数据。
func (m *Module) runDigestLoop(ctx context.Context) {
	m.runDailyDigests(ctx)
	ticker := time.NewTicker(digestInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.runDailyDigests(ctx)
		}
	}
}
