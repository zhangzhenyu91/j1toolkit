package appshell

import (
	"testing"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/test"
	"fyne.io/fyne/v2/widget"

	"github.com/zhangzhenyu91/j1toolkit/uvmp-toolkit/core-go/internal/ui"
)

func TestNavEntryGeometry(t *testing.T) {
	app := test.NewApp()
	defer app.Quit()

	entry := newNavEntry("轨迹派车单导出", "route", nil)
	width := float32(176)
	entry.Resize(fyne.NewSize(width, 44))

	if got := entry.MinSize().Height; got != 44 {
		t.Fatalf("导航命中区高度应为 44，实际 %.1f", got)
	}
	if entry.label.TextSize != 14 {
		t.Fatalf("导航标题字号应为 14，实际 %.1f", entry.label.TextSize)
	}
	if entry.icon.Size().Width != 12 || entry.icon.Size().Height != 12 {
		t.Fatalf("导航图标应为 12×12，实际 %v", entry.icon.Size())
	}
	if entry.icon.Size().Width >= entry.label.TextSize {
		t.Fatalf("导航图标必须小于文字：icon=%.1f text=%.1f", entry.icon.Size().Width, entry.label.TextSize)
	}
	if entry.bg.CornerRadius != 8 {
		t.Fatalf("同级导航圆角应为 8，实际 %.1f", entry.bg.CornerRadius)
	}
	if entry.icon.Position().X != 12 {
		t.Fatalf("导航内侧左留白应为 12，实际 %.1f", entry.icon.Position().X)
	}
	wantLabelX := float32(32)
	if entry.label.Position().X != wantLabelX {
		t.Fatalf("图文间距应固定为 8，标题 X 应为 %.1f，实际 %.1f", wantLabelX, entry.label.Position().X)
	}
	if right := entry.label.Position().X + entry.label.Size().Width; right != width-12 {
		t.Fatalf("导航左右内留白应相等，标题右缘应为 %.1f，实际 %.1f", width-12, right)
	}
	if entry.label.Size().Width < entry.label.MinSize().Width {
		t.Fatalf("最长中文标题不得截断：width=%.1f min=%.1f", entry.label.Size().Width, entry.label.MinSize().Width)
	}
	iconCenter := entry.icon.Position().Y + entry.icon.Size().Height/2
	labelCenter := entry.label.Position().Y + entry.label.Size().Height/2
	if iconCenter != labelCenter {
		t.Fatalf("图标与标题应同中心线：%.1f / %.1f", iconCenter, labelCenter)
	}
}

func TestSidebarSurfaceAndOuterPadding(t *testing.T) {
	app := test.NewApp()
	defer app.Quit()
	s := newTestShell(t, app, ui.NewFakeBackend(), Options{DisableTray: true, DisableScheduler: true})
	side := s.buildSideBar()

	t.Run("侧栏圆角", func(t *testing.T) {
		root, ok := side.(*fyne.Container)
		if !ok || len(root.Objects) < 1 {
			t.Fatalf("侧栏缺少面板表面：%T", side)
		}
		bg, ok := root.Objects[0].(*canvas.Rectangle)
		if !ok || bg.StrokeWidth != 1 || bg.StrokeColor != Border {
			t.Fatalf("侧栏应保留 1px 边界：%+v", bg)
		}
		if bg.CornerRadius != 0 || bg.TopLeftCornerRadius != 0 || bg.TopRightCornerRadius != 0 ||
			bg.BottomRightCornerRadius != 0 || bg.BottomLeftCornerRadius != 12 {
			t.Fatalf("侧栏应仅左下 12px 圆角、上方与右侧直角：%+v", bg)
		}
	})

	t.Run("三段左右留白", func(t *testing.T) {
		search := findCanvasObject(side, func(obj fyne.CanvasObject) bool {
			entry, ok := obj.(*widget.Entry)
			return ok && entry.PlaceHolder == "搜索工具"
		})
		if search == nil {
			t.Fatal("未找到工具搜索框")
		}
		nav := objectParent(side, s.navEntries["home"])
		if nav == nil {
			t.Fatal("未找到导航列表")
		}
		for name, target := range map[string]fyne.CanvasObject{
			"搜索":    search,
			"导航滚动区": nav,
			"底部设置":  s.navEntries["settings"],
		} {
			parent := objectParent(side, target)
			if parent == nil {
				t.Fatalf("%s缺少直接父容器", name)
			}
			if got := parent.MinSize().Width - target.MinSize().Width; got != 24 {
				t.Fatalf("%s左右外留白应均为 12，合计 24，实际 %.1f", name, got)
			}
		}
		searchParent := objectParent(side, search)
		if got := searchParent.MinSize().Height - search.MinSize().Height; got != 24 {
			t.Fatalf("搜索框上下留白应为顶部 12 + 底部 12，实际合计 %.1f", got)
		}
	})
}

