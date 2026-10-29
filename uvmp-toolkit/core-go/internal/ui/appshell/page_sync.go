package appshell

import (
	"context"
	"fmt"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/widget"

	"github.com/zhangzhenyu91/j1toolkit/uvmp-toolkit/core-go/internal/ui"
)

// pageSync 树障/交跨统计表同步页（tree_sync / crossing_sync 共用骨架，契约同构）：
// 状态卡 / 同步设置 / 立即执行（按 U 盘指令、夜间兜底）/ 执行记录。
type pageSync struct {
	shell       *Shell
	appID       string
	title       string
	defFileName string
	root        fyne.CanvasObject

	statusKV   *kvCard
	cfgHint    *widget.Label
	localPath  *widget.Entry
	fileName   *widget.Entry
	nightly    *widget.Entry
	runBtn     *widget.Button
	nightlyBtn *widget.Button
	cancelBtn  *widget.Button
	log        *LogView
	doneInfo   *widget.Label
	history    *HistoryList
	currentJob *ui.JobHandle
}

func newPageSync(s *Shell, appID string) *pageSync {
	title, defFile := "树障同步", "检修一班树木统计表.xlsx"
	if appID == "crossing_sync" {
		title, defFile = "交跨同步", "检修一班交跨统计表.xlsx"
	}
	p := &pageSync{shell: s, appID: appID, title: title, defFileName: defFile}

	p.statusKV = newKVCard("状态",
		[2]string{"内网表", "-"},
		[2]string{"文件检查", "-"},
		[2]string{"U盘挂载", "-"},
		[2]string{"上次兜底", "-"},
		[2]string{"最近任务", "-"},
	)
	p.cfgHint = muted("")
	statusCard := newCard("状态", p.cfgHint, p.statusKV.box)

	p.localPath = widget.NewEntry()
	p.localPath.SetPlaceHolder("内网统计表 xlsx 完整路径")
	pickBtn := widget.NewButton("选择文件", p.pickFile)
	p.fileName = widget.NewEntry()
	p.fileName.SetPlaceHolder(defFile)
	p.nightly = widget.NewEntry()
	p.nightly.SetPlaceHolder("23:55")
	saveBtn := widget.NewButton("保存", p.save)
	saveBtn.Importance = widget.HighImportance
	syncCard := newCard("同步设置",
		hrowGrow(p.localPath, pickBtn),
		hrow(muted("上盘文件名"), p.fileName, muted("夜间兜底"), p.nightly, saveBtn),
		muted("服务端挂载 U 盘并下发指令后，客户端每分钟轮询执行：snapshot=内网表拷贝上盘，replace=U盘新表覆盖内网表（原文件自动备份为 .bak）；夜间兜底时刻无指令也拷贝内网表上盘。00:00 回写窗口不暂停轮询，replace 在服务端挂载后下一轮执行。"),
	)

	p.runBtn = widget.NewButtonWithIcon("立即执行一次（按 U 盘指令）", nil, func() { p.start(map[string]any{"tick": true}) })
	p.runBtn.Importance = widget.HighImportance
	p.nightlyBtn = widget.NewButtonWithIcon("执行一次夜间兜底", nil, func() { p.start(map[string]any{"nightly": true}) })
	p.cancelBtn = widget.NewButtonWithIcon("取消", nil, p.cancel)
	p.cancelBtn.Importance = widget.DangerImportance
	p.cancelBtn.Hide()
	p.log = NewLogView()
	p.doneInfo = muted("")
	p.doneInfo.Hide()
	runCard := newCard("立即执行",
		hrow(p.runBtn, p.nightlyBtn, p.cancelBtn),
		p.doneInfo,
		p.log.Root,
	)

	p.history = NewHistoryList("最近执行记录", false, func(r map[string]any) string {
		return ui.SyncSummary(ui.SyncRunResultFrom(r))
	})
	p.history.OnLog = func(rec ui.HistoryRecord) { showJobLog(s, rec.JobID) }
	p.history.OnDelete = func(rec ui.HistoryRecord) {
		ConfirmDanger(s.Win, "删除执行记录",
			fmt.Sprintf("将删除 %s 的这条执行记录及其任务日志。确定继续？", rec.Time),
			"删除", func() {
				s.Async(func() error {
					_, err := s.Backend.DeleteHistory(context.Background(), p.appID, rec.JobID)
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
					r, err := s.Backend.DeleteHistory(context.Background(), p.appID, "")
					if err == nil {
						p.reloadHistory()
						s.Toast(fmt.Sprintf("已清空 %d 条记录", r.Records), ToastSuccess)
					}
					return err
				})
			})
	}

	p.root = pageScroll(statusCard, syncCard, runCard, p.history.Root)
	return p
}

func (p *pageSync) Object() fyne.CanvasObject { return p.root }

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
			usb = "未检测到（服务端挂载后自动执行）"
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
			p.statusKV.SetValues(
				[2]string{"内网表", orDash(ss.LocalPath)},
				[2]string{"文件检查", fileCheck},
				[2]string{"U盘挂载", usb},
				[2]string{"上次兜底", stamp},
				[2]string{"最近任务", lastRun},
			)
			if ss.Configured() {
				p.cfgHint.SetText("")
			} else {
				p.cfgHint.SetText("未配置内网表路径，请先完成下方配置")
			}
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
		p.shell.Toast("请先选择内网表 xlsx 文件", ToastWarning)
		return
	}
	nt := strings.TrimSpace(p.nightly.Text)
	if !hhmmRe.MatchString(nt) {
		p.shell.Toast("夜间兜底时间格式应为 HH:MM，如 23:55", ToastWarning)
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

func (p *pageSync) start(params map[string]any) {
	if p.localPath.Text == "" {
		p.shell.Toast("尚未配置内网表路径——请先在上方「同步设置」选择文件并保存", ToastWarning)
		return
	}
	p.shell.Async(func() error {
		runUI(func() {
			p.log.Clear()
			p.doneInfo.Hide()
			p.runBtn.Disable()
			p.nightlyBtn.Disable()
			p.cancelBtn.Show()
			var h *ui.JobHandle
			h = p.shell.StartJob(p.appID, params, ui.JobHandler{
				OnLog: func(line string) { p.log.Append(line) },
				OnDone: func(job ui.Job) {
					if p.currentJob != h {
						return // 过期任务终态：不覆盖新一轮状态
					}
					p.currentJob = nil
					p.runBtn.Enable()
					p.nightlyBtn.Enable()
					p.cancelBtn.Hide()
					text := job.Error
					if text == "" {
						text = ui.SyncSummary(ui.SyncRunResultFrom(job.Result))
					}
					p.doneInfo.SetText(job.Status.CN() + "：" + text)
					p.doneInfo.Show()
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
					ShowToast(p.shell.Win, "启动失败："+err.Error(), ToastError)
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
