package appshell

import (
	"strings"
	"testing"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/driver/desktop"
	"fyne.io/fyne/v2/test"
	"fyne.io/fyne/v2/widget"

	"github.com/zhangzhenyu91/j1toolkit/uvmp-toolkit/core-go/internal/ui"
)

type appSelectControl interface {
	fyne.CanvasObject
	fyne.Tappable
	fyne.Focusable
	fyne.Disableable
	SelectedIndex() int
}

type appSelectFixture struct {
	name         string
	control      appSelectControl
	optionCount  int
	wantSelected string
	selected     func() string
}

func appSelectFixtures(s *Shell) []appSelectFixture {
	history := s.pages["history"].(*pageHistory)
	settings := s.pages["settings"].(*pageSettings)
	tasks := homeTaskList()
	return []appSelectFixture{
		{
			name: "历史工具筛选", control: history.toolFilter, optionCount: len(tasks) + 1,
			wantSelected: tasks[len(tasks)-1].name, selected: func() string { return history.toolFilter.Selected },
		},
		{
			name: "历史状态筛选", control: history.statusFilter, optionCount: len(historyStatusOptions),
			wantSelected: historyStatusOptions[len(historyStatusOptions)-1].label,
			selected:     func() string { return history.statusFilter.Selected },
		},
		{
			name: "设置执行方式", control: settings.driverSel, optionCount: 2,
			wantSelected: driverApp, selected: func() string { return settings.driverSel.Selected },
		},
	}
}

func appSelectPopupOptions(root fyne.CanvasObject) []interface {
	desktop.Cursorable
	fyne.Tappable
} {
	var options []interface {
		desktop.Cursorable
		fyne.Tappable
	}
	walkPageCanvasObjects(root, func(object fyne.CanvasObject) bool {
		if option, ok := object.(interface {
			desktop.Cursorable
			fyne.Tappable
		}); ok {
			options = append(options, option)
			return false
		}
		return true
	})
	return options
}

func TestAppSelectClosedCursorAndDisabledState(t *testing.T) {
	app := test.NewApp()
	defer app.Quit()
	s := newTestShell(t, app, ui.NewFakeBackend(), Options{DisableTray: true, DisableScheduler: true})
	for _, fixture := range appSelectFixtures(s) {
		t.Run(fixture.name, func(t *testing.T) {
			cursorable, ok := fixture.control.(desktop.Cursorable)
			if !ok {
				t.Fatalf("闭合 Select %T 未实现 desktop.Cursorable", fixture.control)
			}
			if got := cursorable.Cursor(); got != desktop.PointerCursor {
				t.Fatalf("启用 Select 应使用手型指针：%v", got)
			}
			fixture.control.Disable()
			if got := cursorable.Cursor(); got != desktop.DefaultCursor {
				t.Fatalf("禁用 Select 应使用普通指针：%v", got)
			}
			fixture.control.Tapped(nil)
			if overlay := s.Win.Canvas().Overlays().Top(); overlay != nil {
				t.Fatal("禁用 Select 不应打开弹层")
			}
			fixture.control.Enable()
			if got := cursorable.Cursor(); got != desktop.PointerCursor {
				t.Fatalf("重新启用 Select 应恢复手型指针：%v", got)
			}
		})
	}
}

func TestAppSelectPopupOptionsUsePointerCursorAndSelect(t *testing.T) {
	app := test.NewApp()
	defer app.Quit()
	s := newTestShell(t, app, ui.NewFakeBackend(), Options{DisableTray: true, DisableScheduler: true})
	for _, fixture := range appSelectFixtures(s) {
		t.Run(fixture.name, func(t *testing.T) {
			fixture.control.Tapped(nil)
			overlay := s.Win.Canvas().Overlays().Top()
			if overlay == nil {
				t.Fatal("Select 点击后应打开真实弹层")
			}
			defer s.Win.Canvas().Overlays().Remove(overlay)
			options := appSelectPopupOptions(overlay)
			if len(options) != fixture.optionCount {
				t.Fatalf("弹层手型选项数=%d，期望每个选项均有手型，共 %d 项", len(options), fixture.optionCount)
			}
			for i, option := range options {
				if got := option.Cursor(); got != desktop.PointerCursor {
					t.Fatalf("弹层第 %d 项应使用手型指针：%v", i, got)
				}
			}
			options[len(options)-1].Tapped(nil)
			if got := fixture.selected(); got != fixture.wantSelected {
				t.Fatalf("弹层选择结果=%q，期望 %q", got, fixture.wantSelected)
			}
			if got := fixture.control.SelectedIndex(); got != fixture.optionCount-1 {
				t.Fatalf("弹层选择索引=%d，期望 %d", got, fixture.optionCount-1)
			}
			if s.Win.Canvas().Overlays().Top() != nil {
				t.Fatal("选择后应关闭弹层")
			}
		})
	}
}

