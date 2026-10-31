package appshell

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/test"
	"fyne.io/fyne/v2/widget"

	"github.com/zhangzhenyu91/j1toolkit/uvmp-toolkit/core-go/internal/ui"
)

func TestMergeHistorySortsAllRecords(t *testing.T) {
	tasks := homeTaskList()
	records := map[string][]ui.HistoryRecord{
		"pcd_export": {
			{JobID: "job-c", Time: "2026-10-29 09:00:00", Status: ui.JobSuccess},
			{JobID: "job-old", Time: "2026-10-27 09:00:00", Status: ui.JobFailed, Error: "导出失败"},
		},
		"course_autoplay": {
			{JobID: "job-z", Time: "2026-10-29 11:00:00", Status: ui.JobSuccess},
		},
		"quiz_search": {
			{JobID: "job-quiz", Time: "2026-10-28 09:00:00", Status: ui.JobSuccess},
		},
		"daily_sync": {
			{JobID: "job-y", Time: "2026-10-29 11:00:00", Status: ui.JobSuccess},
		},
		"tree_sync": {
			{JobID: "job-new", Time: "2026-10-29 12:00:00", Status: ui.JobSuccess},
		},
		"crossing_sync": {
			{JobID: "job-crossing", Time: "2026-10-29 08:00:00", Status: ui.JobCancelled},
		},
	}

	merged := mergeHistory(tasks, records)
	got := make([]string, 0, len(merged))
	for _, entry := range merged {
		got = append(got, entry.rec.JobID)
	}
	want := []string{"job-new", "job-z", "job-y", "job-c", "job-crossing", "job-quiz", "job-old"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("合并排序不符：got=%v want=%v", got, want)
	}
	if len(merged) != 7 {
		t.Fatalf("统一历史页不应按首页六条截断：%d", len(merged))
	}
	if merged[0].appID != "tree_sync" || merged[0].appName != "树障同步" {
		t.Fatalf("工具元数据未保留：%+v", merged[0])
	}
	if summary := merged[len(merged)-1].summary; summary != friendlyTaskError("导出失败") {
		t.Fatalf("失败摘要未沿用业务友好口径：%q", summary)
	}
}

func TestMergeHistoryStableWhenTimeAndJobIDEqual(t *testing.T) {
	tasks := []homeTask{{id: "first", name: "第一工具"}, {id: "second", name: "第二工具"}}
	rec := ui.HistoryRecord{JobID: "same-job", Time: "2026-10-29 10:00:00", Status: ui.JobSuccess}
	merged := mergeHistory(tasks, map[string][]ui.HistoryRecord{
		"first":  {rec},
		"second": {rec},
	})
	if len(merged) != 2 || merged[0].appID != "first" || merged[1].appID != "second" {
		t.Fatalf("完全相同的排序键应保持原顺序：%+v", merged)
	}
}

func TestFilterHistory(t *testing.T) {
	entries := []historyEntry{
		{
			appID: "pcd_export", appName: "轨迹派车单导出", summary: "导出完成：Alpha",
			rec: ui.HistoryRecord{JobID: "JOB-100", Time: "2026-10-29 08:00:00", Status: ui.JobSuccess},
		},
		{
			appID: "daily_sync", appName: "派车同步", summary: "导出完成：6 条",
			rec: ui.HistoryRecord{JobID: "job-101", Time: "2026-10-29 09:00:00", Status: ui.JobFailed, Error: "SSO 登录失败"},
		},
		{
			appID: "course_autoplay", appName: "学堂刷课", summary: "已暂停，可回到页面继续",
			rec: ui.HistoryRecord{JobID: "job-102", Time: "2026-10-28 09:00:00", Status: ui.JobRunning, Result: map[string]any{"paused": true}},
		},
		{
			appID: "tree_sync", appName: "树障同步", summary: "同步完成",
			rec: ui.HistoryRecord{JobID: "job-103", Time: "2026-10-27 09:00:00", Status: ui.JobCancelled},
		},
	}

	tests := []struct {
		name   string
		filter historyFilter
		want   []string
	}{
		{name: "默认保留全部", want: []string{"JOB-100", "job-101", "job-102", "job-103"}},
		{name: "工具", filter: historyFilter{appID: "pcd_export"}, want: []string{"JOB-100"}},
		{name: "状态", filter: historyFilter{status: string(ui.JobFailed)}, want: []string{"job-101"}},
		{name: "暂停优先于任务状态", filter: historyFilter{status: "paused"}, want: []string{"job-102"}},
		{name: "关键词忽略大小写", filter: historyFilter{keyword: "alpha"}, want: []string{"JOB-100"}},
		{name: "关键词搜索错误", filter: historyFilter{keyword: "sso 登录"}, want: []string{"job-101"}},
		{name: "关键词搜索任务号", filter: historyFilter{keyword: "JOB-102"}, want: []string{"job-102"}},
		{name: "日期", filter: historyFilter{date: "2026-10-29"}, want: []string{"JOB-100", "job-101"}},
		{
			name: "组合条件",
			filter: historyFilter{
				appID: "daily_sync", status: string(ui.JobFailed), keyword: "6 条", date: "2026-10-29",
			},
			want: []string{"job-101"},
		},
		{name: "无匹配", filter: historyFilter{appID: "quiz_search"}, want: []string{}},
		{name: "非法日期不匹配也不猜日期", filter: historyFilter{date: "2026-02-30"}, want: []string{}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			filtered := filterHistory(entries, tt.filter)
			got := make([]string, 0, len(filtered))
			for _, entry := range filtered {
				got = append(got, entry.rec.JobID)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("筛选结果不符：got=%v want=%v", got, tt.want)
			}
		})
	}
	if len(entries) != 4 || entries[0].rec.JobID != "JOB-100" {
		t.Fatalf("筛选不应修改已加载数据：%+v", entries)
	}
}

