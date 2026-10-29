package appshell

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/widget"

	"github.com/zhangzhenyu91/j1toolkit/uvmp-toolkit/core-go/internal/ui"
)

// pageCourse 学堂刷课（course_autoplay）：受控浏览器 / 执行选项 / 开始-暂停 / 检查点 / 执行记录。
type pageCourse struct {
	shell *Shell
	root  fyne.CanvasObject

	statusKV  *kvCard
	startURL  *widget.Entry
	openBtn   *widget.Button
	scanOnly  *widget.Check
	maxTry    *widget.Entry
	timeout   *widget.Entry
	startBtn  *widget.Button
	pauseBtn  *widget.Button
	pauseHint *widget.Label
	startHint *widget.Label
	running   bool
	progress  *progressRow
	log       *LogView
	doneInfo  *widget.Label
	ckList    *fyne.Container
	history   *HistoryList

	currentJob *ui.JobHandle
	scanMode   bool // 启动时的模式（完成提醒以此为准）
}

func newPageCourse(s *Shell) *pageCourse {
	p := &pageCourse{shell: s}

	p.statusKV = newKVCard("受控浏览器",
		[2]string{"CDP 地址", "-"},
		[2]string{"浏览器", "-"},
		[2]string{"连接 / 页面", "-"},
		[2]string{"Playwright", "-"},
		[2]string{"可恢复培训班", "-"},
	)
	p.startURL = widget.NewEntry()
	p.startURL.SetPlaceHolder("输入打开浏览器后自动访问的 http/https 网址")
	p.openBtn = widget.NewButton("打开浏览器", p.openBrowser)
	card1 := newCard("受控浏览器",
		p.statusKV.box,
		hrowGrow(p.startURL, p.openBtn),
		muted("启动或复用受控浏览器；请在该浏览器登录、打开课程设置列表并置前台。"),
	)

	p.scanOnly = widget.NewCheck("仅扫描（不进入课程）", nil)
	p.maxTry = widget.NewEntry()
	p.maxTry.SetText("3")
	p.maxTry.Resize(fyne.NewSize(60, 0))
	p.timeout = widget.NewEntry()
	p.timeout.SetText("120")
	p.timeout.Resize(fyne.NewSize(70, 0))
	card2 := newCard("执行选项",
		hrow(p.scanOnly, muted("单课尝试次数"), p.maxTry, muted("单步超时（秒）"), p.timeout),
	)

	p.startBtn = widget.NewButtonWithIcon("开始学习", nil, p.startRun)
	p.startBtn.Importance = widget.HighImportance
	p.startBtn.Disable() // 未连接受控浏览器前不可用（状态刷新后按连接/运行态放行）
	p.pauseBtn = widget.NewButtonWithIcon("暂停学习", nil, p.pause)
	p.pauseBtn.Importance = widget.DangerImportance
	p.pauseBtn.Hide()
	p.pauseHint = muted("")
	p.pauseHint.Hide()
	p.startHint = muted("请先打开受控浏览器")
	p.progress = newProgressRow()
	p.log = NewLogView()
	p.doneInfo = muted("")
	p.doneInfo.Hide()
	card3 := newCard("执行",
		hrow(p.startBtn, p.pauseBtn, p.pauseHint),
		p.startHint,
		p.doneInfo,
		p.progress.Root,
		p.log.Root,
	)

	p.ckList = container.NewVBox()
	card4 := newCard("续刷检查点", p.ckList)

	p.history = NewHistoryList("最近执行记录", true, func(r map[string]any) string {
		cr := ui.CourseResultFrom(r)
		if cr.Paused {
			return fmt.Sprintf("已处理 %d 门次；可续刷", cr.Processed)
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
			fmt.Sprintf("将删除 %s 的这条执行记录及其任务日志。确定继续？", rec.Time),
			"删除", func() {
				s.Async(func() error {
					_, err := s.Backend.DeleteHistory(context.Background(), "course_autoplay", rec.JobID)
					if err == nil {
						p.reloadHistory()
						s.Toast("已删除", ToastSuccess)
					}
					return err
				})
			})
	}
	p.history.OnClear = func() {
		ConfirmDanger(s.Win, "清空执行记录",
			"将删除该应用全部执行记录及其任务日志。确定继续？", "清空", func() {
				s.Async(func() error {
					r, err := s.Backend.DeleteHistory(context.Background(), "course_autoplay", "")
					if err == nil {
						p.reloadHistory()
						s.Toast(fmt.Sprintf("已清空 %d 条记录", r.Records), ToastSuccess)
					}
					return err
				})
			})
	}

	p.root = pageScroll(card1, card2, card3, card4, p.history.Root)
	return p
}

func (p *pageCourse) Object() fyne.CanvasObject { return p.root }

func (p *pageCourse) OnShow() {
	p.reloadStatus()
	p.reloadCheckpoints()
	p.reloadHistory()
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
				pages = fmt.Sprintf("%d 个页", st.Pages)
			}
			pw := "不可用"
			if st.Playwright {
				pw = "可用"
			}
			cdp := st.CDPURL
			if cdp == "" {
				cdp = "未启动"
			}
			browser := st.Browser
			if browser == "" {
				browser = "-"
			}
			p.statusKV.SetValues(
				[2]string{"CDP 地址", cdp},
				[2]string{"浏览器", browser},
				[2]string{"连接 / 页面", conn + " / " + pages},
				[2]string{"Playwright", pw},
				[2]string{"可恢复培训班", fmt.Sprintf("%d 个", st.ActiveLists)},
			)
			if p.startURL.Text == "" {
				p.startURL.SetText(st.StartURL)
			}
			// 未连接/无 Playwright 时禁用「开始学习」并提示先开浏览器；任务运行中保持禁用
			if st.Connected && st.Playwright && !p.running {
				p.startBtn.Enable()
			} else {
				p.startBtn.Disable()
			}
			if st.Connected && st.Playwright {
				p.startHint.Hide()
			} else {
				p.startHint.SetText("请先打开受控浏览器")
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
				p.ckList.Add(muted("暂无检查点（开始学习后按培训班/列表独立持久化）"))
			}
			for _, c := range cks {
				p.ckList.Add(kvGrid(
					[2]string{"培训班", c.TrainID},
					[2]string{"列表", c.ListName},
					[2]string{"续刷页 / 已处理", fmt.Sprintf("第 %d 页 / %d（失败 %d 跳过 %d）", c.Page, c.Processed, c.Failed, c.Skipped)},
					[2]string{"更新时间", c.UpdatedAt},
				))
				p.ckList.Add(widget.NewSeparator())
			}
			p.ckList.Refresh()
		})
		return nil
	})
}

