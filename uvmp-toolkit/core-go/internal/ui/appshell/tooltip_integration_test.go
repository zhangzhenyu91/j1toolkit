package appshell

import (
	"image/color"
	"testing"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/test"
	"fyne.io/fyne/v2/widget"

	"github.com/zhangzhenyu91/j1toolkit/uvmp-toolkit/core-go/internal/ui"
)

func TestAppIconButtonTooltipIsPaintedOutsideScrollViewport(t *testing.T) {
	s, button, scroll := newTooltipShellButton(t)
	showAppButtonTooltip(t, s.Win, button)
	panelPos, panelSize := appButtonTooltipAbsoluteRect(s.Win, button)
	scrollPos := fyne.CurrentApp().Driver().AbsolutePositionForObject(scroll)
	scrollBottom := scrollPos.Y + scroll.Size().Height
	if panelPos.Y+panelSize.Height <= scrollBottom {
		t.Fatalf("测试未覆盖滚动视口外绘制：panelBottom=%.2f scrollBottom=%.2f", panelPos.Y+panelSize.Height, scrollBottom)
	}
	sample := fyne.NewPos(panelPos.X+panelSize.Width-6, panelPos.Y+panelSize.Height/2)
	x, y := s.Win.Canvas().PixelCoordinateForPosition(sample)
	got := color.NRGBAModel.Convert(s.Win.Canvas().Capture().At(x, y)).(color.NRGBA)
	want := color.NRGBA{R: 0xFF, G: 0xFF, B: 0xFF, A: 0xFF}
	if got != want {
		t.Fatalf("滚动视口外的 tooltip 背景未真实绘制：got=%#v want=%#v", got, want)
	}
	runUI(func() { button.MouseOut() })
}

func TestAppIconButtonTooltipParentPageHideCleansVisible(t *testing.T) {
	s, button, _ := newTooltipShellButton(t)
	showAppButtonTooltip(t, s.Win, button)
	panel := button.tooltip.host.panel
	s.Navigate("history")
	if s.pages["settings"].Object().Visible() || !button.Visible() {
		t.Fatal("测试必须覆盖仅父页隐藏、按钮自身 Visible 仍为 true 的场景")
	}
	if appTooltipDirectPanelVisible(panel) {
		t.Fatal("父页隐藏后 active tooltip 的真实面板仍可见")
	}
	s.Navigate("settings")
	time.Sleep(700 * time.Millisecond)
	if appTooltipDirectPanelVisible(panel) {
		t.Fatal("切回旧页后 active tooltip 不应复活")
	}
}

func TestAppIconButtonTooltipParentPageHideCancelsPending(t *testing.T) {
	s, button, _ := newTooltipShellButton(t)
	runUI(func() { button.MouseIn(nil) })
	panel := button.tooltip.host.panel
	s.Navigate("history")
	time.Sleep(700 * time.Millisecond)
	if appTooltipDirectPanelVisible(panel) {
		t.Fatal("父页隐藏后待显示 tooltip 被旧计时器复活")
	}
	s.Navigate("settings")
	time.Sleep(700 * time.Millisecond)
	if appTooltipDirectPanelVisible(panel) {
		t.Fatal("切回旧页后 pending tooltip 不应复活")
	}
}

func newTooltipShellButton(t *testing.T) (*Shell, *appButton, *container.Scroll) {
	t.Helper()
	a := test.NewApp()
	t.Cleanup(a.Quit)
	s := newTestShell(t, a, ui.NewFakeBackend(), Options{DisableTray: true, DisableScheduler: true})
	button := newAppIconButton("删除记录", "trash", widget.LowImportance, nil)
	scroll := container.NewVScroll(container.NewVBox(spacer(400), button))
	previous := s.pages["settings"].Object()
	s.pages["settings"] = &tooltipShellTestPage{root: scroll}
	replaced := false
	runUI(func() {
		background := canvas.NewRectangle(color.NRGBA{R: 0x12, G: 0x34, B: 0x56, A: 0xFF})
		s.content.Objects = append([]fyne.CanvasObject{background}, s.content.Objects...)
		for i, object := range s.content.Objects {
			if object == previous {
				s.content.Objects[i] = scroll
				replaced = true
			}
		}
		s.content.Refresh()
		scroll.Resize(fyne.NewSize(240, 80))
	})
	if !replaced {
		t.Fatal("受控滚动页未接入真实 Shell 内容层")
	}
	s.Navigate("settings")
	runUI(func() { scroll.ScrollToBottom() })
	if scroll.Offset.Y <= 0 {
		t.Fatal("测试必须真实滚动到设置页底部")
	}
	t.Cleanup(func() { runUI(func() { button.MouseOut() }) })
	return s, button, scroll
}

type tooltipShellTestPage struct{ root *container.Scroll }

func (p *tooltipShellTestPage) Object() fyne.CanvasObject { return p.root }
func (p *tooltipShellTestPage) OnShow()                   {}

func appButtonTooltipAbsoluteRect(window fyne.Window, button *appButton) (fyne.Position, fyne.Size) {
	var position fyne.Position
	var size fyne.Size
	runUI(func() {
		tooltip := button.tooltip
		if tooltip == nil || tooltip.host == nil {
			return
		}
		position = fyne.CurrentApp().Driver().AbsolutePositionForObject(tooltip.host.panel)
		size = tooltip.host.panel.Size()
	})
	return position, size
}
