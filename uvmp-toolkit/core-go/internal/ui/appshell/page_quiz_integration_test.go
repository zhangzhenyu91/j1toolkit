package appshell

import (
	"context"
	"testing"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/test"

	"github.com/zhangzhenyu91/j1toolkit/uvmp-toolkit/core-go/internal/ui"
)

type quizBankEnableCall struct {
	bankID  string
	enabled bool
}

type quizIntegrationBackend struct {
	ui.Backend
	setEnabled chan quizBankEnableCall
	bankReads  chan struct{}
}

func (b *quizIntegrationBackend) QuizSetBankEnabled(ctx context.Context, bankID string, enabled bool) error {
	err := b.Backend.QuizSetBankEnabled(ctx, bankID, enabled)
	if err == nil {
		b.setEnabled <- quizBankEnableCall{bankID: bankID, enabled: enabled}
	}
	return err
}

func (b *quizIntegrationBackend) QuizBanks(ctx context.Context) ([]ui.QuizBank, error) {
	banks, err := b.Backend.QuizBanks(ctx)
	b.bankReads <- struct{}{}
	return banks, err
}

func TestQuizBankRowProgrammaticCheckDoesNotSaveOrReload(t *testing.T) {
	fake := ui.NewFakeBackend()
	bank, err := fake.QuizImportBank(context.Background(), "安规.xlsx")
	if err != nil {
		t.Fatal(err)
	}
	backend := &quizIntegrationBackend{
		Backend:    fake,
		setEnabled: make(chan quizBankEnableCall, 8),
		bankReads:  make(chan struct{}, 8),
	}
	app := test.NewApp()
	defer app.Quit()
	s := newTestShell(t, app, backend, Options{DisableTray: true, DisableScheduler: true})
	p := s.pages["quiz_search"].(*pageQuiz)

	var row fyne.CanvasObject
	runUI(func() { row = p.bankRow(bank) })
	select {
	case call := <-backend.setEnabled:
		t.Fatalf("程序化回填不应保存题库启用状态：%+v", call)
	case <-backend.bankReads:
		t.Fatal("程序化回填不应触发题库重新加载")
	case <-time.After(150 * time.Millisecond):
	}

	check := quizBankRowCheck(t, row)
	runUI(func() { check.Tapped(nil) })
	select {
	case call := <-backend.setEnabled:
		if call.bankID != bank.ID || call.enabled {
			t.Fatalf("用户勾选保存参数不符：%+v", call)
		}
	case <-time.After(time.Second):
		t.Fatal("用户真实勾选仍应保存启用状态")
	}
	select {
	case <-backend.bankReads:
	case <-time.After(time.Second):
		t.Fatal("用户真实勾选保存后仍应刷新题库")
	}
}

func TestQuizOldShellReloadDoesNotClearNewWindowTooltip(t *testing.T) {
	oldApp := test.NewApp()
	defer oldApp.Quit()
	oldShell := newTestShell(t, oldApp, ui.NewFakeBackend(), Options{DisableTray: true, DisableScheduler: true})
	p := oldShell.pages["quiz_search"].(*pageQuiz)
	oldShell.Navigate("quiz_search")
	waitShellCond(t, "旧题库页初始加载", func() bool {
		ready := false
		runUI(func() { ready = len(p.bankList.Objects) > 0 })
		return ready
	})

	window, button, _ := newTooltipTestButton(t, nil)
	showAppButtonTooltip(t, window, button)
	runUI(func() {
		p.bankList.Objects = nil
		p.reloadBanks()
	})
	waitShellCond(t, "旧页面异步回调完成列表重建", func() bool {
		rebuilt := false
		runUI(func() { rebuilt = len(p.bankList.Objects) > 0 })
		return rebuilt
	})
	if got := appButtonTooltipVisibleCount(button); got != 1 {
		t.Fatal("旧 Shell 的异步 Reset 不应清除后续窗口 tooltip")
	}
	runUI(func() { button.MouseOut() })
}

func quizBankRowCheck(t *testing.T, row fyne.CanvasObject) *enterpriseCheck {
	t.Helper()
	var check *enterpriseCheck
	walkPageCanvasObjects(row, func(object fyne.CanvasObject) bool {
		if candidate, ok := object.(*enterpriseCheck); ok {
			check = candidate
			return false
		}
		return true
	})
	if check == nil {
		t.Fatal("题库行缺少启用复选框")
	}
	return check
}
