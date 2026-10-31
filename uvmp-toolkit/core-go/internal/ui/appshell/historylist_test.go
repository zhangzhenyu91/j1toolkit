package appshell

import (
	"strings"
	"testing"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/test"
	"fyne.io/fyne/v2/widget"

	"github.com/zhangzhenyu91/j1toolkit/uvmp-toolkit/core-go/internal/ui"
)

func TestHistoryListCompactActionsStayVisibleAndCentered(t *testing.T) {
	a := test.NewApp()
	t.Cleanup(a.Quit)
	a.Settings().SetTheme(NewTheme())
	h := NewHistoryList("最近执行记录", true, nil)
	h.records = []ui.HistoryRecord{{
		JobID: "job-long", Time: "2026-10-31 09:00:00", Status: ui.JobFailed,
		Error: strings.Repeat("很长的执行结果摘要", 30), Dir: "D:/output",
	}}
	row := newHistoryListRowFixture(t, h)

	for name, button := range map[string]*appButton{
		"日志": row.log, "目录": row.dir, "删除": row.delete,
	} {
		assertCompactHistoryButton(t, name, button)
		if !strings.Contains(button.AccessibilityLabel(), "job-long") ||
			!strings.Contains(button.AccessibilityLabel(), "2026-10-31 09:00:00") {
			t.Fatalf("%s按钮的可访问名称未绑定任务号和时间：%q", name, button.AccessibilityLabel())
		}
	}
	requireDangerTrashIcon(t, row.delete)

	row.row.Resize(fyne.NewSize(560, 120))
	if row.ops.Size().Height != row.ops.MinSize().Height {
		t.Fatalf("操作组不应被 Border 右槽拉伸：got=%v min=%v", row.ops.Size(), row.ops.MinSize())
	}
	gotCenter := row.ops.Position().Y + row.ops.Size().Height/2
	if gotCenter < 59.5 || gotCenter > 60.5 {
		t.Fatalf("操作组未垂直居中：center=%.1f rowCenter=60", gotCenter)
	}
	if right := row.ops.Position().X + row.ops.Size().Width; right > 560 {
		t.Fatalf("长摘要下操作组被挤出视口：right=%.1f width=560", right)
	}
}

func TestHistoryListRebindResetsTooltipAndActions(t *testing.T) {
	a := test.NewApp()
	t.Cleanup(a.Quit)
	a.Settings().SetTheme(NewTheme())
	h := NewHistoryList("最近执行记录", true, nil)
	first := ui.HistoryRecord{JobID: "job-first", Time: "2026-10-31 09:00:00", Status: ui.JobSuccess, Dir: "D:/first"}
	h.records = []ui.HistoryRecord{first}
	row := newHistoryListRowFixture(t, h)
	window := test.NewWindow(newAppTooltipHost(row.scope))
	window.Resize(fyne.NewSize(640, 180))
	t.Cleanup(window.Close)

	var logged, opened, deleted string
	h.OnLog = func(rec ui.HistoryRecord) { logged = rec.JobID }
	h.OnOpenDir = func(rec ui.HistoryRecord) { opened = rec.Dir }
	h.OnDelete = func(rec ui.HistoryRecord) { deleted = rec.JobID }
	row.log.Tapped(nil)
	row.dir.Tapped(nil)
	row.delete.Tapped(nil)
	if logged != "job-first" || opened != "D:/first" || deleted != "job-first" {
		t.Fatalf("首次记录操作回调不符：log=%q dir=%q delete=%q", logged, opened, deleted)
	}

	showAppButtonTooltip(t, window, row.log)
	panel := row.log.tooltip.host.panel
	second := ui.HistoryRecord{JobID: "job-second", Time: "2026-10-31 10:00:00", Status: ui.JobFailed}
	h.records = []ui.HistoryRecord{second}
	h.list.UpdateItem(0, row.scope)
	if row.log.tooltip.host != nil || panel.Visible() {
		t.Fatal("行重绑定前应清理旧记录 tooltip")
	}
	if row.dirScope.Visible() {
		t.Fatal("无目录记录必须通过目录 scope 隐藏操作")
	}
	for name, button := range map[string]*appButton{
		"日志": row.log, "目录": row.dir, "删除": row.delete,
	} {
		label := button.AccessibilityLabel()
		if !strings.Contains(label, "job-second") || !strings.Contains(label, second.Time) || strings.Contains(label, "job-first") {
			t.Fatalf("%s按钮未随记录重绑中文名称：%q", name, label)
		}
		if button.tooltip == nil || button.tooltip.label != label {
			t.Fatalf("%s按钮 tooltip 未随记录重绑：%q", name, label)
		}
	}
	row.log.Tapped(nil)
	row.delete.Tapped(nil)
	if logged != "job-second" || deleted != "job-second" {
		t.Fatalf("复用行仍回调旧记录：log=%q delete=%q", logged, deleted)
	}

	second.Dir = "D:/second"
	h.records = []ui.HistoryRecord{second}
	h.list.UpdateItem(0, row.scope)
	if !row.dirScope.Visible() {
		t.Fatal("目录重新存在时应通过 scope 恢复显示")
	}
	row.dir.Tapped(nil)
	if opened != "D:/second" {
		t.Fatalf("目录回调未重绑：%q", opened)
	}
}

