package appshell

import (
	"image/color"
	"testing"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/test"

	"github.com/zhangzhenyu91/j1toolkit/uvmp-toolkit/core-go/internal/ui"
)

func assertWindowModeChanges(t *testing.T, got []bool, want ...bool) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("最大化通知次数错误：got=%v want=%v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("最大化通知顺序错误：got=%v want=%v", got, want)
		}
	}
}

func TestWindowTitleBar_InitialMaximizedAppliesOnceAfterCallback(t *testing.T) {
	a := test.NewApp()
	defer a.Quit()
	a.Settings().SetTheme(NewTheme())
	ops := &fakeWindowOps{isMaximized: true}
	var bar *windowTitleBar
	runUI(func() { bar = newWindowTitleBar(ops, func() {}) })
	var changes []bool
	bar.onMaximizeChanged = func(maximized bool) { changes = append(changes, maximized) }

	runUI(func() { bar.syncMaximize() })
	assertWindowModeChanges(t, changes, true)
	runUI(func() {
		renderer := test.WidgetRenderer(bar)
		renderer.Layout(fyne.NewSize(1200, titleBarHeight))
		renderer.Layout(fyne.NewSize(1200, titleBarHeight))
	})
	assertWindowModeChanges(t, changes, true)

	runUI(func() {
		ops.isMaximized = false
		bar.syncMaximize()
	})
	assertWindowModeChanges(t, changes, true, false)
}

func TestWindowTitleBar_InitialRestoredDoesNotNotify(t *testing.T) {
	a := test.NewApp()
	defer a.Quit()
	a.Settings().SetTheme(NewTheme())
	ops := &fakeWindowOps{}
	var bar *windowTitleBar
	runUI(func() { bar = newWindowTitleBar(ops, func() {}) })
	var changes []bool
	bar.onMaximizeChanged = func(maximized bool) { changes = append(changes, maximized) }

	runUI(func() {
		bar.syncMaximize()
		test.WidgetRenderer(bar).Layout(fyne.NewSize(900, titleBarHeight))
	})
	assertWindowModeChanges(t, changes)
	runUI(func() {
		ops.isMaximized = true
		bar.syncMaximize()
	})
	assertWindowModeChanges(t, changes, true)
}

func TestWindowTitleBar_MaximizeChangeOnlyOnTransition(t *testing.T) {
	a := test.NewApp()
	defer a.Quit()
	a.Settings().SetTheme(NewTheme())
	ops := &fakeWindowOps{}
	var bar *windowTitleBar
	runUI(func() { bar = newWindowTitleBar(ops, func() {}) })
	var changes []bool
	bar.onMaximizeChanged = func(maximized bool) { changes = append(changes, maximized) }
	assertWindowModeChanges(t, changes)

	runUI(func() {
		ops.isMaximized = true
		bar.syncMaximize()
	})
	assertWindowModeChanges(t, changes, true)
	runUI(func() {
		renderer := test.WidgetRenderer(bar)
		renderer.Layout(fyne.NewSize(900, titleBarHeight))
		renderer.Layout(fyne.NewSize(1000, titleBarHeight))
		bar.syncMaximize()
	})
	assertWindowModeChanges(t, changes, true)

	runUI(func() {
		ops.isMaximized = false
		test.WidgetRenderer(bar).Layout(fyne.NewSize(900, titleBarHeight))
	})
	assertWindowModeChanges(t, changes, true, false)
	runUI(func() { bar.syncMaximize() })
	assertWindowModeChanges(t, changes, true, false)
}

