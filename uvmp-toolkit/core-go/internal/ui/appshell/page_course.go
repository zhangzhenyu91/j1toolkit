package appshell

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"github.com/zhangzhenyu91/j1toolkit/uvmp-toolkit/core-go/internal/ui"
)

// pageCourse 学堂刷课（course_autoplay）：课程浏览器 / 学习选项 / 开始学习 / 学习进度 / 执行记录。
// 主状态只展示业务语言，连接参数与高级设置分别收进默认折叠区。
type pageCourse struct {
	shell *Shell
	root  fyne.CanvasObject

	statusKV     *kvCard
	techStatusKV *kvCard
	techSec      *collapsibleSection
	advancedSec  *collapsibleSection
	startURL     *widget.Entry
	openBtn      *appButton
	scanOnly     *enterpriseCheck
	maxTry       *widget.Entry
	timeout      *widget.Entry
	startBtn     *appButton
	pauseBtn     *appButton
	pauseHint    *widget.Label
	startHint    *widget.Label
	stage        *widget.Label
	running      bool
	progress     *progressRow
	result       *taskResult
	doneInfo     *widget.Label // 结果摘要（历史用例/外部断言共用）
	ckList       *fyne.Container
	history      *HistoryList

	currentJob *ui.JobHandle
	scanMode   bool // 启动时的模式（完成提醒以此为准）

	layout          *pageSplitLayout
	cardBox         *fyne.Container
	statusBox       fyne.CanvasObject
	statusNote      *widget.Label
	compactStatus   *statusLine
	checkpointsCard fyne.CanvasObject
	historyScope    *appTooltipScope
	techCard        fyne.CanvasObject
	startCard       fyne.CanvasObject
	maximized       bool
}

