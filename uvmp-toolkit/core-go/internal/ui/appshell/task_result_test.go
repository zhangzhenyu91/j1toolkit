package appshell

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"fyne.io/fyne/v2/test"

	"github.com/zhangzhenyu91/j1toolkit/uvmp-toolkit/core-go/internal/ui"
)

// 本文件：五业务页结果区/折叠日志/产物入口的 fake 测试。

// TestTaskOutcomeSummaries 结果摘要纯函数：完成信息、产物入口与失败标记。
func TestTaskOutcomeSummaries(t *testing.T) {
	t.Run("派车成功带产物目录", func(t *testing.T) {
		o := dailyOutcome(ui.Job{Status: ui.JobSuccess, Result: map[string]any{
			"count": 6, "file": "2026-10-28-派车单.xlsx", "dest": "U盘", "dir": "mem://usb",
		}})
		if o.Status != "成功" || o.Failed {
			t.Fatalf("状态不符: %+v", o)
		}
		for _, want := range []string{"6 条", "2026-10-28-派车单.xlsx", "U盘"} {
			if !strings.Contains(o.Summary, want) {
				t.Fatalf("摘要缺 %q: %s", want, o.Summary)
			}
		}
		if o.Artifact.Path != "mem://usb" || o.Artifact.Label != "打开产物目录" {
			t.Fatalf("产物入口不符: %+v", o.Artifact)
		}
	})

	t.Run("派车失败摘要取错误并标记展开日志", func(t *testing.T) {
		o := dailyOutcome(ui.Job{Status: ui.JobFailed, Error: "SSO 登录失败"})
		if !o.Failed || !strings.Contains(o.Summary, "SSO 登录失败") {
			t.Fatalf("失败结果不符: %+v", o)
		}
	})

	t.Run("树障快照指向U盘", func(t *testing.T) {
		o := syncOutcome(ui.Job{Status: ui.JobSuccess, Result: map[string]any{
			"kind": "snapshot", "cycle_id": "c-1", "file": "统计表.xlsx",
		}}, "/media/GLKVM", "D:/台账/树障.xlsx")
		if !strings.Contains(o.Summary, "拷贝到 U 盘") || !strings.Contains(o.Summary, "统计表.xlsx") {
			t.Fatalf("快照摘要不符: %s", o.Summary)
		}
		if o.Artifact.Path != "/media/GLKVM" || o.Artifact.Label != "打开 U 盘目录" {
			t.Fatalf("快照产物不符: %+v", o.Artifact)
		}
	})

	t.Run("交跨替换指向内网表目录", func(t *testing.T) {
		o := syncOutcome(ui.Job{Status: ui.JobSuccess, Result: map[string]any{
			"kind": "replace", "file": "交跨.xlsx",
		}}, "/media/GLKVM", "D:/台账/交跨.xlsx")
		if !strings.Contains(o.Summary, "覆盖内网表") || !strings.Contains(o.Summary, ".bak") {
			t.Fatalf("替换摘要不符: %s", o.Summary)
		}
		if o.Artifact.Path != filepath.Dir("D:/台账/交跨.xlsx") || o.Artifact.Label != "打开内网表目录" {
			t.Fatalf("替换产物不符: %+v", o.Artifact)
		}
	})

	t.Run("树障无指令跳过不算失败", func(t *testing.T) {
		o := syncOutcome(ui.Job{Status: ui.JobSuccess, Result: map[string]any{"skipped": "no_command"}}, "/media/GLKVM", "")
		if o.Failed || !strings.Contains(o.Summary, "无同步指令") {
			t.Fatalf("跳过结果不符: %+v", o)
		}
		if o.Artifact.Path != "" {
			t.Fatalf("跳过不应给产物入口: %+v", o.Artifact)
		}
	})

	t.Run("轨迹导出计数与目录", func(t *testing.T) {
		o := pcdOutcome(ui.Job{Status: ui.JobSuccess, Result: map[string]any{
			"total": 3, "pcd_ok": 3, "pcd_fail": 0, "track_ok": 2, "track_fail": 1,
			"not_found": []any{"PC9"}, "dir": "mem://output/批次_导出",
		}})
		for _, want := range []string{"共处理 3 单", "派车单成功 3", "轨迹成功 2", "未找到 1 单"} {
			if !strings.Contains(o.Summary, want) {
				t.Fatalf("摘要缺 %q: %s", want, o.Summary)
			}
		}
		if o.Artifact.Path != "mem://output/批次_导出" {
			t.Fatalf("产物目录不符: %+v", o.Artifact)
		}
	})

	t.Run("课程暂停与扫描阶段", func(t *testing.T) {
		paused := courseOutcome(ui.Job{Status: ui.JobCancelled, Result: map[string]any{
			"paused": true, "total": 10, "completed": 4, "pending": 6, "processed": 4,
		}}, false)
		if paused.Status != "已暂停" || !strings.Contains(paused.Summary, "可稍后继续") {
			t.Fatalf("暂停结果不符: %+v", paused)
		}
		scan := courseOutcome(ui.Job{Status: ui.JobSuccess, Result: map[string]any{
			"total": 10, "completed": 10, "pending": 0,
		}}, true)
		if !strings.Contains(scan.Summary, "扫描完成") {
			t.Fatalf("扫描结果不符: %s", scan.Summary)
		}
		pending := courseOutcome(ui.Job{Status: ui.JobSuccess, Result: map[string]any{
			"total": 10, "completed": 8, "pending": 2,
		}}, false)
		if !strings.Contains(pending.Summary, "仍有未完成") {
			t.Fatalf("未完成结果不符: %s", pending.Summary)
		}
	})
}