func TestHistoryListClearUsesNeutralSurfaceAndDangerIcon(t *testing.T) {
	a := test.NewApp()
	t.Cleanup(a.Quit)
	a.Settings().SetTheme(NewTheme())
	h := NewHistoryList("最近执行记录", true, nil)
	object := findCanvasObject(h.Root, func(object fyne.CanvasObject) bool {
		button, ok := object.(*appButton)
		return ok && button.Text == "清空"
	})
	button, ok := object.(*appButton)
	if !ok {
		t.Fatalf("表头清空应使用 appButton，实际 %T", object)
	}
	if button.Importance != widget.LowImportance {
		t.Fatalf("清空应使用中性表面而非实心危险按钮：%v", button.Importance)
	}
	requireDangerTrashIcon(t, button)
	if button.tooltip == nil || button.AccessibilityLabel() == "" || button.tooltip.label != button.AccessibilityLabel() {
		t.Fatalf("清空按钮缺少一致的中文名称和 tooltip：%q", button.AccessibilityLabel())
	}
	cleared := 0
	h.OnClear = func() { cleared++ }
	button.Tapped(nil)
	if cleared != 1 {
		t.Fatalf("清空回调次数应为 1，实际 %d", cleared)
	}
}

func TestHistoryListSetRecordsClearsTooltip(t *testing.T) {
	a := test.NewApp()
	t.Cleanup(a.Quit)
	a.Settings().SetTheme(NewTheme())
	h := NewHistoryList("最近执行记录", true, nil)
	window := test.NewWindow(newAppTooltipHost(h.Root))
	window.Resize(fyne.NewSize(720, 420))
	t.Cleanup(window.Close)
	h.SetRecords([]ui.HistoryRecord{{JobID: "job-tooltip", Time: "2026-10-31 09:00:00", Status: ui.JobSuccess}})
	var button *appButton
	waitShellCond(t, "历史卡真实行按钮", func() bool {
		button = findHistoryActionButton(h.Root, "查看日志")
		return button != nil
	})
	showAppButtonTooltip(t, window, button)
	panel := button.tooltip.host.panel

	h.SetRecords(nil)
	if button.tooltip.host != nil || panel.Visible() {
		t.Fatal("SetRecords 后不得遗留旧记录 tooltip")
	}
	time.Sleep(700 * time.Millisecond)
	if panel.Visible() {
		t.Fatal("SetRecords 后旧 tooltip 不应复活")
	}
}

type historyListRowFixture struct {
	scope    *appTooltipScope
	row      *fyne.Container
	ops      *fyne.Container
	log      *appButton
	dirScope *appTooltipScope
	dir      *appButton
	delete   *appButton
}

func newHistoryListRowFixture(t *testing.T, h *HistoryList) historyListRowFixture {
	t.Helper()
	object := h.list.CreateItem()
	scope, ok := object.(*appTooltipScope)
	if !ok {
		t.Fatalf("HistoryList CreateItem 应返回 appTooltipScope 包装行，实际 %T", object)
	}
	h.list.UpdateItem(0, scope)
	row := scope.content
	if len(row.Objects) != 2 {
		t.Fatalf("历史行结构不完整：%d", len(row.Objects))
	}
	center, ok := row.Objects[1].(*fyne.Container)
	if !ok || len(center.Objects) != 1 {
		t.Fatalf("操作组应由独立居中容器承载，实际 %T", row.Objects[1])
	}
	ops, ok := center.Objects[0].(*fyne.Container)
	if !ok || len(ops.Objects) != 3 {
		t.Fatalf("历史行应保留日志、目录、删除三个操作，实际 %T", center.Objects[0])
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
	deleteButton, ok := ops.Objects[2].(*appButton)
	if !ok {
		t.Fatalf("删除操作应使用 appButton，实际 %T", ops.Objects[2])
	}
	return historyListRowFixture{scope: scope, row: row, ops: ops, log: log, dirScope: dirScope, dir: dir, delete: deleteButton}
}

func assertCompactHistoryButton(t *testing.T, name string, button *appButton) {
	t.Helper()
	if button.Importance != widget.LowImportance {
		t.Fatalf("%s按钮应使用 LowImportance 中性表面：%v", name, button.Importance)
	}
	size := button.MinSize()
	if size.Width < 36 || size.Width > 40 || size.Height < 36 || size.Height > 40 {
		t.Fatalf("%s按钮命中区应为 36–40px：%v", name, size)
	}
	if button.Icon == nil || button.AccessibilityLabel() == "" || button.tooltip == nil {
		t.Fatalf("%s按钮必须有图标、中文名称和 tooltip", name)
	}
	if button.tooltip.label != button.AccessibilityLabel() {
		t.Fatalf("%s按钮名称与 tooltip 不一致：%q / %q", name, button.AccessibilityLabel(), button.tooltip.label)
	}
}

func requireDangerTrashIcon(t *testing.T, button *appButton) {
	t.Helper()
	if button.Icon == nil || button.Icon.Name() != "trash.svg" {
		t.Fatalf("危险操作必须使用 trash 图标：%+v", button.Icon)
	}
	if !strings.Contains(string(button.Icon.Content()), `stroke="#F53F3F"`) {
		t.Fatalf("trash 图标必须使用 DangerRed：%s", button.Icon.Content())
	}
}

func findHistoryActionButton(root fyne.CanvasObject, prefix string) *appButton {
	if root == nil {
		return nil
	}
	if button, ok := root.(*appButton); ok && strings.HasPrefix(button.AccessibilityLabel(), prefix) {
		return button
	}
	var children []fyne.CanvasObject
	switch object := root.(type) {
	case *fyne.Container:
		children = object.Objects
	case *appTooltipScope:
		children = object.content.Objects
	case fyne.Widget:
		children = test.WidgetRenderer(object).Objects()
	}
	for _, child := range children {
		if button := findHistoryActionButton(child, prefix); button != nil {
			return button
		}
	}
	return nil
}
