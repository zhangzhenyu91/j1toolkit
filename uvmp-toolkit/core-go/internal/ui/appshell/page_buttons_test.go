package appshell

import (
	"strings"
	"testing"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/test"
	"fyne.io/fyne/v2/widget"

	"github.com/zhangzhenyu91/j1toolkit/uvmp-toolkit/core-go/internal/ui"
)

type pageButtonInfo struct {
	button *widget.Button
	app    *appButton
}

func collectPageButtons(object fyne.CanvasObject) []pageButtonInfo {
	var buttons []pageButtonInfo
	walkPageCanvasObjects(object, func(object fyne.CanvasObject) bool {
		switch button := object.(type) {
		case *appButton:
			buttons = append(buttons, pageButtonInfo{button: button.Button, app: button})
			return false
		case *widget.Button:
			buttons = append(buttons, pageButtonInfo{button: button})
			return false
		default:
			return true
		}
	})
	return buttons
}

func walkPageCanvasObjects(object fyne.CanvasObject, visit func(fyne.CanvasObject) bool) {
	if object == nil || !visit(object) {
		return
	}
	switch object := object.(type) {
	case *fyne.Container:
		for _, child := range object.Objects {
			walkPageCanvasObjects(child, visit)
		}
	case *container.Scroll:
		walkPageCanvasObjects(object.Content, visit)
	case fyne.Widget:
		for _, child := range test.WidgetRenderer(object).Objects() {
			walkPageCanvasObjects(child, visit)
		}
	}
}

func pageButtonWithText(t *testing.T, root fyne.CanvasObject, text string) pageButtonInfo {
	t.Helper()
	var matches []pageButtonInfo
	for _, button := range collectPageButtons(root) {
		if button.button.Text == text {
			matches = append(matches, button)
		}
	}
	if len(matches) == 0 {
		t.Fatalf("页面缺少按钮 %q", text)
	}
	for _, button := range matches {
		if button.app == nil {
			t.Fatalf("按钮 %q 仍是 %T，应迁移到 appButton", text, button.button)
		}
	}
	return matches[0]
}

func pageTooltipScopeFor(root, target fyne.CanvasObject) *appTooltipScope {
	var found *appTooltipScope
	var walk func(fyne.CanvasObject, *appTooltipScope)
	walk = func(object fyne.CanvasObject, scope *appTooltipScope) {
		if object == nil || found != nil {
			return
		}
		if current, ok := object.(*appTooltipScope); ok {
			scope = current
		}
		if object == target {
			found = scope
			return
		}
		switch object := object.(type) {
		case *fyne.Container:
			for _, child := range object.Objects {
				walk(child, scope)
			}
		case *container.Scroll:
			walk(object.Content, scope)
		case fyne.Widget:
			for _, child := range test.WidgetRenderer(object).Objects() {
				walk(child, scope)
			}
		}
	}
	walk(root, nil)
	return found
}

func pageCanvasObjectEffectivelyVisible(root, target fyne.CanvasObject) bool {
	var walk func(fyne.CanvasObject) (bool, bool)
	walk = func(object fyne.CanvasObject) (bool, bool) {
		if object == nil {
			return false, true
		}
		if object == target {
			return true, object.Visible()
		}
		var children []fyne.CanvasObject
		switch object := object.(type) {
		case *fyne.Container:
			children = object.Objects
		case *container.Scroll:
			children = []fyne.CanvasObject{object.Content}
		case fyne.Widget:
			children = test.WidgetRenderer(object).Objects()
		}
		for _, child := range children {
			if found, visible := walk(child); found {
				return true, object.Visible() && visible
			}
		}
		return false, true
	}
	found, visible := walk(root)
	return found && visible
}

