package appshell

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/test"

	"github.com/zhangzhenyu91/j1toolkit/uvmp-toolkit/core-go/internal/ui"
)

// Task6：首页、轨迹导出、题库、学堂的常规 / 最大化布局。
// 视口尺寸按 1160×780 与最小 900×600 窗口扣除侧栏、页头、顶栏后的实际内容区估算。

const (
	pageNormalViewportW float32 = 928
	pageNormalViewportH float32 = 638
	pageMinViewportW    float32 = 668
	pageMinViewportH    float32 = 458
)

func pageScrollOf(t *testing.T, page Page) *container.Scroll {
	t.Helper()
	if home, ok := page.(*pageHome); ok {
		if home.pane == nil || home.pane.mainScroll == nil {
			t.Fatal("首页缺少主滚动区")
		}
		return home.pane.mainScroll
	}
	scroll, ok := page.Object().(*container.Scroll)
	if !ok {
		t.Fatalf("页面根对象不是整页滚动区：%T", page.Object())
	}
	return scroll
}

func resizePageViewport(t *testing.T, page Page, width, height float32) {
	t.Helper()
	runUI(func() {
		scroll := pageScrollOf(t, page)
		scroll.Resize(fyne.NewSize(width, height))
		scroll.Refresh()
	})
}

// objectShownIn 判断 target 在 root 树中是否真正可见（任一祖先隐藏即不可见）。
func objectShownIn(root, target fyne.CanvasObject) bool {
	found, visible := walkObjectVisibility(root, target, true)
	return found && visible
}

func walkObjectVisibility(object, target fyne.CanvasObject, parentVisible bool) (bool, bool) {
	if object == nil {
		return false, false
	}
	visible := parentVisible && object.Visible()
	if object == target {
		return true, visible
	}
	walkChildren := func(children []fyne.CanvasObject) (bool, bool) {
		for _, child := range children {
			if found, shown := walkObjectVisibility(child, target, visible); found {
				return true, shown
			}
		}
		return false, false
	}
	switch node := object.(type) {
	case *fyne.Container:
		return walkChildren(node.Objects)
	case *container.Scroll:
		return walkObjectVisibility(node.Content, target, visible)
	case *minBox:
		return walkObjectVisibility(node.content, target, visible)
	case *appTooltipScope:
		return walkObjectVisibility(node.content, target, visible)
	case *pageStage:
		return walkObjectVisibility(node.content, target, visible)
	case *homePane:
		return walkObjectVisibility(node.mainScroll, target, visible)
	case *homeBottom:
		if found, shown := walkObjectVisibility(node.background, target, visible); found {
			return true, shown
		}
		return walkObjectVisibility(node.recent, target, visible)
	case fyne.Widget:
		return walkChildren(test.WidgetRenderer(node).Objects())
	}
	return false, false
}

func assertShown(t *testing.T, step string, root, target fyne.CanvasObject, want bool) {
	t.Helper()
	if target == nil {
		t.Fatalf("%s：目标对象为空", step)
	}
	if got := objectShownIn(root, target); got != want {
		t.Fatalf("%s：可见性=%v，期望 %v", step, got, want)
	}
}

func assertColumnSplit(t *testing.T, step string, left, right fyne.CanvasObject) {
	t.Helper()
	if left.Position().X != 0 {
		t.Fatalf("%s：左列应贴左，实际 X=%.1f", step, left.Position().X)
	}
	if right.Position().X <= left.Size().Width {
		t.Fatalf("%s：右列应在左列右侧，rightX=%.1f leftW=%.1f", step, right.Position().X, left.Size().Width)
	}
}

func assertCardFitsViewport(t *testing.T, step string, card fyne.CanvasObject, height float32) {
	t.Helper()
	bottom := card.Position().Y + card.Size().Height
	if bottom > height {
		t.Fatalf("%s：主执行卡底部 %.1f 超出常规视口 %.1f", step, bottom, height)
	}
}

func assertCardsFitViewport(t *testing.T, step string, cards []fyne.CanvasObject, height float32) {
	t.Helper()
	for _, card := range cards {
		assertCardFitsViewport(t, step, card, height)
	}
}

func assertCardWidthsFit(t *testing.T, step string, cards []fyne.CanvasObject, columnWidth float32) {
	t.Helper()
	for i, card := range cards {
		if card == nil || !card.Visible() {
			continue
		}
		if min := card.MinSize().Width; min > columnWidth+0.5 {
			t.Fatalf("%s：第 %d 张卡片最小宽度 %.1f 超过列宽 %.1f，会被裁切", step, i, min, columnWidth)
		}
	}
}

func TestProgressRowDefaultHiddenAndResetShows(t *testing.T) {
	app := test.NewApp()
	defer app.Quit()
	row := newProgressRow()
	if row.Root.Visible() {
		t.Fatal("空闲进度条默认应隐藏，不占任务卡高度")
	}
	runUI(func() { row.Reset() })
	if !row.Root.Visible() {
		t.Fatal("任务启动 Reset 后进度条应显示")
	}
	if row.bar.Value != 0 {
		t.Fatalf("Reset 后进度应归零：%v", row.bar.Value)
	}
}

