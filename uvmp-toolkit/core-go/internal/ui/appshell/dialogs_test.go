package appshell

import (
	"testing"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/driver/desktop"
	"fyne.io/fyne/v2/test"
	"fyne.io/fyne/v2/widget"

	"github.com/zhangzhenyu91/j1toolkit/uvmp-toolkit/core-go/internal/ui"
)

func collectDialogObjects(obj fyne.CanvasObject, objects *[]fyne.CanvasObject) {
	if obj == nil {
		return
	}
	switch o := obj.(type) {
	case *appButton:
		*objects = append(*objects, o)
		return
	case *widget.Button:
		*objects = append(*objects, o)
		return
	case *widget.PopUp:
		collectDialogObjects(o.Content, objects)
		return
	case *fyne.Container:
		for _, child := range o.Objects {
			collectDialogObjects(child, objects)
		}
	default:
		if w, ok := obj.(fyne.Widget); ok {
			for _, child := range w.CreateRenderer().Objects() {
				collectDialogObjects(child, objects)
			}
		}
	}
}

func visibleDialogButtons(t *testing.T, window fyne.Window) []fyne.CanvasObject {
	t.Helper()
	var buttons []fyne.CanvasObject
	runUI(func() {
		overlay := window.Canvas().Overlays().Top()
		if overlay == nil {
			t.Fatal("弹层未显示")
		}
		collectDialogObjects(overlay, &buttons)
	})
	return buttons
}

func requireGraphicDialogButton(t *testing.T, object fyne.CanvasObject) *appButton {
	t.Helper()
	button, ok := object.(*appButton)
	if !ok {
		t.Fatalf("自有弹层按钮应使用 appButton：%T", object)
	}
	if button.Icon == nil {
		t.Fatal("自有弹层按钮不得使用 nil 图标")
	}
	if got := button.Cursor(); got != desktop.PointerCursor {
		t.Fatalf("自有弹层按钮应使用手型指针：%v", got)
	}
	return button
}

func TestInfoAndWarnUseGraphicDismissButton(t *testing.T) {
	for _, tt := range []struct {
		name string
		show func(fyne.Window)
	}{
		{name: "Info", show: func(window fyne.Window) { Info(window, "提示", "操作已完成") }},
		{name: "Warn", show: func(window fyne.Window) { Warn(window, "警示", "请检查配置") }},
	} {
		t.Run(tt.name, func(t *testing.T) {
			a := test.NewApp()
			defer a.Quit()
			window := test.NewWindow(container.NewVBox())
			defer window.Close()
			runUI(func() { tt.show(window) })
			buttons := visibleDialogButtons(t, window)
			if len(buttons) != 1 {
				t.Fatalf("信息弹层应只有一枚关闭按钮：%d", len(buttons))
			}
			button := requireGraphicDialogButton(t, buttons[0])
			if button.AccessibilityLabel() != "知道了" {
				t.Fatalf("关闭按钮中文名称不符：%q", button.AccessibilityLabel())
			}
			runUI(func() { button.Tapped(nil) })
			if top := window.Canvas().Overlays().Top(); top != nil {
				t.Fatal("点击关闭按钮应关闭弹层")
			}
		})
	}
}