func TestSidebarDisclosureAndGroupSpacing(t *testing.T) {
	app := test.NewApp()
	defer app.Quit()
	s := newTestShell(t, app, ui.NewFakeBackend(), Options{DisableTray: true, DisableScheduler: true})
	nav := container.NewVBox()
	s.rebuildNavEntries(nav, "")
	nav.Resize(fyne.NewSize(176, nav.MinSize().Height))

	home := s.navEntries["home"]
	history := s.navEntries["history"]
	if gap := history.Position().Y - (home.Position().Y + home.Size().Height); gap != 4 {
		t.Fatalf("组内导航间距应为 4，实际 %.1f", gap)
	}
	caption := findCanvasObject(nav, func(obj fyne.CanvasObject) bool {
		text, ok := obj.(*canvas.Text)
		return ok && text.Text == "主应用"
	})
	if caption == nil {
		t.Fatal("未找到主应用分组标题")
	}
	if caption.(*canvas.Text).TextSize != 12 {
		t.Fatalf("导航分组字号应为 12，实际 %.1f", caption.(*canvas.Text).TextSize)
	}
	captionBox := objectParent(nav, caption)
	if gap := captionBox.Position().Y - (history.Position().Y + history.Size().Height); gap != 12 {
		t.Fatalf("组间距离应为 12，实际 %.1f", gap)
	}

	var advanced *navEntry
	for _, obj := range nav.Objects {
		if entry, ok := obj.(*navEntry); ok && entry.title == "开发者工具" {
			advanced = entry
			break
		}
	}
	if advanced == nil {
		t.Fatal("开发者工具应使用与其他导航一致的图标行，而不是纯文字标准按钮")
	}
	var arrow *canvas.Image
	for _, obj := range advanced.CreateRenderer().Objects() {
		if icon, ok := obj.(*canvas.Image); ok && icon != advanced.icon {
			arrow = icon
		}
	}
	if arrow == nil {
		t.Fatal("开发者工具导航行缺少展开箭头")
	}
	advanced.Tapped(nil)
	if !s.advancedNavExpanded || len(s.navEntries) != 9 {
		t.Fatalf("点击图标行应展开开发者工具并保留导航状态：expanded=%v entries=%d", s.advancedNavExpanded, len(s.navEntries))
	}
}

func TestSharedCardSurfaceGeometry(t *testing.T) {
	app := test.NewApp()
	defer app.Quit()

	tests := []struct {
		name string
		root fyne.CanvasObject
	}{
		{name: "普通卡", root: newCard("标题", widget.NewLabel("内容"))},
		{name: "折叠卡", root: newCollapsibleSection("标题", widget.NewLabel("内容"), true).Root},
		{name: "历史卡", root: NewHistoryList("标题", true, nil).Root},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assertSharedCardSurface(t, tt.root)
		})
	}
}

