// 撤回测试：整条删除（先推删除后本地删）、撤到图片仅摘辅证、撤回先于落库的竞态补判、未命中暂存。
package wechatanswer

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// recallMsg 构造撤回系统消息。
func recallMsg(sender, svrID string, at int64) *Message {
	return ParseMessage(map[string]any{
		"kind": "system", "type": 10002.0, "conversation_id": testGroup, "is_group": true,
		"sender_id": sender, "content": "撤回了一条消息", "svr_id": svrID,
		"created_at": float64(at), "direction": "incoming",
		"system": map[string]any{"category": "message_recalled"},
	})
}

func TestRecallDeletesRecordAndPushesDelete(t *testing.T) {
	env := newTestEnv(t, treeRule())
	env.dify.outputs["tree-extract"] = map[string]any{
		"is_tree_hazard": "true", "line_name": "10kV某线", "pole_from": 12.0,
	}
	at := env.now.Unix()
	env.m.HandleMessage(context.Background(), textMsg("u1", "msg-rm-1", "12# 杨树超高", at))
	records := env.state.all()
	if len(records) != 1 {
		t.Fatalf("报送后应有 1 条记录，实际 %d", len(records))
	}
	recordKey := records[0].Key

	if !env.m.HandleMessage(context.Background(), recallMsg("u1", "sys-rm-1", at+30)) {
		t.Fatal("撤回事件应被消费")
	}
	calls := env.sink.payloads()
	if len(calls) != 2 {
		t.Fatalf("sink 调用 %d 次，期望 2 次（upsert + delete）", len(calls))
	}
	del := decodePayload(t, calls[1].Payload)
	deletes, _ := del["deletes"].([]any)
	if len(deletes) != 1 {
		t.Fatalf("deletes 数量 %d", len(deletes))
	}
	if deletes[0].(map[string]any)["crane_record_id"] != recordKey {
		t.Fatalf("删除载荷 crane_record_id=%v，期望 %s", deletes[0].(map[string]any)["crane_record_id"], recordKey)
	}
	if del["team_name"] != "检修一班" {
		t.Fatalf("删除载荷 team_name=%v", del["team_name"])
	}
	if len(env.state.all()) != 0 {
		t.Fatal("本地记录应已删除")
	}
	if len(env.sender.texts) != 1 || env.sender.texts[0] != "已删除" {
		t.Fatalf("回执不符: %v", env.sender.texts)
	}
}

func TestRecallImageRemovesPhotoOnly(t *testing.T) {
	env := newTestEnv(t, treeRule())
	env.dify.outputs["tree-extract"] = map[string]any{
		"is_tree_hazard": "true", "line_name": "10kV某线", "pole_from": 12.0,
	}
	at := env.now.Unix()
	// 李四发图 → 张三「先引用图片、再发送记录文字」挂辅证 → 李四撤回图片
	env.m.HandleMessage(context.Background(), imageMsg("u2", "img-rm-1", "file-no-1", at, ""))
	report := quoteImageTextMsg("u1", "msg-rm-2", "img-rm-1", "12# 杨树超高", at+10)
	env.m.HandleMessage(context.Background(), report)
	if len(env.sink.payloads()) != 1 {
		t.Fatalf("记录带辅证一次性上送，sink 调用 %d 次", len(env.sink.payloads()))
	}

	if !env.m.HandleMessage(context.Background(), recallMsg("u2", "sys-rm-2", at+20)) {
		t.Fatal("撤回事件应被消费")
	}
	calls := env.sink.payloads()
	if len(calls) != 2 {
		t.Fatalf("sink 调用 %d 次，期望 2 次（辅证变更重推）", len(calls))
	}
	rec := decodePayload(t, calls[1].Payload)["records"].([]any)[0].(map[string]any)
	photos, _ := rec["evidence_photos"].([]any)
	if len(photos) != 0 {
		t.Fatalf("辅证应被摘除，剩余 %d 张", len(photos))
	}
	// 记录保留，协作供图人随照片移除失效
	records := env.state.all()
	if len(records) != 1 {
		t.Fatalf("记录应保留，现存 %d 条", len(records))
	}
	if records[0].CoSenderID != "" {
		t.Fatalf("协作供图人应清空，实际 %q", records[0].CoSenderID)
	}
	if len(env.sender.texts) != 1 || env.sender.texts[0] != "已删除辅证照片" {
		t.Fatalf("回执不符: %v", env.sender.texts)
	}
}

