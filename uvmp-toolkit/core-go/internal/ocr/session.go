package ocr

// DetSession 文本检测会话（DBNet 概率图，模型 det.onnx）。
// 实现：ORT 会话见子包 ort（yalue/onnxruntime_go 运行时加载 ONNX Runtime 1.20.1
// 共享库），测试用 fake。
type DetSession interface {
	// RunDet 执行 det 推理：nchw 为 [1,3,dh,dw] 展平（长 3*dh*dw），返回概率图
	// [1,1,dh,dw] 展平（长 dh*dw）。
	RunDet(nchw []float32, dw, dh int) (prob []float32, err error)
	// Close 释放会话。
	Close() error
}

// RecSession 行文字识别会话（CTC，模型 rec.onnx）。
type RecSession interface {
	// RunRec 执行 rec 推理：nchw 为 [1,3,h,w] 展平（长 3*h*w），返回 logits
	// [1,T,C] 展平（长 T*C）与维度 T、C。
	RunRec(nchw []float32, w, h int) (logits []float32, timeSteps, classes int, err error)
	// Close 释放会话。
	Close() error
}

// SessionFactory det/rec 会话工厂（模型加载有耗时，会话常驻由 Engine 持有）。
type SessionFactory interface {
	// OpenDet 加载 det 会话（modelsDir/det.onnx）。
	OpenDet(modelsDir string) (DetSession, error)
	// OpenRec 加载 rec 会话（modelsDir/rec.onnx）。
	OpenRec(modelsDir string) (RecSession, error)
}

// RuntimeVersioner 可选能力：会话工厂报告推理运行时版本（selftest 报告用）。
type RuntimeVersioner interface {
	RuntimeVersion() string
}
