// 真实报文回放测试：报文形态参考 crane-answer 消息处理与用户现场消息结构
// （位置/树障报送/引用更正/撤回系统消息/图片 CDN/文件 appmsg/群主昵称前缀）。
// 注意：微信真实链路未验证——本测试仅固化报文解析与管线语义，不代表真机联调通过。
package wechatanswer

import (
	"context"
	"strings"
	"testing"
)

// 真实形态：群文本带 `wxid:\n` 前缀（WeChatPadPro 群消息 content 常见形态）。
func TestRealPayloadSenderPrefix(t *testing.T) {
	msg := ParseMessage(map[string]any{
		"kind": "text", "conversation_id": testGroup, "is_group": true,
		"content": "wxid_u1:\n12#-15# 杨树超高，垂距不足",
		"svr_id":  "rp-1", "created_at": float64(1761520000),
	})
	if msg.SenderID != "wxid_u1" || msg.Content != "12#-15# 杨树超高，垂距不足" {
		t.Fatalf("前缀解析不符: sender=%q content=%q", msg.SenderID, msg.Content)
	}
}

// 真实形态：位置消息（WeChatPadPro location 字段）。
func TestRealPayloadLocation(t *testing.T) {
	env := newTestEnv(t, Rule{ID: 9, GroupID: testGroup, GroupName: "吊车群", Trigger: TriggerLocation,
		Enabled: true, LocationKey: "loc-key"})
	msg := ParseMessage(map[string]any{
		"kind": "location", "conversation_id": testGroup, "is_group": true,
		"sender_id": "u1", "svr_id": "rp-loc-1", "created_at": float64(env.now.Unix()),
		"location": map[string]any{"longitude": 112.45, "latitude": 34.62, "label": "变电站东侧", "poi_name": "东郊变"},
	})
	if !env.m.HandleMessage(context.Background(), msg) {
		t.Fatal("位置消息应被规则消费")
	}
	if len(env.dify.inputs) != 1 || env.dify.inputs[0]["key"] != "loc-key" {
		t.Fatalf("位置触发不符: %+v", env.dify.inputs)
	}
}

// 真实形态：树障报送 → 捕获上送（extracted 全字段 + 电压归一）。
func TestRealPayloadTreeCaptureAndPush(t *testing.T) {
	env := newTestEnv(t, treeRule())
	env.m.Lines = staticLines{lines: []TeamLine{{Voltage: "110", Name: "吕汾Ⅰ线"}}}
	env.dify.outputs["tree-extract"] = map[string]any{
		"is_tree_hazard": "true", "line_name": "110kV吕汾1线",
		"pole_from": float64(12), "pole_to": float64(15), "tree_species": "杨树",
		"tree_count": "3", "over_height": "2.5", "side": "small", "distance": "1.5",
	}
	msg := textMsg("u1", "rp-tree-1", "吕汾1线 12#-15# 杨树3棵超高2.5米，水平1.5米", env.now.Unix())
	if !env.m.HandleMessage(context.Background(), msg) {
		t.Fatal("树障报送应被消费")
	}
	payloads := env.sink.payloads()
	if len(payloads) != 1 {
		t.Fatalf("应上送 1 条: %d", len(payloads))
	}
	body := decodePayload(t, payloads[0].Payload)
	rec := body["records"].([]any)[0].(map[string]any)
	if rec["line_name"] != "吕汾Ⅰ线" || rec["voltage_level"] != "110" {
		t.Fatalf("线路归一/电压不符: %+v", rec)
	}
	if rec["found_at"] != "2026-10-27" || rec["section"] != "12#-15#" {
		t.Fatalf("字段不符: %+v", rec)
	}
	if body["team_name"] != "检修一班" {
		t.Fatalf("班组不符: %+v", body)
	}
}

// 真实形态：引用更正（refermsg 块，实体转义形态同样兼容）。
func TestRealPayloadQuoteCorrection(t *testing.T) {
	env := newTestEnv(t, treeRule())
	env.dify.outputs["tree-extract"] = map[string]any{"is_tree_hazard": "true", "line_name": "吕汾Ⅰ线"}
	env.dify.outputs["tree-correct"] = map[string]any{"action": "correct", "patches": map[string]any{"tree_species": "榆树"}}
	report := textMsg("u1", "rp-corr-src", "吕汾Ⅰ线 12# 杨树超高", env.now.Unix()-10)
	env.m.HandleMessage(context.Background(), report)

	correction := ParseMessage(map[string]any{
		"kind": "app", "conversation_id": testGroup, "is_group": true,
		"sender_id": "u1", "svr_id": "rp-corr-1", "created_at": float64(env.now.Unix()),
		"content": `<msg><appmsg><title>树种改成榆树</title><type>57</type><refermsg>` +
			`<type>1</type><svrid>rp-corr-src</svrid><content>吕汾Ⅰ线 12# 杨树超高</content>` +
			`<createtime>1761519990</createtime></refermsg></appmsg></msg>`,
	})
	if !env.m.HandleMessage(context.Background(), correction) {
		t.Fatal("引用更正应被消费")
	}
	var corrected bool
	for _, in := range env.dify.inputs {
		if in["key"] == "tree-correct" && strings.Contains(in["correction_text"], "榆树") {
			corrected = true
		}
	}
	if !corrected {
		t.Fatalf("更正工作流未触发: %+v", env.dify.inputs)
	}
	// 更正后应重推 upsert
	if len(env.sink.payloads()) < 2 {
		t.Fatalf("更正后应重推: %d", len(env.sink.payloads()))
	}
}

