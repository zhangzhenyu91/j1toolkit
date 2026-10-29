// Package ort 是 ocr 会话层的 ONNX Runtime 实现：以 yalue/onnxruntime_go 在运行时
// 加载现有 ONNX Runtime 1.20.1 共享库（随 desktop/node_modules/onnxruntime-node
// 分发的 libonnxruntime.so.1.20.1 / onnxruntime.dll / libonnxruntime.1.20.1.dylib，
// 不做构建期链接），加载 PP-OCRv4 mobile 模型（det.onnx/rec.onnx），
// 提供 ocr.SessionFactory 实现（Factory）供 ocr.Engine / ocr.Selftest 注入。
//
// 启用方式：yalue/onnxruntime_go 为 cgo 包（需 CGO + C 工具链），本包的 ORT 实现
// 置于构建标签 onnx 之下（见 ort_onnx.go）：
//
//  1. core-go/go.mod 已钉 github.com/yalue/onnxruntime_go v1.16.0；
//  2. CGO_ENABLED=1 且安装 C 工具链（Linux/macOS：gcc；Windows：MinGW-w64）；
//  3. go build -tags onnx ./...。
//
// 默认（不带 -tags onnx）构建仅含本说明文件，保证 go test ./... 在无依赖、
// 无 CGO 环境下不受影响；ocr 纯函数层与 Engine 测试不需要本包。
package ort
