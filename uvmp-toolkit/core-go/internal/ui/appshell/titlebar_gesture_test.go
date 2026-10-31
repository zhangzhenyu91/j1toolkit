package appshell

import (
	"image/color"
	"testing"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/driver/desktop"
	"fyne.io/fyne/v2/test"
)

func TestWindowDragArea_OneNativeMovePerGesture(t *testing.T) {
	ops := &fakeWindowOps{}
	drag := newWindowDragArea(canvas.NewRectangle(color.Transparent), ops)
	for range 5 {
		drag.Dragged(&fyne.DragEvent{})
	}
	if ops.moves != 1 {
		t.Fatalf("同一手势重复启动原生移动：%d", ops.moves)
	}
	drag.DragEnd()
	for range 5 {
		drag.Dragged(&fyne.DragEvent{})
	}
	if ops.moves != 2 {
		t.Fatalf("第二次手势未能重新启动原生移动：%d", ops.moves)
	}
}

func TestWindowDragArea_MouseDownResetsStaleDrag(t *testing.T) {
	ops := &fakeWindowOps{}
	drag := newWindowDragArea(canvas.NewRectangle(color.Transparent), ops)
	drag.Dragged(&fyne.DragEvent{})
	if ops.moves != 1 {
		t.Fatalf("首次拖动未启动原生移动：%d", ops.moves)
	}
	// 原生模态移动可能消费 ButtonRelease，DragEnd 不会到达；下一次按下必须复位手势标志。
	drag.MouseDown(&desktop.MouseEvent{Button: desktop.MouseButtonPrimary})
	drag.Dragged(&fyne.DragEvent{})
	if ops.moves != 2 {
		t.Fatalf("MouseDown 未复位陈旧拖动手势：%d", ops.moves)
	}
}

func TestWindowDragArea_NonPrimaryMouseDownKeepsGesture(t *testing.T) {
	ops := &fakeWindowOps{}
	drag := newWindowDragArea(canvas.NewRectangle(color.Transparent), ops)
	drag.Dragged(&fyne.DragEvent{})
	drag.MouseDown(&desktop.MouseEvent{Button: desktop.MouseButtonSecondary})
	drag.Dragged(&fyne.DragEvent{})
	if ops.moves != 1 {
		t.Fatalf("非主键按下不应复位拖动手势：%d", ops.moves)
	}
}

func TestWindowDragArea_DoubleTapDoesNotStartMove(t *testing.T) {
	ops := &fakeWindowOps{}
	drag := newWindowDragArea(canvas.NewRectangle(color.Transparent), ops)
	drag.DoubleTapped(&fyne.PointEvent{})
	if ops.maximized != 1 {
		t.Fatalf("双击未切换最大化：%d", ops.maximized)
	}
	if ops.moves != 0 {
		t.Fatalf("双击不应启动窗口移动：%d", ops.moves)
	}
}

func TestWindowResizeGrip_OneNativeResizePerGesture(t *testing.T) {
	ops := &fakeWindowOps{}
	grip := newWindowResizeGrip(resizeBottomRight, ops)
	for range 5 {
		grip.Dragged(&fyne.DragEvent{})
	}
	if len(ops.resizes) != 1 || ops.resizes[0] != resizeBottomRight {
		t.Fatalf("同一手势重复启动原生缩放：%v", ops.resizes)
	}
	grip.DragEnd()
	for range 5 {
		grip.Dragged(&fyne.DragEvent{})
	}
	if len(ops.resizes) != 2 || ops.resizes[1] != resizeBottomRight {
		t.Fatalf("第二次手势未能重新启动原生缩放：%v", ops.resizes)
	}
}

func TestWindowResizeGrip_MouseDownResetsStaleResize(t *testing.T) {
	ops := &fakeWindowOps{}
	grip := newWindowResizeGrip(resizeBottomRight, ops)
	grip.Dragged(&fyne.DragEvent{})
	if len(ops.resizes) != 1 {
		t.Fatalf("首次拖动未启动原生缩放：%v", ops.resizes)
	}
	grip.MouseDown(&desktop.MouseEvent{Button: desktop.MouseButtonPrimary})
	grip.Dragged(&fyne.DragEvent{})
	if len(ops.resizes) != 2 {
		t.Fatalf("MouseDown 未复位陈旧缩放手势：%v", ops.resizes)
	}
	// DragEnd 仍幂等，不重复 EndResize。
	grip.DragEnd()
	grip.DragEnd()
	if len(ops.resizes) != 2 {
		t.Fatalf("DragEnd 重复调用改变了缩放次数：%v", ops.resizes)
	}
}

