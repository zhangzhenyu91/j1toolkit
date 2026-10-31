package worklog

// Task 6 fix round 2：readUploadFile 在 multipart 解析前用 MaxBytesReader 限总量。

import (
	"bytes"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestReadUploadFilePreLimit 大无关字段不能让请求整体读入后再过 file size 检查。
func TestReadUploadFilePreLimit(t *testing.T) {
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	fw, err := mw.CreateFormFile("file", "a.txt")
	if err != nil {
		t.Fatalf("multipart: %v", err)
	}
	_, _ = fw.Write([]byte("hi"))
	_ = mw.WriteField("padding", strings.Repeat("p", 2<<20))
	_ = mw.Close()

	req := httptest.NewRequest(http.MethodPost, "/upload", &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	rec := httptest.NewRecorder()
	if _, _, ok := readUploadFile(rec, req, 8); ok {
		t.Fatalf("超大请求体不应成功读取")
	}
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("应返回 400，实际 %d %s", rec.Code, rec.Body.String())
	}
}
