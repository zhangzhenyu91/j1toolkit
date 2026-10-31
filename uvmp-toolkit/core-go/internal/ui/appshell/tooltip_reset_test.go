package appshell

import (
	"testing"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/test"
	"fyne.io/fyne/v2/widget"
)

var tooltipResetStates = []struct {
	name   string
	active bool
}{
	{name: "Active", active: true},
	{name: "Pending", active: false},
}

func TestAppIconButtonTooltipScopeResetBeforeObjectsRebuild(t *testing.T) {
	for _, state := range tooltipResetStates {
		t.Run(state.name, func(t *testing.T) {
			a := test.NewApp()
			t.Cleanup(a.Quit)
			a.Settings().SetTheme(NewTheme())
			button := newAppIconButton("删除记录", "trash", widget.LowImportance, nil)
			content := container.NewVBox(button)
			scope := newAppTooltipScope(content)
			window, entry := newTooltipResetWindow(t, a, scope)
			panel := primeTooltipForReset(t, window, button, state.active)

			runUI(func() {
				scope.Reset()
				content.Objects = nil
				content.Add(widget.NewLabel("重建后的行"))
				content.Refresh()
			})

			assertTooltipResetResult(t, window, entry, button, panel)
		})
	}
}

func TestAppIconButtonTooltipScopeResetBeforeListUpdateItem(t *testing.T) {
	for _, state := range tooltipResetStates {
		t.Run(state.name, func(t *testing.T) {
			a := test.NewApp()
			t.Cleanup(a.Quit)
			a.Settings().SetTheme(NewTheme())
			rows := map[*appTooltipScope]*appButton{}
			var rowScope *appTooltipScope
			var rowButton *appButton
			rebind := false
			list := widget.NewList(
				func() int { return 1 },
				func() fyne.CanvasObject {
					button := newAppIconButton("删除记录", "trash", widget.LowImportance, nil)
					scope := newAppTooltipScope(container.NewVBox(button))
					rows[scope] = button
					return scope
				},
				func(_ widget.ListItemID, object fyne.CanvasObject) {
					scope := object.(*appTooltipScope)
					scope.Reset()
					if rebind {
						scope.content.Objects = nil
						scope.content.Add(widget.NewLabel("重绑定后的行"))
						scope.content.Refresh()
						return
					}
					rowScope = scope
					rowButton = rows[scope]
				},
			)
			window, entry := newTooltipResetWindow(t, a, list)
			waitShellCond(t, "List 创建真实可见行", func() bool {
				ready := false
				runUI(func() { ready = rowScope != nil && rowButton != nil })
				return ready
			})
			var scope *appTooltipScope
			var button *appButton
			runUI(func() {
				scope = rowScope
				button = rowButton
			})
			panel := primeTooltipForReset(t, window, button, state.active)

			runUI(func() {
				rebind = true
				list.UpdateItem(0, scope)
			})

			assertTooltipResetResult(t, window, entry, button, panel)
		})
	}
}

func TestAppIconButtonTooltipScopeResetDoesNotAffectOtherWindow(t *testing.T) {
	a := test.NewApp()
	t.Cleanup(a.Quit)
	a.Settings().SetTheme(NewTheme())
	otherButton := newAppIconButton("删除记录", "trash", widget.LowImportance, nil)
	otherScope := newAppTooltipScope(container.NewVBox(otherButton))
	otherWindow, _ := newTooltipResetWindow(t, a, otherScope)
	showAppButtonTooltip(t, otherWindow, otherButton)
	firstButton := newAppIconButton("删除记录", "trash", widget.LowImportance, nil)
	firstScope := newAppTooltipScope(container.NewVBox(firstButton))
	firstWindow, firstEntry := newTooltipResetWindow(t, a, firstScope)
	showAppButtonTooltip(t, firstWindow, firstButton)
	firstPanel := firstButton.tooltip.host.panel
	firstWindow.Canvas().Focus(firstEntry)

	runUI(func() { firstScope.Reset() })

	assertTooltipResetResult(t, firstWindow, firstEntry, firstButton, firstPanel)
	if got := appButtonTooltipVisibleCount(otherButton); got != 1 {
		t.Fatalf("Reset 不应清理其他窗口的 active tooltip：visible=%d", got)
	}
}

func newTooltipResetWindow(t *testing.T, app fyne.App, body fyne.CanvasObject) (fyne.Window, *widget.Entry) {
	t.Helper()
	if fyne.CurrentApp() != app {
		t.Fatal("Reset 测试窗口必须属于当前 Fyne App")
	}
	entry := widget.NewEntry()
	window := test.NewWindow(newAppTooltipHost(container.NewBorder(entry, nil, nil, nil, body)))
	window.Resize(fyne.NewSize(320, 200))
	window.Canvas().Focus(entry)
	if got := window.Canvas().Focused(); got != entry {
		t.Fatalf("Reset 测试前置焦点未建立：%T", got)
	}
	t.Cleanup(window.Close)
	return window, entry
}

func primeTooltipForReset(t *testing.T, window fyne.Window, button *appButton, active bool) *fyne.Container {
	t.Helper()
	if active {
		showAppButtonTooltip(t, window, button)
	} else {
		runUI(func() { button.MouseIn(nil) })
	}
	var panel *fyne.Container
	runUI(func() {
		if button.tooltip.host != nil {
			panel = button.tooltip.host.panel
		}
	})
	if panel == nil {
		t.Fatal("Reset 测试必须先取得真实宿主面板")
	}
	return panel
}

func assertTooltipResetResult(t *testing.T, window fyne.Window, entry *widget.Entry, button *appButton, panel *fyne.Container) {
	t.Helper()
	var host *appTooltipHost
	runUI(func() { host = button.tooltip.host })
	if host != nil {
		t.Fatal("Reset 后旧 tooltip 不应继续持有宿主")
	}
	if appTooltipDirectPanelVisible(panel) {
		t.Fatal("Reset 后旧 active 面板仍可见")
	}
	time.Sleep(700 * time.Millisecond)
	if appTooltipDirectPanelVisible(panel) {
		t.Fatal("重建或重绑定后，旧 pending/active 面板不应复活")
	}
	if got := window.Canvas().Focused(); got != entry {
		t.Fatalf("Reset 不应抢占键盘焦点：%T", got)
	}
}
