// 回放测试：撤回（不同/同 sender 时间线、撤回人不明）、图片辅证路径、字母杆号初筛与区段。
// 以 fake 件串行重放群消息序列，断言绝不误删非撤回人记录、普通群图不误挂、字母杆号不被剥离。
package wechatanswer

import (
	"context"
	"testing"
)

// 回放：不同 sender——李四撤回自己的消息（名下无记录）时，张三的记录绝不能被删。
func TestReplayRecallDifferentSenderKeepsOthersRecord(t *testing.T) {
	env := newTestEnv(t, treeRule())
	env.dify.outputs["tree-extract"] = map[string]any{"is_tree_hazard": "true", "line_name": "10kV某线"}
	at := env.now.Unix()
	env.m.HandleMessage(context.Background(), textMsg("u1", "msg-rp-1", "12# 杨树超高", at))
	if !env.m.HandleMessage(context.Background(), recallMsg("u2", "sys-rp-1", at+30)) {
		t.Fatal("撤回事件应被消费")
	}
	records := env.state.all()
	if len(records) != 1 {
		t.Fatalf("非撤回人的记录不得删除，现存 %d 条", len(records))
	}
	if records[0].SenderID != "u1" {
		t.Fatalf("被误删/误留的记录归属不符: %s", records[0].SenderID)
	}
	for _, p := range env.sink.payloads() {
		if _, ok := decodePayload(t, p.Payload)["deletes"]; ok {
			t.Fatal("不同 sender 撤回不得推送 deletes 载荷")
		}
	}
}

// 回放：撤回人不明（无 sender、文案无引号名）时不删任何记录。
func TestReplayRecallUnknownRecallerKeepsAll(t *testing.T) {
	env := newTestEnv(t, treeRule())
	env.dify.outputs["tree-extract"] = map[string]any{"is_tree_hazard": "true", "line_name": "10kV某线"}
	at := env.now.Unix()
	env.m.HandleMessage(context.Background(), textMsg("u1", "msg-rp-2", "12# 杨树超高", at))
	recall := ParseMessage(map[string]any{
		"kind": "system", "type": 10002.0, "conversation_id": testGroup, "is_group": true,
		"sender_id": "", "content": "撤回了一条消息", "svr_id": "sys-rp-2",
		"created_at": float64(at + 10), "direction": "incoming",
		"system": map[string]any{"category": "message_recalled"},
	})
	if !env.m.HandleMessage(context.Background(), recall) {
		t.Fatal("撤回事件应被消费")
	}
	if len(env.state.all()) != 1 {
		t.Fatalf("撤回人不明时不得删除任何记录，现存 %d 条", len(env.state.all()))
	}
}

// 回放：暂存补判只作用于同 sender——李四的暂存撤回不得误删张三后到的记录。
func TestReplayRecallStashNotCrossSender(t *testing.T) {
	env := newTestEnv(t, treeRule())
	env.dify.outputs["tree-extract"] = map[string]any{"is_tree_hazard": "true", "line_name": "10kV某线"}
	at := env.now.Unix()
	// 李四先撤回（未命中任何记录 → 暂存），随后张三报送
	env.m.HandleMessage(context.Background(), recallMsg("u2", "sys-rp-3", at+10))
	env.m.HandleMessage(context.Background(), textMsg("u1", "msg-rp-3", "12# 杨树超高", at))
	if len(env.state.all()) != 1 {
		t.Fatalf("跨 sender 的暂存撤回不得误删他人记录，现存 %d 条", len(env.state.all()))
	}
	if len(env.dify.inputs) != 1 {
		t.Fatalf("记录应正常提取，Dify 调用 %d 次", len(env.dify.inputs))
	}
}

// 回放：同 sender 时间线——连续两条记录，撤回只删较新一条。
func TestReplayRecallSameSenderTimelineNewest(t *testing.T) {
	env := newTestEnv(t, treeRule())
	env.dify.outputs["tree-extract"] = map[string]any{"is_tree_hazard": "true", "line_name": "10kV某线"}
	at := env.now.Unix()
	env.m.HandleMessage(context.Background(), textMsg("u1", "msg-rp-4a", "12# 杨树超高", at))
	env.m.HandleMessage(context.Background(), textMsg("u1", "msg-rp-4b", "15# 柳树超高", at+50))
	if !env.m.HandleMessage(context.Background(), recallMsg("u1", "sys-rp-4", at+60)) {
		t.Fatal("撤回事件应被消费")
	}
	records := env.state.all()
	if len(records) != 1 {
		t.Fatalf("撤回应命中较新一条，现存 %d 条", len(records))
	}
	if records[0].MsgID != "msg-rp-4a" {
		t.Fatalf("应保留较早记录，实际 %s", records[0].MsgID)
	}
}

