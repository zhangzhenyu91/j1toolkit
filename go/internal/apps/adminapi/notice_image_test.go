package adminapi

import (
	"bytes"
	"context"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"shade/internal/auth"
	"shade/internal/httpapi"
)

type fakeNoticeImages struct {
	key  string
	data []byte
}

func (f *fakeNoticeImages) PutBuffer(_ context.Context, key string, data []byte, _ string) error {
	f.key, f.data = key, data
	return nil
}
func (f *fakeNoticeImages) PublicURL(key string) string { return "https://files.example/" + key }

func TestNoticeImageUpload(t *testing.T) {
	ident := auth.Identity{ID: 1, Username: "admin", Role: "admin"}
	users := auth.StaticUserStore{Users: map[string]struct {
		Identity auth.Identity
		Hash     string
	}{"admin": {Identity: ident}}}
	authn := auth.NewService(users, nil, "test-secret", time.Hour, time.Hour)
	login, err := authn.IssueToken(context.Background(), ident, "web")
	if err != nil {
		t.Fatal(err)
	}
	images := &fakeNoticeImages{}
	m := New(nil)
	m.COS = images
	router := httpapi.NewRouter(httpapi.Deps{Auth: authn}, m)

	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	part, _ := mw.CreateFormFile("file", "notice.png")
	_, _ = part.Write([]byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n', 0, 0})
	_ = mw.Close()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/notice/upload-image", &body)
	req.Header.Set("Authorization", "Bearer "+login.Token)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusOK || !strings.HasPrefix(images.key, "notice/") || !strings.HasSuffix(images.key, ".png") {
		t.Fatalf("上传失败: %d %s key=%q", w.Code, w.Body.String(), images.key)
	}
	var response struct {
		Code int `json:"code"`
		Data struct {
			URL string `json:"url"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil || response.Code != 0 || response.Data.URL == "" {
		t.Fatalf("上传响应不符: %s", w.Body.String())
	}

	bad := httptest.NewRequest(http.MethodPost, "/api/v1/notice/upload-image", strings.NewReader("not multipart"))
	bad.Header.Set("Authorization", "Bearer "+login.Token)
	bad.Header.Set("Content-Type", "multipart/form-data; boundary=x")
	badW := httptest.NewRecorder()
	router.ServeHTTP(badW, bad)
	if badW.Code != http.StatusBadRequest {
		t.Fatalf("非法上传应返回 400: %d", badW.Code)
	}
}
