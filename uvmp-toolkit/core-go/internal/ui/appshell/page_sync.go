package appshell

import (
	"context"
	"fmt"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/widget"

	"github.com/zhangzhenyu91/j1toolkit/uvmp-toolkit/core-go/internal/ui"
)

// pageSync 树障/交跨统计表同步页（tree_sync / crossing_sync 共用骨架，契约同构）：
// 状态卡 / 同步设置 / 立即同步 / 执行记录。
// 完成摘要与产物入口常驻结果区，技术日志收进「运行日志」折叠（失败自动展开）。
type pageSync struct {
	shell        *Shell
	appID        string
	defFileName  string
	root         fyne.CanvasObject
	scroll       *container.Scroll
	content      *fyne.Container
	historyScope *appTooltipScope

	statusKV     *kvCard
	cfgHint      *widget.Label
	fileHint     *widget.Label
	usbHint      *widget.Label
	compactRow   *fyne.Container
	safetyNote   *widget.Label
	settingsNote *widget.Label
	runNote      *widget.Label
	localPath    *widget.Entry
	fileName     *widget.Entry
	nightly      *widget.Entry
	runBtn       *appButton
	nightlyBtn   *appButton
	cancelBtn    *appButton
	progress     *progressRow
	result       *taskResult
	history      *HistoryList
	formLoaded   bool
	currentJob   *ui.JobHandle
	maximized    bool

	// 最近一次状态回填（结果摘要需要 U 盘挂载点 / 内网表路径）
	usbMountPath string
	localPathTxt string
}

func newPageSync(s *Shell, appID string) *pageSync {
	defFile := "检修一班树木统计表.xlsx"
	if appID == "crossing_sync" {
		defFile = "检修一班交跨统计表.xlsx"
	}
	p := &pageSync{shell: s, appID: appID, defFileName: defFile}

	p.statusKV = newKVCard("状态",
		[2]string{"内网表", "-"},
		[2]string{"文件检查", "-"},
		[2]string{"U 盘状态", "-"},
		[2]string{"上次自动备份", "-"},
		[2]string{"最近任务", "-"},
	)
	p.cfgHint = muted("")
	p.fileHint = stateLabel("文件检查：-", widget.LowImportance)
	p.usbHint = stateLabel("U 盘：-", widget.LowImportance)
	p.compactRow = container.NewGridWithColumns(2, p.fileHint, p.usbHint)
	// 常规只露文件检查与 U 盘两列紧凑状态，最大化只露五行完整状态；两组互斥，避免同卡叠放。
	statusCard := cardSurface(container.NewVBox(secTitle("运行状态"), p.cfgHint, p.compactRow, p.statusKV.box))

	p.localPath = widget.NewEntry()
	p.localPath.SetPlaceHolder("选择内网统计表，或输入文件位置")
	pickBtn := newAppButton("选择文件", "file", widget.LowImportance, p.pickFile)
	p.fileName = widget.NewEntry()
	p.fileName.SetPlaceHolder(defFile)
	p.nightly = widget.NewEntry()
	p.nightly.SetPlaceHolder("23:55")
	ntBox := newMinBox(fyne.NewSize(90, 0), p.nightly)
	saveBtn := newAppButton("保存设置", "save", widget.LowImportance, p.save)
	p.safetyNote = muted("替换内网表前会自动备份原文件。")
	p.settingsNote = muted("U 盘接入后会按其中的同步任务自动处理：备份内网表，或用 U 盘中的新表替换内网表；替换前会自动备份原文件。到达每日自动备份时间时，会再备份一次内网表。")
	syncCard := cardSurface(container.NewVBox(
		secTitle("同步设置"),
		hrow(hbLabel("内网表", widget.LowImportance), newMinBox(fyne.NewSize(240, 0), p.localPath), pickBtn),
		hrow(hbLabel("U 盘中的文件名", widget.LowImportance), newMinBox(fyne.NewSize(200, 0), p.fileName)),
		// 安全说明与备份时间同行，常规可见且不额外占用首屏高度。
		hrow(hbLabel("每日自动备份时间", widget.LowImportance), ntBox, saveBtn, p.safetyNote),
		p.settingsNote,
	))

	p.runBtn = newAppButton("按 U 盘中的同步任务执行", "play", widget.HighImportance, func() {
		p.start(map[string]any{"tick": true}, "正在按 U 盘中的同步任务执行…")
	})
	p.nightlyBtn = newAppButton("立即备份到 U 盘", "upload", widget.MediumImportance, func() {
		p.start(map[string]any{"nightly": true}, "正在备份内网表到 U 盘…")
	})
	p.cancelBtn = newAppButton("取消执行", "cancel", widget.LowImportance, p.cancel)
	p.cancelBtn.Hide()
	p.progress = newProgressRow()
	p.result = newTaskResult(openPathVia(s), s)
	p.runNote = muted("可按 U 盘中的任务同步，或直接备份内网表；完成后可查看同步结果。")
	runCard := newCard("立即同步",
		hrow(p.runBtn, p.nightlyBtn, p.cancelBtn),
		p.runNote,
		p.progress.Root,
		p.result.Root,
	)

	p.history = NewHistoryList("最近执行记录", false, func(r map[string]any) string {
		return ui.SyncSummary(ui.SyncRunResultFrom(r))
	})
	p.history.OnLog = func(rec ui.HistoryRecord) { showJobLog(s, rec.JobID) }
	p.history.OnDelete = func(rec ui.HistoryRecord) {
		ConfirmDanger(s.Win, "删除执行记录",
			fmt.Sprintf("将删除 %s 的这条执行记录及其任务日志、参数快照。确定继续？", rec.Time),
			"删除", func() {
				s.Async(func() error {
					_, err := s.Backend.DeleteHistory(context.Background(), p.appID, rec.JobID)
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
					r, err := s.Backend.DeleteHistory(context.Background(), p.appID, "")
					if err == nil {
						s.deleteAppJobParams(p.appID)
						p.reloadHistory()
						s.Toast(fmt.Sprintf("已清空 %d 条记录", r.Records), ToastSuccess)
					}
					return err
				})
			})
	}

	p.historyScope = newAppTooltipScope(p.history.Root.(*fyne.Container))
	p.scroll = pageScroll(statusCard, syncCard, runCard, p.result.LogRoot, p.historyScope)
	p.content = p.scroll.Content.(*fyne.Container)
	p.root = p.scroll
	p.applyWindowMode(false)
	return p
}

