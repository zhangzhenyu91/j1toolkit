package wmadd

// /render multipart 请求链路回归：真实图片、旧 JSON 退役、50 MiB 边界与临时下载。

import (
	"bytes"
	"encoding/json"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"math/rand"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func renderMultipartReq(t *testing.T, env *testEnv, photo []byte, wmRaw string) *httptest.ResponseRecorder {
	t.Helper()
	env.module.DataDir = t.TempDir()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	part, err := mw.CreateFormFile("image", "photo.bin")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write(photo); err != nil {
		t.Fatal(err)
	}
	if wmRaw != "" {
		if err := mw.WriteField("wm", wmRaw); err != nil {
			t.Fatal(err)
		}
	}
	if err := mw.Close(); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/wmadd/render", &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("Authorization", "Bearer test")
	rec := httptest.NewRecorder()
	env.handler.ServeHTTP(rec, req)
	return rec
}

func wantCode(t *testing.T, rec *httptest.ResponseRecorder, status, code int, msgSub string) {
	t.Helper()
	if rec.Code != status {
		t.Fatalf("HTTP 状态应 %d，实际 %d %s", status, rec.Code, rec.Body.String())
	}
	got, _ := decodeData(t, rec)
	if got != code {
		t.Fatalf("业务码应 %d，实际 %d body=%s", code, got, rec.Body.String())
	}
	if msgSub != "" && !strings.Contains(rec.Body.String(), msgSub) {
		t.Fatalf("消息应含 %q，实际 %s", msgSub, rec.Body.String())
	}
}

func noisePixels(w, h int) *image.RGBA {
	rnd := rand.New(rand.NewSource(1))
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{uint8(rnd.Intn(256)), uint8(rnd.Intn(256)), uint8(rnd.Intn(256)), 255})
		}
	}
	return img
}

func realPhoto(t *testing.T, format string) []byte {
	t.Helper()
	var buf bytes.Buffer
	switch format {
	case "png":
		if err := png.Encode(&buf, noisePixels(600, 580)); err != nil {
			t.Fatalf("编码 PNG 失败: %v", err)
		}
	default:
		if err := jpeg.Encode(&buf, noisePixels(1120, 840), &jpeg.Options{Quality: 95}); err != nil {
			t.Fatalf("编码 JPEG 失败: %v", err)
		}
	}
	if buf.Len() < 700*1024 || buf.Len() > 2*1024*1024 {
		t.Fatalf("%s 测试图应约 1MB，实际 %d 字节", format, buf.Len())
	}
	return buf.Bytes()
}

func TestRenderRealPhotoMultipart(t *testing.T) {
	for _, tc := range []struct{ name string }{{"jpeg"}, {"png"}} {
		t.Run(tc.name, func(t *testing.T) {
			env := newTestServer(t, identUser)
			photo := realPhoto(t, tc.name)
			rec := renderMultipartReq(t, env, photo, `{"content":"绝缘子清扫","orientation":"up"}`)
			wantCode(t, rec, http.StatusOK, 0, "")
			if !bytes.Equal(env.renderer.photo, photo) {
				t.Fatalf("渲染入参与上送照片字节不一致（%d→%d）", len(photo), env.renderer.photoLen)
			}
			_, data := decodeData(t, rec)
			url, _ := data["url"].(string)
			if url == "" || data["render_id"] == "" {
				t.Fatalf("应返回临时下载地址：%+v", data)
			}
			req := httptest.NewRequest(http.MethodGet, url, nil)
			req.Header.Set("Authorization", "Bearer test")
			download := httptest.NewRecorder()
			env.handler.ServeHTTP(download, req)
			if download.Code != http.StatusOK || download.Body.String() != "jpeg-bytes" ||
				download.Header().Get("Content-Type") != "image/jpeg" {
				t.Fatalf("临时下载不符：%d %s %q", download.Code, download.Body.String(), download.Header().Get("Content-Type"))
			}
		})
	}
}

func TestRenderRejectsLegacyJSON(t *testing.T) {
	env := newTestServer(t, identUser)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/wmadd/render", strings.NewReader(`{"image":"data:image/jpeg;base64,AAAA"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer test")
	rec := httptest.NewRecorder()
	env.handler.ServeHTTP(rec, req)
	wantCode(t, rec, http.StatusBadRequest, codeParam, "multipart/form-data")
}

func TestRenderPhotoSizeBoundary(t *testing.T) {
	env := newTestServer(t, identUser)
	valid := realPhoto(t, "png")
	exact := append([]byte(nil), valid...)
	exact = append(exact, make([]byte, (50<<20)-len(exact))...)
	rec := renderMultipartReq(t, env, exact, `{"orientation":"up"}`)
	wantCode(t, rec, http.StatusOK, 0, "")

	over := append(append([]byte(nil), exact...), 1)
	rec = renderMultipartReq(t, env, over, `{"orientation":"up"}`)
	wantCode(t, rec, http.StatusRequestEntityTooLarge, codeSize, "照片大小应在 50MB 以内")
}

func TestRenderRejectsBadImage(t *testing.T) {
	env := newTestServer(t, identUser)
	rec := renderMultipartReq(t, env, []byte("not-image"), "")
	wantCode(t, rec, http.StatusBadRequest, codeFormat, "jpeg/png")
}

func TestRenderWMJSONFields(t *testing.T) {
	env := newTestServer(t, identUser)
	rec := renderMultipartReq(t, env, realPhoto(t, "jpeg"), `{"content":"绝缘子清扫","orientation":"right"}`)
	wantCode(t, rec, http.StatusOK, 0, "")
	if env.renderer.orientation != "right" || env.renderer.wm.Content != "绝缘子清扫" {
		t.Fatalf("wm 字段解析不符：%+v %+v", env.renderer.wm, env.renderer.orientation)
	}
}

func TestRenderResponseIsSmallJSON(t *testing.T) {
	env := newTestServer(t, identUser)
	rec := renderMultipartReq(t, env, realPhoto(t, "jpeg"), "")
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(rec.Body.String(), "base64") {
		t.Fatal("渲染响应不得再携带 base64 图片")
	}
}
