package ocr

import (
	"context"
	"runtime"
	"testing"
)

// TestParseAVX2Golden JS 金标对拍：AVX2 /proc/cpuinfo 正则（index.cjs selftest 口径）。
func TestParseAVX2Golden(t *testing.T) {
	for i, c := range jsGoldenAVX2 {
		got := ParseAVX2CPUInfo(c.Content)
		if got == nil || *got != c.Result {
			t.Fatalf("avx2[%d] %q = %v，want %v", i, c.Content, got, c.Result)
		}
	}
}

// TestAVX2FlagNonLinux 非 linux 平台返回 nil（同 JS 非 linux 为 null）。
func TestAVX2FlagNonLinux(t *testing.T) {
	if runtime.GOOS != "linux" {
		if v := AVX2Flag(); v != nil {
			t.Fatalf("非 linux 应返回 nil，got %v", *v)
		}
	}
}

// TestSelftestPass 分步自检全绿（fake 会话 + golden PNG 通过）。
func TestSelftestPass(t *testing.T) {
	dir := writeTestModels(t)
	pngPath := dir + "/golden.png"
	writeSyntheticPNG(t, pngPath)
	var logs []string
	rep, err := Selftest(context.Background(), SelftestConfig{
		Factory:   &fakeFactory{version: "fake-ort"},
		ModelsDir: dir,
		Golden:    &GoldenSpec{PNGPath: pngPath, Expect: "AB"},
		Log:       func(line string) { logs = append(logs, line) },
	})
	if err != nil {
		t.Fatalf("Selftest：%v", err)
	}
	if !rep.OK || rep.Golden == nil || !rep.Golden.OK {
		t.Fatalf("自检未通过：%+v", rep)
	}
	if rep.ORTVersion != "fake-ort" {
		t.Fatalf("ORTVersion = %q", rep.ORTVersion)
	}
	if rep.VocabSize != 6 {
		t.Fatalf("VocabSize = %d，want 6", rep.VocabSize)
	}
	if len(rep.Steps) != 5 {
		t.Fatalf("步骤数 = %d，want 5：%+v", len(rep.Steps), rep.Steps)
	}
	for _, s := range rep.Steps {
		if !s.OK {
			t.Fatalf("步骤未通过：%+v", s)
		}
	}
	if len(logs) != len(rep.Steps)+2 { // AVX2 行 + 每步一行 + golden 行
		t.Fatalf("日志行数 = %d，want %d：%v", len(logs), len(rep.Steps)+2, logs)
	}
	if rep.Golden.Got != jsGoldenFull.Text {
		t.Fatalf("golden 识别 = %q", rep.Golden.Got)
	}
}

// TestSelftestGoldenFail golden 未命中期望文本 → 整体不通过、错误可定位。
func TestSelftestGoldenFail(t *testing.T) {
	dir := writeTestModels(t)
	pngPath := dir + "/golden.png"
	writeSyntheticPNG(t, pngPath)
	rep, err := Selftest(context.Background(), SelftestConfig{
		Factory:   &fakeFactory{},
		ModelsDir: dir,
		Golden:    &GoldenSpec{PNGPath: pngPath, Expect: "不存在的文本"},
	})
	if err != nil {
		t.Fatalf("Selftest：%v", err)
	}
	if rep.OK || rep.Golden == nil || rep.Golden.OK {
		t.Fatalf("golden 未命中应整体失败：%+v", rep)
	}
	if rep.Golden.Got != jsGoldenFull.Text {
		t.Fatalf("golden 识别 = %q", rep.Golden.Got)
	}
}

// TestSelftestStepFailure 分步失败定位：det 会话打不开 → 中止于第一步。
func TestSelftestStepFailure(t *testing.T) {
	dir := writeTestModels(t)
	rep, err := Selftest(context.Background(), SelftestConfig{
		Factory:   &fakeFactory{openErr: errFakeSession},
		ModelsDir: dir,
	})
	if err == nil {
		t.Fatal("失败自检应返回错误")
	}
	if len(rep.Steps) != 1 || rep.Steps[0].OK || rep.Steps[0].Name != "创建 det 会话" {
		t.Fatalf("应中止于第一步：%+v", rep.Steps)
	}
	if rep.Steps[0].Error == "" {
		t.Fatal("失败步骤应带错误文本")
	}
}

// TestSelftestConfigErrors 参数缺失。
func TestSelftestConfigErrors(t *testing.T) {
	if _, err := Selftest(context.Background(), SelftestConfig{}); err == nil {
		t.Fatal("缺 Factory/ModelsDir 应报错")
	}
}
