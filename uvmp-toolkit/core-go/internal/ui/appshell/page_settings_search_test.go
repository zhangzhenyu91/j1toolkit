package appshell

import (
	"fmt"
	"testing"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/test"

	"github.com/zhangzhenyu91/j1toolkit/uvmp-toolkit/core-go/internal/ui"
)

func TestSettingsSearchMatchesChineseKeywords(t *testing.T) {
	groups := defaultSettingsSearchIndex()
	tests := []struct {
		name  string
		query string
		want  int
	}{
		{name: "账号说明", query: "密码留空", want: 0},
		{name: "浏览器关键词", query: "国密", want: 1},
		{name: "后台预登记别名", query: "开机自启", want: 2},
		{name: "快捷键与关于", query: "OFL", want: 3},
		{name: "诊断说明", query: "模拟数据", want: 4},
		{name: "导出字段名", query: "渲染并发", want: 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			matches := settingsSearchMatches(tt.query, groups)
			for i, matched := range matches {
				if want := i == tt.want; matched != want {
					t.Fatalf("查询 %q：第 %d 组命中=%v，期望 %v（全部结果 %v）", tt.query, i, matched, want, matches)
				}
			}
		})
	}

	matches := settingsSearchMatches("  ", groups)
	for i, matched := range matches {
		if !matched {
			t.Fatalf("空白查询应命中全部组，第 %d 组未命中", i)
		}
	}
}

func TestPageSettingsSearchNoResultsAndClearRestores(t *testing.T) {
	app := test.NewApp()
	defer app.Quit()
	s := newTestShell(t, app, ui.NewFakeBackend(), Options{DisableTray: true, DisableScheduler: true})
	p := s.pages["settings"].(*pageSettings)

	initialVisible, initialExpanded, initialEmpty, initialActive := snapshotSettingsSearchState(p)
	wantInitialExpanded := []bool{true, false, false, false, false}
	assertSettingsSearchState(t, "初始状态", initialVisible, initialExpanded, initialEmpty, initialActive,
		[]bool{true, true, true, true, true}, wantInitialExpanded, false, false)

	// 构造不同于默认值的搜索前状态，验证清空恢复的是快照而不是固定默认值。
	beforeSearch := []bool{false, true, false, true, false}
	runUI(func() {
		for i, expanded := range beforeSearch {
			p.sections[i].SetExpanded(expanded)
		}
		p.search.SetText("开机自启")
	})
	visible, expanded, empty, active := snapshotSettingsSearchState(p)
	assertSettingsSearchState(t, "中文关键词过滤", visible, nil, empty, active,
		[]bool{false, false, true, false, false}, nil, false, true)
	if !expanded[2] {
		t.Fatal("命中组应自动展开")
	}

	// 连续输入无结果查询不得覆盖首次搜索前的展开状态快照。
	runUI(func() { p.search.SetText("完全不存在的设置") })
	visible, expanded, empty, active = snapshotSettingsSearchState(p)
	assertSettingsSearchState(t, "无结果", visible, nil, empty, active,
		[]bool{false, false, false, false, false}, nil, true, true)

	runUI(func() { p.search.SetText("") })
	visible, expanded, empty, active = snapshotSettingsSearchState(p)
	assertSettingsSearchState(t, "清空恢复", visible, expanded, empty, active,
		[]bool{true, true, true, true, true}, beforeSearch, false, false)
}

func snapshotSettingsSearchState(p *pageSettings) (visible, expanded []bool, empty, active bool) {
	runUI(func() {
		visible = make([]bool, len(p.sections))
		expanded = make([]bool, len(p.sections))
		for i, section := range p.sections {
			visible[i] = pageCanvasObjectEffectivelyVisible(p.sectionScopes[i], section.Root)
			expanded[i] = section.Expanded()
		}
		empty = p.searchEmpty.Visible()
		active = p.searchActive
	})
	return visible, expanded, empty, active
}