// 回放：同 sender 时间线——记录后发裸图，撤回按「撤回图片」处理（记录保留）。
func TestReplayRecallSameSenderImageNewerCleansImageOnly(t *testing.T) {
	env := newTestEnv(t, treeRule())
	env.dify.outputs["tree-extract"] = map[string]any{"is_tree_hazard": "true", "line_name": "10kV某线"}
	at := env.now.Unix()
	env.m.HandleMessage(context.Background(), textMsg("u1", "msg-rp-5", "12# 杨树超高", at))
	env.m.HandleMessage(context.Background(), imageMsg("u1", "img-rp-5", "file-no-1", at+20, ""))
	if !env.m.HandleMessage(context.Background(), recallMsg("u1", "sys-rp-5", at+30)) {
		t.Fatal("撤回事件应被消费")
	}
	records := env.state.all()
	if len(records) != 1 {
		t.Fatalf("图片撤回不得连带删记录，现存 %d 条", len(records))
	}
	if entry, _ := env.state.Image(context.Background(), "img-rp-5"); entry != nil {
		t.Fatal("较新的未挂接图片应随撤回清理")
	}
}

// 回放：同 sender 时间线——裸图先发、记录后发，撤回按「撤回记录」处理（整条删除）。
func TestReplayRecallSameSenderRecordNewerDeletesRecord(t *testing.T) {
	env := newTestEnv(t, treeRule())
	env.dify.outputs["tree-extract"] = map[string]any{"is_tree_hazard": "true", "line_name": "10kV某线"}
	at := env.now.Unix()
	env.m.HandleMessage(context.Background(), imageMsg("u1", "img-rp-6", "file-no-1", at, ""))
	env.m.HandleMessage(context.Background(), textMsg("u1", "msg-rp-6", "12# 杨树超高", at+20))
	if !env.m.HandleMessage(context.Background(), recallMsg("u1", "sys-rp-6", at+30)) {
		t.Fatal("撤回事件应被消费")
	}
	if len(env.state.all()) != 0 {
		t.Fatalf("较新的记录应随撤回整条删除，现存 %d 条", len(env.state.all()))
	}
	calls := env.sink.payloads()
	del := decodePayload(t, calls[len(calls)-1].Payload)
	deletes, _ := del["deletes"].([]any)
	if len(deletes) != 1 {
		t.Fatalf("deletes 数量 %d", len(deletes))
	}
}

// 回放：辅证仅「先引用图片、再发送记录文字」路径——跨 sender 挂接协作供图后，
// 供图人撤回图片只摘照片，记录不删。
func TestReplayQuoteImageThenRecordPhotoLifecycle(t *testing.T) {
	env := newTestEnv(t, treeRule())
	env.dify.outputs["tree-extract"] = map[string]any{
		"is_tree_hazard": "true", "line_name": "10kV某线", "pole_from": 12.0,
	}
	at := env.now.Unix()
	// 李四发图 → 张三引用图片发记录文字 → 李四撤回图片
	env.m.HandleMessage(context.Background(), imageMsg("u2", "img-rp-7", "file-no-1", at, ""))
	report := quoteImageTextMsg("u1", "msg-rp-7", "img-rp-7", "12# 杨树超高", at+10)
	env.m.HandleMessage(context.Background(), report)
	records := env.state.all()
	if len(records) != 1 {
		t.Fatalf("记录应落库，现存 %d 条", len(records))
	}
	photos, _ := env.state.PhotosByRecord(context.Background(), records[0].Key)
	if len(photos) != 1 {
		t.Fatalf("先引用图片再发记录应挂辅证，实际 %d 张", len(photos))
	}
	if !env.m.HandleMessage(context.Background(), recallMsg("u2", "sys-rp-7", at+20)) {
		t.Fatal("撤回事件应被消费")
	}
	records = env.state.all()
	if len(records) != 1 {
		t.Fatalf("图片撤回只摘照片不删记录，现存 %d 条", len(records))
	}
	photos, _ = env.state.PhotosByRecord(context.Background(), records[0].Key)
	if len(photos) != 0 {
		t.Fatalf("辅证应被摘除，剩余 %d 张", len(photos))
	}
}