// TestTaskResult_FoldLogAndOpen 结果区：日志默认收起、失败自动展开、产物按钮打开目录。
func TestTaskResult_FoldLogAndOpen(t *testing.T) {
	var opened []string
	r := newTaskResult(func(path string) { opened = append(opened, path) })

	r.Begin("正在导出…")
	if r.logSec.Expanded() {
		t.Fatal("新一轮执行时运行日志应默认收起")
	}
	r.Log("步骤 1/3")
	if len(r.log.Lines()) != 1 {
		t.Fatalf("日志未写入: %v", r.log.Lines())
	}

	r.Apply(dailyOutcome(ui.Job{Status: ui.JobSuccess, Result: map[string]any{
		"count": 6, "file": "2026-10-28-派车单.xlsx", "dest": "U盘", "dir": "mem://usb",
	}}))
	if r.logSec.Expanded() {
		t.Fatal("成功后运行日志应保持收起")
	}
	if !r.summary.Visible() || !strings.Contains(r.summary.Text, "导出完成") {
		t.Fatalf("完成摘要未展示: %q", r.summary.Text)
	}
	if !r.artifactBtn.Visible() {
		t.Fatal("有产物目录时应显示打开按钮")
	}
	r.artifactBtn.OnTapped()
	if len(opened) != 1 || opened[0] != "mem://usb" {
		t.Fatalf("产物打开回调不符: %v", opened)
	}

	// 失败自动展开运行日志；再次执行收起并清空
	r.Apply(taskOutcome{Status: "失败", Summary: "导出失败：网络中断", Failed: true})
	if !r.logSec.Expanded() {
		t.Fatal("失败时运行日志应自动展开")
	}
	if r.artifactBtn.Visible() {
		t.Fatal("失败无产物时不应显示打开按钮")
	}
	r.Apply(taskOutcome{Status: "已取消", Summary: "已取消"})
	if r.logSec.Expanded() {
		t.Fatal("取消后运行日志应收起")
	}
	r.Begin("重新开始")
	if len(r.log.Lines()) != 0 || r.summary.Text == "" {
		t.Fatalf("重新开始时日志应清空、状态应更新: lines=%d summary=%q", len(r.log.Lines()), r.summary.Text)
	}

	// 启动失败：错误写入日志并自动展开
	r.FailStart(errors.New("配置缺失"))
	if !r.logSec.Expanded() {
		t.Fatal("启动失败时运行日志应自动展开")
	}
	if !strings.Contains(strings.Join(r.log.Lines(), "\n"), "配置缺失") {
		t.Fatalf("启动失败应写入运行日志: %v", r.log.Lines())
	}
}

