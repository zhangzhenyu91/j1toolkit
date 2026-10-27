// 聊天日志 WF4 每日总结归档测试：纪要生成、文档推送、失败 attempts 与丢弃口径。
package wechatanswer

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// digestEnv 带归档假件的测试环境。
type digestEnv struct {
	*testEnv
	days    *memChatDayStore
	chatlog *fakeChatlog
	files   fakeFiles
}

func newDigestEnv(t *testing.T) *digestEnv {
	t.Helper()
	rule := treeRule()
	rule.ChatDigestKey = "digest-key"
	env := newTestEnv(t, rule)
	days := newMemChatDayStore()
	cl := &fakeChatlog{}
	env.m.ChatDays = days
	env.m.Chatlog = cl
	env.dify.outputs["digest-key"] = map[string]any{"summary": "今日完成巡视，无异常。"}
	return &digestEnv{testEnv: env, days: days, chatlog: cl}
}

func TestDailyDigestArchivesAndCleans(t *testing.T) {
	env := newDigestEnv(t)
	ctx := context.Background()
	env.days.addMessage(ChatMessage{Day: "2026-10-26", GroupID: testGroup, GroupName: "吊车群",
		SvrID: "m1", SenderID: "u1", SenderName: "张三", Content: "今天风有点大", Time: 1761450000})
	env.days.addDocument(ChatDocument{Day: "2026-10-26", GroupID: testGroup, GroupName: "吊车群",
		SvrID: "doc-1", SenderID: "u1", SenderName: "张三", FileName: "巡视记录.docx", Time: 1761450100})
	// 文档下载参数缓存（fileStore 同口径）
	_ = env.m.mediaCache().PutMedia(ctx, &MediaEntry{SvrID: "doc-1", Kind: "file", FileName: "巡视记录.docx", AttachID: "a-1", DataLen: 3})
	env.m.Files = fakeFiles{data: map[string][]byte{"a-1": []byte("doc")}}

	env.m.runDailyDigests(ctx)

	if len(env.chatlog.items) != 2 {
		t.Fatalf("应推送纪要+1 文档，实际 %d: %+v", len(env.chatlog.items), env.chatlog.items)
	}
	digest := env.chatlog.items[0]
	if digest.Kind != "digest" || digest.Date != "2026-10-26" || digest.GroupName != "吊车群" {
		t.Fatalf("纪要载荷不符: %+v", digest)
	}
	if !strings.Contains(digest.DigestMD, "# 吊车群 2026-10-26 聊天纪要") ||
		!strings.Contains(digest.DigestMD, "今日完成巡视，无异常。") ||
		!strings.Contains(digest.DigestMD, "原始消息 1 条 / 归档文档 1 个：巡视记录.docx") {
		t.Fatalf("纪要内容不符: %s", digest.DigestMD)
	}
	if env.chatlog.items[1].Kind != "file" || env.chatlog.items[1].File.Name != "巡视记录.docx" {
		t.Fatalf("文档载荷不符: %+v", env.chatlog.items[1])
	}
	// 成功后当日数据清理
	msgs, _ := env.days.DayMessages(ctx, "2026-10-26", testGroup)
	if len(msgs) != 0 {
		t.Fatal("归档成功后应清理当日数据")
	}
}

func TestDailyDigestInputTruncation(t *testing.T) {
	var msgs []ChatMessage
	for i := 0; i < 500; i++ {
		msgs = append(msgs, ChatMessage{SenderName: "张三", Content: "收到", Time: 1761450000})
	}
	input := buildDigestInput(msgs)
	lines := strings.Split(input, "\n")
	if len(lines) != digestMaxMessages+1 {
		t.Fatalf("截断后行数不符: %d", len(lines))
	}
	if !strings.Contains(lines[len(lines)-1], "其余 100 条从略") {
		t.Fatalf("截断提示缺失: %q", lines[len(lines)-1])
	}
}

func TestDailyDigestFailureAttemptsThenDrop(t *testing.T) {
	env := newDigestEnv(t)
	ctx := context.Background()
	env.chatlog.err = errors.New("推送失败")
	env.days.addMessage(ChatMessage{Day: "2026-10-26", GroupID: testGroup, GroupName: "吊车群",
		SvrID: "m1", SenderID: "u1", Content: "x", Time: 1761450000})

	for i := 1; i <= 3; i++ {
		env.m.runDailyDigests(ctx)
		pending, _ := env.days.PendingDays(ctx, "2026-10-27")
		if i < 3 && len(pending) == 0 {
			t.Fatalf("第 %d 次失败应保留待补跑", i)
		}
	}
	pending, _ := env.days.PendingDays(ctx, "2026-10-27")
	if len(pending) != 0 {
		t.Fatalf("连续失败 3 次应丢弃: %+v", pending)
	}
}

func TestDailyDigestNoRuleDiscards(t *testing.T) {
	env := newDigestEnv(t)
	ctx := context.Background()
	// 该群规则无 chat_digest Key
	env.m.Rules = staticRules{byGroup: map[string][]Rule{testGroup: {treeRule()}}}
	env.days.addMessage(ChatMessage{Day: "2026-10-26", GroupID: testGroup, GroupName: "吊车群",
		SvrID: "m1", SenderID: "u1", Content: "x", Time: 1761450000})
	env.m.runDailyDigests(ctx)
	pending, _ := env.days.PendingDays(ctx, "2026-10-27")
	if len(pending) != 0 {
		t.Fatal("未配总结 Key 的过期数据应丢弃")
	}
	if len(env.chatlog.items) != 0 {
		t.Fatal("未配总结 Key 不应推送")
	}
}
