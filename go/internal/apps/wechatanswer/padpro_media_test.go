// 媒体下载测试：语音 silk 转 WAV / wav、mp3 直出、格式探测与参数错误/大小边界、
// 文件分片拼接；网关请求形态对齐 crane-answer src/wechatpadpro/fileStore.js。
// 全部走本地 httptest 假网关与本地生成素材（正弦 PCM → SILK），不访问任何云端服务。
package wechatanswer

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"shade/internal/compat/cryptoaudio"
)

// mediaReq 假网关收到的一次请求。
type mediaReq struct {
	path string
	body map[string]any
}

// mediaGW 本地假网关：记录请求、按 reply 回二进制（base64 包成 Data，口径同 extractMediaBinary）。
type mediaGW struct {
	cli   *PadProClient
	mu    sync.Mutex
	reqs  []mediaReq
	reply func(body map[string]any) []byte
}

// newMediaGW 起假网关并返回绑定其上的 PadProClient。
func newMediaGW(t *testing.T, reply func(body map[string]any) []byte) *mediaGW {
	t.Helper()
	gw := &mediaGW{reply: reply}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		gw.mu.Lock()
		gw.reqs = append(gw.reqs, mediaReq{path: r.URL.Path, body: body})
		gw.mu.Unlock()
		var payload []byte
		if gw.reply != nil {
			payload = gw.reply(body)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"Success": true,
			"Code":    0,
			"Data":    base64.StdEncoding.EncodeToString(payload),
		})
	}))
	t.Cleanup(srv.Close)
	gw.cli = &PadProClient{Cfg: fakeCfg{values: map[string]string{
		"WECHATPADPRO_TOKEN":    "test-token",
		"WECHATPADPRO_API_BASE": srv.URL,
		"WECHATPADPRO_BOT_WXID": "bot@x",
	}}}
	return gw
}

// requests 返回已收到的请求快照。
func (g *mediaGW) requests() []mediaReq {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]mediaReq(nil), g.reqs...)
}

// genVoicePCM 生成测试用 16bit 单声道正弦 PCM（24kHz）。
func genVoicePCM(ms int) []byte {
	rate := cryptoaudio.DefaultSilkSampleRate
	n := rate * ms / 1000
	out := make([]byte, n*2)
	for i := 0; i < n; i++ {
		v := int16(12000 * math.Sin(2*math.Pi*440*float64(i)/float64(rate)))
		binary.LittleEndian.PutUint16(out[i*2:], uint16(v))
	}
	return out
}

// genVoiceSilk 生成微信变体 SILK 测试素材（经 cryptoaudio 真实编码器，tencent 封装）。
func genVoiceSilk(t *testing.T) []byte {
	t.Helper()
	silk, err := cryptoaudio.EncodeSilk(genVoicePCM(200), cryptoaudio.DefaultSilkSampleRate)
	if err != nil {
		t.Fatalf("SILK 测试素材编码失败: %v", err)
	}
	return silk
}

// voiceEntry 构造语音下载参数。
func voiceEntry(length int64) MediaEntry {
	return MediaEntry{
		Kind:         "voice",
		GroupID:      "g1@chatroom",
		MsgID:        "msg-1",
		NewMsgID:     "new-1",
		MasterBufID:  "buf-1",
		Length:       length,
		FromUserName: "user-1",
	}
}

// TestPadProVoiceSilkToWav silk 语音下载转 WAV，网关请求形态对齐 fileStore.js downloadVoiceBuffer。
func TestPadProVoiceSilkToWav(t *testing.T) {
	silk := genVoiceSilk(t)
	gw := newMediaGW(t, func(map[string]any) []byte { return silk })
	got, ext, err := gw.cli.DownloadMedia(context.Background(), voiceEntry(int64(len(silk))))
	if err != nil {
		t.Fatalf("silk 语音应转码成功: %v", err)
	}
	if ext != "wav" {
		t.Fatalf("ext = %q, 期望 wav", ext)
	}
	if cryptoaudio.DetectAudioFormat(got) != cryptoaudio.FormatWAV {
		t.Fatal("转码产物应为 wav")
	}
	if _, rate, err := cryptoaudio.ParseWAV(got); err != nil || rate != cryptoaudio.DefaultSilkSampleRate {
		t.Fatalf("WAV 解析失败: rate=%d %v", rate, err)
	}

	reqs := gw.requests()
	if len(reqs) != 1 {
		t.Fatalf("网关请求数 = %d, 期望 1", len(reqs))
	}
	req := reqs[0]
	if req.path != "/v1/Tools/DownloadVoiceBinary" {
		t.Fatalf("path = %q", req.path)
	}
	want := map[string]any{
		"chat_room_name": "g1@chatroom",
		"client_msg_id":  "msg-1",
		"file_name":      "voice.silk",
		"format":         float64(1),
		"from_user_name": "user-1",
		"length":         float64(len(silk)),
		"master_buf_id":  "buf-1",
		"msg_id":         "msg-1",
		"new_msg_id":     "new-1",
		"to_user_name":   "bot@x",
	}
	for k, v := range want {
		if req.body[k] != v {
			t.Fatalf("请求字段 %s = %v, 期望 %v", k, req.body[k], v)
		}
	}
}