func TestWindowTitleBar_MaximizeChangeFromButtonDoubleTapAndAsyncRestore(t *testing.T) {
	a := test.NewApp()
	defer a.Quit()
	a.Settings().SetTheme(NewTheme())
	ops := &fakeWindowOps{}
	var bar *windowTitleBar
	runUI(func() { bar = newWindowTitleBar(ops, func() {}) })
	var changes []bool
	bar.onMaximizeChanged = func(maximized bool) { changes = append(changes, maximized) }

	runUI(func() { bar.maximize.Tapped(nil) })
	assertWindowModeChanges(t, changes, true)
	runUI(func() {
		test.WidgetRenderer(bar).Layout(fyne.NewSize(1200, titleBarHeight))
		bar.syncMaximize()
	})
	assertWindowModeChanges(t, changes, true)

	runUI(func() {
		ops.isMaximized = false
		bar.Refresh()
	})
	assertWindowModeChanges(t, changes, true, false)

	drag := newWindowDragArea(canvas.NewRectangle(color.Transparent), ops)
	drag.onMaximize = bar.syncMaximize
	runUI(func() { drag.DoubleTapped(&fyne.PointEvent{}) })
	assertWindowModeChanges(t, changes, true, false, true)
	runUI(func() { bar.syncMaximize() })
	assertWindowModeChanges(t, changes, true, false, true)
}

type windowModeSpyPage struct {
	root    fyne.CanvasObject
	onShows int
	changes []bool
}

func newWindowModeSpyPage() *windowModeSpyPage {
	p := &windowModeSpyPage{root: canvas.NewRectangle(color.Transparent)}
	p.root.Hide()
	return p
}

func (p *windowModeSpyPage) Object() fyne.CanvasObject { return p.root }
func (p *windowModeSpyPage) OnShow()                   { p.onShows++ }
func (p *windowModeSpyPage) SetMaximized(maximized bool) {
	p.changes = append(p.changes, maximized)
}

var _ windowModePage = (*windowModeSpyPage)(nil)

var windowModeTestPageIDs = []string{
	"home", "history", "daily_sync", "tree_sync", "crossing_sync",
	"pcd_export", "quiz_search", "course_autoplay", "settings",
}

func TestShellSetWindowMaximized_NotifiesAllNinePagesWithoutActivation(t *testing.T) {
	s := &Shell{pages: make(map[string]Page, len(windowModeTestPageIDs))}
	spies := make(map[string]*windowModeSpyPage, len(windowModeTestPageIDs))
	for _, id := range windowModeTestPageIDs {
		spy := newWindowModeSpyPage()
		spies[id] = spy
		s.pages[id] = spy
	}
	runUI(func() {
		s.setWindowMaximized(false)
		s.setWindowMaximized(true)
		s.setWindowMaximized(false)
	})
	for _, id := range windowModeTestPageIDs {
		spy := spies[id]
		if s.pages[id] != spy {
			t.Errorf("页面 %s 被重建", id)
		}
		assertWindowModeChanges(t, spy.changes, false, true, false)
		if spy.onShows != 0 {
			t.Errorf("页面 %s 被最大化通知激活：%d", id, spy.onShows)
		}
		if spy.root.Visible() {
			t.Errorf("隐藏页面 %s 被最大化通知显示", id)
		}
	}
}

func TestShellSetWindowMaximized_UpdatesContentWidth(t *testing.T) {
	a := test.NewApp()
	defer a.Quit()
	a.Settings().SetTheme(NewTheme())
	body := canvas.NewRectangle(color.Transparent)
	layout := &maxWidthLayout{max: contentMaxW}
	column := container.New(layout, body)
	s := &Shell{contentLayout: layout, contentColumn: column}
	runUI(func() {
		column.Resize(fyne.NewSize(1600, 600))
		s.setWindowMaximized(false)
	})
	if got := body.Size().Width; got != 1080 {
		t.Fatalf("常规内容宽度错误：%.1f", got)
	}
	if got := body.Position().X; got != 260 {
		t.Fatalf("常规内容未居中：%.1f", got)
	}
	runUI(func() { s.setWindowMaximized(true) })
	if got := body.Size().Width; got != 1440 {
		t.Fatalf("最大化内容宽度错误：%.1f", got)
	}
	if got := body.Position().X; got != 80 {
		t.Fatalf("最大化内容未居中：%.1f", got)
	}
	runUI(func() { s.setWindowMaximized(false) })
	if got := body.Size().Width; got != 1080 {
		t.Fatalf("还原后内容宽度错误：%.1f", got)
	}
	if got := body.Position().X; got != 260 {
		t.Fatalf("还原后内容未居中：%.1f", got)
	}
}

