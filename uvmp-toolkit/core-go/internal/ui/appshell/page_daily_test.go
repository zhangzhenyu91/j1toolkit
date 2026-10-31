package appshell

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/test"
	"fyne.io/fyne/v2/widget"

	"github.com/zhangzhenyu91/j1toolkit/uvmp-toolkit/core-go/internal/ui"
)

func TestPageDailyReloadStatusPreservesUnsavedInputs(t *testing.T) {
	f := ui.NewFakeBackend()
	ctx := context.Background()
	app := test.NewApp()
	defer app.Quit()
	s := newTestShell(t, app, f, Options{DisableTray: true, DisableScheduler: true})
	p := s.pages["daily_sync"].(*pageDaily)

	s.Navigate("daily_sync")
	waitShellCond(t, "派车同步首次配置回填", func() bool {
		var loaded bool
		runUI(func() { loaded = p.formLoaded })
		return loaded
	})

	if err := f.SaveConfig(ctx, ui.ConfigPatch{
		Schedule: &ui.ScheduleConfig{Enabled: true, Time: "06:30", Driver: "app"},
		USB:      &ui.USBConfig{Label: "外部U盘", FallbackDir: "D:/外部"},
	}); err != nil {
		t.Fatal(err)
	}
	runUI(func() {
		p.schEnable.SetChecked(false)
		p.schTime.SetText("10:41")
		p.usbLabel.SetText("未保存U盘")
		p.usbFallback.SetText("D:/未保存")
	})

	s.Navigate("home")
	s.Navigate("daily_sync")
	waitShellCond(t, "派车同步后续状态刷新", func() bool {
		var due string
		runUI(func() { due = p.dueLabel.Text })
		return strings.Contains(due, "06:30")
	})
	assertDailyForm(t, p, false, "10:41", "未保存U盘", "D:/未保存")

	runUI(func() { p.saveSchedule() })
	waitShellCond(t, "保存定时后刷新状态", func() bool {
		var due string
		runUI(func() { due = p.dueLabel.Text })
		return strings.Contains(due, "10:41")
	})
	assertDailyForm(t, p, false, "10:41", "未保存U盘", "D:/未保存")
	cfg, err := f.GetConfig(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Schedule.Enabled || cfg.Schedule.Time != "10:41" || cfg.Schedule.Driver != "app" {
		t.Fatalf("保存后的定时配置不符: %+v", cfg.Schedule)
	}
}

func assertDailyForm(t *testing.T, p *pageDaily, enabled bool, timeStr, label, fallback string) {
	t.Helper()
	var gotEnabled bool
	var gotTime, gotLabel, gotFallback string
	runUI(func() {
		gotEnabled = p.schEnable.Checked
		gotTime = p.schTime.Text
		gotLabel = p.usbLabel.Text
		gotFallback = p.usbFallback.Text
	})
	if gotEnabled != enabled || gotTime != timeStr || gotLabel != label || gotFallback != fallback {
		t.Fatalf("状态刷新覆盖了表单输入: enabled=%v time=%q label=%q fallback=%q", gotEnabled, gotTime, gotLabel, gotFallback)
	}
}

// ---------------------------------------------------------------- Task7 常规/最大化布局

// task7CountingBackend 统计窗口模式切换期间是否触达 Backend；切换只允许改显隐与布局。
type task7CountingBackend struct {
	ui.Backend
	calls atomic.Int32
}

func (b *task7CountingBackend) count() { b.calls.Add(1) }

func (b *task7CountingBackend) Ping(ctx context.Context) error {
	b.count()
	return b.Backend.Ping(ctx)
}

func (b *task7CountingBackend) SystemInfo(ctx context.Context) (ui.SystemInfo, error) {
	b.count()
	return b.Backend.SystemInfo(ctx)
}

func (b *task7CountingBackend) GetConfig(ctx context.Context) (ui.Config, error) {
	b.count()
	return b.Backend.GetConfig(ctx)
}

func (b *task7CountingBackend) AppStatus(ctx context.Context, appID string) (map[string]any, error) {
	b.count()
	return b.Backend.AppStatus(ctx, appID)
}

func (b *task7CountingBackend) History(ctx context.Context, appID string) ([]ui.HistoryRecord, error) {
	b.count()
	return b.Backend.History(ctx, appID)
}

func (b *task7CountingBackend) LogStats(ctx context.Context) (ui.LogStats, error) {
	b.count()
	return b.Backend.LogStats(ctx)
}

// task7ChildObjects 返回对象树中的子对象（容器、滚动体、Scope 与自绘控件渲染器）。
func task7ChildObjects(obj fyne.CanvasObject) []fyne.CanvasObject {
	switch o := obj.(type) {
	case *fyne.Container:
		return o.Objects
	case *container.Scroll:
		if o.Content == nil {
			return nil
		}
		return []fyne.CanvasObject{o.Content}
	case *appTooltipScope:
		return []fyne.CanvasObject{o.content}
	case fyne.Widget:
		return test.WidgetRenderer(o).Objects()
	}
	return nil
}

// task7Offset 计算 target 相对 root 的坐标（逐层累加各子对象 Position）。
func task7Offset(root, target fyne.CanvasObject) (fyne.Position, bool) {
	if root == nil {
		return fyne.Position{}, false
	}
	if root == target {
		return fyne.NewPos(0, 0), true
	}
	for _, child := range task7ChildObjects(root) {
		if child == nil {
			continue
		}
		if pos, ok := task7Offset(child, target); ok {
			return pos.Add(child.Position()), true
		}
	}
	return fyne.Position{}, false
}

func task7RequireOffset(t *testing.T, root, target fyne.CanvasObject) fyne.Position {
	t.Helper()
	pos, ok := task7Offset(root, target)
	if !ok {
		t.Fatalf("对象 %T 不在页面树中", target)
	}
	return pos
}

// task7FindLabel 在页面树中查找以 prefix 开头的标签（紧凑状态行断言用）。
func task7FindLabel(root fyne.CanvasObject, prefix string) *widget.Label {
	var found *widget.Label
	var walk func(fyne.CanvasObject)
	walk = func(obj fyne.CanvasObject) {
		if found != nil || obj == nil {
			return
		}
		if label, ok := obj.(*widget.Label); ok {
			if strings.HasPrefix(label.Text, prefix) {
				found = label
				return
			}
		}
		for _, child := range task7ChildObjects(obj) {
			walk(child)
		}
	}
	walk(root)
	return found
}

func task7ScrollContent(t *testing.T, root fyne.CanvasObject) *fyne.Container {
	t.Helper()
	scroll, ok := root.(*container.Scroll)
	if !ok {
		t.Fatalf("页面根对象应为整页滚动体，实际 %T", root)
	}
	content, ok := scroll.Content.(*fyne.Container)
	if !ok {
		t.Fatalf("整页滚动内容应为容器，实际 %T", scroll.Content)
	}
	return content
}

func task7RequireWindowMode(t *testing.T, page Page) windowModePage {
	t.Helper()
	mode, ok := page.(windowModePage)
	if !ok {
		t.Fatalf("页面 %T 未实现 windowModePage", page)
	}
	return mode
}

func task7AssertNoBackendCalls(t *testing.T, backend *task7CountingBackend, step string) {
	t.Helper()
	if got := backend.calls.Load(); got != 0 {
		t.Fatalf("%s：切换窗口模式触发了 %d 次 Backend 调用", step, got)
	}
}

func task7ResizePage(t *testing.T, root fyne.CanvasObject, width, height float32) {
	t.Helper()
	runUI(func() {
		root.Resize(fyne.NewSize(width, height))
		root.Refresh()
	})
}

func TestPageDailyNormalModeKeepsSecondaryStatusCollapsed(t *testing.T) {
	f := ui.NewFakeBackend()
	app := test.NewApp()
	defer app.Quit()
	s := newTestShell(t, app, f, Options{DisableTray: true, DisableScheduler: true})
	p := s.pages["daily_sync"].(*pageDaily)
	s.Navigate("daily_sync")
	waitShellCond(t, "派车同步首次配置回填", func() bool {
		var loaded bool
		runUI(func() { loaded = p.formLoaded })
		return loaded
	})
	task7RequireWindowMode(t, p)

	runUI(func() {
		if pageCanvasObjectEffectivelyVisible(p.root, p.statusKV.box) {
			t.Fatal("常规模式不应展开四行完整状态")
		}
		if pageCanvasObjectEffectivelyVisible(p.root, p.history.Root) {
			t.Fatal("常规模式应隐藏执行记录次级内容")
		}
		for name, target := range map[string]fyne.CanvasObject{
			"到期状态":  p.dueLabel,
			"U 盘位置": p.usbMount,
			"同步设置":  p.schTime,
			"U 盘名称": p.usbLabel,
			"备用位置":  p.usbFallback,
			"立即执行":  p.startBtn,
			"结果区":   p.result.Root,
			"排错日志":  p.result.LogRoot,
		} {
			if !pageCanvasObjectEffectivelyVisible(p.root, target) {
				t.Fatalf("常规模式缺少主流程控件：%s", name)
			}
		}
	})

	// 失败终态必须自动展开排错日志，且日志入口仍可见。
	runUI(func() { p.result.FailStart(errors.New("布局回归失败")) })
	if !p.result.logSec.Expanded() {
		t.Fatal("派车页失败终态未自动展开排错日志")
	}
	runUI(func() {
		if !pageCanvasObjectEffectivelyVisible(p.root, p.result.LogRoot) {
			t.Fatal("失败后排错日志入口不可见")
		}
	})
}

func TestPageDailyMainFlowFitsNormalViewport(t *testing.T) {
	f := ui.NewFakeBackend()
	app := test.NewApp()
	defer app.Quit()
	s := newTestShell(t, app, f, Options{DisableTray: true, DisableScheduler: true})
	p := s.pages["daily_sync"].(*pageDaily)
	s.Navigate("daily_sync")
	waitShellCond(t, "派车同步首次配置回填", func() bool {
		var loaded bool
		runUI(func() { loaded = p.formLoaded })
		return loaded
	})

	// 结果区进入执行中（摘要可见），首屏必须容纳真实卡片高度而不是空闲 MinSize。
	runUI(func() {
		p.result.Begin("正在导出派车单，请稍候…")
		p.progress.Reset() // progressRow 改为 Reset 显示/默认隐藏后，仍按真实执行流展示进度
		p.progress.Set(1, 3, "查询 1/3")
	})
	// 默认窗口 1160×780：扣除顶栏、页头与侧栏后页面约为 928×638，主流程（状态/设置/执行）应在一屏内。
	task7ResizePage(t, p.root, 928, 638)
	content := task7ScrollContent(t, p.root)
	runCard := content.Objects[2]
	if bottom := runCard.Position().Y + runCard.Size().Height; bottom > 638 {
		t.Fatalf("1160×780 常规窗口主流程超出首屏：执行卡底部 %.1f > 638", bottom)
	}
	runUI(func() {
		if !p.result.summary.Visible() || !strings.Contains(p.result.summary.Text, "正在导出") {
			t.Fatalf("执行中摘要未展示：%q", p.result.summary.Text)
		}
		if p.result.status.Text != "执行中" {
			t.Fatalf("结果状态应为执行中：%q", p.result.status.Text)
		}
		if !pageCanvasObjectEffectivelyVisible(p.root, p.progress.Root) {
			t.Fatal("执行中进度区应可见")
		}
	})
	if min := content.MinSize(); min.Width > 928 {
		t.Fatalf("常规窗口出现横向溢出：内容最小宽度 %.1f > 928", min.Width)
	}

	// 900×600：关键设置与执行仍可通过整页滚动到达底部。
	task7ResizePage(t, p.root, 900, 600)
	scroll := p.root.(*container.Scroll)
	runUI(func() { scroll.ScrollToBottom() })
	wantOffset := fyne.Max(0, content.MinSize().Height-scroll.Size().Height)
	if scroll.Offset.Y != wantOffset {
		t.Fatalf("900×600 未滚动到内容底部：offset=%.1f want=%.1f", scroll.Offset.Y, wantOffset)
	}
	if runCard.Position().Y+runCard.MinSize().Height > content.MinSize().Height {
		t.Fatal("执行卡超出内容范围，无法通过滚动到达")
	}

	// 668×458 常规同样必须能滚到底。
	task7ResizePage(t, p.root, 668, 458)
	runUI(func() { scroll.ScrollToBottom() })
	if want := fyne.Max(0, content.Size().Height-scroll.Size().Height); scroll.Offset.Y != want {
		t.Fatalf("668×458 常规未滚动到内容底部：offset=%.1f want=%.1f", scroll.Offset.Y, want)
	}
	if scroll.Offset.Y <= 0 {
		t.Fatal("668×458 常规长内容应可滚动")
	}
}

func TestPageDailyMaximizedModeUsesTwoColumnsAndKeepsState(t *testing.T) {
	f := ui.NewFakeBackend()
	counter := &task7CountingBackend{Backend: f}
	app := test.NewApp()
	defer app.Quit()
	s := newTestShell(t, app, counter, Options{DisableTray: true, DisableScheduler: true})
	p := s.pages["daily_sync"].(*pageDaily)
	s.Navigate("daily_sync")
	waitShellCond(t, "派车同步首次配置回填", func() bool {
		var loaded bool
		runUI(func() { loaded = p.formLoaded })
		return loaded
	})
	time.Sleep(150 * time.Millisecond)
	task7RequireWindowMode(t, p)

	job := &ui.JobHandle{}
	runUI(func() {
		p.schTime.SetText("11:11")
		p.usbLabel.SetText("布局测试U盘")
		p.usbFallback.SetText("D:/布局测试")
		p.runDate.SetText("2026-10-30")
		p.currentJob = job
		p.startBtn.Disable()
	})
	root := p.root
	content := task7ScrollContent(t, p.root)
	historyRoot := p.history.Root
	counter.calls.Store(0)

	task7ResizePage(t, root, 1200, 700)
	runUI(func() { s.setWindowMaximized(true) })
	task7AssertNoBackendCalls(t, counter, "最大化")

	if p.root != root || task7ScrollContent(t, p.root) != content || p.history.Root != historyRoot {
		t.Fatal("切换最大化重建了页面或内容容器")
	}
	runUI(func() {
		if !pageCanvasObjectEffectivelyVisible(p.root, p.statusKV.box) {
			t.Fatal("最大化应显示四行完整状态")
		}
		if !pageCanvasObjectEffectivelyVisible(p.root, p.history.Root) {
			t.Fatal("最大化应显示执行记录")
		}
		if !pageCanvasObjectEffectivelyVisible(p.root, p.result.LogRoot) {
			t.Fatal("最大化不得隐藏排错日志入口")
		}
	})
	rows, ok := p.statusKV.box.Objects[0].(*fyne.Container)
	if !ok || len(rows.Objects) != 4 {
		t.Fatalf("完整状态应为四行，实际 %T", p.statusKV.box.Objects[0])
	}
	task7ResizePage(t, root, 1200, 700)
	first := task7RequireOffset(t, content, content.Objects[0])
	right := task7RequireOffset(t, content, content.Objects[2])
	if first.Y != right.Y || first.X == right.X {
		t.Fatalf("最大化应为左状态/设置、右执行/日志双列：left=%v right=%v", first, right)
	}

	runUI(func() {
		if p.schTime.Text != "11:11" || p.usbLabel.Text != "布局测试U盘" || p.usbFallback.Text != "D:/布局测试" || p.runDate.Text != "2026-10-30" {
			t.Fatal("最大化切换清空了未保存输入")
		}
		if p.currentJob != job || !p.startBtn.Disabled() {
			t.Fatal("最大化切换清空或替换了进行中的任务状态")
		}
	})

	// 668×458：最大化窄宽必须退化为单列，全部预建内容可经整页滚动到达。
	task7ResizePage(t, root, 668, 458)
	for i := 1; i < len(content.Objects); i++ {
		if !content.Objects[i].Visible() {
			continue
		}
		pos := task7RequireOffset(t, content, content.Objects[i])
		if pos.X != 0 {
			t.Fatalf("668×458 最大化第 %d 张卡片未退化为单列：x=%.1f", i, pos.X)
		}
	}
	scroll := root.(*container.Scroll)
	runUI(func() { scroll.ScrollToBottom() })
	last := content.Objects[len(content.Objects)-1]
	if bottom := last.Position().Y + last.Size().Height; bottom > content.Size().Height {
		t.Fatalf("668×458 最大化末卡超出内容：bottom=%.1f content=%.1f", bottom, content.Size().Height)
	}
	if want := fyne.Max(0, content.Size().Height-scroll.Size().Height); scroll.Offset.Y != want {
		t.Fatalf("668×458 最大化未滚到内容底部：offset=%.1f want=%.1f", scroll.Offset.Y, want)
	}
	if scroll.Offset.Y <= 0 {
		t.Fatal("668×458 最大化长内容应可滚动")
	}

	counter.calls.Store(0)
	runUI(func() { s.setWindowMaximized(false) })
	task7AssertNoBackendCalls(t, counter, "还原")
	if s.pages["daily_sync"] != p || s.current != "daily_sync" {
		t.Fatal("切换最大化重建了页面或切走了当前页")
	}
	runUI(func() {
		if pageCanvasObjectEffectivelyVisible(p.root, p.statusKV.box) {
			t.Fatal("还原后应重新收起四行完整状态")
		}
		if pageCanvasObjectEffectivelyVisible(p.root, p.history.Root) {
			t.Fatal("还原后应重新隐藏执行记录")
		}
		if p.schTime.Text != "11:11" || p.currentJob != job {
			t.Fatal("还原后输入或任务状态丢失")
		}
	})
	task7ResizePage(t, root, 1200, 700)
	first = task7RequireOffset(t, content, content.Objects[0])
	restored := task7RequireOffset(t, content, content.Objects[1])
	if first.X != restored.X || first.Y == restored.Y {
		t.Fatalf("还原后应恢复单列：first=%v second=%v", first, restored)
	}
}

func TestPageDailyFailureLogScrollsIntoView(t *testing.T) {
	f := ui.NewFakeBackend()
	app := test.NewApp()
	defer app.Quit()
	s := newTestShell(t, app, f, Options{DisableTray: true, DisableScheduler: true})
	p := s.pages["daily_sync"].(*pageDaily)
	s.Navigate("daily_sync")
	waitShellCond(t, "派车同步首次配置回填", func() bool {
		var loaded bool
		runUI(func() { loaded = p.formLoaded })
		return loaded
	})
	content := task7ScrollContent(t, p.root)
	task7ResizePage(t, p.root, 900, 360)
	runUI(func() { p.scroll.ScrollToTop() })

	runUI(func() { p.result.FailStart(errors.New("布局回归失败")) })
	if !p.result.logSec.Expanded() {
		t.Fatal("失败终态应自动展开排错日志")
	}
	offset := p.scroll.Offset.Y
	pos := task7RequireOffset(t, content, p.result.LogRoot)
	if offset <= 0 {
		t.Fatalf("失败终态未在页面滚动区滚动：offset=%.1f", offset)
	}
	if pos.Y < offset-0.5 || pos.Y+p.result.LogRoot.MinSize().Height > offset+p.scroll.Size().Height+0.5 {
		t.Fatalf("失败日志未进入页面视口：logY=%.1f h=%.1f offset=%.1f view=%.1f",
			pos.Y, p.result.LogRoot.MinSize().Height, offset, p.scroll.Size().Height)
	}

	// 成功终态不得跳滚。
	runUI(func() {
		p.scroll.ScrollToTop()
		p.result.Begin("重新开始")
		p.result.Apply(taskOutcome{Status: "成功", Summary: "完成"})
	})
	if p.scroll.Offset.Y != 0 {
		t.Fatalf("成功终态不应滚动页面：offset=%.1f", p.scroll.Offset.Y)
	}
}

type task7Rect struct {
	x, y, w, h float32
}

func task7RectOf(t *testing.T, root, obj fyne.CanvasObject) task7Rect {
	t.Helper()
	pos := task7RequireOffset(t, root, obj)
	return task7Rect{x: pos.X, y: pos.Y, w: obj.Size().Width, h: obj.Size().Height}
}

func (r task7Rect) overlaps(o task7Rect) bool {
	return r.x < o.x+o.w && o.x < r.x+r.w && r.y < o.y+o.h && o.y < r.y+r.h
}

func assertTask7TextFits(t *testing.T, step string, obj fyne.CanvasObject) {
	t.Helper()
	min := obj.MinSize()
	if obj.Size().Width+0.5 < min.Width || obj.Size().Height+0.5 < min.Height {
		t.Fatalf("%s：文本矩形 %.1f×%.1f 小于最小需求 %.1f×%.1f", step, obj.Size().Width, obj.Size().Height, min.Width, min.Height)
	}
}

func TestPageDailyStatusCardModesAndTextGeometry(t *testing.T) {
	f := ui.NewFakeBackend()
	app := test.NewApp()
	defer app.Quit()
	s := newTestShell(t, app, f, Options{DisableTray: true, DisableScheduler: true})
	p := s.pages["daily_sync"].(*pageDaily)
	s.Navigate("daily_sync")
	waitShellCond(t, "派车同步首次配置回填", func() bool {
		var loaded bool
		runUI(func() { loaded = p.formLoaded })
		return loaded
	})
	runUI(func() {
		p.dueLabel.SetText("今天 09:15 将自动导出派车单（共 12 条）")
		p.usbMount.SetText("U 盘位置：/run/media/user/GLKVM-很长的挂载点名称")
	})
	task7ResizePage(t, p.root, 928, 638)

	runUI(func() {
		title := task7FindLabel(p.root, "运行状态")
		if title == nil || !pageCanvasObjectEffectivelyVisible(p.root, title) {
			t.Fatal("状态卡标题应为运行状态且可见")
		}
		if !pageCanvasObjectEffectivelyVisible(p.root, p.dueLabel) || !pageCanvasObjectEffectivelyVisible(p.root, p.usbMount) {
			t.Fatal("常规应显示到期与 U 盘紧凑状态")
		}
		if pageCanvasObjectEffectivelyVisible(p.root, p.statusKV.box) {
			t.Fatal("常规只应显示紧凑状态，不显示完整 statusKV")
		}
		due := task7RectOf(t, p.root, p.dueLabel)
		usb := task7RectOf(t, p.root, p.usbMount)
		if due.overlaps(usb) {
			t.Fatalf("常规紧凑状态文本重叠：due=%+v usb=%+v", due, usb)
		}
		titleRect := task7RectOf(t, p.root, title)
		if titleRect.overlaps(due) || titleRect.overlaps(usb) {
			t.Fatalf("常规状态标题与紧凑文本重叠：title=%+v due=%+v usb=%+v", titleRect, due, usb)
		}
		assertTask7TextFits(t, "常规到期状态", p.dueLabel)
		assertTask7TextFits(t, "常规 U 盘状态", p.usbMount)
	})

	runUI(func() { s.setWindowMaximized(true) })
	task7ResizePage(t, p.root, 1200, 700)
	runUI(func() {
		title := task7FindLabel(p.root, "运行状态")
		if title == nil || !pageCanvasObjectEffectivelyVisible(p.root, title) {
			t.Fatal("最大化状态卡标题应为运行状态且可见")
		}
		if !pageCanvasObjectEffectivelyVisible(p.root, p.statusKV.box) {
			t.Fatal("最大化应显示完整 statusKV")
		}
		if pageCanvasObjectEffectivelyVisible(p.root, p.dueLabel) || pageCanvasObjectEffectivelyVisible(p.root, p.usbMount) {
			t.Fatal("最大化只应显示完整 statusKV，不显示紧凑状态")
		}
		assertTask7TextFits(t, "最大化完整状态", p.statusKV.box)
	})

	runUI(func() { s.setWindowMaximized(false) })
	runUI(func() {
		if !pageCanvasObjectEffectivelyVisible(p.root, p.dueLabel) {
			t.Fatal("还原后应恢复紧凑状态")
		}
		if pageCanvasObjectEffectivelyVisible(p.root, p.statusKV.box) {
			t.Fatal("还原后应隐藏完整 statusKV")
		}
	})
}
