// 图片挂接测试：裸图缓存备查、仅「先引用图片、再发送记录文字」挂辅证；
// 「引用记录文字、再发图片」按无法识别处理（不挂接、不误删），普通群图不挂后续记录。
package wechatanswer

import (
	"context"
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

func TestQuoteRecordImageCachedNotAttached(t *testing.T) {
	env := newTestEnv(t, treeRule())
	env.dify.outputs["tree-extract"] = map[string]any{
		"is_tree_hazard": "true", "line_name": "10kV某线", "pole_from": 12.0,
	}
	at := env.now.Unix()
	// 先报送一条树障记录
	env.m.HandleMessage(context.Background(), textMsg("u1", "msg-rec-1", "12# 杨树超高", at))
	// 李四引用该记录发图 → 按已证实无法识别处理：仅缓存，不挂接、不误删、不重推
	refer := referXMLFor("msg-rec-1", "12# 杨树超高", "0")
	img := imageMsg("u2", "img-3", "file-no-1", at+20, refer)
	if !env.m.HandleMessage(context.Background(), img) {
		t.Fatal("引用记录发图应被消费")
	}
	if len(env.sink.payloads()) != 1 {
		t.Fatalf("引用记录发图不应重推，sink 调用 %d 次", len(env.sink.payloads()))
	}
	entry, _ := env.state.Image(context.Background(), "img-3")
	if entry == nil {
		t.Fatal("图片应缓存备查")
	}
	if entry.RecordKey != "" {
		t.Fatalf("引用记录发图不得挂接记录，实际挂到 %s", entry.RecordKey)
	}
	records := env.state.all()
	if len(records) != 1 {
		t.Fatalf("记录应保留，现存 %d 条", len(records))
	}
	photos, _ := env.state.PhotosByRecord(context.Background(), records[0].Key)
	if len(photos) != 0 {
		t.Fatalf("不得误挂辅证照片，实际 %d 张", len(photos))
	}
	if len(env.sender.quotes) != 1 || env.sender.quotes[0] != "已记录" {
		t.Fatalf("回执不符: %v", env.sender.quotes)
	}
}

func TestImageQuoteMissingCDNParamsNotAttached(t *testing.T) {
	env := newTestEnv(t, treeRule())
	env.dify.outputs["tree-extract"] = map[string]any{"is_tree_hazard": "true", "line_name": "10kV某线"}
	at := env.now.Unix()
	env.m.HandleMessage(context.Background(), textMsg("u1", "msg-rec-2", "12# 杨树超高", at))
	// 无 CDN 参数的图片（含引用形态）同样不挂接、不误删
	refer := referXMLFor("msg-rec-2", "12# 杨树超高", "0")
	env.m.HandleMessage(context.Background(), imageMsg("u2", "img-4", "", at+20, refer))
	if len(env.sender.quotes) != 1 || env.sender.quotes[0] != "已记录" {
		t.Fatalf("回执不符: %v", env.sender.quotes)
	}
	records := env.state.all()
	if len(records) != 1 {
		t.Fatalf("记录应保留，现存 %d 条", len(records))
	}
	photos, _ := env.state.PhotosByRecord(context.Background(), records[0].Key)
	if len(photos) != 0 {
		t.Fatalf("不得误挂辅证照片，实际 %d 张", len(photos))
	}
	if len(env.sink.payloads()) != 1 {
		t.Fatalf("不应重推，sink 调用 %d 次", len(env.sink.payloads()))
	}
}

func TestImageQuoteCreatetimeNoFallbackAttach(t *testing.T) {
	env := newTestEnv(t, treeRule())
	env.dify.outputs["tree-extract"] = map[string]any{"is_tree_hazard": "true", "line_name": "10kV某线"}
	at := env.now.Unix()
	env.m.HandleMessage(context.Background(), textMsg("u1", "msg-rec-3", "12# 杨树超高", at))
	// extcommoninfo 简形 createtime 贴近记录也不得挂接（普通群图不随意挂到记录）
	refer := "<refermsg><svrid>unknown-id</svrid><type>1</type><createtime>" +
		itoa(int(at)) + "</createtime></refermsg>"
	env.m.HandleMessage(context.Background(), imageMsg("u2", "img-5", "file-no-1", at+1, refer))
	entry, _ := env.state.Image(context.Background(), "img-5")
	if entry == nil {
		t.Fatal("图片应缓存备查")
	}
	if entry.RecordKey != "" {
		t.Fatalf("createtime 兜底不得挂接记录，实际挂到 %s", entry.RecordKey)
	}
	records := env.state.all()
	if len(records) != 1 {
		t.Fatalf("记录应保留，现存 %d 条", len(records))
	}
	photos, _ := env.state.PhotosByRecord(context.Background(), records[0].Key)
	if len(photos) != 0 {
		t.Fatalf("不得误挂辅证照片，实际 %d 张", len(photos))
	}
}
