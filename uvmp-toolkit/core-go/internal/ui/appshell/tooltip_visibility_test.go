package appshell

import (
	"testing"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/test"
	"fyne.io/fyne/v2/widget"
)

func TestAppIconButtonTooltipDirectParentHideCleansActiveAndDoesNotRevive(t *testing.T) {
	window, button, parent := newDirectParentTooltipFixture(t)
	showAppButtonTooltip(t, window, button)
	panel := button.tooltip.host.panel
	if panel == nil || !appTooltipDirectPanelVisible(panel) {
		t.Fatal("active 测试必须先取得真实可见的宿主面板")
	}
	runUI(func() { parent.Hide() })
	waitShellCond(t, "直接父容器 Hide 清理 active tooltip", func() bool {
		return !appTooltipDirectPanelVisible(panel)
	})
	runUI(func() { parent.Show() })
	time.Sleep(700 * time.Millisecond)
	if appTooltipDirectPanelVisible(panel) {
		t.Fatal("父容器重新显示后，旧 active tooltip 不应复活")
	}
}

func TestAppIconButtonTooltipDirectParentHideCancelsPendingAndDoesNotRevive(t *testing.T) {
	_, button, parent := newDirectParentTooltipFixture(t)
	runUI(func() { button.MouseIn(nil) })
	if button.tooltip.host == nil || button.tooltip.host.panel == nil {
		t.Fatal("pending 测试必须先取得宿主面板引用")
	}
	panel := button.tooltip.host.panel
	runUI(func() { parent.Hide() })
	time.Sleep(700 * time.Millisecond)
	if appTooltipDirectPanelVisible(panel) {
		t.Fatal("直接父容器 Hide 后，pending tooltip 不应显示")
	}
	runUI(func() { parent.Show() })
	time.Sleep(700 * time.Millisecond)
	if appTooltipDirectPanelVisible(panel) {
		t.Fatal("父容器重新显示后，旧 pending tooltip 不应复活")
	}
}

func newDirectParentTooltipFixture(t *testing.T) (fyne.Window, *appButton, tooltipScopeForTest) {
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

func appTooltipDirectPanelVisible(panel *fyne.Container) bool {
	visible := false
	runUI(func() { visible = panel.Visible() })
	return visible
}
