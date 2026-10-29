package appshell

import (
	"bytes"
	"context"
	"errors"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/test"
	"fyne.io/fyne/v2/widget"

	"github.com/zhangzhenyu91/j1toolkit/uvmp-toolkit/core-go/internal/ui"
)

// 壳层重做后的结构性冒烟：折叠分组契约、侧栏/页头联动、无托盘扫描事件、
// 首页任务入口与最近结果、设置分组与浏览器留空自动检测。
// 全部走 FakeBackend（零网络、零真实业务）。

// TestCollapsibleSection_Contract 另一代理约定控件契约：Root/SetExpanded/Expanded/Toggle，
// 标题行可点，收起时内容不占高度。
func TestCollapsibleSection_Contract(t *testing.T) {
	app := test.NewApp()
	defer app.Quit()

	content := widget.NewLabel("分组内容")
	var sec *collapsibleSection
	runUI(func() { sec = newCollapsibleSection("分组标题", content, false) })

	if sec == nil || sec.Root == nil {
		t.Fatal("折叠分组未构造")
	}
	if sec.Expanded() {
		t.Fatal("initiallyExpanded=false 时应为收起")
	}
	if sec.body.Visible() {
		t.Fatal("收起时内容应隐藏")
	}
	if got := sec.title; got != "分组标题" {
		t.Fatalf("标题不符: %q", got)
	}

	// 点击标题行展开
	runUI(func() { sec.header.Tapped(&fyne.PointEvent{}) })
	if !sec.Expanded() || !sec.body.Visible() {
		t.Fatal("点击标题行应展开并显示内容")
	}

	// 直接收起 / Toggle 展开
	runUI(func() { sec.SetExpanded(false) })
	if sec.Expanded() || sec.body.Visible() {
		t.Fatal("SetExpanded(false) 应收起并隐藏内容")
	}
	runUI(func() { sec.Toggle() })
	if !sec.Expanded() || !sec.body.Visible() {
		t.Fatal("Toggle 应展开")
	}
}

// TestShell_ShellChrome 侧栏 8 项、设置沉底、页头随切页更新、运行中任务提示。
func TestShell_ShellChrome(t *testing.T) {
	f := ui.NewFakeBackend()
	f.StepDelay = 100 * time.Millisecond
	app := test.NewApp()
	defer app.Quit()
	s := newTestShell(t, app, f, Options{DisableTray: true, DisableScheduler: true})

	if len(s.navEntries) != 8 {
		t.Fatalf("侧栏应有 8 个常驻入口: %d", len(s.navEntries))
	}
	var title, subtitle string
	runUI(func() {
		title, subtitle = s.header.title.Text, s.header.subtitle.Text
	})
	if title != "首页" || subtitle == "" {
		t.Fatalf("首页页头不符: %q / %q", title, subtitle)
	}

	runUI(func() { s.Navigate("settings") })
	runUI(func() {
		title = s.header.title.Text
	})
	if title != "设置" {
		t.Fatalf("切页后页头未更新: %q", title)
	}
	var settingsActive, homeActive, settingsVisible, homeVisible bool
	runUI(func() {
		settingsActive = s.navEntries["settings"].active
		homeActive = s.navEntries["home"].active
		settingsVisible = s.pages["settings"].Object().Visible()
		homeVisible = s.pages["home"].Object().Visible()
	})
	if !settingsActive || homeActive {
		t.Fatalf("导航高亮不符: settings=%v home=%v", settingsActive, homeActive)
	}
	if !settingsVisible || homeVisible {
		t.Fatalf("常驻页 Show/Hide 不符: settings=%v home=%v", settingsVisible, homeVisible)
	}

	// 运行中任务在页头提示，结束后隐藏
	done := make(chan struct{}, 1)
	s.StartJob("daily_sync", map[string]any{}, ui.JobHandler{OnDone: func(ui.Job) { done <- struct{}{} }})
	waitShellCond(t, "页头运行中提示", func() bool {
		var visible bool
		var text string
		runUI(func() {
			visible = s.header.status.Visible()
			text = s.header.status.Text
		})
		return visible && strings.Contains(text, "执行中")
	})
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("等待任务结束超时")
	}
	waitShellCond(t, "页头运行中提示消失", func() bool {
		var visible bool
		runUI(func() { visible = s.header.status.Visible() })
		return !visible
	})
}

