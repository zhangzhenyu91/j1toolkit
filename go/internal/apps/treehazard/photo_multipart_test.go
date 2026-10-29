package treehazard

import (
	"bytes"
	"encoding/base64"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"
)

func validTestPNG(t *testing.T) []byte {
	t.Helper()
	buf, err := base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAusB9Y9Z4XcAAAAASUVORK5CYII=")
	if err != nil {
		t.Fatal(err)
	}
	return buf
}

func uploadTreePhotoReq(t *testing.T, env *testEnv, path string, photo []byte) *httptest.ResponseRecorder {
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