// TestPagePcdProgressShowsOnStart 空闲不显示进度，开始导出后显示并随任务收口。
func TestPagePcdProgressShowsOnStart(t *testing.T) {
	f := ui.NewFakeBackend()
	f.StepDelay = 150 * time.Millisecond
	ctx := context.Background()
	if err := f.SaveConfig(ctx, ui.ConfigPatch{Dev: &ui.DevConfig{Mock: true}}); err != nil {
		t.Fatal(err)
	}
	app := test.NewApp()
	defer app.Quit()
	s := newTestShell(t, app, f, Options{DisableTray: true, DisableScheduler: true})
	p := s.pages["pcd_export"].(*pagePcd)
	s.Navigate("pcd_export")
	time.Sleep(50 * time.Millisecond)
	runUI(func() {
		p.codes = []map[string]any{{"seq": 1, "code": "J001"}}
		p.outdir.SetText("mem://out")
	})
	if p.progress.Root.Visible() {
		t.Fatal("空闲轨迹导出不应显示 0% 进度条")
	}
	runUI(func() { p.startRun() })
	waitShellCond(t, "开始导出显示进度", func() bool {
		var visible bool
		runUI(func() { visible = p.progress.Root.Visible() && p.currentJob != nil })
		return visible
	})
	waitShellCond(t, "导出任务收口", func() bool {
		var job *ui.JobHandle
		runUI(func() { job = p.currentJob })
		return job == nil
	})
	empty := findVisibleLabelContaining(p.previewCard, "粘贴并确认单号后在此列出")
	if empty == nil || !empty.Visible() || p.codesView.Root.Visible() {
		t.Fatal("成功导出清空单号后应恢复单号明细空态")
	}
}

func TestPageWindowModes_ImplementSetMaximized(t *testing.T) {
	f := ui.NewFakeBackend()
	app := test.NewApp()
	defer app.Quit()
	s := newTestShell(t, app, f, Options{DisableTray: true, DisableScheduler: true})

	for _, id := range []string{"home", "pcd_export", "quiz_search", "course_autoplay"} {
		page := s.pages[id]
		if _, ok := page.(windowModePage); !ok {
			t.Fatalf("页面 %s 未实现 SetMaximized", id)
		}
	}
}

// TestPagePcdWindowMode_NormalHidesSecondaryAndMaximizedUsesTwoColumns
// 常规只保留选择/粘贴、数量、目录、导出选项、开始/取消、进度与结果；预览、长说明、历史次级隐藏。
func TestPagePcdWindowMode_NormalHidesSecondaryAndMaximizedUsesTwoColumns(t *testing.T) {
	f := ui.NewFakeBackend()
	app := test.NewApp()
	defer app.Quit()
	s := newTestShell(t, app, f, Options{DisableTray: true, DisableScheduler: true})
	p := s.pages["pcd_export"].(*pagePcd)

	runUI(func() {
		p.codesText.SetText("J001\nJ002")
		p.outdir.SetText("D:/导出")
	})
	resizePageViewport(t, p, pageNormalViewportW, pageNormalViewportH)

	for name, target := range map[string]fyne.CanvasObject{
		"单号多行输入": p.codesText,
		"保存位置":   p.outdir,
		"导出选项":   p.optTrack,
		"开始导出":   p.startBtn,
		"结果摘要":   p.result.Root,
		"排错日志入口": p.result.LogRoot,
	} {
		assertShown(t, "常规 "+name, p.cardBox, target, true)
	}
	for name, target := range map[string]fyne.CanvasObject{
		"空闲进度": p.progress.Root,
		"单号明细": p.previewCard,
		"长说明":  p.notes,
		"历史记录": p.history.Root,
	} {
		assertShown(t, "常规隐藏 "+name, p.cardBox, target, false)
	}
	assertCardFitsViewport(t, "常规开始导出", p.startCard, pageNormalViewportH)

	runUI(func() { p.SetMaximized(true) })
	resizePageViewport(t, p, 1440, pageNormalViewportH)
	for name, target := range map[string]fyne.CanvasObject{
		"单号明细": p.previewCard,
		"长说明":  p.notes,
		"历史记录": p.history.Root,
	} {
		assertShown(t, "最大化显示 "+name, p.cardBox, target, true)
	}
	assertColumnSplit(t, "最大化双栏", p.startCard, p.previewCard)

	// 长单号预览保留内层滚动，不被整页滚动吃掉。
	runUI(func() {
		lines := make([]string, 0, 160)
		for i := 1; i <= 160; i++ {
			lines = append(lines, fmt.Sprintf("%d. J%04d", i, i))
		}
		p.codesView.SetText(strings.Join(lines, "\n"))
		reflowInScroll(p.codesView.Root)
		p.codesView.scroll.ScrollToBottom()
	})
	if p.codesView.scroll.Offset.Y <= 0 {
		t.Fatalf("单号明细内层滚动不可用：offset=%v", p.codesView.scroll.Offset)
	}

	runUI(func() {
		p.SetMaximized(false)
		p.codesView.Clear()
	})
	resizePageViewport(t, p, pageNormalViewportW, pageNormalViewportH)
	assertShown(t, "还原隐藏单号明细", p.cardBox, p.previewCard, false)
	assertShown(t, "还原隐藏历史", p.cardBox, p.history.Root, false)
	if p.codesText.Text != "J001\nJ002" || p.outdir.Text != "D:/导出" {
		t.Fatalf("最大化切换清空了输入：%q / %q", p.codesText.Text, p.outdir.Text)
	}
}

