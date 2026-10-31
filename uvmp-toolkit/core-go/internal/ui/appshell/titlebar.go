package appshell

import (
	"fmt"
	"image/color"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/driver/desktop"
	"fyne.io/fyne/v2/widget"
)

const (
	titleBarHeight   float32 = 48
	windowButtonSize float32 = 44
)

// windowBrand 使用单一文本对象，天然共享基线，不再引入单独字体和位图管线。
type windowBrand struct {
	widget.BaseWidget
	icon  *canvas.Image
	label *canvas.Text
}

func newWindowBrand() *windowBrand {
	b := &windowBrand{}
	b.ExtendBaseWidget(b)
	b.icon = canvas.NewImageFromResource(ShellIcon())
	b.icon.FillMode = canvas.ImageFillContain
	b.icon.ScaleMode = canvas.ImageScaleSmooth
	b.label = canvas.NewText("Shade 壹匣 · 内网工具箱", TextPrimary)
	b.label.TextSize = 13
	b.label.TextStyle = fyne.TextStyle{Bold: true}
	return b
}

func (b *windowBrand) CreateRenderer() fyne.WidgetRenderer {
	return &windowBrandRenderer{b: b}
}

type windowBrandRenderer struct{ b *windowBrand }

func (r *windowBrandRenderer) Layout(size fyne.Size) {
	r.b.icon.Move(fyne.NewPos(14, (size.Height-20)/2))
	r.b.icon.Resize(fyne.NewSquareSize(20))
	textSize := r.b.label.MinSize()
	r.b.label.Move(fyne.NewPos(44, (size.Height-textSize.Height)/2))
	r.b.label.Resize(textSize)
}
func (r *windowBrandRenderer) MinSize() fyne.Size {
	return fyne.NewSize(58+r.b.label.MinSize().Width, titleBarHeight)
}
func (r *windowBrandRenderer) Refresh() {
	r.b.icon.Refresh()
	r.b.label.Refresh()
}
func (r *windowBrandRenderer) Destroy() {}
func (r *windowBrandRenderer) Objects() []fyne.CanvasObject {
	return []fyne.CanvasObject{r.b.icon, r.b.label}
}

// windowTitleBar 将产品标识、窗口拖动区与三枚窗口控制按钮放在同一连续页首。
type windowTitleBar struct {
	widget.BaseWidget
	ops                   nativeWindowOps
	root                  fyne.CanvasObject
	divider               *canvas.Rectangle
	minimize              *windowControlButton
	maximize              *windowControlButton
	close                 *windowControlButton
	onMaximizeChanged     func(bool)
	lastMaximized         bool
	maximizeStateKnown    bool
	maximizeCallbackReady bool
}

func newWindowTitleBar(ops nativeWindowOps, closeWindow func()) *windowTitleBar {
	t := &windowTitleBar{ops: ops}
	t.ExtendBaseWidget(t)

	left := newWindowDragArea(newWindowBrand(), ops)
	middle := newWindowDragArea(canvas.NewRectangle(color.Transparent), ops)
	left.onMaximize = func() { t.syncMaximize() }
	middle.onMaximize = left.onMaximize

	t.minimize = newWindowControlButton("minimize", "最小化", ops.Minimize)
	t.maximize = newWindowControlButton("maximize", "最大化", func() {
		ops.ToggleMaximize()
		t.syncMaximize()
	})
	t.close = newWindowControlButton("close", "关闭", closeWindow)
	right := container.New(windowControlsLayout{}, t.minimize, t.maximize, t.close)
	row := container.NewBorder(nil, nil, left, right, middle)
	t.root = container.NewStack(canvas.NewRectangle(CardBG), row)
	t.divider = canvas.NewRectangle(Border)
	t.syncMaximize()
	return t
}

// windowBodyLayout 顶栏占顶部固定高度，其余内容铺满剩余空间；仅在显式设置 gap 时留间隙。
type windowBodyLayout struct {
	top fyne.CanvasObject
	gap float32
}

