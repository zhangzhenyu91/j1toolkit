package appshell

import (
	"testing"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/driver/desktop"
	"fyne.io/fyne/v2/test"
	"fyne.io/fyne/v2/widget"

	"github.com/zhangzhenyu91/j1toolkit/uvmp-toolkit/core-go/internal/ui"
)

type shellActionControl interface {
	fyne.CanvasObject
	fyne.Tappable
	fyne.Focusable
	fyne.Accessible
	desktop.Cursorable
}

func TestShellRowsPointerKeyboardAndChineseAccessibility(t *testing.T) {
	tests := []struct {
		name  string
		label string
		new   func(func()) fyne.CanvasObject
	}{
		{name: "首页状态行", label: "树障同步", new: func(tapped func()) fyne.CanvasObject {
			return newHomeRow(homeTask{name: "树障同步", icon: "forest"}, tapped)
		}},
		{name: "折叠标题", label: "高级设置", new: func(tapped func()) fyne.CanvasObject { return newSectionHeader("高级设置", tapped) }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := test.NewApp()
			defer a.Quit()
			a.Settings().SetTheme(NewTheme())
			tapped := 0
			control, ok := tt.new(func() { tapped++ }).(shellActionControl)
			if !ok {
				t.Fatal("可点击行必须同时支持焦点、可访问名称与手型指针")
			}
			if got := control.Cursor(); got != desktop.PointerCursor {
				t.Fatalf("可点击行应使用手型指针：%v", got)
			}
			if got := control.AccessibilityLabel(); got != tt.label {
				t.Fatalf("中文可访问名称不符：got=%q want=%q", got, tt.label)
			}
			if got := control.AccessibilityRole(); got != fyne.AccessibleRoleButton {
				t.Fatalf("可点击行应暴露按钮角色：%v", got)
			}
			window := test.NewWindow(container.NewVBox(control))
			defer window.Close()
			window.Canvas().FocusNext()
			if got := window.Canvas().Focused(); got != control {
				t.Fatalf("Tab 应聚焦可点击行：%T %p", got, got)
			}
			for _, key := range []fyne.KeyName{fyne.KeySpace, fyne.KeyReturn, fyne.KeyEnter} {
				control.TypedKey(&fyne.KeyEvent{Name: key})
			}
			if tapped != 3 {
				t.Fatalf("Space/Enter 应各激活一次，实际 %d 次", tapped)
			}
		})
	}
}

func TestHomeQuickRowUsesIconButtonsWithoutChangingGeometry(t *testing.T) {
	a := test.NewApp()
	defer a.Quit()
	a.Settings().SetTheme(NewTheme())
	row := newHomeQuickRow(homeTask{name: "轨迹派车单导出", desc: "批量导出", icon: "route"}, false, nil, nil)
	if len(row.actions.Objects) != 2 {
		t.Fatalf("快捷行应保留收藏和打开两个动作：%d", len(row.actions.Objects))
	}
	for i, object := range row.actions.Objects {
		button, ok := object.(*appButton)
		if !ok || button.Icon == nil {
			t.Fatalf("快捷行动作 %d 应迁移为带图标的 appButton：%T", i, object)
		}
		if button.AccessibilityLabel() == "" || button.Cursor() != desktop.PointerCursor {
			t.Fatalf("快捷行动作 %d 缺少中文名称或手型指针", i)
		}
	}

	width := float32(360)
	row.Resize(fyne.NewSize(width, row.MinSize().Height))
	textX := homeQuickIconSize + homeQuickIconGap
	desc := row.visibleDesc()
	if desc.Position().X != textX || row.icon.Size().Width != homeQuickIconSize {
		t.Fatalf("快捷行图标和描述几何被改变：icon=%v desc=%v", row.icon.Size(), desc.Position())
	}
	actionsMin := row.actions.MinSize()
	if row.actions.Position().X != width-actionsMin.Width {
		t.Fatalf("快捷行动作区仍应右对齐：%v", row.actions.Position())
	}
}

func TestPageHeaderFavoriteSynchronizesIconAndAccessibleName(t *testing.T) {
	a := test.NewApp()
	defer a.Quit()
	header := newPageHeader()
	button, ok := any(header.favorite).(*appButton)
	if !ok {
		t.Fatalf("页头收藏应使用 appButton：%T", header.favorite)
	}
	for _, tt := range []struct {
		favorite bool
		label    string
	}{
		{favorite: true, label: "已收藏"},
		{favorite: false, label: "收藏"},
	} {
		header.SetFavorite("pcd_export", tt.favorite)
		if button.Text != tt.label || button.AccessibilityLabel() != tt.label {
			t.Fatalf("动态收藏文案与可访问名称未同步：%q / %q", button.Text, button.AccessibilityLabel())
		}
		if button.Icon == nil || button.Icon.Name() != "star.svg" {
			t.Fatalf("动态收藏按钮缺少语义图标：%v", button.Icon)
		}
	}
}

