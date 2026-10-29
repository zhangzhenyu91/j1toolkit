package appshell

import (
	"context"
	"sync"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"github.com/zhangzhenyu91/j1toolkit/uvmp-toolkit/core-go/internal/platform"
	"github.com/zhangzhenyu91/j1toolkit/uvmp-toolkit/core-go/internal/ui"
)

// AppTitle 窗口/托盘标题。
const AppTitle = "Shade 壹匣 - 内网"

// Page 常驻页面：构造一次，切页只 Show/Hide，任务进度/表单状态不丢
// （对应桌面壳 keep-alive 约定，已踩坑：重建组件会重置状态）。
type Page interface {
	Object() fyne.CanvasObject
	OnShow() // 切回页面时刷新数据（对应 onActivated）
}

// Options Shell 可选依赖。
type Options struct {
	AutoStart        *platform.AutoStart // 开机自启管理（nil 则设置页不提供开关）
	DisableTray      bool                // 测试/无托盘环境跳过托盘
	DisableScheduler bool                // 测试跳过 60s 驻留调度
}

// Shell 桌面壳主壳体：导航、8 常驻页面、任务事件泵、关窗驻留/退出确认、托盘与驻留调度。
type Shell struct {
	App     fyne.App
	Win     fyne.Window
	Backend ui.Backend
	Runner  *ui.JobRunner

	opts    Options
	pages   map[string]Page
	navBtns map[string]*widget.Button
	navMeta []navItem
	current string
	content *fyne.Container

	mu          sync.Mutex
	jobCount    int
	forceQuit   bool
	closeToTray bool
	tray        *TrayMenu
	quitOnce    sync.Once

	sched     *platform.Scheduler
	shellSub  ui.Subscription
	cancelSub context.CancelFunc
}

type navItem struct {
	id    string
	title string
	icon  fyne.Resource
}

// NewShell 构建 AppShell（窗口 + 导航 + 8 常驻页面 + 托盘 + 驻留调度）。
// 窗口使用系统标题栏（不自绘无边框壳）。
func NewShell(a fyne.App, backend ui.Backend, opt Options) (*Shell, error) {
	sub, err := backend.Subscribe(context.Background())
	if err != nil {
		return nil, err
	}
	shellSub, err := backend.Subscribe(context.Background())
	if err != nil {
		_ = sub.Close()
		return nil, err
	}

	s := &Shell{
		App:         a,
		Backend:     backend,
		Runner:      ui.NewJobRunner(backend, sub),
		opts:        opt,
		pages:       map[string]Page{},
		navBtns:     map[string]*widget.Button{},
		shellSub:    shellSub,
		closeToTray: true, // 与 electron 默认一致，待配置加载后覆盖
	}
	s.cancelSub = func() {}

	s.Win = a.NewWindow(AppTitle)
	s.Win.Resize(fyne.NewSize(1160, 780))
	// 系统标题栏：使用默认窗口装饰（不 SetDecorations 为无边框）；
	// 最小尺寸经 newMinBox 内容约束派生（fyne.Window 无 SetMinSize）

	s.buildPages()
	s.buildLayout()
	s.bindWindow()

	if !opt.DisableTray {
		s.tray = SetupTray(a, s.Win, s.onTrayExit, s.onTrayStopQuiz)
	}
	if !opt.DisableScheduler {
		s.sched = platform.NewScheduler(s.tick)
		s.sched.Start(context.Background())
	}
	go s.pumpShellEvents()
	go s.pumpQuizEvents()
	go s.loadUIConfig()
	return s, nil
}

func (s *Shell) buildPages() {
	s.navMeta = []navItem{
		{"home", "首页", theme.HomeIcon()},
		{"daily_sync", "派车同步", theme.ViewRefreshIcon()},
		{"tree_sync", "树障同步", theme.DocumentIcon()},
		{"crossing_sync", "交跨同步", theme.FolderIcon()},
		{"pcd_export", "轨迹导出", theme.DownloadIcon()},
		{"quiz_search", "题库搜题", theme.SearchIcon()},
		{"course_autoplay", "学堂刷课", theme.MediaPlayIcon()},
		{"settings", "设置", theme.SettingsIcon()},
	}
	s.pages["home"] = newPageHome(s)
	s.pages["daily_sync"] = newPageDaily(s)
	s.pages["tree_sync"] = newPageSync(s, "tree_sync")
	s.pages["crossing_sync"] = newPageSync(s, "crossing_sync")
	s.pages["pcd_export"] = newPagePcd(s)
	s.pages["quiz_search"] = newPageQuiz(s)
	s.pages["course_autoplay"] = newPageCourse(s)
	s.pages["settings"] = newPageSettings(s)
}

