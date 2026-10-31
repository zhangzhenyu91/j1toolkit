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

// pageGridLayout 在同一批已建对象上按列重排（窗口最大化用）：跳过隐藏对象，
// 前 fullWidth 个可见对象整行展示，其余按行优先分列、列宽均分、行高取该行最高卡片。
// split>0 时改用业务页双栏：前 split 个可见对象堆左列，其余堆右列。
// 页面只替换容器 Layout 字段，不重建控件，未保存输入与进行中任务随对象保留。
type pageGridLayout struct {
	columns       int
	gap           float32
	fullWidth     int
	split         int
	breakWidth    float32 // >0 且可用宽度更窄时退化为单列（最大化窄屏可达性）
	measuredWidth float32 // 最近一次 Layout 的宽度；MinSize 用它判断是否退化
}

func (l *pageGridLayout) columnCount() int {
	if l.columns < 1 {
		return 1
	}
	return l.columns
}

func (l *pageGridLayout) narrow(width float32) bool {
	return l.breakWidth > 0 && width > 0 && width < l.breakWidth
}

func (l *pageGridLayout) MinSize(objects []fyne.CanvasObject) fyne.Size {
	visible := visibleHomeObjects(objects)
	if len(visible) == 0 {
		return fyne.Size{}
	}
	if l.narrow(l.measuredWidth) {
		return gridStackSize(visible, l.gap)
	}
	if l.split > 0 && l.split < len(visible) {
		leftSize := gridStackSize(visible[:l.split], l.gap)
		rightSize := gridStackSize(visible[l.split:], l.gap)
		// 最小宽度只取单列最宽卡：视口窄于 breakWidth 时内容仍可收缩，由 Layout 退化为单列。
		return fyne.NewSize(fyne.Max(leftSize.Width, rightSize.Width), fyne.Max(leftSize.Height, rightSize.Height))
	}
	full := l.fullWidth
	if full < 0 {
		full = 0
	}
	if full > len(visible) {
		full = len(visible)
	}
	width, height := float32(0), float32(0)
	rows := 0
	for _, obj := range visible[:full] {
		min := obj.MinSize()
		width = fyne.Max(width, min.Width)
		if rows > 0 {
			height += l.gap
		}
		height += min.Height
		rows++
	}
	rest := visible[full:]
	if len(rest) == 0 {
		return fyne.NewSize(width, height)
	}
	cols := l.columnCount()
	colMin := make([]float32, cols)
	rowCount := (len(rest) + cols - 1) / cols
	rowMin := make([]float32, rowCount)
	for i, obj := range rest {
		min := obj.MinSize()
		colMin[i%cols] = fyne.Max(colMin[i%cols], min.Width)
		rowMin[i/cols] = fyne.Max(rowMin[i/cols], min.Height)
	}
	used := cols
	if len(rest) < used {
		used = len(rest)
	}
	gridWidth := float32(0)
	for c := 0; c < used; c++ {
		gridWidth = fyne.Max(gridWidth, colMin[c])
	}
	width = fyne.Max(width, gridWidth)
	for _, h := range rowMin {
		if rows > 0 {
			height += l.gap
		}
		height += h
		rows++
	}
	return fyne.NewSize(width, height)
}

