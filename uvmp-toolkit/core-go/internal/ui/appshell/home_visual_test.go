package appshell

import (
	"strings"
	"testing"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/test"
	"fyne.io/fyne/v2/widget"

	"github.com/zhangzhenyu91/j1toolkit/uvmp-toolkit/core-go/internal/ui"
)

// 首页新版信息架构 / 响应式主从 / 入口行为的结构回归（FakeBackend，零网络）。

// collectPrimaryButtons 收集对象树中可见的 HighImportance 按钮（首页只允许一枚主按钮）。
func collectPrimaryButtons(obj fyne.CanvasObject) []*widget.Button {
	var out []*widget.Button
	var walk func(fyne.CanvasObject)
	walk = func(o fyne.CanvasObject) {
		if o == nil {
			return
		}
		if b, ok := o.(*widget.Button); ok {
			if b.Importance == widget.HighImportance && b.Visible() {
				out = append(out, b)
			}
			return
		}
		switch c := o.(type) {
		case *fyne.Container:
			for _, child := range c.Objects {
				walk(child)
			}
		case *container.Scroll:
			walk(c.Content)
		case *homePane:
			walk(c.mainScroll)
		}
	}
	walk(obj)
	return out
}

// TestHome_PrimaryToolsSinglePrimaryButton 三项主工具顺序 + 首页唯一主按钮「开始导出」
// 只切页、不自动执行任务。
func TestHome_PrimaryToolsSinglePrimaryButton(t *testing.T) {
	f := ui.NewFakeBackend()
	f.StepDelay = time.Millisecond
	app := test.NewApp()
	defer app.Quit()
	s := newTestShell(t, app, f, Options{DisableTray: true, DisableScheduler: true})
	p := s.pages["home"].(*pageHome)

	tools := homeToolList()
	wantTools := []string{"pcd_export", "course_autoplay", "quiz_search"}
	if len(tools) != len(wantTools) {
		t.Fatalf("主工具数量不符: %d", len(tools))
	}
	for i, id := range wantTools {
		if tools[i].id != id {
			t.Fatalf("第 %d 项主工具应为 %s: %s", i+1, id, tools[i].id)
		}
	}
	bg := homeBackgroundList()
	wantBg := []string{"daily_sync", "tree_sync", "crossing_sync"}
	for i, id := range wantBg {
		if bg[i].id != id {
			t.Fatalf("第 %d 项后台同步应为 %s: %s", i+1, id, bg[i].id)
		}
	}
	if len(p.cards) != 6 {
		t.Fatalf("六个任务入口应全部保留: %d", len(p.cards))
	}

	var primary []*widget.Button
	runUI(func() { primary = collectPrimaryButtons(p.pane.mainScroll.Content) })
	if len(primary) != 1 {
		t.Fatalf("首页应只有一枚主按钮: %d", len(primary))
	}
	if primary[0].Text != "开始导出" {
		t.Fatalf("主按钮文案不符: %q", primary[0].Text)
	}

	before := s.RunningJobs()
	runUI(func() { primary[0].OnTapped() })
	if s.current != "pcd_export" {
		t.Fatalf("「开始导出」应切到轨迹导出页，实际: %s", s.current)
	}
	if s.RunningJobs() != before {
		t.Fatalf("点主按钮不得自动执行任务: before=%d after=%d", before, s.RunningJobs())
	}
}

// TestHome_ResponsiveMasterDetail 宽窗主从、窄窗单列：最近结果在窄窗回到主列末尾。
func TestHome_ResponsiveMasterDetail(t *testing.T) {
	f := ui.NewFakeBackend()
	f.StepDelay = time.Millisecond
	app := test.NewApp()
	defer app.Quit()
	s := newTestShell(t, app, f, Options{DisableTray: true, DisableScheduler: true})
	p := s.pages["home"].(*pageHome)

	runUI(func() { p.pane.Resize(fyne.NewSize(1000, 680)) })
	var wide bool
	var asideVisible, inlineVisible bool
	var mainW, asideW float32
	runUI(func() {
		wide = p.pane.wide
		asideVisible = p.pane.asideScroll.Visible()
		inlineVisible = p.pane.inlineRecent.Visible()
		mainW = p.pane.mainScroll.Size().Width
		asideW = p.pane.asideScroll.Size().Width
	})
	if !wide || !asideVisible || inlineVisible {
		t.Fatalf("宽窗应为主从布局: wide=%v aside=%v inline=%v", wide, asideVisible, inlineVisible)
	}
	if mainW+homeGap+asideW > 1000.5 || mainW < 500 {
		t.Fatalf("宽窗分栏宽度不符: main=%.1f aside=%.1f", mainW, asideW)
	}

	runUI(func() { p.pane.Resize(fyne.NewSize(760, 680)) })
	runUI(func() {
		wide = p.pane.wide
		asideVisible = p.pane.asideScroll.Visible()
		inlineVisible = p.pane.inlineRecent.Visible()
	})
	if wide || asideVisible || !inlineVisible {
		t.Fatalf("窄窗应为单列且最近结果并入主列: wide=%v aside=%v inline=%v", wide, asideVisible, inlineVisible)
	}
}

// TestHome_NavIconsAndBackgroundRows 侧栏使用统一线性 SVG 图标（语义各不相同）；
// 后台同步行整行可点进对应页面。
func TestHome_NavIconsAndBackgroundRows(t *testing.T) {
	f := ui.NewFakeBackend()
	f.StepDelay = time.Millisecond
	app := test.NewApp()
	defer app.Quit()
	s := newTestShell(t, app, f, Options{DisableTray: true, DisableScheduler: true})
	p := s.pages["home"].(*pageHome)

	seen := map[string]string{}
	for id, entry := range s.navEntries {
		res := entry.icon.Resource
		if res == nil || !strings.HasSuffix(strings.ToLower(res.Name()), ".svg") {
			t.Fatalf("侧栏 %s 应使用 SVG 线性图标: %v", id, res)
		}
		if prev, ok := seen[res.Name()]; ok {
			t.Fatalf("%s 与 %s 复用了同一个图标 %s", id, prev, res.Name())
		}
		seen[res.Name()] = id
	}

	for _, id := range []string{"daily_sync", "tree_sync", "crossing_sync"} {
		row, ok := p.cards[id].Root.(*homeRow)
		if !ok {
			t.Fatalf("%s 后台同步入口应为紧凑状态行: %T", id, p.cards[id].Root)
		}
		if row.iconName == "" {
			t.Fatalf("%s 状态行缺少线性图标", id)
		}
	}
	runUI(func() {
		p.cards["tree_sync"].Root.(*homeRow).Tapped(&fyne.PointEvent{})
	})
	if s.current != "tree_sync" {
		t.Fatalf("后台同步行应可进入对应页面: %s", s.current)
	}
}

// TestHome_StatusColorSemantics 状态圆点语义色（成功绿 / 失败红 / 警告橙 / 取消灰）。
func TestHome_StatusColorSemantics(t *testing.T) {
	if statusColor(widget.SuccessImportance) != SuccessGreen {
		t.Fatal("成功状态应为绿点")
	}
	if statusColor(widget.DangerImportance) != DangerRed {
		t.Fatal("失败状态应为红点")
	}
	if statusColor(widget.WarningImportance) != WarningOrange {
		t.Fatal("警告状态应为橙点")
	}
	if statusColor(widget.LowImportance) != TextTertiary {
		t.Fatal("低强调状态应为三级灰点，而不是整行亮色")
	}
}