// TestShell_QuizEventsWithoutTray 无托盘（DisableTray）时扫描事件不得触碰 nil 托盘句柄。
func TestShell_QuizEventsWithoutTray(t *testing.T) {
	f := ui.NewFakeBackend()
	f.StepDelay = 0
	ctx := context.Background()
	app := test.NewApp()
	defer app.Quit()
	s := newTestShell(t, app, f, Options{DisableTray: true, DisableScheduler: true})
	if s.tray != nil {
		t.Fatal("DisableTray 下托盘应为 nil")
	}

	if _, err := f.QuizImportBank(ctx, "安规.xlsx"); err != nil {
		t.Fatalf("QuizImportBank: %v", err)
	}
	if _, err := f.QuizScanStart(ctx); err != nil {
		t.Fatalf("QuizScanStart: %v", err)
	}
	waitShellCond(t, "扫描事件回流", func() bool {
		st, _ := f.QuizScanState(ctx)
		return st.Scanning
	})
	// 事件泵在独立协程处理；nil 托盘解引用会 panic 整个测试进程。
	time.Sleep(100 * time.Millisecond)
	if _, err := f.QuizScanStop(ctx); err != nil {
		t.Fatalf("QuizScanStop: %v", err)
	}
	if s.RunningJobs() != 0 {
		t.Fatalf("扫描启停不应产生任务计数: %d", s.RunningJobs())
	}
}