func TestWindowResizeGrip_CursorMatchesDirection(t *testing.T) {
	tests := []struct {
		direction int
		want      desktop.Cursor
	}{
		{resizeTopLeft, desktop.NWSEResizeCursor},
		{resizeTop, desktop.VResizeCursor},
		{resizeTopRight, desktop.NESWResizeCursor},
		{resizeRight, desktop.HResizeCursor},
		{resizeBottomRight, desktop.NWSEResizeCursor},
		{resizeBottom, desktop.VResizeCursor},
		{resizeBottomLeft, desktop.NESWResizeCursor},
		{resizeLeft, desktop.HResizeCursor},
	}
	for _, tt := range tests {
		grip := newWindowResizeGrip(tt.direction, &fakeWindowOps{})
		cursorable, ok := any(grip).(interface{ Cursor() desktop.Cursor })
		if !ok {
			t.Fatalf("方向 %d 未提供边缘缩放指针", tt.direction)
		}
		if got := cursorable.Cursor(); got != tt.want {
			t.Errorf("方向 %d 指针错误：got=%v want=%v", tt.direction, got, tt.want)
		}
	}
}

func TestWindowTitleBar_ControlsAreFlushAndClickable(t *testing.T) {
	a := test.NewApp()
	defer a.Quit()
	a.Settings().SetTheme(NewTheme())
	ops := &fakeWindowOps{edgeResize: true}
	closed := 0
	var bar *windowTitleBar
	var topRightGrip *windowResizeGrip
	runUI(func() { bar = newWindowTitleBar(ops, func() { closed++ }) })
	win := a.NewWindow("窗口控制几何")
	defer win.Close()
	win.SetPadded(false)
	runUI(func() {
		root := container.New(windowBodyLayout{top: bar}, canvas.NewRectangle(color.Transparent), bar)
		frame := newWindowFrame(ops, root)
		overlay := frame.(*fyne.Container).Objects[1].(*windowResizeOverlay)
		topRightGrip = overlay.grips[resizeTopRight]
		win.SetContent(frame)
		win.Resize(fyne.NewSize(600, 120))
	})

	buttons := []*windowControlButton{bar.minimize, bar.maximize, bar.close}
	for _, button := range buttons {
		if got, want := button.MinSize(), fyne.NewSize(44, 48); got != want {
			t.Errorf("%s 最小命中区错误：got=%v want=%v", button.label, got, want)
		}
		if got, want := button.Size(), fyne.NewSize(44, 48); got != want {
			t.Errorf("%s 实际命中区错误：got=%v want=%v", button.label, got, want)
		}
	}
	driver := a.Driver()
	minimizePos := driver.AbsolutePositionForObject(bar.minimize)
	maximizePos := driver.AbsolutePositionForObject(bar.maximize)
	closePos := driver.AbsolutePositionForObject(bar.close)
	barPos := driver.AbsolutePositionForObject(bar)
	if maximizePos.X-minimizePos.X != 44 || closePos.X-maximizePos.X != 44 {
		t.Errorf("窗口按钮之间存在间距：%.1f / %.1f", maximizePos.X-minimizePos.X, closePos.X-maximizePos.X)
	}
	if closePos.X+bar.close.Size().Width != barPos.X+bar.Size().Width {
		t.Errorf("关闭按钮未贴住内容区右边：close=%v bar=%v", closePos, barPos)
	}
	if closePos.Y != barPos.Y {
		t.Errorf("关闭按钮未贴住内容区顶边：close=%v bar=%v", closePos, barPos)
	}
	if got := topRightGrip.Size(); got != fyne.NewSquareSize(16) {
		t.Fatalf("右上 grip 尺寸错误：%v", got)
	}
	gripPos := driver.AbsolutePositionForObject(topRightGrip)
	left := max(closePos.X, gripPos.X)
	top := max(closePos.Y, gripPos.Y)
	right := min(closePos.X+bar.close.Size().Width, gripPos.X+topRightGrip.Size().Width)
	bottom := min(closePos.Y+bar.close.Size().Height, gripPos.Y+topRightGrip.Size().Height)
	if right-left < 4 || bottom-top < 4 {
		t.Fatalf("关闭按钮与右上 grip 没有有效交集：close=%v grip=%v", closePos, gripPos)
	}
	clickTests := []struct {
		name string
		at   fyne.Position
	}{
		{"关闭中心", closePos.Add(fyne.NewPos(22, 24))},
		{"顶部 6px 重叠区", closePos.Add(fyne.NewPos(22, 3))},
		{"右侧 6px 重叠区", closePos.Add(fyne.NewPos(41, 24))},
		{"右上 16×16 重叠区起点", fyne.NewPos(left+2, top+2)},
		{"右上 16×16 重叠区末端", fyne.NewPos(right-2, bottom-2)},
	}
	for _, tt := range clickTests {
		before := closed
		test.TapCanvas(win.Canvas(), tt.at)
		if closed != before+1 {
			t.Errorf("%s未触发关闭：次数 %d → %d", tt.name, before, closed)
		}
	}
}

