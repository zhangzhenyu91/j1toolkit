package appshell

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/test"

	"github.com/zhangzhenyu91/j1toolkit/uvmp-toolkit/core-go/internal/platform"
	"github.com/zhangzhenyu91/j1toolkit/uvmp-toolkit/core-go/internal/ui"
)

// 页面交互 parity 冒烟（FakeBackend/CoreBackend，对齐现有九页与 desktop/src/main.cjs 契约）：
// 导出参数、设置持久化、课程状态/暂停、托盘菜单/关窗、搜题事件、历史打开目录。

// dispFake 记录 Dispose 调用次数（Shell 退出释放链断言）。
type dispFake struct {
	ui.Backend
	disposed int
}

func (d *dispFake) Dispose() { d.disposed++ }

// waitFakeCond 限时轮询条件（读 FakeBackend 状态用，不用 UI 控件）。
func waitFakeCond(t *testing.T, desc string, cond func() bool) {
	t.Helper()
	if !waitForShell(3*time.Second, cond) {
		t.Fatalf("timeout waiting for %s", desc)
	}
}

// TestPcdExport_ParamsAndCleanup 轨迹导出：批次名去 .xlsx 后缀 / outdir 去空格 /
// 完成后清空单号与日志（结果落入执行记录）。
func TestPcdExport_ParamsAndCleanup(t *testing.T) {
	f := ui.NewFakeBackend()
	f.StepDelay = time.Millisecond
	ctx := context.Background()
	app := test.NewApp()
	defer app.Quit()
	s := newTestShell(t, app, f, Options{DisableTray: true, DisableScheduler: true})
	p := s.pages["pcd_export"].(*pagePcd)
	s.Navigate("pcd_export")
	time.Sleep(50 * time.Millisecond)

	// xlsx 导入：批次名去 .xlsx 后缀，展示名保留文件名
	runUI(func() { p.loadXlsx("D:/exports/2026-10-28-派车单.xlsx") })
	waitShellCond(t, "xlsx 预览回填", func() bool {
		var n int
		runUI(func() { n = len(p.codes) })
		return n == 3
	})
	var batch, shown string
	runUI(func() { batch, shown = p.batchName, p.fileName.Text })
	if batch != "2026-10-28-派车单" {
		t.Fatalf("批次名应去 .xlsx 后缀: %q", batch)
	}
	if shown != "2026-10-28-派车单.xlsx" {
		t.Fatalf("展示名应保留文件名: %q", shown)
	}

	// 粘贴解析：批次名固定「粘贴文本」
	runUI(func() {
		p.codesText.SetText(" PC001, PC002 ")
		p.parseText()
	})
	waitShellCond(t, "粘贴预览回填", func() bool {
		var n int
		runUI(func() { n = len(p.codes) })
		return n == 2
	})
	runUI(func() { batch = p.batchName })
	if batch != "粘贴文本" {
		t.Fatalf("粘贴批次名应为「粘贴文本」: %q", batch)
	}

	// 凭据预检过后执行：outdir 去空格进参数；完成后清台（单号/日志）
	if err := f.SaveConfig(ctx, ui.ConfigPatch{Dev: &ui.DevConfig{Mock: true}}); err != nil {
		t.Fatal(err)
	}
	runUI(func() {
		p.outdir.SetText("  mem://out-batch  ")
		p.startRun()
	})
	waitShellCond(t, "任务完成清台", func() bool {
		var n int
		runUI(func() { n = len(p.codes) })
		return n == 0
	})
	recs, err := f.History(ctx, "pcd_export")
	if err != nil || len(recs) != 1 {
		t.Fatalf("应有 1 条执行记录: recs=%d err=%v", len(recs), err)
	}
	job, err := f.GetJob(ctx, recs[0].JobID)
	if err != nil {
		t.Fatalf("GetJob: %v", err)
	}
	if job.Result["batch_name"] != "粘贴文本" {
		t.Fatalf("任务 batch_name 不符: %v", job.Result["batch_name"])
	}
	if job.Result["outdir"] != "mem://out-batch" {
		t.Fatalf("任务 outdir 未去空格: %v", job.Result["outdir"])
	}
	var logLines int
	var startDisabled bool
	runUI(func() {
		logLines = len(p.log.Lines())
		startDisabled = p.startBtn.Disabled()
	})
	if logLines != 0 {
		t.Fatalf("完成后日志应清空: %d 行", logLines)
	}
	if !startDisabled {
		t.Fatal("清台后无单号应禁用「开始导出」")
	}
}