func (p *pageCourse) reloadHistory() {
	reloadHistory(p.shell, "course_autoplay", p.history)
}

// openBrowser 保存启动网址并启动/复用受控浏览器（非 Job）。
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
		p.shell.Toast("请在该浏览器登录、打开课程设置列表并置前台", ToastSuccess)
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
		p.pauseHint.SetText("等待旧任务结束…")
		p.pauseHint.Show()
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
			p.log.Clear()
			p.doneInfo.Hide()
			p.progress.Reset()
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
				OnLog:      func(line string) { p.log.Append(line) },
				OnProgress: func(pr ui.JobProgress) { setProgress(p.progress, pr) },
				OnDone: func(job ui.Job) {
					if p.currentJob != h {
						return // 过期任务终态：不覆盖新一轮状态
					}
					p.currentJob = nil
					p.running = false
					p.openBtn.Enable()
					p.pauseBtn.Hide()
					p.pauseHint.Hide()
					p.reloadStatus()
					p.reloadCheckpoints()
					p.reloadHistory()
					cr := ui.CourseResultFrom(job.Result)
					switch {
					case job.Paused():
						p.doneInfo.SetText(fmt.Sprintf("已暂停：浏览器和登录态保留，可切换列表后再次开始学习。%s", ui.CourseCountText(cr)))
					case job.Status == ui.JobSuccess && cr.Pending > 0:
						p.doneInfo.SetText("仍有未完成：" + ui.CourseCountText(cr))
					case job.Status == ui.JobSuccess:
						label := "扫描完成"
						if !p.scanMode {
							label = "全部学习完成 100%"
						}
						p.doneInfo.SetText(label + "：" + ui.CourseCountText(cr))
						// 完成提醒以启动时模式为准：正式学习且本轮无未完成课程才弹
						if cr.Pending == 0 && !p.scanMode {
							Info(p.shell.Win, "学习完成", fmt.Sprintf("当前课程列表已全部达到 100%%，共 %d 门课程。", cr.Total))
						}
					case job.Status == ui.JobCancelled:
						p.doneInfo.SetText("已取消：" + ui.CourseCountText(cr))
					default:
						text := job.Error
						if text == "" {
							text = ui.CourseCountText(cr)
						}
						p.doneInfo.SetText("执行失败：" + text)
					}
					p.doneInfo.Show()
				},
				OnError: func(err error) {
					if p.currentJob != h {
						return
					}
					p.currentJob = nil
					p.running = false
					p.startBtn.Enable()
					p.openBtn.Enable()
					p.pauseBtn.Hide()
					p.pauseHint.Hide()
					ShowToast(p.shell.Win, "启动失败："+err.Error(), ToastError)
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
	p.startBtn.Enable()
	p.pauseBtn.Hide()
	p.pauseHint.SetText("正在暂停")
	p.pauseHint.Show()
	job.CancelNotify(func(err error) {
		// 取消请求失败：任务仍在跑，明示失败不留「正在暂停」假象
		ShowToast(p.shell.Win, "暂停失败："+err.Error(), ToastError)
		runUI(func() {
			if p.currentJob == job {
				p.pauseHint.SetText("暂停失败：" + err.Error())
			}
		})
	})
	// 终态到达后 OnDone 收口并隐藏提示
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
