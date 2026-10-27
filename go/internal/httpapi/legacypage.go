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

// WithLegacyPages 托管需要服务端注入配置的独立页面，不接管 Vue 路由。
// 顶象 AppID 每次请求读取，配置变更后立即生效，不写入静态产物。
func WithLegacyPages(next http.Handler, publicDir string, cfg ConfigValue) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
