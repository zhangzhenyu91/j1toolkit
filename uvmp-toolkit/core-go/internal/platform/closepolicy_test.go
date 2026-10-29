package platform

import "testing"

func TestClosePolicy_OnWindowClose(t *testing.T) {
	cases := []struct {
		name string
		p    ClosePolicy
		want CloseAction
	}{
		{"关窗驻留开启且托盘可用", ClosePolicy{CloseToTray: true, TrayAvailable: true}, ActionHide},
		{"关窗驻留关闭", ClosePolicy{CloseToTray: false, TrayAvailable: true}, ActionExit},
		{"托盘不可用", ClosePolicy{CloseToTray: true, TrayAvailable: false}, ActionExit},
	}
	for _, c := range cases {
		if got := c.p.OnWindowClose(); got != c.want {
			t.Fatalf("%s: OnWindowClose=%v 期望 %v", c.name, got, c.want)
		}
	}
}

func TestClosePolicy_OnTrayExit(t *testing.T) {
	p := ClosePolicy{JobRunning: false}
	if got := p.OnTrayExit(); got != ActionExit {
		t.Fatalf("无任务时托盘退出应直接退出: %v", got)
	}
	p.JobRunning = true
	if got := p.OnTrayExit(); got != ActionConfirmExit {
		t.Fatalf("有任务时托盘退出应先确认: %v", got)
	}
	if msg := p.ExitConfirmMessage(); msg == "" {
		t.Fatal("退出确认文案不应为空")
	}
}

func TestCloseAction_String(t *testing.T) {
	if ActionHide.String() != "hide" || ActionExit.String() != "exit" || ActionConfirmExit.String() != "confirm-exit" {
		t.Fatal("CloseAction 文案不符")
	}
}
