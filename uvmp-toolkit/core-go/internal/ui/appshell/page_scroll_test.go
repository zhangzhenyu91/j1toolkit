package appshell

import (
	"testing"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/driver/desktop"
	"fyne.io/fyne/v2/test"

	"github.com/zhangzhenyu91/j1toolkit/uvmp-toolkit/core-go/internal/ui"
)

func TestPageScrollGapsOnlyBetweenVisibleCards(t *testing.T) {
	app := test.NewApp()
	defer app.Quit()

	first := canvas.NewRectangle(nil)
	first.SetMinSize(fyne.NewSize(100, 40))
	hidden := canvas.NewRectangle(nil)
	hidden.SetMinSize(fyne.NewSize(100, 50))
	hidden.Hide()
	third := canvas.NewRectangle(nil)
	third.SetMinSize(fyne.NewSize(100, 60))
	scroll := pageScroll(first, hidden, third)
	content := scroll.Content.(*fyne.Container)

	if got, want := content.MinSize().Height, float32(112); got != want {
		t.Fatalf("隐藏卡片两侧不得累计间距：got=%.1f want=%.1f", got, want)
	}
	content.Resize(fyne.NewSize(200, content.MinSize().Height))
	if got, want := third.Position().Y, float32(52); got != want {
		t.Fatalf("第三个卡片应紧跟 40px 首卡与 12px 间距：got=%.1f want=%.1f", got, want)
	}

	hidden.Show()
	if got, want := content.MinSize().Height, float32(174); got != want {
		t.Fatalf("卡片重新显示后应只在三个相邻可见卡片间产生两个间距：got=%.1f want=%.1f", got, want)
	}
}

func TestPageSettingsSearchGeometryWithHiddenCards(t *testing.T) {
	app := test.NewApp()
	defer app.Quit()
	s := newTestShell(t, app, ui.NewFakeBackend(), Options{DisableTray: true, DisableScheduler: true})
	p := s.pages["settings"].(*pageSettings)
	content := p.root.(*container.Scroll).Content.(*fyne.Container)
	searchCard := content.Objects[0]
	initialCards := []fyne.CanvasObject{searchCard}
	for _, scope := range p.sectionScopes {
		initialCards = append(initialCards, scope)
	}

	if p.searchEmpty.Visible() {
		t.Fatal("初始无搜索时 searchEmpty 应隐藏")
	}
	assertVisiblePageCardsGeometry(t, "初始隐藏空态", content, initialCards)

	runUI(func() { p.search.SetText("完全不存在的设置") })
	if !p.searchEmpty.Visible() {
		t.Fatal("无匹配搜索应显示 searchEmpty")
	}
	for i, scope := range p.sectionScopes {
		if scope.Visible() {
			t.Fatalf("无匹配搜索不应保留第 %d 个设置组", i)
		}
	}
	assertVisiblePageCardsGeometry(t, "搜索过滤隐藏全部设置组", content, []fyne.CanvasObject{searchCard, p.searchEmpty})

	runUI(func() { p.search.SetText("") })
	if p.searchEmpty.Visible() {
		t.Fatal("清空搜索后 searchEmpty 应重新隐藏")
	}
	assertVisiblePageCardsGeometry(t, "清空搜索恢复", content, initialCards)
}

func assertVisiblePageCardsGeometry(t *testing.T, step string, content *fyne.Container, cards []fyne.CanvasObject) {
	t.Helper()
	wantHeight := float32(0)
	for i, card := range cards {
		if i > 0 {
			wantHeight += 12
		}
		wantHeight += card.MinSize().Height
	}
	if got := content.MinSize().Height; got != wantHeight {
		t.Fatalf("%s：可见卡片 MinSize 不应包含隐藏卡片间距：got=%.1f want=%.1f", step, got, wantHeight)
	}
	content.Resize(fyne.NewSize(800, wantHeight))
	wantY := float32(0)
	for i, card := range cards {
		if card.Position().Y != wantY {
			t.Fatalf("%s：第 %d 个可见卡片 Y=%.1f，期望 %.1f", step, i, card.Position().Y, wantY)
		}
		wantY += card.Size().Height
		if i < len(cards)-1 {
			wantY += 12
		}
	}
}

func TestNavEntryUsesPointerCursor(t *testing.T) {
	app := test.NewApp()
	defer app.Quit()
	entry := newNavEntry("首页", "home", nil)
	if got := entry.Cursor(); got != desktop.PointerCursor {
		t.Fatalf("可点击导航应使用手型指针：got=%v want=%v", got, desktop.PointerCursor)
	}
}
