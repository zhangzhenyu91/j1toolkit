//go:build onnx

package ort

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync"

	yalue "github.com/yalue/onnxruntime_go"

	"github.com/zhangzhenyu91/j1toolkit/uvmp-toolkit/core-go/internal/ocr"
)

// 环境初始化（yalue 内部为进程级单一 OrtEnv）：全局一次，幂等。
var (
	initMu   sync.Mutex
	initDone bool
	initErr  error
	libUsed  string
)

// SharedLibraryName 当前平台的 ONNX Runtime 共享库文件名（onnxruntime-node 发行名）。
func SharedLibraryName() string {
	switch runtime.GOOS {
	case "windows":
		return "onnxruntime.dll"
	case "darwin":
		return "libonnxruntime.1.20.1.dylib"
	default:
		return "libonnxruntime.so.1.20.1"
	}
}

// nodeArch onnxruntime-node 目录布局中的架构名（bin/napi-v3/<os>/<arch>）。
func nodeArch() string {
	if runtime.GOARCH == "arm64" {
		return "arm64"
	}
	return "x64"
}

// nodeOS onnxruntime-node 目录布局中的平台名。
func nodeOS() string {
	switch runtime.GOOS {
	case "windows":
		return "win32"
	case "darwin":
		return "darwin"
	default:
		return "linux"
	}
}

// FindSharedLibrary 按序探测 ONNX Runtime 共享库，返回首个存在的绝对路径。
// 探测顺序：UVMP_ORT_LIB 环境变量（显式指定）→ 各 searchRoots 下的
// node_modules/onnxruntime-node/bin/napi-v3/<os>/<arch>/<库名>（含相对上溯两级，
// 覆盖开发仓 desktop/ 与打包后 resources/app.asar.unpacked/ 布局）→
// searchRoots 目录内直接存放的 <库名>。全部未命中返回错误。
func FindSharedLibrary(searchRoots ...string) (string, error) {
	name := SharedLibraryName()
	if p := os.Getenv("UVMP_ORT_LIB"); p != "" {
		if abs, err := filepath.Abs(p); err == nil {
			if st, err := os.Stat(abs); err == nil && !st.IsDir() {
				return abs, nil
			}
		}
	}
	rel := filepath.Join("node_modules", "onnxruntime-node", "bin", "napi-v3", nodeOS(), nodeArch(), name)
	for _, root := range searchRoots {
		if root == "" {
			continue
		}
		for _, dir := range []string{root, filepath.Join(root, ".."), filepath.Join(root, "..", "..")} {
			for _, cand := range []string{filepath.Join(dir, rel), filepath.Join(dir, name)} {
				if abs, err := filepath.Abs(cand); err == nil {
					if st, err := os.Stat(abs); err == nil && !st.IsDir() {
						return abs, nil
					}
				}
			}
		}
	}
	return "", fmt.Errorf("ort: 未找到 ONNX Runtime 共享库 %s（可用 UVMP_ORT_LIB 指定）", name)
}

// Init 运行时加载 ONNX Runtime 共享库并初始化全局环境（幂等；已初始化则忽略参数）。
// libPath 为空时按 FindSharedLibrary(searchRoots...) 探测。
func Init(libPath string, searchRoots ...string) error {
	initMu.Lock()
	defer initMu.Unlock()
	if initDone {
		return initErr
	}
	if yalue.IsInitialized() {
		initDone = true
		libUsed = "(已由进程内他处初始化)"
		return nil
	}
	p := libPath
	if p == "" {
		found, err := FindSharedLibrary(searchRoots...)
		if err != nil {
			initErr = err
			initDone = true
			return err
		}
		p = found
	}
	yalue.SetSharedLibraryPath(p)
	if err := yalue.InitializeEnvironment(); err != nil {
		initErr = fmt.Errorf("ort: 初始化 ONNX Runtime 失败（%s）: %w", p, err)
		initDone = true
		return initErr
	}
	libUsed = p
	initDone = true
	return nil
}

// RuntimeVersion 已加载的 ONNX Runtime 版本串（未初始化时为空）。
func RuntimeVersion() string {
	initMu.Lock()
	defer initMu.Unlock()
	if !yalue.IsInitialized() {
		return ""
	}
	return yalue.GetVersion()
}

// SharedLibraryPath 实际加载的共享库路径（Init 成功后可查）。
func SharedLibraryPath() string {
	initMu.Lock()
	defer initMu.Unlock()
	return libUsed
}

// Factory ocr.SessionFactory 的 ORT 实现（det/rec 会话工厂）。
type Factory struct {
	// LibPath 显式共享库路径；空则按 FindSharedLibrary(modelsDir) 探测。
	LibPath string
	// SearchRoots 共享库探测根目录（通常传 modelsDir 与应用根目录）。
	SearchRoots []string
}

var _ ocr.SessionFactory = (*Factory)(nil)
var _ ocr.RuntimeVersioner = (*Factory)(nil)

// OpenDet 加载 det 会话（modelsDir/det.onnx；输入/输出名取模型声明的第一个，
// 同 JS det.inputNames[0]/outputNames[0]）。
func (f *Factory) OpenDet(modelsDir string) (ocr.DetSession, error) {
	s, err := f.open(modelsDir, "det.onnx")
	if err != nil {
		return nil, err
	}
	return &detSession{s: s}, nil
}

