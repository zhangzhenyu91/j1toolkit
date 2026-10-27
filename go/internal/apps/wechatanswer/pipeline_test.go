// 管线语义测试：交跨消息语义（双漏斗分流/终审/未配 Key）与未消费归档、位置触发。
package wechatanswer

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

const testGroup = "123@chatroom"

// testEnv 测试装配：全注入假件，不连网不连库。
type testEnv struct {
	m        *Module
	state    *memState
	queue    *memQueue
	sink     *fakeSink
	dify     *fakeDify
	sender   *fakeSender
	archiver *fakeArchiver
	images   staticImages
	now      time.Time
}

var testJpeg = []byte{0xff, 0xd8, 0xff, 0xe0, 0x00, 0x10, 0x4a, 0x46, 0x49, 0x46}

func newTestEnv(t *testing.T, rules ...Rule) *testEnv {
	t.Helper()
	env := &testEnv{
		state:    newMemState(),
		queue:    newMemQueue(),
		sink:     &fakeSink{},
		dify:     &fakeDify{outputs: map[string]map[string]any{}},
		sender:   &fakeSender{},
		archiver: &fakeArchiver{},
		images:   staticImages{data: map[string][]byte{"file-no-1": testJpeg}},
		now:      time.Date(2026, 10, 27, 10, 0, 0, 0, time.FixedZone("CST", 8*3600)),
	}
	m := New(nil, nil)
	m.ServerDir = t.TempDir() // 辅证照片只写临时目录，不污染仓库
	m.State = env.state
	m.Queue = env.queue
	m.Sink = env.sink
	m.Dify = env.dify
	m.Sender = env.sender
	m.Archiver = env.archiver
	m.Images = env.images
	m.Names = staticNames{names: map[string]string{"u1": "张三", "u2": "李四", "bot": "监听号"}}
	m.Rules = staticRules{byGroup: map[string][]Rule{testGroup: rules}}
	m.Now = func() time.Time { return env.now }
	env.m = m
	return env
}

func treeRule() Rule {
	return Rule{
		ID: 1, GroupID: testGroup, GroupName: "吊车群", Trigger: TriggerTreeWatch, Enabled: true,
		ExtractKey: "tree-extract", CorrectKey: "tree-correct", CrossingKey: "cross-extract",
		CrossingCorrectKey: "cross-correct", TeamName: "检修一班",
	}
}

// textMsg 构造群文本消息。
func textMsg(sender, svrID, content string, at int64) *Message {
	return ParseMessage(map[string]any{
		"kind": "text", "conversation_id": testGroup, "is_group": true,
		"sender_id": sender, "content": content, "svr_id": svrID,
		"created_at": float64(at), "direction": "incoming",
	})
}

// decodePayload 解析 sink 载荷。
func decodePayload(t *testing.T, raw []byte) map[string]any {
	t.Helper()
	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("载荷不是合法 JSON: %v", err)
	}
	return body
}

func TestCrossingGateSemantics(t *testing.T) {
	cases := []struct {
		text     string
		crossing bool
		tree     bool
	}{
		{"1#-3# 10kV交跨房屋", true, false},
		{"交叉跨越房屋", false, false},
		{"3号杆交跨10kV线路", true, false},
		{"12#-15# 垂距不足", false, true},
		{"杨树 水平距离2米", false, true},
		{"12# 交叉跨越", true, false},
		{"12#-15# 交叉 棵树超高", true, true},
		{"今天天气不错", false, false},
		{"交跨 5#-8# 跨越低压线", true, false},
	}
	for _, c := range cases {
		if got := passesCrossingGate(c.text); got != c.crossing {
			t.Errorf("passesCrossingGate(%q)=%v，期望 %v", c.text, got, c.crossing)
		}
		if got := passesCaptureGate(c.text); got != c.tree {
			t.Errorf("passesCaptureGate(%q)=%v，期望 %v", c.text, got, c.tree)
		}
	}
}

