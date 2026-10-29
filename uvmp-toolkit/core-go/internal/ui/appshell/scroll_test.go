package appshell

import (
	"testing"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/test"

	"github.com/zhangzhenyu91/j1toolkit/uvmp-toolkit/core-go/internal/ui"
)

// 两页共用整页滚动条：必须能实际到达底部并回到顶部，不能只验证有条滚动轨道。
func TestBusinessPages_ScrollRange(t *testing.T) {
	f := ui.NewFakeBackend()
	f.StepDelay = time.Millisecond
	a := test.NewApp()
	defer a.Quit()
	s := newTestShell(t, a, f, Options{DisableTray: true, DisableScheduler: true})

	for _, pageID := range []string{"pcd_export", "course_autoplay"} {
		s.Navigate(pageID)
		time.Sleep(50 * time.Millisecond)
		sc, ok := s.pages[pageID].Object().(*container.Scroll)
		if !ok {
			t.Fatalf("%s 根对象不是整页滚动区：%T", pageID, s.pages[pageID].Object())
		}
		runUI(func() {
			sc.Resize(fyne.NewSize(900, 600))
			sc.ScrollToBottom()
		})
		if sc.Offset.Y <= 0 || sc.Content.Position().Y >= 0 {
			t.Fatalf("%s 无法滚动到底部：offset=%v content=%v", pageID, sc.Offset, sc.Content.Position())
		}
		runUI(func() { sc.ScrollToTop() })
		if sc.Offset.Y != 0 || sc.Content.Position().Y != 0 {
			t.Fatalf("%s 无法回到顶部：offset=%v content=%v", pageID, sc.Offset, sc.Content.Position())
		}
	}
}
