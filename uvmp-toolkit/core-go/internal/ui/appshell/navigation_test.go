package appshell

import (
	"testing"
	"time"

	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/test"
	"fyne.io/fyne/v2/widget"

	"github.com/zhangzhenyu91/j1toolkit/uvmp-toolkit/core-go/internal/ui"
)

func TestShellNavigationSearchFavoritesAndRecent(t *testing.T) {
	f := ui.NewFakeBackend()
	app := test.NewApp()
	defer app.Quit()
	s := newTestShell(t, app, f, Options{DisableTray: true, DisableScheduler: true})

	nav := container.NewVBox()
	s.rebuildNavEntries(nav, "轨迹")
	if _, ok := s.navEntries["settings"]; !ok {
		t.Fatal("设置入口不应被搜索重建移除")
	}
	if _, ok := s.navEntries["pcd_export"]; !ok {
		t.Fatal("搜索轨迹应命中轨迹导出")
	}
	if _, ok := s.navEntries["quiz_search"]; ok {
		t.Fatal("搜索轨迹不应保留题库搜题")
	}
	s.rebuildNavEntries(nav, "")
	if len(s.navEntries) != 6 {
		t.Fatalf("清空搜索默认只保留 6 个主入口，实际 %d", len(s.navEntries))
	}
	s.advancedNavExpanded = true
	s.rebuildNavEntries(nav, "")
	if len(s.navEntries) != 9 {
		t.Fatalf("展开开发者工具应恢复 9 个入口，实际 %d", len(s.navEntries))
	}

	if !s.toggleFavorite("pcd_export") {
		t.Fatal("首次收藏轨迹导出应返回 true")
	}
	ids := s.quickToolIDs()
	if len(ids) != 1 || ids[0] != "pcd_export" {
		t.Fatalf("快捷入口应包含收藏工具: %v", ids)
	}
	s.Navigate("daily_sync")
	ids = s.quickToolIDs()
	if len(ids) != 2 || ids[1] != "daily_sync" {
		t.Fatalf("最近打开应补在收藏之后: %v", ids)
	}
	if s.header.favorite.Hidden {
		t.Fatal("业务工具页应显示收藏入口")
	}
	s.Navigate("settings")
	if s.header.favorite.Visible() {
		t.Fatal("设置页不应显示工具收藏入口")
	}
}

func TestShellStartJobSavesParamsBeforeDone(t *testing.T) {
	f := ui.NewFakeBackend()
	f.StepDelay = time.Millisecond
	app := test.NewApp()
	defer app.Quit()
	s := newTestShell(t, app, f, Options{DisableTray: true, DisableScheduler: true})

	done := make(chan ui.Job, 1)
	s.StartJob("daily_sync", map[string]any{"date": "2026-10-29"}, ui.JobHandler{
		OnDone: func(job ui.Job) {
			params, ok := s.jobParams(job.ID)
			if !ok || params["date"] != "2026-10-29" {
				t.Errorf("完成回调前未保存参数快照: %v %v", params, ok)
			}
			done <- job
		},
	})
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("等待任务完成超时")
	}
}

func TestPagePcdRestoreParams(t *testing.T) {
	f := ui.NewFakeBackend()
	app := test.NewApp()
	defer app.Quit()
	s := newTestShell(t, app, f, Options{DisableTray: true, DisableScheduler: true})
	p := s.pages["pcd_export"].(*pagePcd)
	p.RestoreParams(map[string]any{
		"codes":          []any{map[string]any{"seq": float64(1), "code": "A001"}},
		"outdir":         "D:/导出",
		"batch_name":     "测试批次",
		"batch_subdir":   false,
		"export_pcd":     true,
		"export_track":   false,
		"calibrate_noon": true,
	})
	if len(p.codes) != 1 || p.codes[0]["code"] != "A001" || p.outdir.Text != "D:/导出" {
		t.Fatalf("单号或输出目录未恢复: codes=%v outdir=%q", p.codes, p.outdir.Text)
	}
	if p.optBatch.Checked || p.optTrack.Checked || !p.optPcd.Checked || !p.optCalib.Checked {
		t.Fatal("导出选项未按快照恢复")
	}
	if p.startBtn.Importance != widget.HighImportance {
		t.Fatal("回填后仍应保留唯一主执行按钮")
	}
}