func TestAppSelectPopupOptionsHaveSemanticIcons(t *testing.T) {
	app := test.NewApp()
	defer app.Quit()
	s := newTestShell(t, app, ui.NewFakeBackend(), Options{DisableTray: true, DisableScheduler: true})
	selectControl := s.pages["settings"].(*pageSettings).driverSel
	selectControl.SetSelected(driverApp)
	selectControl.Tapped(nil)
	overlay := s.Win.Canvas().Overlays().Top()
	if overlay == nil {
		t.Fatal("设置 Select 应打开真实弹层")
	}
	defer s.Win.Canvas().Overlays().Remove(overlay)
	options := appSelectPopupOptions(overlay)
	if len(options) != 2 {
		t.Fatalf("设置弹层选项数=%d，期望 2", len(options))
	}
	selectedIndex := selectControl.SelectedIndex()
	for i, option := range options {
		button, ok := option.(*appButton)
		if !ok {
			t.Fatalf("第 %d 项不是 appButton：%T", i, option)
		}
		if button.Icon == nil {
			t.Fatalf("第 %d 项不得是纯文字按钮", i)
		}
		wantIcon := "chevron-right.svg"
		if i == selectedIndex {
			wantIcon = "check.svg"
		}
		if got := button.Icon.Name(); got != wantIcon {
			t.Fatalf("第 %d 项图标=%q，期望 %q", i, got, wantIcon)
		}
		if got := button.Cursor(); got != desktop.PointerCursor {
			t.Fatalf("第 %d 项增加图标后应保留手型：%v", i, got)
		}
	}
}

func TestAppSelectLongListScrollsFromFirstToLastInSmallWindow(t *testing.T) {
	app := test.NewApp()
	defer app.Quit()
	app.Settings().SetTheme(NewTheme())
	optionsText := []string{
		"选项 01", "选项 02", "选项 03", "选项 04", "选项 05", "选项 06",
		"选项 07", "选项 08", "选项 09", "选项 10", "选项 11", "选项 12",
	}
	changed := ""
	selectControl := newAppSelect(optionsText, func(text string) { changed = text })
	window := test.NewWindow(container.NewCenter(selectControl))
	defer window.Close()
	window.Resize(fyne.NewSize(320, 140))
	selectControl.Tapped(nil)
	overlay := window.Canvas().Overlays().Top()
	if overlay == nil || selectControl.popUp == nil {
		t.Fatal("长列表 Select 应打开真实弹层")
	}
	defer window.Canvas().Overlays().Remove(overlay)
	_, areaSize := window.Canvas().InteractiveArea()
	if got := selectControl.popUp.Size().Height; got > areaSize.Height {
		t.Errorf("小窗口弹层高度=%v，可用高度=%v", got, areaSize.Height)
	}
	var scroll *container.Scroll
	walkPageCanvasObjects(overlay, func(object fyne.CanvasObject) bool {
		if current, ok := object.(*container.Scroll); ok {
			scroll = current
			return false
		}
		return true
	})
	if scroll == nil {
		t.Fatal("长列表弹层必须包含真实纵向滚动容器")
	}
	if scroll.Direction != container.ScrollVerticalOnly {
		t.Fatalf("菜单滚动方向=%v，期望仅纵向", scroll.Direction)
	}
	if scroll.Size().Height >= scroll.Content.MinSize().Height {
		t.Fatalf("滚动视口高度=%v 未小于内容高度=%v", scroll.Size().Height, scroll.Content.MinSize().Height)
	}
	options := appSelectPopupOptions(overlay)
	if len(options) != len(optionsText) {
		t.Fatalf("滚动菜单选项数=%d，期望 %d", len(options), len(optionsText))
	}
	first := options[0].(*appButton)
	last := options[len(options)-1].(*appButton)
	firstTop := first.Position().Y + scroll.Content.Position().Y
	if scroll.Offset.Y != 0 || firstTop < 0 || firstTop+first.Size().Height > scroll.Size().Height {
		t.Fatalf("初始首项应完整可见：offset=%v firstTop=%v viewport=%v", scroll.Offset, firstTop, scroll.Size())
	}
	scroll.ScrollToBottom()
	if scroll.Offset.Y <= 0 || scroll.Content.Position().Y >= 0 {
		t.Fatalf("菜单未真实滚到底部：offset=%v content=%v", scroll.Offset, scroll.Content.Position())
	}
	lastTop := last.Position().Y + scroll.Content.Position().Y
	if lastTop < 0 || lastTop+last.Size().Height > scroll.Size().Height {
		t.Fatalf("末项滚动后仍不可达：lastTop=%v lastHeight=%v viewport=%v", lastTop, last.Size().Height, scroll.Size())
	}
	last.Tapped(nil)
	if selectControl.Selected != optionsText[len(optionsText)-1] || changed != optionsText[len(optionsText)-1] {
		t.Fatalf("末项选择或回调错误：selected=%q changed=%q", selectControl.Selected, changed)
	}
	if window.Canvas().Overlays().Top() != nil {
		t.Fatal("选择末项后应关闭弹层")
	}
}