func (l *pageGridLayout) Layout(objects []fyne.CanvasObject, size fyne.Size) {
	l.measuredWidth = size.Width
	visible := visibleHomeObjects(objects)
	if len(visible) == 0 {
		return
	}
	if l.narrow(size.Width) {
		gridStackCards(visible, 0, size.Width, l.gap)
		return
	}
	if l.split > 0 && l.split < len(visible) {
		rightWidth := (size.Width - l.gap) / 2
		leftWidth := size.Width - l.gap - rightWidth
		if leftWidth < 0 || rightWidth < 0 {
			leftWidth, rightWidth = size.Width, size.Width
		}
		gridStackCards(visible[:l.split], 0, leftWidth, l.gap)
		gridStackCards(visible[l.split:], leftWidth+l.gap, rightWidth, l.gap)
		return
	}
	full := l.fullWidth
	if full < 0 {
		full = 0
	}
	if full > len(visible) {
		full = len(visible)
	}
	y := float32(0)
	for _, obj := range visible[:full] {
		min := obj.MinSize()
		obj.Move(fyne.NewPos(0, y))
		obj.Resize(fyne.NewSize(size.Width, min.Height))
		y += min.Height + l.gap
	}
	rest := visible[full:]
	if len(rest) == 0 {
		return
	}
	cols := l.columnCount()
	used := cols
	if len(rest) < used {
		used = len(rest)
	}
	cellWidth := size.Width
	if used > 1 {
		cellWidth = (size.Width - l.gap*float32(used-1)) / float32(used)
	}
	if cellWidth < 0 {
		cellWidth = 0
	}
	rowCount := (len(rest) + cols - 1) / cols
	for row := 0; row < rowCount; row++ {
		rowHeight := float32(0)
		for col := 0; col < cols; col++ {
			i := row*cols + col
			if i >= len(rest) {
				break
			}
			rowHeight = fyne.Max(rowHeight, rest[i].MinSize().Height)
		}
		for col := 0; col < cols; col++ {
			i := row*cols + col
			if i >= len(rest) {
				break
			}
			obj := rest[i]
			obj.Move(fyne.NewPos(float32(col)*(cellWidth+l.gap), y))
			obj.Resize(fyne.NewSize(cellWidth, rowHeight))
		}
		y += rowHeight + l.gap
	}
}

// gridStackSize 单列堆叠的最小尺寸（pageGridLayout 双栏分支自用，不依赖其它页面助手）。
func gridStackSize(objs []fyne.CanvasObject, gap float32) fyne.Size {
	width, height := float32(0), float32(0)
	for i, obj := range objs {
		if i > 0 {
			height += gap
		}
		min := obj.MinSize()
		width = fyne.Max(width, min.Width)
		height += min.Height
	}
	return fyne.NewSize(width, height)
}

// gridStackCards 把一组卡片按最小高度纵向堆到指定列宽。
func gridStackCards(objs []fyne.CanvasObject, x, width, gap float32) {
	y := float32(0)
	for i, obj := range objs {
		if i > 0 {
			y += gap
		}
		height := obj.MinSize().Height
		obj.Move(fyne.NewPos(x, y))
		obj.Resize(fyne.NewSize(width, height))
		y += height
	}
}