// TestPageQuizWindowMode_PrimaryStaysLeftAndCollapsibleStateIsPreserved
// 常规主操作顺序不变；最大化主列（题库+启停）与次级列（自测/高级设置/诊断）分区。
func TestPageQuizWindowMode_PrimaryStaysLeftAndCollapsibleStateIsPreserved(t *testing.T) {
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
	runUI(func() {
		p.optionsSec.SetExpanded(true)
		p.diagSec.SetExpanded(false)
	})
	resizePageViewport(t, p, pageNormalViewportW, pageNormalViewportH)

	for name, target := range map[string]fyne.CanvasObject{
		"题库操作":   p.bankList,
		"开始搜题":   p.scanBtn,
		"A 区域":   p.boxACheck,
		"B 区域":   p.boxBCheck,
		"高级设置入口": p.optionsSec.Root,
		"技术诊断入口": p.diagSec.Root,
	} {
		assertShown(t, "常规 "+name, p.cardBox, target, true)
	}
	assertShown(t, "常规查看答案折叠入口", p.cardBox, p.testCard, true)
	assertShown(t, "常规收起题干自测", p.cardBox, p.testText, false)
	assertCardFitsViewport(t, "常规开始搜题", p.startCard, pageNormalViewportH)

	runUI(func() { p.SetMaximized(true) })
	resizePageViewport(t, p, 1440, pageNormalViewportH)
	assertColumnSplit(t, "最大化双栏", p.startCard, p.optionsSec.Root)
	assertShown(t, "最大化查看答案", p.cardBox, p.testCard, true)
	assertShown(t, "最大化保持收起题干自测", p.cardBox, p.testText, false)
	if p.answerSec.Expanded() {
		t.Fatal("最大化不应自动展开查看答案")
	}
	assertShown(t, "最大化高级设置", p.cardBox, p.optionsSec.Root, true)
	assertShown(t, "最大化技术诊断", p.cardBox, p.diagSec.Root, true)
	if !p.optionsSec.Expanded() || p.diagSec.Expanded() {
		t.Fatalf("最大化改写了折叠状态：options=%v diag=%v", p.optionsSec.Expanded(), p.diagSec.Expanded())
	}

	runUI(func() { p.SetMaximized(false) })
	if !p.optionsSec.Expanded() || p.diagSec.Expanded() {
		t.Fatalf("还原改写了折叠状态：options=%v diag=%v", p.optionsSec.Expanded(), p.diagSec.Expanded())
	}
}

// TestPageQuizWindowMode_LongBankListScrolls 长题库仍可滚动到底。
func TestPageQuizWindowMode_LongBankListScrolls(t *testing.T) {
	f := ui.NewFakeBackend()
	for i := 0; i < 18; i++ {
		if _, err := f.QuizImportBank(context.Background(), fmt.Sprintf("题库-%02d.xlsx", i)); err != nil {
			t.Fatal(err)
		}
	}
	app := test.NewApp()
	defer app.Quit()
	s := newTestShell(t, app, f, Options{DisableTray: true, DisableScheduler: true})
	p := s.pages["quiz_search"].(*pageQuiz)
	s.Navigate("quiz_search")
	waitShellCond(t, "长题库回填", func() bool {
		var n int
		runUI(func() { n = len(p.bankList.Objects) })
		return n >= 18
	})
	resizePageViewport(t, p, pageMinViewportW, pageMinViewportH)
	scroll := pageScrollOf(t, p)
	runUI(func() { scroll.ScrollToBottom() })
	if scroll.Offset.Y <= 0 {
		t.Fatalf("长题库无法滚动到底：offset=%v", scroll.Offset)
	}
	runUI(func() { scroll.ScrollToTop() })
	if scroll.Offset.Y != 0 {
		t.Fatalf("长题库无法回到顶部：offset=%v", scroll.Offset)
	}
}

// TestPageCourseWindowMode_CompactNormalAndFullMaximized
// 常规只保留紧凑就绪状态、URL、仅扫描、开始/暂停、阶段/结果；完整状态/检查点/历史/技术详情最大化显示。
func TestPageCourseWindowMode_CompactNormalAndFullMaximized(t *testing.T) {
	f := ui.NewFakeBackend()
	app := test.NewApp()
	defer app.Quit()
	s := newTestShell(t, app, f, Options{DisableTray: true, DisableScheduler: true})
	p := s.pages["course_autoplay"].(*pageCourse)
	s.Navigate("course_autoplay")
	time.Sleep(50 * time.Millisecond)
	runUI(func() {
		p.startURL.SetText("https://edu.example/course")
		p.advancedSec.SetExpanded(true)
	})
	resizePageViewport(t, p, pageNormalViewportW, pageNormalViewportH)

	for name, target := range map[string]fyne.CanvasObject{
		"紧凑状态":   p.compactStatus.Root,
		"课程网址":   p.startURL,
		"仅扫描":    p.scanOnly,
		"开始学习":   p.startBtn,
		"阶段":     p.stage,
		"结果摘要":   p.result.Root,
		"排错日志入口": p.result.LogRoot,
		"高级设置":   p.advancedSec.Root,
		"必要操作说明": p.statusNote,
	} {
		assertShown(t, "常规 "+name, p.cardBox, target, true)
	}
	if !strings.Contains(p.statusNote.Text, "登录学堂") || !strings.Contains(p.statusNote.Text, "保持课程浏览器在前台") {
		t.Fatalf("常规缺少登录学堂/进入课程设置列表/保持前台说明：%q", p.statusNote.Text)
	}
	for name, target := range map[string]fyne.CanvasObject{
		"空闲进度":    p.progress.Root,
		"完整浏览器状态": p.statusBox,
		"学习进度":    p.checkpointsCard,
		"最近执行记录":  p.history.Root,
		"技术详情":    p.techCard,
	} {
		assertShown(t, "常规隐藏 "+name, p.cardBox, target, false)
	}
	assertCardFitsViewport(t, "常规开始学习", p.startCard, pageNormalViewportH)

	runUI(func() { p.SetMaximized(true) })
	resizePageViewport(t, p, 1440, pageNormalViewportH)
	for name, target := range map[string]fyne.CanvasObject{
		"完整浏览器状态": p.statusBox,
		"学习进度":    p.checkpointsCard,
		"最近执行记录":  p.history.Root,
		"技术详情":    p.techCard,
	} {
		assertShown(t, "最大化显示 "+name, p.cardBox, target, true)
	}
	assertShown(t, "最大化隐藏紧凑状态", p.cardBox, p.compactStatus.Root, false)
	assertColumnSplit(t, "最大化双栏", p.startCard, p.checkpointsCard)
	if !p.advancedSec.Expanded() {
		t.Fatal("最大化改写了高级设置折叠状态")
	}

	runUI(func() { p.SetMaximized(false) })
	if !p.advancedSec.Expanded() {
		t.Fatal("还原改写了高级设置折叠状态")
	}
	if p.startURL.Text != "https://edu.example/course" {
		t.Fatalf("最大化切换清空了课程网址：%q", p.startURL.Text)
	}
}