func TestCollapsibleHideShowUsesTooltipScope(t *testing.T) {
	a := test.NewApp()
	defer a.Quit()
	a.Settings().SetTheme(NewTheme())
	button := newAppIconButton("删除记录", "trash", widget.LowImportance, nil)
	section := newCollapsibleSection("危险操作", button, true)
	window := test.NewWindow(newAppTooltipHost(section.Root))
	defer window.Close()
	window.Resize(fyne.NewSize(360, 240))
	showAppButtonTooltip(t, window, button)
	panel := button.tooltip.host.panel
	runUI(func() { section.SetExpanded(false) })
	waitShellCond(t, "折叠时清理 active tooltip", func() bool { return !appTooltipDirectPanelVisible(panel) })
	runUI(func() { section.SetExpanded(true) })
	time.Sleep(700 * time.Millisecond)
	if appTooltipDirectPanelVisible(panel) {
		t.Fatal("重新展开后不应复活折叠前的 tooltip")
	}
}

func collectShellActionButtons(obj fyne.CanvasObject, buttons *[]*appButton) {
	if obj == nil {
		return
	}
	if button, ok := obj.(*appButton); ok {
		*buttons = append(*buttons, button)
		return
	}
	if box, ok := obj.(*fyne.Container); ok {
		for _, child := range box.Objects {
			collectShellActionButtons(child, buttons)
		}
		return
	}
	if w, ok := obj.(fyne.Widget); ok {
		for _, child := range w.CreateRenderer().Objects() {
			collectShellActionButtons(child, buttons)
		}
	}
}

func TestHomeFeatureAndRecentActionsUseGraphicButtons(t *testing.T) {
	a := test.NewApp()
	defer a.Quit()
	a.Settings().SetTheme(NewTheme())
	p := &pageHome{cards: map[string]*homeTaskCard{}}
	feature := p.newFeatureCard(homeTask{id: "course_autoplay", name: "学堂刷课", icon: "course"})
	var featureButtons []*appButton
	collectShellActionButtons(feature.Root, &featureButtons)
	if len(featureButtons) != 1 || featureButtons[0].Icon == nil {
		t.Fatalf("首页特色卡入口应保留一个带图标按钮：%d", len(featureButtons))
	}

	view := newRecentView(nil)
	item := homeRecent{appName: "轨迹派车单导出", summary: "已完成"}
	item.rec.JobID = "job-1"
	item.rec.Dir = "."
	view.render([]homeRecent{item})
	var recentButtons []*appButton
	collectShellActionButtons(view.root, &recentButtons)
	if len(recentButtons) != 2 {
		t.Fatalf("最近结果应提供查看日志和打开目录两枚图标按钮：%d", len(recentButtons))
	}
	for _, button := range recentButtons {
		if button.Icon == nil || button.AccessibilityLabel() == "" {
			t.Fatal("最近结果按钮缺少语义图标或中文名称")
		}
	}
}

func TestHomeQuickRefreshResetsTooltipBeforeRebuild(t *testing.T) {
	f := ui.NewFakeBackend()
	app := test.NewApp()
	defer app.Quit()
	s := newTestShell(t, app, f, Options{DisableTray: true, DisableScheduler: true})
	runUI(func() {
		s.toggleFavorite("pcd_export")
		s.pages["home"].(*pageHome).quick.refresh()
	})
	quick := s.pages["home"].(*pageHome).quick
	var buttons []*appButton
	collectShellActionButtons(quick.root, &buttons)
	if len(buttons) == 0 {
		t.Fatal("快捷行重建前应先存在图标按钮")
	}
	button := buttons[0]
	showAppButtonTooltip(t, s.Win, button)
	panel := button.tooltip.host.panel
	runUI(func() { quick.refresh() })
	waitShellCond(t, "快捷行重建前清理 tooltip", func() bool { return !appTooltipDirectPanelVisible(panel) })
}

func TestRecentRefreshResetsTooltipBeforeRebuild(t *testing.T) {
	a := test.NewApp()
	defer a.Quit()
	a.Settings().SetTheme(NewTheme())
	view := newRecentView(nil)
	window := test.NewWindow(newAppTooltipHost(view.root))
	defer window.Close()
	window.Resize(fyne.NewSize(420, 240))
	item := homeRecent{appName: "轨迹派车单导出", summary: "已完成"}
	item.rec.JobID = "job-1"
	runUI(func() { view.render([]homeRecent{item}) })
	var buttons []*appButton
	collectShellActionButtons(view.root, &buttons)
	if len(buttons) == 0 {
		t.Fatal("最近结果重建前应先存在图标按钮")
	}
	button := buttons[0]
	showAppButtonTooltip(t, window, button)
	panel := button.tooltip.host.panel
	runUI(func() { view.render([]homeRecent{item}) })
	waitShellCond(t, "最近结果重建前清理 tooltip", func() bool { return !appTooltipDirectPanelVisible(panel) })
}

func TestTooltipScopeParticipatesInObjectTraversal(t *testing.T) {
	child := widget.NewLabel("内容")
	scope := newAppTooltipScope(container.NewVBox(child))
	if !containsObject(scope, child) {
		t.Fatal("滚动与显隐查找必须穿透 appTooltipScope")
	}
}
