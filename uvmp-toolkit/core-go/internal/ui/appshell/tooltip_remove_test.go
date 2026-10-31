package appshell

import (
	"testing"
	"time"

	"fyne.io/fyne/v2"
)

func TestAppIconButtonTooltipRemoveFromCanvasCleansActive(t *testing.T) {
	window, button, parent := newDirectParentTooltipFixture(t)
	showAppButtonTooltip(t, window, button)
	panel := button.tooltip.host.panel
	removeTooltipButtonFromCanvas(window, parent, button)
	if button.tooltip.host != nil {
		t.Fatal("动态 remove 后 active tooltip 仍持有宿主并续表")
	}
	if appTooltipDirectPanelVisible(panel) {
		t.Fatal("动态 remove 后 active tooltip 的真实面板仍可见")
	}
	runUI(func() { parent.(*appTooltipScope).content.Add(button) })
	time.Sleep(700 * time.Millisecond)
	if appTooltipDirectPanelVisible(panel) {
		t.Fatal("按钮重新加入 Canvas 后，旧 active tooltip 不应复活")
	}
}

func TestAppIconButtonTooltipRemoveFromCanvasCancelsPending(t *testing.T) {
	window, button, parent := newDirectParentTooltipFixture(t)
	runUI(func() { button.MouseIn(nil) })
	panel := button.tooltip.host.panel
	removeTooltipButtonFromCanvas(window, parent, button)
	if button.tooltip.host != nil {
		t.Fatal("动态 remove 后 pending tooltip 仍持有宿主并续表")
	}
	runUI(func() { parent.(*appTooltipScope).content.Add(button) })
	time.Sleep(700 * time.Millisecond)
	if button.tooltip.host != nil || appTooltipDirectPanelVisible(panel) {
		t.Fatal("按钮重新加入 Canvas 后，旧 pending tooltip 不应复活")
	}
}

func removeTooltipButtonFromCanvas(window fyne.Window, parent tooltipScopeForTest, button *appButton) {
	runUI(func() {
		parent.Remove(button)
		parent.Refresh()
		window.Canvas().Capture()
	})
}
