package appshell

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/widget"

	"github.com/zhangzhenyu91/j1toolkit/uvmp-toolkit/core-go/internal/ui"
)

const historyAllTools = "全部工具"

var historyStatusOptions = []struct {
	label string
	value string
}{
	{"全部状态", ""},
	{"排队中", string(ui.JobPending)},
	{"执行中", string(ui.JobRunning)},
	{"成功", string(ui.JobSuccess)},
	{"失败", string(ui.JobFailed)},
	{"已取消", string(ui.JobCancelled)},
	{"已暂停", "paused"},
}

// historyEntry 统一历史页的一条记录；appID/appName 来自六个业务工具入口。
type historyEntry struct {
	appID   string
	appName string
	summary string
	rec     ui.HistoryRecord
}

// historyFilter 仅描述客户端筛选条件，不参与后端查询。
type historyFilter struct {
	appID   string
	status  string
	keyword string
	date    string
}

// historyParamRestorer 由业务页按需实现；历史页不理解和修改具体业务表单。
type historyParamRestorer interface {
	RestoreParams(map[string]any)
}

// pageHistory 六个业务工具的独立统一历史页。
type pageHistory struct {
	shell *Shell
	root  fyne.CanvasObject

	toolFilter    *widget.Select
	statusFilter  *widget.Select
	keywordFilter *widget.Entry
	dateFilter    *widget.Entry

	list    *widget.List
	empty   *widget.Label
	count   *widget.Label
	alert   *fyne.Container
	openDir func(string)

	all      []historyEntry
	filtered []historyEntry
	loading  bool
	loadErrs []string
	updating bool
	reloadID atomic.Uint64
}

var _ Page = (*pageHistory)(nil)

func newPageHistory(s *Shell) *pageHistory {
	p := &pageHistory{shell: s, openDir: openPathVia(s)}

	toolOptions := []string{historyAllTools}
	for _, task := range homeTaskList() {
		toolOptions = append(toolOptions, task.name)
	}
	p.toolFilter = widget.NewSelect(toolOptions, nil)
	p.toolFilter.SetSelected(historyAllTools)
	statusOptions := make([]string, 0, len(historyStatusOptions))
	for _, option := range historyStatusOptions {
		statusOptions = append(statusOptions, option.label)
	}
	p.statusFilter = widget.NewSelect(statusOptions, nil)
	p.statusFilter.SetSelected(historyStatusOptions[0].label)

	p.keywordFilter = widget.NewEntry()
	p.keywordFilter.SetPlaceHolder("搜索工具、任务号、时间或结果摘要")
	p.dateFilter = widget.NewEntry()
	p.dateFilter.SetPlaceHolder("可选，YYYY-MM-DD")

	p.empty = muted("正在读取六个工具的历史记录…")
	p.count = hbLabel("", widget.LowImportance)
	p.list = widget.NewList(
		func() int { return len(p.filtered) },
		p.newHistoryRow,
		p.updateHistoryRow,
	)
	p.list.Hide()

	clearBtn := newMotionButton("清除筛选", p.resetFilters)
	clearBtn.Importance = widget.LowImportance
	refreshBtn := newMotionButton("刷新", p.reload)
	refreshBtn.Importance = widget.LowImportance
	filterCard := newCard("筛选",
		container.NewGridWithColumns(2,
			formRow("工具", p.toolFilter),
			formRow("状态", p.statusFilter),
		),
		container.NewGridWithColumns(2,
			formRow("关键词", p.keywordFilter),
			formRow("日期", p.dateFilter),
		),
		hrow(clearBtn, refreshBtn),
	)

	p.alert = container.NewVBox()
	p.alert.Hide()
	top := container.NewVBox(filterCard, p.alert, spacer(12))
	listHead := container.NewVBox(
		hrowGrow(secTitle("执行记录"), p.count),
		widget.NewSeparator(),
	)
	listBody := container.NewBorder(listHead, nil, nil, nil, container.NewStack(p.list, container.NewCenter(p.empty)))
	historyCard := cardSurface(listBody)
	p.root = container.NewBorder(top, nil, nil, nil, historyCard)

	p.toolFilter.OnChanged = func(string) { p.applyFilters() }
	p.statusFilter.OnChanged = func(string) { p.applyFilters() }
	p.keywordFilter.OnChanged = func(string) { p.applyFilters() }
	p.dateFilter.OnChanged = func(string) { p.applyFilters() }
	return p
}