// TestDailySync_OpenDirAndDriverPreserve 派车同步：执行日期去空格 / 完成提示「打开目录」/
// 定时保存不改写执行模式（driver）。
func TestDailySync_OpenDirAndDriverPreserve(t *testing.T) {
	f := ui.NewFakeBackend()
	f.StepDelay = time.Millisecond
	ctx := context.Background()
	app := test.NewApp()
	defer app.Quit()
	s := newTestShell(t, app, f, Options{DisableTray: true, DisableScheduler: true})
	p := s.pages["daily_sync"].(*pageDaily)
	s.Navigate("daily_sync")
	time.Sleep(80 * time.Millisecond)

	// 配置 driver=app：定时保存只更新 enabled/time，不改 driver
	if err := f.SaveConfig(ctx, ui.ConfigPatch{Schedule: &ui.ScheduleConfig{Enabled: true, Time: "09:15", Driver: "app"}}); err != nil {
		t.Fatal(err)
	}
	runUI(func() {
		p.schTime.SetText("08:30")
		p.schEnable.SetChecked(true)
		p.saveSchedule()
	})
	waitFakeCond(t, "定时设置保存", func() bool {
		cfg, _ := f.GetConfig(ctx)
		return cfg.Schedule.Time == "08:30"
	})
	cfg, _ := f.GetConfig(ctx)
	if cfg.Schedule.Driver != "app" {
		t.Fatalf("saveSchedule 不应改写 driver: %q", cfg.Schedule.Driver)
	}

	// 立即执行：date 去空格 → 历史结果文件名按修剪后日期（dev.mock 过凭据预检）
	if err := f.SaveConfig(ctx, ui.ConfigPatch{Dev: &ui.DevConfig{Mock: true}}); err != nil {
		t.Fatal(err)
	}
	runUI(func() {
		p.runDate.SetText(" 2026-10-28 ")
		p.startRun()
	})
	waitShellCond(t, "完成提示带打开目录", func() bool {
		var vis bool
		runUI(func() { vis = p.openDirBtn.Visible() })
		return vis
	})
	recs, err := f.History(ctx, "daily_sync")
	if err != nil || len(recs) != 1 {
		t.Fatalf("应有 1 条执行记录: recs=%d err=%v", len(recs), err)
	}
	job, err := f.GetJob(ctx, recs[0].JobID)
	if err != nil {
		t.Fatalf("GetJob: %v", err)
	}
	if job.Result["file"] != "2026-10-28-派车单.xlsx" {
		t.Fatalf("date 未去空格或参数丢失: %v", job.Result["file"])
	}
	runUI(func() { p.openDirBtn.OnTapped() })
	waitFakeCond(t, "打开产物目录", func() bool {
		for _, opened := range f.Opened() {
			if opened == "open:mem://usb" {
				return true
			}
		}
		return false
	})
}