// TestPadProVoicePassthrough wav / mp3 下载内容直出不重编码。
func TestPadProVoicePassthrough(t *testing.T) {
	wav := cryptoaudio.WrapWAV(genVoicePCM(50), cryptoaudio.DefaultSilkSampleRate)
	cases := []struct {
		name string
		in   []byte
		ext  string
	}{
		{"wav", wav, "wav"},
		{"mp3(ID3)", []byte("ID3\x04\x00\x00\x00\x00\x00\x00"), "mp3"},
		{"mp3(帧同步)", []byte{0xff, 0xfb, 0x90, 0x00, 0, 0, 0, 0}, "mp3"},
	}
	for _, tc := range cases {
		gw := newMediaGW(t, func(map[string]any) []byte { return tc.in })
		got, ext, err := gw.cli.DownloadMedia(context.Background(), voiceEntry(int64(len(tc.in))))
		if err != nil {
			t.Fatalf("%s 直出失败: %v", tc.name, err)
		}
		if ext != tc.ext || !bytes.Equal(got, tc.in) {
			t.Fatalf("%s 直出不一致: ext=%q len=%d", tc.name, ext, len(got))
		}
	}
}

// TestPadProVoiceUnknownAndEmpty 未知编码与空载荷报错，且不再返回「转码未接入」。
func TestPadProVoiceUnknownAndEmpty(t *testing.T) {
	gw := newMediaGW(t, func(map[string]any) []byte { return []byte("plain-voice") })
	_, _, err := gw.cli.DownloadMedia(context.Background(), voiceEntry(11))
	if err == nil || !strings.Contains(err.Error(), "无法识别") {
		t.Fatalf("未知编码应报无法识别, got %v", err)
	}
	if strings.Contains(err.Error(), "未接入") {
		t.Fatalf("不应再返回转码未接入: %v", err)
	}

	empty := newMediaGW(t, func(map[string]any) []byte { return nil })
	if _, _, err := empty.cli.DownloadMedia(context.Background(), voiceEntry(1)); err == nil {
		t.Fatal("空载荷应报错")
	}
}

// TestPadProVoiceParamErrors 语音参数缺失与大小边界，均不应发起网关请求。
func TestPadProVoiceParamErrors(t *testing.T) {
	gw := newMediaGW(t, func(map[string]any) []byte { return []byte("x") })
	base := voiceEntry(100)
	noIDs := base
	noIDs.MsgID, noIDs.NewMsgID = "", ""
	noBuf := base
	noBuf.MasterBufID = ""
	noLen := base
	noLen.Length = 0
	tooBig := base
	tooBig.Length = mediaMaxBytes + 1

	cases := []struct {
		name string
		e    MediaEntry
		want string
	}{
		{"缺 msg_id/new_msg_id", noIDs, "语音参数不全"},
		{"缺 master_buf_id", noBuf, "语音参数不全"},
		{"length 非正", noLen, "语音参数不全"},
		{"超 50MB", tooBig, "超过 50MB"},
	}
	for _, tc := range cases {
		_, _, err := gw.cli.DownloadMedia(context.Background(), tc.e)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("%s: err = %v, 期望包含 %q", tc.name, err, tc.want)
		}
	}
	if n := len(gw.requests()); n != 0 {
		t.Fatalf("参数错误不应请求网关，请求数 = %d", n)
	}

	_, _, err := gw.cli.DownloadMedia(context.Background(), tooBig)
	var askErr *AskError
	if !errors.As(err, &askErr) || askErr.Code != "file_too_large" {
		t.Fatalf("超限应为 file_too_large 业务码, got %v", err)
	}
}