func (p *pageHistory) Object() fyne.CanvasObject { return p.root }

// OnShow 每次恰好读取六个业务工具各一次；首页的轻量最近结果逻辑保持独立。
func (p *pageHistory) OnShow() { p.reload() }

func (p *pageHistory) reload() {
	id := p.reloadID.Add(1)
	runUI(func() {
		if id != p.reloadID.Load() {
			return
		}
		p.loading = true
		p.loadErrs = nil
		p.renderAlert()
		p.applyFilters()
	})
	p.shell.Async(func() error {
		ctx := context.Background()
		tasks := homeTaskList()
		records := make(map[string][]ui.HistoryRecord, len(tasks))
		errs := make([]string, 0)
		for _, task := range tasks {
			recs, err := p.shell.Backend.History(ctx, task.id)
			if err != nil {
				errs = append(errs, fmt.Sprintf("%s：%v", task.name, err))
				continue
			}
			records[task.id] = recs
		}
		merged := mergeHistory(tasks, records)
		runUI(func() {
			if id != p.reloadID.Load() {
				return
			}
			p.all = merged
			p.loading = false
			p.loadErrs = errs
			p.renderAlert()
			p.applyFilters()
		})
		return nil
	})
}

// mergeHistory 合并全部记录，不按首页的最近六条截断；Time 相同再按 JobID 倒序。
func mergeHistory(tasks []homeTask, records map[string][]ui.HistoryRecord) []historyEntry {
	total := 0
	for _, task := range tasks {
		total += len(records[task.id])
	}
	merged := make([]historyEntry, 0, total)
	for _, task := range tasks {
		for _, rec := range records[task.id] {
			merged = append(merged, historyEntry{
				appID:   task.id,
				appName: task.name,
				summary: homeResultSummary(task.id, rec),
				rec:     rec,
			})
		}
	}
	sort.SliceStable(merged, func(i, j int) bool {
		if merged[i].rec.Time == merged[j].rec.Time {
			return merged[i].rec.JobID > merged[j].rec.JobID
		}
		return merged[i].rec.Time > merged[j].rec.Time
	})
	return merged
}

// filterHistory 对已加载数据做工具、状态、关键词和可选日期筛选。
func filterHistory(entries []historyEntry, filter historyFilter) []historyEntry {
	filter.appID = strings.TrimSpace(filter.appID)
	filter.status = strings.TrimSpace(filter.status)
	filter.keyword = strings.ToLower(strings.TrimSpace(filter.keyword))
	filter.date = strings.TrimSpace(filter.date)
	if !validHistoryDate(filter.date) {
		return []historyEntry{}
	}

	filtered := make([]historyEntry, 0, len(entries))
	for _, entry := range entries {
		if filter.appID != "" && entry.appID != filter.appID {
			continue
		}
		if filter.status != "" && historyStatusKey(entry.rec) != filter.status {
			continue
		}
		if filter.date != "" && (len(entry.rec.Time) < len(filter.date) || entry.rec.Time[:len(filter.date)] != filter.date) {
			continue
		}
		if filter.keyword != "" && !strings.Contains(historySearchText(entry), filter.keyword) {
			continue
		}
		filtered = append(filtered, entry)
	}
	return filtered
}

func validHistoryDate(date string) bool {
	if date == "" {
		return true
	}
	parsed, err := time.Parse("2006-01-02", date)
	return err == nil && parsed.Format("2006-01-02") == date
}

func historyStatusKey(rec ui.HistoryRecord) string {
	if rec.Paused() {
		return "paused"
	}
	return string(rec.Status)
}