func TestAppSelectKeyboardActiveOptionStaysVisibleInSmallWindow(t *testing.T) {
	app := test.NewApp()
	defer app.Quit()
	app.Settings().SetTheme(NewTheme())
	optionsText := []string{
		"选项 01", "选项 02", "选项 03", "选项 04", "选项 05", "选项 06",
		"选项 07", "选项 08", "选项 09", "选项 10", "选项 11", "选项 12",
	}
	changed := ""
	selectControl := newAppSelect(optionsText, func(text string) { changed = text })
	window := test.NewWindow(container.NewCenter(selectControl))
	defer window.Close()
	window.Resize(fyne.NewSize(320, 140))
	selectControl.TypedKey(&fyne.KeyEvent{Name: fyne.KeyDown})
	menu, ok := window.Canvas().Focused().(*appSelectMenu)
	if !ok {
		t.Fatalf("弹层焦点类型=%T，期望 appSelectMenu", window.Canvas().Focused())
	}
	assertActiveVisible := func(step string, want int) {
		t.Helper()
		if menu.active != want {
			t.Fatalf("%s：活动索引=%d，期望 %d", step, menu.active, want)
		}
		option := menu.options[want]
		top := option.Position().Y + menu.scroll.Content.Position().Y
		if top < 0 || top+option.Size().Height > menu.scroll.Size().Height {
			t.Fatalf("%s：活动项未完整可见：top=%v height=%v viewport=%v offset=%v",
				step, top, option.Size().Height, menu.scroll.Size().Height, menu.scroll.Offset)
		}
	}
	menu.TypedKey(&fyne.KeyEvent{Name: fyne.KeyDown})
	assertActiveVisible("首次 Down", 0)
	for want := 1; want < len(optionsText); want++ {
		menu.TypedKey(&fyne.KeyEvent{Name: fyne.KeyDown})
		assertActiveVisible("连续 Down", want)
	}
	menu.TypedKey(&fyne.KeyEvent{Name: fyne.KeyDown})
	assertActiveVisible("末项 Down 循环", 0)
	menu.TypedKey(&fyne.KeyEvent{Name: fyne.KeyUp})
	assertActiveVisible("首项 Up 循环", len(optionsText)-1)
	for want := len(optionsText) - 2; want >= 0; want-- {
		menu.TypedKey(&fyne.KeyEvent{Name: fyne.KeyUp})
		assertActiveVisible("连续 Up", want)
	}
	menu.TypedKey(&fyne.KeyEvent{Name: fyne.KeyUp})
	assertActiveVisible("再次 Up 循环", len(optionsText)-1)
	menu.TypedKey(&fyne.KeyEvent{Name: fyne.KeyReturn})
	wantSelected := optionsText[len(optionsText)-1]
	if selectControl.Selected != wantSelected || changed != wantSelected {
		t.Fatalf("Enter 选择或回调错误：selected=%q changed=%q，期望 %q", selectControl.Selected, changed, wantSelected)
	}
	if window.Canvas().Overlays().Top() != nil {
		t.Fatal("Enter 选择后应关闭弹层")
	}
}