func (l windowBodyLayout) Layout(objects []fyne.CanvasObject, size fyne.Size) {
	height := l.top.MinSize().Height
	l.top.Move(fyne.NewPos(0, 0))
	l.top.Resize(fyne.NewSize(size.Width, height))
	for _, obj := range objects {
		if obj != l.top {
			obj.Move(fyne.NewPos(0, height+l.gap))
			obj.Resize(fyne.NewSize(size.Width, size.Height-height-l.gap))
		}
	}
}

func (l windowBodyLayout) MinSize(objects []fyne.CanvasObject) fyne.Size {
	result := fyne.Size{}
	for _, obj := range objects {
		if obj == l.top {
			continue
		}
		m := obj.MinSize()
		if m.Width > result.Width {
			result.Width = m.Width
		}
		if m.Height > result.Height {
			result.Height = m.Height
		}
	}
	result.Height += l.top.MinSize().Height + l.gap
	return result
}

func (t *windowTitleBar) syncMaximize() {
	maximized := t.ops.Maximized()
	if maximized {
		t.maximize.SetKind("restore")
	} else {
		t.maximize.SetKind("maximize")
	}
	if t.onMaximizeChanged != nil && !t.maximizeCallbackReady {
		t.maximizeCallbackReady = true
		t.lastMaximized = maximized
		t.maximizeStateKnown = true
		if maximized {
			t.onMaximizeChanged(maximized)
		}
		return
	}
	if !t.maximizeStateKnown {
		t.lastMaximized = maximized
		t.maximizeStateKnown = true
		return
	}
	if t.lastMaximized == maximized {
		return
	}
	t.lastMaximized = maximized
	if t.onMaximizeChanged != nil {
		t.onMaximizeChanged(maximized)
	}
}

func (t *windowTitleBar) CreateRenderer() fyne.WidgetRenderer {
	return &windowTitleBarRenderer{t: t}
}

type windowTitleBarRenderer struct{ t *windowTitleBar }

func (r *windowTitleBarRenderer) Layout(size fyne.Size) {
	// ShowWindowAsync 与窗口管理器均异步落定；尺寸重排时读取真实状态，
	// 避免按钮/双击后图标仍停留在上一个最大化状态。
	r.t.syncMaximize()
	r.t.root.Resize(size)
	r.t.divider.Move(fyne.NewPos(0, size.Height-1))
	r.t.divider.Resize(fyne.NewSize(size.Width, 1))
}
func (r *windowTitleBarRenderer) MinSize() fyne.Size {
	m := r.t.root.MinSize()
	m.Height = titleBarHeight
	return m
}
func (r *windowTitleBarRenderer) Refresh() {
	r.t.syncMaximize()
	r.t.root.Refresh()
	r.t.divider.Refresh()
}
func (r *windowTitleBarRenderer) Destroy() {}
func (r *windowTitleBarRenderer) Objects() []fyne.CanvasObject {
	return []fyne.CanvasObject{r.t.root, r.t.divider}
}

type windowControlsLayout struct{}

func (windowControlsLayout) Layout(objects []fyne.CanvasObject, size fyne.Size) {
	for i, obj := range objects {
		obj.Move(fyne.NewPos(float32(i)*windowButtonSize, 0))
		obj.Resize(fyne.NewSize(windowButtonSize, size.Height))
	}
}

func (windowControlsLayout) MinSize(objects []fyne.CanvasObject) fyne.Size {
	return fyne.NewSize(float32(len(objects))*windowButtonSize, titleBarHeight)
}

// windowDragArea 将整段空白与品牌区交给系统移动窗口，双击切换最大化。
type windowDragArea struct {
	widget.BaseWidget
	content    fyne.CanvasObject
	ops        nativeWindowOps
	onMaximize func()
	dragging   bool
}

func newWindowDragArea(content fyne.CanvasObject, ops nativeWindowOps) *windowDragArea {
	d := &windowDragArea{content: content, ops: ops}
	d.ExtendBaseWidget(d)
	return d
}

func (d *windowDragArea) Dragged(*fyne.DragEvent) {
	if d.dragging {
		return
	}
	d.dragging = true
	d.ops.BeginMove()
}
func (d *windowDragArea) DragEnd() { d.dragging = false }

// MouseDown 只复位上一手势的陈旧标志：原生模态移动可能消费 ButtonRelease，
// 导致 DragEnd 不到达；按下本身不启动移动、不消费 Draggable/DoubleTapped。
func (d *windowDragArea) MouseDown(event *desktop.MouseEvent) {
	if event != nil && event.Button == desktop.MouseButtonPrimary {
		d.dragging = false
	}
}