func TestValidHistoryDate(t *testing.T) {
	tests := []struct {
		date string
		want bool
	}{
		{date: "", want: true},
		{date: "2026-10-29", want: true},
		{date: "2028-02-29", want: true},
		{date: "2026-02-29", want: false},
		{date: "2026-13-01", want: false},
		{date: "2026-1-01", want: false},
		{date: "20261029", want: false},
	}
	for _, tt := range tests {
		if got := validHistoryDate(tt.date); got != tt.want {
			t.Errorf("validHistoryDate(%q)=%v，期望 %v", tt.date, got, tt.want)
		}
	}
}

func TestPageHistoryActionsUseAppButtonsAndRebind(t *testing.T) {
	a := test.NewApp()
	t.Cleanup(a.Quit)
	s := newTestShell(t, a, ui.NewFakeBackend(), Options{DisableTray: true, DisableScheduler: true})
	p := s.pages["history"].(*pageHistory)
	first := historyEntry{
		appID: "pcd_export", appName: "轨迹派车单导出", summary: "第一次执行",
		rec: ui.HistoryRecord{JobID: "job-page-first", Time: "2026-10-31 09:00:00", Status: ui.JobSuccess, Dir: "D:/page-first"},
	}
	p.filtered = []historyEntry{first}
	row := newPageHistoryRowFixture(t, p)
	opened := ""
	p.openDir = func(path string) { opened = path }
	row.dir.Tapped(nil)
	if opened != "D:/page-first" {
		t.Fatalf("目录操作行为改变：%q", opened)
	}
	if !row.restore.Disabled() {
		t.Fatal("无参数快照时参数回填应保持禁用")
	}

	window := test.NewWindow(newAppTooltipHost(row.scope))
	window.Resize(fyne.NewSize(720, 180))
	t.Cleanup(window.Close)
	showAppButtonTooltip(t, window, row.log)
	panel := row.log.tooltip.host.panel
	second := historyEntry{
		appID: "tree_sync", appName: "树障同步", summary: "第二次执行",
		rec: ui.HistoryRecord{JobID: "job-page-second", Time: "2026-10-31 10:00:00", Status: ui.JobFailed},
	}
	s.saveJobParams(second.rec.JobID, second.appID, map[string]any{"source": "second"})
	p.filtered = []historyEntry{second}
	p.updateHistoryRow(0, row.scope)
	if row.log.tooltip.host != nil || panel.Visible() {
		t.Fatal("独立历史行重绑定前应清理旧 tooltip")
	}
	if row.dirScope.Visible() {
		t.Fatal("无目录记录应通过 scope 隐藏目录操作")
	}
	if row.restore.Disabled() {
		t.Fatal("新记录存在参数快照时应重新启用参数回填")
	}
	for name, button := range map[string]*appButton{
		"日志": row.log, "目录": row.dir, "参数回填": row.restore, "删除": row.delete,
	} {
		label := button.AccessibilityLabel()
		for _, want := range []string{second.appName, second.rec.JobID, second.rec.Time} {
			if !strings.Contains(label, want) {
				t.Fatalf("%s按钮名称未随工具/任务/时间重绑，缺少 %q：%q", name, want, label)
			}
		}
		if strings.Contains(label, first.rec.JobID) || button.tooltip == nil || button.tooltip.label != label {
			t.Fatalf("%s按钮仍残留旧记录或 tooltip 未重绑：%q", name, label)
		}
	}
}

