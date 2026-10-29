package appshell

import (
	"context"
	"fmt"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/widget"

	"github.com/zhangzhenyu91/j1toolkit/uvmp-toolkit/core-go/internal/ui"
)

// pageDaily 派车同步（daily_sync）：定时设置 / U 盘配置 / 立即执行 / 执行记录。
// 运行状态与完成摘要常驻结果区，技术日志收进「运行日志」折叠（失败自动展开）。
type pageDaily struct {
	shell *Shell
	root  fyne.CanvasObject

	statusKV    *kvCard
	dueLabel    *widget.Label
	schEnable   *enterpriseCheck
	schTime     *widget.Entry
	usbLabel    *widget.Entry
	usbFallback *widget.Entry
	usbMount    *widget.Label
	runDate     *widget.Entry
	startBtn    *widget.Button
	cancelBtn   *widget.Button
	progress    *progressRow
	result      *taskResult
	openDirBtn  *widget.Button // 结果区产物按钮（历史用例/外部断言共用）
	history     *HistoryList
	driver      string
	currentJob  *ui.JobHandle
}

func newPageDaily(s *Shell) *pageDaily {
	p := &pageDaily{shell: s, driver: "os"}

	p.statusKV = newKVCard("状态",
		[2]string{"上次成功", "-"},
		[2]string{"上次产物", "-"},
		[2]string{"最近任务", "-"},
		[2]string{"下次定时", "-"},
	)
	p.dueLabel = stateLabel("", widget.LowImportance)
	statusCard := newCard("运行状态", p.dueLabel, p.statusKV.box)

	p.schEnable = newEnterpriseCheck("启用每日定时导出", nil)
	p.schEnable.SetChecked(true)
	p.schTime = widget.NewEntry()
	p.schTime.SetPlaceHolder("09:15")
	schSave := newMotionButton("保存定时", p.saveSchedule)
	schSave.Importance = widget.LowImportance
	schCard := newCard("定时设置",
		hrow(p.schEnable, newMinBox(fyne.NewSize(110, 0), p.schTime), schSave),
		muted("由系统定时器每分钟检查一次，到点自动执行；改时间即时生效，无需管理员权限；机器关机错过到点会在开机后自动补跑。执行模式（系统定时器 / 客户端驻留执行）在设置页切换。"),
	)

	p.usbLabel = widget.NewEntry()
	p.usbLabel.SetPlaceHolder("GLKVM")
	p.usbFallback = widget.NewEntry()
	p.usbFallback.SetPlaceHolder("U盘不可用时的落盘目录（留空则任务失败）")
	usbSave := newMotionButton("保存 U 盘设置", p.saveUSB)
	usbSave.Importance = widget.LowImportance
	usbDetect := newMotionButton("重新检测", p.OnShow)
	p.usbMount = stateLabel("挂载点：-", widget.LowImportance)
	usbCard := newCard("U 盘（KVM 虚拟 U 盘）",
		hrow(hbLabel("卷标", widget.LowImportance), newMinBox(fyne.NewSize(150, 0), p.usbLabel), usbSave, usbDetect),
		p.usbFallback,
		p.usbMount,
	)

	p.runDate = widget.NewEntry()
	p.runDate.SetPlaceHolder("留空 = 今天（YYYY-MM-DD）")
	p.startBtn = newMotionButtonWithIcon("立即执行一次", nil, p.startRun)
	p.startBtn.Importance = widget.HighImportance
	p.cancelBtn = newMotionButtonWithIcon("取消执行", nil, p.cancel)
	p.cancelBtn.Importance = widget.DangerImportance
	p.cancelBtn.Hide()
	p.progress = newProgressRow()
	p.result = newTaskResult(openPathVia(s))
	p.openDirBtn = p.result.artifactBtn
	runCard := newCard("立即执行",
		hrow(newMinBox(fyne.NewSize(220, 0), p.runDate), p.startBtn, p.cancelBtn),
		muted("派车单默认导出到 U 盘；任务进行中可随时取消，完成后可直接打开产物目录。"),
		p.progress.Root,
		p.result.Root,
	)

	p.history = NewHistoryList("最近执行记录", false, func(r map[string]any) string {
		return ui.DailySummary(ui.DailyResultFrom(r))
	})
	p.history.OnLog = func(rec ui.HistoryRecord) { showJobLog(s, rec.JobID) }
	p.history.OnDelete = func(rec ui.HistoryRecord) {
		ConfirmDanger(s.Win, "删除执行记录",
			fmt.Sprintf("将删除 %s 的这条执行记录及其任务日志。确定继续？", rec.Time),
			"删除", func() {
				s.Async(func() error {
					_, err := s.Backend.DeleteHistory(context.Background(), "daily_sync", rec.JobID)
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
					r, err := s.Backend.DeleteHistory(context.Background(), "daily_sync", "")
					if err == nil {
						p.reloadHistory()
						s.Toast(fmt.Sprintf("已清空 %d 条记录", r.Records), ToastSuccess)
					}
					return err
				})
			})
	}

	p.root = pageScroll(statusCard, schCard, usbCard, runCard, p.result.LogRoot, p.history.Root)
	return p
}

