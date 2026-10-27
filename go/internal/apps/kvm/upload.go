// multipart 转发小工具：把上传文件重新组装为 multipart/form-data 写入设备 fileshare /push。
package kvm

import (
	"io"
	"mime/multipart"
)

// multipartWriter 包装 multipart.Writer，记录首个错误供管道写协程短路。
type multipartWriter struct {
	w   *multipart.Writer
	err error
}

// multipartWriter 构造（pw 为管道写端；contentType 供请求头使用）。
func newMultipartWriter(pw *io.PipeWriter) *multipartWriter {
	return &multipartWriter{w: multipart.NewWriter(pw)}
}

// contentType multipart/form-data; boundary=...
func (m *multipartWriter) contentType() string { return m.w.FormDataContentType() }

// writeFile 写一个 files 字段文件（字段名与 Node FormData.append('files', ...) 一致）。
func (m *multipartWriter) writeFile(field, name string, r io.Reader) error {
	if m.err != nil {
		return m.err
	}
	part, err := m.w.CreateFormFile(field, name)
	if err != nil {
		m.err = err
		return err
	}
	_, err = io.Copy(part, r)
	m.err = err
	return err
}

// close 正常收尾（写结束边界）。
func (m *multipartWriter) close() error {
	if m.err != nil {
		return m.err
	}
	return m.w.Close()
}

// abort 记录错误（后续写入短路）。
func (m *multipartWriter) abort(err error) {
	if m.err == nil {
		m.err = err
	}
}