func assertSettingsSearchState(
	t *testing.T,
	step string,
	visible, expanded []bool,
	empty, active bool,
	wantVisible, wantExpanded []bool,
	wantEmpty, wantActive bool,
) {
	t.Helper()
	for i, want := range wantVisible {
		if visible[i] != want {
			t.Fatalf("%s：第 %d 组显示=%v，期望 %v（全部结果 %v）", step, i, visible[i], want, visible)
		}
	}
	for i, want := range wantExpanded {
		if expanded[i] != want {
			t.Fatalf("%s：第 %d 组展开=%v，期望 %v（全部结果 %v）", step, i, expanded[i], want, expanded)
		}
	}
	if empty != wantEmpty {
		t.Fatalf("%s：空态显示=%v，期望 %v", step, empty, wantEmpty)
	}
	if active != wantActive {
		t.Fatalf("%s：搜索激活=%v，期望 %v", step, active, wantActive)
	}
}

// ---------------------------------------------------------------- Task7 常规/最大化双列布局

func TestPageSettingsMaximizedTwoColumnsPreserveSectionState(t *testing.T) {
	fake := ui.NewFakeBackend()
	counter := &task7CountingBackend{Backend: fake}
	a := test.NewApp()
	t.Cleanup(a.Quit)
	s := newTestShell(t, a, counter, Options{DisableTray: true, DisableScheduler: true})
	p := s.pages["settings"].(*pageSettings)
	s.Navigate("settings")
	waitShellCond(t, "设置页配置回填", func() bool {
		var workers string
		runUI(func() { workers = p.wQuery.Text })
		return workers != ""
	})
	task7RequireWindowMode(t, p)
	content := task7ScrollContent(t, p.root)
	searchCard := content.Objects[0]

	runUI(func() {
		if !p.sections[0].Expanded() || p.sections[1].Expanded() || p.sections[2].Expanded() {
			t.Fatal("设置页默认只应展开账号分组")
		}
		p.ssoUser.SetText("布局账号")
		p.ssoPass.SetText("布局密码")
		p.browser.SetText("D:/布局浏览器.exe")
		p.wQuery.SetText("6")
		p.wRender.SetText("3")
		p.sections[0].SetExpanded(true)  // 账号默认展开
		p.sections[2].SetExpanded(false) // 用户自行收起的分组不得被最大化强制展开
	})
	p.root.Refresh()
	beforeVisible, beforeExpanded, _, _ := snapshotSettingsSearchState(p)

	task7ResizePage(t, p.root, 944, 638)
	first := task7RequireOffset(t, content, p.sectionScopes[0])
	second := task7RequireOffset(t, content, p.sectionScopes[1])
	if first.X != second.X || first.Y == second.Y {
		t.Fatalf("常规设置应为单列：first=%v second=%v", first, second)
	}

	counter.calls.Store(0)
	runUI(func() { s.setWindowMaximized(true) })
	task7AssertNoBackendCalls(t, counter, "设置页最大化")

	visible, expanded, _, _ := snapshotSettingsSearchState(p)
	for i := range beforeExpanded {
		if expanded[i] != beforeExpanded[i] {
			t.Fatalf("最大化修改了第 %d 个分组折叠状态：%v → %v", i, beforeExpanded[i], expanded[i])
		}
		if visible[i] != beforeVisible[i] {
			t.Fatalf("最大化修改了第 %d 个分组可见性", i)
		}
	}
	if p.sections[4].Expanded() {
		t.Fatal("最大化不得强制展开开发诊断")
	}

	task7ResizePage(t, p.root, 1200, 700)
	if pos := task7RequireOffset(t, content, searchCard); pos.X != 0 {
		t.Fatalf("最大化搜索卡应从内容左缘开始：x=%.1f", pos.X)
	}
	if searchCard.Size().Width != content.Size().Width {
		t.Fatalf("最大化搜索卡应独占整行：%.1f / %.1f", searchCard.Size().Width, content.Size().Width)
	}
	first = task7RequireOffset(t, content, p.sectionScopes[0])
	second = task7RequireOffset(t, content, p.sectionScopes[1])
	if first.Y != second.Y || first.X == second.X {
		t.Fatalf("最大化设置应为双列：first=%v second=%v", first, second)
	}
	runUI(func() {
		if !pageCanvasObjectEffectivelyVisible(p.root, p.sectionScopes[4]) {
			t.Fatal("最大化不得隐藏开发诊断")
		}
		if p.ssoUser.Text != "布局账号" || p.ssoPass.Text != "布局密码" || p.browser.Text != "D:/布局浏览器.exe" ||
			p.wQuery.Text != "6" || p.wRender.Text != "3" {
			t.Fatal("最大化切换清空了设置输入")
		}
	})

	// 668×458：最大化双列必须退化为单列，搜索卡仍整行，分组可滚动到底。
	task7ResizePage(t, p.root, 668, 458)
	if searchCard.Size().Width != content.Size().Width {
		t.Fatalf("668×458 最大化搜索卡应仍占整行：%.1f / %.1f", searchCard.Size().Width, content.Size().Width)
	}
	first = task7RequireOffset(t, content, p.sectionScopes[0])
	second = task7RequireOffset(t, content, p.sectionScopes[1])
	if first.X != second.X || first.Y == second.Y {
		t.Fatalf("668×458 最大化设置应退化为单列：first=%v second=%v", first, second)
	}
	scroll := p.root.(*container.Scroll)
	runUI(func() { scroll.ScrollToBottom() })
	last := p.sectionScopes[4]
	lastPos := task7RequireOffset(t, content, last)
	if bottom := lastPos.Y + last.Size().Height; bottom > content.Size().Height {
		t.Fatalf("668×458 最大化末组超出内容：bottom=%.1f content=%.1f", bottom, content.Size().Height)
	}
	if want := fyne.Max(0, content.Size().Height-scroll.Size().Height); scroll.Offset.Y != want {
		t.Fatalf("668×458 最大化设置未滚到内容底部：offset=%.1f want=%.1f", scroll.Offset.Y, want)
	}
	if scroll.Offset.Y <= 0 {
		t.Fatal("668×458 最大化设置长内容应可滚动")
	}

	runUI(func() { s.setWindowMaximized(false) })
	visible, expanded, _, _ = snapshotSettingsSearchState(p)
	for i := range beforeExpanded {
		if expanded[i] != beforeExpanded[i] || visible[i] != beforeVisible[i] {
			t.Fatalf("还原后第 %d 个分组状态未恢复", i)
		}
	}
	runUI(func() {
		if p.ssoUser.Text != "布局账号" || p.ssoPass.Text != "布局密码" || p.browser.Text != "D:/布局浏览器.exe" ||
			p.wQuery.Text != "6" || p.wRender.Text != "3" {
			t.Fatal("还原后设置输入丢失")
		}
	})
	task7ResizePage(t, p.root, 944, 638)
	first = task7RequireOffset(t, content, p.sectionScopes[0])
	second = task7RequireOffset(t, content, p.sectionScopes[1])
	if first.X != second.X || first.Y == second.Y {
		t.Fatalf("还原后设置应恢复单列：first=%v second=%v", first, second)
	}
}