func historySearchText(entry historyEntry) string {
	status, _ := historyStatusStyle(entry.rec)
	return strings.ToLower(strings.Join([]string{
		entry.appID,
		entry.appName,
		entry.rec.JobID,
		entry.rec.Time,
		status,
		entry.summary,
		entry.rec.Error,
		entry.rec.Dir,
	}, "\n"))
}

func (p *pageHistory) currentFilter() historyFilter {
	filter := historyFilter{
		keyword: p.keywordFilter.Text,
		date:    p.dateFilter.Text,
	}
	if selected := p.toolFilter.Selected; selected != "" && selected != historyAllTools {
		for _, task := range homeTaskList() {
			if task.name == selected {
				filter.appID = task.id
				break
			}
		}
	}
	for _, option := range historyStatusOptions {
		if option.label == p.statusFilter.Selected {
			filter.status = option.value
			break
		}
	}
	return filter
}

func (p *pageHistory) applyFilters() {
	if p.updating {
		return
	}
	filter := p.currentFilter()
	p.filtered = filterHistory(p.all, filter)
	p.count.SetText(fmt.Sprintf("显示 %d / 共 %d 条", len(p.filtered), len(p.all)))

	switch {
	case p.loading && len(p.all) == 0:
		p.empty.SetText("正在读取六个工具的历史记录…")
	case !validHistoryDate(strings.TrimSpace(filter.date)):
		p.empty.SetText("日期格式不正确，请使用 YYYY-MM-DD，或留空表示不限日期。")
	case len(p.all) == 0 && len(p.loadErrs) == len(homeTaskList()):
		p.empty.SetText("历史记录加载失败，请检查客户端服务后点击“刷新”。")
	case len(p.all) == 0:
		p.empty.SetText("还没有执行记录。运行任意工具后，记录会出现在这里。")
	case len(p.filtered) == 0:
		p.empty.SetText("没有符合当前筛选条件的记录，可调整条件或点击“清除筛选”。")
	}

	if len(p.filtered) == 0 {
		p.list.Hide()
		p.empty.Show()
	} else {
		p.empty.Hide()
		p.list.Show()
	}
	p.list.Refresh()
	if p.root != nil {
		p.root.Refresh()
	}
}

func (p *pageHistory) resetFilters() {
	p.updating = true
	p.toolFilter.SetSelected(historyAllTools)
	p.statusFilter.SetSelected(historyStatusOptions[0].label)
	p.keywordFilter.SetText("")
	p.dateFilter.SetText("")
	p.updating = false
	p.applyFilters()
}

func (p *pageHistory) renderAlert() {
	if len(p.loadErrs) == 0 {
		p.alert.Hide()
		p.alert.Objects = nil
		return
	}
	prefix := "部分工具的历史记录读取失败，其他工具结果仍可使用："
	if len(p.loadErrs) == len(homeTaskList()) {
		prefix = "六个工具的历史记录均读取失败："
	}
	p.alert.Objects = []fyne.CanvasObject{statusBar("error", prefix+strings.Join(p.loadErrs, "；"))}
	p.alert.Show()
	p.alert.Refresh()
}

func (p *pageHistory) newHistoryRow() fyne.CanvasObject {
	app := widget.NewLabel("")
	app.TextStyle = fyne.TextStyle{Bold: true}
	app.Truncation = fyne.TextTruncateEllipsis
	when := widget.NewLabel("")
	when.Importance = widget.LowImportance
	status := widget.NewLabel("")
	status.TextStyle = fyne.TextStyle{Bold: true}
	summary := widget.NewLabel("")
	summary.Wrapping = fyne.TextWrapOff
	summary.Truncation = fyne.TextTruncateEllipsis

	logBtn := newMotionButton("查看日志", nil)
	logBtn.Importance = widget.LowImportance
	dirBtn := newMotionButton("打开目录", nil)
	dirBtn.Importance = widget.LowImportance
	restoreBtn := newMotionButton("参数回填", nil)
	restoreBtn.Importance = widget.LowImportance
	deleteBtn := newMotionButton("删除", nil)
	deleteBtn.Importance = widget.LowImportance

	left := container.NewVBox(container.NewGridWithColumns(3, app, when, status), summary)
	ops := container.NewHBox(logBtn, dirBtn, restoreBtn, deleteBtn)
	// 操作放在摘要下一行，避免窄窗下按钮挤压工具名称和结果。
	return container.NewVBox(left, ops)
}

