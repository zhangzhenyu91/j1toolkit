package wmadd

// /render 请求体链路回归（真实大图 JSON 问题）：小程序 pkg-wmadd 将原图 base64 进 JSON
// 上送 /api/v1/wmadd/render，本文件按该口径构造请求体，覆盖：
//   - 真实约 1MB PNG/JPEG 合法体（基线：1MB 级在服务端链路本身可通过）
//   - 旧 8MiB 截断线以上、15MB 照片上限、贴旧 Node 20/22mb 的请求体边界
//   - 超限体与坏 JSON 的业务码分明（40024 照片大小 / 40001 不是合法 JSON，不得截断后误报）
// 体积上限口径对齐旧 Node server/src/index.js：/api/v1/wmadd express.json({ limit: '22mb' })。

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// doRaw 直发原始请求体（坏 JSON / 精确体积边界用例不经 json.Marshal）。
func (e *testEnv) doRaw(t *testing.T, method, path string, raw []byte) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer test")
	rec := httptest.NewRecorder()
	e.handler.ServeHTTP(rec, req)
	return rec
}

// wantCode 断言 HTTP 状态 + 业务码（msgSub 非空时再断言消息片段）。
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

// noisePixels 确定性噪声像素（噪声不可压缩，编码体积随像素数线性可预测）。
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

// realPhoto 真实有效的 PNG/JPEG 测试图，体积约 1MB（客户端实拍/选片量级）。
func realPhoto(t *testing.T, format string) []byte {
	t.Helper()
	var buf bytes.Buffer
	switch format {
	case "png":
		// 原始像素 600*580*3 ≈ 1.02MB，PNG 压不动噪声 → 成品约 1MB
		if err := png.Encode(&buf, noisePixels(600, 580)); err != nil {
			t.Fatalf("编码 PNG 失败: %v", err)
		}
	default:
		// 噪声 JPEG(q95) 约 1.2 字节/像素，1120*840 ≈ 1.1MB 量级
		if err := jpeg.Encode(&buf, noisePixels(1120, 840), &jpeg.Options{Quality: 95}); err != nil {
			t.Fatalf("编码 JPEG 失败: %v", err)
		}
	}
	if buf.Len() < 700*1024 || buf.Len() > 2*1024*1024 {
		t.Fatalf("%s 测试图应约 1MB，实际 %d 字节", format, buf.Len())
	}
	return buf.Bytes()
}

// clientRenderBody 按小程序 pkg-wmadd/pages/index/index.js 的 readAndRender → renderAndSave
// 口径拼请求体：{ image: dataURL, wm: {…, orientation} }（wx.request 对对象单次 JSON 序列化）。
func clientRenderBody(t *testing.T, mime string, photo []byte, orientation string) []byte {
	t.Helper()
	raw, err := json.Marshal(map[string]any{
		"image": "data:image/" + mime + ";base64," + base64.StdEncoding.EncodeToString(photo),
		"wm": map[string]any{
			"content":     "绝缘子清扫",
			"time":        "2026.10.28 09:00",
			"weather":     "多云 18°C",
			"location":    "尧都区",
			"longitude":   "111.500000°E",
			"latitude":    "36.200000°N",
			"antiCode":    "ABCDEFGHJKLMNP",
			"orientation": orientation,
		},
	})
	if err != nil {
		t.Fatalf("编码请求体失败: %v", err)
	}
	return raw
}

// TestRenderRealPhotoJSON 真实约 1MB PNG/JPEG 按客户端口径上送：应渲染成功且照片字节级还原。
// 基线证据：1MB 级照片体约 1.4MB，服务端链路本身可通过（生产若在此量级失败属仓外因素）。
func TestRenderRealPhotoJSON(t *testing.T) {
	for _, tc := range []struct{ name, mime string }{{"jpeg", "jpeg"}, {"png", "png"}} {
		t.Run(tc.name, func(t *testing.T) {
			env := newTestServer(t, identUser)
			photo := realPhoto(t, tc.name)
			raw := clientRenderBody(t, tc.mime, photo, "up")
			t.Logf("照片 %d 字节，JSON 请求体 %d 字节", len(photo), len(raw))
			rec := env.doRaw(t, http.MethodPost, "/api/v1/wmadd/render", raw)
			if rec.Code != http.StatusOK {
				t.Fatalf("1MB 真图应渲染成功: %d %s", rec.Code, rec.Body.String())
			}
			code, data := decodeData(t, rec)
			if code != 0 {
				t.Fatalf("业务码应 0，实际 %d %s", code, rec.Body.String())
			}
			if !bytes.Equal(env.renderer.photo, photo) {
				t.Fatalf("渲染入参与上送照片字节不一致（%d→%d）", len(photo), env.renderer.photoLen)
			}
			img, _ := data["image"].(string)
			if !strings.HasPrefix(img, "data:image/jpeg;base64,") {
				t.Fatalf("回图应为 jpeg dataURL: %.40s", img)
			}
		})
	}
}

