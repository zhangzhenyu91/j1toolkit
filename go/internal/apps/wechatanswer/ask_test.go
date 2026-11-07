// 群内提问测试：六形态装配、Call Me 出口、Markdown 转纯文本/长答分条、失败回执映射。
package wechatanswer

import (
	"context"
	"strings"
	"testing"
	"unicode/utf8"
)

func askRule() Rule {
	r := treeRule()
	r.AskEnabled = true
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
	// 首条回答引用提问锚定；短回答不分条。
	if len(env.sender.quotes) != 2 || env.sender.quotes[1] != "这是模型回答内容。" {
		t.Fatalf("回答文字回执不符: %v", env.sender.quotes)
	}
	if len(env.sender.texts) != 0 {
		t.Fatalf("短回答不应额外分条: %v", env.sender.texts)
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

func TestGroupAskWeChatMentionSeparator(t *testing.T) {
	// 微信 @ 提及在昵称后插入 U+2005（四分之一全角空格）：剥离后必须保留真正的问题。
	env := newAskEnv(t, askRule())
	env.m.AskSync = true

	msg := botMentionMsg("u1", "ask-msg-sep", "@监听号\u2005什么是垂距", env.now.Unix())
	if !env.m.HandleMessage(context.Background(), msg) {
		t.Fatal("@提问消息应被消费")
	}
	if len(env.ask.reqs) != 1 {
		t.Fatalf("应调用一次 Call Me，实际 %d", len(env.ask.reqs))
	}
	if q := env.ask.reqs[0].Query; q != "什么是垂距" {
		t.Fatalf("提问应剔除 @ 提及，实际 %q", q)
	}
}

func TestGroupAskMarkdownTextReply(t *testing.T) {
	env := newAskEnv(t, askRule())
	env.m.AskSync = true
	env.ask.ans = "## 结论\n\n**垂距不足**，建议砍伐。\n\n- 12# 杨树超高\n- 15# 榆树\n\n" +
		"参考：[规程原文](https://example.com/doc)\n\n| 杆号 | 垂距 |\n| --- | --- |\n| 12# | 1.2m |"

	msg := botMentionMsg("u1", "ask-msg-md", "@监听号 怎么处理", env.now.Unix())
	env.m.HandleMessage(context.Background(), msg)
	if len(env.sender.quotes) < 2 {
		t.Fatalf("应回发文字回答: %v", env.sender.quotes)
	}
	reply := env.sender.quotes[len(env.sender.quotes)-1]
	for _, want := range []string{
		"【结论】", "垂距不足，建议砍伐。", "• 12# 杨树超高", "• 15# 榆树",
		"规程原文（https://example.com/doc）", "杆号 ｜ 垂距", "12# ｜ 1.2m",
	} {
		if !strings.Contains(reply, want) {
			t.Fatalf("文字回答缺少 %q：%q", want, reply)
		}
	}
	for _, bad := range []string{"##", "**", "| ---", "](https"} {
		if strings.Contains(reply, bad) {
			t.Fatalf("文字回答残留 Markdown %q：%q", bad, reply)
		}
	}
}

func TestGroupAskLongAnswerSplit(t *testing.T) {
	env := newAskEnv(t, askRule())
	env.m.AskSync = true
	// 远超单条上限：应按行切成多条，且每条不超过上限（末条截断提示允许略超）。
	env.ask.ans = strings.Repeat("这是很长的回答段落，包含足够多的文字内容。\n", wechatTextLimit/10)

	msg := botMentionMsg("u1", "ask-msg-long", "@监听号 详细说明", env.now.Unix())
	env.m.HandleMessage(context.Background(), msg)
	all := append(append([]string(nil), env.sender.quotes...), env.sender.texts...)
	// quotes[0] 是「正在思考」，回答至少 2 条。
	if len(all) < 3 {
		t.Fatalf("长回答应分条发送，实际 %d 条", len(all))
	}
	for _, s := range all[1:] {
		if n := utf8.RuneCountInString(s); n > wechatTextLimit+60 {
			t.Fatalf("单条回答过长（%d rune）: %q", n, s)
		}
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
