package appshell

import (
	"context"
	"strings"
	"testing"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/test"
	"fyne.io/fyne/v2/widget"

	"github.com/zhangzhenyu91/j1toolkit/uvmp-toolkit/core-go/internal/ui"
)

// final_fix_test：全分支最终修复波的跨页回归。

func TestCancelAndPauseButtonsUseNeutralSurface(t *testing.T) {
	app := test.NewApp()
	defer app.Quit()
	s := newTestShell(t, app, ui.NewFakeBackend(), Options{DisableTray: true, DisableScheduler: true})

	cases := []struct {
		pageID string
		label  string
		icon   string
	}{
		{"pcd_export", "取消导出", "cancel.svg"},
		{"daily_sync", "取消执行", "cancel.svg"},
		{"tree_sync", "取消执行", "cancel.svg"},
		{"crossing_sync", "取消执行", "cancel.svg"},
		{"course_autoplay", "暂停学习", "pause.svg"},
	}
	for _, tc := range cases {
		t.Run(tc.pageID+"/"+tc.label, func(t *testing.T) {
			button := pageButtonWithText(t, s.pages[tc.pageID].Object(), tc.label)
			if button.button.Importance != widget.LowImportance {
				t.Fatalf("%s 应为中性面 LowImportance，实际 %v", tc.label, button.button.Importance)
			}
			if button.button.Icon == nil || button.button.Icon.Name() != tc.icon {
				t.Fatalf("%s 图标应为 %s，实际 %v", tc.label, tc.icon, button.button.Icon)
			}
		})
	}
}

func TestQuizAnswerSectionDefaultsCollapsedAndKeepsUserState(t *testing.T) {
	f := ui.NewFakeBackend()
	if _, err := f.QuizImportBank(context.Background(), "安规.xlsx"); err != nil {
		t.Fatal(err)
	}
	app := test.NewApp()
	defer app.Quit()
	s := newTestShell(t, app, f, Options{DisableTray: true, DisableScheduler: true})
	p := s.pages["quiz_search"].(*pageQuiz)
	s.Navigate("quiz_search")
	waitShellCond(t, "题库页回填", func() bool {
		var n int
		runUI(func() { n = len(p.bankList.Objects) })
		return n > 0
	})
	resizePageViewport(t, p, pageNormalViewportW, pageNormalViewportH)

	var header *sectionHeader
	walkPageCanvasObjects(p.cardBox, func(object fyne.CanvasObject) bool {
		if candidate, ok := object.(*sectionHeader); ok && candidate.title == "查看答案" {
			header = candidate
		}
		return true
	})
	if header == nil {
		t.Fatal("题干自测/查看答案应改为默认收起的折叠入口")
	}
	if header.expanded {
		t.Fatal("查看答案入口常规应默认收起")
	}
	if pageCanvasObjectEffectivelyVisible(p.cardBox, p.testText) {
		t.Fatal("查看答案收起时题干输入不应可见")
	}

	runUI(func() { header.Tapped(nil) })
	if !pageCanvasObjectEffectivelyVisible(p.cardBox, p.testText) {
		t.Fatal("展开查看答案后题干输入应可见")
	}

	runUI(func() { p.SetMaximized(true) })
	resizePageViewport(t, p, 1440, pageNormalViewportH)
	if !header.expanded || !pageCanvasObjectEffectivelyVisible(p.cardBox, p.testText) {
		t.Fatal("最大化不应改写用户已展开的查看答案状态")
	}
	runUI(func() { p.SetMaximized(false) })
	if !header.expanded || !pageCanvasObjectEffectivelyVisible(p.cardBox, p.testText) {
		t.Fatal("还原不应改写用户已展开的查看答案状态")
	}
}

