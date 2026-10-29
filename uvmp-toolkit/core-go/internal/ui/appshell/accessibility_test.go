package appshell

import (
	"testing"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/test"

	"github.com/zhangzhenyu91/j1toolkit/uvmp-toolkit/core-go/internal/ui"
)

func TestNavEntryKeyboardActivationAndFocus(t *testing.T) {
	app := test.NewApp()
	defer app.Quit()
	s := newTestShell(t, app, ui.NewFakeBackend(), Options{DisableTray: true, DisableScheduler: true})
	entry := s.navEntries["pcd_export"]
	entry.FocusGained()
	if !entry.focused || entry.bg.StrokeWidth == 0 {
		t.Fatal("导航获得键盘焦点后应显示焦点描边")
	}
	entry.TypedKey(&fyne.KeyEvent{Name: fyne.KeyReturn})
	if s.current != "pcd_export" {
		t.Fatalf("回车应激活导航，当前页=%s", s.current)
	}
	entry.FocusLost()
	if entry.focused || entry.bg.StrokeWidth != 0 {
		t.Fatal("焦点离开后应移除焦点描边")
	}
}
