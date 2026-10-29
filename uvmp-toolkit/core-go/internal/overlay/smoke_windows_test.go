// overlay：Windows 真实窗口冒烟（OVERLAY_SMOKE=1 启用；本机手动验证用，不进默认测试）。
// 覆盖：透明置顶扫描框/结果窗创建与绘制、Move/Resize/opacity、光标与按键轮询、
// 多显示器枚举、DPI 缩放、整屏截帧与框裁剪。
package overlay

import (
	"os"
	"testing"
	"time"
)

func TestWindowsSmoke(t *testing.T) {
	if os.Getenv("OVERLAY_SMOKE") != "1" {
		t.Skip("真实窗口冒烟：设 OVERLAY_SMOKE=1 启用")
	}
	host, err := NewHost()
	if err != nil {
		t.Fatal(err)
	}
	defer host.Close()

	ds := host.Displays()
	if len(ds) == 0 {
		t.Fatal("无显示器")
	}
	for _, d := range ds {
		t.Logf("显示器 #%d bounds=%+v workArea=%+v scale=%.2f", d.ID, d.Bounds, d.WorkArea, d.Scale)
	}
	p := host.Primary()
	t.Logf("主显示器=%+v", p)

	// 光标与按键轮询
	for i := 0; i < 3; i++ {
		pos, down, err := host.PointerState()
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("光标=%+v leftDown=%v", pos, down)
		time.Sleep(20 * time.Millisecond)
	}

	// 扫描框（透明 + 品牌蓝描边 + 标签/隐藏钮/缩放柄）
	boxWin, err := host.NewWindow(WindowOpts{
		Bounds:  Rect{X: p.WorkArea.X + 120, Y: p.WorkArea.Y + 120, W: 720, H: 180},
		Title:   "冒烟扫描框A",
		Opacity: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer boxWin.Close()
	if err := boxWin.Paint(mustBox(t, boxWin)); err != nil {
		t.Fatal(err)
	}

	// 结果窗（白卡 + 文案）
	resWin, err := host.NewWindow(WindowOpts{
		Bounds:  Rect{X: p.WorkArea.X + 200, Y: p.WorkArea.Y + 380, W: 380, H: 260},
		Title:   "冒烟结果窗",
		Opacity: 0.92,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer resWin.Close()
	if err := resWin.Paint(mustResult(t, resWin)); err != nil {
		t.Fatal(err)
	}

	// Move + Resize + opacity
	b := boxWin.Bounds()
	boxWin.SetBounds(Rect{X: b.X + 40, Y: b.Y + 30, W: b.W + 60, H: b.H + 20})
	b = boxWin.Bounds()
	if b.W != 780 || b.H != 200 {
		t.Fatalf("resize 未生效：%+v", b)
	}
	if err := boxWin.Paint(mustBox(t, boxWin)); err != nil {
		t.Fatal(err)
	}
	resWin.SetOpacity(0.75)
	resWin.SetVisible(false)
	resWin.SetVisible(true)

	// 截屏 + 裁剪（显示器整帧 → 框区域稠密 BGRA）
	frame, err := host.CaptureDisplay(p, MaxThumb)
	if err != nil {
		t.Fatal(err)
	}
	if frame == nil || frame.Width == 0 || frame.Height == 0 {
		t.Fatal("截屏为空")
	}
	t.Logf("截帧=%dx%d (display#%d) scale=%.2f", frame.Width, frame.Height, frame.DisplayID, p.Scale)
	crop, err := CropFrame(frame, p, boxWin.Bounds())
	if err != nil {
		t.Fatalf("裁剪失败：%v", err)
	}
	if len(crop.Pix) != crop.Width*crop.Height*4 {
		t.Fatalf("裁剪缓冲不稠密：%d != %d*%d*4", len(crop.Pix), crop.Width, crop.Height)
	}
	t.Logf("裁剪=%dx%d", crop.Width, crop.Height)

	// 命中测试与拖拽数学（纯函数顺带冒烟）
	if HitTestScanBox(719, 1, 720, 180) != ZoneClose {
		t.Error("HitTestScanBox close")
	}
	d := NewDragger(DragMove, Point{X: 10, Y: 10}, boxWin.Bounds(), 160, 60)
	t.Logf("拖拽目标=%+v", d.Step(Point{X: 60, Y: 40}))
	t.Log("Windows 真窗冒烟通过")
}

func mustBox(t *testing.T, w Window) *Buffer {
	t.Helper()
	b := w.Bounds()
	buf := NewBuffer(b.W, b.H)
	DrawScanBox(buf, "A")
	return buf
}

func mustResult(t *testing.T, w Window) *Buffer {
	t.Helper()
	b := w.Bounds()
	buf := NewBuffer(b.W, b.H)
	DrawResultCard(buf, ResultView{
		Title: "题库搜题",
		OcrMs: 42,
		Sections: []ResultSection{
			{Tag: "A", ShowTag: true, Matched: true, Answer: "A", BankName: "测试库", ScorePct: 87,
				Stem: "我国安全生产工作的方针是？安全第一、预防为主、综合治理",
				Options: []ResultOpt{
					{Letter: "A", Text: "安全第一、预防为主、综合治理", Hit: true},
					{Letter: "B", Text: "质量第一"},
					{Letter: "C", Text: "效率优先"},
				}},
			{Tag: "B", ShowTag: true, Waiting: true},
		},
	})
	return buf
}