// courseStatusBackend 固定课程浏览器状态，验证紧凑状态与开始提示的语义一致性。
type courseStatusBackend struct {
	ui.Backend
	status ui.CourseStatus
	calls  chan struct{}
}

func (b *courseStatusBackend) CourseStatus(context.Context) (ui.CourseStatus, error) {
	if b.calls != nil {
		select {
		case b.calls <- struct{}{}:
		default:
		}
	}
	return b.status, nil
}

// TestPageCourseCompactStatusMatchesReadiness 只有 Connected&&Playwright 才是绿色就绪；
// 已连接但自动操作不可用时必须警告，并与 startHint 文案一致。
func TestPageCourseCompactStatusMatchesReadiness(t *testing.T) {
	cases := []struct {
		name        string
		status      ui.CourseStatus
		wantCompact string
		wantHint    string
		wantDot     interface{}
		wantStart   bool
	}{
		{
			name:        "未连接",
			status:      ui.CourseStatus{Connected: false, Playwright: false},
			wantCompact: "请先打开课程浏览器",
			wantHint:    "请先打开课程浏览器",
			wantDot:     WarningOrange,
		},
		{
			name:        "已连接但自动操作不可用",
			status:      ui.CourseStatus{Connected: true, Pages: 2, ActiveLists: 1, Playwright: false},
			wantCompact: "自动操作不可用",
			wantHint:    "自动操作不可用",
			wantDot:     WarningOrange,
		},
		{
			name:        "就绪",
			status:      ui.CourseStatus{Connected: true, Pages: 3, ActiveLists: 2, Playwright: true},
			wantCompact: "可开始学习",
			wantDot:     SuccessGreen,
			wantStart:   true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			backend := &courseStatusBackend{Backend: ui.NewFakeBackend(), status: tc.status, calls: make(chan struct{}, 4)}
			app := test.NewApp()
			defer app.Quit()
			s := newTestShell(t, app, backend, Options{DisableTray: true, DisableScheduler: true})
			p := s.pages["course_autoplay"].(*pageCourse)
			s.Navigate("course_autoplay")
			waitShellCond(t, "课程状态读取", func() bool {
				select {
				case <-backend.calls:
					return true
				default:
					return false
				}
			})
			waitShellCond(t, "紧凑状态回填", func() bool {
				var text string
				runUI(func() { text = p.compactStatus.label.Text })
				return strings.Contains(text, tc.wantCompact)
			})
			runUI(func() {
				if !strings.Contains(p.compactStatus.label.Text, tc.wantCompact) {
					t.Fatalf("紧凑状态文案不符：%q", p.compactStatus.label.Text)
				}
				if p.compactStatus.dot.FillColor != tc.wantDot {
					t.Fatalf("紧凑状态语义色不符：%v want %v", p.compactStatus.dot.FillColor, tc.wantDot)
				}
				if p.startBtn.Disabled() == tc.wantStart {
					t.Fatalf("开始学习可用态不符：disabled=%v wantStart=%v", p.startBtn.Disabled(), tc.wantStart)
				}
				if tc.wantHint == "" {
					if p.startHint.Visible() {
						t.Fatal("就绪时不应显示先打开浏览器提示")
					}
					return
				}
				if !p.startHint.Visible() || !strings.Contains(p.startHint.Text, tc.wantHint) {
					t.Fatalf("开始提示与紧凑状态不一致：visible=%v text=%q", p.startHint.Visible(), p.startHint.Text)
				}
			})
		})
	}
}

// TestPageCourseCompactStatusRunningText 运行中文案由 startRun/pause 显式设置，不依赖状态刷新分支。
func TestPageCourseCompactStatusRunningText(t *testing.T) {
	f := ui.NewFakeBackend()
	f.StepDelay = 200 * time.Millisecond
	backend := &courseStatusBackend{
		Backend: f,
		status:  ui.CourseStatus{Connected: true, Pages: 2, ActiveLists: 1, Playwright: true},
	}
	app := test.NewApp()
	defer app.Quit()
	s := newTestShell(t, app, backend, Options{DisableTray: true, DisableScheduler: true})
	p := s.pages["course_autoplay"].(*pageCourse)
	s.Navigate("course_autoplay")
	waitShellCond(t, "开始学习可用", func() bool {
		var disabled bool
		runUI(func() { disabled = p.startBtn.Disabled() })
		return !disabled
	})
	runUI(func() { p.startRun() })
	waitShellCond(t, "进入学习态", func() bool {
		var running bool
		runUI(func() { running = p.running })
		return running
	})
	var text string
	runUI(func() { text = p.compactStatus.label.Text })
	if !strings.Contains(text, "正在学习") {
		t.Fatalf("运行中紧凑状态未显示学习中文案：%q", text)
	}
	runUI(func() { p.pause() })
	waitShellCond(t, "暂停收口", func() bool {
		var running bool
		runUI(func() { running = p.running })
		return !running
	})
}

// courseFailBackend 可控课程状态读取失败与取消失败，覆盖暂停失败/终态回滚。
type courseFailBackend struct {
	ui.Backend
	status     ui.CourseStatus
	failStatus atomic.Bool
	cancelErr  error
}

func (b *courseFailBackend) CourseStatus(context.Context) (ui.CourseStatus, error) {
	if b.failStatus.Load() {
		return ui.CourseStatus{}, errors.New("课程状态读取失败")
	}
	return b.status, nil
}

func (b *courseFailBackend) CancelJob(ctx context.Context, jobID string) error {
	if b.cancelErr != nil {
		return b.cancelErr
	}
	return b.Backend.CancelJob(ctx, jobID)
}