func (d *windowDragArea) MouseUp(*desktop.MouseEvent) {}

func (d *windowDragArea) DoubleTapped(*fyne.PointEvent) {
	d.ops.ToggleMaximize()
	if d.onMaximize != nil {
		d.onMaximize()
	}
}
func (d *windowDragArea) CreateRenderer() fyne.WidgetRenderer {
	return widget.NewSimpleRenderer(d.content)
}

var _ fyne.Draggable = (*windowDragArea)(nil)
var _ fyne.DoubleTappable = (*windowDragArea)(nil)
var _ desktop.Mouseable = (*windowDragArea)(nil)

// windowControlButton 矢量图标按钮：44×48 命中区、悬停反馈、Tab/空格/回车可用。
type windowControlButton struct {
	widget.BaseWidget
	label   string
	kind    string
	onTap   func()
	hovered bool
	focused bool
	bg      *canvas.Rectangle
	focus   *canvas.Rectangle
	icon    *canvas.Image
}

func newWindowControlButton(kind, label string, onTap func()) *windowControlButton {
	b := &windowControlButton{kind: kind, label: label, onTap: onTap}
	b.ExtendBaseWidget(b)
	b.bg = canvas.NewRectangle(color.Transparent)
	b.focus = canvas.NewRectangle(color.Transparent)
	b.focus.StrokeWidth = 1.5
	b.icon = canvas.NewImageFromResource(windowControlIcon(kind, TextSecondary))
	b.icon.FillMode = canvas.ImageFillContain
	b.icon.ScaleMode = canvas.ImageScaleSmooth
	return b
}

func (b *windowControlButton) AccessibilityLabel() string { return b.label }
func (b *windowControlButton) AccessibilityRole() fyne.AccessibleRole {
	return fyne.AccessibleRoleButton
}
func (b *windowControlButton) Cursor() desktop.Cursor { return desktop.PointerCursor }
func (b *windowControlButton) SetKind(kind string) {
	if b.kind == kind {
		return
	}
	b.kind = kind
	b.Refresh()
}
func (b *windowControlButton) Tapped(*fyne.PointEvent) {
	if c := fyne.CurrentApp().Driver().CanvasForObject(b); c != nil {
		c.Focus(b)
	}
	animateControlTap(b)
	if b.onTap != nil {
		b.onTap()
	}
}
func (b *windowControlButton) TypedKey(event *fyne.KeyEvent) {
	if event.Name == fyne.KeySpace || event.Name == fyne.KeyReturn {
		b.Tapped(nil)
	}
}
func (b *windowControlButton) TypedRune(rune) {}
func (b *windowControlButton) FocusGained() {
	b.focused = true
	b.Refresh()
}
func (b *windowControlButton) FocusLost() {
	b.focused = false
	b.Refresh()
}
func (b *windowControlButton) MouseIn(*desktop.MouseEvent) {
	b.hovered = true
	b.Refresh()
}
func (b *windowControlButton) MouseMoved(*desktop.MouseEvent) {}
func (b *windowControlButton) MouseOut() {
	b.hovered = false
	b.Refresh()
}

func (b *windowControlButton) applyStyle() {
	foreground := TextSecondary
	b.bg.FillColor = color.Transparent
	if b.hovered {
		foreground = TextPrimary
		b.bg.FillColor = BlockBG
		if b.kind == "close" {
			foreground = DangerRed
			b.bg.FillColor = DangerRedBG
		}
	}
	b.bg.StrokeColor = color.Transparent
	b.bg.StrokeWidth = 0
	b.focus.StrokeColor = color.Transparent
	if b.focused {
		b.focus.StrokeColor = BrandBlue
	}
	b.icon.Resource = windowControlIcon(b.kind, foreground)
	b.bg.Refresh()
	b.focus.Refresh()
	b.icon.Refresh()
}

func (b *windowControlButton) CreateRenderer() fyne.WidgetRenderer {
	return &windowControlRenderer{b: b}
}

