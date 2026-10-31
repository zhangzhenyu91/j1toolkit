package httpapi

// 静态资源托管回归：缺失的带扩展名文件必须 404（不得回落 SPA index.html），
// KVM 设备安装素材由 /kvm-device/ 直接按文件返回且不可越出素材目录。

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type staticTestCfg struct{}

func (staticTestCfg) Get(_ context.Context, key string) (string, error) {
	if key == "SGCC_DX_APPID" {
		return "fixture-app-id", nil
	}
	return "", errors.New("not found")
}

func newSPAHandler(t *testing.T, files map[string]string) http.Handler {
	t.Helper()
	dir := t.TempDir()
	for name, body := range files {
		path := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("创建目录: %v", err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatalf("写文件: %v", err)
		}
	}
	return WithSPA(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "api", http.StatusTeapot)
	}), dir)
}

func TestWithSPAMissingStaticAssetReturns404(t *testing.T) {
	h := newSPAHandler(t, map[string]string{"index.html": "<html>spa-index</html>"})
	for _, path := range []string{"/assets/missing.js", "/kvm-device/install.sh", "/favicon.ico"} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s 缺失静态资源应 404，实际 %d", path, rec.Code)
		}
		if strings.Contains(rec.Body.String(), "spa-index") {
			t.Errorf("%s 不得回落 SPA HTML: %q", path, rec.Body.String())
		}
	}
}

// 旧 Node 网页端 *.html 深链必须继续可用：缺失的 .html 路径回落 SPA（由前端
// legacy-urls 跳 hash 路由），不能被静态资源启发式误判为缺失资源 404。
func TestWithSPALegacyHTMLDeepLinksFallBackToSPA(t *testing.T) {
	h := newSPAHandler(t, map[string]string{"index.html": "<html>spa-index</html>"})
	for _, path := range []string{
		"/worklog.html",
		"/share.html",
		"/teamdata.html",
		"/legacy/worklog.html",
		"/deep/nested/office.html",
	} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "spa-index") {
			t.Errorf("%s 旧 html 深链应回落 SPA: %d %q", path, rec.Code, rec.Body.String())
		}
	}
	// 其他缺失静态资源仍必须 404，不能被 .html 例外放宽。
	for _, path := range []string{"/assets/missing.js", "/kvm-device/install.sh", "/favicon.ico", "/doc.pdf"} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s 缺失静态资源应仍为 404，实际 %d", path, rec.Code)
		}
	}
}

func TestWithSPAServesBuiltAssetsAndHistoryFallback(t *testing.T) {
	h := newSPAHandler(t, map[string]string{
		"index.html":      "<html>spa-index</html>",
		"assets/app-1.js": "console.log(1)",
	})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/assets/app-1.js", nil))
	if rec.Code != http.StatusOK || rec.Body.String() != "console.log(1)" {
		t.Fatalf("已构建静态资源应原样返回: %d %q", rec.Code, rec.Body.String())
	}
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/worklog/2026-10", nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "spa-index") {
		t.Fatalf("无扩展名 SPA 路由应回落 index.html: %d %q", rec.Code, rec.Body.String())
	}
}

func TestWithLegacyPagesServesKVMDeviceAssets(t *testing.T) {
	publicDir := t.TempDir()
	kvmDir := filepath.Join(publicDir, "kvm-device")
	if err := os.MkdirAll(kvmDir, 0o755); err != nil {
		t.Fatalf("创建 KVM 素材目录: %v", err)
	}
	script := "#!/bin/sh\necho shade-kvm\n"
	if err := os.WriteFile(filepath.Join(kvmDir, "install.sh"), []byte(script), 0o644); err != nil {
		t.Fatalf("写 install.sh: %v", err)
	}
	if err := os.WriteFile(filepath.Join(publicDir, "secret.txt"), []byte("outside"), 0o644); err != nil {
		t.Fatalf("写越界文件: %v", err)
	}
	h := WithLegacyPages(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "spa-fallback", http.StatusOK)
	}), publicDir, nil)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/kvm-device/install.sh", nil))
	if rec.Code != http.StatusOK || rec.Body.String() != script {
		t.Fatalf("KVM install.sh 应原样返回: %d %q", rec.Code, rec.Body.String())
	}

	cases := []struct {
		path string
		code int
	}{
		{"/kvm-device/missing.sh", http.StatusNotFound},
		{"/kvm-device/", http.StatusNotFound},
		{"/kvm-device/../secret.txt", http.StatusNotFound},
	}
	for _, c := range cases {
		rec = httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, c.path, nil))
		if rec.Code != c.code {
			t.Errorf("%s 应 %d，实际 %d", c.path, c.code, rec.Code)
		}
		if strings.Contains(rec.Body.String(), "spa-fallback") || strings.Contains(rec.Body.String(), "outside") {
			t.Errorf("%s 不得回落 SPA 或越出素材目录: %q", c.path, rec.Body.String())
		}
	}

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/kvm-device", nil))
	if rec.Code != http.StatusMovedPermanently || rec.Header().Get("Location") != "/kvm-device/" {
		t.Fatalf("/kvm-device 应跳转到 /kvm-device/: %d %q", rec.Code, rec.Header().Get("Location"))
	}
}

func TestWithLegacyPagesStillInjectsCaptchaAppID(t *testing.T) {
	publicDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(publicDir, "sgcc-captcha.html"),
		[]byte(`<div data-app="__SGCC_DX_APPID__"></div>`), 0o644); err != nil {
		t.Fatalf("写滑块页: %v", err)
	}
	h := WithLegacyPages(http.NotFoundHandler(), publicDir, staticTestCfg{})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/sgcc-captcha.html", nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "fixture-app-id") {
		t.Fatalf("滑块页 AppID 注入失败: %d %q", rec.Code, rec.Body.String())
	}
}