func newPageCourse(s *Shell) *pageCourse {
	p := &pageCourse{shell: s}

	p.statusKV = newKVCard("课程浏览器状态",
		[2]string{"浏览器状态", "尚未就绪"},
		[2]string{"学习状态", "请先打开课程浏览器"},
		[2]string{"已打开页面", "-"},
		[2]string{"可继续学习", "-"},
	)
	p.techStatusKV = newKVCard("技术状态",
		[2]string{"连接状态", "-"},
		[2]string{"连接地址", "-"},
		[2]string{"浏览器", "-"},
		[2]string{"自动操作", "-"},
	)
	p.techSec = newCollapsibleSection("技术详情（供排错）", p.techStatusKV.box, false)
	p.techCard = p.techSec.Root
	p.techCard.Hide()
	p.startURL = widget.NewEntry()
	p.startURL.SetPlaceHolder("输入要打开的课程页面网址")
	p.openBtn = newAppButton("打开课程浏览器", "globe", widget.LowImportance, p.openBrowser)
	// 常规只保留一行紧凑就绪状态；完整浏览器键值状态最大化再展开。
	p.compactStatus = newStatusLine("浏览器未就绪 · 请先打开课程浏览器", widget.WarningImportance, theme.SizeNameCaptionText)
	p.statusBox = p.statusKV.box
	p.statusBox.Hide()
	// 登录学堂、进入课程设置列表、保持前台是开始学习的前提，常规也始终可见。
	p.statusNote = muted("打开后请登录学堂，进入课程设置列表，并保持课程浏览器在前台。")
	card1 := newCard("课程浏览器",
		p.compactStatus.Root,
		p.statusBox,
		formRow("课程网址", p.startURL, p.openBtn),
		p.statusNote,
	)

	p.scanOnly = newEnterpriseCheck("只查看课程清单，不进入课程", nil)
	p.maxTry = widget.NewEntry()
	p.maxTry.SetText("3")
	p.timeout = widget.NewEntry()
	p.timeout.SetText("120")
	card2 := newCard("学习选项", p.scanOnly)
	advancedBody := container.NewVBox(
		hrow(taskFieldLabel("每门课程最多重试"), newMinBox(fyne.NewSize(70, 0), p.maxTry), muted("次")),
		hrow(taskFieldLabel("页面无响应等待时间"), newMinBox(fyne.NewSize(80, 0), p.timeout), muted("秒")),
	)
	p.advancedSec = newCollapsibleSection("高级设置（异常时调整）", advancedBody, false)

	p.startBtn = newAppButton("开始学习", "play", widget.HighImportance, p.startRun)
	p.startBtn.Disable() // 浏览器就绪前不可用（状态刷新后按连接/运行态放行）
	p.pauseBtn = newAppButton("暂停学习", "pause", widget.LowImportance, p.pause)
	p.pauseBtn.Hide()
	p.pauseHint = hbLabel("", widget.LowImportance)
	p.pauseHint.Hide()
	p.startHint = muted("请先打开课程浏览器")
	p.stage = stateLabel("阶段：未开始", widget.LowImportance)
	p.progress = newProgressRow()
	p.result = newTaskResult(openPathVia(s), s)
	p.doneInfo = p.result.summary
	p.startCard = newCard("开始学习",
		hrow(p.startBtn, p.pauseBtn, p.pauseHint),
		p.startHint,
		p.stage,
		p.progress.Root,
		p.result.Root,
	)

	p.ckList = container.NewVBox()
	p.checkpointsCard = newCard("学习进度", p.ckList)
	p.checkpointsCard.Hide()

	p.history = NewHistoryList("最近执行记录", true, func(r map[string]any) string {
		cr := ui.CourseResultFrom(r)
		if cr.Paused {
			return fmt.Sprintf("已处理 %d 门次；可继续学习", cr.Processed)
		}
		return ui.CourseCountText(cr)
	})
	p.history.OnLog = func(rec ui.HistoryRecord) { showJobLog(s, rec.JobID) }
	p.history.OnOpenDir = func(rec ui.HistoryRecord) {
		if rec.Dir != "" {
			s.Async(func() error { return s.Backend.OpenPath(context.Background(), rec.Dir) })
		}
	}
	p.history.OnDelete = func(rec ui.HistoryRecord) {
		ConfirmDanger(s.Win, "删除执行记录",
			fmt.Sprintf("将删除 %s 的这条执行记录及其任务日志、参数快照。确定继续？", rec.Time),
			"删除", func() {
				s.Async(func() error {
					_, err := s.Backend.DeleteHistory(context.Background(), "course_autoplay", rec.JobID)
					if err == nil {
						s.deleteJobParams(rec.JobID)
						p.reloadHistory()
						s.Toast("已删除", ToastSuccess)
					}
					return err
				})
			})
	}
	p.history.OnClear = func() {
		ConfirmDanger(s.Win, "清空执行记录",
			"将删除该应用全部执行记录及其任务日志、参数快照。确定继续？", "清空", func() {
				s.Async(func() error {
					r, err := s.Backend.DeleteHistory(context.Background(), "course_autoplay", "")
					if err == nil {
						s.deleteAppJobParams("course_autoplay")
						p.reloadHistory()
						s.Toast(fmt.Sprintf("已清空 %d 条记录", r.Records), ToastSuccess)
					}
					return err
				})
			})
	}

	// 常规双列：左浏览器（紧凑状态+URL）/学习选项；右开始学习/结果/排错日志；
	// 最大化右列再展示完整浏览器状态、学习进度、执行历史与技术详情。
	p.historyScope = newAppTooltipScope(container.NewVBox(p.history.Root))
	p.historyScope.Hide()
	p.layout = &pageSplitLayout{gap: cardGap, normalSplit: 3, maxSplit: 5}
	p.cardBox = container.New(p.layout,
		card1, card2, p.advancedSec.Root, p.startCard, p.result.LogRoot,
		p.checkpointsCard, p.historyScope, p.techCard)
	p.root = container.NewVScroll(p.cardBox)
	return p
}

func (p *pageCourse) Object() fyne.CanvasObject { return p.root }

