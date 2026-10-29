package ocr

import (
	"testing"
)

// equalF32 逐值比较 float32 切片（金标为 JS Float32Array 实测值，须精确一致）。
func equalF32(t *testing.T, name string, got, want []float32) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s 长度不符：got %d want %d", name, len(got), len(want))
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("%s[%d] 不符：got %v want %v", name, i, got[i], want[i])
		}
	}
}

// TestBGRAToRGBPlanesGolden JS 金标对拍：BGRA → RGB 平面。
func TestBGRAToRGBPlanesGolden(t *testing.T) {
	g := jsGoldenBGRA
	planes := BGRAToRGBPlanes(g.Input, g.W, g.H)
	equalF32(t, "r", planes[2], g.R)
	equalF32(t, "g", planes[1], g.G)
	equalF32(t, "b", planes[0], g.B)
}

// TestResizePlanesGolden JS 金标对拍：双线性缩放（含上采样/缩小/1x1，权重怪癖逐值一致）。
func TestResizePlanesGolden(t *testing.T) {
	for key, c := range jsGoldenResize {
		got := ResizePlanes(c.In, c.SW, c.SH, c.DW, c.DH)
		for ch := 0; ch < 3; ch++ {
			equalF32(t, "resizePlanes_"+key+"[ch]", got[ch], c.Out[ch])
		}
	}
}

// TestPlanesToNCHWGolden JS 金标对拍：NCHW 归一化 (v/127.5-1)。
func TestPlanesToNCHWGolden(t *testing.T) {
	g := jsGoldenNCHW
	got := PlanesToNCHW(g.In, g.W, g.H)
	equalF32(t, "nchw", got, g.Out)
}

// TestResizePlanesIdentity 同尺寸缩放恒等（补充语义检查）。
func TestResizePlanesIdentity(t *testing.T) {
	in := Planes{
		{1, 2, 3, 4},
		{5, 6, 7, 8},
		{9, 10, 11, 12},
	}
	got := ResizePlanes(in, 2, 2, 2, 2)
	for ch := 0; ch < 3; ch++ {
		equalF32(t, "identity", got[ch], in[ch])
	}
}
