package appshell

import (
	"context"
	"fmt"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/widget"

	"github.com/zhangzhenyu91/j1toolkit/uvmp-toolkit/core-go/internal/ui"
)

// pageDaily 派车同步（daily_sync）：每日自动导出 / U 盘保存位置 / 立即执行一次 / 执行记录。
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
	formLoaded  bool
	currentJob  *ui.JobHandle
}

func newPageDaily(s *Shell) *pageDaily {
	p := &pageDaily{shell: s, driver: "os"}

	p.statusKV = newKVCard("状态",
		[2]string{"上次成功", "-"},
		[2]string{"上次产物", "-"},
		[2]string{"最近任务", "-"},
		[2]string{"下次自动导出", "-"},
	)
	p.dueLabel = stateLabel("", widget.LowImportance)
	statusCard := newCard("运行状态", p.dueLabel, p.statusKV.box)

	p.schEnable = newEnterpriseCheck("每天自动导出派车单", nil)
	p.schEnable.SetChecked(true)
	p.schTime = widget.NewEntry()
	p.schTime.SetPlaceHolder("09:15")
	schSave := newMotionButton("保存设置", p.saveSchedule)
	schSave.Importance = widget.LowImportance
	schCard := newCard("每日自动导出",
		hrow(p.schEnable, hbLabel("导出时间", widget.LowImportance), newMinBox(fyne.NewSize(110, 0), p.schTime), schSave),
		muted("到点自动执行，关机错过会在开机后补跑；修改时间后立即生效。"),
	)

	p.usbLabel = widget.NewEntry()
	p.usbLabel.SetPlaceHolder("GLKVM")
	p.usbFallback = widget.NewEntry()
	p.usbFallback.SetPlaceHolder("U 盘不可用时的备用保存位置（留空则导出失败）")
	usbSave := newMotionButton("保存设置", p.saveUSB)
	usbSave.Importance = widget.LowImportance
	usbDetect := newMotionButton("重新查找 U 盘", p.OnShow)
	p.usbMount = stateLabel("U 盘位置：-", widget.LowImportance)
	usbCard := newCard("U 盘保存位置",
		hrow(hbLabel("U 盘名称", widget.LowImportance), newMinBox(fyne.NewSize(150, 0), p.usbLabel), usbSave, usbDetect),
		p.usbFallback,
		p.usbMount,
		muted("派车单默认保存到 U 盘；找不到 U 盘时，可保存到备用位置。"),
	)

	p.runDate = widget.NewEntry()
	p.runDate.SetPlaceHolder("导出日期（留空为今天）")
	p.startBtn = newMotionButtonWithIcon("立即执行一次", nil, p.startRun)
	p.startBtn.Importance = widget.HighImportance
	p.cancelBtn = newMotionButtonWithIcon("取消执行", nil, p.cancel)
	p.cancelBtn.Importance = widget.DangerImportance
	p.cancelBtn.Hide()
	p.progress = newProgressRow()
	p.result = newTaskResult(openPathVia(s), s)
	p.openDirBtn = p.result.artifactBtn
	runCard := newCard("立即执行一次",
		hrow(newMinBox(fyne.NewSize(220, 0), p.runDate), p.startBtn, p.cancelBtn),
		muted("选择要导出的日期，留空则导出今天；完成后可直接打开保存位置。"),
		p.progress.Root,
		p.result.Root,
	)

	p.history = NewHistoryList("最近执行记录", false, func(r map[string]any) string {
		return ui.DailySummary(ui.DailyResultFrom(r))
	})
	p.history.OnLog = func(rec ui.HistoryRecord) { showJobLog(s, rec.JobID) }
	p.history.OnDelete = func(rec ui.HistoryRecord) {
		ConfirmDanger(s.Win, "删除执行记录",
			fmt.Sprintf("将删除 %s 的这条执行记录及其任务日志、参数快照。确定继续？", rec.Time),
			"删除", func() {
				s.Async(func() error {
					_, err := s.Backend.DeleteHistory(context.Background(), "daily_sync", rec.JobID)
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
					r, err := s.Backend.DeleteHistory(context.Background(), "daily_sync", "")
					if err == nil {
						s.deleteAppJobParams("daily_sync")
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
				[2]string{"下次自动导出", ds.Schedule.NextRun},
			)
			p.dueLabel.SetText(ds.DueReason)
			mount := ds.USB.Mount
			if mount == "" {
				mount = "未检测到（导出时将自动查找）"
			}
			p.usbMount.SetText("U 盘位置：" + mount)
			if !p.formLoaded {
				p.formLoaded = true
				p.schEnable.SetChecked(ds.Schedule.Enabled)
				if ds.Schedule.Time != "" {
					p.schTime.SetText(ds.Schedule.Time)
				}
				p.usbLabel.SetText(ds.USB.Label)
				p.usbFallback.SetText(ds.USB.FallbackDir)
			}
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
			p.shell.Toast("已保存，到点会自动执行", ToastSuccess)
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
			ShowToast(p.shell.Win, "尚未配置派车系统账号——请先到「设置」页填写（或开启开发模式试用）", ToastWarning)
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
