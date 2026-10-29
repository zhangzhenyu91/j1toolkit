package ocr

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// SelftestConfig OCR 环境自检参数。
type SelftestConfig struct {
	// Factory det/rec 会话工厂（ORT 实现见子包 ort）。
	Factory SessionFactory
	// ModelsDir 模型目录（det.onnx/rec.onnx/ppocr_keys.txt）。
	ModelsDir string
	// Golden 可选 golden PNG 识别（渲染已知文本图 → OCR → 比对期望，同 ocr_smoke）。
	Golden *GoldenSpec
	// Log 每步日志回调（每行一条，同 worker_diag「每步都有输出」）；nil 则丢弃。
	Log func(line string)
}

// GoldenSpec golden PNG 识别期望（自检末步）。
type GoldenSpec struct {
	// PNGPath 测试图路径（PNG，密集解码为 BGRA 后走完整管线）。
	PNGPath string
	// Expect 期望文本片段（识别全文包含即通过，同 ocr_smoke 的 EXPECT 口径）。
	Expect string
}

// GoldenResult golden PNG 识别结果。
type GoldenResult struct {
	OK        bool   `json:"ok"`
	Expect    string `json:"expect"`
	Got       string `json:"got"`
	Lines     []Line `json:"lines"`
	ElapsedMs int64  `json:"elapsedMs"`
	Error     string `json:"error,omitempty"`
}

// SelftestStep 自检单步结果（哪一步断、报什么错直接定位修复方向）。
type SelftestStep struct {
	Name      string `json:"name"`
	OK        bool   `json:"ok"`
	ElapsedMs int64  `json:"elapsedMs"`
	Error     string `json:"error,omitempty"`
	Detail    string `json:"detail,omitempty"`
}

// SelftestReport 自检报告（步骤逐一报告 + AVX2 标志 + 可选 golden 结果）。
type SelftestReport struct {
	OK         bool           `json:"ok"`
	Steps      []SelftestStep `json:"steps"`
	ORTVersion string         `json:"ortVersion,omitempty"`
	AVX2       *bool          `json:"avx2"` // nil=未知（非 linux 或读取失败）
	DetOutDims []int          `json:"detOutDims,omitempty"`
	RecOutDims []int          `json:"recOutDims,omitempty"`
	VocabSize  int            `json:"vocabSize"`
	Golden     *GoldenResult  `json:"golden,omitempty"`
}