func assertSharedCardSurface(t *testing.T, root fyne.CanvasObject) {
	t.Helper()
	box, ok := root.(*fyne.Container)
	if !ok || len(box.Objects) != 2 {
		t.Fatalf("卡片应复用统一白卡表面：%T", root)
	}
	bg, ok := box.Objects[0].(*canvas.Rectangle)
	if !ok || bg.CornerRadius != 12 || bg.StrokeWidth != 1 || bg.StrokeColor != Border {
		t.Fatalf("卡片应为 12px 圆角与 1px 边界：%+v", bg)
	}
	padded, ok := box.Objects[1].(*fyne.Container)
	if !ok || len(padded.Objects) != 1 {
		t.Fatalf("卡片缺少统一内容内边距：%T", box.Objects[1])
	}
	body := padded.Objects[0]
	paddedMin, bodyMin := padded.MinSize(), body.MinSize()
	pad := fyne.NewSize(paddedMin.Width-bodyMin.Width, paddedMin.Height-bodyMin.Height)
	if pad.Width != 28 || pad.Height != 28 {
		t.Fatalf("卡片四边内边距应均为 14，实际尺寸增量 %v", pad)
	}
}

func TestCollapsibleAlignmentAndCollapsedHeight(t *testing.T) {
	app := test.NewApp()
	defer app.Quit()

	sec := newCollapsibleSection("分组标题", widget.NewLabel("分组内容"), true)
	sec.Root.Resize(fyne.NewSize(400, sec.Root.MinSize().Height))
	if sec.header.label.Position().X != 0 || sec.content.Position().X != 0 {
		t.Fatalf("折叠卡标题与内容左缘应一致：title=%.1f content=%.1f", sec.header.label.Position().X, sec.content.Position().X)
	}
	expandedHeight := sec.Root.MinSize().Height
	sec.SetExpanded(false)
	if collapsedHeight := sec.Root.MinSize().Height; collapsedHeight >= expandedHeight {
		t.Fatalf("隐藏内容不得计入 MinSize 或留下空白：collapsed=%.1f expanded=%.1f", collapsedHeight, expandedHeight)
	}
}

func TestResponsiveLayoutsIgnoreHiddenObjects(t *testing.T) {
	app := test.NewApp()
	defer app.Quit()

	t.Run("双工具卡", func(t *testing.T) {
		first := canvas.NewRectangle(nil)
		first.SetMinSize(fyne.NewSize(100, 40))
		second := canvas.NewRectangle(nil)
		second.SetMinSize(fyne.NewSize(120, 50))
		second.Hide()
		pair := container.New(&toolPairLayout{breakAt: 520, gap: 16}, first, second)
		if got := pair.MinSize(); got != first.MinSize() {
			t.Fatalf("隐藏卡片不得计入双卡 MinSize：got=%v want=%v", got, first.MinSize())
		}
		pair.Resize(fyne.NewSize(400, 40))
		if first.Size().Width != 400 {
			t.Fatalf("唯一可见卡片应占满宽度，不留隐藏卡片空位：%.1f", first.Size().Width)
		}
	})

	t.Run("首页下部双栏", func(t *testing.T) {
		background := canvas.NewRectangle(nil)
		background.SetMinSize(fyne.NewSize(220, 60))
		recent := canvas.NewRectangle(nil)
		recent.SetMinSize(fyne.NewSize(320, 80))
		bottom := newHomeBottom(background, recent)
		bottom.Resize(fyne.NewSize(1000, 100))
		recent.Hide()
		if got := bottom.MinSize(); got != background.MinSize() {
			t.Fatalf("隐藏侧栏不得计入首页下部 MinSize：got=%v want=%v", got, background.MinSize())
		}
		bottom.Resize(fyne.NewSize(500, 100))
		if background.Size().Width != 500 {
			t.Fatalf("隐藏侧栏后主列应占满宽度：%.1f", background.Size().Width)
		}
	})
}

