// 图片挂接测试：裸图缓存备查、引用图片发文字带辅证、引用记录发图挂辅证并重推、参数缺失回执。
package wechatanswer

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// imageMsg 构造群图片消息（含 CDN 下载参数；fileNo 为空则不带参数）。
func imageMsg(sender, svrID, fileNo string, at int64, referXML string) *Message {
	img := map[string]any{}
	if fileNo != "" {
		img["cdn_download_contexts"] = []any{
			map[string]any{"variant": "standard", "file_no": fileNo, "file_aes_key": "aes-key-1"},
		}
		img["data_len"] = float64(len(testJpeg))
	}
	raw := map[string]any{
		"kind": "image", "conversation_id": testGroup, "is_group": true,
		"sender_id": sender, "svr_id": svrID, "created_at": float64(at),
		"direction": "incoming", "image": img,
	}
	if referXML != "" {
		raw["content"] = referXML
	}
	return ParseMessage(raw)
}

// referXMLFor 生成图片报文 extcommoninfo 简形 refermsg（引用记录发图形态）。
func referXMLFor(quotedSvrID, quotedContent, createtime string) string {
	return "<msg><appmsg><extcommoninfo><refermsg>" +
		"<svrid>" + quotedSvrID + "</svrid><type>1</type>" +
		"<content>" + quotedContent + "</content>" +
		"<createtime>" + createtime + "</createtime>" +
		"</refermsg></extcommoninfo></appmsg></msg>"
}

// quoteImageTextMsg 引用图片发文字（app.quote 形态，msg_type=3 表示被引用是图片）。
func quoteImageTextMsg(sender, svrID, quotedImageSvrID, text string, at int64) *Message {
	return ParseMessage(map[string]any{
		"kind": "text", "conversation_id": testGroup, "is_group": true,
		"sender_id": sender, "content": text, "svr_id": svrID, "created_at": float64(at),
		"direction": "incoming",
		"app": map[string]any{
			"category": "quote", "title": text,
			"reference": map[string]any{
				"svr_id": quotedImageSvrID, "content": "[图片]", "msg_type": 3.0,
			},
		},
	})
}

func TestBareImageCachedWithoutPublish(t *testing.T) {
	env := newTestEnv(t, treeRule())
	at := env.now.Unix()
	if !env.m.HandleMessage(context.Background(), imageMsg("u2", "img-1", "file-no-1", at, "")) {
		t.Fatal("裸图消息应被消费")
	}
	entry, _ := env.state.Image(context.Background(), "img-1")
	if entry == nil || entry.FileNo != "file-no-1" {
		t.Fatalf("图片缓存缺失: %+v", entry)
	}
	if entry.RecordKey != "" {
		t.Fatal("裸图不应挂接记录")
	}
	if len(env.sink.payloads()) != 0 {
		t.Fatal("裸图不应上送")
	}
}

func TestQuoteImageReportAttachesPhoto(t *testing.T) {
	env := newTestEnv(t, treeRule())
	env.dify.outputs["tree-extract"] = map[string]any{
		"is_tree_hazard": "true", "line_name": "10kV某线", "pole_from": 12.0, "pole_to": 15.0,
	}
	at := env.now.Unix()
	// 裸图（李四供图）→ 引用图片发树障文字（张三报送）
	env.m.HandleMessage(context.Background(), imageMsg("u2", "img-2", "file-no-1", at, ""))
	report := quoteImageTextMsg("u1", "msg-photo-1", "img-2", "12#-15# 杨树垂距不足", at+10)
	if !env.m.HandleMessage(context.Background(), report) {
		t.Fatal("引用图片的树障报送应被消费")
	}
	calls := env.sink.payloads()
	if len(calls) != 1 {
		t.Fatalf("sink 调用 %d 次，期望 1 次", len(calls))
	}
	rec := decodePayload(t, calls[0].Payload)["records"].([]any)[0].(map[string]any)
	photos, _ := rec["evidence_photos"].([]any)
	if len(photos) != 1 {
		t.Fatalf("辅证照片 %d 张，期望 1 张", len(photos))
	}
	photo := photos[0].(map[string]any)
	if photo["msg_id"] != "img-2" {
		t.Fatalf("照片 msg_id=%v", photo["msg_id"])
	}
	if img, _ := photo["image"].(string); !strings.HasPrefix(img, "data:image/jpeg;base64,") {
		t.Fatalf("照片 dataURL 形态不符: %.40s", img)
	}
	// 协作供图人：供图人 ≠ 记录者时发现人展示两人
	if rec["finder_name"] != "张三 李四" {
		t.Fatalf("finder_name=%v，期望 张三 李四", rec["finder_name"])
	}
	if len(env.sender.quotes) != 1 || !strings.HasPrefix(env.sender.quotes[0], "已记录") {
		t.Fatalf("回执不符: %v", env.sender.quotes)
	}
	entry, _ := env.state.Image(context.Background(), "img-2")
	if entry == nil || entry.RecordKey == "" {
		t.Fatal("图片应已挂接记录")
	}
}