func TestWindowControlButton_PointerCursor(t *testing.T) {
	button := newWindowControlButton("close", "关闭", nil)
	if got := button.Cursor(); got != desktop.PointerCursor {
		t.Fatalf("窗口控制按钮指针错误：got=%v want=%v", got, desktop.PointerCursor)
	}
}

func TestWindowControlButton_HoverAndFocusUseSeparateGeometry(t *testing.T) {
	a := test.NewApp()
	defer a.Quit()
	a.Settings().SetTheme(NewTheme())
	button := newWindowControlButton("close", "关闭", nil)
	runUI(func() {
		button.Resize(fyne.NewSize(44, 48))
		button.MouseIn(nil)
		button.FocusGained()
	})
	if button.bg.CornerRadius != 0 {
		t.Fatalf("悬停背景必须直角铺满：%.1f", button.bg.CornerRadius)
	}
	if got, want := button.bg.Size(), fyne.NewSize(44, 48); got != want {
		t.Fatalf("悬停背景未铺满命中区：got=%v want=%v", got, want)
	}
	if button.bg.StrokeWidth != 0 {
		t.Fatalf("键盘焦点不应复用悬停背景外缘：%.1f", button.bg.StrokeWidth)
	}
	objects := test.WidgetRenderer(button).Objects()
	if len(objects) != 3 {
		t.Fatalf("缺少独立焦点描边对象：%d", len(objects))
	}
	focus, ok := objects[2].(*canvas.Rectangle)
	if !ok || focus == button.bg {
		t.Fatalf("焦点描边未独立绘制：%T", objects[2])
	}
	if got, want := focus.Position(), fyne.NewPos(2, 2); got != want {
		t.Fatalf("焦点描边未内缩 2px：got=%v want=%v", got, want)
	}
	if got, want := focus.Size(), fyne.NewSize(40, 44); got != want {
		t.Fatalf("焦点描边尺寸错误：got=%v want=%v", got, want)
	}
	if focus.StrokeColor != BrandBlue || focus.StrokeWidth == 0 {
		t.Fatalf("焦点描边缺少可见边框：%+v", focus)
	}
}

func TestWindowTitleBar_DividerStaysWithin48Pixels(t *testing.T) {
	a := test.NewApp()
	defer a.Quit()
	a.Settings().SetTheme(NewTheme())
	bar := newWindowTitleBar(&fakeWindowOps{}, func() {})
	runUI(func() { bar.Resize(fyne.NewSize(600, titleBarHeight)) })
	if got := bar.MinSize().Height; got != 48 {
		t.Fatalf("顶栏总高错误：%.1f", got)
	}
	objects := test.WidgetRenderer(bar).Objects()
	if len(objects) != 2 {
		t.Fatalf("顶栏缺少独立底部分隔线：%d", len(objects))
	}
	divider, ok := objects[1].(*canvas.Rectangle)
	if !ok {
		t.Fatalf("顶栏分隔线类型错误：%T", objects[1])
	}
	if divider.FillColor != Border {
		t.Fatalf("顶栏分隔线颜色错误：%v", divider.FillColor)
	}
	if got, want := divider.Position(), fyne.NewPos(0, 47); got != want {
		t.Fatalf("顶栏分隔线位置错误：got=%v want=%v", got, want)
	}
	if got, want := divider.Size(), fyne.NewSize(600, 1); got != want {
		t.Fatalf("顶栏分隔线尺寸错误：got=%v want=%v", got, want)
	}
}
