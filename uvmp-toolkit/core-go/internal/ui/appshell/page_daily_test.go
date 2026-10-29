package appshell

import (
	"context"
	"strings"
	"testing"

	"fyne.io/fyne/v2/test"

	"github.com/zhangzhenyu91/j1toolkit/uvmp-toolkit/core-go/internal/ui"
)

func TestPageDailyReloadStatusPreservesUnsavedInputs(t *testing.T) {
	f := ui.NewFakeBackend()
	ctx := context.Background()
	app := test.NewApp()
	defer app.Quit()
	s := newTestShell(t, app, f, Options{DisableTray: true, DisableScheduler: true})
	p := s.pages["daily_sync"].(*pageDaily)

	s.Navigate("daily_sync")
	waitShellCond(t, "派车同步首次配置回填", func() bool {
		var loaded bool
		runUI(func() { loaded = p.formLoaded })
		return loaded
	})

	if err := f.SaveConfig(ctx, ui.ConfigPatch{
		Schedule: &ui.ScheduleConfig{Enabled: true, Time: "06:30", Driver: "app"},
		USB:      &ui.USBConfig{Label: "外部U盘", FallbackDir: "D:/外部"},
	}); err != nil {
		t.Fatal(err)
	}
	runUI(func() {
		p.schEnable.SetChecked(false)
		p.schTime.SetText("10:41")
		p.usbLabel.SetText("未保存U盘")
		p.usbFallback.SetText("D:/未保存")
	})

	s.Navigate("home")
	s.Navigate("daily_sync")
	waitShellCond(t, "派车同步后续状态刷新", func() bool {
		var due string
		runUI(func() { due = p.dueLabel.Text })
		return strings.Contains(due, "06:30")
	})
	assertDailyForm(t, p, false, "10:41", "未保存U盘", "D:/未保存")

	runUI(func() { p.saveSchedule() })
	waitShellCond(t, "保存定时后刷新状态", func() bool {
		var due string
		runUI(func() { due = p.dueLabel.Text })
		return strings.Contains(due, "10:41")
	})
	assertDailyForm(t, p, false, "10:41", "未保存U盘", "D:/未保存")
	cfg, err := f.GetConfig(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Schedule.Enabled || cfg.Schedule.Time != "10:41" || cfg.Schedule.Driver != "app" {
		t.Fatalf("保存后的定时配置不符: %+v", cfg.Schedule)
	}
}

func assertDailyForm(t *testing.T, p *pageDaily, enabled bool, timeStr, label, fallback string) {
	t.Helper()
	var gotEnabled bool
	var gotTime, gotLabel, gotFallback string
	runUI(func() {
		gotEnabled = p.schEnable.Checked
		gotTime = p.schTime.Text
		gotLabel = p.usbLabel.Text
		gotFallback = p.usbFallback.Text
	})
	if gotEnabled != enabled || gotTime != timeStr || gotLabel != label || gotFallback != fallback {
		t.Fatalf("状态刷新覆盖了表单输入: enabled=%v time=%q label=%q fallback=%q", gotEnabled, gotTime, gotLabel, gotFallback)
	}
}
