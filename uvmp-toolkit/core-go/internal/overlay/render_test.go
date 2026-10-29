// 渲染与命中测试：扫描框/结果窗像素断言（品牌蓝描边、白卡、红答案区）与命中分区。
package overlay

import (
	"image/color"
	"testing"
)

func px(b *Buffer, x, y int) color.NRGBA {
	i := (y*b.W + x) * 4
	return color.NRGBA{R: b.Pix[i], G: b.Pix[i+1], B: b.Pix[i+2], A: b.Pix[i+3]}
}

func TestHitTestScanBox(t *testing.T) {
	w, h := 720, 180
	if z := HitTestScanBox(w-1, 1, w, h); z != ZoneClose {
		t.Errorf("top-right = %v, want ZoneClose", z)
	}
	if z := HitTestScanBox(w-1, h-1, w, h); z != ZoneResize {
		t.Errorf("bottom-right = %v, want ZoneResize", z)
	}
	if z := HitTestScanBox(30, 30, w, h); z != ZoneMove {
		t.Errorf("center = %v, want ZoneMove", z)
	}
	if z := HitTestScanBox(1, 1, w, h); z != ZoneMove {
		t.Errorf("top-left = %v, want ZoneMove", z)
	}
}

func TestHitTestResult(t *testing.T) {
	w, h := 380, 260
	if z := HitTestResult(20, 5, w, h); z != ZoneMove {
		t.Errorf("head = %v, want ZoneMove", z)
	}
	if z := HitTestResult(w-1, h-1, w, h); z != ZoneResize {
		t.Errorf("grip = %v, want ZoneResize", z)
	}
	if z := HitTestResult(20, 200, w, h); z != ZoneNone {
		t.Errorf("body = %v, want ZoneNone", z)
	}
}

func TestDrawScanBoxPixels(t *testing.T) {
	b := NewBuffer(720, 180)
	DrawScanBox(b, "A")
	// 左上描边：品牌蓝
	if c := px(b, 1, 90); c != (color.NRGBA{R: 0x0e, G: 0x3d, B: 0xa8, A: 255}) {
		t.Errorf("left border = %+v", c)
	}
	// 标签底：品牌蓝
	if c := px(b, 5, 5); c.R != 0x0e || c.A != 255 {
		t.Errorf("tag bg = %+v", c)
	}
	// 右上隐藏钮：品牌蓝
	if c := px(b, 720-5, 5); c.R != 0x0e || c.G != 0x3d {
		t.Errorf("close bg = %+v", c)
	}
	// 框内：品牌蓝 12% 着色（预乘后 R≈2 且半透明）
	c := px(b, 360, 90)
	if c.A < 25 || c.A > 40 {
		t.Errorf("inner alpha = %d, want ~31", c.A)
	}
	// 右下缩放柄对角线外（右下角）为品牌蓝
	if c := px(b, 719, 179); c.R != 0x0e {
		t.Errorf("grip corner = %+v", c)
	}
}

func TestDrawResultCardPixels(t *testing.T) {
	b := NewBuffer(380, 260)
	DrawResultCard(b, ResultView{
		Title: "题库搜题",
		OcrMs: 42,
		Sections: []ResultSection{
			{Tag: "A", ShowTag: true, Matched: true, Answer: "A", BankName: "测试库", ScorePct: 87,
				Stem: "我国安全生产工作的方针是？", Options: []ResultOpt{
					{Letter: "A", Text: "安全第一", Hit: true},
					{Letter: "B", Text: "质量第一"},
				}},
			{Tag: "B", ShowTag: true},
		},
	})
	// 白卡中心
	if c := px(b, 190, 4); c.A != 0 {
		t.Errorf("outside card should be transparent, got %+v", c)
	}
	if c := px(b, 190, 130); c.R != 255 || c.G != 255 || c.B != 255 || c.A != 255 {
		t.Errorf("card center = %+v, want opaque white", c)
	}
	// 答案红（22px 大字区域上半）
	foundRed := false
	for y := 46; y < 72 && !foundRed; y++ {
		for x := 30; x < 120; x++ {
			c := px(b, x, y)
			if c.R == 0xf5 && c.G == 0x3f && c.B == 0x3f {
				foundRed = true
				break
			}
		}
	}
	if !foundRed {
		t.Error("answer area should contain danger red")
	}
	// 第二分区（等待扫描…）存在浅灰文字像素
	foundAux := false
	for y := 130; y < 250 && !foundAux; y++ {
		for x := 30; x < 200; x++ {
			if px(b, x, y) == (color.NRGBA{R: 0x86, G: 0x90, B: 0x9c, A: 255}) {
				foundAux = true
				break
			}
		}
	}
	if !foundAux {
		t.Error("waiting section should contain aux-gray text pixels")
	}
}

func TestDrawResultCardJudgeLine(t *testing.T) {
	b := NewBuffer(380, 260)
	DrawResultCard(b, ResultView{
		Title: "题库搜题",
		Sections: []ResultSection{
			{Tag: "A", Matched: true, Answer: "A", JudgeLine: "正确", ScorePct: 90},
		},
	})
	foundRed := false
	for y := 40; y < 160 && !foundRed; y++ {
		for x := 20; x < 360; x++ {
			if px(b, x, y) == (color.NRGBA{R: 0xf5, G: 0x3f, B: 0x3f, A: 255}) {
				foundRed = true
				break
			}
		}
	}
	if !foundRed {
		t.Error("judge hit line should be danger red")
	}
}
