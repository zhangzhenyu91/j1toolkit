//go:build !onnx

package ui

import (
	"errors"

	"github.com/zhangzhenyu91/j1toolkit/uvmp-toolkit/core-go/internal/ocr"
)

// newSessionFactory 非 onnx 构建：OCR 推理未编入，明确报错不冒充可用
// （ORT 走共享库加载、无需 CGO；构建加 -tags onnx 即可启用）。
func newSessionFactory(string) (ocr.SessionFactory, error) {
	return nil, errors.New("OCR 引擎未编入：请以 -tags onnx 构建（ONNX Runtime 共享库加载，无需 CGO）")
}