func (p *pageSync) Object() fyne.CanvasObject { return p.root }

var _ windowModePage = (*pageSync)(nil)

// SetMaximized 只切换预建内容的显隐与列布局：不重建页面、不清输入/任务、不访问 Backend。
func (p *pageSync) SetMaximized(maximized bool) {
	if p.maximized == maximized {
		return
	}
	runUI(func() {
		p.maximized = maximized
		p.applyWindowMode(maximized)
	})
}

func (p *pageSync) applyWindowMode(maximized bool) {
	if maximized {
		p.statusKV.box.Show()
		p.compactRow.Hide()
		p.settingsNote.Show()
		p.runNote.Show()
		p.historyScope.showForShell(p.shell)
		p.content.Layout = &pageGridLayout{
			gap: cardGap, split: 2, breakWidth: 940,
			measuredWidth: p.content.Size().Width,
		}
	} else {
		p.statusKV.box.Hide()
		p.compactRow.Show()
		p.settingsNote.Hide()
		p.runNote.Hide()
		p.historyScope.hideForShell(p.shell)
		p.content.Layout = pageCardLayout{gap: cardGap}
	}
	p.content.Refresh()
	p.scroll.Refresh()
}

func (p *pageSync) OnShow() {
	p.reloadStatus()
	p.reloadHistory()
}

