package callme

// Task 8 round 1：附件原名只能来自 multipart body 的 name 字段（r.PostFormValue），
// URL query 的 name 不得冒充文件名；缺省仍回退 multipart 文件名。

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestAttachUploadIgnoresQueryName(t *testing.T) {
	wk := &fakeWeKnora{}
	m := attachmentModule(wk)
	body := handMultipart("b10", nil, "file", "report.pdf", "application/pdf", []byte("PDF"))
	req := attachmentRequest(body, "b10")
	req.URL.RawQuery = "name=evil.pdf"
	rec := httptest.NewRecorder()
	m.handleUploadAttachment(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("应回退 header 文件名而非 query: %d %s", rec.Code, rec.Body.String())
	}
	if len(wk.uploaded) != 1 || wk.uploaded[0].filename != "report.pdf" {
		t.Fatalf("query name 不得冒充附件名，实际 %+v", wk.uploaded)
	}
}

func TestAttachUploadBodyNameBeatsQueryName(t *testing.T) {
	wk := &fakeWeKnora{}
	m := attachmentModule(wk)
	body := handMultipart("b11", [][2]string{{"name", "真实附件.mp4"}},
		"file", "wxfile://tmp_x", "application/octet-stream", []byte("MP4"))
	req := attachmentRequest(body, "b11")
	req.URL.RawQuery = "name=evil.pdf"
	rec := httptest.NewRecorder()
	m.handleUploadAttachment(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("body name 应生效: %d %s", rec.Code, rec.Body.String())
	}
	if len(wk.uploaded) != 1 || wk.uploaded[0].filename != "真实附件.mp4" {
		t.Fatalf("应优先 body name，实际 %+v", wk.uploaded)
	}
}
