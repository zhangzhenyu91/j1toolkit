package appshell

import (
	"context"
	"testing"

	"fyne.io/fyne/v2/test"

	"github.com/zhangzhenyu91/j1toolkit/uvmp-toolkit/core-go/internal/ui"
)

func TestPageSyncReloadStatusPreservesUnsavedInputs(t *testing.T) {
	f := ui.NewFakeBackend()
	ctx := context.Background()
	if err := f.SaveConfig(ctx, ui.ConfigPatch{
		TreeSync: &ui.SyncAppConfig{LocalPath: "D:/初始/树木.xlsx", FileName: "初始.xlsx", NightlyTime: "23:55"},
	}); err != nil {
		t.Fatal(err)
	}
	app := test.NewApp()
	defer app.Quit()
	s := newTestShell(t, app, f, Options{DisableTray: true, DisableScheduler: true})
	p := s.pages["tree_sync"].(*pageSync)

	s.Navigate("tree_sync")
	waitShellCond(t, "树障同步首次配置回填", func() bool {
		var loaded bool
		runUI(func() { loaded = p.formLoaded })
		return loaded
	})
	assertSyncForm(t, p, "D:/初始/树木.xlsx", "初始.xlsx", "23:55")

	if err := f.SaveConfig(ctx, ui.ConfigPatch{
		TreeSync: &ui.SyncAppConfig{LocalPath: "D:/外部/树木.xlsx", FileName: "外部.xlsx", NightlyTime: "23:30"},
	}); err != nil {
		t.Fatal(err)
	}
	runUI(func() {
		p.localPath.SetText("D:/未保存/树木.xlsx")
		p.fileName.SetText("未保存.xlsx")
		p.nightly.SetText("22:45")
	})

	s.Navigate("home")
	s.Navigate("tree_sync")
	waitShellCond(t, "树障同步后续状态刷新", func() bool {
		var path string
		runUI(func() { path = p.localPathTxt })
		return path == "D:/外部/树木.xlsx"
	})
	assertSyncForm(t, p, "D:/未保存/树木.xlsx", "未保存.xlsx", "22:45")

	runUI(func() { p.save() })
	waitShellCond(t, "保存同步设置后刷新状态", func() bool {
		var path string
		runUI(func() { path = p.localPathTxt })
		return path == "D:/未保存/树木.xlsx"
	})
	assertSyncForm(t, p, "D:/未保存/树木.xlsx", "未保存.xlsx", "22:45")
	cfg, err := f.GetConfig(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.TreeSync.LocalPath != "D:/未保存/树木.xlsx" || cfg.TreeSync.FileName != "未保存.xlsx" || cfg.TreeSync.NightlyTime != "22:45" {
		t.Fatalf("保存后的同步配置不符: %+v", cfg.TreeSync)
	}
}

func assertSyncForm(t *testing.T, p *pageSync, localPath, fileName, nightly string) {
	t.Helper()
	var gotPath, gotName, gotNightly string
	runUI(func() {
		gotPath = p.localPath.Text
		gotName = p.fileName.Text
		gotNightly = p.nightly.Text
	})
	if gotPath != localPath || gotName != fileName || gotNightly != nightly {
		t.Fatalf("状态刷新覆盖了表单输入: path=%q name=%q nightly=%q", gotPath, gotName, gotNightly)
	}
}
