package httpapi

import (
	"bytes"
	"errors"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"net/http"
	"strings"
)

// ImageUploadMaxBytes 单张图片统一上限（50 MiB）。
const ImageUploadMaxBytes = 50 << 20

var (
	ErrImageRequired = errors.New("请选择要上传的图片")
	ErrImageTooLarge = errors.New("图片大小应在 50MB 以内")
	ErrImageFormat   = errors.New("图片格式不受支持")
	ErrMultipart     = errors.New("请求应为 multipart/form-data")
)

// MultipartImage 二进制图片与表单字段（不经过 JSON/base64，不写临时文件）。
type MultipartImage struct {
	Buf         []byte
	ContentType string
	Values      map[string][]string
}

// Value 取首个表单字段值。
func (m *MultipartImage) Value(key string) string {
	if m == nil {
		return ""
	}
	if values := m.Values[key]; len(values) > 0 {
		return values[0]
	}
	return ""
}

// ReadMultipartImage 流式解析 multipart 图片及小表单字段。
// field 为文件字段名；allowed 为允许的真实 MIME（按文件头识别，不信任客户端声明）。
func ReadMultipartImage(w http.ResponseWriter, r *http.Request, field string, allowed map[string]bool) (*MultipartImage, error) {
	if r == nil || !strings.HasPrefix(strings.ToLower(r.Header.Get("Content-Type")), "multipart/form-data") {
		return nil, ErrMultipart
	}
	r.Body = http.MaxBytesReader(w, r.Body, ImageUploadMaxBytes+(1<<20))
	mr, err := r.MultipartReader()
	if err != nil {
		return nil, ErrMultipart
	}
	out := &MultipartImage{Values: map[string][]string{}}
	for {
		part, err := mr.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		name := part.FormName()
		if name == field && part.FileName() != "" && out.Buf == nil {
			buf, readErr := io.ReadAll(io.LimitReader(part, ImageUploadMaxBytes+1))
			_ = part.Close()
			if readErr != nil {
				return nil, readErr
			}
			if len(buf) == 0 {
				return nil, ErrImageRequired
			}
			if len(buf) > ImageUploadMaxBytes {
				return nil, ErrImageTooLarge
			}
			contentType := http.DetectContentType(buf)
			if !allowed[contentType] {
				return nil, ErrImageFormat
			}
			if contentType != "image/webp" {
				cfg, _, err := image.DecodeConfig(bytes.NewReader(buf))
				if err != nil {
					return nil, ErrImageFormat
				}
				if int64(cfg.Width)*int64(cfg.Height) > 100_000_000 {
					return nil, fmt.Errorf("%w：图片像素不得超过 1 亿", ErrImageTooLarge)
				}
			}
			out.Buf = buf
			out.ContentType = contentType
			continue
		}
		// 普通业务字段限制 1 MiB；其余文件字段不接收。
		if part.FileName() == "" && name != "" {
			raw, readErr := io.ReadAll(io.LimitReader(part, (1<<20)+1))
			_ = part.Close()
			if readErr != nil {
				return nil, readErr
			}
			if len(raw) > 1<<20 {
				return nil, ErrMultipart
			}
			out.Values[name] = append(out.Values[name], string(raw))
			continue
		}
		_ = part.Close()
	}
	if out.Buf == nil {
		return nil, ErrImageRequired
	}
	return out, nil
}