// SetMaximized 只切显隐与双列布局，不重建页面、不改高级设置折叠状态、不读 Backend。
func (p *pageCourse) SetMaximized(maximized bool) {
	if p == nil || p.maximized == maximized {
		return
	}
	runUI(func() {
		p.maximized = maximized
		p.layout.maximized = maximized
		if maximized {
			p.compactStatus.Root.Hide()
			p.statusBox.Show()
			p.checkpointsCard.Show()
			p.historyScope.showForShell(p.shell)
			p.techCard.Show()
		} else {
			p.compactStatus.Root.Show()
			p.statusBox.Hide()
			p.checkpointsCard.Hide()
			p.historyScope.hideForShell(p.shell)
			p.techCard.Hide()
		}
		p.cardBox.Refresh()
		p.root.Refresh()
	})
}

var _ windowModePage = (*pageCourse)(nil)

func (p *pageCourse) OnShow() {
	p.reloadStatus()
	p.reloadCheckpoints()
	p.reloadHistory()
}

// courseStartHint 未就绪时与紧凑状态同口径的下一步提示。
func courseStartHint(st ui.CourseStatus) string {
	if st.Connected {
		return "浏览器已连接，但自动操作不可用，请重新打开课程浏览器"
	}
	return "请先打开课程浏览器"
}

// courseCompactStatus 只有连接且自动操作可用才算绿色就绪；已连接但不可用必须警告。
func courseCompactStatus(st ui.CourseStatus) (string, widget.Importance) {
	if st.Connected && st.Playwright {
		return fmt.Sprintf("浏览器已就绪 · 可开始学习 · %d 个页面 · 可继续 %d 个培训班", st.Pages, st.ActiveLists), widget.SuccessImportance
	}
	if st.Connected {
		return courseStartHint(st), widget.WarningImportance
	}
	return "浏览器未就绪 · 请先打开课程浏览器", widget.WarningImportance
}

func (p *pageCourse) applyCompactStatus(st ui.CourseStatus) {
	text, imp := courseCompactStatus(st)
	p.compactStatus.Set(text, imp)
}

// setCompactRunning 按启动时的 scanMode 恢复运行中文案（开始、暂停失败回滚共用）。
func (p *pageCourse) setCompactRunning() {
	text := "正在学习 · 浏览器已就绪"
	if p.scanMode {
		text = "正在扫描课程清单 · 浏览器已就绪"
	}
	p.compactStatus.Set(text, widget.MediumImportance)
}

// setCompactOutcome 终态先写任务结论，避免随后状态读取失败时残留运行/暂停假象。
func (p *pageCourse) setCompactOutcome(job ui.Job) {
	text := courseStageText(job, p.scanMode)
	imp := widget.MediumImportance
	switch {
	case job.Status == ui.JobSuccess:
		imp = widget.SuccessImportance
	case job.Status == ui.JobFailed:
		imp = widget.DangerImportance
	case job.Status == ui.JobCancelled:
		imp = widget.LowImportance
	}
	p.compactStatus.Set("本次"+text+" · 浏览器状态待刷新", imp)
}

func (p *pageCourse) reloadStatus() {
	p.shell.Async(func() error {
		st, err := p.shell.Backend.CourseStatus(context.Background())
		if err != nil {
			return nil
		}
		runUI(func() {
			conn := "未连接"
			pages := "-"
			if st.Connected {
				conn = "已连接"
				pages = fmt.Sprintf("%d 个页面", st.Pages)
			}
			auto := "不可用"
			if st.Playwright {
				auto = "可用"
			}
			port := st.CDPURL
			if port == "" {
				port = "未启动"
			}
			browser := st.Browser
			if browser == "" {
				browser = "-"
			}
			ready := st.Connected && st.Playwright
			browserState := "浏览器未就绪"
			if st.Connected {
				browserState = "浏览器已就绪"
			}
			learningState := "暂不可开始学习"
			if ready {
				learningState = "可开始学习"
			}
			p.statusKV.SetValues(
				[2]string{"浏览器状态", browserState},
				[2]string{"学习状态", learningState},
				[2]string{"已打开页面", pages},
				[2]string{"可继续学习", fmt.Sprintf("%d 个培训班", st.ActiveLists)},
			)
			p.techStatusKV.SetValues(
				[2]string{"连接状态", conn},
				[2]string{"连接地址", port},
				[2]string{"浏览器", browser},
				[2]string{"自动操作", auto},
			)
			// 运行中文案由 startRun/pause 显式维护，状态回填只负责静态就绪态。
			if !p.running {
				p.applyCompactStatus(st)
			}
			if p.startURL.Text == "" {
				p.startURL.SetText(st.StartURL)
			}
			// 未连接/自动操作不可用时禁用「开始学习」并提示先开浏览器；任务运行中保持禁用
			if ready && !p.running {
				p.startBtn.Enable()
			} else {
				p.startBtn.Disable()
			}
			if ready {
				p.startHint.Hide()
			} else {
				p.startHint.SetText(courseStartHint(st))
				p.startHint.Show()
			}
			if p.running {
				p.openBtn.Disable()
			} else {
				p.openBtn.Enable()
			}
		})
		return nil
	})
}