// TestPageCoursePauseFailureRestoresRunningCompact 暂停请求失败时不能停留在「正在暂停」，
// 必须按 scanMode 恢复运行中文案。
func TestPageCoursePauseFailureRestoresRunningCompact(t *testing.T) {
	f := ui.NewFakeBackend()
	f.StepDelay = 300 * time.Millisecond
	backend := &courseFailBackend{
		Backend:   f,
		status:    ui.CourseStatus{Connected: true, Pages: 2, ActiveLists: 1, Playwright: true},
		cancelErr: errors.New("取消请求被拒绝"),
	}
	app := test.NewApp()
	defer app.Quit()
	s := newTestShell(t, app, backend, Options{DisableTray: true, DisableScheduler: true})
	p := s.pages["course_autoplay"].(*pageCourse)
	s.Navigate("course_autoplay")
	waitShellCond(t, "开始学习可用", func() bool {
		var disabled bool
		runUI(func() { disabled = p.startBtn.Disabled() })
		return !disabled
	})
	runUI(func() { p.startRun() })
	waitShellCond(t, "进入学习态", func() bool {
		var running bool
		runUI(func() { running = p.running })
		return running
	})
	runUI(func() { p.pause() })
	waitShellCond(t, "暂停失败回填", func() bool {
		var text string
		runUI(func() { text = p.pauseHint.Text })
		return strings.Contains(text, "暂停失败")
	})
	var compact string
	runUI(func() { compact = p.compactStatus.label.Text })
	if strings.Contains(compact, "正在暂停") || !strings.Contains(compact, "正在学习") {
		t.Fatalf("暂停失败后紧凑状态未回滚到运行文案：%q", compact)
	}
	waitShellCond(t, "失败后任务最终收口", func() bool {
		var running bool
		runUI(func() { running = p.running })
		return !running
	})
}

// TestPageCourseTerminalCompactSurvivesStatusReadFailure 终态后即使 CourseStatus 读取失败，
// 也不能把「正在学习/正在暂停」留在紧凑状态。
func TestPageCourseTerminalCompactSurvivesStatusReadFailure(t *testing.T) {
	f := ui.NewFakeBackend()
	f.StepDelay = 100 * time.Millisecond
	backend := &courseFailBackend{
		Backend: f,
		status:  ui.CourseStatus{Connected: true, Pages: 2, ActiveLists: 1, Playwright: true},
	}
	app := test.NewApp()
	defer app.Quit()
	s := newTestShell(t, app, backend, Options{DisableTray: true, DisableScheduler: true})
	p := s.pages["course_autoplay"].(*pageCourse)
	s.Navigate("course_autoplay")
	waitShellCond(t, "开始学习可用", func() bool {
		var disabled bool
		runUI(func() { disabled = p.startBtn.Disabled() })
		return !disabled
	})
	runUI(func() {
		p.scanOnly.SetChecked(true)
		p.startRun()
	})
	waitShellCond(t, "进入扫描态", func() bool {
		var running bool
		runUI(func() { running = p.running })
		return running
	})
	backend.failStatus.Store(true)
	waitShellCond(t, "扫描终态与状态读取失败", func() bool {
		var running bool
		var text string
		runUI(func() {
			running = p.running
			text = p.compactStatus.label.Text
		})
		return !running && !strings.Contains(text, "正在")
	})
	var compact string
	runUI(func() { compact = p.compactStatus.label.Text })
	if !strings.Contains(compact, "扫描完成") {
		t.Fatalf("终态紧凑状态未回退到任务结论：%q", compact)
	}
}

// TestPageSplitLayout_NarrowStacksRightBelowLeft 窄于 600 时右列按原顺序叠到左列下方，
// 不重叠，且 MinSize 与单列布局一致。
func TestPageSplitLayout_NarrowStacksRightBelowLeft(t *testing.T) {
	first := sizedRect(100, 40)
	second := sizedRect(120, 50)
	third := sizedRect(140, 60)
	layout := &pageSplitLayout{gap: 12, normalSplit: 1, maxSplit: 1}
	cards := container.New(layout, first, second, third)
	cards.Resize(fyne.NewSize(500, 400))

	wantY := float32(0)
	for i, card := range []fyne.CanvasObject{first, second, third} {
		if card.Position().Y != wantY {
			t.Fatalf("窄宽第 %d 张卡 Y=%.1f，期望 %.1f（右列应顺序下叠）", i, card.Position().Y, wantY)
		}
		if card.Position().X != 0 || card.Size().Width != 500 {
			t.Fatalf("窄宽第 %d 张卡应占满整行：pos=%v size=%v", i, card.Position(), card.Size())
		}
		wantY += card.MinSize().Height + 12
	}
	wantHeight := first.MinSize().Height + 12 + second.MinSize().Height + 12 + third.MinSize().Height
	if got := layout.minSizeAt(500, cards.Objects).Height; got != wantHeight {
		t.Fatalf("窄宽纯函数 MinSize 高度 %.1f 与单列布局不一致，期望 %.1f", got, wantHeight)
	}
	if got := layout.MinSize(cards.Objects).Height; got != wantHeight {
		t.Fatalf("窄宽 MinSize 高度 %.1f 与单列布局不一致，期望 %.1f", got, wantHeight)
	}
	if got := cards.MinSize().Height; got != wantHeight {
		t.Fatalf("窄宽容器 MinSize 高度 %.1f，期望 %.1f", got, wantHeight)
	}
}

