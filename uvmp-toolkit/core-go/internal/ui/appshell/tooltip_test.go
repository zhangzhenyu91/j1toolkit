package appshell

import (
	"testing"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/test"
	"fyne.io/fyne/v2/widget"
)

func TestAppIconButtonTooltipIsDelayedAndDoesNotStealFocus(t *testing.T) {
	window, button, entry := newTooltipTestButton(t, nil)
	if got := window.Canvas().Focused(); got != entry {
		t.Fatalf("测试前置焦点未建立：%T", got)
	}
	runUI(func() { button.MouseIn(nil) })
	time.Sleep(100 * time.Millisecond)
	if got := appButtonTooltipVisibleCount(button); got != 0 {
		t.Fatalf("tooltip 不应立即出现：visible=%d", got)
	}
	waitShellCond(t, "紧凑按钮延时 tooltip", func() bool {
		return appButtonTooltipVisibleCount(button) == 1
	})
	if got := window.Canvas().Focused(); got != entry {
		t.Fatalf("tooltip 不应抢占键盘焦点：%T", got)
	}
	if got := appButtonTooltipText(button); got != "删除记录" {
		t.Fatalf("tooltip 应显示中文操作名称：%q", got)
	}
	runUI(func() { button.MouseOut() })
}

func TestAppIconButtonTooltipMouseOutCancelsAndCleansUp(t *testing.T) {
	window, button, _ := newTooltipTestButton(t, nil)
	showAppButtonTooltip(t, window, button)
	runUI(func() { button.MouseOut() })
	if got := appButtonTooltipVisibleCount(button); got != 0 {
		t.Fatalf("MouseOut 应立即清理 tooltip：visible=%d", got)
	}
	runUI(func() {
		button.MouseIn(nil)
		button.MouseOut()
	})
	time.Sleep(700 * time.Millisecond)
	if got := appButtonTooltipVisibleCount(button); got != 0 {
		t.Fatalf("MouseOut 后旧计时器不应重新显示 tooltip：visible=%d", got)
	}
}

func TestAppIconButtonTooltipTapCancelsWithoutSwallowingCallback(t *testing.T) {
	called := 0
	window, button, _ := newTooltipTestButton(t, func() { called++ })
	showAppButtonTooltip(t, window, button)
	runUI(func() { button.Tapped(nil) })
	if got := appButtonTooltipVisibleCount(button); got != 0 {
		t.Fatalf("点击应立即清理 tooltip：visible=%d", got)
	}
	if called != 1 {
		t.Fatalf("tooltip 清理不得吞掉按钮回调：%d", called)
	}
}

func TestAppIconButtonTooltipDisableCancelsAndPreventsHover(t *testing.T) {
	window, button, _ := newTooltipTestButton(t, nil)
	showAppButtonTooltip(t, window, button)
	runUI(func() { button.Disable() })
	if got := appButtonTooltipVisibleCount(button); got != 0 {
		t.Fatalf("禁用应立即清理 tooltip：visible=%d", got)
	}
	runUI(func() { button.MouseIn(nil) })
	time.Sleep(700 * time.Millisecond)
	if got := appButtonTooltipVisibleCount(button); got != 0 {
		t.Fatalf("禁用按钮不应显示 tooltip：visible=%d", got)
	}
}

func TestAppIconButtonTooltipHideCancelsAndPreventsHover(t *testing.T) {
	window, button, _ := newTooltipTestButton(t, nil)
	showAppButtonTooltip(t, window, button)
	runUI(func() { button.Hide() })
	if got := appButtonTooltipVisibleCount(button); got != 0 {
		t.Fatalf("切页或对象隐藏应立即清理 tooltip：visible=%d", got)
	}
	runUI(func() { button.MouseIn(nil) })
	time.Sleep(700 * time.Millisecond)
	if got := appButtonTooltipVisibleCount(button); got != 0 {
		t.Fatalf("隐藏按钮不应显示 tooltip：visible=%d", got)
	}
}

func newTooltipTestButton(t *testing.T, tapped func()) (fyne.Window, *appButton, *widget.Entry) {
	t.Helper()
	a := test.NewApp()
	t.Cleanup(a.Quit)
	a.Settings().SetTheme(NewTheme())
	entry := widget.NewEntry()
	button := newAppIconButton("删除记录", "trash", widget.LowImportance, tapped)
	host := newAppTooltipHost(container.NewVBox(entry, button))
	window := test.NewWindow(host)
	window.Resize(fyne.NewSize(320, 200))
	window.Canvas().Focus(entry)
	t.Cleanup(window.Close)
	return window, button, entry
}

func showAppButtonTooltip(t *testing.T, window fyne.Window, button *appButton) {
	t.Helper()
	runUI(func() { button.MouseIn(nil) })
	waitShellCond(t, "tooltip 前置显示", func() bool {
		return appButtonTooltipVisibleCount(button) == 1
	})
}

func appButtonTooltipVisibleCount(button *appButton) int {
	count := 0
	runUI(func() {
		tooltip := button.tooltip
		if tooltip != nil && tooltip.host != nil && tooltip.host.active == tooltip && tooltip.host.panel.Visible() {
			count = 1
		}
	})
	return count
}

func appButtonTooltipText(button *appButton) string {
	text := ""
	runUI(func() {
		tooltip := button.tooltip
		if tooltip != nil && tooltip.host != nil && tooltip.host.active == tooltip {
			text = tooltip.host.text.Text
		}
	})
	return text
}
