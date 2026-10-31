package appshell

import (
	"context"
	"sync"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
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

type windowModePage interface {
	SetMaximized(bool)
}

// Options Shell 可选依赖。
type Options struct {
	AutoStart        *platform.AutoStart // 开机自启管理（nil 则设置页不提供开关）
	DisableTray      bool                // 测试/无托盘环境跳过托盘
	DisableScheduler bool                // 测试跳过 60s 驻留调度
}

// Shell 桌面壳主壳体：导航、9 常驻页面、任务事件泵、关窗驻留/退出确认、托盘与驻留调度。
type Shell struct {
	App     fyne.App
	Win     fyne.Window
	Backend ui.Backend
	Runner  *ui.JobRunner

	opts                Options
	pages               map[string]Page
	navEntries          map[string]*navEntry
	navMeta             []navItem
	header              *pageHeader
	titleBar            *windowTitleBar
	windowOps           nativeWindowOps
	current             string
	navQuery            string
	advancedNavExpanded bool
	content             *fyne.Container
	contentLayout       *maxWidthLayout
	contentColumn       *fyne.Container
	stage               *pageStage

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
	desc  string // 页头一句话说明（面向非开发者的任务描述）
	icon  string // 线性图标名（icons.go）
}

// NewShell 构建 AppShell（一体式页首 + 导航 + 9 常驻页面 + 托盘 + 驻留调度）。
// 官方无边框窗口配合系统窗口消息：不引入 Web 壳，不绕过关闭拦截。
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
		navEntries:  map[string]*navEntry{},
		shellSub:    shellSub,
		closeToTray: true, // 与 electron 默认一致，待配置加载后覆盖
	}
	s.cancelSub = func() {}

	s.Win = newShellWindow(a)
	s.windowOps = newNativeWindowOps(s.Win)
	s.Win.Resize(fyne.NewSize(1160, 780))
	// 最小尺寸由一体式页首与工作区的 MinSize 共同派生。

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
		{"home", "首页", "三项主应用与常用入口", "home"},
		{"history", "执行历史", "跨工具筛选记录、回看结果并回填参数", "history"},
	}
	s.navMeta = append(s.navMeta, toolNavItems()...)
	s.navMeta = append(s.navMeta, navItem{"settings", "设置", "账号、宿主浏览器、导出与后台、快捷键与开发诊断", "settings"})
	s.pages["home"] = newPageHome(s)
	s.pages["history"] = newPageHistory(s)
	s.pages["daily_sync"] = newPageDaily(s)
	s.pages["tree_sync"] = newPageSync(s, "tree_sync")
	s.pages["crossing_sync"] = newPageSync(s, "crossing_sync")
	s.pages["pcd_export"] = newPagePcd(s)
	s.pages["quiz_search"] = newPageQuiz(s)
	s.pages["course_autoplay"] = newPageCourse(s)
	s.pages["settings"] = newPageSettings(s)
}

// buildLayout 装配一体式页首 + 图标文字导航（设置沉底）+ 页头 + 限宽内容区。
// 页面对象仍为常驻 Show/Hide，不重建。
func (s *Shell) buildLayout() {
	s.header = newPageHeader()
	s.header.favorite.SetOnTapped(func() { s.toggleFavorite(s.current) })

	s.content = container.NewStack()
	for _, it := range s.navMeta {
		obj := s.pages[it.id].Object()
		obj.Hide()
		s.content.Add(obj)
	}
	// 页头固定在内容区顶部，仅页面主体滚动；所有常驻页共用这一层页面留白。
	s.stage = newPageStage(s.content)
	body := container.NewBorder(s.header.Root, nil, nil, nil, padAll(pageTopGap, cardGap, columnGap, columnGap, s.stage))
	s.contentLayout = &maxWidthLayout{max: contentMaxW}
	s.contentColumn = container.New(s.contentLayout, body)

	// 先建侧栏再切首页：否则导航项尚未创建，首页初始激活态不会点亮。
	sideBar := s.buildSideBar()
	s.Navigate("home")

	s.titleBar = newWindowTitleBar(s.windowOps, func() { s.Win.Close() })
	s.titleBar.onMaximizeChanged = s.setWindowMaximized
	if window, ok := s.Win.(interface{ setNativeStateCallback(func()) }); ok {
		window.setNativeStateCallback(s.titleBar.syncMaximize)
	}
	s.titleBar.syncMaximize()
	workspace := container.NewBorder(nil, nil, sideBar, nil, s.contentColumn)
	root := container.New(windowBodyLayout{top: s.titleBar}, workspace, s.titleBar)
	// 900×600 包含顶栏和一圈细边框，不使用额外画布或边缘留白。
	s.Win.SetContent(newAppTooltipHost(newWindowFrame(s.windowOps,
		newMinBox(fyne.NewSize(898, 598), root))))
	s.bindShortcuts()
}