func TestPageSettingsMaximizedSearchStillFiltersAndRestores(t *testing.T) {
	a := test.NewApp()
	t.Cleanup(a.Quit)
	s := newTestShell(t, a, ui.NewFakeBackend(), Options{DisableTray: true, DisableScheduler: true})
	p := s.pages["settings"].(*pageSettings)
	s.Navigate("settings")
	time.Sleep(100 * time.Millisecond)
	mode := task7RequireWindowMode(t, p)

	runUI(func() { p.sections[1].SetExpanded(false) })
	p.root.Refresh()
	before := []bool{false, true, false, true, false}
	runUI(func() {
		for i, expanded := range before {
			p.sections[i].SetExpanded(expanded)
		}
	})
	runUI(func() { mode.SetMaximized(true) })

	runUI(func() { p.search.SetText("开机自启") })
	visible, expanded, empty, active := snapshotSettingsSearchState(p)
	assertSettingsSearchState(t, "最大化中文关键词过滤", visible, nil, empty, active,
		[]bool{false, false, true, false, false}, nil, false, true)
	if !expanded[2] {
		t.Fatal("最大化搜索命中组应自动展开")
	}

	runUI(func() { p.search.SetText("完全不存在的设置") })
	visible, expanded, empty, active = snapshotSettingsSearchState(p)
	assertSettingsSearchState(t, "最大化无结果", visible, nil, empty, active,
		[]bool{false, false, false, false, false}, nil, true, true)

	runUI(func() { p.search.SetText("") })
	visible, expanded, empty, active = snapshotSettingsSearchState(p)
	assertSettingsSearchState(t, "最大化清空恢复", visible, expanded, empty, active,
		[]bool{true, true, true, true, true}, before, false, false)
}

