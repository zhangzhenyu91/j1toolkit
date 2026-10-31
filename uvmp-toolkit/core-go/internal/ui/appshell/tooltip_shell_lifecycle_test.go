package appshell

import (
	"testing"
	"time"

	"fyne.io/fyne/v2/test"

	"github.com/zhangzhenyu91/j1toolkit/uvmp-toolkit/core-go/internal/ui"
)

func TestSettingsOldShellSelftestDoesNotClearNewWindowTooltip(t *testing.T) {
	fake := ui.NewFakeBackend()
	fake.StepDelay = time.Millisecond
	oldApp := test.NewApp()
	defer oldApp.Quit()
	oldShell := newTestShell(t, oldApp, fake, Options{DisableTray: true, DisableScheduler: true})
	p := oldShell.pages["settings"].(*pageSettings)
	oldShell.Navigate("settings")

	window, button, _ := newTooltipTestButton(t, nil)
	showAppButtonTooltip(t, window, button)
	runUI(func() {
		p.testPaths.Objects = nil
		p.runSelftest("cdp")
	})
	waitShellCond(t, "旧设置页自检产物完成回填", func() bool {
		count := 0
		runUI(func() { count = len(p.testPaths.Objects) })
		return count == 2
	})
	if got := appButtonTooltipVisibleCount(button); got != 1 {
		t.Fatal("旧设置页自检 Reset 不应清除后续窗口 tooltip")
	}
	runUI(func() { button.MouseOut() })
}

func TestTaskResultOldShellShowHideDoesNotClearNewWindowTooltip(t *testing.T) {
	oldApp := test.NewApp()
	defer oldApp.Quit()
	oldShell := newTestShell(t, oldApp, ui.NewFakeBackend(), Options{DisableTray: true, DisableScheduler: true})
	p := oldShell.pages["daily_sync"].(*pageDaily)
	oldShell.Navigate("daily_sync")
	r := p.result

	window, button, _ := newTooltipTestButton(t, nil)
	showAppButtonTooltip(t, window, button)
	r.Apply(taskOutcome{
		Status: "成功", Summary: "同步完成",
		Artifact: taskArtifact{Label: "打开 U 盘目录", Path: "mem://usb"},
	})
	if got := appButtonTooltipVisibleCount(button); got != 1 {
		t.Fatal("旧结果区 Show 不应清除后续窗口 tooltip")
	}
	if r.summary.Text != "同步完成" || !pageCanvasObjectEffectivelyVisible(r.Root, r.artifactBtn) {
		t.Fatal("跳过旧对象 Reset 不得跳过结果 Apply 与产物显示")
	}

	r.Begin("正在执行新一轮")
	if got := appButtonTooltipVisibleCount(button); got != 1 {
		t.Fatal("旧结果区 Hide 不应清除后续窗口 tooltip")
	}
	if r.summary.Text != "正在执行新一轮" || pageCanvasObjectEffectivelyVisible(r.Root, r.artifactBtn) {
		t.Fatal("跳过旧对象 Reset 不得跳过新一轮回填与产物隐藏")
	}
	runUI(func() { button.MouseOut() })
}

func TestTaskResultArtifactSetActionSynchronizesAccessibleName(t *testing.T) {
	r := newTaskResult(nil)
	for _, label := range []string{"打开 U 盘目录", "打开内网表目录"} {
		r.Apply(taskOutcome{
			Status: "成功", Summary: "同步完成",
			Artifact: taskArtifact{Label: label, Path: "mem://artifact"},
		})
		if r.artifactBtn.Text != label {
			t.Fatalf("产物按钮文字=%q，期望 %q", r.artifactBtn.Text, label)
		}
		if got := r.artifactBtn.AccessibilityLabel(); got != label {
			t.Fatalf("产物按钮可访问名称=%q，期望 %q", got, label)
		}
		if r.artifactBtn.Icon == nil || r.artifactBtn.Icon.Name() != "folder.svg" {
			t.Fatal("动态产物入口应保持 folder 图标")
		}
	}
}