// TestHomeQuickLayout_NarrowMinSizeUsesSingleColumn 窄宽下单列排版时 MinSize 必须是单列总高，
// 否则六行快捷入口会溢出卡片。
func TestHomeQuickLayout_NarrowMinSizeUsesSingleColumn(t *testing.T) {
	layout := &homeQuickLayout{gap: 12}
	rows := make([]fyne.CanvasObject, 6)
	for i := range rows {
		rows[i] = sizedRect(100, 50)
	}
	box := container.New(layout, rows...)
	box.Resize(fyne.NewSize(500, 500))
	for i, row := range rows {
		wantY := float32(i) * (50 + 12)
		if row.Position().Y != wantY || row.Position().X != 0 {
			t.Fatalf("窄宽快捷入口第 %d 行位置错误：%v，期望 Y=%.1f", i, row.Position(), wantY)
		}
	}
	wantHeight := float32(6*50 + 5*12)
	if got := layout.MinSize(box.Objects).Height; got != wantHeight {
		t.Fatalf("窄宽快捷入口 MinSize 高度 %.1f，期望单列总高 %.1f", got, wantHeight)
	}
	if got := box.MinSize().Height; got != wantHeight {
		t.Fatalf("窄宽快捷入口容器 MinSize 高度 %.1f，期望 %.1f", got, wantHeight)
	}
}

// TestHomeQuickLayout_WideUsesThreeColumns 600px 以上六项入口按三列两行排列，MinSize 同步为两行高度。
func TestHomeQuickLayout_WideUsesThreeColumns(t *testing.T) {
	layout := &homeQuickLayout{gap: 12}
	rows := make([]fyne.CanvasObject, 6)
	for i := range rows {
		rows[i] = sizedRect(100, 50)
	}
	box := container.New(layout, rows...)
	box.Resize(fyne.NewSize(900, 500))
	for i := 0; i < 3; i++ {
		if rows[i].Position().Y != 0 {
			t.Fatalf("前三项应在同一行：第 %d 行 Y=%.1f", i, rows[i].Position().Y)
		}
		if i > 0 && rows[i].Position().X <= rows[i-1].Position().X {
			t.Fatalf("第三列 X 应递增：%v", rows[i].Position())
		}
	}
	if rows[3].Position().Y <= rows[0].Position().Y {
		t.Fatalf("第 4 项应换到第二行：first=%v fourth=%v", rows[0].Position(), rows[3].Position())
	}
	wantWidth, wantHeight := float32(3*100+2*12), float32(2*50+12)
	if got := layout.MinSize(box.Objects); got != fyne.NewSize(wantWidth, wantHeight) {
		t.Fatalf("三列 MinSize=%v，期望 %v", got, fyne.NewSize(wantWidth, wantHeight))
	}
}

// TestToolPairLayout_NarrowMinSizeUsesStackedHeight 双卡布局在单列宽度下的 MinSize 必须为叠列总高。
func TestToolPairLayout_NarrowMinSizeUsesStackedHeight(t *testing.T) {
	first := sizedRect(100, 40)
	second := sizedRect(120, 50)
	layout := &toolPairLayout{breakAt: 520, gap: 16}
	pair := container.New(layout, first, second)
	pair.Resize(fyne.NewSize(400, 300))
	if first.Position().Y != 0 || second.Position().Y != 56 {
		t.Fatalf("窄宽双卡未顺序下叠：first=%v second=%v", first.Position(), second.Position())
	}
	if got, want := layout.MinSize(pair.Objects).Height, float32(40+16+50); got != want {
		t.Fatalf("窄宽双卡 MinSize 高度 %.1f，期望 %.1f", got, want)
	}
}

func sizedRect(width, height float32) fyne.CanvasObject {
	rect := canvas.NewRectangle(nil)
	rect.SetMinSize(fyne.NewSize(width, height))
	return rect
}

// TestPageHomeWindowMode_QuickThreeColumnsAndRecentAside
// 常用入口三列紧凑（六项两行）、常规描述单行省略；最近结果仅最大化右栏显示，隐藏时不占 MinSize。
func TestPageHomeWindowMode_QuickThreeColumnsAndRecentAside(t *testing.T) {
	f := ui.NewFakeBackend()
	app := test.NewApp()
	defer app.Quit()
	s := newTestShell(t, app, f, Options{DisableTray: true, DisableScheduler: true})
	p := s.pages["home"].(*pageHome)
	for _, task := range homeTaskList() {
		s.toggleFavorite(task.id)
	}
	runUI(func() { p.quick.refresh() })
	resizePageViewport(t, p, pageNormalViewportW, pageNormalViewportH)
	assertCardFitsViewport(t, "首页特色卡", p.pair, pageNormalViewportH)

	if len(p.quick.rows) != 6 {
		t.Fatalf("六项常用入口应全部保留：%d", len(p.quick.rows))
	}
	first, second, third, fourth := p.quick.rows[0], p.quick.rows[1], p.quick.rows[2], p.quick.rows[3]
	if first.Position().X != 0 || second.Position().X <= first.Size().Width || third.Position().X <= second.Position().X {
		t.Fatalf("常规常用入口未按三列排列：%v %v %v", first.Position(), second.Position(), third.Position())
	}
	if fourth.Position().Y <= first.Position().Y {
		t.Fatalf("六项入口应在三列下排成两行：first=%v fourth=%v", first.Position(), fourth.Position())
	}
	if !first.descNormal.Visible() || first.descFull.Visible() {
		t.Fatal("常规应只有单行省略描述")
	}
	if first.descNormal.Wrapping != fyne.TextWrapOff || first.descNormal.Truncation != fyne.TextTruncateEllipsis {
		t.Fatalf("常规描述应单行省略：wrap=%v trunc=%v", first.descNormal.Wrapping, first.descNormal.Truncation)
	}
	assertShown(t, "常规隐藏最近结果", p.pane.mainScroll, p.views[0].root, false)
	if got, want := p.bottom.MinSize(), p.bottom.background.MinSize(); got != want {
		t.Fatalf("隐藏最近结果仍占 MinSize：got=%v want=%v", got, want)
	}

	runUI(func() { p.SetMaximized(true) })
	resizePageViewport(t, p, 1440, pageNormalViewportH)
	assertShown(t, "最大化显示最近结果", p.pane.mainScroll, p.views[0].root, true)
	if !first.descFull.Visible() || first.descNormal.Visible() {
		t.Fatal("最大化应显示完整描述")
	}
	if first.descFull.Wrapping != fyne.TextWrapBreak {
		t.Fatalf("最大化描述应完整换行：%v", first.descFull.Wrapping)
	}
	if p.quick.rows[0].Position().X != 0 || p.quick.rows[1].Position().X <= p.quick.rows[0].Size().Width ||
		p.quick.rows[2].Position().X <= p.quick.rows[1].Position().X {
		t.Fatal("最大化常用入口应保持三列")
	}

	runUI(func() { p.SetMaximized(false) })
	assertShown(t, "还原再次隐藏最近结果", p.pane.mainScroll, p.views[0].root, false)
	if !first.descNormal.Visible() || first.descFull.Visible() {
		t.Fatal("还原后应回到单行省略描述")
	}
}