// 回放：普通群图不得随意挂到后续记录。
func TestReplayPlainImageNotAttachedToLaterRecord(t *testing.T) {
	env := newTestEnv(t, treeRule())
	env.dify.outputs["tree-extract"] = map[string]any{"is_tree_hazard": "true", "line_name": "10kV某线"}
	at := env.now.Unix()
	env.m.HandleMessage(context.Background(), imageMsg("u2", "img-rp-8", "file-no-1", at, ""))
	env.m.HandleMessage(context.Background(), textMsg("u1", "msg-rp-8", "12# 杨树超高", at+30))
	records := env.state.all()
	if len(records) != 1 {
		t.Fatalf("记录应落库，现存 %d 条", len(records))
	}
	photos, _ := env.state.PhotosByRecord(context.Background(), records[0].Key)
	if len(photos) != 0 {
		t.Fatalf("普通群图不得挂到后续记录，实际 %d 张", len(photos))
	}
	entry, _ := env.state.Image(context.Background(), "img-rp-8")
	if entry == nil || entry.RecordKey != "" {
		t.Fatalf("普通群图应保持未挂接缓存: %+v", entry)
	}
}

// 回放：字母杆号（12A#/15B#，大小写/连字符/区间）过初筛且区段保留字母，数值锚点置空。
func TestReplayLetterPoleGateAndSection(t *testing.T) {
	for _, text := range []string{
		"12A# 杨树超高",
		"12a# 柳树垂距不足",
		"12A#-15B# 杨树超高",
		"12a#-15b# 柳树水平距离2米",
		"12-A# 杨树超高",
	} {
		if !passesCaptureGate(text) {
			t.Fatalf("初筛应放行字母杆号: %q", text)
		}
	}
	if passesCaptureGate("今天天气不错") {
		t.Fatal("无杆号消息不应过初筛")
	}

	env := newTestEnv(t, treeRule())
	env.dify.outputs["tree-extract"] = map[string]any{
		"is_tree_hazard": "true", "line_name": "10kV某线",
		"pole_from": "12A", "pole_to": "15B", "tree_species": "杨树",
	}
	at := env.now.Unix()
	if !env.m.HandleMessage(context.Background(), textMsg("u1", "msg-rp-9", "某线 12A#-15B# 杨树超高", at)) {
		t.Fatal("字母杆号报送应被消费")
	}
	payloads := env.sink.payloads()
	if len(payloads) != 1 {
		t.Fatalf("应上送 1 条: %d", len(payloads))
	}
	rec := decodePayload(t, payloads[0].Payload)["records"].([]any)[0].(map[string]any)
	if rec["section"] != "12A#-15B#" {
		t.Fatalf("区段应保留字母，实际 %v", rec["section"])
	}
	if rec["pole_from"] != nil || rec["pole_to"] != nil {
		t.Fatalf("字母杆号数值锚点应置空，实际 %v / %v", rec["pole_from"], rec["pole_to"])
	}
}

// 回放：交跨初筛同样放行字母杆号，区段不剥离字母。
func TestReplayLetterPoleCrossingGateAndSection(t *testing.T) {
	for _, text := range []string{
		"12A#-15B# 交跨房屋",
		"12a# 交叉跨越10kV线路",
	} {
		if !passesCrossingGate(text) {
			t.Fatalf("交跨初筛应放行字母杆号: %q", text)
		}
	}
	env := newTestEnv(t, treeRule())
	env.dify.outputs["cross-extract"] = map[string]any{
		"is_crossing": "true", "crossing_type": "房屋",
		"pole_from": "12A", "pole_to": "12A",
	}
	at := env.now.Unix()
	if !env.m.HandleMessage(context.Background(), textMsg("u1", "msg-rp-10", "某线 12A# 交跨房屋", at)) {
		t.Fatal("字母杆号交跨报送应被消费")
	}
	payloads := env.sink.payloads()
	if len(payloads) != 1 {
		t.Fatalf("应上送 1 条: %d", len(payloads))
	}
	rec := decodePayload(t, payloads[0].Payload)["records"].([]any)[0].(map[string]any)
	if rec["section"] != "12A#" {
		t.Fatalf("区段应保留字母，实际 %v", rec["section"])
	}
	if rec["pole_from"] != nil || rec["pole_to"] != nil {
		t.Fatalf("字母杆号数值锚点应置空，实际 %v / %v", rec["pole_from"], rec["pole_to"])
	}
}