func TestConfirmVariantsUseGraphicButtonsAndSingleDangerAction(t *testing.T) {
	for _, tt := range []struct {
		name           string
		show           func(fyne.Window, func())
		wantImportance widget.Importance
	}{
		{name: "Confirm", wantImportance: widget.HighImportance, show: func(window fyne.Window, ok func()) {
			Confirm(window, "确认", "继续执行？", "继续", ok)
		}},
		{name: "ConfirmDanger", wantImportance: widget.DangerImportance, show: func(window fyne.Window, ok func()) {
			ConfirmDanger(window, "危险操作", "确认删除？", "删除", ok)
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			a := test.NewApp()
			defer a.Quit()
			window := test.NewWindow(container.NewVBox())
			defer window.Close()
			confirmed := 0
			runUI(func() { tt.show(window, func() { confirmed++ }) })
			buttons := visibleDialogButtons(t, window)
			if len(buttons) != 2 {
				t.Fatalf("确认弹层应有取消和最终确认两枚按钮：%d", len(buttons))
			}
			cancel := requireGraphicDialogButton(t, buttons[0])
			confirm := requireGraphicDialogButton(t, buttons[1])
			if cancel.Text != "取消" || confirm.Text == "" {
				t.Fatalf("确认弹层按钮文案丢失：%q / %q", cancel.Text, confirm.Text)
			}
			if confirm.Importance != tt.wantImportance {
				t.Fatalf("最终确认重要性不符：got=%v want=%v", confirm.Importance, tt.wantImportance)
			}
			if cancel.Importance == widget.DangerImportance {
				t.Fatal("取消按钮不得使用危险红实心")
			}
			runUI(func() { confirm.Tapped(nil) })
			if confirmed != 1 {
				t.Fatalf("最终确认应执行一次原回调：%d", confirmed)
			}
			if top := window.Canvas().Overlays().Top(); top != nil {
				t.Fatal("最终确认后应关闭弹层")
			}
		})
	}
}

func TestPromptTextUsesGraphicButtonsAndPreservesSubmittedText(t *testing.T) {
	a := test.NewApp()
	defer a.Quit()
	window := test.NewWindow(container.NewVBox())
	defer window.Close()
	submitted := ""
	runUI(func() { PromptText(window, "重命名", "原名称", func(text string) { submitted = text }) })
	var entries []*widget.Entry
	runUI(func() {
		var walk func(fyne.CanvasObject)
		walk = func(obj fyne.CanvasObject) {
			switch o := obj.(type) {
			case *widget.Entry:
				entries = append(entries, o)
			case *widget.PopUp:
				walk(o.Content)
			case *fyne.Container:
				for _, child := range o.Objects {
					walk(child)
				}
			default:
				if w, ok := obj.(fyne.Widget); ok {
					for _, child := range w.CreateRenderer().Objects() {
						walk(child)
					}
				}
			}
		}
		walk(window.Canvas().Overlays().Top())
	})
	if len(entries) != 1 {
		t.Fatalf("文本弹层应保留一个 Entry：%d", len(entries))
	}
	if got := entries[0].Cursor(); got != desktop.TextCursor {
		t.Fatalf("文本 Entry 应保持 IBeam：%v", got)
	}
	entries[0].SetText("新名称")
	buttons := visibleDialogButtons(t, window)
	if len(buttons) != 2 {
		t.Fatalf("文本弹层应有两枚动作按钮：%d", len(buttons))
	}
	requireGraphicDialogButton(t, buttons[0])
	confirm := requireGraphicDialogButton(t, buttons[1])
	runUI(func() { confirm.Tapped(nil) })
	if submitted != "新名称" {
		t.Fatalf("确认回调文本不符：%q", submitted)
	}
}

func TestJobLogUsesGraphicCloseButton(t *testing.T) {
	f := ui.NewFakeBackend()
	f.StepDelay = time.Millisecond
	a := test.NewApp()
	defer a.Quit()
	s := newTestShell(t, a, f, Options{DisableTray: true, DisableScheduler: true})
	done := make(chan struct{})
	handle := s.StartJob("daily_sync", map[string]any{}, ui.JobHandler{
		OnDone: func(ui.Job) { close(done) },
	})
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("等待 FakeBackend 任务终态超时")
	}
	runUI(func() { showJobLog(s, handle.ID()) })
	waitShellCond(t, "日志弹层显示", func() bool { return s.Win.Canvas().Overlays().Top() != nil })
	buttons := visibleDialogButtons(t, s.Win)
	if len(buttons) == 0 {
		t.Fatal("日志弹层缺少关闭按钮")
	}
	closeButton := requireGraphicDialogButton(t, buttons[len(buttons)-1])
	if closeButton.AccessibilityLabel() != "关闭" {
		t.Fatalf("日志关闭按钮中文名称不符：%q", closeButton.AccessibilityLabel())
	}
	runUI(func() { closeButton.Tapped(nil) })
	if top := s.Win.Canvas().Overlays().Top(); top != nil {
		t.Fatal("日志关闭按钮应关闭弹层")
	}
}