// pageDaily 派车同步（daily_sync）：每日自动导出 / U 盘保存位置 / 立即执行一次 / 执行记录。
// 运行状态与完成摘要常驻结果区，技术日志收进「运行日志」折叠（失败自动展开）。
type pageDaily struct {
	shell        *Shell
	root         fyne.CanvasObject
	scroll       *container.Scroll
	content      *fyne.Container
	historyScope *appTooltipScope

	statusKV     *kvCard
	dueLabel     *widget.Label
	usbMount     *widget.Label
	compactRow   *fyne.Container
	settingsNote *widget.Label
	runNote      *widget.Label
	schEnable    *enterpriseCheck
	schTime      *widget.Entry
	usbLabel     *widget.Entry
	usbFallback  *widget.Entry
	runDate      *widget.Entry
	startBtn     *appButton
	cancelBtn    *appButton
	progress     *progressRow
	result       *taskResult
	openDirBtn   *appButton // 结果区产物按钮（历史用例/外部断言共用）
	history      *HistoryList
	driver       string
	formLoaded   bool
	currentJob   *ui.JobHandle
	maximized    bool
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
	p.usbMount = stateLabel("U 盘位置：-", widget.LowImportance)
	// 常规只露到期与 U 盘两列紧凑状态，最大化只露四行完整状态；两组互斥，避免同卡叠放。
	p.compactRow = container.NewGridWithColumns(2, p.dueLabel, p.usbMount)
	statusCard := cardSurface(container.NewVBox(secTitle("运行状态"), p.compactRow, p.statusKV.box))

	p.schEnable = newEnterpriseCheck("每天自动导出", nil)
	p.schEnable.SetChecked(true)
	p.schTime = widget.NewEntry()
	p.schTime.SetPlaceHolder("09:15")
	schSave := newAppButton("保存设置", "save", widget.LowImportance, p.saveSchedule)
	p.usbLabel = widget.NewEntry()
	p.usbLabel.SetPlaceHolder("GLKVM")
	p.usbFallback = widget.NewEntry()
	p.usbFallback.SetPlaceHolder("U 盘不可用时的备用保存位置（留空则导出失败）")
	usbSave := newAppButton("保存设置", "save", widget.LowImportance, p.saveUSB)
	usbDetect := newAppButton("重新查找 U 盘", "refresh", widget.MediumImportance, p.OnShow)
	p.settingsNote = muted("到点自动执行，关机错过会在开机后补跑；派车单默认保存到 U 盘，找不到时可保存到备用位置。")
	settingsCard := newCard("定时与 U 盘设置",
		hrow(p.schEnable, hbLabel("导出时间", widget.LowImportance), newMinBox(fyne.NewSize(100, 0), p.schTime), schSave),
		hrow(hbLabel("U 盘名称", widget.LowImportance), newMinBox(fyne.NewSize(130, 0), p.usbLabel), usbSave, usbDetect),
		p.usbFallback,
		p.settingsNote,
	)

	p.runDate = widget.NewEntry()
	p.runDate.SetPlaceHolder("导出日期（留空为今天）")
	p.startBtn = newAppButton("立即执行一次", "play", widget.HighImportance, p.startRun)
	p.cancelBtn = newAppButton("取消执行", "cancel", widget.LowImportance, p.cancel)
	p.cancelBtn.Hide()
	p.progress = newProgressRow()
	p.result = newTaskResult(openPathVia(s), s)
	p.openDirBtn = p.result.artifactBtn
	p.runNote = muted("选择要导出的日期，留空则导出今天；完成后可直接打开保存位置。")
	runCard := newCard("立即执行一次",
		hrow(newMinBox(fyne.NewSize(220, 0), p.runDate), p.startBtn, p.cancelBtn),
		p.runNote,
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

	p.historyScope = newAppTooltipScope(p.history.Root.(*fyne.Container))
	p.scroll = pageScroll(statusCard, settingsCard, runCard, p.result.LogRoot, p.historyScope)
	p.content = p.scroll.Content.(*fyne.Container)
	p.root = p.scroll
	p.applyWindowMode(false)
	return p
}

func (p *pageDaily) Object() fyne.CanvasObject { return p.root }

var _ windowModePage = (*pageDaily)(nil)

// SetMaximized 只切换预建内容的显隐与列布局：不重建页面、不清输入/任务、不访问 Backend。
func (p *pageDaily) SetMaximized(maximized bool) {
	if p.maximized == maximized {
		return
	}
	runUI(func() {
		p.maximized = maximized
		p.applyWindowMode(maximized)
	})
}

func (p *pageDaily) applyWindowMode(maximized bool) {
	if maximized {
		p.compactRow.Hide()
		p.statusKV.box.Show()
		p.settingsNote.Show()
		p.runNote.Show()
		p.historyScope.showForShell(p.shell)
		p.content.Layout = &pageGridLayout{
			gap: cardGap, split: 2, breakWidth: 940,
			measuredWidth: p.content.Size().Width,
		}
	} else {
		p.compactRow.Show()
		p.statusKV.box.Hide()
		p.settingsNote.Hide()
		p.runNote.Hide()
		p.historyScope.hideForShell(p.shell)
		p.content.Layout = pageCardLayout{gap: cardGap}
	}
	p.content.Refresh()
	p.scroll.Refresh()
}

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
