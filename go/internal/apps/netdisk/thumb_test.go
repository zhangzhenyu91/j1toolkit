package netdisk

// 缩略图生成测试：解码/缩放/EXIF 转正/webp 输出与 /thumb 缓存口径；不访问真实 OpenList。

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/image/webp"
)

// makePNG 构造渐变测试图（非纯色，便于校验像素映射）。
func makePNG(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.NRGBA{R: uint8(x * 7), G: uint8(y * 5), B: 128, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("png 编码: %v", err)
	}
	return buf.Bytes()
}

// jpegWithOrientation 构造带 EXIF Orientation 的 JPEG。
func jpegWithOrientation(t *testing.T, w, h, orientation int) []byte {
	t.Helper()
	img := image.NewGray(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.SetGray(x, y, color.Gray{Y: uint8(x*40 + y*10)})
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, nil); err != nil {
		t.Fatalf("jpeg 编码: %v", err)
	}
	// APP1 段：Exif\0\0 + TIFF(II/42/IFD0 一条 Orientation SHORT)。
	tiff := []byte{
		'I', 'I', 42, 0, 8, 0, 0, 0,
		1, 0,
		0x12, 0x01, 3, 0, 1, 0, 0, 0, byte(orientation), 0, 0, 0,
		0, 0, 0, 0,
	}
	payload := append([]byte("Exif\x00\x00"), tiff...)
	segLen := len(payload) + 2
	app1 := []byte{0xFF, 0xE1, byte(segLen >> 8), byte(segLen)}
	app1 = append(app1, payload...)
	out := append([]byte{0xFF, 0xD8}, app1...)
	out = append(out, buf.Bytes()[2:]...) // 接回 SOI 之后的原始段
	return out
}

func TestMakeThumbScaleAndFormat(t *testing.T) {
	th := NewWebpThumbnailer()
	// 小图不放大：2x1 → 2x1。
	out, err := th.MakeThumb(makePNG(t, 2, 1))
	if err != nil {
		t.Fatalf("MakeThumb: %v", err)
	}
	img, err := webp.Decode(bytes.NewReader(out))
	if err != nil {
		t.Fatalf("webp 解码: %v", err)
	}
	if img.Bounds().Dx() != 2 || img.Bounds().Dy() != 1 {
		t.Fatalf("小图不应放大: %v", img.Bounds())
	}
	// 大图等比缩至 ≤384：800x400 → 384x192。
	out, err = th.MakeThumb(makePNG(t, 800, 400))
	if err != nil {
		t.Fatalf("MakeThumb 大图: %v", err)
	}
	img, err = webp.Decode(bytes.NewReader(out))
	if err != nil {
		t.Fatalf("webp 解码: %v", err)
	}
	if img.Bounds().Dx() != 384 || img.Bounds().Dy() != 192 {
		t.Fatalf("缩放尺寸错误: %v want 384x192", img.Bounds())
	}
	// 非图片拒绝。
	if _, err := th.MakeThumb([]byte("not-an-image")); err == nil {
		t.Fatalf("非图片应报错")
	}
}

func TestMakeThumbExifRotate(t *testing.T) {
	th := NewWebpThumbnailer()
	// Orientation=6（顺时针 90）：4x2 → 2x4。
	out, err := th.MakeThumb(jpegWithOrientation(t, 4, 2, 6))
	if err != nil {
		t.Fatalf("MakeThumb: %v", err)
	}
	img, err := webp.Decode(bytes.NewReader(out))
	if err != nil {
		t.Fatalf("webp 解码: %v", err)
	}
	if img.Bounds().Dx() != 2 || img.Bounds().Dy() != 4 {
		t.Fatalf("EXIF 转正后尺寸错误: %v want 2x4", img.Bounds())
	}
	// Orientation=1 不转：4x2 保持。
	out, err = th.MakeThumb(jpegWithOrientation(t, 4, 2, 1))
	if err != nil {
		t.Fatalf("MakeThumb: %v", err)
	}
	img, err = webp.Decode(bytes.NewReader(out))
	if err != nil {
		t.Fatalf("webp 解码: %v", err)
	}
	if img.Bounds().Dx() != 4 || img.Bounds().Dy() != 2 {
		t.Fatalf("Orientation=1 不应转正: %v", img.Bounds())
	}
}

// imageOL 假 OpenList：/api/fs/get 元信息 + /d/ 图片字节。
func imageOL(t *testing.T, payload []byte) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/api/auth/login":
			_, _ = w.Write([]byte(`{"code":200,"data":{"token":"tk-1"}}`))
		case r.URL.Path == "/api/fs/get":
			_, _ = w.Write([]byte(`{"code":200,"data":{"name":"a.png","is_dir":false,"size":3,"modified":"2026-10-27 10:00:00","sign":"s1"}}`))
		case strings.HasPrefix(r.URL.Path, "/d/"):
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write(payload)
		case r.URL.Path == "/api/fs/put":
			_, _ = w.Write([]byte(`{"code":200,"data":null}`))
		default:
			_, _ = w.Write([]byte(`{"code":500,"message":"unknown api"}`))
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestHandleThumbWithCache(t *testing.T) {
	srv := imageOL(t, makePNG(t, 8, 4))
	m := New(openFakeDB(t))
	m.OL = &OpenListHTTP{BaseURL: srv.URL, Username: "u", Password: "p"}
	m.Base = BaseDirs{PersonalRoot: "/nd/my", PublicRoot: "/nd/pub"}
	m.Thumb = NewWebpThumbnailer()
	m.ThumbDir = filepath.Join(t.TempDir(), "thumbs")
	h := newTestRouter(t, m)

	req := withFakeAuth(httptest.NewRequest(http.MethodGet,
		"/api/v1/netdisk/thumb?space=my&path=/a.png", nil))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("thumb 失败: %d %s", rec.Code, rec.Body)
	}
	if rec.Header().Get("Content-Type") != "image/webp" {
		t.Fatalf("Content-Type 错误: %s", rec.Header().Get("Content-Type"))
	}
	if _, err := webp.Decode(bytes.NewReader(rec.Body.Bytes())); err != nil {
		t.Fatalf("响应非 webp: %v", err)
	}
	// 缓存落盘（键 = docKey(abs|size|modified)）。
	entries, err := os.ReadDir(m.ThumbDir)
	if err != nil || len(entries) != 1 {
		t.Fatalf("缓存文件应恰 1 个: %v %v", entries, err)
	}
	// 第二次命中缓存（删 Thumb 生成器仍应 200，证明走缓存直出）。
	m.Thumb = nil
	req = withFakeAuth(httptest.NewRequest(http.MethodGet,
		"/api/v1/netdisk/thumb?space=my&path=/a.png", nil))
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "image/webp" {
		t.Fatalf("缓存命中失败: %d %s", rec.Code, rec.Body)
	}
}