// TestPageWindowModes_PrimaryCardsFitNormalViewport 1160×780 时左右两列的主输入/主执行卡均在视口内，
// 题库使用真实 1 个启用题库，而不是空题库占位。
func TestPageWindowModes_PrimaryCardsFitNormalViewport(t *testing.T) {
	f := ui.NewFakeBackend()
	if _, err := f.QuizImportBank(context.Background(), "安规.xlsx"); err != nil {
		t.Fatal(err)
	}
	app := test.NewApp()
	defer app.Quit()
	s := newTestShell(t, app, f, Options{DisableTray: true, DisableScheduler: true})
	s.Navigate("quiz_search")
	waitShellCond(t, "题库真实回填", func() bool {
		var n int
		runUI(func() { n = len(s.pages["quiz_search"].(*pageQuiz).bankList.Objects) })
		return n > 0
	})

	pcd := s.pages["pcd_export"].(*pagePcd)
	resizePageViewport(t, pcd, pageNormalViewportW, pageNormalViewportH)
	assertCardsFitViewport(t, "轨迹导出左列", pcd.cardBox.Objects[0:2], pageNormalViewportH)
	assertCardFitsViewport(t, "轨迹导出右列开始导出", pcd.startCard, pageNormalViewportH)
	assertShown(t, "轨迹导出空闲进度", pcd.cardBox, pcd.progress.Root, false)

	quiz := s.pages["quiz_search"].(*pageQuiz)
	resizePageViewport(t, quiz, pageNormalViewportW, pageNormalViewportH)
	assertCardFitsViewport(t, "题库 1 题库卡", quiz.bankCard, pageNormalViewportH)
	assertCardFitsViewport(t, "题库 1 开始搜题", quiz.startCard, pageNormalViewportH)

	course := s.pages["course_autoplay"].(*pageCourse)
	resizePageViewport(t, course, pageNormalViewportW, pageNormalViewportH)
	assertCardsFitViewport(t, "学堂左列", course.cardBox.Objects[0:2], pageNormalViewportH)
	assertCardFitsViewport(t, "学堂右列开始学习", course.startCard, pageNormalViewportH)
	assertShown(t, "学堂空闲进度", course.cardBox, course.progress.Root, false)
}

// TestPageWindowModes_QuizLongBanksScrollable 真实 18 题库时首屏放不下，必须可滚动到开始搜题卡。
func TestPageWindowModes_QuizLongBanksScrollable(t *testing.T) {
	f := ui.NewFakeBackend()
	for i := 0; i < 18; i++ {
		if _, err := f.QuizImportBank(context.Background(), fmt.Sprintf("题库-%02d.xlsx", i)); err != nil {
			t.Fatal(err)
		}
	}
	app := test.NewApp()
	defer app.Quit()
	s := newTestShell(t, app, f, Options{DisableTray: true, DisableScheduler: true})
	quiz := s.pages["quiz_search"].(*pageQuiz)
	s.Navigate("quiz_search")
	waitShellCond(t, "18 个题库回填", func() bool {
		var n int
		runUI(func() { n = len(quiz.bankList.Objects) })
		return n >= 18
	})
	resizePageViewport(t, quiz, pageNormalViewportW, pageNormalViewportH)
	scroll := pageScrollOf(t, quiz)
	if scroll.Content.MinSize().Height <= pageNormalViewportH {
		t.Fatalf("18 题库内容高度 %.1f 应超过视口", scroll.Content.MinSize().Height)
	}
	target := quiz.startCard.Position().Y
	if maxOffset := scroll.Content.MinSize().Height - pageNormalViewportH; target > maxOffset {
		target = maxOffset
	}
	runUI(func() { scroll.ScrollToOffset(fyne.NewPos(0, target)) })
	top := quiz.startCard.Position().Y - scroll.Offset.Y
	if top < 0 || top+quiz.startCard.Size().Height > pageNormalViewportH {
		t.Fatalf("滚动后开始搜题卡不可见：top=%.1f bottom=%.1f offset=%.1f", top, top+quiz.startCard.Size().Height, scroll.Offset.Y)
	}
}