// TestPcdExport_KeepsResultSummary 轨迹导出终态保留完成摘要与产物目录入口（不再只剩执行记录）。
func TestPcdExport_KeepsResultSummary(t *testing.T) {
	f := ui.NewFakeBackend()
	f.StepDelay = time.Millisecond
	ctx := context.Background()
	app := test.NewApp()
	defer app.Quit()
	s := newTestShell(t, app, f, Options{DisableTray: true, DisableScheduler: true})
	p := s.pages["pcd_export"].(*pagePcd)
	s.Navigate("pcd_export")
	time.Sleep(50 * time.Millisecond)

	if err := f.SaveConfig(ctx, ui.ConfigPatch{Dev: &ui.DevConfig{Mock: true}}); err != nil {
		t.Fatal(err)
	}
	runUI(func() {
		p.codes = []map[string]any{{"seq": 1, "code": "PC001"}, {"seq": 2, "code": "PC002"}}
		p.batchName = "粘贴文本"
		p.outdir.SetText("mem://out-batch")
		p.startRun()
	})
	waitShellCond(t, "导出完成摘要", func() bool {
		var text string
		runUI(func() { text = p.result.summary.Text })
		return strings.Contains(text, "导出完成")
	})

	var summary, status string
	var openVisible bool
	runUI(func() {
		summary = p.result.summary.Text
		status = p.result.status.Text
		openVisible = p.result.artifactBtn.Visible()
	})
	if status != "成功" {
		t.Fatalf("终态状态不符: %q", status)
	}
	for _, want := range []string{"共处理 3 单", "派车单成功 3", "轨迹成功 2"} {
		if !strings.Contains(summary, want) {
			t.Fatalf("终态摘要缺 %q: %s", want, summary)
		}
	}
	if !openVisible {
		t.Fatal("终态应显示「打开产物目录」")
	}

	runUI(func() { p.result.artifactBtn.OnTapped() })
	waitFakeCond(t, "打开产物目录", func() bool {
		for _, opened := range f.Opened() {
			if opened == "open:mem://output/批次_导出" {
				return true
			}
		}
		return false
	})
}

// TestQuizDiag_AutoExpandOnError 搜题诊断折叠：默认收起，引擎/截屏异常自动展开。
func TestQuizDiag_AutoExpandOnError(t *testing.T) {
	f := ui.NewFakeBackend()
	app := test.NewApp()
	defer app.Quit()
	s := newTestShell(t, app, f, Options{DisableTray: true, DisableScheduler: true})
	pq := s.pages["quiz_search"].(*pageQuiz)
	s.Navigate("quiz_search")
	time.Sleep(50 * time.Millisecond)

	base := ui.QuizScanState{
		Interval: 1000, Threshold: 0.6, Opacity: 0.9, QuestionCount: 3,
		BoxVisible: map[string]bool{"A": true, "B": true},
	}
	runUI(func() { pq.applyScanState(base) })
	if pq.diagSec.Expanded() {
		t.Fatal("无异常时运行诊断应默认收起")
	}

	bad := base
	bad.EngineError = "模型缺失"
	runUI(func() { pq.applyScanState(bad) })
	if !pq.diagSec.Expanded() {
		t.Fatal("引擎异常时运行诊断应自动展开")
	}
	if !strings.Contains(pq.scanDiag.Text, "模型缺失") {
		t.Fatalf("诊断文案未带错误: %s", pq.scanDiag.Text)
	}
}
