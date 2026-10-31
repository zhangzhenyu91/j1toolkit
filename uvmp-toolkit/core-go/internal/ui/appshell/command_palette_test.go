package appshell

import (
	"reflect"
	"testing"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/driver/desktop"
	"fyne.io/fyne/v2/test"
	"fyne.io/fyne/v2/widget"

	"github.com/zhangzhenyu91/j1toolkit/uvmp-toolkit/core-go/internal/ui"
)

func commandPaletteTestItems() []commandPaletteItem {
	return buildCommandPaletteItems([]navItem{
		{id: "home", title: "首页", desc: "主任务与最近结果", icon: "home"},
		{id: "pcd_export", title: "Trace Export", desc: "批量生成 PDF 报表", icon: "route"},
		{id: "history", title: "执行历史", desc: "查看任务结果", icon: "history"},
		{id: "settings", title: "设置", desc: "账号与 Browser 配置", icon: "settings"},
	}, func(string) bool { return false })
}

func TestFilterCommandPaletteItemsTitleDescriptionAndCase(t *testing.T) {
	items := commandPaletteTestItems()
	tests := []struct {
		name  string
		query string
		want  []string
	}{
		{name: "标题大小写不敏感", query: "trace", want: []string{"Trace Export", "收藏 Trace Export"}},
		{name: "描述大小写不敏感", query: "pdf", want: []string{"Trace Export", "收藏 Trace Export"}},
		{name: "非业务页描述", query: "browser", want: []string{"设置"}},
		{name: "无匹配", query: "不存在的命令", want: []string{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			filtered := filterCommandPaletteItems(items, tt.query)
			got := make([]string, 0, len(filtered))
			for _, item := range filtered {
				got = append(got, item.title)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("过滤结果不符：got=%v want=%v", got, tt.want)
			}
		})
	}
}

func TestFilterCommandPaletteItemsEmptyQuery(t *testing.T) {
	items := commandPaletteTestItems()
	for _, query := range []string{"", "  \t\n"} {
		filtered := filterCommandPaletteItems(items, query)
		if !reflect.DeepEqual(filtered, items) {
			t.Fatalf("空查询应保留全部命令：query=%q got=%v want=%v", query, filtered, items)
		}
	}
}

func TestBuildCommandPaletteItemsFavoriteOnlyForBusinessTools(t *testing.T) {
	items := buildCommandPaletteItems([]navItem{
		{id: "home", title: "首页", desc: "主任务", icon: "home"},
		{id: "history", title: "执行历史", desc: "历史记录", icon: "history"},
		{id: "settings", title: "设置", desc: "应用配置", icon: "settings"},
		{id: "pcd_export", title: "轨迹导出", desc: "导出报表", icon: "route"},
	}, func(appID string) bool { return appID == "pcd_export" })

	favorites := make(map[string]commandPaletteItem)
	for _, item := range items {
		if item.action == commandPaletteToggleFavorite {
			favorites[item.pageID] = item
		}
	}
	if len(favorites) != 1 {
		t.Fatalf("只有业务工具应出现收藏命令：%v", favorites)
	}
	favorite, ok := favorites["pcd_export"]
	if !ok || favorite.title != "取消收藏 轨迹导出" {
		t.Fatalf("业务工具收藏命令不符：%+v ok=%v", favorite, ok)
	}
	for _, pageID := range []string{"home", "history", "settings"} {
		if _, exists := favorites[pageID]; exists {
			t.Fatalf("非业务页不应出现收藏命令：%s", pageID)
		}
	}
}

func TestCommandPaletteVisibleRowUsesPointerAndChineseAccessibility(t *testing.T) {
	a := test.NewApp()
	defer a.Quit()
	s := newTestShell(t, a, ui.NewFakeBackend(), Options{DisableTray: true, DisableScheduler: true})
	runUI(func() { s.showCommandPalette() })
	defer runUI(func() {
		if overlay := s.Win.Canvas().Overlays().Top(); overlay != nil {
			overlay.Hide()
		}
	})
	var list *widget.List
	var entry *commandEntry
	runUI(func() {
		var walk func(fyne.CanvasObject)
		walk = func(obj fyne.CanvasObject) {
			if obj == nil {
				return
			}
			switch o := obj.(type) {
			case *widget.List:
				list = o
			case *commandEntry:
				entry = o
			case *widget.PopUp:
				walk(o.Content)
			case *fyne.Container:
				for _, child := range o.Objects {
					walk(child)
				}
			}
			if _, ok := obj.(*widget.List); ok {
				return
			}
			if _, ok := obj.(*fyne.Container); !ok {
				if w, ok := obj.(fyne.Widget); ok {
					for _, child := range w.CreateRenderer().Objects() {
						walk(child)
					}
				}
			}
		}
		walk(s.Win.Canvas().Overlays().Top())
	})
	if list == nil || entry == nil || list.Length() == 0 {
		t.Fatalf("命令面板测试前置未建立：list=%T entry=%T", list, entry)
	}
	if got := entry.Cursor(); got != desktop.TextCursor {
		t.Fatalf("命令面板文本 Entry 应保持 IBeam：%v", got)
	}
	var row fyne.CanvasObject
	runUI(func() {
		row = list.CreateItem()
		list.UpdateItem(0, row)
	})
	foundTitle := false
	var walkRow func(fyne.CanvasObject)
	walkRow = func(obj fyne.CanvasObject) {
		switch o := obj.(type) {
		case *widget.Label:
			foundTitle = foundTitle || o.Text == "首页"
		case *fyne.Container:
			for _, child := range o.Objects {
				walkRow(child)
			}
		default:
			if w, ok := obj.(fyne.Widget); ok {
				for _, child := range w.CreateRenderer().Objects() {
					walkRow(child)
				}
			}
		}
	}
	walkRow(row)
	if !foundTitle {
		t.Fatal("命令行模板未绑定首页标题")
	}
	accessible, ok := row.(fyne.Accessible)
	if !ok || accessible.AccessibilityLabel() != "首页" {
		t.Fatalf("命令行应暴露中文可访问名称：%T", row)
	}
	cursor, ok := row.(desktop.Cursorable)
	if !ok || cursor.Cursor() != desktop.PointerCursor {
		t.Fatal("命令面板可点击行应使用手型指针")
	}
	if _, ok := row.(desktop.Hoverable); !ok {
		t.Fatal("命令行须同时实现 Hoverable，驱动才会查询 Cursor")
	}
}
