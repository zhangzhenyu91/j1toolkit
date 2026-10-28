package render

import (
	"bytes"
	"io"
	"strings"
	"sync"
)

// redactor 日志/诊断脱敏：把已知凭证片段替换成 ***，保证令牌不落日志。
type redactor struct {
	mu      sync.Mutex
	secrets []string
}

const redactMask = "***"

// add 登记需要脱敏的机密片段（空串忽略）。
func (r *redactor) add(s string) {
	if s == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, e := range r.secrets {
		if e == s {
			return
		}
	}
	r.secrets = append(r.secrets, s)
}

// apply 返回脱敏后的文本。
func (r *redactor) apply(s string) string {
	r.mu.Lock()
	secrets := r.secrets
	r.mu.Unlock()
	for _, e := range secrets {
		s = strings.ReplaceAll(s, e, redactMask)
	}
	return s
}

// redactingWriter 行缓冲的脱敏写入器：浏览器 stderr 等原始输出按行过脱敏再写入 dst，
// 防止令牌随崩溃/导航日志写进日志文件。
type redactingWriter struct {
	mu      sync.Mutex
	red     *redactor
	dst     io.Writer
	partial []byte
}

func newRedactingWriter(red *redactor, dst io.Writer) *redactingWriter {
	return &redactingWriter{red: red, dst: dst}
}

// Write 暂存半行，凑齐整行才脱敏写出。
func (w *redactingWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.partial = append(w.partial, p...)
	for {
		i := bytes.IndexByte(w.partial, '\n')
		if i < 0 {
			break
		}
		line := w.partial[:i+1]
		w.partial = w.partial[i+1:]
		if _, err := io.WriteString(w.dst, w.red.apply(string(line))); err != nil {
			return len(p), err
		}
	}
	return len(p), nil
}

// Flush 输出未满一行的残留（浏览器退出后调用）。
func (w *redactingWriter) Flush() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.partial) > 0 {
		_, err := io.WriteString(w.dst, w.red.apply(string(w.partial)))
		w.partial = nil
		return err
	}
	return nil
}
