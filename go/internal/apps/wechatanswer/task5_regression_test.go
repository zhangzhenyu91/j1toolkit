// Task 5 数据损失回归：未加载照片不得以空数组清除辅证、更正重推保留既有照片、
// 补推队列按 record_key 合并且删除抑制旧 upsert。
package wechatanswer

import (
	"context"
	"testing"
)

// TestPayloadOmitsEvidencePhotosWhenUnloaded 从存储读出的记录（照片未装配）载荷必须省略
// evidence_photos，而不是输出空数组让 treehazard 当作权威清除。
func TestPayloadOmitsEvidencePhotosWhenUnloaded(t *testing.T) {
	env := newTestEnv(t, treeRule())
	rec := &Record{
		Key: "rec-unloaded", Kind: KindTree, GroupID: testGroup, SenderID: "u1",
		CapturedAt: env.now.Unix(), Status: "active",
		Extracted: map[string]any{"line_name": "10kV某线", "pole_from": 12.0},
	}
	body := env.m.buildRecordPayload(rec)
	if _, has := body["evidence_photos"]; has {
		t.Fatalf("照片未加载时不得输出 evidence_photos（当前 %#v）", body["evidence_photos"])
	}
}

// TestCorrectionKeepsExistingEvidencePhotos 文字更正不含照片键时，重推前必须加载现有照片，
// 载荷仍携带原辅证（不得以空数组清除）。
func TestCorrectionKeepsExistingEvidencePhotos(t *testing.T) {
	env := newTestEnv(t, treeRule())
	env.dify.outputs["tree-extract"] = map[string]any{
		"is_tree_hazard": "true", "line_name": "10kV某线", "pole_from": 12.0,
	}
	env.dify.outputs["tree-correct"] = map[string]any{
		"action": "correct", "patches": map[string]any{"tree_species": "榆树"},
	}
	at := env.now.Unix()
	env.m.HandleMessage(context.Background(), imageMsg("u2", "img-t5-1", "file-no-1", at, ""))
	env.m.HandleMessage(context.Background(), quoteImageTextMsg("u1", "msg-t5-1", "img-t5-1", "12# 杨树超高", at+10))
	calls := env.sink.payloads()
	if len(calls) != 1 {
		t.Fatalf("首次报送应上送一次，实际 %d", len(calls))
	}
	first := decodePayload(t, calls[0].Payload)["records"].([]any)[0].(map[string]any)
	if photos, _ := first["evidence_photos"].([]any); len(photos) != 1 {
		t.Fatalf("首次报送应带 1 张辅证，实际 %#v", first["evidence_photos"])
	}
	// 真实 SQL 存储的 FindTodayRecord/GetRecord 不装配照片（照片另表）；内存假件会带 Photos，
	// 这里清掉以复现“更正时照片未加载”的真实路径。
	env.state.mu.Lock()
	for _, stored := range env.state.records {
		stored.Photos = nil
	}
	env.state.mu.Unlock()

	quoteMsg := ParseMessage(map[string]any{
		"kind": "text", "conversation_id": testGroup, "is_group": true,
		"sender_id": "u1", "content": "改成榆树", "svr_id": "msg-t5-2", "created_at": float64(at + 30),
		"app": map[string]any{
			"category": "quote", "title": "改成榆树",
			"reference": map[string]any{
				"svr_id": "msg-t5-1", "content": "12# 杨树超高", "msg_type": 1.0,
			},
		},
	})
	if !env.m.HandleMessage(context.Background(), quoteMsg) {
		t.Fatal("更正消息应被消费")
	}
	calls = env.sink.payloads()
	if len(calls) != 2 {
		t.Fatalf("更正应重推一次，sink 调用 %d 次", len(calls))
	}
	rec := decodePayload(t, calls[1].Payload)["records"].([]any)[0].(map[string]any)
	photos, _ := rec["evidence_photos"].([]any)
	if len(photos) != 1 || photos[0].(map[string]any)["msg_id"] != "img-t5-1" {
		t.Fatalf("更正重推必须保留原辅证照片，实际 %#v", rec["evidence_photos"])
	}
	if rec["tree_species"] != "榆树" {
		t.Fatalf("更正未生效: %v", rec["tree_species"])
	}
}