// Selftest OCR 环境自检（对齐 worker_diag.cjs 分步语义）：det 会话 → det 零张量
// 推理（64x64，验证内核/指令集）→ rec 会话 → rec 零张量推理（48x320）→ 读取识别
// 字典 → 可选 golden PNG 识别。任何一步失败即停，报告定位到该步。
func Selftest(ctx context.Context, cfg SelftestConfig) (SelftestReport, error) {
	rep := SelftestReport{OK: false}
	logf := func(f string, a ...any) {
		if cfg.Log != nil {
			cfg.Log(fmt.Sprintf(f, a...))
		}
	}
	if cfg.Factory == nil || cfg.ModelsDir == "" {
		return rep, fmt.Errorf("ocr: Selftest 需 Factory 与 ModelsDir")
	}
	if v, ok := cfg.Factory.(RuntimeVersioner); ok {
		rep.ORTVersion = v.RuntimeVersion()
	}
	rep.AVX2 = AVX2Flag()
	logf("[selftest] AVX2：%v（nil=未知）", formatAVX2(rep.AVX2))

	step := func(name string, fn func() (string, error)) bool {
		t0 := time.Now()
		detail, err := fn()
		s := SelftestStep{Name: name, OK: err == nil, ElapsedMs: time.Since(t0).Milliseconds(), Detail: detail}
		if err != nil {
			s.Error = err.Error()
		}
		rep.Steps = append(rep.Steps, s)
		if err != nil {
			logf("[selftest] %s 失败：%v", name, err)
			return false
		}
		logf("[selftest] %s OK（%dms）%s", name, s.ElapsedMs, detail)
		return true
	}

	var det DetSession
	var rec RecSession
	defer func() {
		if det != nil {
			_ = det.Close()
		}
		if rec != nil {
			_ = rec.Close()
		}
	}()

	ok := step("创建 det 会话", func() (string, error) {
		var err error
		det, err = cfg.Factory.OpenDet(cfg.ModelsDir)
		if err != nil {
			return "", err
		}
		return cfg.ModelsDir + "/det.onnx", nil
	})
	if !ok {
		return rep, fmt.Errorf("ocr: selftest 中止：%s", rep.Steps[len(rep.Steps)-1].Name)
	}
	ok = step("det 推理（64x64 零张量，验证内核/指令集）", func() (string, error) {
		nchw := make([]float32, 3*64*64)
		prob, err := det.RunDet(nchw, 64, 64)
		if err != nil {
			return "", err
		}
		rep.DetOutDims = []int{1, 1, 64, 64}
		return fmt.Sprintf("输出 %d 个概率值", len(prob)), nil
	})
	if !ok {
		return rep, fmt.Errorf("ocr: selftest 中止：%s", rep.Steps[len(rep.Steps)-1].Name)
	}
	ok = step("创建 rec 会话", func() (string, error) {
		var err error
		rec, err = cfg.Factory.OpenRec(cfg.ModelsDir)
		if err != nil {
			return "", err
		}
		return cfg.ModelsDir + "/rec.onnx", nil
	})
	if !ok {
		return rep, fmt.Errorf("ocr: selftest 中止：%s", rep.Steps[len(rep.Steps)-1].Name)
	}
	ok = step("rec 推理（48x320 零张量）", func() (string, error) {
		nchw := make([]float32, 3*48*320)
		logits, t, c, err := rec.RunRec(nchw, 320, 48)
		if err != nil {
			return "", err
		}
		rep.RecOutDims = []int{1, t, c}
		return fmt.Sprintf("输出维度 [1,%d,%d]（%d 个值）", t, c, len(logits)), nil
	})
	if !ok {
		return rep, fmt.Errorf("ocr: selftest 中止：%s", rep.Steps[len(rep.Steps)-1].Name)
	}
	ok = step("读取识别字典 ppocr_keys.txt", func() (string, error) {
		keys, err := LoadKeys(cfg.ModelsDir + "/ppocr_keys.txt")
		if err != nil {
			return "", err
		}
		rep.VocabSize = len(BuildChars(keys))
		return fmt.Sprintf("字典字符数：%d（词表 %d）", len(keys), rep.VocabSize), nil
	})
	if !ok {
		return rep, fmt.Errorf("ocr: selftest 中止：%s", rep.Steps[len(rep.Steps)-1].Name)
	}

	rep.OK = true
	if cfg.Golden != nil {
		g := runGolden(ctx, det, rec, cfg)
		rep.Golden = &g
		rep.OK = g.OK
	}
	return rep, nil
}

// runGolden golden PNG 识别（det/rec 会话复用自检会话，走完整管线）。
func runGolden(ctx context.Context, det DetSession, rec RecSession, cfg SelftestConfig) GoldenResult {
	t0 := time.Now()
	g := GoldenResult{Expect: cfg.Golden.Expect}
	logf := func(f string, a ...any) {
		if cfg.Log != nil {
			cfg.Log(fmt.Sprintf(f, a...))
		}
	}
	bgra, w, h, err := LoadPNGToBGRA(cfg.Golden.PNGPath)
	if err != nil {
		g.Error = err.Error()
		logf("[selftest] golden PNG 读取失败：%v", err)
		return g
	}
	_ = ctx // 识别为同步 CPU 管线，ctx 仅留接口位（取消由调用方关闭会话达成）
	keys, err := LoadKeys(cfg.ModelsDir + "/ppocr_keys.txt")
	if err != nil {
		g.Error = err.Error()
		return g
	}
	p, err := NewPipeline(det, rec, BuildChars(keys))
	if err != nil {
		g.Error = err.Error()
		return g
	}
	res, err := p.Run(bgra, w, h)
	if err != nil {
		g.Error = err.Error()
		logf("[selftest] golden PNG 识别失败：%v", err)
		return g
	}
	g.Got = res.Text
	g.Lines = res.Lines
	g.ElapsedMs = time.Since(t0).Milliseconds()
	g.OK = containsText(res.Text, cfg.Golden.Expect)
	logf("[selftest] golden PNG %s：期望包含「%s」，识别「%s」",
		map[bool]string{true: "PASS", false: "FAIL"}[g.OK], cfg.Golden.Expect, res.Text)
	return g
}

// containsText 期望片段包含判定（同 ocr_smoke res.text.includes(EXPECT)）。
func containsText(text, expect string) bool {
	if expect == "" {
		return false
	}
	return strings.Contains(text, expect)
}

// formatAVX2 AVX2 标志的报告文本。
func formatAVX2(v *bool) string {
	if v == nil {
		return "未知"
	}
	if *v {
		return "支持"
	}
	return "不支持"
}
