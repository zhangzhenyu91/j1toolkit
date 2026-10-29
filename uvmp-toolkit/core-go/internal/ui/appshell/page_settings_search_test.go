package appshell

import (
	"testing"

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
	wantInitialExpanded := []bool{true, true, true, false, false}
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
			visible[i] = section.Root.Visible()
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
