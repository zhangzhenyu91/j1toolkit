package adminapi

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// NoticeImageStore 通知配图对象存储，由 COSStore 实现。
type NoticeImageStore interface {
	PutBuffer(ctx context.Context, key string, data []byte, contentType string) error
	PublicURL(key string) string
}

var noticeImageTypes = map[string]string{
	"image/jpeg": ".jpg",
	"image/png":  ".png",
	"image/gif":  ".gif",
	"image/webp": ".webp",
}

func (m *Module) noticeImageUpload(w http.ResponseWriter, r *http.Request) {
	ident, ok := m.ident(w, r)
	if !ok || !m.requireAdmin(w, ident) {
		return
	}
	if m.COS == nil {
		fail(w, http.StatusServiceUnavailable, codeInternal, "未配置对象存储，无法上传图片")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 51<<20)
	if err := r.ParseMultipartForm(51 << 20); err != nil {
		fail(w, http.StatusBadRequest, 40030, "图片大小应在 50MB 以内")
		return
	}
	file, _, err := r.FormFile("file")
	if err != nil {
		fail(w, http.StatusBadRequest, 40030, "请选择要上传的图片")
		return
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, 50<<20+1))
	if err != nil || len(data) == 0 {
		fail(w, http.StatusBadRequest, 40030, "请选择要上传的图片")
		return
	}
	if len(data) > 50<<20 {
		fail(w, http.StatusBadRequest, 40030, "图片大小应在 50MB 以内")
		return
	}
	contentType := http.DetectContentType(data)
	ext, supported := noticeImageTypes[contentType]
	if !supported {
		fail(w, http.StatusBadRequest, 40030, "仅支持 JPG/PNG/GIF/WebP 图片")
		return
	}
	var nonce [4]byte
	_, _ = rand.Read(nonce[:])
	key := fmt.Sprintf("notice/%s/%d-%s%s", time.Now().Format("200601"), time.Now().UnixMilli(), hex.EncodeToString(nonce[:]), ext)
	if err := m.COS.PutBuffer(r.Context(), key, data, contentType); err != nil {
		writeErr(w, err)
		return
	}
	url := strings.TrimSpace(m.COS.PublicURL(key))
	if url == "" {
		writeErr(w, fmt.Errorf("对象存储未配置访问地址"))
		return
	}
	writeOK(w, map[string]string{"url": url})
}