var _ fyne.Tappable = (*windowControlButton)(nil)
var _ fyne.Focusable = (*windowControlButton)(nil)
var _ desktop.Hoverable = (*windowControlButton)(nil)
var _ fyne.Accessible = (*windowControlButton)(nil)

type windowControlRenderer struct{ b *windowControlButton }

func (r *windowControlRenderer) Layout(size fyne.Size) {
	r.b.bg.Resize(size)
	r.b.focus.Move(fyne.NewPos(2, 2))
	r.b.focus.Resize(fyne.NewSize(size.Width-4, size.Height-4))
	iconSize := float32(18)
	r.b.icon.Move(fyne.NewPos((size.Width-iconSize)/2, (size.Height-iconSize)/2))
	r.b.icon.Resize(fyne.NewSquareSize(iconSize))
}
func (r *windowControlRenderer) MinSize() fyne.Size {
	return fyne.NewSize(windowButtonSize, titleBarHeight)
}
func (r *windowControlRenderer) Refresh() { r.b.applyStyle() }
func (r *windowControlRenderer) Destroy() {}
func (r *windowControlRenderer) Objects() []fyne.CanvasObject {
	return []fyne.CanvasObject{r.b.bg, r.b.icon, r.b.focus}
}

var windowControlPaths = map[string]string{
	"minimize": `<path d="M5 12h14"/>`,
	"maximize": `<rect x="5" y="5" width="14" height="14" rx="1.5"/>`,
	"restore":  `<rect x="7.5" y="7.5" width="11.5" height="11.5" rx="1.5"/><path d="M5 16.5H4.5A1.5 1.5 0 0 1 3 15V4.5A1.5 1.5 0 0 1 4.5 3H15a1.5 1.5 0 0 1 1.5 1.5V5"/>`,
	"close":    `<path d="m6 6 12 12M18 6 6 18"/>`,
}

func windowControlIcon(kind string, c color.Color) fyne.Resource {
	path := windowControlPaths[kind]
	if path == "" {
		path = windowControlPaths["maximize"]
	}
	svg := fmt.Sprintf(lineIconTemplate, hexColor(c), path)
	return fyne.NewStaticResource("window-"+kind+".svg", []byte(svg))
}

// windowResizeOverlay 是无边框窗口的透明边缘热区；命中后由系统或 WM 原生完成缩放。
type windowResizeOverlay struct {
	widget.BaseWidget
	grips []*windowResizeGrip
}

func newWindowResizeOverlay(ops nativeWindowOps) *windowResizeOverlay {
	o := &windowResizeOverlay{}
	o.ExtendBaseWidget(o)
	for direction := resizeTopLeft; direction <= resizeLeft; direction++ {
		o.grips = append(o.grips, newWindowResizeGrip(direction, ops))
	}
	return o
}

func (o *windowResizeOverlay) CreateRenderer() fyne.WidgetRenderer {
	objects := make([]fyne.CanvasObject, len(o.grips))
	for i, grip := range o.grips {
		objects[i] = grip
	}
	return &windowResizeRenderer{o: o, objects: objects}
}

type windowResizeRenderer struct {
	o       *windowResizeOverlay
	objects []fyne.CanvasObject
}

func (r *windowResizeRenderer) Layout(size fyne.Size) {
	const edge = float32(6)
	const corner = float32(16)
	g := r.o.grips
	g[resizeTopLeft].Move(fyne.NewPos(0, 0))
	g[resizeTopLeft].Resize(fyne.NewSquareSize(corner))
	g[resizeTopRight].Move(fyne.NewPos(size.Width-corner, 0))
	g[resizeTopRight].Resize(fyne.NewSquareSize(corner))
	g[resizeBottomLeft].Move(fyne.NewPos(0, size.Height-corner))
	g[resizeBottomLeft].Resize(fyne.NewSquareSize(corner))
	g[resizeBottomRight].Move(fyne.NewPos(size.Width-corner, size.Height-corner))
	g[resizeBottomRight].Resize(fyne.NewSquareSize(corner))
	g[resizeTop].Move(fyne.NewPos(corner, 0))
	g[resizeTop].Resize(fyne.NewSize(size.Width-2*corner, edge))
	g[resizeBottom].Move(fyne.NewPos(corner, size.Height-edge))
	g[resizeBottom].Resize(fyne.NewSize(size.Width-2*corner, edge))
	g[resizeLeft].Move(fyne.NewPos(0, corner))
	g[resizeLeft].Resize(fyne.NewSize(edge, size.Height-2*corner))
	g[resizeRight].Move(fyne.NewPos(size.Width-edge, corner))
	g[resizeRight].Resize(fyne.NewSize(edge, size.Height-2*corner))
}
func (r *windowResizeRenderer) MinSize() fyne.Size { return fyne.Size{} }
func (r *windowResizeRenderer) Refresh()           {}
func (r *windowResizeRenderer) Destroy()           {}
func (r *windowResizeRenderer) Objects() []fyne.CanvasObject {
	return r.objects
}

