package callme

// Task 8 契约：小程序手拼 multipart 必须被标准解析；附件原始文件名以表单字段 name 为准
// （wx.uploadFile 的 multipart 文件名只能是临时路径 basename），并允许 mp4。

import (
	"bytes"
	"context"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"path"
	"strings"
	"testing"

	"shade/internal/auth"
)

// handMultipart 按小程序 utils/multipart.js 的字节格式手拼 multipart/form-data。
func handMultipart(boundary string, fields [][2]string, fileField, fileName, contentType string, file []byte) []byte {
	var b bytes.Buffer
	for _, f := range fields {
		b.WriteString("--" + boundary + "\r\n")
		b.WriteString(`Content-Disposition: form-data; name="` + f[0] + "\"\r\n\r\n")
		b.WriteString(f[1] + "\r\n")
	}
	b.WriteString("--" + boundary + "\r\n")
	b.WriteString(`Content-Disposition: form-data; name="` + fileField + `"; filename="` + fileName + "\"\r\n")
	if contentType != "" {
		b.WriteString("Content-Type: " + contentType + "\r\n")
	}
	b.WriteString("\r\n")
	b.Write(file)
	b.WriteString("\r\n--" + boundary + "--\r\n")
	return b.Bytes()
}

func attachmentModule(wk *fakeWeKnora) *Module {
	return &Module{DB: nil, WeKnora: wk, Permit: AllowAllPermit{}}
}

func attachmentRequest(body []byte, boundary string) *http.Request {
	req := httptest.NewRequest(http.MethodPost, "/api/v1/callme/sessions/s1/attachments", bytes.NewReader(body))
	req.Header.Set("Content-Type", "multipart/form-data; boundary="+boundary)
	return req.WithContext(context.WithValue(req.Context(), identityContextKey,
		auth.Identity{ID: 1, Username: "u1", Role: "user"}))
}

// TestAttachUploadUsesFormNameAndAllowsMp4：临时路径 basename 无扩展名时，必须用 formData.name
// 的原始文件名（含 mp4）；否则前端选择器允许而服务端 400。
func TestAttachUploadUsesFormNameAndAllowsMp4(t *testing.T) {
	wk := &fakeWeKnora{}
	m := attachmentModule(wk)
	body := handMultipart("----ShadeCallMe8", [][2]string{{"name", "设备巡检.mp4"}},
		"file", "wxfile://tmp_9f1c", "application/octet-stream", []byte("MP4DATA"))
	rec := httptest.NewRecorder()
	m.handleUploadAttachment(rec, attachmentRequest(body, "----ShadeCallMe8"))
	if rec.Code != http.StatusOK {
		t.Fatalf("应接受 mp4 且使用表单原名: %d %s", rec.Code, rec.Body.String())
	}
	if len(wk.uploaded) != 1 {
		t.Fatalf("未上传: %+v", wk.uploaded)
	}
	got := wk.uploaded[0]
	if got.filename != "设备巡检.mp4" {
		t.Fatalf("附件原名应取 formData.name，实际 %q", got.filename)
	}
	if got.size != len("MP4DATA") {
		t.Fatalf("附件字节数不符: %d", got.size)
	}
}

// TestAttachUploadSanitizesFormName：表单原名不能带路径穿越/引号换行。
func TestAttachUploadSanitizesFormName(t *testing.T) {
	wk := &fakeWeKnora{}
	m := attachmentModule(wk)
	body := handMultipart("b8", [][2]string{{"name", `..\..\etc/passwd.pdf`}},
		"file", "wxfile://tmp", "", []byte("PDF"))
	rec := httptest.NewRecorder()
	m.handleUploadAttachment(rec, attachmentRequest(body, "b8"))
	if rec.Code != http.StatusOK {
		t.Fatalf("应接受清洗后的 pdf: %d %s", rec.Code, rec.Body.String())
	}
	if len(wk.uploaded) != 1 || wk.uploaded[0].filename != "passwd.pdf" {
		t.Fatalf("原名应清洗为 basename，实际 %+v", wk.uploaded)
	}
}

// TestAttachUploadFallsBackToHeaderName：旧客户端不带 name 字段时仍用 multipart 文件名。
func TestAttachUploadFallsBackToHeaderName(t *testing.T) {
	wk := &fakeWeKnora{}
	m := attachmentModule(wk)
	body := handMultipart("b9", nil, "file", "report.pdf", "application/pdf", []byte("PDF"))
	rec := httptest.NewRecorder()
	m.handleUploadAttachment(rec, attachmentRequest(body, "b9"))
	if rec.Code != http.StatusOK {
		t.Fatalf("旧客户端上传应成功: %d %s", rec.Code, rec.Body.String())
	}
	if len(wk.uploaded) != 1 || wk.uploaded[0].filename != "report.pdf" {
		t.Fatalf("应回退 header 文件名，实际 %+v", wk.uploaded)
	}
}

// TestChatHandBuiltMultipartStreamsFullResponse：手拼 multipart（字段在文件前后均可）应完整解析，
// SSE 响应不得因 multipart 入口被截断。
func TestChatHandBuiltMultipartStreamsFullResponse(t *testing.T) {
	wk := &fakeWeKnora{stream: `data:{"response_type":"answer","content":"答案","done":true,"request_id":"r1"}` + "\n\n"}
	m := attachmentModule(wk)
	photo, _ := base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAusB9Y9Z4XcAAAAASUVORK5CYII=")
	body := handMultipart("----ShadeChat8", [][2]string{
		{"session_id", "s1"}, {"query", "看图"}, {"web_search", "true"},
	}, "image", "photo.png", "image/png", photo)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/callme/chat", bytes.NewReader(body))
	req.Header.Set("Content-Type", "multipart/form-data; boundary=----ShadeChat8")
	req = req.WithContext(context.WithValue(req.Context(), identityContextKey,
		auth.Identity{ID: 1, Username: "u1", Role: "user"}))
	rec := httptest.NewRecorder()
	m.handleChat(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("手拼 multipart 应成功: %d %s", rec.Code, rec.Body.String())
	}
	out := rec.Body.String()
	if !strings.Contains(out, "答案") || !strings.Contains(out, "data:") {
		t.Fatalf("SSE 响应不完整: %q", out)
	}
	if len(wk.chatReqs) != 1 {
		t.Fatalf("chat 入参缺失: %+v", wk.chatReqs)
	}
	if len(wk.chatReqs[0].Images) != 1 || !wk.chatReqs[0].WebSearch {
		t.Fatalf("multipart 字段解析不符: %+v", wk.chatReqs[0])
	}
}

// TestAttachmentNameHelper 固定清洗规则（basename、去控制字符、保留中文/扩展名）。
func TestAttachmentNameHelper(t *testing.T) {
	cases := map[string]string{
		"设备巡检.mp4":             "设备巡检.mp4",
		`..\..\etc/passwd.pdf`: "passwd.pdf",
		"../报表 2026.xlsx":      "报表 2026.xlsx",
		"a\r\nb.pdf":           "ab.pdf",
		`"quoted".docx`:        "quoted.docx",
		"":                     "",
	}
	for in, want := range cases {
		if got := cleanAttachmentName(in); got != want {
			t.Fatalf("cleanAttachmentName(%q)=%q，期望 %q", in, got, want)
		}
	}
	// 纯 basename 校验（不依赖平台分隔符）
	if got := cleanAttachmentName("/var/tmp/x.pdf"); got != path.Base("/var/tmp/x.pdf") {
		t.Fatalf("绝对路径应取 basename，实际 %q", got)
	}
}