// buildSideBar 左侧导航轨：工具搜索、分组导航与设置沉底，不增加项目树或多标签。
func (s *Shell) buildSideBar() fyne.CanvasObject {
	search := widget.NewEntry()
	search.SetPlaceHolder("搜索工具")
	nav := container.NewVBox()
	s.rebuildNavEntries(nav, "")
	search.OnChanged = func(query string) { s.rebuildNavEntries(nav, query) }

	navScroll := container.NewVScroll(padAll(0, 6, navOuterX, navOuterX, nav))
	navScroll.SetMinSize(fyne.NewSize(0, 280))
	searchBox := padAll(pageTopGap, 12, navOuterX, navOuterX, search)

	settings := newNavEntry("设置", "settings", func() { s.Navigate("settings") })
	s.navEntries["settings"] = settings
	bottom := container.NewVBox(widget.NewSeparator(), padAll(8, 8, navOuterX, navOuterX, settings))

	inner := container.NewBorder(searchBox, bottom, nil, nil, navScroll)
	return sidebarSurface(container.New(fixedWidthLayout{width: sideBarWidth}, inner))
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

// Navigate 切页（常驻页面 Show/Hide，不重建），同步导航高亮与页头。
func (s *Shell) Navigate(pageID string) {
	runUI(func() {
		p, ok := s.pages[pageID]
		if !ok {
			return
		}
		clearAppTooltipForWindow(s.Win)
		for id, other := range s.pages {
			if id == pageID {
				other.Object().Show()
			} else {
				other.Object().Hide()
			}
		}
		s.current = pageID
		if shellIsBusinessTool(pageID) {
			s.recordRecentTool(pageID)
		}
		for id, entry := range s.navEntries {
			entry.SetActive(id == pageID)
		}
		if s.header != nil {
			if meta, ok := s.navByID(pageID); ok {
				s.header.Set(meta.title, meta.desc)
				s.header.SetFavorite(pageID, s.isFavorite(pageID))
			}
		}
		if s.stage != nil {
			s.stage.Play()
		}
		p.OnShow()
	})
}

// navByID 按页面 id 取导航元信息（页头文案）。
func (s *Shell) navByID(pageID string) (navItem, bool) {
	for _, it := range s.navMeta {
		if it.id == pageID {
			return it, true
		}
	}
	return navItem{}, false
}

// StartJob 启动任务并把回调切回 UI 线程；运行计数用于退出确认与页头状态。
func (s *Shell) StartJob(appID string, params map[string]any, h ui.JobHandler) *ui.JobHandle {
	// 立即固定参数副本：页面在任务成功后可能清空输入，不能到终态再读原 map。
	snapshot, snapshotErr := shellDeepCopyJobParams(params)

	s.mu.Lock()
	s.jobCount++
	running := s.jobCount
	s.mu.Unlock()
	s.showRunning(running)

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
			runUI(func() {
				if snapshotErr == nil {
					s.saveJobParams(job.ID, appID, snapshot)
				}
				if h.OnDone != nil {
					h.OnDone(job)
				}
			})
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
	running := s.jobCount
	s.mu.Unlock()
	s.showRunning(running)
}

// showRunning 页头显示运行中任务数（业务状态优先；0 时隐藏）。
func (s *Shell) showRunning(n int) {
	if s.header == nil {
		return
	}
	runUI(func() { s.header.SetRunning(n) })
}

// ToggleMaximize 切换工作区最大化与还原，供窗口按钮和原生窗口冒烟共用。
func (s *Shell) ToggleMaximize() { s.windowOps.ToggleMaximize() }

// WindowMaximized 返回原生窗口的真实最大化状态。
func (s *Shell) WindowMaximized() bool { return s.windowOps.Maximized() }

func (s *Shell) setWindowMaximized(maximized bool) {
	if s.contentLayout != nil {
		s.contentLayout.max = contentMaxW
		if maximized {
			s.contentLayout.max = contentMaximizedW
		}
		if s.contentColumn != nil {
			s.contentColumn.Refresh()
		}
	}
	for _, page := range s.pages {
		if modePage, ok := page.(windowModePage); ok {
			modePage.SetMaximized(maximized)
		}
	}
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
// 测试/无托盘环境（Options.DisableTray）下 tray 为 nil，必须显式跳过托盘刷新，
// 否则扫描事件一到就会解引用 nil 托盘句柄（已踩坑）。
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
		if s.tray != nil {
			s.tray.SetQuizScanning(ev.Scanning)
		}
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