func TestPageSettingsMaximizedExpandedGroupsFitColumns(t *testing.T) {
	a := test.NewApp()
	t.Cleanup(a.Quit)
	s := newTestShell(t, a, ui.NewFakeBackend(), Options{DisableTray: true, DisableScheduler: true})
	p := s.pages["settings"].(*pageSettings)
	s.Navigate("settings")
	waitShellCond(t, "设置页配置回填", func() bool {
		var workers string
		runUI(func() { workers = p.wQuery.Text })
		return workers != ""
	})
	mode := task7RequireWindowMode(t, p)
	runUI(func() { mode.SetMaximized(true) })
	runUI(func() {
		p.sections[1].SetExpanded(true) // 宿主浏览器
		p.sections[2].SetExpanded(true) // 导出与后台
	})

	checkCardsFit := func(step string) {
		for i, scope := range p.sectionScopes {
			if !scope.Visible() {
				continue
			}
			if min := scope.MinSize().Width; min > scope.Size().Width+0.5 {
				t.Fatalf("%s：第 %d 组卡片最小宽度 %.1f 超过实际列宽 %.1f", step, i, min, scope.Size().Width)
			}
		}
	}

	// 900/1000 内容宽：展开两组后双列会裁切，必须退化为单列。
	for _, width := range []float32{900, 1000} {
		task7ResizePage(t, p.root, width, 638)
		first := task7RequireOffset(t, p.content, p.sectionScopes[0])
		second := task7RequireOffset(t, p.content, p.sectionScopes[1])
		if first.X != second.X || first.Y == second.Y {
			t.Fatalf("%.0f 宽展开分组应退化为单列：first=%v second=%v", width, first, second)
		}
		checkCardsFit(fmt.Sprintf("%.0f 宽单列", width))
	}

	// 真正大屏仍双列，且列宽不小于卡片 MinSize。
	task7ResizePage(t, p.root, 1200, 638)
	first := task7RequireOffset(t, p.content, p.sectionScopes[0])
	second := task7RequireOffset(t, p.content, p.sectionScopes[1])
	if first.Y != second.Y || first.X == second.X {
		t.Fatalf("1200 宽应恢复双列：first=%v second=%v", first, second)
	}
	checkCardsFit("1200 宽双列")
}

func TestPageSettingsNormalScrollsAtNarrowViewport(t *testing.T) {
	a := test.NewApp()
	t.Cleanup(a.Quit)
	s := newTestShell(t, a, ui.NewFakeBackend(), Options{DisableTray: true, DisableScheduler: true})
	p := s.pages["settings"].(*pageSettings)
	s.Navigate("settings")
	waitShellCond(t, "设置页配置回填", func() bool {
		var workers string
		runUI(func() { workers = p.wQuery.Text })
		return workers != ""
	})
	runUI(func() {
		p.sections[1].SetExpanded(true)
		p.sections[2].SetExpanded(true)
	})
	content := task7ScrollContent(t, p.root)
	task7ResizePage(t, p.root, 668, 458)
	scroll := p.root.(*container.Scroll)
	runUI(func() { scroll.ScrollToBottom() })
	if want := fyne.Max(0, content.Size().Height-scroll.Size().Height); scroll.Offset.Y != want {
		t.Fatalf("668×458 常规设置未滚到内容底部：offset=%.1f want=%.1f", scroll.Offset.Y, want)
	}
	if scroll.Offset.Y <= 0 {
		t.Fatal("668×458 常规设置长内容应可滚动")
	}
	runUI(func() { scroll.ScrollToTop() })
	if scroll.Offset.Y != 0 {
		t.Fatal("668×458 常规设置无法回到顶部")
	}
}
