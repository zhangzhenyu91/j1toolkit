package appshell

import (
	"testing"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/test"

	"github.com/zhangzhenyu91/j1toolkit/uvmp-toolkit/core-go/internal/ui"
)

type fakeWindowOps struct {
	minimized   int
	maximized   int
	moves       int
	resizes     []int
	isMaximized bool
	edgeResize  bool
}

func (f *fakeWindowOps) Prepare()  {}
func (f *fakeWindowOps) Minimize() { f.minimized++ }
func (f *fakeWindowOps) ToggleMaximize() {
	f.maximized++
	f.isMaximized = !f.isMaximized
}
func (f *fakeWindowOps) BeginMove() { f.moves++ }
func (f *fakeWindowOps) BeginResize(direction int) {
	f.resizes = append(f.resizes, direction)
}
func (f *fakeWindowOps) EndResize()               {}
func (f *fakeWindowOps) Maximized() bool          { return f.isMaximized }
func (f *fakeWindowOps) SupportsEdgeResize() bool { return f.edgeResize }

func TestWindowTitleBar_ControlsAndKeyboard(t *testing.T) {
	a := test.NewApp()
	defer a.Quit()
	a.Settings().SetTheme(NewTheme())
	ops := &fakeWindowOps{}
	closed := 0
	var bar *windowTitleBar
	runUI(func() { bar = newWindowTitleBar(ops, func() { closed++ }) })

	if bar.minimize.AccessibilityLabel() != "最小化" || bar.maximize.AccessibilityLabel() != "最大化" || bar.close.AccessibilityLabel() != "关闭" {
		t.Fatal("窗口控制按钮缺少明确的辅助功能名称")
	}
	for _, button := range []*windowControlButton{bar.minimize, bar.maximize, bar.close} {
		if got := button.MinSize(); got.Width < 44 || got.Height < 44 {
			t.Fatalf("%s 命中区不足 44×44：%v", button.AccessibilityLabel(), got)
		}
	}

	runUI(func() { bar.minimize.Tapped(nil) })
	if ops.minimized != 1 {
		t.Fatalf("最小化未调用原生窗口行为：%d", ops.minimized)
	}
	runUI(func() { bar.maximize.Tapped(nil) })
	if ops.maximized != 1 || bar.maximize.kind != "restore" {
		t.Fatalf("最大化状态未联动：次数=%d 图标=%s", ops.maximized, bar.maximize.kind)
	}
	runUI(func() { bar.close.TypedKey(&fyne.KeyEvent{Name: fyne.KeyReturn}) })
	if closed != 1 {
		t.Fatalf("回车未触发关闭：%d", closed)
	}
	runUI(func() { bar.close.TypedKey(&fyne.KeyEvent{Name: fyne.KeySpace}) })
	if closed != 2 {
		t.Fatalf("空格未触发关闭：%d", closed)
	}

	runUI(func() {
		bar.close.MouseIn(nil)
		bar.close.FocusGained()
	})
	if bar.close.bg.FillColor != DangerRedBG || bar.close.bg.StrokeColor != BrandBlue {
		t.Fatal("关闭按钮应有可辨识的悬停与键盘焦点反馈")
	}
}

func TestWindowTitleBar_DragAndResize(t *testing.T) {
	a := test.NewApp()
	defer a.Quit()
	ops := &fakeWindowOps{edgeResize: true}
	var bar *windowTitleBar
	runUI(func() { bar = newWindowTitleBar(ops, func() {}) })
	drag := newWindowDragArea(bar, ops)
	runUI(func() { drag.Dragged(&fyne.DragEvent{}) })
	if ops.moves != 1 {
		t.Fatalf("标题栏拖动未交给系统窗口管理器：%d", ops.moves)
	}
	runUI(func() { drag.DoubleTapped(&fyne.PointEvent{}) })
	if ops.maximized != 1 {
		t.Fatalf("双击未切换最大化：%d", ops.maximized)
	}

	frame := newWindowFrame(ops, bar)
	if frame == nil {
		t.Fatal("X11 缩放热区未创建")
	}
	grip := newWindowResizeGrip(resizeBottomRight, ops)
	runUI(func() { grip.Dragged(&fyne.DragEvent{}) })
	if len(ops.resizes) != 1 || ops.resizes[0] != resizeBottomRight {
		t.Fatalf("边缘缩放方向错误：%v", ops.resizes)
	}
}

func TestShell_IntegratedChromeAndMinimumSize(t *testing.T) {
	f := ui.NewFakeBackend()
	f.StepDelay = time.Millisecond
	a := test.NewApp()
	defer a.Quit()
	s := newTestShell(t, a, f, Options{DisableTray: true, DisableScheduler: true})
	if s.titleBar == nil || s.titleBar.close == nil {
		t.Fatal("主窗缺少一体式窗口控制区")
	}
	if got := s.Win.Content().MinSize(); got.Width != 900 || got.Height < 600 {
		t.Fatalf("窗口最小尺寸不符合 900×600 目标：%v", got)
	}
	if s.Win.Title() != AppTitle {
		t.Fatalf("系统任务栏/切换器标题丢失：%q", s.Win.Title())
	}
}
