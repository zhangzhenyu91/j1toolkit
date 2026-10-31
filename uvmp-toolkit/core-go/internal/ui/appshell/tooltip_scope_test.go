package appshell

import (
	"testing"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/test"
	"fyne.io/fyne/v2/widget"
)

func TestAppIconButtonTooltipScopeRapidHideShowDoesNotReviveActive(t *testing.T) {
	window, button, parent := newTooltipScopeFixture(t)
	showAppButtonTooltip(t, window, button)
	panel := button.tooltip.host.panel
	runUI(func() {
		parent.Hide()
		parent.Show()
	})
	time.Sleep(700 * time.Millisecond)
	if appTooltipDirectPanelVisible(panel) {
		t.Fatal("同一间隔内 Hide→Show 后，旧 active tooltip 不应复活")
	}
}

func TestAppIconButtonTooltipScopeRapidHideShowDoesNotRevivePending(t *testing.T) {
	_, button, parent := newTooltipScopeFixture(t)
	runUI(func() { button.MouseIn(nil) })
	panel := button.tooltip.host.panel
	runUI(func() {
		parent.Hide()
		parent.Show()
	})
	time.Sleep(700 * time.Millisecond)
	if appTooltipDirectPanelVisible(panel) {
		t.Fatal("同一间隔内 Hide→Show 后，旧 pending tooltip 不应复活")
	}
}

type tooltipScopeForTest interface {
	fyne.CanvasObject
	Hide()
	Show()
	Remove(fyne.CanvasObject)
}

func newTooltipScopeFixture(t *testing.T) (fyne.Window, *appButton, tooltipScopeForTest) {
	t.Helper()
	a := test.NewApp()
	t.Cleanup(a.Quit)
	a.Settings().SetTheme(NewTheme())
	button := newAppIconButton("删除记录", "trash", widget.LowImportance, nil)
	parent := newAppTooltipScope(container.NewVBox(button))
	window := test.NewWindow(newAppTooltipHost(parent))
	window.Resize(fyne.NewSize(320, 200))
	t.Cleanup(window.Close)
	t.Cleanup(func() { runUI(func() { button.MouseOut() }) })
	return window, button, parent
}