func TestBusinessPagesUseConstructedAppButtons(t *testing.T) {
	app := test.NewApp()
	defer app.Quit()
	s := newTestShell(t, app, ui.NewFakeBackend(), Options{DisableTray: true, DisableScheduler: true})

	tests := []struct {
		pageID  string
		buttons map[string]string
	}{
		{pageID: "pcd_export", buttons: map[string]string{
			"选择派车单表格…": "file.svg", "确认粘贴的单号": "check.svg", "选择文件夹…": "folder.svg",
			"开始导出": "play.svg", "取消导出": "cancel.svg", "打开产物目录": "folder.svg",
			"复制": "copy.svg", "导出": "download.svg",
		}},
		{pageID: "daily_sync", buttons: map[string]string{
			"保存设置": "save.svg", "重新查找 U 盘": "refresh.svg", "立即执行一次": "play.svg",
			"取消执行": "cancel.svg", "打开产物目录": "folder.svg", "复制": "copy.svg", "导出": "download.svg",
		}},
		{pageID: "tree_sync", buttons: map[string]string{
			"选择文件": "file.svg", "保存设置": "save.svg", "按 U 盘中的同步任务执行": "play.svg",
			"立即备份到 U 盘": "upload.svg", "取消执行": "cancel.svg", "打开产物目录": "folder.svg",
			"复制": "copy.svg", "导出": "download.svg",
		}},
		{pageID: "quiz_search", buttons: map[string]string{
			"导入 Excel 题库…": "upload.svg", "下载导入模板": "download.svg", "开始搜题": "play.svg",
			"重置区域位置": "reset.svg", "保存题图": "save.svg", "引擎自检": "flask.svg", "题干自测": "search.svg",
		}},
		{pageID: "course_autoplay", buttons: map[string]string{
			"打开课程浏览器": "globe.svg", "开始学习": "play.svg", "暂停学习": "pause.svg",
			"打开产物目录": "folder.svg", "复制": "copy.svg", "导出": "download.svg",
		}},
		{pageID: "settings", buttons: map[string]string{
			"保存账号": "save.svg", "浏览…": "file.svg", "自动检测": "search.svg", "保存浏览器": "save.svg",
			"保存并发设置": "save.svg", "保存后台设置": "save.svg", "保存开发设置": "save.svg",
			"检查 PDF 渲染": "flask.svg", "检查派车系统登录": "flask.svg", "检查浏览器渲染": "flask.svg",
			"打开配置目录": "folder.svg", "打开状态目录": "folder.svg", "打开日志目录": "folder.svg", "一键清除": "trash.svg",
		}},
	}
	for _, tt := range tests {
		t.Run(tt.pageID, func(t *testing.T) {
			root := s.pages[tt.pageID].Object()
			for label, iconName := range tt.buttons {
				button := pageButtonWithText(t, root, label)
				if button.button.Icon == nil || button.button.Icon.Name() != iconName {
					t.Fatalf("按钮 %q 图标不符：%v，期望 %s", label, button.button.Icon, iconName)
				}
				if button.button.OnTapped == nil {
					t.Fatalf("按钮 %q 丢失原回调", label)
				}
			}
			primary := 0
			for _, button := range collectPageButtons(root) {
				if button.button.Importance == widget.HighImportance {
					primary++
				}
			}
			wantPrimary := 1
			if tt.pageID == "settings" {
				wantPrimary = 0
			}
			if primary != wantPrimary {
				t.Fatalf("页面 HighImportance 主按钮数=%d，期望 %d", primary, wantPrimary)
			}
		})
	}
}