func TestAppSelectKeyboardInteraction(t *testing.T) {
	app := test.NewApp()
	defer app.Quit()
	s := newTestShell(t, app, ui.NewFakeBackend(), Options{DisableTray: true, DisableScheduler: true})
	selectControl := s.pages["settings"].(*pageSettings).driverSel
	selectControl.SetSelected(driverOS)
	s.Win.Canvas().Focus(selectControl)
	selectControl.TypedKey(&fyne.KeyEvent{Name: fyne.KeyRight})
	if selectControl.Selected != driverApp {
		t.Fatalf("右键应选择下一项：%q", selectControl.Selected)
	}
	selectControl.TypedKey(&fyne.KeyEvent{Name: fyne.KeyLeft})
	if selectControl.Selected != driverOS {
		t.Fatalf("左键应选择上一项：%q", selectControl.Selected)
	}
	selectControl.TypedKey(&fyne.KeyEvent{Name: fyne.KeyDown})
	overlay := s.Win.Canvas().Overlays().Top()
	if overlay == nil {
		t.Fatal("下方向键应打开 Select 弹层")
	}
	defer s.Win.Canvas().Overlays().Remove(overlay)
	options := appSelectPopupOptions(overlay)
	if len(options) != 2 {
		t.Fatalf("键盘弹层的两个选项均应实现手型，实际 %d", len(options))
	}
	focused := s.Win.Canvas().Focused()
	if focused == nil || focused == selectControl {
		t.Fatalf("弹层应接管键盘焦点，实际 %T", focused)
	}
	focused.TypedKey(&fyne.KeyEvent{Name: fyne.KeyDown})
	focused.TypedKey(&fyne.KeyEvent{Name: fyne.KeyReturn})
	if selectControl.Selected != driverOS {
		t.Fatalf("弹层方向键+回车应选择第一项：%q", selectControl.Selected)
	}
	if s.Win.Canvas().Overlays().Top() != nil {
		t.Fatal("键盘选择后应关闭弹层")
	}
}

func TestHistorySelectPopupRetainsChangedCallback(t *testing.T) {
	app := test.NewApp()
	defer app.Quit()
	s := newTestShell(t, app, ui.NewFakeBackend(), Options{DisableTray: true, DisableScheduler: true})
	p := s.pages["history"].(*pageHistory)
	p.all = []historyEntry{
		{rec: ui.HistoryRecord{JobID: "paused", Status: ui.JobRunning, Result: map[string]any{"paused": true}}},
		{rec: ui.HistoryRecord{JobID: "success", Status: ui.JobSuccess}},
	}
	p.applyFilters()
	p.statusFilter.Tapped(nil)
	overlay := s.Win.Canvas().Overlays().Top()
	if overlay == nil {
		t.Fatal("状态筛选应打开弹层")
	}
	defer s.Win.Canvas().Overlays().Remove(overlay)
	options := appSelectPopupOptions(overlay)
	if len(options) != len(historyStatusOptions) {
		t.Fatalf("状态筛选弹层手型选项数=%d，期望 %d", len(options), len(historyStatusOptions))
	}
	options[len(options)-1].Tapped(nil)
	if p.statusFilter.Selected != "已暂停" || len(p.filtered) != 1 || p.filtered[0].rec.JobID != "paused" {
		t.Fatalf("选择回调未继续驱动历史筛选：selected=%q filtered=%+v", p.statusFilter.Selected, p.filtered)
	}
}

func TestAppButtonConstructsTextIconAndChineseAccessibilityLabel(t *testing.T) {
	a := test.NewApp()
	defer a.Quit()
	button := newAppButton("保存设置", "download", widget.MediumImportance, nil)
	if button.Text != "保存设置" || button.Importance != widget.MediumImportance {
		t.Fatalf("按钮文字或重要性未在构造时确定：%q / %v", button.Text, button.Importance)
	}
	if button.Icon == nil {
		t.Fatal("图标文字按钮必须构造非 nil 图标")
	}
	if got := button.AccessibilityLabel(); got != "保存设置" {
		t.Fatalf("中文可访问名称不符：%q", got)
	}
	if got := button.AccessibilityRole(); got != fyne.AccessibleRoleButton {
		t.Fatalf("可访问角色应为按钮：%v", got)
	}
}

func TestAppIconButtonHasCompactHitAreaAndChineseAccessibilityLabel(t *testing.T) {
	a := test.NewApp()
	defer a.Quit()
	a.Settings().SetTheme(NewTheme())
	button := newAppIconButton("删除记录", "trash", widget.LowImportance, nil)
	if button.Icon == nil {
		t.Fatal("紧凑图标按钮必须构造非 nil 图标")
	}
	if got := button.AccessibilityLabel(); got != "删除记录" {
		t.Fatalf("紧凑按钮应使用中文可访问名称而非图标文件名：%q", got)
	}
	size := button.MinSize()
	if size.Width < 36 || size.Width > 40 || size.Height < 36 || size.Height > 40 {
		t.Fatalf("紧凑按钮命中区应为 36–40px：%v", size)
	}
}

