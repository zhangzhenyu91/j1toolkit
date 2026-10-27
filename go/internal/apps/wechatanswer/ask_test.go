// 群内提问 WF3 测试：六形态装配、Call Me 出口、WF3 命名与 md 文件回发、失败回执映射。
package wechatanswer

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func askRule() Rule {
	r := treeRule()
	r.AskEnabled = true
	r.AskNameKey = "ask-name-key"
	return r
}

// askEnv 带提问假件的测试环境。
type askEnv struct {
	*testEnv
	ask  *fakeAsk
	docs fakeFiles
}

func newAskEnv(t *testing.T, rules ...Rule) *askEnv {
	t.Helper()
	env := newTestEnv(t, rules...)
	a := &fakeAsk{ans: "这是模型回答内容。"}
	env.m.Ask = a
	env.m.Cfg = fakeCfg{values: map[string]string{"WECHATPADPRO_BOT_WXID": "bot"}}
	return &askEnv{testEnv: env, ask: a}
}

// botMentionMsg 带 @监听号 的群消息（at 列表形态，参考 crane 真实报文键名）。
func botMentionMsg(sender, svrID, content string, at int64) *Message {
	return ParseMessage(map[string]any{
		"kind": "text", "conversation_id": testGroup, "is_group": true,
		"sender_id": sender, "content": content, "svr_id": svrID,
		"created_at": float64(at), "direction": "incoming",
		"at_user_ids": []any{"bot"},
	})
}

func TestGroupAskTextForm(t *testing.T) {
	env := newAskEnv(t, askRule())
	env.dify.outputs["ask-name-key"] = map[string]any{"result": map[string]any{"filename": "线路缺陷答疑"}}
	env.m.AskSync = true

	msg := botMentionMsg("u1", "ask-msg-1", "@监听号 什么是垂距", env.now.Unix())
	consumed := env.m.HandleMessage(context.Background(), msg)
	if !consumed {
		t.Fatal("@提问消息应被消费（不进聊天归档）")
	}
	if len(env.ask.reqs) != 1 {
		t.Fatalf("应调用一次 Call Me，实际 %d", len(env.ask.reqs))
	}
	req := env.ask.reqs[0]
	if req.Wxid != "u1" || !strings.Contains(req.Query, "什么是垂距") || req.SessionTitle != "检修一班群内提问" {
		t.Fatalf("提问载荷不符: %+v", req)
	}
	files := env.sender.sentFiles()
	if len(files) != 1 {
		t.Fatalf("应回发 1 个 md 文件，实际 %d", len(files))
	}
	md := string(files[0].Data)
	if files[0].FileName != "线路缺陷答疑.md" || !strings.Contains(md, "这是模型回答内容。") || !strings.Contains(md, "提问人：张三") {
		t.Fatalf("回答 md 不符: %s / %s", files[0].FileName, md)
	}
}

func TestGroupAskQuoteTextForm(t *testing.T) {
	env := newAskEnv(t, askRule())
	env.m.AskSync = true

	// 引用文字提问（refermsg 真实 XML 形态）
	msg := ParseMessage(map[string]any{
		"kind": "app", "conversation_id": testGroup, "is_group": true,
		"sender_id": "u1", "svr_id": "ask-msg-2", "created_at": float64(env.now.Unix()),
		"content": "@监听号 这是什么缺陷\n<msg><appmsg><title>@监听号 这是什么缺陷</title><type>57</type>" +
			"<refermsg><type>1</type><svrid>rec-src-1</svrid><content>12# 杨树超高</content><createtime>1761520000</createtime></refermsg>" +
			"</appmsg></msg>",
		"at_user_ids": []any{"bot"},
	})
	env.m.HandleMessage(context.Background(), msg)
	if len(env.ask.reqs) != 1 {
		t.Fatalf("应调用一次 Call Me，实际 %d", len(env.ask.reqs))
	}
	q := env.ask.reqs[0].Query
	if !strings.Contains(q, "12# 杨树超高") {
		t.Fatalf("引用文字应并入提问: %q", q)
	}
}

func TestGroupAskErrorReply(t *testing.T) {
	env := newAskEnv(t, askRule())
	env.m.AskSync = true
	env.ask.err = &AskError{Code: "user_not_found", Message: "未绑定"}

	msg := botMentionMsg("u1", "ask-msg-3", "@监听号 提问", env.now.Unix())
	env.m.HandleMessage(context.Background(), msg)
	if len(env.sender.quotes) == 0 || !strings.Contains(env.sender.quotes[len(env.sender.quotes)-1], "尚未绑定壹匣账号") {
		t.Fatalf("失败回执不符: %v", env.sender.quotes)
	}
}

func TestGroupAskDisabledSilent(t *testing.T) {
	env := newAskEnv(t, treeRule()) // 未开提问
	env.m.AskSync = true
	msg := botMentionMsg("u1", "ask-msg-4", "@监听号 提问", env.now.Unix())
	if !env.m.HandleMessage(context.Background(), msg) {
		t.Fatal("@消息即使未启用提问也应消费")
	}
	if len(env.ask.reqs) != 0 {
		t.Fatal("未启用提问不应调用 Call Me")
	}
}

func TestGroupAskFallbackFilename(t *testing.T) {
	env := newAskEnv(t, askRule())
	env.m.AskSync = true
	env.dify.err = errors.New("WF3 不可用")

	msg := botMentionMsg("u1", "ask-msg-5", "@监听号 提问", env.now.Unix())
	env.m.HandleMessage(context.Background(), msg)
	files := env.sender.sentFiles()
	if len(files) != 1 || !strings.HasPrefix(files[0].FileName, "群内问答-") {
		t.Fatalf("WF3 失败应使用兜底文件名: %+v", files)
	}
}
