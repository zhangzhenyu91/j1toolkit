//go:build onnx

package ort

// ONNX 集成测试（ORT_IT=1 启用）：用本地 PP-OCRv4 模型与本地 ONNX Runtime 共享库
// 验证完整推理链（会话加载 → det/rec 动态形状真实推理 → det→后处理→rec→CTC 完整管线）。
// 纯逻辑测试（fake 会话的前处理/后处理/解码）见 internal/ocr 各 _test.go，此处只做
// 真实 ORT 集成口径、不重复。模型/库探测：UVMP_OCR_MODELS / UVMP_ORT_LIB 显式指定
// → 开发仓 desktop/ 布局 → 安装相对 native/models 布局。

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/zhangzhenyu91/j1toolkit/uvmp-toolkit/core-go/internal/ocr"
	"github.com/zhangzhenyu91/j1toolkit/uvmp-toolkit/core-go/internal/quizsearch"
)

// itSkip 未开集成测试开关时跳过（同 course.COURSE_IT 口径）。
func itSkip(t *testing.T) {
	t.Helper()
	if os.Getenv("ORT_IT") == "" {
		t.Skip("ONNX 集成测试默认跳过：设 ORT_IT=1 后运行（可用 UVMP_ORT_LIB/UVMP_OCR_MODELS 指定库与模型）")
	}
}

// assetRoots 资产探测根：测试源码所在仓树（runtime.Caller）与 go test 工作目录。
func assetRoots() []string {
	roots := []string{}
	appendTree := func(dir string) {
		roots = append(roots,
			filepath.Join(dir, "..", "..", "..", ".."),             // uvmp-toolkit/
			filepath.Join(dir, "..", "..", "..", "..", ".."),       // j1toolkit/
			filepath.Join(dir, "..", "..", "..", "..", "..", ".."), // 仓根上一级
		)
	}
	if _, file, _, ok := runtime.Caller(0); ok {
		appendTree(filepath.Dir(file))
	}
	if wd, err := os.Getwd(); err == nil {
		appendTree(wd)
	}
	return roots
}

// resolveITModels 定位本地模型目录（quizsearch.ModelFiles 四文件齐备）。
func resolveITModels(t *testing.T) string {
	t.Helper()
	if p := os.Getenv("UVMP_OCR_MODELS"); p != "" {
		if abs, err := filepath.Abs(p); err == nil && hasModels(abs) {
			return abs
		}
		t.Fatalf("UVMP_OCR_MODELS 指定的模型目录不完整（需 det.onnx/rec.onnx/cls.onnx/ppocr_keys.txt）: %s", p)
	}
	for _, root := range assetRoots() {
		for _, cand := range []string{
			filepath.Join(root, "desktop", "electron", "quizsearch", "models"),
			filepath.Join(root, "electron", "quizsearch", "models"),
			filepath.Join(root, "models"),
		} {
			if abs, err := filepath.Abs(cand); err == nil && hasModels(abs) {
				return abs
			}
		}
	}
	t.Fatal("未找到本地模型目录（需 det.onnx/rec.onnx/cls.onnx/ppocr_keys.txt；可用 UVMP_OCR_MODELS 指定）")
	return ""
}

// hasModels 模型目录必需文件是否齐备（quizsearch.ModelFiles 四文件口径，与生产一致）。
func hasModels(dir string) bool {
	for _, f := range quizsearch.ModelFiles {
		if st, err := os.Stat(filepath.Join(dir, f)); err != nil || st.IsDir() {
			return false
		}
	}
	return true
}

// resolveITLib 定位本地 ONNX Runtime 共享库：UVMP_ORT_LIB 显式 → 开发布局
// （ort.FindSharedLibrary，node_modules/onnxruntime-node 预编译库）→ 安装相对
// <root>/native/<库名>。
func resolveITLib(t *testing.T, modelsDir string) string {
	t.Helper()
	if p := os.Getenv("UVMP_ORT_LIB"); p != "" {
		if abs, err := filepath.Abs(p); err == nil {
			if st, err := os.Stat(abs); err == nil && !st.IsDir() {
				return abs
			}
		}
		t.Fatalf("UVMP_ORT_LIB 指定的共享库不存在: %s", p)
	}
	roots := append([]string{modelsDir}, assetRoots()...)
	// 开发布局补根：desktop/node_modules/onnxruntime-node 预编译库
	for _, r := range assetRoots() {
		roots = append(roots,
			filepath.Join(r, "desktop"),
			filepath.Join(r, "uvmp-toolkit", "desktop"),
		)
	}
	if p, err := FindSharedLibrary(roots...); err == nil {
		return p
	}
	name := SharedLibraryName()
	for _, root := range roots {
		if root == "" {
			continue
		}
		if abs, err := filepath.Abs(filepath.Join(root, "native", name)); err == nil {
			if st, err := os.Stat(abs); err == nil && !st.IsDir() {
				return abs
			}
		}
	}
	t.Fatal("未找到本地 ONNX Runtime 共享库（可用 UVMP_ORT_LIB 指定）")
	return ""
}

