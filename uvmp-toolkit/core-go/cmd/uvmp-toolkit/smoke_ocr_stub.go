//go:build !onnx

package main

import (
	"fmt"
	"os"

	"github.com/zhangzhenyu91/j1toolkit/uvmp-toolkit/core-go/internal/ui"
)

// smokeOCR OCR 冒烟（非 onnx 构建）：OCR 推理未编入，以非 0 退出（契约要求
// GUI 以 -tags onnx 构建，见 packaging/fyne/README.md）。报错口径复用
// ui.QuizEngineSelftest 的未编入提示。
func smokeOCR() int {
	_, err := ui.QuizEngineSelftest()
	if err != nil {
		fmt.Fprintf(os.Stderr, "OCR 冒烟失败：%v（当前为非 onnx 构建，须以 -tags onnx 构建才能跑推理）\n", err)
		return 1
	}
	fmt.Fprintln(os.Stderr, "OCR 冒烟失败：当前构建未编入 OCR 推理，请以 -tags onnx 构建")
	return 1
}