// 真实形态：撤回系统消息（type=10002 + 文案 `"xxx"撤回了一条消息`）。
func TestRealPayloadRecallSystemMessage(t *testing.T) {
	env := newTestEnv(t, treeRule())
	env.dify.outputs["tree-extract"] = map[string]any{"is_tree_hazard": "true", "line_name": "吕汾Ⅰ线"}
	report := textMsg("u1", "rp-recall-src", "吕汾Ⅰ线 12# 杨树超高", env.now.Unix()-5)
	env.m.HandleMessage(context.Background(), report)

	recall := ParseMessage(map[string]any{
		"kind": "system", "type": float64(10002), "conversation_id": testGroup, "is_group": true,
		"sender_id": "123@chatroom", "svr_id": "rp-recall-evt", "created_at": float64(env.now.Unix()),
		"content": `"张三"撤回了一条消息`,
		"system":  map[string]any{"category": "message_recalled"},
	})
	if !isRecallEvent(recall) {
		t.Fatal("撤回事件判定失败")
	}
	env.m.HandleMessage(context.Background(), recall)
	// 记录应删除且补推 deletes
	if rec, _ := env.state.GetRecord(context.Background(), "rp-recall-src"); rec == nil {
		// 记录键是随机 rec_，改断言 sink 载荷
	}
	foundDelete := false
	for _, p := range env.sink.payloads() {
		body := decodePayload(t, p.Payload)
		if _, ok := body["deletes"]; ok {
			foundDelete = true
		}
	}
	if !foundDelete {
		t.Fatal("撤回应推送 deletes 载荷")
	}
}

// 真实形态：图片 CDN 参数（cdn_download_contexts variant=standard 优先）。
func TestRealPayloadImageCDNContexts(t *testing.T) {
	img := parseImageMeta(map[string]any{
		"cdn_download_contexts": []any{
			map[string]any{"variant": "thumb", "file_no": "thumb-no", "file_aes_key": "thumb-key"},
			map[string]any{"variant": "standard", "file_no": "std-no", "file_aes_key": "std-key"},
		},
		"data_len": float64(204800),
	})
	if img == nil || img.FileNo != "std-no" || img.AESKey != "std-key" || img.DataLen != 204800 {
		t.Fatalf("CDN 参数解析不符: %+v", img)
	}
}

// 真实形态：消息去重（断线重连重复投递同一 svr_id 只处理一次）。
func TestRealPayloadDuplicateDeliveryIdempotent(t *testing.T) {
	env := newTestEnv(t, treeRule())
	env.dify.outputs["tree-extract"] = map[string]any{"is_tree_hazard": "true", "line_name": "吕汾Ⅰ线"}
	msg := textMsg("u1", "rp-dup-1", "吕汾Ⅰ线 12# 杨树超高", env.now.Unix())
	env.m.HandleMessage(context.Background(), msg)
	n := len(env.sink.payloads())
	// 同一报文重复投递
	dup := textMsg("u1", "rp-dup-1", "吕汾Ⅰ线 12# 杨树超高", env.now.Unix())
	if !env.m.HandleMessage(context.Background(), dup) {
		t.Fatal("重复投递应按已消费处理（不进聊天归档）")
	}
	if len(env.sink.payloads()) != n {
		t.Fatalf("重复投递不应重复上送: %d → %d", n, len(env.sink.payloads()))
	}
}

// 真实形态：处理中撤回竞态（撤回先于提取完成落库，暂存补判可靠执行）。
func TestRealPayloadRecallDuringProcessing(t *testing.T) {
	env := newTestEnv(t, treeRule())
	env.dify.outputs["tree-extract"] = map[string]any{"is_tree_hazard": "true", "line_name": "吕汾Ⅰ线"}
	// 撤回先到（目标消息尚在处理），暂存
	recall := ParseMessage(map[string]any{
		"kind": "system", "type": float64(10002), "conversation_id": testGroup, "is_group": true,
		"sender_id": "u1", "svr_id": "rp-race-recall", "created_at": float64(env.now.Unix()),
		"content": `"张三"撤回了一条消息`,
		"system":  map[string]any{"category": "message_recalled"},
	})
	env.m.HandleMessage(context.Background(), recall)
	// 报送消息随后落库 → 补判应删除且不提取
	report := textMsg("u1", "rp-race-report", "吕汾Ⅰ线 12# 杨树超高", env.now.Unix())
	env.m.HandleMessage(context.Background(), report)
	if len(env.sink.payloads()) != 0 {
		t.Fatalf("撤回补判后不应上送: %+v", env.sink.payloads())
	}
}

