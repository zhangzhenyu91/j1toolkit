package wmadd

import (
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"time"

	"github.com/go-chi/chi/v5"
)

const renderResultTTL = 5 * time.Minute

var renderIDRe = regexp.MustCompile(`^[a-f0-9]{32}$`)

func (m *Module) renderDir() string {
	if m.DataDir != "" {
		return m.DataDir
	}
	return filepath.Join("data", "wmadd")
}

// saveRenderResult 渲染结果短时落盘，以下载地址代替 base64 响应。
func (m *Module) saveRenderResult(buf []byte) (string, string, error) {
	dir := m.renderDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", "", err
	}
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", "", err
	}
	id := hex.EncodeToString(raw[:])
	name := id + ".jpg"
	if err := os.WriteFile(filepath.Join(dir, name), buf, 0o600); err != nil {
		return "", "", err
	}
	time.AfterFunc(renderResultTTL, func() { _ = os.Remove(filepath.Join(dir, name)) })
	// 重启后的遗留结果也按一小时兜底清理。
	cutoff := time.Now().Add(-time.Hour)
	if entries, err := os.ReadDir(dir); err == nil {
		for _, entry := range entries {
			if info, err := entry.Info(); err == nil && info.ModTime().Before(cutoff) {
				_ = os.Remove(filepath.Join(dir, entry.Name()))
			}
		}
	}
	return id, "/api/v1/wmadd/render/" + id, nil
}

// serveRenderResult 下载短时渲染结果（仍需登录与 wm-add 权限）。
func (m *Module) serveRenderResult(w http.ResponseWriter, r *http.Request) {
	if _, ok := m.enter(w, r); !ok {
		return
	}
	id := chi.URLParam(r, "id")
	if !renderIDRe.MatchString(id) {
		fail(w, http.StatusNotFound, codeParam, "渲染结果不存在或已过期")
		return
	}
	name := id + ".jpg"
	path := filepath.Join(m.renderDir(), name)
	if _, err := os.Stat(path); err != nil {
		fail(w, http.StatusNotFound, codeParam, "渲染结果不存在或已过期")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("Content-Disposition", `attachment; filename="watermark-`+name+`"`)
	time.AfterFunc(renderResultTTL, func() { _ = os.Remove(path) })
	http.ServeFile(w, r, path)
}