// assertFiniteAll 张量值全部有限（无 NaN/Inf）。
func assertFiniteAll(t *testing.T, name string, v []float32) {
	t.Helper()
	for i, x := range v {
		if math.IsNaN(float64(x)) || math.IsInf(float64(x), 0) {
			t.Fatalf("%s 第 %d 个值非有限: %v", name, i, x)
		}
	}
}

// TestORTIntegrationFullInference 真实模型 + 真实运行时的完整推理集成测试。
func TestORTIntegrationFullInference(t *testing.T) {
	itSkip(t)
	modelsDir := resolveITModels(t)
	lib := resolveITLib(t, modelsDir)
	t.Logf("models=%s", modelsDir)
	t.Logf("lib=%s", lib)

	if err := Init(lib, modelsDir); err != nil {
		t.Fatalf("ORT 初始化失败: %v", err)
	}
	if v := RuntimeVersion(); v == "" {
		t.Fatal("ORT 版本为空（运行时未加载）")
	}
	t.Logf("ONNX Runtime %s（%s）", RuntimeVersion(), SharedLibraryPath())

	f := &Factory{LibPath: lib, SearchRoots: []string{modelsDir}}

	keys, err := ocr.LoadKeys(filepath.Join(modelsDir, "ppocr_keys.txt"))
	if err != nil {
		t.Fatalf("读取词表失败: %v", err)
	}
	chars := ocr.BuildChars(keys)

	t.Run("det 动态形状推理", func(t *testing.T) {
		det, err := f.OpenDet(modelsDir)
		if err != nil {
			t.Fatalf("加载 det.onnx 失败: %v", err)
		}
		defer det.Close() //nolint:errcheck
		// 零张量两档形状：尺寸正确、数值有限（零输入经 DBNet 得全零概率图属正常）
		for _, tc := range []struct{ w, h int }{{64, 64}, {32, 96}} {
			prob, err := det.RunDet(make([]float32, 3*tc.w*tc.h), tc.w, tc.h)
			if err != nil {
				t.Fatalf("det 推理失败（%dx%d）: %v", tc.w, tc.h, err)
			}
			if len(prob) != tc.w*tc.h {
				t.Fatalf("det 输出长度 = %d，期望 %d", len(prob), tc.w*tc.h)
			}
			assertFiniteAll(t, "det 输出", prob)
			t.Logf("det 零张量 %dx%d 输出 %d 个概率值", tc.w, tc.h, len(prob))
		}
		// 真实前处理输入（内置黑字条纹图 vs 纯白空白图，同管线前处理同形状）：
		// 输出必须不同，证明模型对输入有响应、推理生效（小尺寸零输入经 sigmoid
		// 下溢可得精确全零，不能据此判失效）
		const sw, sh = 160, 48
		white := make([]byte, sw*sh*4)
		for i := 0; i < len(white); i += 4 {
			white[i], white[i+1], white[i+2], white[i+3] = 0xFF, 0xFF, 0xFF, 0xFF
		}
		stripes := append([]byte(nil), white...)
		for _, bar := range []struct{ y0, y1, x0, x1 int }{{8, 16, 10, 150}, {24, 32, 10, 120}} {
			for y := bar.y0; y < bar.y1; y++ {
				for x := bar.x0; x < bar.x1; x++ {
					o := (y*sw + x) * 4
					stripes[o], stripes[o+1], stripes[o+2] = 0, 0, 0
				}
			}
		}
		dw, dh, _ := ocr.DetPreprocessSize(sw, sh)
		prep := func(bgra []byte) []float32 {
			return ocr.PlanesToNCHW(ocr.ResizePlanes(ocr.BGRAToRGBPlanes(bgra, sw, sh), sw, sh, dw, dh), dw, dh)
		}
		whiteProb, err := det.RunDet(prep(white), dw, dh)
		if err != nil {
			t.Fatalf("det 推理失败（空白图 %dx%d）: %v", dw, dh, err)
		}
		stripeProb, err := det.RunDet(prep(stripes), dw, dh)
		if err != nil {
			t.Fatalf("det 推理失败（条纹图 %dx%d）: %v", dw, dh, err)
		}
		if len(whiteProb) != dw*dh || len(stripeProb) != dw*dh {
			t.Fatalf("det 输出长度 = %d/%d，期望 %d", len(whiteProb), len(stripeProb), dw*dh)
		}
		assertFiniteAll(t, "det 空白图输出", whiteProb)
		assertFiniteAll(t, "det 条纹图输出", stripeProb)
		same := true
		var wMax, sMax float32
		for i := range whiteProb {
			if whiteProb[i] != stripeProb[i] {
				same = false
			}
			if whiteProb[i] > wMax {
				wMax = whiteProb[i]
			}
			if stripeProb[i] > sMax {
				sMax = stripeProb[i]
			}
		}
		if same {
			t.Fatal("det 对空白图与条纹图输出完全相同，推理未生效")
		}
		t.Logf("det %dx%d：空白图 max=%.3g 条纹图 max=%.3g（输出随输入变化）", dw, dh, wMax, sMax)
	})

	t.Run("rec 动态宽度推理", func(t *testing.T) {
		rec, err := f.OpenRec(modelsDir)
		if err != nil {
			t.Fatalf("加载 rec.onnx 失败: %v", err)
		}
		defer rec.Close() //nolint:errcheck
		prevT := 0
		for _, w := range []int{320, 640} {
			logits, ts, classes, err := rec.RunRec(make([]float32, 3*48*w), w, 48)
			if err != nil {
				t.Fatalf("rec 推理失败（%dx48）: %v", w, err)
			}
			if ts <= 0 || classes <= 0 {
				t.Fatalf("rec 输出维度非法 [1,%d,%d]", ts, classes)
			}
			if len(logits) != ts*classes {
				t.Fatalf("rec 输出长度 = %d，期望 T*C = %d", len(logits), ts*classes)
			}
			assertFiniteAll(t, "rec 输出", logits)
			if classes != len(chars) {
				t.Fatalf("rec 类别数 %d 与词表 %d 不一致（模型与 ppocr_keys.txt 不匹配）", classes, len(chars))
			}
			if prevT > 0 && ts <= prevT {
				t.Fatalf("rec 时间步未随宽度增长: %d → %d", prevT, ts)
			}
			t.Logf("rec w=%d 输出 [1,%d,%d]", w, ts, classes)
			prevT = ts
		}
	})

	t.Run("完整管线 det→后处理→rec→CTC", func(t *testing.T) {
		det, err := f.OpenDet(modelsDir)
		if err != nil {
			t.Fatalf("加载 det.onnx 失败: %v", err)
		}
		defer det.Close() //nolint:errcheck
		rec, err := f.OpenRec(modelsDir)
		if err != nil {
			t.Fatalf("加载 rec.onnx 失败: %v", err)
		}
		defer rec.Close() //nolint:errcheck
		p, err := ocr.NewPipeline(det, rec, chars)
		if err != nil {
			t.Fatalf("组装管线失败: %v", err)
		}
		// 内置空白图（纯白 160x48）走完整管线：空白无文本行属正常，管线跑通不报错即验证
		const w, h = 160, 48
		blank := make([]byte, w*h*4)
		for i := 0; i < len(blank); i += 4 {
			blank[i], blank[i+1], blank[i+2], blank[i+3] = 0xFF, 0xFF, 0xFF, 0xFF
		}
		res, err := p.Run(blank, w, h)
		if err != nil {
			t.Fatalf("完整管线推理失败: %v", err)
		}
		t.Logf("空白图识别：%d 行文本 %q（%dms）", len(res.Lines), res.Text, res.ElapsedMs)
		for i, l := range res.Lines {
			if math.IsNaN(l.Score) || math.IsInf(l.Score, 0) {
				t.Fatalf("第 %d 行得分非有限: %v", i, l.Score)
			}
		}
	})
}

// TestORTIntegrationSmokeReport 排障口径复用：ocr.Selftest 零张量步骤在真实模型/库
// 上逐步通过（与 UVMP_FYNE_SMOKE=ocr 同链路），报告哪一步断直接定位。
func TestORTIntegrationSmokeReport(t *testing.T) {
	itSkip(t)
	modelsDir := resolveITModels(t)
	lib := resolveITLib(t, modelsDir)
	rep, err := ocr.Selftest(t.Context(), ocr.SelftestConfig{
		Factory:   &Factory{LibPath: lib, SearchRoots: []string{modelsDir}},
		ModelsDir: modelsDir,
		Log:       func(line string) { t.Log(line) },
	})
	if err != nil || !rep.OK {
		t.Fatalf("OCR 集成自检未通过：err=%v ok=%v steps=%+v", err, rep.OK, rep.Steps)
	}
	fmt.Printf("OCR 集成自检：ORT %s det=%v rec=%v vocab=%d\n",
		rep.ORTVersion, rep.DetOutDims, rep.RecOutDims, rep.VocabSize)
}