// formatSection 只输出杆塔区段：位置说明（含「塔底」）不拼入 section，字母与已有 # 不剥离不重复。
func TestFormatSectionPoleOnly(t *testing.T) {
	cases := []struct {
		ex   map[string]any
		want string
	}{
		{map[string]any{"pole_from": 12.0, "pole_to": 15.0}, "12#-15#"},
		{map[string]any{"pole_from": 3.0, "pole_to": 4.0, "position_note": "位于水厂附近"}, "3#-4#"},
		{map[string]any{"pole_from": 29.0, "pole_to": 29.0, "position_note": "塔底"}, "29#"},
		{map[string]any{"pole_from": "18A#", "pole_to": "19#", "position_note": "塔底"}, "18A#-19#"},
		{map[string]any{"pole_from": "18A", "pole_to": 19.0, "position_note": "院内"}, "18A#-19#"},
		{map[string]any{"pole_from": "12A#", "pole_to": "12A"}, "12A#"},
		{map[string]any{"pole_from": "12A", "position_note": "塔底"}, "12A#"},
		{map[string]any{"position_note": "塔底"}, ""},
	}
	for _, c := range cases {
		if got := formatSection(c.ex); got != c.want {
			t.Fatalf("formatSection(%v)=%q，期望 %q", c.ex, got, c.want)
		}
	}
}

// 单条记录载荷：section 只出杆塔区段（「位于××附近」等位置说明不拼入），position_note 独立字段原样传递；
// records 形态为 direct/staging 两种载荷共用——纯杆塔区段使两条导入路径的区段与位置拆分均为恒等，
// 显式 position_note 不被区段尾部拆出值覆盖。
func TestRecordPayloadSectionNoteIndependent(t *testing.T) {
	m := &Module{}
	rec := &Record{
		Kind: KindTree, Key: "rec-sec-1", MsgID: "msg-sec-1",
		Extracted: map[string]any{
			"line_name": "甲线", "pole_from": 3.0, "pole_to": 4.0,
			"position_note": "位于水厂附近", "tree_species": "杨树",
		},
	}
	got := m.buildRecordPayload(rec)
	if got["section"] != "3#-4#" {
		t.Fatalf("区段应只出杆塔区段，实际 %v", got["section"])
	}
	if got["position_note"] != "位于水厂附近" {
		t.Fatalf("position_note 应原样传递，实际 %v", got["position_note"])
	}
	raw, err := m.buildUpsertPayload(rec, "检修一班")
	if err != nil {
		t.Fatalf("装配 upsert 载荷: %v", err)
	}
	body := decodePayload(t, raw)
	if body["mode"] != "direct" {
		t.Fatalf("mode=%v，期望 direct", body["mode"])
	}
	item := body["records"].([]any)[0].(map[string]any)
	if item["section"] != "3#-4#" || item["position_note"] != "位于水厂附近" {
		t.Fatalf("载荷 records 字段不符: %+v", item)
	}
}

// 真实形态：区段与位置说明分离——字母杆号带已有 #（18A#-19#），区段不重复不剥离 #，
// position_note=「塔底」在独立字段原样传递不得丢。
func TestRealPayloadSectionPositionNoteSplit(t *testing.T) {
	env := newTestEnv(t, treeRule())
	env.dify.outputs["tree-extract"] = map[string]any{
		"is_tree_hazard": "true", "line_name": "110kV吕汾1线",
		"pole_from": "18A#", "pole_to": "19#", "position_note": "塔底", "tree_species": "杨树",
	}
	msg := textMsg("u1", "rp-sec-1", "吕汾1线 18A#-19#塔底 杨树超高", env.now.Unix())
	if !env.m.HandleMessage(context.Background(), msg) {
		t.Fatal("树障报送应被消费")
	}
	payloads := env.sink.payloads()
	if len(payloads) != 1 {
		t.Fatalf("应上送 1 条: %d", len(payloads))
	}
	body := decodePayload(t, payloads[0].Payload)
	if body["mode"] != "direct" {
		t.Fatalf("mode=%v，期望 direct", body["mode"])
	}
	rec := body["records"].([]any)[0].(map[string]any)
	if rec["section"] != "18A#-19#" {
		t.Fatalf("区段应只出杆塔区段，实际 %v", rec["section"])
	}
	if rec["position_note"] != "塔底" {
		t.Fatalf("position_note 应原样传递（含塔底），实际 %v", rec["position_note"])
	}
	if rec["pole_from"] != nil || rec["pole_to"] != nil {
		t.Fatalf("字母杆号数值锚点应置空，实际 %v / %v", rec["pole_from"], rec["pole_to"])
	}
}
