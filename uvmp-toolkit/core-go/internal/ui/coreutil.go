package ui

import (
	"os"
	"strings"
)

// 本文件：CoreBackend 与解码器共用的轻量取值小工具（无 IO 语义的在 decode.go）。

// readFileText 读文本文件（非法字节替换，同 rpc 口径）。
func readFileText(p string) (string, error) {
	data, err := os.ReadFile(p)
	if err != nil {
		return "", err
	}
	return strings.ToValidUTF8(string(data), "\uFFFD"), nil
}

// dirStat 目录下文件数与总字节。
func dirStat(dir string) (int, int64) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0, 0
	}
	n, total := 0, int64(0)
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		n++
		if info, err := e.Info(); err == nil {
			total += info.Size()
		}
	}
	return n, total
}
