package httpapi

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// WithSPA 在 API 路由外提供 Vue SPA 静态资源和 history fallback。
// webDir 通常是 web/dist；未构建时返回明确的提示，不把目录错误伪装成 404。
func WithSPA(api http.Handler, webDir string) http.Handler {
	fileServer := http.FileServer(http.Dir(webDir))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") || r.URL.Path == "/healthz" {
			api.ServeHTTP(w, r)
			return
		}
		candidate := filepath.Join(webDir, filepath.Clean(r.URL.Path))
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			fileServer.ServeHTTP(w, r)
			return
		}
		index := filepath.Join(webDir, "index.html")
		if _, err := os.Stat(index); err != nil {
			http.Error(w, "Web SPA 尚未构建，请先执行 npm run build", http.StatusServiceUnavailable)
			return
		}
		http.ServeFile(w, r, index)
	})
}
