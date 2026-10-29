package appshell

import (
	"context"
	"strings"
	"testing"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/test"

	"github.com/zhangzhenyu91/j1toolkit/uvmp-toolkit/core-go/internal/ui"
)

// AppShell + 8 页 + FakeBackend 冒烟：真实构造壳体与页面、切页、跑任务事件流。
// 仅依赖 fyne 测试驱动（无需显示器/窗口系统）。

// newTestShell 在 UI 执行体内构造壳体：构造期 Navigate 的 Async 回填不得与
// 构造收尾的 SetContent 布局并发触碰控件（测试驱动内联语义下经 runUI 串行）。
func newTestShell(t *testing.T, app fyne.App, f ui.Backend, opt Options) *Shell {
	t.Helper()
	app.Settings().SetTheme(NewTheme()) // 与 main.go 一致：自定义字号/颜色 token 需要壳层主题
	var s *Shell
	var err error
	runUI(func() {
		s, err = NewShell(app, f, opt)
	})
	if err != nil {
		t.Fatalf("NewShell: %v", err)
	}
	return s
}

func TestShellSmoke_WithFakeBackend(t *testing.T) {
	f := ui.NewFakeBackend()
	f.StepDelay = time.Millisecond
	app := test.NewApp()
	defer app.Quit()

	s := newTestShell(t, app, f, Options{DisableTray: true, DisableScheduler: true})
	if s.RunningJobs() != 0 {
		t.Fatalf("初始任务数应为 0: %d", s.RunningJobs())
	}

	// 8 页常驻且可切（每页停留片刻：OnShow 的 Async 回填经 runUI 串行，
	// 与下一动作并发触碰控件会撞 Fyne 内部缓存，已踩坑）
	for _, id := range []string{"home", "daily_sync", "tree_sync", "crossing_sync", "pcd_export", "quiz_search", "course_autoplay", "settings"} {
		p, ok := s.pages[id]
		if !ok {
			t.Fatalf("缺少页面 %s", id)
		}
		if p.Object() == nil {
			t.Fatalf("页面 %s 根对象为空", id)
		}
		s.Navigate(id)
		if s.current != id {
			t.Fatalf("切页失败：%s → %s", id, s.current)
		}
		time.Sleep(40 * time.Millisecond)
	}

	// 经 StartJob 跑通事件流（日志→进度→终态）
	logCh := make(chan string, 16)
	doneCh := make(chan ui.Job, 1)
	h := s.StartJob("daily_sync", map[string]any{}, ui.JobHandler{
		OnLog:  func(line string) { logCh <- line },
		OnDone: func(job ui.Job) { doneCh <- job },
	})
	select {
	case job := <-doneCh:
		if job.Status != ui.JobSuccess {
			t.Fatalf("任务终态不符: %+v", job)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("等待任务终态超时")
	}
	select {
	case <-logCh:
	default:
		t.Fatal("未收到任务日志事件")
	}
	if h.ID() == "" {
		t.Fatal("任务 id 为空")
	}
	deadline := time.Now().Add(time.Second)
	for s.RunningJobs() != 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if s.RunningJobs() != 0 {
		t.Fatalf("任务结束后计数未归零: %d", s.RunningJobs())
	}

	// 关窗驻留策略：默认 close_to_tray=true，无托盘时仍应可退出
	if got := s.closePolicy().OnWindowClose(); got.String() == "" {
		t.Fatal("CloseAction 无文案")
	}
}

// TestShellSmoke_PageInteract 交互冒烟：直接驱动页面刷新与控件回调。
func TestShellSmoke_PageInteract(t *testing.T) {
	f := ui.NewFakeBackend()
	f.StepDelay = 0
	app := test.NewApp()
	defer app.Quit()
	s := newTestShell(t, app, f, Options{DisableTray: true, DisableScheduler: true})

	for _, id := range []string{"home", "daily_sync", "quiz_search", "course_autoplay", "settings"} {
		s.Navigate(id)
		time.Sleep(40 * time.Millisecond) // 让 OnShow 的 Async 回填先落地（避免与下一页并发触碰控件）
	}
	// 等后台刷新完成（Async 协程）
	time.Sleep(150 * time.Millisecond)

	// 题库：导入→匹配链路（直接打接口冒烟页面之外的数据通路）
	if _, err := f.QuizImportBank(nil, "安规.xlsx"); err != nil {
		t.Fatalf("QuizImportBank: %v", err)
	}
}

// TestShellSmoke_QuizScanControls 题库搜题页扫描控制冒烟：页面控件回调接 FakeBackend
// 扫描通路（启停/显隐/选项防抖/自检/诊断/重置），事件经订阅刷新状态区。
func TestShellSmoke_QuizScanControls(t *testing.T) {
	f := ui.NewFakeBackend()
	f.StepDelay = 0
	app := test.NewApp()
	defer app.Quit()
	s := newTestShell(t, app, f, Options{DisableTray: true, DisableScheduler: true})
	pq, ok := s.pages["quiz_search"].(*pageQuiz)
	if !ok {
		t.Fatalf("quiz 页类型不符: %T", s.pages["quiz_search"])
	}
	ctx := context.Background()

	// 无题库开始搜题：报错不进扫描态（页面控件调用一律经 runUI 串行）
	runUI(func() { pq.toggleScan() })
	time.Sleep(150 * time.Millisecond)
	if st, _ := f.QuizScanState(ctx); st.Scanning {
		t.Fatal("无题库不应进入扫描态")
	}

	if _, err := f.QuizImportBank(ctx, "安规.xlsx"); err != nil {
		t.Fatalf("QuizImportBank: %v", err)
	}
	runUI(func() { pq.toggleScan() })
	waitShellCond(t, "进入扫描态", func() bool {
		st, _ := f.QuizScanState(ctx)
		return st.Scanning
	})

	// A 框显隐（含 boxHidden 事件回流）
	runUI(func() { pq.setBoxVisible("A", false) })
	waitShellCond(t, "A 框隐藏", func() bool {
		st, _ := f.QuizScanState(ctx)
		return !st.BoxVisible["A"]
	})

	// 选项防抖落盘（400ms 静默后提交）
	runUI(func() { pq.thresholdSl.SetValue(0.8) })
	waitShellCond(t, "阈值落盘", func() bool {
		st, _ := f.QuizScanState(ctx)
		return st.Threshold == 0.8
	})

	// 引擎自检回填 / 保存题图 / 重置布局
	runUI(func() { pq.runSelftest() })
	waitShellCond(t, "自检回填", func() bool {
		var txt string
		runUI(func() { txt = pq.selfResult.Text })
		return strings.Contains(txt, "自检")
	})
	runUI(func() { pq.debugCapture() })
	runUI(func() { pq.resetBoxes() })
	waitShellCond(t, "双框重置回填", func() bool {
		st, _ := f.QuizScanState(ctx)
		return st.BoxVisible["A"] && st.BoxVisible["B"]
	})

	// 停止（等状态回填后再触发；偶发未回填时再补一次，保证确定性收口）
	time.Sleep(200 * time.Millisecond)
	runUI(func() { pq.toggleScan() })
	stopped := func() bool {
		st, _ := f.QuizScanState(ctx)
		return !st.Scanning
	}
	if !waitForShell(600*time.Millisecond, stopped) {
		runUI(func() { pq.toggleScan() })
		waitShellCond(t, "退出扫描态", stopped)
	}
}

// waitForShell 限时轮询条件。
func waitForShell(d time.Duration, cond func() bool) bool {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return false
}

// waitShellCond 轮询等待条件，超时即失败；达成后静置一拍，
// 让回填协程的 runUI 收尾后再做下一步操作。
func waitShellCond(t *testing.T, desc string, cond func() bool) {
	t.Helper()
	if !waitForShell(3*time.Second, cond) {
		t.Fatalf("timeout waiting for %s", desc)
	}
	time.Sleep(50 * time.Millisecond)
}
