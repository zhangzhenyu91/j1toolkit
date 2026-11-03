package wechatanswer

// 引用更正改线路名称：与首次录入同口径匹配线路表（含罗马数字归一/12 写法），
// 命中取规范线路名并补电压，再重推载荷。关联别名匹配由 linecatalog/teamdata 测试覆盖。

import (
	"context"
	"testing"
)

func TestQuoteCorrectCanonicalizesLineName(t *testing.T) {
	rule := treeRule()
	env := newTestEnv(t, rule)
	env.m.Lines = staticLines{lines: []TeamLine{{Voltage: "110", Name: "峰义ⅠⅡ线"}}}
	env.dify.outputs["tree-extract"] = map[string]any{
		"is_tree_hazard": "true", "line_name": "甲线", "pole_from": 12.0, "tree_species": "杨树",
	}
	env.dify.outputs["tree-correct"] = map[string]any{
		"action": "correct", "patches": map[string]any{"line_name": "峰义12线"},
	}
	at := env.now.Unix()
	if !env.m.HandleMessage(context.Background(), textMsg("u1", "msg-line-1", "12# 杨树超高", at)) {
		t.Fatal("原始消息应被消费")
	}
	quoteMsg := ParseMessage(map[string]any{
		"kind": "text", "conversation_id": testGroup, "is_group": true,
		"sender_id": "u1", "content": "线路改成峰义12线", "svr_id": "msg-line-2", "created_at": float64(at + 30),
		"app": map[string]any{
			"category":  "quote",
			"title":     "线路改成峰义12线",
			"reference": map[string]any{"svr_id": "msg-line-1", "content": "12# 杨树超高", "msg_type": 1.0},
		},
	})
	if !env.m.HandleMessage(context.Background(), quoteMsg) {
		t.Fatal("更正消息应被消费")
	}
	calls := env.sink.payloads()
	if len(calls) != 2 {
		t.Fatalf("sink 调用 %d 次，期望 2 次（原始 + 更正重推）", len(calls))
	}
	rec := decodePayload(t, calls[1].Payload)["records"].([]any)[0].(map[string]any)
	if rec["line_name"] != "峰义ⅠⅡ线" || rec["voltage_level"] != "110" {
		t.Fatalf("更正线路名应归一为 峰义ⅠⅡ线/110，实际 %v / %v", rec["line_name"], rec["voltage_level"])
	}
}