func TestCrossingCaptureSinkPayload(t *testing.T) {
	env := newTestEnv(t, treeRule())
	env.dify.outputs["cross-extract"] = map[string]any{
		"is_crossing":    "true",
		"line_name":      "10kV某线",
		"pole_from":      1.0,
		"pole_to":        3.0,
		"crossing_type":  "房屋",
		"cross_vertical": "8",
		"has_hazard":     "否",
		"remark":         "现场核查",
	}
	at := env.now.Unix()
	msg := textMsg("u1", "msg-cross-1", "1#-3# 10kV线路交跨房屋，跨越高度8米", at)
	if !env.m.HandleMessage(context.Background(), msg) {
		t.Fatal("交跨报送应被消费")
	}
	calls := env.sink.payloads()
	if len(calls) != 1 {
		t.Fatalf("sink 调用 %d 次，期望 1 次", len(calls))
	}
	if calls[0].Kind != KindCrossing {
		t.Fatalf("kind=%q，期望 %q", calls[0].Kind, KindCrossing)
	}
	body := decodePayload(t, calls[0].Payload)
	if body["team_name"] != "检修一班" || body["mode"] != "direct" {
		t.Fatalf("载荷外壳不符: %v", body)
	}
	wantSession := testGroup + "_" + beijingDay(at) + "_crossing"
	if body["session_id"] != wantSession {
		t.Fatalf("session_id=%v，期望 %s", body["session_id"], wantSession)
	}
	recs, _ := body["records"].([]any)
	if len(recs) != 1 {
		t.Fatalf("records 数量 %d", len(recs))
	}
	rec := recs[0].(map[string]any)
	if rec["crossing_type"] != "房屋" || rec["inspect_at"] != beijingDay(at) {
		t.Fatalf("交跨字段不符: %v", rec)
	}
	if rec["section"] != "1#-3#" {
		t.Fatalf("section=%v", rec["section"])
	}
	if rec["crane_record_id"] == "" {
		t.Fatal("crane_record_id 为空")
	}
	if _, hasPhotos := rec["evidence_photos"]; hasPhotos {
		t.Fatal("交跨载荷不应带辅证照片")
	}
	// 提取 Key 必须是交跨独立密钥
	if len(env.dify.inputs) != 1 || env.dify.inputs[0]["key"] != "cross-extract" {
		t.Fatalf("Dify 调用不符: %v", env.dify.inputs)
	}
	if len(env.sender.quotes) != 1 || !strings.HasPrefix(env.sender.quotes[0], "已记录") {
		t.Fatalf("回执不符: %v", env.sender.quotes)
	}
	if len(env.archiver.msgs) != 0 {
		t.Fatal("被消费消息不应进聊天归档")
	}
}

func TestCrossingRejectedByDify(t *testing.T) {
	env := newTestEnv(t, treeRule())
	env.dify.outputs["cross-extract"] = map[string]any{"is_crossing": "false"}
	msg := textMsg("u1", "msg-cross-2", "1#-3# 交跨房屋", env.now.Unix())
	if !env.m.HandleMessage(context.Background(), msg) {
		t.Fatal("应被消费（终审拒绝也算消费）")
	}
	if len(env.sink.payloads()) != 0 {
		t.Fatal("终审拒绝不应上送")
	}
	if got := env.state.all(); len(got) != 0 {
		t.Fatalf("终审拒绝不应留记录，现存 %d 条", len(got))
	}
}

func TestCrossingVerdictVariants(t *testing.T) {
	for _, v := range []any{false, 0.0, "否", "不是", "false", "NO", "0"} {
		if verdictAccept(v) {
			t.Errorf("verdictAccept(%v) 应为否", v)
		}
	}
	for _, v := range []any{nil, true, 1.0, "是", "true", "疑似"} {
		if !verdictAccept(v) {
			t.Errorf("verdictAccept(%v) 应为是", v)
		}
	}
}

func TestCrossingGateWithoutKeyConsumedSilently(t *testing.T) {
	rule := treeRule()
	rule.CrossingKey = ""
	env := newTestEnv(t, rule)
	msg := textMsg("u1", "msg-cross-3", "1#-3# 交跨房屋", env.now.Unix())
	if !env.m.HandleMessage(context.Background(), msg) {
		t.Fatal("未配交跨 Key 的交跨报送算消费，不进兜底")
	}
	if len(env.sink.payloads()) != 0 || len(env.dify.inputs) != 0 {
		t.Fatal("未配 Key 不应提取/上送")
	}
	if len(env.archiver.msgs) != 0 {
		t.Fatal("不应进聊天归档")
	}
}

func TestCrossingGateBeatsTreeGate(t *testing.T) {
	env := newTestEnv(t, treeRule())
	env.dify.outputs["cross-extract"] = map[string]any{"is_crossing": "true", "crossing_type": "树木"}
	env.dify.outputs["tree-extract"] = map[string]any{"is_tree_hazard": "true", "tree_species": "杨树"}
	msg := textMsg("u1", "msg-cross-4", "1#-3# 交跨 树木垂距不足", env.now.Unix())
	if !env.m.HandleMessage(context.Background(), msg) {
		t.Fatal("应被消费")
	}
	calls := env.sink.payloads()
	if len(calls) != 1 || calls[0].Kind != KindCrossing {
		t.Fatalf("双闸同时命中应走交跨，实际 %v", calls)
	}
	if len(env.dify.inputs) != 1 || env.dify.inputs[0]["key"] != "cross-extract" {
		t.Fatalf("应只调交跨提取 Key: %v", env.dify.inputs)
	}
}

