package ocr

import (
	"os"
	"regexp"
	"runtime"
)

// avx2FlagRe 与 JS 同口径：/(^|\s)avx2(\s|$)/m（/proc/cpuinfo 的 flags 行）。
var avx2FlagRe = regexp.MustCompile(`(^|\s)avx2(\s|$)`)

// AVX2Flag CPU 指令集体检：linux 读 /proc/cpuinfo 判 AVX2（onnxruntime 预编译内核
// 在无 AVX2 的老 CPU 上会在推理时崩 SIGILL，先体检再跑，同 JS selftest 口径）；
// 非 linux 返回 nil（未知）。selftest 报告亦带此标志。
func AVX2Flag() *bool {
	if runtime.GOOS != "linux" {
		return nil
	}
	data, err := os.ReadFile("/proc/cpuinfo")
	if err != nil {
		return nil
	}
	return ParseAVX2CPUInfo(string(data))
}

// ParseAVX2CPUInfo 解析 /proc/cpuinfo 文本（与 JS 正则同语义；纯函数便于对拍）。
func ParseAVX2CPUInfo(content string) *bool {
	v := avx2FlagRe.MatchString(content)
	return &v
}