func (p *pageCourse) reloadCheckpoints() {
	p.shell.Async(func() error {
		cks, err := p.shell.Backend.CourseCheckpoints(context.Background())
		if err != nil {
			return err
		}
		runUI(func() {
			p.ckList.Objects = nil
			if len(cks) == 0 {
				p.ckList.Add(muted("暂无学习进度；开始学习后会按培训班和课程列表保存。"))
			}
			for _, c := range cks {
				p.ckList.Add(kvGrid(
					[2]string{"培训班", c.TrainID},
					[2]string{"课程列表", c.ListName},
					[2]string{"继续学习页 / 已处理", fmt.Sprintf("第 %d 页 / %d（失败 %d 跳过 %d）", c.Page, c.Processed, c.Failed, c.Skipped)},
					[2]string{"更新时间", c.UpdatedAt},
				))
				p.ckList.Add(widget.NewSeparator())
			}
			p.ckList.Refresh()
			reflowInScroll(p.ckList) // 学习进度动态增删后同步卡片高度
		})
		return nil
	})
}

func (p *pageCourse) reloadHistory() {
	reloadHistory(p.shell, "course_autoplay", p.history)
}

// openBrowser 保存课程网址并启动/复用课程浏览器（非 Job）。
func (p *pageCourse) openBrowser() {
	url := p.startURL.Text
	p.shell.Async(func() error {
		cfg, err := p.shell.Backend.GetConfig(context.Background())
		if err != nil {
			return err
		}
		ba := cfg.BrowserAutomation
		ba.StartURL = url
		if err := p.shell.Backend.SaveConfig(context.Background(), ui.ConfigPatch{
			BrowserAutomation: &ba,
		}); err != nil {
			return err
		}
		if err := p.shell.Backend.CourseOpenBrowser(context.Background(), url); err != nil {
			return err
		}
		p.shell.Toast("请在课程浏览器中登录学堂，进入课程设置列表并保持在前台", ToastSuccess)
		p.reloadStatus()
		return nil
	})
}