func TestPageHistoryFilterRefreshClearsTooltip(t *testing.T) {
	a := test.NewApp()
	t.Cleanup(a.Quit)
	s := newTestShell(t, a, ui.NewFakeBackend(), Options{DisableTray: true, DisableScheduler: true})
	s.Navigate("history")
	p := s.pages["history"].(*pageHistory)
	waitShellCond(t, "独立历史页加载完成", func() bool {
		loading := false
		runUI(func() { loading = p.loading })
		return !loading
	})
	runUI(func() {
		p.all = []historyEntry{{
			appID: "pcd_export", appName: "轨迹派车单导出", summary: "可筛选记录",
			rec: ui.HistoryRecord{JobID: "job-filter-tooltip", Time: "2026-10-31 09:00:00", Status: ui.JobSuccess},
		}}
		p.applyFilters()
	})
	var button *appButton
	waitShellCond(t, "独立历史页真实行按钮", func() bool {
		button = findHistoryActionButton(p.root, "查看日志")
		return button != nil
	})
	showAppButtonTooltip(t, s.Win, button)
	panel := button.tooltip.host.panel

	runUI(func() { p.keywordFilter.SetText("绝对不匹配的关键词") })
	if button.tooltip.host != nil || panel.Visible() {
		t.Fatal("筛选刷新后不得遗留旧行 tooltip")
	}
	time.Sleep(700 * time.Millisecond)
	if panel.Visible() {
		t.Fatal("筛选刷新后旧 tooltip 不应复活")
	}
}

func TestPageHistoryFilterActionsUseAppButtons(t *testing.T) {
	a := test.NewApp()
	t.Cleanup(a.Quit)
	s := newTestShell(t, a, ui.NewFakeBackend(), Options{DisableTray: true, DisableScheduler: true})
	p := s.pages["history"].(*pageHistory)
	for _, tt := range []struct {
		label string
		icon  string
	}{
		{label: "清除筛选", icon: "reset.svg"},
		{label: "刷新", icon: "refresh.svg"},
	} {
		button := findHistoryActionButton(p.root, tt.label)
		if button == nil {
			t.Fatalf("%s 应迁移为 appButton", tt.label)
		}
		if button.Importance != widget.LowImportance || button.Icon == nil || button.Icon.Name() != tt.icon {
			t.Fatalf("%s 按钮样式或图标不符：%v / %+v", tt.label, button.Importance, button.Icon)
		}
		if button.tooltip == nil || button.tooltip.label != button.AccessibilityLabel() {
			t.Fatalf("%s 按钮缺少一致的中文 tooltip", tt.label)
		}
	}
}

type pageHistoryRowFixture struct {
	scope    *appTooltipScope
	row      *fyne.Container
	dirScope *appTooltipScope
	log      *appButton
	dir      *appButton
	restore  *appButton
	delete   *appButton
}

func newPageHistoryRowFixture(t *testing.T, p *pageHistory) pageHistoryRowFixture {
	t.Helper()
	object := p.list.CreateItem()
	scope, ok := object.(*appTooltipScope)
	if !ok {
		t.Fatalf("独立历史 CreateItem 应返回 appTooltipScope 包装行，实际 %T", object)
	}
	p.updateHistoryRow(0, scope)
	row := scope.content
	if len(row.Objects) != 2 {
		t.Fatalf("独立历史行结构不完整：%d", len(row.Objects))
	}
	ops, ok := row.Objects[1].(*fyne.Container)
	if !ok || len(ops.Objects) != 4 {
		t.Fatalf("独立历史应保留日志、目录、回填、删除四个操作，实际 %T", row.Objects[1])
	}
	log, ok := ops.Objects[0].(*appButton)
	if !ok {
		t.Fatalf("日志操作应使用 appButton，实际 %T", ops.Objects[0])
	}
	dirScope, ok := ops.Objects[1].(*appTooltipScope)
	if !ok || len(dirScope.content.Objects) != 1 {
		t.Fatalf("目录操作应由 appTooltipScope 管理显隐，实际 %T", ops.Objects[1])
	}
	dir, ok := dirScope.content.Objects[0].(*appButton)
	if !ok {
		t.Fatalf("目录操作应使用 appButton，实际 %T", dirScope.content.Objects[0])
	}
	restore, ok := ops.Objects[2].(*appButton)
	if !ok {
		t.Fatalf("参数回填应使用 appButton，实际 %T", ops.Objects[2])
	}
	deleteButton, ok := ops.Objects[3].(*appButton)
	if !ok {
		t.Fatalf("删除操作应使用 appButton，实际 %T", ops.Objects[3])
	}
	for name, button := range map[string]*appButton{"日志": log, "目录": dir, "删除": deleteButton} {
		assertCompactHistoryButton(t, name, button)
	}
	requireDangerTrashIcon(t, deleteButton)
	if restore.Text != "参数回填" || restore.Icon == nil || restore.Importance != widget.LowImportance || restore.tooltip == nil {
		t.Fatalf("参数回填应使用 LowImportance 图标文字按钮及 tooltip：%+v", restore)
	}
	return pageHistoryRowFixture{scope: scope, row: row, dirScope: dirScope, log: log, dir: dir, restore: restore, delete: deleteButton}
}