func (p *pageDaily) Object() fyne.CanvasObject { return p.root }

func (p *pageDaily) OnShow() {
	p.reloadStatus()
	p.reloadHistory()
}

func (p *pageDaily) reloadStatus() {
	p.shell.Async(func() error {
		st, err := p.shell.Backend.AppStatus(context.Background(), "daily_sync")
		if err != nil {
			return nil // 状态卡失败不打断页面
		}
		ds := ui.DailyStatusFrom(st)
		lastSuccess := "从未执行"
		if ds.Stamp.Time != "" {
			lastSuccess = ds.Stamp.Time
			if ds.Stamp.Count != 0 {
				lastSuccess += fmt.Sprintf("（%d 条）", ds.Stamp.Count)
			}
		}
		lastFile := ds.Stamp.File
		if lastFile == "" {
			lastFile = "-"
		}
		lastRun := "-"
		if ds.LastRun.Time != "" {
			lastRun = ds.LastRun.Time + "　" + ds.LastRun.Status.CN()
			if ds.LastRun.Error != "" {
				lastRun += "：" + ds.LastRun.Error
			}
		}
		runUI(func() {
			p.driver = ds.Schedule.Driver
			p.statusKV.SetValues(
				[2]string{"上次成功", lastSuccess},
				[2]string{"上次产物", lastFile},
				[2]string{"最近任务", lastRun},
				[2]string{"下次定时", ds.Schedule.NextRun},
			)
			p.dueLabel.SetText(ds.DueReason)
			p.schEnable.SetChecked(ds.Schedule.Enabled)
			if ds.Schedule.Time != "" {
				p.schTime.SetText(ds.Schedule.Time)
			}
			p.usbLabel.SetText(ds.USB.Label)
			p.usbFallback.SetText(ds.USB.FallbackDir)
			mount := ds.USB.Mount
			if mount == "" {
				mount = "未检测到（执行时将尝试自动挂载）"
			}
			p.usbMount.SetText("挂载点：" + mount)
		})
		return nil
	})
}

func (p *pageDaily) reloadHistory() {
	reloadHistory(p.shell, "daily_sync", p.history)
}

func (p *pageDaily) saveSchedule() {
	timeStr := p.schTime.Text
	enabled := p.schEnable.Checked
	if !hhmmRe.MatchString(timeStr) {
		p.shell.Toast("时间格式应为 HH:MM，如 09:15", ToastWarning)
		return
	}
	p.shell.Async(func() error {
		cfg, err := p.shell.Backend.GetConfig(context.Background())
		if err != nil {
			return err
		}
		// 仅更新 enabled/time；driver 保留配置现值（状态刷新失败时不得误改执行模式）
		sch := cfg.Schedule
		sch.Enabled = enabled
		sch.Time = timeStr
		err = p.shell.Backend.SaveConfig(context.Background(), ui.ConfigPatch{Schedule: &sch})
		if err == nil {
			p.shell.Toast("已保存，定时器将在下一检查周期生效", ToastSuccess)
			p.reloadStatus()
		}
		return err
	})
}

func (p *pageDaily) saveUSB() {
	label := strings.TrimSpace(p.usbLabel.Text)
	if label == "" {
		label = "GLKVM"
	}
	fallback := strings.TrimSpace(p.usbFallback.Text)
	p.shell.Async(func() error {
		err := p.shell.Backend.SaveConfig(context.Background(), ui.ConfigPatch{
			USB: &ui.USBConfig{Label: label, FallbackDir: fallback},
		})
		if err == nil {
			p.shell.Toast("已保存", ToastSuccess)
			p.reloadStatus()
		}
		return err
	})
}

// startRun 立即执行（凭据预检后下发任务）。
func (p *pageDaily) startRun() {
	date := strings.TrimSpace(p.runDate.Text)
	p.shell.Async(func() error {
		ok, err := ssoReady(p.shell)
		if err == nil && !ok {
			ShowToast(p.shell.Win, "尚未配置 SSO 账号密码——请先到「设置」页填写（或开启开发模式试用）", ToastWarning)
			return nil
		}
		runUI(func() {
			p.result.Begin("正在导出派车单，请稍候…")
			p.progress.Reset()
			p.startBtn.Disable()
			p.cancelBtn.Show()
			var h *ui.JobHandle
			h = p.shell.StartJob("daily_sync", map[string]any{"date": date}, ui.JobHandler{
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
				},
				OnDone: func(job ui.Job) {
					if p.currentJob != h {
						return // 过期任务终态：不覆盖新一轮状态
					}
					p.currentJob = nil
					p.startBtn.Enable()
					p.cancelBtn.Hide()
					p.result.Apply(dailyOutcome(job))
					p.reloadStatus()
					p.reloadHistory()
				},
				OnError: func(err error) {
					if p.currentJob != h {
						return
					}
					p.currentJob = nil
					p.startBtn.Enable()
					p.cancelBtn.Hide()
					p.result.FailStart(err)
				},
			})
			p.currentJob = h
		})
		return nil
	})
}

func (p *pageDaily) cancel() {
	if p.currentJob != nil {
		p.currentJob.Cancel()
	}
}