// TestRenderBodyOverOld8MiB 回归：请求体超过旧 8MiB 截断线（6MB+ 相册原图 base64 量级），
// 修复前被 io.LimitReader 静默截断后误报 40001「不是合法 JSON」，修复后应正常渲染。
func TestRenderBodyOverOld8MiB(t *testing.T) {
	env := newTestServer(t, identUser)
	photo := make([]byte, 7*1024*1024) // 7MB 原图 → base64 后请求体约 9.3MB
	raw := clientRenderBody(t, "jpeg", photo, "up")
	if len(raw) <= 8<<20 {
		t.Fatalf("请求体应超过旧 8MiB 截断线，实际 %d", len(raw))
	}
	t.Logf("照片 %d 字节，JSON 请求体 %d 字节（旧截断线 8MiB）", len(photo), len(raw))
	rec := env.doRaw(t, http.MethodPost, "/api/v1/wmadd/render", raw)
	wantCode(t, rec, http.StatusOK, 0, "")
}

// TestRenderPhotoSizeBoundary 照片体积边界：15MB 整为合法上限（base64 后请求体约 20MB，
// 贴旧 Node 20/22mb 口径）应通过；15MB+1 应 40024「照片大小应在 15MB 以内」，不得误报 40001。
func TestRenderPhotoSizeBoundary(t *testing.T) {
	env := newTestServer(t, identUser)
	ok := make([]byte, 15*1024*1024) // 恰 15MB：合法上限
	raw := clientRenderBody(t, "jpeg", ok, "up")
	t.Logf("15MB 照片 JSON 请求体 %d 字节（旧 Node 20/22mb 边界）", len(raw))
	if len(raw) < 19*1024*1024 {
		t.Fatalf("15MB 照片 base64 后请求体应接近 20MB，实际 %d", len(raw))
	}
	rec := env.doRaw(t, http.MethodPost, "/api/v1/wmadd/render", raw)
	wantCode(t, rec, http.StatusOK, 0, "")

	over := make([]byte, 15*1024*1024+1)
	rec = env.doRaw(t, http.MethodPost, "/api/v1/wmadd/render", clientRenderBody(t, "jpeg", over, "up"))
	wantCode(t, rec, http.StatusBadRequest, codeSize, "照片大小应在 15MB 以内")
}

// TestRenderBodyOverCap 请求体超 JSON 上限（22MiB，对齐 Node express.json({limit:'22mb'})）
// 应 40024 照片大小口径；io.ReadAll 必须检测超限，不得静默截断后误报 40001。
func TestRenderBodyOverCap(t *testing.T) {
	env := newTestServer(t, identUser)
	huge := "data:image/jpeg;base64," + strings.Repeat("A", 22<<20) // 体超 22MiB
	raw, err := json.Marshal(map[string]any{"image": huge})
	if err != nil {
		t.Fatalf("编码请求体失败: %v", err)
	}
	t.Logf("超限 JSON 请求体 %d 字节（上限 22MiB）", len(raw))
	rec := env.doRaw(t, http.MethodPost, "/api/v1/wmadd/render", raw)
	wantCode(t, rec, http.StatusBadRequest, codeSize, "照片大小应在 15MB 以内")
}

// TestRenderBadJSON 坏 JSON 请求体（未超限）应 40001「参数错误：请求体不是合法 JSON」，
// 与超限 40024 分明。
func TestRenderBadJSON(t *testing.T) {
	env := newTestServer(t, identUser)
	cases := map[string][]byte{
		"截断JSON": []byte(`{"image":"data:image/jpeg;base64,AAA`),
		"非JSON":  []byte(`image=not-json`),
		"顶层非对象":  []byte(`[1,2,3]`),
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			rec := env.doRaw(t, http.MethodPost, "/api/v1/wmadd/render", raw)
			wantCode(t, rec, http.StatusBadRequest, codeParam, "参数错误：请求体不是合法 JSON")
		})
	}
}
