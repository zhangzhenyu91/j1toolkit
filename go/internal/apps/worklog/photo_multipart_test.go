package worklog

import (
	"bytes"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"
)

// uploadPhotoReq 按双端新契约构造 multipart 原图与业务字段。
func uploadPhotoReq(t *testing.T, env *testEnv, path string, photo []byte, fields map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	part, err := mw.CreateFormFile("image", "photo.png")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write(photo); err != nil {
		t.Fatal(err)
	}
	for key, value := range fields {
		if err := mw.WriteField(key, value); err != nil {
			t.Fatal(err)
		}
	}
	if err := mw.Close(); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, path, &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("Authorization", "Bearer test")
	rec := httptest.NewRecorder()
	env.handler.ServeHTTP(rec, req)
	return rec
}