// TestPushQueueMergesUpsertsByRecordKey 同一记录连续上送失败只保留最新一条待补推载荷。
func TestPushQueueMergesUpsertsByRecordKey(t *testing.T) {
	env := newTestEnv(t, treeRule())
	env.dify.outputs["tree-extract"] = map[string]any{
		"is_tree_hazard": "true", "line_name": "10kV某线", "pole_from": 12.0, "tree_species": "杨树",
	}
	env.dify.outputs["tree-correct"] = map[string]any{
		"action": "correct", "patches": map[string]any{"tree_species": "榆树"},
	}
	env.sink.sticky = 500
	at := env.now.Unix()
	env.m.HandleMessage(context.Background(), textMsg("u1", "msg-t5-q1", "12# 杨树超高", at))
	quoteMsg := ParseMessage(map[string]any{
		"kind": "text", "conversation_id": testGroup, "is_group": true,
		"sender_id": "u1", "content": "改成榆树", "svr_id": "msg-t5-q2", "created_at": float64(at + 30),
		"app": map[string]any{
			"category": "quote", "title": "改成榆树",
			"reference": map[string]any{
				"svr_id": "msg-t5-q1", "content": "12# 杨树超高", "msg_type": 1.0,
			},
		},
	})
	env.m.HandleMessage(context.Background(), quoteMsg)
	if pending := env.queue.pending(); pending != 1 {
		t.Fatalf("同一记录补推应按 record_key 合并为 1 条，实际 %d", pending)
	}

	env.sink.sticky = 0
	if _, err := env.m.processQueueOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	calls := env.sink.payloads()
	if len(calls) != 3 {
		t.Fatalf("sink 调用 %d 次（原始+更正+补推），期望 3", len(calls))
	}
	delivered := decodePayload(t, calls[2].Payload)["records"].([]any)[0].(map[string]any)
	if delivered["tree_species"] != "榆树" {
		t.Fatalf("补推载荷应为最新更正结果，实际 %v", delivered["tree_species"])
	}
}

// TestPushQueueDeleteSuppressesUpsert 删除事件入队时抑制同一记录尚未投递的 upsert，
// 避免补推把已删除记录重新建回。
func TestPushQueueDeleteSuppressesUpsert(t *testing.T) {
	env := newTestEnv(t, treeRule())
	env.dify.outputs["tree-extract"] = map[string]any{
		"is_tree_hazard": "true", "line_name": "10kV某线", "pole_from": 12.0,
	}
	env.sink.sticky = 500
	at := env.now.Unix()
	env.m.HandleMessage(context.Background(), textMsg("u1", "msg-t5-d1", "12# 杨树超高", at))
	if pending := env.queue.pending(); pending != 1 {
		t.Fatalf("首次上送失败应入队 1 条，实际 %d", pending)
	}
	env.m.HandleMessage(context.Background(), recallMsg("u1", "sys-t5-d1", at+30))
	if pending := env.queue.pending(); pending != 1 {
		t.Fatalf("删除入队应抑制旧 upsert，待补推应为 1 条，实际 %d", pending)
	}

	env.sink.sticky = 0
	if _, err := env.m.processQueueOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	calls := env.sink.payloads()
	if len(calls) != 3 {
		t.Fatalf("sink 调用 %d 次（报送+删除+补推），期望 3", len(calls))
	}
	last := decodePayload(t, calls[2].Payload)
	if _, has := last["deletes"]; !has {
		t.Fatalf("补推最后一条必须是删除事件，实际 %v", last)
	}
	if _, has := last["records"]; has {
		t.Fatalf("补推不得再发送被删除记录的 upsert: %v", last)
	}
}