// TestPageWindowModes_MinimumWindowKeepsPrimaryReachable 900×600 关键输入与主执行可达，滚动兜底可用。
func TestPageWindowModes_MinimumWindowKeepsPrimaryReachable(t *testing.T) {
	f := ui.NewFakeBackend()
	app := test.NewApp()
	defer app.Quit()
	s := newTestShell(t, app, f, Options{DisableTray: true, DisableScheduler: true})

	for _, id := range []string{"home", "pcd_export", "quiz_search", "course_autoplay"} {
		page := s.pages[id]
		resizePageViewport(t, page, pageMinViewportW, pageMinViewportH)
		scroll := pageScrollOf(t, page)
		content := scroll.Content
		if home, ok := page.(*pageHome); ok {
			content = home.pane.mainScroll.Content
		}
		if got := content.MinSize().Width; got > pageMinViewportW {
			t.Fatalf("%s 在 900×600 下内容最小宽度 %.1f 超视口 %.1f，关键控件会被裁切", id, got, pageMinViewportW)
		}
		switch p := page.(type) {
		case *pageHome:
			assertShown(t, "900×600 首页常用入口", p.pane.mainScroll, p.quick.root, true)
			if len(p.quick.rows) == 0 {
				for _, task := range homeTaskList() {
					s.toggleFavorite(task.id)
				}
				runUI(func() { p.quick.refresh() })
				resizePageViewport(t, page, pageMinViewportW, pageMinViewportH)
			}
			if got := p.pane.mainScroll.Content.MinSize().Width; got > pageMinViewportW {
				t.Fatalf("900×600 六项常用入口内容最小宽度 %.1f 超视口 %.1f", got, pageMinViewportW)
			}
			for i, row := range p.quick.rows {
				titleRight := row.title.Position().X + row.title.Size().Width
				if titleRight > row.actions.Position().X+0.5 {
					t.Fatalf("900×600 常用入口第 %d 行标题与操作重叠：titleRight=%.1f actionsLeft=%.1f", i, titleRight, row.actions.Position().X)
				}
			}
		case *pagePcd:
			assertShown(t, "900×600 开始导出", p.cardBox, p.startBtn, true)
			assertCardWidthsFit(t, "900×600 轨迹导出", p.cardBox.Objects, (pageMinViewportW-p.layout.gap)/2)
		case *pageQuiz:
			assertShown(t, "900×600 开始搜题", p.cardBox, p.scanBtn, true)
		case *pageCourse:
			assertShown(t, "900×600 开始学习", p.cardBox, p.startBtn, true)
			assertCardWidthsFit(t, "900×600 学堂", p.cardBox.Objects, (pageMinViewportW-p.layout.gap)/2)
		}
		if scroll.Offset.Y != 0 && scroll.Content.Position().Y != 0 {
			t.Fatalf("%s 初始不应带滚动偏移：%v / %v", id, scroll.Offset, scroll.Content.Position())
		}
		if scroll.Content.MinSize().Height > pageMinViewportH {
			runUI(func() { scroll.ScrollToBottom() })
			if scroll.Offset.Y <= 0 {
				t.Fatalf("%s 在 900×600 下无法滚动到底", id)
			}
			runUI(func() { scroll.ScrollToTop() })
			if scroll.Offset.Y != 0 {
				t.Fatalf("%s 无法回到顶部", id)
			}
		}
	}
}

// TestPageWindowModes_TogglePreservesStateAndSkipsBackend 最大化/还原不重建、不清输入/任务、不增加 Backend 调用。
func TestPageWindowModes_TogglePreservesStateAndSkipsBackend(t *testing.T) {
	fake := ui.NewFakeBackend()
	guard := &heavyGuardBackend{Backend: fake}
	app := test.NewApp()
	defer app.Quit()
	s := newTestShell(t, app, guard, Options{DisableTray: true, DisableScheduler: true})
	home := s.pages["home"].(*pageHome)
	pcd := s.pages["pcd_export"].(*pagePcd)
	quiz := s.pages["quiz_search"].(*pageQuiz)
	course := s.pages["course_autoplay"].(*pageCourse)
	waitShellCond(t, "首次首页回填", func() bool {
		var text string
		runUI(func() { text = home.cards["daily_sync"].status.Text })
		return text != "正在读取状态…"
	})
	time.Sleep(250 * time.Millisecond)
	guard.Reset()

	job := &ui.JobHandle{}
	runUI(func() {
		pcd.codesText.SetText("KEEP-1\nKEEP-2")
		pcd.outdir.SetText("D:/keep")
		pcd.currentJob = job
		quiz.testText.SetText("保留题干")
		quiz.optionsSec.SetExpanded(true)
		quiz.diagSec.SetExpanded(false)
		course.startURL.SetText("https://keep.example/")
		course.advancedSec.SetExpanded(true)
		course.maxTry.SetText("7")
	})
	pagesBefore := make(map[string]Page, len(s.pages))
	for id, page := range s.pages {
		pagesBefore[id] = page
	}
	optionsBefore := quiz.optionsSec.Expanded()
	diagBefore := quiz.diagSec.Expanded()
	advancedBefore := course.advancedSec.Expanded()

	runUI(func() {
		s.setWindowMaximized(true)
		s.setWindowMaximized(false)
	})
	if calls := guard.systemInfoCalls.Load() + guard.appStatusCalls.Load() + guard.quizStateCalls.Load() +
		guard.courseStateCalls.Load() + guard.getConfigCalls.Load() + guard.historyCalls.Load() + guard.pingCalls.Load(); calls != 0 {
		t.Fatalf("最大化切换读取了 Backend：%d", calls)
	}
	for id, before := range pagesBefore {
		if s.pages[id] != before {
			t.Fatalf("页面 %s 被重建", id)
		}
	}
	runUI(func() {
		if pcd.codesText.Text != "KEEP-1\nKEEP-2" || pcd.outdir.Text != "D:/keep" {
			t.Fatalf("轨迹导出输入被修改：%q / %q", pcd.codesText.Text, pcd.outdir.Text)
		}
		if quiz.testText.Text != "保留题干" {
			t.Fatalf("题库题干输入被修改：%q", quiz.testText.Text)
		}
		if course.startURL.Text != "https://keep.example/" || course.maxTry.Text != "7" {
			t.Fatalf("课程输入被修改：%q / %q", course.startURL.Text, course.maxTry.Text)
		}
	})
	if pcd.currentJob != job {
		t.Fatal("当前任务句柄被替换")
	}
	if quiz.optionsSec.Expanded() != optionsBefore || quiz.diagSec.Expanded() != diagBefore {
		t.Fatal("题库折叠状态被修改")
	}
	if course.advancedSec.Expanded() != advancedBefore {
		t.Fatal("课程高级设置折叠状态被修改")
	}
}
