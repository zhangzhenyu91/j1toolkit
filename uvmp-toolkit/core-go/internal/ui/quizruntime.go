package ui

import (
	"errors"
	"os"
	"path/filepath"

	"github.com/zhangzhenyu91/j1toolkit/uvmp-toolkit/core-go/internal/ocr"
	"github.com/zhangzhenyu91/j1toolkit/uvmp-toolkit/core-go/internal/overlay"
	"github.com/zhangzhenyu91/j1toolkit/uvmp-toolkit/core-go/internal/quizsearch"
	"github.com/zhangzhenyu91/j1toolkit/uvmp-toolkit/core-go/internal/state"
)

// 本文件：题库搜题运行时装配——OCR 模型目录/ORT 推理库路径解析 + 原生 overlay.Host
// + ocr.Engine→quizsearch.Engine 适配（quizsearch.NewOCREngineFactory），供桌面壳/自检复用。
// ORT 会话工厂按构建标签拆分（quizruntime_ort_onnx.go / quizruntime_ort_stub.go），
// 非 onnx 构建明确报错、不冒充可用。

// 模型目录必需文件 = quizsearch.ModelFiles 自检四文件口径（det/rec/cls/词表，与旧版一致；cls 随模型提供）。

// QuizScannerOptions 扫描服务装配参数（零值=按环境/默认探测）。
type QuizScannerOptions struct {
	// DataDir 题库与设置目录；空=ResolveDataDir（旧 Electron 目录原地可读优先）。
	DataDir string
	// ModelsDir OCR 模型目录；空=QuizModelsDir 自动探测。
	ModelsDir string
	// Host 悬浮窗/截屏宿主；空=overlay.NewHost 原生宿主（测试注入 fake）。
	Host overlay.Host
	// NewEngine 引擎工厂；空=quizsearch.NewOCREngineFactory(ORT 工厂)。
	NewEngine quizsearch.EngineFactory
}

// QuizModelsDir 解析 OCR 模型目录（必需 quizsearch.ModelFiles 四文件：det/rec/cls/词表）。
// 探测顺序：UVMP_OCR_MODELS → exe 旁 models/ → 上溯两级 models/ →
// 开发仓 desktop/electron/quizsearch/models → 打包 resources 布局。
func QuizModelsDir() (string, error) {
	if p := os.Getenv("UVMP_OCR_MODELS"); p != "" {
		if abs, err := filepath.Abs(p); err == nil && hasModelFiles(abs) {
			return abs, nil
		}
		return "", errors.New("UVMP_OCR_MODELS 指定的模型目录不完整（需 det.onnx/rec.onnx/cls.onnx/ppocr_keys.txt）")
	}
	roots := []string{exeDir()}
	if wd, err := os.Getwd(); err == nil {
		roots = append(roots, wd)
	}
	for _, root := range roots {
		for _, dir := range []string{
			root,
			filepath.Join(root, ".."),
			filepath.Join(root, "..", ".."),
			filepath.Join(root, "electron", "quizsearch", "models"),
			filepath.Join(root, "..", "desktop", "electron", "quizsearch", "models"),
			filepath.Join(root, "models"),
		} {
			if abs, err := filepath.Abs(dir); err == nil && hasModelFiles(abs) {
				return abs, nil
			}
		}
	}
	return "", errors.New("未找到 OCR 模型目录（需 det.onnx/rec.onnx/cls.onnx/ppocr_keys.txt；可用 UVMP_OCR_MODELS 指定）")
}

func hasModelFiles(dir string) bool {
	for _, f := range quizsearch.ModelFiles {
		if st, err := os.Stat(filepath.Join(dir, f)); err != nil || st.IsDir() {
			return false
		}
	}
	return true
}

// NewQuizScanner 装配生产扫描服务（A/B 双框 OCR 搜题）：原生悬浮窗宿主 + ORT OCR 引擎。
// 非 onnx 构建 / 缺模型 / 平台无原生悬浮窗时明确报错，不伪造空壳。
func NewQuizScanner(opts QuizScannerOptions) (*quizsearch.Scanner, error) {
	modelsDir := opts.ModelsDir
	if modelsDir == "" {
		var err error
		modelsDir, err = QuizModelsDir()
		if err != nil {
			return nil, err
		}
	} else if !hasModelFiles(modelsDir) {
		return nil, errors.New("OCR 模型目录不完整（需 det.onnx/rec.onnx/cls.onnx/ppocr_keys.txt）：" + modelsDir)
	}
	host := opts.Host
	if host == nil {
		var err error
		host, err = overlay.NewHost()
		if err != nil {
			return nil, err
		}
	}
	newEngine := opts.NewEngine
	if newEngine == nil {
		factory, err := newSessionFactory(modelsDir)
		if err != nil {
			return nil, err
		}
		newEngine = quizsearch.NewOCREngineFactory(factory)
	}
	dataDir := opts.DataDir
	if dataDir == "" {
		dataDir = quizsearch.ResolveDataDir(state.StateDir())
	}
	return quizsearch.New(quizsearch.Config{
		DataDir:   dataDir,
		ModelsDir: modelsDir,
		NewEngine: newEngine,
		Host:      host,
	})
}

// QuizEngineSelftest 真实引擎自检（模型齐备 + ORT 运行时加载 + 可选 golden 识别），
// 供设置页/排障调用；非 onnx 构建报「引擎未编入」而非假装通过。
func QuizEngineSelftest() (map[string]any, error) {
	modelsDir, err := QuizModelsDir()
	if err != nil {
		return nil, err
	}
	factory, err := newSessionFactory(modelsDir)
	if err != nil {
		return nil, err
	}
	out := map[string]any{"ok": true, "models_dir": modelsDir, "engine": "ocr.Engine"}
	if v, ok := factory.(ocr.RuntimeVersioner); ok {
		out["ort_version"] = v.RuntimeVersion()
	}
	// 最小加载探针：det/rec 会话创建即验证 ONNX Runtime 与模型可加载
	det, err := factory.OpenDet(modelsDir)
	if err != nil {
		out["ok"] = false
		out["error"] = err.Error()
		return out, nil
	}
	_ = det.Close()
	rec, err := factory.OpenRec(modelsDir)
	if err != nil {
		out["ok"] = false
		out["error"] = err.Error()
		return out, nil
	}
	_ = rec.Close()
	return out, nil
}