func TestAppButtonIconContrastIsFixedAtConstruction(t *testing.T) {
	a := test.NewApp()
	defer a.Quit()
	tests := []struct {
		name       string
		importance widget.Importance
		wantStroke string
	}{
		{name: "高强调蓝底白图标", importance: widget.HighImportance, wantStroke: `stroke="#FFFFFF"`},
		{name: "危险红底白图标", importance: widget.DangerImportance, wantStroke: `stroke="#FFFFFF"`},
		{name: "低强调次要文字色", importance: widget.LowImportance, wantStroke: `stroke="#4E5969"`},
		{name: "中强调品牌蓝", importance: widget.MediumImportance, wantStroke: `stroke="#0E3DA8"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			button := newAppButton("操作", "refresh", tt.importance, nil)
			if button.Icon == nil || !strings.Contains(string(button.Icon.Content()), tt.wantStroke) {
				t.Fatalf("构造图标对比色错误，期望 %s", tt.wantStroke)
			}
		})
	}
}

func TestAppButtonKeyboardActivation(t *testing.T) {
	a := test.NewApp()
	defer a.Quit()
	called := 0
	button := newAppButton("开始执行", "play", widget.HighImportance, func() { called++ })
	for _, key := range []fyne.KeyName{fyne.KeySpace, fyne.KeyReturn, fyne.KeyEnter} {
		button.TypedKey(&fyne.KeyEvent{Name: key})
	}
	if called != 3 {
		t.Fatalf("Space/Enter 应各激活一次，实际回调 %d 次", called)
	}
}

func TestAppButtonSetOnTappedReplacesHandlerImmediately(t *testing.T) {
	a := test.NewApp()
	defer a.Quit()
	original, replacement := 0, 0
	button := newAppButton("刷新", "refresh", widget.MediumImportance, func() { original++ })
	button.SetOnTapped(func() { replacement++ })
	button.Tapped(nil)
	if original != 0 || replacement != 1 {
		t.Fatalf("SetOnTapped 应同步替换回调：original=%d replacement=%d", original, replacement)
	}
}

func TestAppButtonDisabledIgnoresActivationAndUsesDefaultCursor(t *testing.T) {
	a := test.NewApp()
	defer a.Quit()
	called := 0
	button := newAppIconButton("删除记录", "trash", widget.DangerImportance, func() { called++ })
	if got := button.Cursor(); got != desktop.PointerCursor {
		t.Fatalf("启用按钮应使用手型指针：%v", got)
	}
	button.Disable()
	button.Tapped(nil)
	button.TypedKey(&fyne.KeyEvent{Name: fyne.KeySpace})
	button.TypedKey(&fyne.KeyEvent{Name: fyne.KeyReturn})
	if called != 0 {
		t.Fatalf("禁用按钮不应回调，实际 %d 次", called)
	}
	if got := button.Cursor(); got != desktop.DefaultCursor {
		t.Fatalf("禁用按钮应使用普通指针：%v", got)
	}
	button.Enable()
	if got := button.Cursor(); got != desktop.PointerCursor {
		t.Fatalf("重新启用按钮应恢复手型指针：%v", got)
	}
}

func TestAppButtonRetainsStandardFocus(t *testing.T) {
	a := test.NewApp()
	defer a.Quit()
	button := newAppButton("保存设置", "check", widget.MediumImportance, nil)
	window := test.NewWindow(button)
	defer window.Close()
	window.Canvas().Focus(button)
	if got := window.Canvas().Focused(); got != button {
		t.Fatalf("标准按钮焦点能力丢失：%T %p", got, got)
	}
}

func TestAppButtonSetActionSynchronizesTextIconAccessibilityAndTooltip(t *testing.T) {
	a := test.NewApp()
	defer a.Quit()
	button := newAppIconButton("收藏", "star", widget.LowImportance, nil)
	setter, ok := any(button).(interface{ SetAction(string, string) })
	if !ok {
		t.Fatal("动态按钮应提供同步文案与语义图标的 SetAction")
	}
	setter.SetAction("取消收藏", "close")
	if button.Text != "" || button.Icon == nil || button.Icon.Name() != "close.svg" {
		t.Fatalf("紧凑按钮应只更新语义图标，不得新增文字改变几何：%q / %v", button.Text, button.Icon)
	}
	if got := button.AccessibilityLabel(); got != "取消收藏" {
		t.Fatalf("动态可访问名称未同步：%q", got)
	}
	if button.tooltip == nil || button.tooltip.label != "取消收藏" {
		t.Fatalf("动态 tooltip 未同步：%+v", button.tooltip)
	}
}