func (s *Shell) buildLayout() {
	brand := widget.NewLabel("壹匣")
	brand.TextStyle = fyne.TextStyle{Bold: true}
	brand.Importance = widget.HighImportance

	nav := container.NewVBox(brand, widget.NewSeparator())
	for _, it := range s.navMeta {
		it := it
		btn := widget.NewButtonWithIcon(it.title, it.icon, func() { s.Navigate(it.id) })
		btn.Importance = widget.LowImportance
		s.navBtns[it.id] = btn
		nav.Add(btn)
	}
	navWrap := container.NewVScroll(container.NewPadded(nav))

	s.content = container.NewStack()
	for _, it := range s.navMeta {
		obj := s.pages[it.id].Object()
		obj.Hide()
		s.content.Add(obj)
	}
	s.Navigate("home")

	page := container.NewPadded(s.content)
	// 窗口最小尺寸由内容最小尺寸派生（fyne.Window 无 SetMinSize，已核对 v2.8 API）
	s.Win.SetContent(container.NewBorder(nil, nil, navWrap, nil,
		newMinBox(fyne.NewSize(840, 600), page)))
}

func (s *Shell) bindWindow() {
	s.Win.SetCloseIntercept(func() {
		s.mu.Lock()
		forced := s.forceQuit
		s.mu.Unlock()
		if forced {
			s.Quit() // 已确认退出（托盘「仍要退出」）：不再驻留
			return
		}
		switch s.closePolicy().OnWindowClose() {
		case platform.ActionHide:
			s.Win.Hide()
			ShowToast(s.Win, "已驻留后台，可在托盘图标重新打开", ToastInfo)
		case platform.ActionConfirmExit:
			s.confirmExit()
		default:
			s.Quit()
		}
	})
}

// Navigate 切页（常驻页面 Show/Hide，不重建）。
func (s *Shell) Navigate(pageID string) {
	runUI(func() {
		p, ok := s.pages[pageID]
		if !ok {
			return
		}
		for id, other := range s.pages {
			if id == pageID {
				other.Object().Show()
			} else {
				other.Object().Hide()
			}
		}
		s.current = pageID
		for id, btn := range s.navBtns {
			if id == pageID {
				btn.Importance = widget.HighImportance
			} else {
				btn.Importance = widget.LowImportance
			}
			btn.Refresh()
		}
		p.OnShow()
	})
}

// StartJob 启动任务并把回调切回 UI 线程；运行计数用于退出确认。
func (s *Shell) StartJob(appID string, params map[string]any, h ui.JobHandler) *ui.JobHandle {
	s.mu.Lock()
	s.jobCount++
	s.mu.Unlock()

	wrap := ui.JobHandler{
		OnLog: func(line string) {
			if h.OnLog != nil {
				runUI(func() { h.OnLog(line) })
			}
		},
		OnProgress: func(p ui.JobProgress) {
			if h.OnProgress != nil {
				runUI(func() { h.OnProgress(p) })
			}
		},
		OnDone: func(job ui.Job) {
			s.jobDone()
			if h.OnDone != nil {
				runUI(func() { h.OnDone(job) })
			}
		},
		OnError: func(err error) {
			s.jobDone()
			if h.OnError != nil {
				runUI(func() { h.OnError(err) })
			}
		},
	}
	return s.Runner.Start(appID, params, wrap)
}

func (s *Shell) jobDone() {
	s.mu.Lock()
	if s.jobCount > 0 {
		s.jobCount--
	}
	s.mu.Unlock()
}

// RunningJobs 进行中任务数。
func (s *Shell) RunningJobs() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.jobCount
}

func (s *Shell) closePolicy() platform.ClosePolicy {
	s.mu.Lock()
	defer s.mu.Unlock()
	return platform.ClosePolicy{
		CloseToTray:   s.closeToTray,
		TrayAvailable: s.tray != nil,
		JobRunning:    s.jobCount > 0,
	}
}

// SetCloseToTray 即时生效关窗驻留策略（设置页保存后调用）。
func (s *Shell) SetCloseToTray(v bool) {
	s.mu.Lock()
	s.closeToTray = v
	s.mu.Unlock()
}

func (s *Shell) onTrayExit() {
	switch s.closePolicy().OnTrayExit() {
	case platform.ActionConfirmExit:
		s.Win.Show()
		ConfirmDanger(s.Win, "任务进行中", s.closePolicy().ExitConfirmMessage(), "仍要退出", s.Quit)
	default:
		s.Quit()
	}
}