// OpenRec 加载 rec 会话（modelsDir/rec.onnx）。
func (f *Factory) OpenRec(modelsDir string) (ocr.RecSession, error) {
	s, err := f.open(modelsDir, "rec.onnx")
	if err != nil {
		return nil, err
	}
	return &recSession{s: s}, nil
}

// RuntimeVersion ONNX Runtime 版本串（selftest 报告用）。
func (f *Factory) RuntimeVersion() string { return RuntimeVersion() }

// open 加载单模型动态会话（每次 Run 可变形状输入，输出由 ORT 自动分配）。
func (f *Factory) open(modelsDir, modelFile string) (*yalue.DynamicAdvancedSession, error) {
	roots := append([]string{modelsDir}, f.SearchRoots...)
	if err := Init(f.LibPath, roots...); err != nil {
		return nil, err
	}
	modelPath := filepath.Join(modelsDir, modelFile)
	ins, outs, err := yalue.GetInputOutputInfo(modelPath)
	if err != nil {
		return nil, fmt.Errorf("ort: 读取 %s 输入输出信息失败: %w", modelFile, err)
	}
	if len(ins) == 0 || len(outs) == 0 {
		return nil, fmt.Errorf("ort: %s 输入输出声明为空", modelFile)
	}
	// options 传 nil 用默认会话选项（ORT 默认即全图优化档，同 JS
	// graphOptimizationLevel:'all'；yalue 未暴露该开关）。
	s, err := yalue.NewDynamicAdvancedSession(modelPath,
		[]string{ins[0].Name}, []string{outs[0].Name}, nil)
	if err != nil {
		return nil, fmt.Errorf("ort: 创建 %s 会话失败: %w", modelFile, err)
	}
	return s, nil
}

// detSession DBNet 文本检测会话（ocr.DetSession）。
type detSession struct {
	s *yalue.DynamicAdvancedSession
}

var _ ocr.DetSession = (*detSession)(nil)

// RunDet 推理 NCHW [1,3,dh,dw] → 概率图 [1,1,dh,dw] 展平。
func (d *detSession) RunDet(nchw []float32, dw, dh int) ([]float32, error) {
	n := 3 * dw * dh
	if dw <= 0 || dh <= 0 || len(nchw) < n {
		return nil, fmt.Errorf("ort: det 输入尺寸非法（dw=%d dh=%d len=%d）", dw, dh, len(nchw))
	}
	in, err := yalue.NewTensor(yalue.NewShape(1, 3, int64(dh), int64(dw)), nchw[:n])
	if err != nil {
		return nil, fmt.Errorf("ort: det 输入张量创建失败: %w", err)
	}
	defer in.Destroy() //nolint:errcheck // 销毁失败不影响本次结果
	out, err := runAllocated(d.s, in)
	if err != nil {
		return nil, fmt.Errorf("ort: det 推理失败: %w", err)
	}
	defer out.Destroy() //nolint:errcheck
	prob := make([]float32, dw*dh)
	copy(prob, out.GetData())
	return prob, nil
}

// Close 释放 det 会话。
func (d *detSession) Close() error { return d.s.Destroy() }

// recSession CTC 行文字识别会话（ocr.RecSession）。
type recSession struct {
	s *yalue.DynamicAdvancedSession
}

var _ ocr.RecSession = (*recSession)(nil)

// RunRec 推理 NCHW [1,3,h,w] → logits [1,T,C] 展平并返回 T、C。
func (r *recSession) RunRec(nchw []float32, w, h int) ([]float32, int, int, error) {
	n := 3 * w * h
	if w <= 0 || h <= 0 || len(nchw) < n {
		return nil, 0, 0, fmt.Errorf("ort: rec 输入尺寸非法（w=%d h=%d len=%d）", w, h, len(nchw))
	}
	in, err := yalue.NewTensor(yalue.NewShape(1, 3, int64(h), int64(w)), nchw[:n])
	if err != nil {
		return nil, 0, 0, fmt.Errorf("ort: rec 输入张量创建失败: %w", err)
	}
	defer in.Destroy() //nolint:errcheck
	out, err := runAllocated(r.s, in)
	if err != nil {
		return nil, 0, 0, fmt.Errorf("ort: rec 推理失败: %w", err)
	}
	defer out.Destroy() //nolint:errcheck
	shape := out.GetShape()
	if len(shape) != 3 {
		return nil, 0, 0, fmt.Errorf("ort: rec 输出维度应为 [1,T,C]，实得 %v", []int64(shape))
	}
	t, c := int(shape[1]), int(shape[2])
	logits := make([]float32, t*c)
	copy(logits, out.GetData())
	return logits, t, c, nil
}

// Close 释放 rec 会话。
func (r *recSession) Close() error { return r.s.Destroy() }

// runAllocated 以 nil 输出槽运行动态会话，返回 ORT 自动分配的 float32 输出张量。
func runAllocated(s *yalue.DynamicAdvancedSession, in *yalue.Tensor[float32]) (*yalue.Tensor[float32], error) {
	outs := []yalue.Value{nil}
	if err := s.Run([]yalue.Value{in}, outs); err != nil {
		return nil, err
	}
	out, ok := outs[0].(*yalue.Tensor[float32])
	if !ok {
		if outs[0] != nil {
			_ = outs[0].Destroy()
		}
		return nil, fmt.Errorf("输出类型非 float32 张量")
	}
	return out, nil
}
