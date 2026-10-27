package callme

// 共享小工具：文件名 latin1 还原、限长读、心跳 ticker。

import (
	"io"
	"strings"
	"time"
)

// readAllLimit 限长读全量。
func readAllLimit(r io.Reader, limit int64) ([]byte, error) {
	return io.ReadAll(io.LimitReader(r, limit))
}

// fixLatin1Name multer 默认按 latin1 解析文件名，中文名转回 UTF-8。
func fixLatin1Name(name string) string {
	if name == "" {
		return name
	}
	// 尝试按 latin1 → UTF-8 还原；往返一致说明原本就是 ASCII。
	b := make([]byte, 0, len(name))
	for _, r := range name {
		if r > 255 {
			return name
		}
		b = append(b, byte(r))
	}
	restored := string(b)
	if restored == name {
		return name
	}
	if !strings.ContainsRune(restored, 0xFFFD) {
		return restored
	}
	return name
}

// newTicker 秒级心跳 ticker 包装。
func newTicker(seconds int) *time.Ticker { return time.NewTicker(time.Duration(seconds) * time.Second) }