func TestHomeAndPageScrollShareOuterPagePadding(t *testing.T) {
	app := test.NewApp()
	defer app.Quit()
	s := newTestShell(t, app, ui.NewFakeBackend(), Options{DisableTray: true, DisableScheduler: true})

	stagePadding := objectParent(s.Win.Content(), s.stage)
	if stagePadding == nil {
		t.Fatal("未找到所有常驻页共用的壳层页面留白")
	}
	outer, inner := stagePadding.MinSize(), s.stage.MinSize()
	if width := outer.Width - inner.Width; width != 32 {
		t.Fatalf("首页、pageScroll 与历史页应共享左右各 16 的页面留白，实际合计 %.1f", width)
	}
	if height := outer.Height - inner.Height; height != 24 {
		t.Fatalf("共享页面顶部 12 + 底部 12 留白，实际合计 %.1f", height)
	}

	card := canvas.NewRectangle(nil)
	card.SetMinSize(fyne.NewSize(100, 40))
	scroll := pageScroll(card)
	if padded, ok := scroll.Content.(*fyne.Container); ok && len(padded.Objects) == 1 && padded.Objects[0] != card && padded.Layout != nil {
		t.Fatal("pageScroll 不应叠加第二套页面 padding，应使用壳层共享页面留白")
	}

	mainCol := container.NewVBox(canvas.NewRectangle(nil))
	bottom := newHomeBottom(canvas.NewRectangle(nil), canvas.NewRectangle(nil))
	home := newHomePane(mainCol, bottom)
	if home.mainScroll.Content != mainCol {
		t.Fatal("首页不应使用独立页面 padding，应与 pageScroll 和执行历史页共用壳层留白")
	}
}

func TestHomeQuickRowRendererGeometryAndActions(t *testing.T) {
	app := test.NewApp()
	defer app.Quit()
	s := newTestShell(t, app, ui.NewFakeBackend(), Options{DisableTray: true, DisableScheduler: true})
	home := s.pages["home"].(*pageHome)
	quick := home.quick

	if !quick.empty.Visible() || quick.scope.Visible() {
		t.Fatal("无收藏和最近工具时应保留原空态")
	}
	s.toggleFavorite("pcd_export")
	quick.refresh()
	if len(quick.box.Objects) != 1 {
		t.Fatalf("收藏后应渲染一个快捷行，实际 %d", len(quick.box.Objects))
	}
	rowWidget, ok := quick.box.Objects[0].(fyne.Widget)
	if !ok {
		t.Fatalf("快捷行应使用专用 Widget 渲染器，实际 %T", quick.box.Objects[0])
	}
	row := quick.box.Objects[0]
	row.Resize(fyne.NewSize(420, row.MinSize().Height))
	objects := rowWidget.CreateRenderer().Objects()
	if len(objects) != 4 {
		t.Fatalf("快捷行渲染器应包含图标、标题、描述和操作区，实际 %d", len(objects))
	}
	icon, iconOK := objects[0].(*canvas.Image)
	title, titleOK := objects[1].(*canvas.Text)
	desc, descOK := objects[2].(*widget.Label)
	actions, actionsOK := objects[3].(*fyne.Container)
	if !iconOK || !titleOK || !descOK || !actionsOK {
		t.Fatalf("快捷行渲染对象类型不符：%T %T %T %T", objects[0], objects[1], objects[2], objects[3])
	}
	if icon.Size().Width != 20 || icon.Size().Height != 20 {
		t.Fatalf("快捷行图标应为 20×20，实际 %v", icon.Size())
	}
	iconCenter := icon.Position().Y + icon.Size().Height/2
	titleCenter := title.Position().Y + title.Size().Height/2
	if iconCenter != titleCenter {
		t.Fatalf("20px 图标与标题应同中心线：%.1f / %.1f", iconCenter, titleCenter)
	}
	if title.Position().X != desc.Position().X || title.Position().X <= icon.Position().X+icon.Size().Width {
		t.Fatalf("标题与描述应同 X 且位于图标右侧：icon=%.1f title=%.1f desc=%.1f", icon.Position().X+icon.Size().Width, title.Position().X, desc.Position().X)
	}
	if got := desc.Size().Width; got != 420-title.Position().X {
		t.Fatalf("描述应使用操作区下方的完整剩余宽度，不被操作区挤压：got=%.1f want=%.1f", got, 420-title.Position().X)
	}
	if len(actions.Objects) != 2 {
		t.Fatalf("收藏和打开操作均应保留：%d", len(actions.Objects))
	}
	favorite, favoriteOK := actions.Objects[0].(*appButton)
	open, openOK := actions.Objects[1].(*appButton)
	if !favoriteOK || !openOK {
		t.Fatalf("收藏或打开操作类型不符：%T %T", actions.Objects[0], actions.Objects[1])
	}
	if favorite.AccessibilityLabel() != "取消收藏" || favorite.Icon == nil || open.Icon == nil {
		t.Fatalf("收藏操作名称或动作图标不符：%q", favorite.AccessibilityLabel())
	}
	open.Tapped(nil)
	if s.current != "pcd_export" {
		t.Fatalf("打开操作应进入对应工具，实际 %s", s.current)
	}
	favorite.Tapped(nil)
	if s.isFavorite("pcd_export") {
		t.Fatal("取消收藏行为不应改变")
	}
}