func (p *pageSync) reloadStatus() {
	p.shell.Async(func() error {
		st, err := p.shell.Backend.AppStatus(context.Background(), p.appID)
		if err != nil {
			return nil
		}
		ss := ui.SyncStatusFrom(p.appID, st)
		fileCheck := "-"
		if ss.LocalPath != "" {
			if ss.LocalExists {
				fileCheck = "存在"
			} else {
				fileCheck = "不存在（执行将失败）"
			}
		}
		usb := ss.USBMount
		if usb == "" {
			usb = "未检测到（接入后将自动执行）"
		}
		stamp := ss.Stamp.Time
		if stamp == "" {
			stamp = "从未执行"
		}
		lastRun := "-"
		if ss.LastRun.Time != "" {
			lastRun = ss.LastRun.Time + "　" + ss.LastRun.Status.CN()
			if ss.LastRun.Error != "" {
				lastRun += "：" + ss.LastRun.Error
			}
		}
		runUI(func() {
			p.usbMountPath = ss.USBMount
			p.localPathTxt = ss.LocalPath
			p.statusKV.SetValues(
				[2]string{"内网表", orDash(ss.LocalPath)},
				[2]string{"文件检查", fileCheck},
				[2]string{"U 盘状态", usb},
				[2]string{"上次自动备份", stamp},
				[2]string{"最近任务", lastRun},
			)
			fileImportance := widget.LowImportance
			if ss.LocalPath != "" {
				if ss.LocalExists {
					fileImportance = widget.SuccessImportance
				} else {
					fileImportance = widget.DangerImportance
				}
			}
			usbImportance := widget.WarningImportance
			if ss.USBMount != "" {
				usbImportance = widget.SuccessImportance
			}
			setStateLabel(p.fileHint, "文件检查："+fileCheck, fileImportance)
			setStateLabel(p.usbHint, "U 盘："+usb, usbImportance)
			if ss.Configured() {
				p.cfgHint.SetText("")
				p.cfgHint.Hide()
			} else {
				p.cfgHint.SetText("尚未选择内网统计表，请先在下方完成同步设置")
				p.cfgHint.Show()
			}
			reflowInScroll(p.cfgHint) // 空提示隐藏后卡片回收高度
			if !p.formLoaded {
				p.formLoaded = true
				p.localPath.SetText(ss.LocalPath)
				name := ss.FileName
				if name == "" {
					name = p.defFileName
				}
				p.fileName.SetText(name)
				nt := ss.NightlyTime
				if nt == "" {
					nt = "23:55"
				}
				p.nightly.SetText(nt)
			}
		})
		return nil
	})
}

func (p *pageSync) reloadHistory() {
	reloadHistory(p.shell, p.appID, p.history)
}

func (p *pageSync) pickFile() {
	PickFile(p.shell.Win, "选择内网统计表", []string{".xlsx"}, func(path string) {
		if path != "" {
			p.localPath.SetText(path)
		}
	})
}

func (p *pageSync) save() {
	local := strings.TrimSpace(p.localPath.Text)
	if local == "" {
		p.shell.Toast("请先选择内网统计表", ToastWarning)
		return
	}
	nt := strings.TrimSpace(p.nightly.Text)
	if !hhmmRe.MatchString(nt) {
		p.shell.Toast("请按 24 小时制填写每日自动备份时间，如 23:55", ToastWarning)
		return
	}
	name := strings.TrimSpace(p.fileName.Text)
	if name == "" {
		name = p.defFileName
	}
	patch := ui.SyncAppConfig{LocalPath: local, FileName: name, NightlyTime: nt}
	p.shell.Async(func() error {
		var err error
		if p.appID == "tree_sync" {
			err = p.shell.Backend.SaveConfig(context.Background(), ui.ConfigPatch{TreeSync: &patch})
		} else {
			err = p.shell.Backend.SaveConfig(context.Background(), ui.ConfigPatch{CrossingSync: &patch})
		}
		if err == nil {
			p.shell.Toast("已保存", ToastSuccess)
			p.reloadStatus()
		}
		return err
	})
}

func (p *pageSync) start(params map[string]any, beginText string) {
	if p.localPath.Text == "" {
		p.shell.Toast("尚未配置内网表路径——请先在上方「同步设置」选择文件并保存", ToastWarning)
		return
	}
	p.shell.Async(func() error {
		runUI(func() {
			p.result.Begin(beginText)
			p.progress.Reset()
			p.runBtn.Disable()
			p.nightlyBtn.Disable()
			p.cancelBtn.Show()
			var h *ui.JobHandle
			h = p.shell.StartJob(p.appID, params, ui.JobHandler{
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
					p.runBtn.Enable()
					p.nightlyBtn.Enable()
					p.cancelBtn.Hide()
					p.result.Apply(syncOutcome(job, p.usbMountPath, p.localPathTxt))
					p.reloadStatus()
					p.reloadHistory()
				},
				OnError: func(err error) {
					if p.currentJob != h {
						return
					}
					p.currentJob = nil
					p.runBtn.Enable()
					p.nightlyBtn.Enable()
					p.cancelBtn.Hide()
					p.result.FailStart(err)
				},
			})
			p.currentJob = h
		})
		return nil
	})
}

func (p *pageSync) cancel() {
	if p.currentJob != nil {
		p.currentJob.Cancel()
	}
}

// orDash 空值显示占位。
func orDash(v string) string {
	if v == "" {
		return "未配置"
	}
	return v
}
