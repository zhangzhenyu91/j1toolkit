package appshell

import (
	"context"
	"fmt"
	"image/color"
	"testing"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/test"
	"fyne.io/fyne/v2/theme"

	"github.com/zhangzhenyu91/j1toolkit/uvmp-toolkit/core-go/internal/ui"
)

func TestEnterpriseThemeScrollBarTokens(t *testing.T) {
	th := NewTheme()
	for _, tt := range []struct {
		name string
		key  fyne.ThemeSizeName
		want float32
	}{
		{name: "细滚动条", key: theme.SizeNameScrollBarSmall, want: 2},
		{name: "展开滚动条", key: theme.SizeNameScrollBar, want: 8},
		{name: "滚动条圆角", key: theme.SizeNameScrollBarRadius, want: 4},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := th.Size(tt.key); got != tt.want {
				t.Errorf("尺寸 = %v，期望 %v", got, tt.want)
			}
		})
	}

	gotBar := color.NRGBAModel.Convert(th.Color(theme.ColorNameScrollBar, theme.VariantLight)).(color.NRGBA)
	wantBar := color.NRGBA{R: 0x86, G: 0x90, B: 0x9C, A: 0x80}
	if gotBar != wantBar {
		t.Errorf("滑块颜色 = %#v，期望 TextTertiary 半透明色 %#v", gotBar, wantBar)
	}
	gotBackground := color.NRGBAModel.Convert(th.Color(theme.ColorNameScrollBarBackground, theme.VariantLight)).(color.NRGBA)
	if gotBackground != (color.NRGBA{}) {
		t.Errorf("滚动轨道应保持透明，实际为 %#v", gotBackground)
	}
}

// 整页滚动条：常规/最大化、长数据都必须能实际到达底部并回到顶部。
func TestBusinessPages_ScrollRange(t *testing.T) {
	f := ui.NewFakeBackend()
	f.StepDelay = time.Millisecond
	for i := 0; i < 8; i++ {
		if _, err := f.QuizImportBank(context.Background(), fmt.Sprintf("滚动题库-%02d.xlsx", i)); err != nil {
			t.Fatal(err)
		}
	}
	a := test.NewApp()
	defer a.Quit()
	s := newTestShell(t, a, f, Options{DisableTray: true, DisableScheduler: true})

	for _, pageID := range []string{"pcd_export", "quiz_search", "course_autoplay"} {
		s.Navigate(pageID)
		time.Sleep(60 * time.Millisecond)
		sc, ok := s.pages[pageID].Object().(*container.Scroll)
		if !ok {
			t.Fatalf("%s 根对象不是整页滚动区：%T", pageID, s.pages[pageID].Object())
		}
		assertScrollRange(t, pageID+" 常规", sc)
		runUI(func() { s.setWindowMaximized(true) })
		assertScrollRange(t, pageID+" 最大化", sc)
		runUI(func() { s.setWindowMaximized(false) })
	}
}

func assertScrollRange(t *testing.T, step string, sc *container.Scroll) {
	t.Helper()
	runUI(func() {
		sc.Resize(fyne.NewSize(900, 600))
		sc.Refresh()
		sc.ScrollToBottom()
	})
	if sc.Content.MinSize().Height <= sc.Size().Height {
		// 内容未超出视口时无需滚动，但必须仍停在顶部。
		if sc.Offset.Y != 0 || sc.Content.Position().Y != 0 {
			t.Fatalf("%s：无滚动需求时偏移不为 0：offset=%v content=%v", step, sc.Offset, sc.Content.Position())
		}
		return
	}
	if sc.Offset.Y <= 0 || sc.Content.Position().Y >= 0 {
		t.Fatalf("%s 无法滚动到底部：offset=%v content=%v", step, sc.Offset, sc.Content.Position())
	}
	runUI(func() { sc.ScrollToTop() })
	if sc.Offset.Y != 0 || sc.Content.Position().Y != 0 {
		t.Fatalf("%s 无法回到顶部：offset=%v content=%v", step, sc.Offset, sc.Content.Position())
	}
}