func TestSidebarSearchWithoutMatchesHasNoTrailingGap(t *testing.T) {
	app := test.NewApp()
	defer app.Quit()
	s := newTestShell(t, app, ui.NewFakeBackend(), Options{DisableTray: true, DisableScheduler: true})
	nav := container.NewVBox()
	s.rebuildNavEntries(nav, "绝对没有匹配的工具")

	if len(nav.Objects) == 0 {
		t.Fatal("搜索时仍应保留开发者工具展开行")
	}
	advanced, ok := nav.Objects[len(nav.Objects)-1].(*navEntry)
	if !ok || advanced.title != "开发者工具" {
		t.Fatalf("零命中搜索不得留下无内容的尾部间距：%T", nav.Objects[len(nav.Objects)-1])
	}
	if got := nav.MinSize().Height; got != 44 {
		t.Fatalf("零命中搜索应只保留 44px 展开行，实际 %.1f", got)
	}
}

func TestHomeQuickRowsKeepSixEntriesAndEmptyState(t *testing.T) {
	app := test.NewApp()
	defer app.Quit()
	s := newTestShell(t, app, ui.NewFakeBackend(), Options{DisableTray: true, DisableScheduler: true})
	quick := s.pages["home"].(*pageHome).quick

	for _, task := range homeTaskList() {
		s.toggleFavorite(task.id)
	}
	quick.refresh()
	rows := 0
	for _, obj := range quick.box.Objects {
		if _, separator := obj.(*widget.Separator); !separator {
			rows++
		}
	}
	if rows != 6 || quick.scope.Visible() == false || quick.empty.Visible() {
		t.Fatalf("六项快捷入口与刷新状态应保留：rows=%d box=%v empty=%v", rows, quick.scope.Visible(), quick.empty.Visible())
	}
	for _, task := range homeTaskList() {
		s.toggleFavorite(task.id)
	}
	quick.refresh()
	if !quick.empty.Visible() || quick.scope.Visible() {
		t.Fatal("全部取消收藏且无最近工具时应恢复空态")
	}
}

func findCanvasObject(root fyne.CanvasObject, match func(fyne.CanvasObject) bool) fyne.CanvasObject {
	if root == nil {
		return nil
	}
	if match(root) {
		return root
	}
	switch obj := root.(type) {
	case *fyne.Container:
		for _, child := range obj.Objects {
			if found := findCanvasObject(child, match); found != nil {
				return found
			}
		}
	case *container.Scroll:
		return findCanvasObject(obj.Content, match)
	}
	return nil
}

func objectParent(root, target fyne.CanvasObject) fyne.CanvasObject {
	if root == nil || target == nil {
		return nil
	}
	switch obj := root.(type) {
	case *fyne.Container:
		for _, child := range obj.Objects {
			if child == target {
				return root
			}
			if parent := objectParent(child, target); parent != nil {
				return parent
			}
		}
	case *container.Scroll:
		if obj.Content == target {
			return root
		}
		return objectParent(obj.Content, target)
	case *minBox:
		if obj.content == target {
			return root
		}
		return objectParent(obj.content, target)
	}
	return nil
}
