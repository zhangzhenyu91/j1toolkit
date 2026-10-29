//go:build onnx

package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/zhangzhenyu91/j1toolkit/uvmp-toolkit/core-go/internal/ocr"
	"github.com/zhangzhenyu91/j1toolkit/uvmp-toolkit/core-go/internal/ocr/ort"
	"github.com/zhangzhenyu91/j1toolkit/uvmp-toolkit/core-go/internal/quizsearch"
	"github.com/zhangzhenyu91/j1toolkit/uvmp-toolkit/core-go/internal/ui"
)

// smokeModelsDir 解析 OCR 模型目录：ui.QuizModelsDir（UVMP_OCR_MODELS → exe 旁
// models/ → desktop/electron/quizsearch/models 开发布局）优先；未命中再从可执行
// 目录/工作目录上溯补探开发仓 desktop/ 与安装 models/ 布局。显式指定不完整时
// 直接报错，不静默回退。
func smokeModelsDir() (string, error) {
	if os.Getenv("UVMP_OCR_MODELS") != "" {
		return ui.QuizModelsDir()
	}
	if dir, err := ui.QuizModelsDir(); err == nil {
		return dir, nil
	}
	bases := []string{}
	if exe, err := os.Executable(); err == nil {
		bases = append(bases, filepath.Dir(exe))
	}
	if wd, err := os.Getwd(); err == nil {
		bases = append(bases, wd)
	}
	for _, b := range bases {
		dir := b
		for i := 0; i < 6; i++ {
			for _, cand := range []string{
				filepath.Join(dir, "desktop", "electron", "quizsearch", "models"),
				filepath.Join(dir, "uvmp-toolkit", "desktop", "electron", "quizsearch", "models"),
				filepath.Join(dir, "electron", "quizsearch", "models"),
				filepath.Join(dir, "models"),
			} {
				if abs, err := filepath.Abs(cand); err == nil && smokeHasModels(abs) {
					return abs, nil
				}
			}
			parent := filepath.Dir(dir)
			if parent == dir {
				break
			}
			dir = parent
		}
	}
	return "", errors.New("未找到 OCR 模型目录（需 det.onnx/rec.onnx/cls.onnx/ppocr_keys.txt；可用 UVMP_OCR_MODELS 指定）")
}

// smokeHasModels 模型目录必需文件是否齐备（quizsearch.ModelFiles 四文件口径，
// 与 ui.QuizModelsDir/生产自检一致）。
func smokeHasModels(dir string) bool {
	for _, f := range quizsearch.ModelFiles {
		if st, err := os.Stat(filepath.Join(dir, f)); err != nil || st.IsDir() {
			return false
		}
	}
	return true
}