func TestPcdPreviewEmptyState(t *testing.T) {
	app := test.NewApp()
	defer app.Quit()
	s := newTestShell(t, app, ui.NewFakeBackend(), Options{DisableTray: true, DisableScheduler: true})
	p := s.pages["pcd_export"].(*pagePcd)
	runUI(func() { p.SetMaximized(true) })
	resizePageViewport(t, p, 1440, pageNormalViewportH)

	empty := findVisibleLabelContaining(p.previewCard, "粘贴并确认单号后在此列出")
	if empty == nil {
		t.Fatal("单号明细空白时应显示空态说明")
	}
	if !pageCanvasObjectEffectivelyVisible(p.cardBox, empty) {
		t.Fatal("最大化时单号明细空态应可见")
	}

	runUI(func() {
		p.applyPreview(map[string]any{
			"codes": []any{map[string]any{"seq": 1, "code": "J001"}},
		}, "粘贴文本", "（粘贴文本）")
	})
	if pageCanvasObjectEffectivelyVisible(p.cardBox, empty) {
		t.Fatal("已有真实单号时不应继续显示空态")
	}
	if !pageCanvasObjectEffectivelyVisible(p.cardBox, p.codesView.Root) {
		t.Fatal("已有真实单号时应显示单号列表")
	}
}

func TestSyncNormalModeKeepsSafetySentence(t *testing.T) {
	app := test.NewApp()
	defer app.Quit()
	s := newTestShell(t, app, ui.NewFakeBackend(), Options{DisableTray: true, DisableScheduler: true})
	for _, id := range []string{"tree_sync", "crossing_sync"} {
		t.Run(id, func(t *testing.T) {
			s.Navigate(id)
			page := s.pages[id].(*pageSync)
			resizePageViewport(t, page, pageNormalViewportW, pageNormalViewportH)
			label := findVisibleLabelContaining(page.root, "替换内网表前会自动备份原文件")
			if label == nil || !pageCanvasObjectEffectivelyVisible(page.root, label) {
				t.Fatal("常规必须保留「替换内网表前会自动备份原文件」安全说明")
			}
		})
	}
}

func TestHomeNavSubtitleMatchesVisibleContent(t *testing.T) {
	app := test.NewApp()
	defer app.Quit()
	s := newTestShell(t, app, ui.NewFakeBackend(), Options{DisableTray: true, DisableScheduler: true})
	meta, ok := s.navByID("home")
	if !ok {
		t.Fatal("缺少首页导航元信息")
	}
	if meta.desc != "三项主应用与常用入口" {
		t.Fatalf("首页副标题应只承诺常规可见内容：%q", meta.desc)
	}
}

func TestWindowMaximizedStateCacheStoreLookupForget(t *testing.T) {
	key := uintptr(0x5ad3)
	forgetWindowMaximized(key)
	if _, ok := cachedWindowMaximized(key); ok {
		t.Fatal("清理后缓存不应存在")
	}
	storeWindowMaximized(key, true)
	if value, ok := cachedWindowMaximized(key); !ok || !value {
		t.Fatalf("缓存读取错误：%v/%v", value, ok)
	}
	storeWindowMaximized(key, false)
	if value, ok := cachedWindowMaximized(key); !ok || value {
		t.Fatalf("缓存覆盖错误：%v/%v", value, ok)
	}
	forgetWindowMaximized(key)
	if _, ok := cachedWindowMaximized(key); ok {
		t.Fatal("销毁后缓存应删除")
	}
}

func TestRoundLayoutsZeroWidthNeverNegative(t *testing.T) {
	first := sizedRect(100, 40)
	second := sizedRect(120, 50)
	cases := []struct {
		name   string
		layout fyne.Layout
		objs   []fyne.CanvasObject
	}{
		{"homeQuickLayout", &homeQuickLayout{gap: 12}, []fyne.CanvasObject{sizedRect(80, 30), sizedRect(90, 40), sizedRect(100, 50)}},
		{"toolPairLayout", &toolPairLayout{breakAt: 520, gap: 16}, []fyne.CanvasObject{first, second}},
		{"pageSplitLayout", &pageSplitLayout{gap: 12, normalSplit: 1, maxSplit: 1}, []fyne.CanvasObject{first, second}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tc.layout.Layout(tc.objs, fyne.NewSize(0, 0))
			for i, object := range tc.objs {
				if object.Size().Width < 0 || object.Size().Height < 0 {
					t.Fatalf("零宽布局第 %d 个对象尺寸为负：%v", i, object.Size())
				}
			}
		})
	}
}

func findVisibleLabelContaining(root fyne.CanvasObject, substr string) *widget.Label {
	var found *widget.Label
	walkPageCanvasObjects(root, func(object fyne.CanvasObject) bool {
		if label, ok := object.(*widget.Label); ok && strings.Contains(label.Text, substr) {
			found = label
			return false
		}
		return true
	})
	return found
}