func TestQuizScanButtonSynchronizesIconNameAndTooltip(t *testing.T) {
	app := test.NewApp()
	defer app.Quit()
	s := newTestShell(t, app, ui.NewFakeBackend(), Options{DisableTray: true, DisableScheduler: true})
	p := s.pages["quiz_search"].(*pageQuiz)

	assertState := func(wantText, wantIcon string) {
		t.Helper()
		button := pageButtonWithText(t, p.root, wantText)
		if button.button.Icon == nil || button.button.Icon.Name() != wantIcon {
			t.Fatalf("%s 图标应为 %s", wantText, wantIcon)
		}
		if got := button.app.AccessibilityLabel(); got != wantText {
			t.Fatalf("可访问名称=%q，期望 %q", got, wantText)
		}
		if button.app.tooltip == nil || button.app.tooltip.label != wantText {
			t.Fatalf("%s tooltip 未同步", wantText)
		}
	}
	assertState("开始搜题", "play.svg")
	p.applyScanState(ui.QuizScanState{Scanning: true, Interval: 1000, Threshold: 0.6, Opacity: 0.9})
	assertState("停止搜题", "stop.svg")
	p.applyScanState(ui.QuizScanState{Interval: 1000, Threshold: 0.6, Opacity: 0.9, QuestionCount: 2})
	assertState("开始搜题", "play.svg")
}

func TestDangerActionsUseNeutralSurfaceAndRedTrashIcon(t *testing.T) {
	app := test.NewApp()
	defer app.Quit()
	s := newTestShell(t, app, ui.NewFakeBackend(), Options{DisableTray: true, DisableScheduler: true})

	assertDangerIcon := func(button pageButtonInfo) {
		t.Helper()
		if button.button.Importance != widget.LowImportance {
			t.Fatalf("危险入口应使用 LowImportance 中性表面，实际 %v", button.button.Importance)
		}
		if button.button.Icon == nil || !strings.Contains(string(button.button.Icon.Content()), `stroke="#F53F3F"`) {
			t.Fatal("危险入口应使用 DangerRed trash 图标")
		}
	}
	clear := pageButtonWithText(t, s.pages["settings"].Object(), "一键清除")
	assertDangerIcon(clear)
	if clear.button.Text != "一键清除" {
		t.Fatal("批量一键清除必须保留图标和文字")
	}

	row := s.pages["quiz_search"].(*pageQuiz).bankRow(ui.QuizBank{ID: "bank-1", Name: "题库"})
	var foundDelete bool
	for _, button := range collectPageButtons(row) {
		if button.app == nil {
			t.Fatalf("题库行按钮仍是 %T", button.button)
		}
		switch button.app.AccessibilityLabel() {
		case "重命名":
			if button.button.Icon == nil || button.button.Icon.Name() != "edit.svg" {
				t.Fatal("重命名按钮应使用 edit 图标")
			}
		case "删除":
			foundDelete = true
			assertDangerIcon(button)
			if button.button.Text != "" || button.button.Icon.Name() != "trash.svg" {
				t.Fatal("题库删除应为 trash 紧凑图标按钮")
			}
		}
	}
	if !foundDelete {
		t.Fatal("题库行缺少删除按钮")
	}
}

func TestDynamicButtonAreasUseTooltipScopes(t *testing.T) {
	app := test.NewApp()
	defer app.Quit()
	s := newTestShell(t, app, ui.NewFakeBackend(), Options{DisableTray: true, DisableScheduler: true})

	quiz := s.pages["quiz_search"].(*pageQuiz)
	settings := s.pages["settings"].(*pageSettings)
	pcd := s.pages["pcd_export"].(*pagePcd)
	cases := []struct {
		name   string
		root   fyne.CanvasObject
		target fyne.CanvasObject
	}{
		{name: "题库行动态重建", root: quiz.root, target: quiz.bankList},
		{name: "设置自检产物动态重建", root: settings.root, target: settings.testPaths},
		{name: "结果区产物按钮显隐", root: pcd.result.Root, target: pcd.result.artifactBtn},
	}
	for _, section := range settings.sections {
		cases = append(cases, struct {
			name   string
			root   fyne.CanvasObject
			target fyne.CanvasObject
		}{name: "设置搜索显隐 " + section.title, root: settings.root, target: section.Root})
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			if scope := pageTooltipScopeFor(tt.root, tt.target); scope == nil {
				t.Fatal("动态按钮区域未纳入 appTooltipScope")
			}
		})
	}
}