// smokeOCR OCR 冒烟（onnx 构建）：按安装相对 native/models 布局（兼容开发路径与
// UVMP_OCR_MODELS/UVMP_ORT_LIB 显式指定）真实加载 ONNX Runtime 与 det/rec 模型，
// 零张量推理跑通（ocr.Selftest：det 64x64 / rec 48x320 内置空白输入）后退出 0；
// 不开主窗（无显示环境可跑）。模型/库/推理任一步失败即退出非 0。
func smokeOCR() int {
	ctx := context.Background()

	modelsDir, err := smokeModelsDir()
	if err != nil {
		fmt.Fprintf(os.Stderr, "OCR 冒烟失败：%v\n", err)
		return 1
	}
	lib, err := resolveORTLib(modelsDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "OCR 冒烟失败：%v\n", err)
		return 1
	}
	// ui 侧自检按显式模型/共享库加载（ort.Init 进程级幂等，后续推理同一库）
	if err := os.Setenv("UVMP_OCR_MODELS", modelsDir); err != nil {
		fmt.Fprintf(os.Stderr, "OCR 冒烟失败：注入 UVMP_OCR_MODELS 失败: %v\n", err)
		return 1
	}
	if err := os.Setenv("UVMP_ORT_LIB", lib); err != nil {
		fmt.Fprintf(os.Stderr, "OCR 冒烟失败：注入 UVMP_ORT_LIB 失败: %v\n", err)
		return 1
	}
	fmt.Printf("OCR 冒烟：models=%s lib=%s\n", modelsDir, lib)

	// 生产自检路径复用（设置页/排障同一函数）：模型齐备 + ORT 运行时加载
	out, err := ui.QuizEngineSelftest()
	if err != nil {
		fmt.Fprintf(os.Stderr, "OCR 冒烟失败：%v\n", err)
		return 1
	}
	if ok, _ := out["ok"].(bool); !ok {
		fmt.Fprintf(os.Stderr, "OCR 冒烟失败：引擎自检未通过 %v\n", out["error"])
		return 1
	}
	fmt.Printf("OCR 冒烟：引擎自检通过 %v\n", out)

	// 契约要求：零张量推理跑通（det 64x64 / rec 48x320 内置空白输入）
	rep, err := ocr.Selftest(ctx, ocr.SelftestConfig{
		Factory:   &ort.Factory{LibPath: lib, SearchRoots: []string{modelsDir}},
		ModelsDir: modelsDir,
		Log:       func(line string) { fmt.Println(line) },
	})
	if err != nil || !rep.OK {
		fmt.Fprintf(os.Stderr, "OCR 冒烟失败：零张量推理未跑通 err=%v report_ok=%v\n", err, rep.OK)
		return 1
	}
	fmt.Printf("OCR 冒烟通过：ORT %s det=%v rec=%v vocab=%d\n",
		rep.ORTVersion, rep.DetOutDims, rep.RecOutDims, rep.VocabSize)
	return 0
}

// resolveORTLib 解析 ONNX Runtime 共享库：UVMP_ORT_LIB 显式指定 → 安装相对
// <exeDir>/native/<库名>（packaging/fyne 装配布局，launcher 另注入
// LD_LIBRARY_PATH=native）→ 开发布局（node_modules/onnxruntime-node 预编译库等，
// ort.FindSharedLibrary 探测）。
func resolveORTLib(modelsDir string) (string, error) {
	if p := strings.TrimSpace(os.Getenv("UVMP_ORT_LIB")); p != "" {
		abs, err := filepath.Abs(p)
		if err != nil {
			return "", fmt.Errorf("UVMP_ORT_LIB 路径非法（%s）: %w", p, err)
		}
		if st, err := os.Stat(abs); err != nil || st.IsDir() {
			return "", fmt.Errorf("UVMP_ORT_LIB 指定的共享库不存在: %s", p)
		}
		return abs, nil
	}
	name := ort.SharedLibraryName()
	bases := []string{modelsDir}
	if exe, err := os.Executable(); err == nil {
		bases = append(bases, filepath.Dir(exe))
	}
	if wd, err := os.Getwd(); err == nil {
		bases = append(bases, wd)
	}
	// 开发布局补根：desktop/node_modules/onnxruntime-node 预编译库（各基址上溯若干级）
	roots := []string{}
	for _, b := range bases {
		dir := b
		for i := 0; i < 5; i++ {
			roots = append(roots, dir,
				filepath.Join(dir, "desktop"),
				filepath.Join(dir, "uvmp-toolkit", "desktop"))
			parent := filepath.Dir(dir)
			if parent == dir {
				break
			}
			dir = parent
		}
	}
	for _, dir := range roots {
		if dir == "" {
			continue
		}
		for _, cand := range []string{
			filepath.Join(dir, "native", name),
			filepath.Join(dir, "..", "native", name),
		} {
			if abs, err := filepath.Abs(cand); err == nil {
				if st, err := os.Stat(abs); err == nil && !st.IsDir() {
					return abs, nil
				}
			}
		}
	}
	return ort.FindSharedLibrary(roots...)
}