func TestTreeCaptureStillWorks(t *testing.T) {
	env := newTestEnv(t, treeRule())
	env.dify.outputs["tree-extract"] = map[string]any{
		"is_tree_hazard": "true", "line_name": "10kV某线",
		"pole_from": 12.0, "pole_to": 15.0, "tree_species": "杨树", "tree_count": "3",
		"vertical": "不足", "over_height": "", "distance": "2", "side": "small",
	}
	msg := textMsg("u1", "msg-tree-1", "12#-15# 杨树垂距不足", env.now.Unix())
	if !env.m.HandleMessage(context.Background(), msg) {
		t.Fatal("应被消费")
	}
	calls := env.sink.payloads()
	if len(calls) != 1 || calls[0].Kind != KindTree {
		t.Fatalf("树障上送不符: %v", calls)
	}
	rec := decodePayload(t, calls[0].Payload)["records"].([]any)[0].(map[string]any)
	if rec["tree_species"] != "杨树" || rec["section"] != "12#-15#" {
		t.Fatalf("树障字段不符: %v", rec)
	}
	if rec["dist_small"] != "2" || rec["dist_big"] != "" {
		t.Fatalf("侧别距离不符: %v", rec)
	}
}

func TestUnconsumedMessageArchived(t *testing.T) {
	env := newTestEnv(t, treeRule())
	msg := textMsg("u1", "msg-chat-1", "今天天气不错", env.now.Unix())
	if env.m.HandleMessage(context.Background(), msg) {
		t.Fatal("闲聊不应被消费")
	}
	if len(env.archiver.msgs) != 1 {
		t.Fatalf("归档 %d 条，期望 1 条", len(env.archiver.msgs))
	}
	got := env.archiver.msgs[0]
	if got.GroupName != "吊车群" || got.SenderName != "张三" || got.Day != "2026-10-27" {
		t.Fatalf("归档项不符: %+v", got)
	}
}

func TestMentionCommandsForbiddenButConsumed(t *testing.T) {
	env := newTestEnv(t, treeRule())
	env.m.Cfg = fakeCfg{values: map[string]string{"WECHATPADPRO_BOT_WXID": "bot"}}
	msg := ParseMessage(map[string]any{
		"kind": "text", "conversation_id": testGroup, "is_group": true,
		"sender_id": "u1", "content": "@监听号 汇总", "svr_id": "msg-cmd-1",
		"created_at": float64(env.now.Unix()), "direction": "incoming",
		"mention_list": []any{"bot"},
	})
	if !env.m.HandleMessage(context.Background(), msg) {
		t.Fatal("@指令应被消费")
	}
	if len(env.sink.payloads()) != 0 {
		t.Fatal("严禁汇总/报送：不应有任何上送")
	}
	if len(env.archiver.msgs) != 0 {
		t.Fatal("@指令不进兜底归档")
	}
}

func TestLocationTrigger(t *testing.T) {
	rule := Rule{ID: 2, GroupID: testGroup, GroupName: "吊车群", Trigger: TriggerLocation, Enabled: true, ExtractKey: "loc-key"}
	env := newTestEnv(t, treeRule(), rule)
	msg := ParseMessage(map[string]any{
		"kind": "location", "conversation_id": testGroup, "is_group": true,
		"sender_id": "u1", "svr_id": "msg-loc-1", "created_at": float64(env.now.Unix()),
		"location": map[string]any{"longitude": 116.4, "latitude": 39.9, "label": "现场"},
	})
	if !env.m.HandleMessage(context.Background(), msg) {
		t.Fatal("位置消息应被 location 规则消费")
	}
	if len(env.dify.inputs) != 1 || env.dify.inputs[0]["key"] != "loc-key" {
		t.Fatalf("位置提取调用不符: %v", env.dify.inputs)
	}
}

func TestQuoteCorrectAppliesPatchesAndRepublishes(t *testing.T) {
	env := newTestEnv(t, treeRule())
	env.dify.outputs["tree-extract"] = map[string]any{
		"is_tree_hazard": "true", "line_name": "10kV某线", "pole_from": 12.0, "tree_species": "杨树",
	}
	env.dify.outputs["tree-correct"] = map[string]any{
		"action": "correct", "patches": map[string]any{"tree_species": "榆树"},
	}
	at := env.now.Unix()
	env.m.HandleMessage(context.Background(), textMsg("u1", "msg-q-1", "12# 杨树超高", at))
	// 引用更正
	quoteMsg := ParseMessage(map[string]any{
		"kind": "text", "conversation_id": testGroup, "is_group": true,
		"sender_id": "u1", "content": "改成榆树", "svr_id": "msg-q-2", "created_at": float64(at + 30),
		"app": map[string]any{
			"category": "quote",
			"title":    "改成榆树",
			"reference": map[string]any{
				"svr_id": "msg-q-1", "content": "12# 杨树超高", "msg_type": 1.0,
			},
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
	if rec["tree_species"] != "榆树" {
		t.Fatalf("更正未生效: %v", rec)
	}
	if len(env.sender.quotes) != 2 || env.sender.quotes[1] != "已更正" {
		t.Fatalf("更正回执不符: %v", env.sender.quotes)
	}
}