// TestSettings_PersistAndSelftestPaths 设置页：SSO 去空格且空密码不修改 /
// 并发钳制 / 开机自启落盘 / 自检产物（png/pdf）回显。
func TestSettings_PersistAndSelftestPaths(t *testing.T) {
	f := ui.NewFakeBackend()
	f.StepDelay = time.Millisecond
	ctx := context.Background()
	app := test.NewApp()
	defer app.Quit()
	as := &platform.AutoStart{GOOS: "linux", Home: t.TempDir(), Name: "Shade 壹匣 - 内网", ExecPath: "/opt/uvmp-toolkit"}
	s := newTestShell(t, app, f, Options{DisableTray: true, DisableScheduler: true, AutoStart: as})
	p := s.pages["settings"].(*pageSettings)
	s.Navigate("settings")
	time.Sleep(150 * time.Millisecond) // 等 loadUIConfig/OnShow 回填完成再改控件

	// SSO：账号去空格；空密码=不修改
	if err := f.SaveConfig(ctx, ui.ConfigPatch{SSO: &ui.SSOConfig{Username: "old", Password: "secret"}}); err != nil {
		t.Fatal(err)
	}
	runUI(func() {
		p.ssoUser.SetText("  zhang  ")
		p.ssoPass.SetText("")
		p.saveSSO()
	})
	waitFakeCond(t, "SSO 保存", func() bool {
		cfg, _ := f.GetConfig(ctx)
		return cfg.SSO.Username == "zhang"
	})
	cfg, _ := f.GetConfig(ctx)
	if !cfg.SSO.HasPassword {
		t.Fatal("空密码不应清除已配置密码")
	}

	// 并发：查询 99→8，渲染 0→默认 2
	runUI(func() {
		p.wQuery.SetText("99")
		p.wRender.SetText("0")
		p.saveWorkers()
	})
	waitFakeCond(t, "并发钳制保存", func() bool {
		cfg, _ := f.GetConfig(ctx)
		return cfg.OrderExport.QueryWorkers == 8
	})
	cfg, _ = f.GetConfig(ctx)
	if cfg.OrderExport.RenderWorkers != 2 {
		t.Fatalf("渲染并发钳制不符: %d", cfg.OrderExport.RenderWorkers)
	}

	// 后台运行：开机自启立即落盘（linux .desktop），driver=app 持久
	runUI(func() {
		p.autoStart.SetChecked(true)
		p.closeTray.SetChecked(true)
		p.driverSel.SetSelected(driverApp)
		p.saveUI()
	})
	deskPath, err := as.DesktopFilePath()
	if err != nil {
		t.Fatal(err)
	}
	waitFakeCond(t, "开机自启落盘", func() bool {
		_, serr := os.Stat(deskPath)
		return serr == nil
	})
	cfg, _ = f.GetConfig(ctx)
	if !cfg.UI.Autostart || cfg.Schedule.Driver != "app" {
		t.Fatalf("后台运行设置未持久: ui=%+v driver=%s", cfg.UI, cfg.Schedule.Driver)
	}

	// 环境自检：cdp 产物 png/pdf 以「打开」行回显
	runUI(func() { p.runSelftest("cdp") })
	waitShellCond(t, "自检产物回显", func() bool {
		var n int
		runUI(func() { n = len(p.testPaths.Objects) })
		return n == 2
	})
}