func (p *pageHistory) updateHistoryRow(id widget.ListItemID, obj fyne.CanvasObject) {
	if id >= len(p.filtered) {
		return
	}
	entry := p.filtered[id]
	row := obj.(*fyne.Container)
	left := row.Objects[0].(*fyne.Container)
	ops := row.Objects[1].(*fyne.Container)
	head := left.Objects[0].(*fyne.Container)

	app := head.Objects[0].(*widget.Label)
	when := head.Objects[1].(*widget.Label)
	status := head.Objects[2].(*widget.Label)
	summary := left.Objects[1].(*widget.Label)
	app.SetText(entry.appName)
	when.SetText(entry.rec.Time)
	statusText, statusImportance := historyStatusStyle(entry.rec)
	status.Importance = statusImportance
	status.SetText(statusText)
	summary.SetText(entry.summary)

	logBtn := ops.Objects[0].(*widget.Button)
	dirBtn := ops.Objects[1].(*widget.Button)
	restoreBtn := ops.Objects[2].(*widget.Button)
	deleteBtn := ops.Objects[3].(*widget.Button)
	logBtn.OnTapped = func() {
		animateControlTap(logBtn)
		showJobLog(p.shell, entry.rec.JobID)
	}
	dirBtn.OnTapped = func() {
		animateControlTap(dirBtn)
		if entry.rec.Dir != "" {
			p.openDir(entry.rec.Dir)
		}
	}
	if entry.rec.Dir == "" {
		dirBtn.Hide()
	} else {
		dirBtn.Show()
	}
	restoreBtn.OnTapped = func() {
		animateControlTap(restoreBtn)
		p.restoreParams(entry)
	}
	if _, ok := p.shell.jobParams(entry.rec.JobID); ok {
		restoreBtn.Enable()
	} else {
		restoreBtn.Disable()
	}
	deleteBtn.OnTapped = func() {
		animateControlTap(deleteBtn)
		p.confirmDelete(entry)
	}
}

func (p *pageHistory) confirmDelete(entry historyEntry) {
	ConfirmDanger(p.shell.Win, "删除执行记录",
		fmt.Sprintf("将删除%s在 %s 的这条执行记录及其任务日志、参数快照。确定继续？", entry.appName, entry.rec.Time),
		"删除", func() {
			p.shell.Async(func() error {
				_, err := p.shell.Backend.DeleteHistory(context.Background(), entry.appID, entry.rec.JobID)
				if err != nil {
					return err
				}
				p.shell.deleteJobParams(entry.rec.JobID)
				p.reload()
				p.shell.Toast("已删除", ToastSuccess)
				return nil
			})
		})
}

// restoreParams 只传递参数快照；具体字段如何恢复完全由目标业务页决定。
func (p *pageHistory) restoreParams(entry historyEntry) {
	params, ok := p.shell.jobParams(entry.rec.JobID)
	if !ok {
		Warn(p.shell.Win, "无法回填参数", "这条记录没有可用的参数快照，不会根据旧历史推测参数。")
		return
	}
	p.shell.Navigate(entry.appID)
	page, ok := p.shell.pages[entry.appID]
	if !ok {
		Warn(p.shell.Win, "无法回填参数", "找不到目标工具页面。")
		return
	}
	restorer, ok := page.(historyParamRestorer)
	if !ok {
		Warn(p.shell.Win, "无法回填参数", "目标工具页面暂不支持参数回填，未修改任何业务表单。")
		return
	}
	restorer.RestoreParams(params)
	p.shell.Toast("已回填上次任务参数", ToastSuccess)
}
