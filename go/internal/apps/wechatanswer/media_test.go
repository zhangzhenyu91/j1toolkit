// 媒体下载参数解析与缓存测试（移植 fileStore.js 口径的真实报文形态）。
package wechatanswer

import (
	"context"
	"testing"
)

// 真实文件报文形态（appmsg type=6 + appattach；字段形态待真机首验，按 crane 兼容解析）。
const fileAppMsgXML = `<msg><appmsg appid="" sdkver="0"><title>巡视记录.docx</title><type>6</type>` +
	`<appattach><totallen>102400</totallen><attachid>@cdn_abc123</attachid><fileext>docx</fileext></appattach>` +
	`</appmsg><fromusername>wxid_sender1</fromusername></msg>`

func TestParseMediaParamsFile(t *testing.T) {
	e := ParseMediaParams(fileAppMsgXML)
	if e == nil || e.Kind != "file" {
		t.Fatalf("文件报文解析失败: %+v", e)
	}
	if e.FileName != "巡视记录.docx" || e.AttachID != "@cdn_abc123" || e.DataLen != 102400 {
		t.Fatalf("字段不符: %+v", e)
	}
	if e.FromUserName != "wxid_sender1" {
		t.Fatalf("from_user_name 解析失败: %+v", e)
	}
}

func TestParseMediaParamsVideoVoice(t *testing.T) {
	v := ParseMediaParams(`<msg><videomsg msgid="12345" newmsgid="67890" length="204800" /></msg>`)
	if v == nil || v.Kind != "video" || v.DataLen != 204800 {
		t.Fatalf("视频报文解析失败: %+v", v)
	}
	voice := ParseMediaParams(`<msg><voicemsg msgid="111" master_buf_id="buf-9" length="3300" /></msg>`)
	if voice == nil || voice.Kind != "voice" || voice.MasterBufID != "buf-9" || voice.Length != 3300 {
		t.Fatalf("语音报文解析失败: %+v", voice)
	}
	if ParseMediaParams("普通文本") != nil {
		t.Fatal("非媒体报文应返回 nil")
	}
}

func TestCacheMediaRoundTrip(t *testing.T) {
	env := newTestEnv(t, treeRule())
	m := env.m
	msg := ParseMessage(map[string]any{
		"kind": "file", "conversation_id": testGroup, "is_group": true,
		"sender_id": "u1", "svr_id": "file-msg-1", "created_at": float64(env.now.Unix()),
		"content": fileAppMsgXML,
	})
	entry := m.cacheMedia(context.Background(), msg, "u1")
	if entry == nil || entry.Kind != "file" || entry.AttachID != "@cdn_abc123" {
		t.Fatalf("缓存失败: %+v", entry)
	}
	got := m.cachedMedia(context.Background(), "file-msg-1")
	if got == nil || got.FileName != "巡视记录.docx" {
		t.Fatalf("回读失败: %+v", got)
	}
}

func TestRedactRawMasksSecrets(t *testing.T) {
	raw := map[string]any{
		"token":        "secret-token-value",
		"file_aes_key": "abcdef0123456789",
		"content":      "12# 杨树超高",
		"nested":       map[string]any{"access_token": "zzz-secret-9999", "ok": true},
	}
	out := RedactRaw(raw)
	if out["token"] == "secret-token-value" {
		t.Fatal("token 未脱敏")
	}
	if out["content"] != "12# 杨树超高" {
		t.Fatal("普通字段不应改动")
	}
	nested := out["nested"].(map[string]any)
	if nested["access_token"] == "zzz-secret-9999" {
		t.Fatal("嵌套 token 未脱敏")
	}
}