func TestQuoteRecordWithImageAttachesPhotoAndRepublishes(t *testing.T) {
	env := newTestEnv(t, treeRule())
	env.dify.outputs["tree-extract"] = map[string]any{
		"is_tree_hazard": "true", "line_name": "10kV某线", "pole_from": 12.0,
	}
	at := env.now.Unix()
	// 先报送一条树障记录
	env.m.HandleMessage(context.Background(), textMsg("u1", "msg-rec-1", "12# 杨树超高", at))
	// 李四引用该记录发图（图片 XML 自带 refermsg 简形）→ 挂接辅证
	refer := referXMLFor("msg-rec-1", "12# 杨树超高", "0")
	img := imageMsg("u2", "img-3", "file-no-1", at+20, refer)
	if !env.m.HandleMessage(context.Background(), img) {
		t.Fatal("引用记录发图应被消费")
	}
	calls := env.sink.payloads()
	if len(calls) != 2 {
		t.Fatalf("sink 调用 %d 次，期望 2 次（原始 + 辅证变更重推）", len(calls))
	}
	var last map[string]any
	if err := json.Unmarshal(calls[1].Payload, &last); err != nil {
		t.Fatal(err)
	}
	rec := last["records"].([]any)[0].(map[string]any)
	photos, _ := rec["evidence_photos"].([]any)
	if len(photos) != 1 {
		t.Fatalf("辅证照片 %d 张，期望 1 张", len(photos))
	}
	if rec["crane_record_id"] != decodePayload(t, calls[0].Payload)["records"].([]any)[0].(map[string]any)["crane_record_id"] {
		t.Fatal("重推应保持同一 crane_record_id")
	}
	if len(env.sender.quotes) != 2 || env.sender.quotes[0] != "已记录" || env.sender.quotes[1] != "已添加辅证照片" {
		t.Fatalf("回执不符: %v", env.sender.quotes)
	}
}

func TestImageQuoteMissingCDNParams(t *testing.T) {
	env := newTestEnv(t, treeRule())
	env.dify.outputs["tree-extract"] = map[string]any{"is_tree_hazard": "true", "line_name": "10kV某线"}
	at := env.now.Unix()
	env.m.HandleMessage(context.Background(), textMsg("u1", "msg-rec-2", "12# 杨树超高", at))
	// 无 CDN 参数的图片引用记录发图 → 挂接失败回执，记录保留
	refer := referXMLFor("msg-rec-2", "12# 杨树超高", "0")
	env.m.HandleMessage(context.Background(), imageMsg("u2", "img-4", "", at+20, refer))
	if len(env.sender.quotes) != 2 || env.sender.quotes[1] != "辅证照片下载失败" {
		t.Fatalf("回执不符: %v", env.sender.quotes)
	}
	records := env.state.all()
	if len(records) != 1 {
		t.Fatalf("记录应保留，现存 %d 条", len(records))
	}
	if len(env.sink.payloads()) != 1 {
		t.Fatalf("挂接失败不应重推，sink 调用 %d 次", len(env.sink.payloads()))
	}
}

func TestImageQuoteCreatetimeFallbackLocatesRecord(t *testing.T) {
	env := newTestEnv(t, treeRule())
	env.dify.outputs["tree-extract"] = map[string]any{"is_tree_hazard": "true", "line_name": "10kV某线"}
	at := env.now.Unix()
	env.m.HandleMessage(context.Background(), textMsg("u1", "msg-rec-3", "12# 杨树超高", at))
	// extcommoninfo 简形无 content 标签：按被引用消息创建时间 ±2s 定位
	refer := "<refermsg><svrid>unknown-id</svrid><type>1</type><createtime>" +
		itoa(int(at)) + "</createtime></refermsg>"
	env.m.HandleMessage(context.Background(), imageMsg("u2", "img-5", "file-no-1", at+1, refer))
	if len(env.sender.quotes) != 2 || env.sender.quotes[1] != "已添加辅证照片" {
		t.Fatalf("createtime 兜底定位失败，回执: %v", env.sender.quotes)
	}
}