// onTrayStopQuiz 托盘「停止搜题」（仅扫描中出现在托盘菜单，见 tray.go）。
func (s *Shell) onTrayStopQuiz() {
	s.Async(func() error {
		if _, err := s.Backend.QuizScanStop(context.Background()); err != nil {
			return err
		}
		ShowToast(s.Win, "已停止搜题", ToastInfo)
		return nil
	})
}

// confirmExit 退出确认（有任务进行中时防误杀长任务）。
func (s *Shell) confirmExit() {
	ConfirmDanger(s.Win, "任务进行中", s.closePolicy().ExitConfirmMessage(), "仍要退出", s.Quit)
}

// Quit 真正退出（托盘确认后 / 关窗不定驻留时）；幂等，底层资源只释放一次。
func (s *Shell) Quit() {
	s.quitOnce.Do(func() {
		s.mu.Lock()
		s.forceQuit = true
		s.mu.Unlock()
		if s.sched != nil {
			s.sched.Stop()
		}
		_ = s.Runner.Close()
		_ = s.shellSub.Close()
		if d, ok := s.Backend.(ui.Disposer); ok {
			d.Dispose() // 释放扫描服务/悬浮窗等底层资源（幂等）
		}
		s.Win.Close()
		s.App.Quit()
	})
}

// Toast 轻提示。
func (s *Shell) Toast(msg string, kind ToastKind) { ShowToast(s.Win, msg, kind) }

// Async 后台执行（错误转轻提示），fn 内更新 UI 需经 runUI（页面常用 RunJob 包装已内建）。
func (s *Shell) Async(fn func() error) {
	go func() {
		if err := fn(); err != nil {
			ShowToast(s.Win, err.Error(), ToastError)
		}
	}()
}

// tick 驻留调度单轮判定（schedule.driver=app 时核心真正执行，due_check 门闸在核心侧）。
func (s *Shell) tick(ctx context.Context, kind platform.TickKind) platform.TickOutcome {
	var started bool
	var jobID string
	var err error
	switch kind {
	case platform.TickDaily:
		var r ui.DailyTickResult
		r, err = s.Backend.MaybeRunDaily(ctx)
		started, jobID = r.Started, r.JobID
		if r.TreeSyncNightly != nil && r.TreeSyncNightly.Started {
			started = true
		}
	case platform.TickTreeSync:
		var r ui.TickResult
		r, err = s.Backend.MaybeRunTreeSyncTick(ctx)
		started, jobID = r.Started, r.JobID
	case platform.TickCrossingSync:
		var r ui.TickResult
		r, err = s.Backend.MaybeRunCrossingSyncTick(ctx)
		started, jobID = r.Started, r.JobID
	}
	if started {
		ShowToast(s.Win, "驻留调度：到点执行（"+kind.String()+"）", ToastInfo)
	}
	return platform.TickOutcome{Kind: kind, Started: started, JobID: jobID, Err: err}
}

// pumpShellEvents 壳级事件泵：core.exit 等全局事件经 runUI 切 UI 线程。
func (s *Shell) pumpShellEvents() {
	for ev := range s.shellSub.Events() {
		if ev.Kind == ui.EventCoreExit {
			runUI(func() {
				ShowToast(s.Win, "核心进程已退出", ToastError)
			})
		}
	}
}

// pumpQuizEvents 扫描页事件泵：扫描态联动托盘菜单「停止搜题」，
// 开搜即隐藏主窗（对齐 electron/main.cjs quizsearch「开搜即最小化主窗」）。
func (s *Shell) pumpQuizEvents() {
	sub, err := s.Backend.QuizSubscribe(context.Background())
	if err != nil {
		return
	}
	defer sub.Close()
	for ev := range sub.Events() {
		if ev.Type != ui.QuizEventScan {
			continue
		}
		s.tray.SetQuizScanning(ev.Scanning)
		if ev.Scanning {
			runUI(func() { s.Win.Hide() })
		}
	}
}

// loadUIConfig 启动后读一次壳层配置：关窗驻留开关即时生效；
// 开机自启按配置重放一次（对齐 electron/main.cjs loadUiConfig→applyAutostart，
// 每次启动与配置对齐，换路径安装后自启指向仍正确）。
func (s *Shell) loadUIConfig() {
	cfg, err := s.Backend.GetConfig(context.Background())
	if err != nil {
		return
	}
	s.SetCloseToTray(cfg.UI.CloseToTray)
	if a := s.opts.AutoStart; a != nil {
		var aerr error
		if cfg.UI.Autostart {
			aerr = a.Enable()
		} else {
			aerr = a.Disable()
		}
		if aerr != nil {
			ShowToast(s.Win, "开机自启状态对齐失败："+aerr.Error(), ToastWarning)
		}
	}
}