// TestHome_TaskEntriesAndRecentResults 首页六个任务入口 + 最近结果按应用汇总。
func TestHome_TaskEntriesAndRecentResults(t *testing.T) {
	f := ui.NewFakeBackend()
	f.StepDelay = time.Millisecond
	app := test.NewApp()
	defer app.Quit()
	s := newTestShell(t, app, f, Options{DisableTray: true, DisableScheduler: true})
	p := s.pages["home"].(*pageHome)

	if len(p.cards) != len(homeTaskList()) {
		t.Fatalf("任务入口数量不符: %d / %d", len(p.cards), len(homeTaskList()))
	}
	// 先跑一次派车同步，产生执行记录
	done := make(chan ui.Job, 1)
	s.StartJob("daily_sync", map[string]any{}, ui.JobHandler{OnDone: func(job ui.Job) { done <- job }})
	select {
	case job := <-done:
		if job.Status != ui.JobSuccess {
			t.Fatalf("任务未成功: %+v", job)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("等待任务结束超时")
	}

	runUI(func() { p.reload() })
	waitShellCond(t, "首页最近结果", func() bool {
		var n int
		runUI(func() { n = len(p.recent) })
		return n == 1
	})
	var appID, summary, status string
	runUI(func() {
		appID = p.recent[0].appID
		summary = p.recent[0].summary
		status = p.cards["daily_sync"].status.Text
	})
	if appID != "daily_sync" {
		t.Fatalf("最近结果应用不符: %q", appID)
	}
	if !strings.Contains(summary, "6 条") {
		t.Fatalf("最近结果摘要不符: %q", summary)
	}
	if !strings.Contains(status, "最近执行 成功") || !strings.Contains(status, "定时 09:15") {
		t.Fatalf("任务入口状态未使用最近执行/定时配置: %q", status)
	}
	var rows int
	runUI(func() { rows = len(p.recentBox.Objects) })
	if rows == 0 {
		t.Fatal("最近结果未渲染")
	}
}

// TestSettings_GroupsAndBrowserAuto 设置四分组（诊断默认收起）+ 浏览器留空自动检测口径。
func TestSettings_GroupsAndBrowserAuto(t *testing.T) {
	f := ui.NewFakeBackend()
	f.StepDelay = time.Millisecond
	ctx := context.Background()
	app := test.NewApp()
	defer app.Quit()
	s := newTestShell(t, app, f, Options{DisableTray: true, DisableScheduler: true})
	p := s.pages["settings"].(*pageSettings)
	runUI(func() { s.Navigate("settings") })
	time.Sleep(150 * time.Millisecond)

	if len(p.sections) != 4 {
		t.Fatalf("设置应分 4 组: %d", len(p.sections))
	}
	want := []string{"账号 · 派车系统", "宿主浏览器", "导出与后台", "开发诊断"}
	for i, sec := range p.sections {
		if sec.title != want[i] {
			t.Fatalf("第 %d 组标题不符: %q", i+1, sec.title)
		}
	}
	if p.diag.Expanded() {
		t.Fatal("开发诊断应默认收起（路径/自检/日志进入诊断）")
	}
	if p.logSection.Expanded() {
		t.Fatal("自检日志应默认收起")
	}

	// 浏览器留空 = 自动检测宿主（builtin=false）；FakeBackend 探测不到也不改口径。
	runUI(func() {
		p.browser.SetText("  ")
		p.saveBrowser()
	})
	waitFakeCond(t, "浏览器留空保存为自动检测", func() bool {
		cfg, _ := f.GetConfig(ctx)
		return cfg.Render.Chrome == "" && !cfg.Render.Builtin
	})
	time.Sleep(150 * time.Millisecond) // 等保存尾部的 loadConfig/Toast 收口，避免连续保存偶发抢锁

	// 显式指定：去空格后落盘。
	runUI(func() {
		p.browser.SetText("  C:/Apps/Chrome/chrome.exe ")
		p.saveBrowser()
	})
	waitFakeCond(t, "浏览器显式保存", func() bool {
		cfg, _ := f.GetConfig(ctx)
		return cfg.Render.Chrome == "C:/Apps/Chrome/chrome.exe"
	})

	// 自检：运行中自动展开日志分组，摘要落到状态行。
	runUI(func() { p.runSelftest("cdp") })
	waitShellCond(t, "自检日志自动展开", func() bool { return p.logSection.Expanded() })
	waitShellCond(t, "自检摘要", func() bool {
		var text string
		runUI(func() { text = p.testInfo.Text })
		return strings.Contains(text, "通过")
	})
}

// TestTrayIcon_BrandIcon 托盘图标使用真实品牌图标（32×32 PNG，可解码）。
func TestTrayIcon_BrandIcon(t *testing.T) {
	res := TrayIcon()
	if res == nil {
		t.Fatal("托盘图标为空")
	}
	img, err := png.Decode(bytes.NewReader(res.Content()))
	if err != nil {
		t.Fatalf("托盘图标不可解码: %v", err)
	}
	if b := img.Bounds(); b.Dx() != 32 || b.Dy() != 32 {
		t.Fatalf("托盘图标尺寸应为 32×32: %v", b)
	}
	if len(ShellIcon().Content()) == 0 {
		t.Fatal("品牌图标资源为空")
	}
}

// heavyGuardBackend 包装 FakeBackend：首页一旦误调 SystemInfo/AppStatus/QuizScanState/
// CourseStatus 就阻塞并计数；用于验证首页只走 Ping + 配置 + 执行记录。
type heavyGuardBackend struct {
	ui.Backend
	release chan struct{}

	systemInfoCalls  atomic.Int32
	appStatusCalls   atomic.Int32
	quizStateCalls   atomic.Int32
	courseStateCalls atomic.Int32
	getConfigCalls   atomic.Int32
	historyCalls     atomic.Int32
	pingCalls        atomic.Int32
}

func (b *heavyGuardBackend) block() {
	if b.release != nil {
		<-b.release
	}
}

func (b *heavyGuardBackend) SystemInfo(context.Context) (ui.SystemInfo, error) {
	b.systemInfoCalls.Add(1)
	b.block()
	return ui.SystemInfo{}, errors.New("首页不应调用 SystemInfo")
}

func (b *heavyGuardBackend) AppStatus(context.Context, string) (map[string]any, error) {
	b.appStatusCalls.Add(1)
	b.block()
	return nil, errors.New("首页不应调用 AppStatus")
}

func (b *heavyGuardBackend) QuizScanState(context.Context) (ui.QuizScanState, error) {
	b.quizStateCalls.Add(1)
	b.block()
	return ui.QuizScanState{}, errors.New("首页不应调用 QuizScanState")
}

func (b *heavyGuardBackend) CourseStatus(context.Context) (ui.CourseStatus, error) {
	b.courseStateCalls.Add(1)
	b.block()
	return ui.CourseStatus{}, errors.New("首页不应调用 CourseStatus")
}

func (b *heavyGuardBackend) GetConfig(ctx context.Context) (ui.Config, error) {
	b.getConfigCalls.Add(1)
	return b.Backend.GetConfig(ctx)
}

func (b *heavyGuardBackend) History(ctx context.Context, appID string) ([]ui.HistoryRecord, error) {
	b.historyCalls.Add(1)
	return b.Backend.History(ctx, appID)
}

func (b *heavyGuardBackend) Ping(ctx context.Context) error {
	b.pingCalls.Add(1)
	return b.Backend.Ping(ctx)
}

func (b *heavyGuardBackend) Reset() {
	b.systemInfoCalls.Store(0)
	b.appStatusCalls.Store(0)
	b.quizStateCalls.Store(0)
	b.courseStateCalls.Store(0)
	b.getConfigCalls.Store(0)
	b.historyCalls.Store(0)
	b.pingCalls.Store(0)
}

// TestHomeReload_LightweightNoHeavyBackend 首页刷新只允许 Ping + 一次配置 + 六次历史；
// SystemInfo/AppStatus/QuizScanState/CourseStatus 被阻塞包装也不能被调用。
func TestHomeReload_LightweightNoHeavyBackend(t *testing.T) {
	f := ui.NewFakeBackend()
	f.StepDelay = time.Millisecond
	ctx := context.Background()
	treeFile := filepath.Join(t.TempDir(), "检修一班树木统计表.xlsx")
	if err := os.WriteFile(treeFile, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	missingFile := filepath.Join(t.TempDir(), "不存在的交跨统计表.xlsx")
	if err := f.SaveConfig(ctx, ui.ConfigPatch{
		Schedule:     &ui.ScheduleConfig{Enabled: true, Time: "09:15", Driver: "os"},
		TreeSync:     &ui.SyncAppConfig{LocalPath: treeFile, FileName: "检修一班树木统计表.xlsx", NightlyTime: "23:55"},
		CrossingSync: &ui.SyncAppConfig{LocalPath: missingFile, FileName: "检修一班交跨统计表.xlsx", NightlyTime: "23:55"},
	}); err != nil {
		t.Fatal(err)
	}

	guard := &heavyGuardBackend{Backend: f, release: make(chan struct{})}
	t.Cleanup(func() { close(guard.release) })
	app := test.NewApp()
	defer app.Quit()
	s := newTestShell(t, app, guard, Options{DisableTray: true, DisableScheduler: true})
	p := s.pages["home"].(*pageHome)

	// 等 NewShell 内部首次首页回填完成，再清零计数，测一次显式 reload 的调用面。
	waitShellCond(t, "首次首页回填", func() bool {
		var text string
		runUI(func() { text = p.cards["daily_sync"].status.Text })
		return text != "正在读取状态…"
	})
	time.Sleep(300 * time.Millisecond) // 等壳层 loadUIConfig 的配置读取收口
	guard.Reset()

	runUI(func() { p.reload() })
	waitShellCond(t, "轻量首页回填", func() bool {
		return guard.historyCalls.Load() >= 6
	})
	time.Sleep(100 * time.Millisecond)

	if n := guard.systemInfoCalls.Load(); n != 0 {
		t.Fatalf("首页不应调用 SystemInfo（会触发 USB PowerShell 探测）: %d", n)
	}
	if n := guard.appStatusCalls.Load(); n != 0 {
		t.Fatalf("首页不应调用 AppStatus: %d", n)
	}
	if n := guard.quizStateCalls.Load(); n != 0 {
		t.Fatalf("首页不应调用 QuizScanState: %d", n)
	}
	if n := guard.courseStateCalls.Load(); n != 0 {
		t.Fatalf("首页不应调用 CourseStatus: %d", n)
	}
	if n := guard.getConfigCalls.Load(); n != 1 {
		t.Fatalf("首页应只读一次配置: %d", n)
	}
	if n := guard.historyCalls.Load(); n != 6 {
		t.Fatalf("首页应只读六个应用的执行记录: %d", n)
	}
	if n := guard.pingCalls.Load(); n == 0 {
		t.Fatal("首页应做 Ping 健康检查")
	}

	var daily, tree, crossing, pcd string
	runUI(func() {
		daily = p.cards["daily_sync"].status.Text
		tree = p.cards["tree_sync"].status.Text
		crossing = p.cards["crossing_sync"].status.Text
		pcd = p.cards["pcd_export"].status.Text
	})
	if !strings.Contains(daily, "尚未执行") || !strings.Contains(daily, "定时 09:15") {
		t.Fatalf("派车状态应用配置定时: %q", daily)
	}
	if !strings.Contains(tree, "内网表已就绪") {
		t.Fatalf("树障状态应用 os.Stat 检查配置路径: %q", tree)
	}
	if !strings.Contains(crossing, "内网表不存在") {
		t.Fatalf("交跨状态应报告配置路径不存在: %q", crossing)
	}
	if !strings.Contains(pcd, "尚无执行记录") {
		t.Fatalf("轨迹导出无历史时应显示尚无执行记录: %q", pcd)
	}
}

// TestBrowserModeText_UpgradeCompat 旧 builtin=true 无运行时：核心检测到宿主时必须显示宿主，
// 不能谎称安装包内置 Electron；只有核心返回 electron 路径才显示内置。
func TestBrowserModeText_UpgradeCompat(t *testing.T) {
	host := "C:/ProgramData/360ent/Application/360ent.exe"
	if got := browserModeText(ui.RenderConfig{Builtin: true}, ui.SystemInfo{Chrome: host}, true); !strings.Contains(got, host) || strings.Contains(got, "内置") {
		t.Fatalf("builtin=true 但检测到宿主时应显示宿主: %q", got)
	}
	if got := browserModeText(ui.RenderConfig{Builtin: true}, ui.SystemInfo{}, true); strings.Contains(got, "内置") {
		t.Fatalf("builtin=true 且未检测到运行时不显示内置 Electron: %q", got)
	}
	if got := browserModeText(ui.RenderConfig{Builtin: true}, ui.SystemInfo{Chrome: "C:/app/browser-runtime/electron.exe"}, true); !strings.Contains(got, "内置") {
		t.Fatalf("核心返回 electron 路径时应显示内置浏览器: %q", got)
	}
	if got := browserModeText(ui.RenderConfig{Builtin: false}, ui.SystemInfo{Chrome: host}, true); !strings.Contains(got, "自动检测宿主") {
		t.Fatalf("builtin=false 应显示自动检测宿主: %q", got)
	}
	if got := browserModeText(ui.RenderConfig{Chrome: host}, ui.SystemInfo{Chrome: "C:/other/chrome.exe"}, true); got != host {
		t.Fatalf("显式 render.chrome 应优先: %q", got)
	}
}

// TestHomeDailyState_ConfigAndHistory 派车卡：定时停用明确、失败且无成功记录时按历史表达。
func TestHomeDailyState_ConfigAndHistory(t *testing.T) {
	disabled := homeDailyState(ui.ScheduleConfig{Enabled: false, Time: "09:15"}, nil)
	if !strings.Contains(disabled.text, "定时已停用") || !strings.Contains(disabled.text, "尚未执行") {
		t.Fatalf("停用定时应明确: %q", disabled.text)
	}
	if disabled.imp != widget.WarningImportance {
		t.Fatalf("停用定时应为警告色: %v", disabled.imp)
	}

	failed := homeDailyState(ui.ScheduleConfig{Enabled: true, Time: "09:15"}, []ui.HistoryRecord{
		{Status: ui.JobFailed, Time: "2026-10-29 09:15:00"},
	})
	if !strings.Contains(failed.text, "尚无成功记录") || !strings.Contains(failed.text, "失败") || !strings.Contains(failed.text, "定时 09:15") {
		t.Fatalf("无成功记录应按历史+配置表达: %q", failed.text)
	}
	if failed.imp != widget.DangerImportance {
		t.Fatalf("最近执行失败应为危险色: %v", failed.imp)
	}
}

// pingFailBackend 只让 Ping 失败，验证首页健康告警仍正常。
type pingFailBackend struct{ ui.Backend }

func (b *pingFailBackend) Ping(context.Context) error { return errors.New("核心未连接") }

func TestHomeReload_PingFailureShowsAlert(t *testing.T) {
	f := ui.NewFakeBackend()
	f.StepDelay = time.Millisecond
	app := test.NewApp()
	defer app.Quit()
	s := newTestShell(t, app, &pingFailBackend{Backend: f}, Options{DisableTray: true, DisableScheduler: true})
	p := s.pages["home"].(*pageHome)
	waitShellCond(t, "首页健康告警", func() bool {
		var visible bool
		runUI(func() { visible = p.alert.Visible() })
		return visible
	})
}