type windowResizeGrip struct {
	widget.BaseWidget
	direction int
	ops       nativeWindowOps
	dragging  bool
}

func newWindowResizeGrip(direction int, ops nativeWindowOps) *windowResizeGrip {
	g := &windowResizeGrip{direction: direction, ops: ops}
	g.ExtendBaseWidget(g)
	return g
}
func (g *windowResizeGrip) Dragged(*fyne.DragEvent) {
	if g.dragging {
		return
	}
	g.dragging = true
	g.ops.BeginResize(g.direction)
}
func (g *windowResizeGrip) DragEnd() {
	if !g.dragging {
		return
	}
	g.dragging = false
	g.ops.EndResize()
}

// MouseDown 与拖动区一致：只复位被原生模态消费 DragEnd 后遗留的陈旧标志。
func (g *windowResizeGrip) MouseDown(event *desktop.MouseEvent) {
	if event != nil && event.Button == desktop.MouseButtonPrimary {
		g.dragging = false
	}
}

func (g *windowResizeGrip) MouseUp(*desktop.MouseEvent) {}
func (g *windowResizeGrip) Cursor() desktop.Cursor {
	switch g.direction {
	case resizeTopLeft, resizeBottomRight:
		return desktop.NWSEResizeCursor
	case resizeTop, resizeBottom:
		return desktop.VResizeCursor
	case resizeTopRight, resizeBottomLeft:
		return desktop.NESWResizeCursor
	case resizeRight, resizeLeft:
		return desktop.HResizeCursor
	default:
		return desktop.DefaultCursor
	}
}
func (g *windowResizeGrip) CreateRenderer() fyne.WidgetRenderer {
	return widget.NewSimpleRenderer(canvas.NewRectangle(color.Transparent))
}

var _ fyne.Draggable = (*windowResizeGrip)(nil)
var _ desktop.Cursorable = (*windowResizeGrip)(nil)
var _ desktop.Mouseable = (*windowResizeGrip)(nil)

// windowOutlineLayout 一圈细边框，不含额外画布或右侧/底部留白。
type windowOutlineLayout struct{ outline fyne.CanvasObject }

func (l windowOutlineLayout) Layout(objects []fyne.CanvasObject, size fyne.Size) {
	l.outline.Move(fyne.NewPos(.5, .5))
	l.outline.Resize(fyne.NewSize(size.Width-1, size.Height-1))
	for _, obj := range objects {
		if obj != l.outline {
			obj.Move(fyne.NewPos(1, 1))
			obj.Resize(fyne.NewSize(size.Width-2, size.Height-2))
		}
	}
}

func (l windowOutlineLayout) MinSize(objects []fyne.CanvasObject) fyne.Size {
	result := fyne.Size{}
	for _, obj := range objects {
		if obj == l.outline {
			continue
		}
		m := obj.MinSize()
		result = result.Max(m)
	}
	return result.Add(fyne.NewSize(2, 2))
}

func newWindowFrame(ops nativeWindowOps, content fyne.CanvasObject) fyne.CanvasObject {
	outline := canvas.NewRectangle(color.Transparent)
	outline.StrokeColor = Border
	outline.StrokeWidth = 1
	outline.CornerRadius = 12
	framed := container.NewStack(canvas.NewRectangle(CardBG),
		container.New(windowOutlineLayout{outline: outline}, content, outline))
	if !ops.SupportsEdgeResize() {
		return framed
	}
	return container.NewStack(framed, newWindowResizeOverlay(ops))
}
