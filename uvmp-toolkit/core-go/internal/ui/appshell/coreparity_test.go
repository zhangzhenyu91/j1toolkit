package appshell

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"fyne.io/fyne/v2/test"

	"github.com/zhangzhenyu91/j1toolkit/uvmp-toolkit/core-go/internal/state"
	"github.com/zhangzhenyu91/j1toolkit/uvmp-toolkit/core-go/internal/ui"
)

// TestSettings_CoreBackendConfigRoundtrip 用生产 CoreBackend（真实 config 文件）走
// 设置页保存链路：SSO/并发/后台运行设置持久化后再读回一致（隔离配置与状态目录）。
func TestSettings_CoreBackendConfigRoundtrip(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("UVMP_TOOLKIT_CONFIG", filepath.Join(dir, "config.json"))
	t.Setenv("UVMP_TOOLKIT_CONFIG_INI", filepath.Join(dir, "none.ini"))
	t.Setenv("APPDATA", dir)
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("HOME", dir)
	state.SetRoot(filepath.Join(dir, "state"))
	t.Cleanup(func() { state.SetRoot("") })

	b := ui.NewCoreBackend()
	t.Cleanup(b.Dispose)
	ctx := context.Background()
	app := test.NewApp()
	defer app.Quit()
	s := newTestShell(t, app, b, Options{DisableTray: true, DisableScheduler: true})
	p := s.pages["settings"].(*pageSettings)
	s.Navigate("settings")
	time.Sleep(150 * time.Millisecond)

	runUI(func() {
		p.ssoUser.SetText("zhang")
		p.ssoPass.SetText("pw123")
		p.saveSSO()
	})
	waitFakeCond(t, "SSO 落盘", func() bool {
		cfg, _ := b.GetConfig(ctx)
		return cfg.SSO.Username == "zhang" && cfg.SSO.HasPassword
	})

	runUI(func() {
		p.wQuery.SetText("6")
		p.wRender.SetText("3")
		p.saveWorkers()
	})
	waitFakeCond(t, "并发落盘", func() bool {
		cfg, _ := b.GetConfig(ctx)
		return cfg.OrderExport.QueryWorkers == 6 && cfg.OrderExport.RenderWorkers == 3
	})

	runUI(func() {
		p.closeTray.SetChecked(false)
		p.driverSel.SetSelected(driverApp)
		p.saveUI()
	})
	waitFakeCond(t, "后台运行落盘", func() bool {
		cfg, _ := b.GetConfig(ctx)
		return !cfg.UI.CloseToTray && cfg.Schedule.Driver == "app"
	})

	// 重新加载页面：控件按持久化值回填
	s.Navigate("home")
	s.Navigate("settings")
	waitShellCond(t, "设置回填", func() bool {
		var user string
		var tray bool
		runUI(func() {
			user = p.ssoUser.Text
			tray = p.closeTray.Checked
		})
		return user == "zhang" && !tray
	})
}