// TestPadProChunkedBoundary 文件/视频参数错误与 50MB 大小边界。
func TestPadProChunkedBoundary(t *testing.T) {
	gw := newMediaGW(t, func(map[string]any) []byte { return []byte("z") })
	cases := []struct {
		name string
		e    MediaEntry
		want string
	}{
		{"file 缺 attachid", MediaEntry{Kind: "file", DataLen: 10}, "缺少 attachid"},
		{"file 缺 totallen", MediaEntry{Kind: "file", AttachID: "a1"}, "缺少 totallen"},
		{"file 超 50MB", MediaEntry{Kind: "file", AttachID: "a1", DataLen: mediaMaxBytes + 1}, "超过 50MB"},
		{"video 缺 msg_id", MediaEntry{Kind: "video", DataLen: 10}, "缺少 msg_id"},
		{"video 缺 data_len", MediaEntry{Kind: "video", MsgID: "m1"}, "缺少 data_len"},
		{"video 超 50MB", MediaEntry{Kind: "video", MsgID: "m1", DataLen: mediaMaxBytes + 1}, "超过 50MB"},
		{"未知类型", MediaEntry{Kind: "audio"}, "未知媒体类型"},
	}
	for _, tc := range cases {
		_, _, err := gw.cli.DownloadMedia(context.Background(), tc.e)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("%s: err = %v, 期望包含 %q", tc.name, err, tc.want)
		}
	}
	if n := len(gw.requests()); n != 0 {
		t.Fatalf("参数错误不应请求网关，请求数 = %d", n)
	}

	_, _, err := gw.cli.DownloadMedia(context.Background(), MediaEntry{Kind: "file", AttachID: "a1", DataLen: mediaMaxBytes + 1})
	var askErr *AskError
	if !errors.As(err, &askErr) || askErr.Code != "file_too_large" {
		t.Fatalf("超限应为 file_too_large 业务码, got %v", err)
	}
}

// TestPadProChunkedDownload 文件 512KB 分片拼接与分片请求形态（对齐 fileStore.js downloadFileBuffer）。
func TestPadProChunkedDownload(t *testing.T) {
	payload := make([]byte, mediaChunkSize+10)
	for i := range payload {
		payload[i] = byte(i % 251)
	}
	gw := newMediaGW(t, func(body map[string]any) []byte {
		sec, _ := body["section"].(map[string]any)
		start := int(sec["start_pos"].(float64))
		length := int(sec["data_len"].(float64))
		return payload[start : start+length]
	})
	got, ext, err := gw.cli.DownloadMedia(context.Background(), MediaEntry{
		Kind:         "file",
		AttachID:     "a-1",
		DataLen:      int64(len(payload)),
		FileName:     "Report.PDF",
		FromUserName: "user-1",
	})
	if err != nil {
		t.Fatalf("分片下载失败: %v", err)
	}
	if ext != "pdf" {
		t.Fatalf("ext = %q, 期望 pdf（小写扩展名）", ext)
	}
	if !bytes.Equal(got, payload) {
		t.Fatal("分片拼接结果不一致")
	}

	reqs := gw.requests()
	if len(reqs) != 2 {
		t.Fatalf("请求数 = %d, 期望 2 片", len(reqs))
	}
	for _, req := range reqs {
		if req.path != "/v1/Tools/DownloadFileBinary" {
			t.Fatalf("path = %q", req.path)
		}
	}
	first, second := reqs[0].body, reqs[1].body
	if first["app_id"] != "ID_10001" || first["attach_id"] != "a-1" ||
		first["data_len"] != float64(len(payload)) || first["file_name"] != "Report.PDF" ||
		first["user_name"] != "user-1" {
		t.Fatalf("首片请求字段不符: %v", first)
	}
	sec1, _ := first["section"].(map[string]any)
	sec2, _ := second["section"].(map[string]any)
	if sec1["start_pos"] != float64(0) || sec1["data_len"] != float64(mediaChunkSize) {
		t.Fatalf("首片 section = %v", sec1)
	}
	if sec2["start_pos"] != float64(mediaChunkSize) || sec2["data_len"] != float64(10) {
		t.Fatalf("尾片 section = %v", sec2)
	}

	// 恰好单片：dataLen == mediaChunkSize 只发一次请求
	single := make([]byte, mediaChunkSize)
	gw2 := newMediaGW(t, func(body map[string]any) []byte {
		sec, _ := body["section"].(map[string]any)
		start := int(sec["start_pos"].(float64))
		length := int(sec["data_len"].(float64))
		return single[start : start+length]
	})
	got2, _, err := gw2.cli.DownloadMedia(context.Background(), MediaEntry{
		Kind: "file", AttachID: "a-2", DataLen: int64(len(single)), FileName: "s.bin",
	})
	if err != nil || !bytes.Equal(got2, single) {
		t.Fatalf("单片下载失败: %v", err)
	}
	if n := len(gw2.requests()); n != 1 {
		t.Fatalf("单片请求数 = %d, 期望 1", n)
	}
}