func TestShellSetWindowMaximized_PreservesRealPagesAndState(t *testing.T) {
	fake := ui.NewFakeBackend()
	guard := &heavyGuardBackend{Backend: fake}
	a := test.NewApp()
	defer a.Quit()
	s := newTestShell(t, a, guard, Options{DisableTray: true, DisableScheduler: true})
	home := s.pages["home"].(*pageHome)
	waitShellCond(t, "最大化测试的首次首页回填", func() bool {
		var text string
		runUI(func() { text = home.cards["daily_sync"].status.Text })
		return text != "正在读取状态…"
	})
	time.Sleep(300 * time.Millisecond)

	pcd := s.pages["pcd_export"].(*pagePcd)
	course := s.pages["course_autoplay"].(*pageCourse)
	settings := s.pages["settings"].(*pageSettings)
	job := &ui.JobHandle{}
	beforePages := make(map[string]Page, len(windowModeTestPageIDs))
	for _, id := range windowModeTestPageIDs {
		beforePages[id] = s.pages[id]
	}
	beforeCurrent := s.current
	runUI(func() {
		pcd.codesText.SetText("J001\nJ002")
		pcd.outdir.SetText("保持我的输出目录")
		pcd.currentJob = job
		course.startURL.SetText("https://example.invalid/keep")
		course.advancedSec.SetExpanded(true)
		settings.sections[0].SetExpanded(true)
	})
	beforeSettingsExpanded := make([]bool, len(settings.sections))
	for i, section := range settings.sections {
		beforeSettingsExpanded[i] = section.Expanded()
	}
	beforeCourseExpanded := course.advancedSec.Expanded()
	guard.Reset()

	runUI(func() {
		s.setWindowMaximized(false)
		s.setWindowMaximized(true)
		s.setWindowMaximized(false)
	})
	if calls := guard.systemInfoCalls.Load() + guard.appStatusCalls.Load() + guard.quizStateCalls.Load() +
		guard.courseStateCalls.Load() + guard.getConfigCalls.Load() + guard.historyCalls.Load() + guard.pingCalls.Load(); calls != 0 {
		t.Fatalf("最大化通知读取了 Backend：%d", calls)
	}
	if len(s.pages) != len(beforePages) {
		t.Fatalf("页面数量变化：got=%d want=%d", len(s.pages), len(beforePages))
	}
	for id, before := range beforePages {
		if s.pages[id] != before {
			t.Errorf("页面 %s 指针对象变化", id)
		}
	}
	if s.current != beforeCurrent {
		t.Fatalf("当前页面变化：got=%q want=%q", s.current, beforeCurrent)
	}
	runUI(func() {
		if pcd.codesText.Text != "J001\nJ002" || pcd.outdir.Text != "保持我的输出目录" {
			t.Errorf("轨迹导出输入被修改：%q / %q", pcd.codesText.Text, pcd.outdir.Text)
		}
		if course.startURL.Text != "https://example.invalid/keep" {
			t.Errorf("课程输入被修改：%q", course.startURL.Text)
		}
	})
	if pcd.currentJob != job {
		t.Fatal("当前任务句柄被替换")
	}
	if course.advancedSec.Expanded() != beforeCourseExpanded {
		t.Fatal("课程折叠状态被修改")
	}
	for i, section := range settings.sections {
		if section.Expanded() != beforeSettingsExpanded[i] {
			t.Errorf("设置分组 %d 折叠状态被修改", i)
		}
	}
}
