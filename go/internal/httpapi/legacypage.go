package httpapi

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// ConfigValue 读取公共页面所需的运行时配置。
type ConfigValue interface {
	Get(ctx context.Context, key string) (string, error)
}

// kvmDevicePrefix KVM 设备一键接入素材的公开托管路径（无需鉴权，内容无敏感信息）。
const kvmDevicePrefix = "/kvm-device/"

// WithLegacyPages 托管需要服务端注入配置的独立页面与公开静态素材，不接管 Vue 路由。
// 顶象 AppID 每次请求读取，配置变更后立即生效，不写入静态产物。
// KVM 素材直接按文件返回；目录或缺失文件一律 404，不回落 SPA HTML（curl | sh 不能被
// 200 的 index.html 污染）。
func WithLegacyPages(next http.Handler, publicDir string, cfg ConfigValue) http.Handler {
	kvmRoot := filepath.Join(publicDir, "kvm-device")
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/kvm-device" {
			http.Redirect(w, r, kvmDevicePrefix, http.StatusMovedPermanently)
			return
		}
		if strings.HasPrefix(r.URL.Path, kvmDevicePrefix) {
			servePublicFile(w, r, kvmRoot, strings.TrimPrefix(r.URL.Path, kvmDevicePrefix))
			return
		}
		if r.URL.Path != "/sgcc-captcha.html" {
			next.ServeHTTP(w, r)
			return
		}
		raw, err := os.ReadFile(filepath.Join(publicDir, "sgcc-captcha.html"))
		if err != nil {
			http.Error(w, "滑块接力页未安装", http.StatusServiceUnavailable)
			return
		}
		appID := ""
		if cfg != nil {
			appID, _ = cfg.Get(r.Context(), "SGCC_DX_APPID")
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write([]byte(strings.ReplaceAll(string(raw), "__SGCC_DX_APPID__", appID)))
	})
}

// servePublicFile 只返回 root 下的普通文件：清理 .. 防止越出素材目录，目录不列举。
func servePublicFile(w http.ResponseWriter, r *http.Request, root, rel string) {
	clean := filepath.Clean("/" + rel)
	candidate := filepath.Join(root, filepath.FromSlash(strings.TrimPrefix(clean, "/")))
	info, err := os.Stat(candidate)
	if err != nil || info.IsDir() {
		http.NotFound(w, r)
		return
	}
	http.ServeFile(w, r, candidate)
}
