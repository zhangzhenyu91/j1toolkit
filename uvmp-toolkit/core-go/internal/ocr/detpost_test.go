package ocr

import (
	"testing"
)

// TestDilateGolden JS 金标对拍：3x3 膨胀。
func TestDilateGolden(t *testing.T) {
	g := jsGoldenDilate
	got := Dilate(g.In, g.W, g.H)
	if len(got) != len(g.Out) {
		t.Fatalf("长度不符：got %d want %d", len(got), len(g.Out))
	}
	for i := range got {
		if got[i] != g.Out[i] {
			t.Fatalf("dilate[%d] 不符：got %d want %d", i, got[i], g.Out[i])
		}
	}
}

// TestConnectedComponentsGolden JS 金标对拍：两遍法连通域（含单像素、对角不连通）。
func TestConnectedComponentsGolden(t *testing.T) {
	g := jsGoldenCC
	got := ConnectedComponents(g.In, g.W, g.H)
	if len(got) != len(g.Comps) {
		t.Fatalf("连通域数不符：got %d want %d", len(got), len(g.Comps))
	}
	for i := range got {
		if got[i] != g.Comps[i] {
			t.Fatalf("comps[%d] 不符：got %+v want %+v", i, got[i], g.Comps[i])
		}
	}
}

// TestUnclipRectGolden JS 金标对拍：轴对齐 unclip（贴边/极扁/极小/贴右下）。
func TestUnclipRectGolden(t *testing.T) {
	for i, c := range jsGoldenUnclip {
		got := UnclipRect(c.BX, c.MaxW, c.MaxH)
		if got != c.Out {
			t.Fatalf("unclip[%d] 不符：got %+v want %+v", i, got, c.Out)
		}
	}
}

// TestDetPreprocessSize 合成尺寸序列的 det 预处理尺寸（32 对齐、上采样、封顶）。
func TestDetPreprocessSize(t *testing.T) {
	cases := []struct {
		w, h         int
		dw, dh       int
		usedRatioMin float64 // dw/w 的期望（允许浮点误差）
	}{
		{400, 200, 1472, 736, 3.68},
		{1920, 1080, 960, 544, 0.5}, // 短边≥736，长边>960 且 ratio<=1 → 封顶 960
		{100, 100, 736, 736, 7.36},
		{32, 32, 736, 736, 23},
	}
	for _, c := range cases {
		dw, dh, ratio := DetPreprocessSize(c.w, c.h)
		if dw != c.dw || dh != c.dh {
			t.Fatalf("size(%dx%d) = %dx%d，want %dx%d", c.w, c.h, dw, dh, c.dw, c.dh)
		}
		want := float64(c.dw) / float64(c.w)
		if diff := ratio - want; diff > 1e-12 || diff < -1e-12 {
			t.Fatalf("size(%dx%d) usedRatio = %v，want %v", c.w, c.h, ratio, want)
		}
	}
}

// TestDetPostprocessSynthetic 合成概率图（两高分块 + 噪点）→ 框检出与映射。
func TestDetPostprocessSynthetic(t *testing.T) {
	// 与 jsGoldenFull 同构的合成 det 概率图生成器（dw=1472, dh=736 过大，用小图语义验证）。
	const dw, dh = 64, 32
	prob := make([]float32, dw*dh)
	for i := range prob {
		prob[i] = 0.05
	}
	fillRect := func(x0, y0, x1, y1 int, v float32) {
		for y := y0; y < y1; y++ {
			for x := x0; x < x1; x++ {
				prob[y*dw+x] = v
			}
		}
	}
	fillRect(8, 6, 30, 14, 0.95)  // 主块
	fillRect(40, 20, 50, 26, 0.2) // 低分块（低于 box_thresh 应被丢弃）
	boxes := DetPostprocess(prob, dw, dh, 1, dw, dh)
	if len(boxes) != 1 {
		t.Fatalf("框数 = %d，want 1：%+v", len(boxes), boxes)
	}
	b := boxes[0]
	// unclip 外扩 d = 22*8*1.6/(2*30) ≈ 4.69 → 后处理框应覆盖 (8-5,6-5)-(30+5,14+5) 附近。
	if b.X > 4 || b.X+b.W < 34 || b.Y > 2 || b.Y+b.H < 18 {
		t.Fatalf("框覆盖不足：%+v", b)
	}
}