// TestCourse_StatusPauseAndParams 学堂刷课：未连接禁用开始 + 提示 /
// 打开浏览器保存 start_url / 运行中禁用打开浏览器 / 暂停走 paused 语义 / 执行参数下发。
func TestCourse_StatusPauseAndParams(t *testing.T) {
	f := ui.NewFakeBackend()
	f.StepDelay = 200 * time.Millisecond // 保证可中途暂停
	ctx := context.Background()
	app := test.NewApp()
	defer app.Quit()
	s := newTestShell(t, app, f, Options{DisableTray: true, DisableScheduler: true})
	p := s.pages["course_autoplay"].(*pageCourse)
	s.Navigate("course_autoplay")
	time.Sleep(80 * time.Millisecond)

	// 未连接受控浏览器：不可开始，且有「请先打开受控浏览器」提示
	var startDisabled, hintShown bool
	runUI(func() {
		startDisabled = p.startBtn.Disabled()
		hintShown = p.startHint.Visible()
	})
	if !startDisabled || !hintShown {
		t.Fatalf("未连接时应禁用开始并提示先开浏览器: disabled=%v hint=%v", startDisabled, hintShown)
	}

	// 打开浏览器：保存启动网址并连上
	runUI(func() {
		p.startURL.SetText("https://edu.example/")
		p.openBrowser()
	})
	waitFakeCond(t, "start_url 保存", func() bool {
		cfg, _ := f.GetConfig(ctx)
		return cfg.BrowserAutomation.StartURL == "https://edu.example/"
	})
	waitShellCond(t, "连接后可开始", func() bool {
		var d bool
		runUI(func() { d = p.startBtn.Disabled() })
		return !d
	})

	// 开始学习：参数下发，运行中「打开浏览器」禁用
	runUI(func() {
		p.scanOnly.SetChecked(true)
		p.maxTry.SetText("5")
		p.timeout.SetText("60")
		p.startRun()
	})
	waitShellCond(t, "进入运行态", func() bool {
		var running, openDisabled bool
		runUI(func() {
			running = p.running
			openDisabled = p.openBtn.Disabled()
		})
		return running && openDisabled
	})

	// 暂停：result.paused（非失败，可续刷）
	runUI(func() { p.pause() })
	waitShellCond(t, "暂停收口", func() bool {
		var text string
		runUI(func() { text = p.doneInfo.Text })
		return strings.Contains(text, "已暂停")
	})
	recs, err := f.History(ctx, "course_autoplay")
	if err != nil || len(recs) != 1 {
		t.Fatalf("应有 1 条执行记录: recs=%d err=%v", len(recs), err)
	}
	job, err := f.GetJob(ctx, recs[0].JobID)
	if err != nil {
		t.Fatalf("GetJob: %v", err)
	}
	if !job.Paused() {
		t.Fatalf("暂停结果应为 result.paused: %+v", job.Result)
	}

	// 再跑一轮到完成：执行参数按 UI 下发（scan_only/max_attempts/timeout）
	f.StepDelay = time.Millisecond
	runUI(func() { p.startRun() })
	waitShellCond(t, "二次运行完成", func() bool {
		var text string
		runUI(func() { text = p.doneInfo.Text })
		return strings.Contains(text, "扫描完成")
	})
	recs, err = f.History(ctx, "course_autoplay")
	if err != nil || len(recs) != 2 {
		t.Fatalf("应有 2 条执行记录: recs=%d err=%v", len(recs), err)
	}
	job, err = f.GetJob(ctx, recs[0].JobID)
	if err != nil {
		t.Fatalf("GetJob: %v", err)
	}
	if job.Result["scan_only"] != true || job.Result["max_attempts"] != 5 || job.Result["timeout"] != 60 {
		t.Fatalf("执行参数未按 UI 下发: %+v", job.Result)
	}
}

// TestShell_QuitDisposesBackend Shell 退出释放底层资源且幂等。
func TestShell_QuitDisposesBackend(t *testing.T) {
	d := &dispFake{Backend: ui.NewFakeBackend()}
	app := test.NewApp()
	defer app.Quit()
	s := newTestShell(t, app, d, Options{DisableTray: true, DisableScheduler: true})
	s.Quit()
	s.Quit()
	if d.disposed != 1 {
		t.Fatalf("Dispose 应恰好一次: %d", d.disposed)
	}
}

