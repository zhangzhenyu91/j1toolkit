package appshell

import (
	"math"
	"testing"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/driver/desktop"
	"fyne.io/fyne/v2/test"
	"fyne.io/fyne/v2/widget"
)

func TestEnterpriseCheck_AlignmentAndCallbacks(t *testing.T) {
	a := test.NewApp()
	defer a.Quit()
	a.Settings().SetTheme(NewTheme())
	changed := 0
	check := newEnterpriseCheck("启用定时", func(bool) { changed++ })
	runUI(func() {
		check.Resize(fyne.NewSize(240, 32))
		check.SetChecked(true)
	})
	if changed != 1 || !check.Checked || !check.tick.Visible() {
		t.Fatalf("选中状态或回调不符：changed=%d checked=%v", changed, check.Checked)
	}
	boxCenter := check.box.Position().Y + check.box.Size().Height/2
	labelCenter := check.label.Position().Y + check.label.Size().Height/2
	if math.Abs(float64(boxCenter-labelCenter)) > .01 {
		t.Fatalf("勾选框与文字未按同一中心线对齐：box=%.2f label=%.2f", boxCenter, labelCenter)
	}
	runUI(func() { check.Tapped(nil) })
	if changed != 2 || check.Checked || check.tick.Visible() {
		t.Fatalf("点击未正确取消选中：changed=%d checked=%v", changed, check.Checked)
	}
	runUI(func() {
		check.Disable()
		check.Tapped(nil)
	})
	if changed != 2 || check.Checked {
		t.Fatal("禁用复选框不应响应点击")
	}
}

func TestEnterpriseCheckCursorFollowsDisabledState(t *testing.T) {
	a := test.NewApp()
	defer a.Quit()
	check := newEnterpriseCheck("启用定时", nil)
	if got := check.Cursor(); got != desktop.PointerCursor {
		t.Fatalf("启用复选框应使用手型指针：%v", got)
	}
	runUI(func() { check.Disable() })
	if got := check.Cursor(); got != desktop.DefaultCursor {
		t.Fatalf("禁用复选框应使用普通指针：%v", got)
	}
	runUI(func() { check.Enable() })
	if got := check.Cursor(); got != desktop.PointerCursor {
		t.Fatalf("重新启用复选框应恢复手型指针：%v", got)
	}
}

func TestAppButton_CallbackImmediate(t *testing.T) {
	a := test.NewApp()
	defer a.Quit()
	called := 0
	button := newAppButton("保存", "save", widget.LowImportance, func() { called++ })
	runUI(func() { button.Tapped(nil) })
	if called != 1 {
		t.Fatalf("按钮回调被动画延迟或丢失：%d", called)
	}
	time.Sleep(controlMotionDuration + 80*time.Millisecond)
	if button.Position().Y != 0 {
		t.Fatalf("按钮动画结束未回到原位：%v", button.Position())
	}
}

func TestPageStage_RapidSwitchSettlesLastAnimation(t *testing.T) {
	a := test.NewApp()
	defer a.Quit()
	content := container.NewStack()
	stage := newPageStage(content)
	runUI(func() {
		stage.Resize(fyne.NewSize(800, 600))
		stage.Play()
		stage.Play() // 前一次计时器不得提前收尾后一次动画
	})
	time.Sleep(pageMotionDuration + 90*time.Millisecond)
	if stage.offset != 0 || stage.veil.Visible() || stage.animation != nil {
		t.Fatalf("连续切换后未落位：offset=%.2f veil=%v animation=%v", stage.offset, stage.veil.Visible(), stage.animation != nil)
	}
	if content.Position().Y != 0 {
		t.Fatalf("页面内容未回到原位：%v", content.Position())
	}
}

func TestEnterpriseMotionCurve_BoundedAndFastStart(t *testing.T) {
	previous := float32(-1)
	for _, value := range []float32{0, .1, .25, .5, .75, .9, 1} {
		got := enterpriseMotionCurve(value)
		if got < 0 || got > 1 || got < previous {
			t.Fatalf("缓动曲线越界或倒退：t=%.2f value=%.4f", value, got)
		}
		previous = got
	}
	if enterpriseMotionCurve(.25) <= .25 {
		t.Fatal("曲线应快速起步而非僵硬线性过渡")
	}
}