func TestRecallBeforeRecordPopsAndSkipsDify(t *testing.T) {
	env := newTestEnv(t, treeRule())
	env.dify.outputs["tree-extract"] = map[string]any{"is_tree_hazard": "true", "line_name": "10kV某线"}
	at := env.now.Unix()
	// 撤回事件先于报送消息到达（乱序投递）：暂存 → 报送落库时补判删除，不再提取
	if !env.m.HandleMessage(context.Background(), recallMsg("u1", "sys-rm-3", at+10)) {
		t.Fatal("撤回事件应被消费")
	}
	msg := textMsg("u1", "msg-rm-3", "12# 杨树超高", at)
	if !env.m.HandleMessage(context.Background(), msg) {
		t.Fatal("报送消息应被消费")
	}
	if len(env.dify.inputs) != 0 {
		t.Fatalf("补判命中后不应调用 Dify，实际 %v", env.dify.inputs)
	}
	if len(env.state.all()) != 0 {
		t.Fatal("补判命中的记录应已删除")
	}
	if len(env.sink.payloads()) != 0 {
		t.Fatal("不应上送")
	}
	if len(env.sender.texts) != 1 || env.sender.texts[0] != "已删除" {
		t.Fatalf("回执不符: %v", env.sender.texts)
	}
}

func TestRecallStashDirectionGuard(t *testing.T) {
	env := newTestEnv(t, treeRule())
	env.dify.outputs["tree-extract"] = map[string]any{"is_tree_hazard": "true", "line_name": "10kV某线"}
	at := env.now.Unix()
	// 先撤回闲聊（时间早于报送）再报送：方向性条件（撤回时间 ≥ 报送时间）防止误删新记录
	env.m.HandleMessage(context.Background(), recallMsg("u1", "sys-rm-4", at-60))
	env.m.HandleMessage(context.Background(), textMsg("u1", "msg-rm-4", "12# 杨树超高", at))
	if len(env.state.all()) != 1 {
		t.Fatalf("新报送记录不应被早期撤回误删，现存 %d 条", len(env.state.all()))
	}
	if len(env.dify.inputs) != 1 {
		t.Fatalf("应正常提取，Dify 调用 %d 次", len(env.dify.inputs))
	}
}

func TestRecallNoTargetStashed(t *testing.T) {
	env := newTestEnv(t, treeRule())
	at := env.now.Unix()
	if !env.m.HandleMessage(context.Background(), recallMsg("u1", "sys-rm-5", at)) {
		t.Fatal("撤回事件应被消费")
	}
	if len(env.sink.payloads()) != 0 || len(env.state.all()) != 0 {
		t.Fatal("未命中记录的撤回不应有任何动作")
	}
}

func TestRecallImageCacheOnlySilentCleanup(t *testing.T) {
	env := newTestEnv(t, treeRule())
	at := env.now.Unix()
	env.m.HandleMessage(context.Background(), imageMsg("u2", "img-rm-2", "file-no-1", at, ""))
	if !env.m.HandleMessage(context.Background(), recallMsg("u2", "sys-rm-6", at+10)) {
		t.Fatal("撤回事件应被消费")
	}
	if entry, _ := env.state.Image(context.Background(), "img-rm-2"); entry != nil {
		t.Fatal("未挂接图片应被静默清理")
	}
	if len(env.sender.texts) != 0 {
		t.Fatalf("静默清理不应发回执: %v", env.sender.texts)
	}
}

func TestRecallDeletePayloadUsesCrossingKind(t *testing.T) {
	env := newTestEnv(t, treeRule())
	env.dify.outputs["cross-extract"] = map[string]any{"is_crossing": "true", "crossing_type": "房屋"}
	at := env.now.Unix()
	env.m.HandleMessage(context.Background(), textMsg("u1", "msg-rm-5", "1#-3# 交跨房屋", at))
	env.m.HandleMessage(context.Background(), recallMsg("u1", "sys-rm-7", at+15))
	calls := env.sink.payloads()
	if len(calls) != 2 {
		t.Fatalf("sink 调用 %d 次，期望 2 次", len(calls))
	}
	if calls[0].Kind != KindCrossing || calls[1].Kind != KindCrossing {
		t.Fatalf("交跨删除应走 crossing 分派: %v", []string{calls[0].Kind, calls[1].Kind})
	}
	var del map[string]any
	if err := json.Unmarshal(calls[1].Payload, &del); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(del["session_id"].(string), "_crossing") {
		t.Fatalf("删除载荷 session_id=%v", del["session_id"])
	}
}