// ---------------------------------------------------------------- Task7 常规/最大化筛选布局

func TestPageHistoryFilterGridReflowsWithoutRebuildingControls(t *testing.T) {
	counter := &task7CountingBackend{Backend: ui.NewFakeBackend()}
	a := test.NewApp()
	t.Cleanup(a.Quit)
	s := newTestShell(t, a, counter, Options{DisableTray: true, DisableScheduler: true})
	p := s.pages["history"].(*pageHistory)
	s.Navigate("history")
	waitShellCond(t, "独立历史页加载完成", func() bool {
		loading := false
		runUI(func() { loading = p.loading })
		return !loading
	})
	mode := task7RequireWindowMode(t, p)

	if _, ok := p.root.(*container.Scroll); ok {
		t.Fatal("独立历史页应保持固定筛选 + 列表视口，不新增整页滚动")
	}
	runUI(func() {
		if len(p.filtered) == 0 {
			if !p.empty.Visible() {
				t.Fatal("无记录时应保留列表视口的空态")
			}
		} else if !p.list.Visible() {
			t.Fatal("历史列表视口应保持可见")
		}
		for name, target := range map[string]fyne.CanvasObject{
			"工具": p.toolFilter, "状态": p.statusFilter, "关键词": p.keywordFilter, "日期": p.dateFilter,
		} {
			if !pageCanvasObjectEffectivelyVisible(p.root, target) {
				t.Fatalf("常规模式筛选条件不可见：%s", name)
			}
		}
	})

	task7ResizePage(t, p.root, 1080, 700)
	tool := task7RequireOffset(t, p.root, p.toolFilter)
	status := task7RequireOffset(t, p.root, p.statusFilter)
	keyword := task7RequireOffset(t, p.root, p.keywordFilter)
	date := task7RequireOffset(t, p.root, p.dateFilter)
	if tool.Y != status.Y || tool.X == status.X {
		t.Fatalf("常规模式工具/状态应同行两列：tool=%v status=%v", tool, status)
	}
	if keyword.Y <= tool.Y || keyword.Y != date.Y || keyword.X == date.X {
		t.Fatalf("常规模式关键词/日期应为第二行两列：keyword=%v date=%v tool=%v", keyword, date, tool)
	}

	// 普通历史在 668×458 窄宽下仍保持 2×2，不退化为单列。
	task7ResizePage(t, p.root, 668, 458)
	tool = task7RequireOffset(t, p.root, p.toolFilter)
	status = task7RequireOffset(t, p.root, p.statusFilter)
	keyword = task7RequireOffset(t, p.root, p.keywordFilter)
	date = task7RequireOffset(t, p.root, p.dateFilter)
	if tool.Y != status.Y || tool.X == status.X {
		t.Fatalf("668×458 常规历史应保持 2×2 第一行：tool=%v status=%v", tool, status)
	}
	if keyword.Y <= tool.Y || keyword.Y != date.Y || keyword.X == date.X {
		t.Fatalf("668×458 常规历史应保持 2×2 第二行：keyword=%v date=%v", keyword, date)
	}

	toolWidget := p.toolFilter
	statusWidget := p.statusFilter
	keywordWidget := p.keywordFilter
	dateWidget := p.dateFilter
	toolChanged := p.toolFilter.OnChanged
	statusChanged := p.statusFilter.OnChanged
	keywordChanged := p.keywordFilter.OnChanged
	dateChanged := p.dateFilter.OnChanged
	if toolChanged == nil || statusChanged == nil || keywordChanged == nil || dateChanged == nil {
		t.Fatal("筛选回调不应为空")
	}
	runUI(func() {
		p.keywordFilter.SetText("布局测试关键词")
	})
	before := len(p.filtered)

	counter.calls.Store(0)
	runUI(func() { mode.SetMaximized(true) })
	task7AssertNoBackendCalls(t, counter, "历史页最大化")
	counter.calls.Store(0)
	runUI(func() { mode.SetMaximized(false) })
	task7AssertNoBackendCalls(t, counter, "历史页还原")
	runUI(func() { mode.SetMaximized(true) })
	if p.toolFilter != toolWidget || p.statusFilter != statusWidget || p.keywordFilter != keywordWidget || p.dateFilter != dateWidget {
		t.Fatal("最大化重排复制或替换了筛选控件")
	}
	if p.toolFilter.OnChanged == nil || p.keywordFilter.OnChanged == nil {
		t.Fatal("最大化重排丢失筛选回调")
	}
	runUI(func() {
		for name, target := range map[string]fyne.CanvasObject{
			"工具": p.toolFilter, "状态": p.statusFilter, "关键词": p.keywordFilter, "日期": p.dateFilter,
		} {
			if !pageCanvasObjectEffectivelyVisible(p.root, target) {
				t.Fatalf("最大化不得隐藏筛选条件：%s", name)
			}
		}
		if len(p.filtered) == 0 {
			if !p.empty.Visible() {
				t.Fatal("最大化不应影响列表视口的空态")
			}
		} else if !p.list.Visible() {
			t.Fatal("最大化不应影响列表视口")
		}
	})
	task7ResizePage(t, p.root, 1200, 700)
	tool = task7RequireOffset(t, p.root, p.toolFilter)
	status = task7RequireOffset(t, p.root, p.statusFilter)
	keyword = task7RequireOffset(t, p.root, p.keywordFilter)
	date = task7RequireOffset(t, p.root, p.dateFilter)
	if tool.Y != keyword.Y || keyword.Y != status.Y || status.Y != date.Y {
		t.Fatalf("最大化筛选应重排为单行：tool=%v status=%v keyword=%v date=%v", tool, status, keyword, date)
	}
	if !(tool.X < status.X && status.X < keyword.X && keyword.X < date.X) {
		t.Fatalf("最大化筛选应按工具/状态/关键词/日期从左到右排列：%v %v %v %v", tool, status, keyword, date)
	}

	// 668×458：最大化四列筛选必须退化为单列，且整张筛选卡仍在固定视口内。
	task7ResizePage(t, p.root, 668, 458)
	tool = task7RequireOffset(t, p.root, p.toolFilter)
	status = task7RequireOffset(t, p.root, p.statusFilter)
	keyword = task7RequireOffset(t, p.root, p.keywordFilter)
	date = task7RequireOffset(t, p.root, p.dateFilter)
	if tool.X != status.X || status.X != keyword.X || keyword.X != date.X {
		t.Fatalf("668×458 最大化筛选应退化为单列：tool=%v status=%v keyword=%v date=%v", tool, status, keyword, date)
	}
	if !(tool.Y < status.Y && status.Y < keyword.Y && keyword.Y < date.Y) {
		t.Fatalf("668×458 最大化筛选应按工具/状态/关键词/日期纵向排列：%v %v %v %v", tool, status, keyword, date)
	}
	grid := task7RequireOffset(t, p.root, p.filterGrid)
	if bottom := grid.Y + p.filterGrid.Size().Height; bottom > 458 {
		t.Fatalf("668×458 最大化筛选卡超出固定视口：bottom=%.1f", bottom)
	}

	if len(p.filtered) != before {
		t.Fatalf("重排不应改变筛选结果：%d → %d", before, len(p.filtered))
	}
	runUI(func() {
		if p.keywordFilter.Text != "布局测试关键词" {
			t.Fatalf("重排丢失筛选输入：%q", p.keywordFilter.Text)
		}
		if findHistoryActionButton(p.root, "清除筛选") == nil || findHistoryActionButton(p.root, "刷新") == nil {
			t.Fatal("重排后筛选操作按钮丢失")
		}
	})

	runUI(func() { mode.SetMaximized(false) })
	task7ResizePage(t, p.root, 1080, 700)
	tool = task7RequireOffset(t, p.root, p.toolFilter)
	status = task7RequireOffset(t, p.root, p.statusFilter)
	keyword = task7RequireOffset(t, p.root, p.keywordFilter)
	if tool.Y != status.Y || keyword.Y == tool.Y {
		t.Fatalf("还原后应恢复 2×2 筛选：tool=%v status=%v keyword=%v", tool, status, keyword)
	}
}