// TestShell_JobCountOnStartError 启动失败的任务计数归零（退出确认不误报）。
func TestShell_JobCountOnStartError(t *testing.T) {
	f := ui.NewFakeBackend()
	f.FailNextRun = errors.New("启动失败")
	app := test.NewApp()
	defer app.Quit()
	s := newTestShell(t, app, f, Options{DisableTray: true, DisableScheduler: true})
	errCh := make(chan error, 1)
	s.StartJob("daily_sync", map[string]any{}, ui.JobHandler{OnError: func(e error) { errCh <- e }})
	select {
	case e := <-errCh:
		if e == nil {
			t.Fatal("应收到启动失败")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("未收到 OnError")
	}
	if n := s.RunningJobs(); n != 0 {
		t.Fatalf("启动失败后计数应归零: %d", n)
	}
}

// TestShell_LoadUIConfigAppliesAutostart 启动时按配置重放开机自启（对齐 main.cjs）。
func TestShell_LoadUIConfigAppliesAutostart(t *testing.T) {
	f := ui.NewFakeBackend()
	ctx := context.Background()
	app := test.NewApp()
	defer app.Quit()
	as := &platform.AutoStart{GOOS: "linux", Home: t.TempDir(), Name: "Shade 壹匣 - 内网", ExecPath: "/opt/uvmp-toolkit"}
	s := newTestShell(t, app, f, Options{DisableTray: true, DisableScheduler: true, AutoStart: as})
	time.Sleep(120 * time.Millisecond) // 让 NewShell 自带的启动重放先完成
	deskPath, err := as.DesktopFilePath()
	if err != nil {
		t.Fatal(err)
	}

	if err := f.SaveConfig(ctx, ui.ConfigPatch{UI: &ui.UIConfig{CloseToTray: true, Autostart: true}}); err != nil {
		t.Fatal(err)
	}
	s.loadUIConfig()
	if _, err := os.Stat(deskPath); err != nil {
		t.Fatalf("启动未按配置重放开机自启: %v", err)
	}

	if err := f.SaveConfig(ctx, ui.ConfigPatch{UI: &ui.UIConfig{CloseToTray: true, Autostart: false}}); err != nil {
		t.Fatal(err)
	}
	s.loadUIConfig()
	if _, err := os.Stat(deskPath); err == nil {
		t.Fatal("关闭自启后启动重放应移除自启项")
	}
}

// TestTrayMenu_QuizStopItem 托盘菜单：扫描中追加「停止搜题」（对齐 main.cjs refreshTrayMenu）。
func TestTrayMenu_QuizStopItem(t *testing.T) {
	nope := func() {}
	scanning := buildTrayMenu(true, nope, nope, nope)
	if !menuHas(scanning, "停止搜题") {
		t.Fatal("扫描中托盘应有「停止搜题」")
	}
	if !menuHas(scanning, "显示主窗口") || !menuHas(scanning, "退出") {
		t.Fatal("托盘基础项缺失")
	}
	stopped := buildTrayMenu(false, nope, nope, nope)
	if menuHas(stopped, "停止搜题") {
		t.Fatal("未扫描不应有「停止搜题」")
	}
}

func menuHas(m *fyne.Menu, label string) bool {
	for _, it := range m.Items {
		if it.Label == label {
			return true
		}
	}
	return false
}

// TestQuizScan_NoBanksDisabledAndEvents 搜题页：无启用题库禁用开搜；启停事件回流状态区。
func TestQuizScan_NoBanksDisabledAndEvents(t *testing.T) {
	f := ui.NewFakeBackend()
	f.StepDelay = 0
	ctx := context.Background()
	app := test.NewApp()
	defer app.Quit()
	s := newTestShell(t, app, f, Options{DisableTray: true, DisableScheduler: true})
	pq := s.pages["quiz_search"].(*pageQuiz)
	s.Navigate("quiz_search")
	waitShellCond(t, "无题库禁用开搜", func() bool {
		var d bool
		runUI(func() { d = pq.scanBtn.Disabled() })
		return d
	})

	if _, err := f.QuizImportBank(ctx, "安规.xlsx"); err != nil {
		t.Fatal(err)
	}
	runUI(func() { pq.reloadBanks() })
	waitShellCond(t, "有题库放行开搜", func() bool {
		var d bool
		runUI(func() { d = pq.scanBtn.Disabled() })
		return !d
	})

	runUI(func() { pq.toggleScan() })
	waitShellCond(t, "进入扫描态", func() bool {
		st, _ := f.QuizScanState(ctx)
		return st.Scanning
	})
	waitShellCond(t, "扫描态文案", func() bool {
		var text string
		runUI(func() { text = pq.scanStatus.Text })
		return strings.Contains(text, "搜题中")
	})

	runUI(func() { pq.toggleScan() })
	waitShellCond(t, "退出扫描态", func() bool {
		st, _ := f.QuizScanState(ctx)
		return !st.Scanning
	})
}

// TestShell_SchedulerTickStartsJob 驻留调度 tick 到点执行（托盘驻留调度链路）。
func TestShell_SchedulerTickStartsJob(t *testing.T) {
	f := ui.NewFakeBackend()
	f.StepDelay = time.Millisecond
	f.DueDaily = true
	ctx := context.Background()
	app := test.NewApp()
	defer app.Quit()
	s := newTestShell(t, app, f, Options{DisableTray: true, DisableScheduler: true})
	out := s.tick(ctx, platform.TickDaily)
	if !out.Started || out.JobID == "" {
		t.Fatalf("到点应启动任务: %+v", out)
	}
	waitFakeCond(t, "调度任务执行完并入历史", func() bool {
		recs, _ := f.History(ctx, "daily_sync")
		return len(recs) == 1
	})
}
