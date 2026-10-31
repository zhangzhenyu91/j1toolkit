package httpapi

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// looksLikeStaticAsset 判断末段是否像带扩展名的静态文件（如 install.sh、app.js、logo.png）。
// SPA 的 history 路由不应含点号扩展名，缺失资源必须 404。
func looksLikeStaticAsset(path string) bool {
	name := path
	if i := strings.LastIndexByte(name, '/'); i >= 0 {
		name = name[i+1:]
	}
	return name != "" && !strings.HasPrefix(name, ".") && strings.Contains(name, ".")
}

// isLegacyHTMLPath 旧 Node 网页端的 *.html 深链（根级或嵌套）：文件缺失时必须回落 SPA，
// 由前端 legacy-urls 归一为 hash 路由；/share.html#/s/<id> 的 fragment 不发送到服务端，
// 同样依赖本回落。只认 .html，其他扩展名仍按静态资源 404。
func isLegacyHTMLPath(path string) bool {
	return strings.HasSuffix(strings.ToLower(path), ".html")
}

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
		// 带扩展名的静态资源缺失时必须 404，绝不能回落 index.html 返回 200 HTML：
		// 否则 /kvm-device/install.sh、assets/*.js 等会被 curl | sh / 浏览器当成真实文件。
		// 旧 *.html 深链例外：回落 SPA 后由前端 legacy-urls 跳 hash 路由（保持旧书签/分享链接可用）。
		if looksLikeStaticAsset(r.URL.Path) && !isLegacyHTMLPath(r.URL.Path) {
			http.NotFound(w, r)
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
