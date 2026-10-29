//go:build onnx

package ui

import (
	"os"

	"github.com/zhangzhenyu91/j1toolkit/uvmp-toolkit/core-go/internal/ocr"
	"github.com/zhangzhenyu91/j1toolkit/uvmp-toolkit/core-go/internal/ocr/ort"
)

// newSessionFactory ORT 会话工厂（onnx 构建）：共享库 UVMP_ORT_LIB 显式指定，
// 否则按 modelsDir 旁 node_modules/onnxruntime-node 布局探测（ort.FindSharedLibrary）。
func newSessionFactory(modelsDir string) (ocr.SessionFactory, error) {
	return &ort.Factory{
		LibPath:     os.Getenv("UVMP_ORT_LIB"),
		SearchRoots: []string{modelsDir},
	}, nil
}