// startRun 开始学习；旧任务未终态时先等待（不与旧任务重叠运行）。
func (p *pageCourse) startRun() {
	opt := ui.CourseOptions{
		ScanOnly:    p.scanOnly.Checked,
		MaxAttempts: atoiOr(p.maxTry.Text, 3),
		Timeout:     atoiOr(p.timeout.Text, 120),
	}
	if p.currentJob != nil {
		prev := p.currentJob
		p.pauseHint.SetText("等待上一轮任务结束…")
		p.pauseHint.Show()
		p.compactStatus.Set("等待上一轮任务结束…", widget.MediumImportance)
		go func() {
			<-prev.Done()
			runUI(func() {
				if p.currentJob == prev {
					p.currentJob = nil
					p.running = false
				}
				p.pauseHint.Hide()
				p.startRun()
			})
		}()
		return
	}
	p.scanMode = opt.ScanOnly
	p.shell.Async(func() error {
		runUI(func() {
			mode := "学习"
			if opt.ScanOnly {
				mode = "扫描课程清单"
			}
			p.result.Begin(fmt.Sprintf("正在%s，请保持浏览器前台…", mode))
			p.progress.Reset()
			p.stage.SetText("阶段：准备启动")
			p.setCompactRunning()
			p.running = true
			p.startBtn.Disable()
			p.openBtn.Disable()
			p.pauseBtn.Show()
			var h *ui.JobHandle
			h = p.shell.StartJob("course_autoplay", map[string]any{
				"scan_only":    opt.ScanOnly,
				"max_attempts": opt.MaxAttempts,
				"timeout":      opt.Timeout,
			}, ui.JobHandler{
				OnLog: func(line string) {
					if p.currentJob != h {
						return // 过期任务日志：不污染新一轮
					}
					p.result.Log(line)
				},
				OnProgress: func(pr ui.JobProgress) {
					if p.currentJob != h {
						return
					}
					setProgress(p.progress, pr)
					label := pr.Label
					if label == "" {
						label = "处理中"
					}
					p.stage.SetText(fmt.Sprintf("阶段：%s（%d/%d）", label, pr.Current, pr.Total))
				},
				OnDone: func(job ui.Job) {
					if p.currentJob != h {
						return // 过期任务终态：不覆盖新一轮状态
					}
					p.currentJob = nil
					p.running = false
					p.openBtn.Enable()
					p.pauseBtn.Hide()
					p.pauseHint.Hide()
					o := courseOutcome(job, p.scanMode)
					p.result.Apply(o)
					p.stage.SetText("阶段：" + courseStageText(job, p.scanMode))
					p.setCompactOutcome(job)
					cr := ui.CourseResultFrom(job.Result)
					// 完成提醒以启动时模式为准：正式学习且本轮无未完成课程才弹
					if job.Status == ui.JobSuccess && cr.Pending == 0 && !p.scanMode {
						Info(p.shell.Win, "学习完成", fmt.Sprintf("当前课程列表已全部达到 100%%，共 %d 门课程。", cr.Total))
					}
					p.reloadStatus()
					p.reloadCheckpoints()
					p.reloadHistory()
				},
				OnError: func(err error) {
					if p.currentJob != h {
						return
					}
					p.currentJob = nil
					p.running = false
					p.pauseBtn.Hide()
					p.pauseHint.Hide()
					p.result.FailStart(err)
					p.stage.SetText("阶段：启动失败")
					p.compactStatus.Set("启动失败 · 请检查浏览器或网络后重试", widget.DangerImportance)
					p.reloadStatus()
				},
			})
			p.currentJob = h
		})
		return nil
	})
}

// pause 暂停学习（等价取消任务：保留浏览器与登录态）。
func (p *pageCourse) pause() {
	job := p.currentJob
	if job == nil {
		return
	}
	p.pauseBtn.Hide()
	p.pauseHint.SetText("正在暂停…")
	p.pauseHint.Show()
	p.stage.SetText("阶段：正在暂停")
	p.compactStatus.Set("正在暂停 · 当前进度已保留", widget.MediumImportance)
	job.CancelNotify(func(err error) {
		// 取消请求失败：任务仍在跑，恢复暂停按钮并明示失败，不留「正在暂停」假象
		ShowToast(p.shell.Win, "暂停失败："+err.Error(), ToastError)
		runUI(func() {
			if p.currentJob == job {
				p.pauseHint.SetText("暂停失败：" + err.Error())
				p.pauseBtn.Show()
				p.stage.SetText("阶段：暂停失败，任务仍在执行")
				p.setCompactRunning()
			}
		})
	})
	// 终态到达后 OnDone 收口并隐藏提示
}

// courseStageText 终态阶段文案（学习/扫描/暂停/失败）。
func courseStageText(job ui.Job, scanOnly bool) string {
	switch {
	case job.Paused():
		return "已暂停（可继续学习）"
	case job.Status == ui.JobSuccess && scanOnly:
		return "扫描完成"
	case job.Status == ui.JobSuccess:
		return "学习完成"
	case job.Status == ui.JobCancelled:
		return "已取消"
	case job.Status == ui.JobFailed:
		return "执行失败"
	default:
		return job.Status.CN()
	}
}

// atoiOr 数字解析，非法或 0 回默认。
func atoiOr(s string, def int) int {
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil || n <= 0 {
		return def
	}
	return n
}

// clampInt 钳制到 [lo, hi]。
func clampInt(n, lo, hi int) int {
	if n < lo {
		return lo
	}
	if n > hi {
		return hi
	}
	return n
}
