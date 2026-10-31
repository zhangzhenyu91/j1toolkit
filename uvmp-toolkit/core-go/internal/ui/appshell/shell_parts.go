package appshell

import (
	"fmt"
	"image/color"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/driver/desktop"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
)

// 壳层尺寸 token（政企蓝白形态：左导航轨 + 页头 + 限宽内容区）。
const (
	sideBarWidth      float32 = 200 // 左侧导航轨宽度（图标+文字，非开发者可读）
	contentMaxW       float32 = 1080
	contentMaximizedW float32 = 1440
	navLabelSize      float32 = 14
	navCaptionSize    float32 = 12
	navIconSize       float32 = 12
	navItemHeight     float32 = 44
	navRadius         float32 = 8
	navOuterX         float32 = 12
	navInnerX         float32 = 12
	navIconGap        float32 = 8
	navItemGap        float32 = 4
	navGroupGap       float32 = 12
	headerTitleSz     float32 = 19
	headerSubtitle    float32 = 12.5
)

// fixedWidthLayout 固定宽度布局（侧栏）、高度撑满可用空间。
type fixedWidthLayout struct {
	width float32
}

func (l fixedWidthLayout) MinSize(objs []fyne.CanvasObject) fyne.Size {
	h := float32(0)
	if len(objs) > 0 {
		h = objs[0].MinSize().Height
	}
	return fyne.NewSize(l.width, h)
}

func (l fixedWidthLayout) Layout(objs []fyne.CanvasObject, size fyne.Size) {
	if len(objs) == 0 {
		return
	}
	objs[0].Move(fyne.NewPos(0, 0))
	objs[0].Resize(fyne.NewSize(l.width, size.Height))
}

// maxWidthLayout 限宽并水平居中（桌面宽屏留白，窄窗自动占满）。
type maxWidthLayout struct {
	max float32
}

func (l maxWidthLayout) MinSize(objs []fyne.CanvasObject) fyne.Size {
	if len(objs) == 0 {
		return fyne.Size{}
	}
	return objs[0].MinSize()
}

func (l maxWidthLayout) Layout(objs []fyne.CanvasObject, size fyne.Size) {
	if len(objs) == 0 {
		return
	}
	w := size.Width
	if l.max > 0 && w > l.max {
		w = l.max
	}
	x := (size.Width - w) / 2
	objs[0].Move(fyne.NewPos(x, 0))
	objs[0].Resize(fyne.NewSize(w, size.Height))
}

// navEntry 左侧导航项：线性图标 + 文字；激活态浅蓝底 + 品牌蓝 + 左侧 3px 标记。
type navEntry struct {
	widget.BaseWidget

	title    string
	iconName string
	active   bool
	hover    bool
	focused  bool
	expanded bool
	onTap    func()

	bg     *canvas.Rectangle
	marker *canvas.Rectangle
	icon   *canvas.Image
	label  *canvas.Text
	arrow  *canvas.Image
}

// newNavEntry 构造导航项（点击切页；标题与图标区域均可点）。
func newNavEntry(title, iconName string, onTap func()) *navEntry {
	n := &navEntry{title: title, iconName: iconName, onTap: onTap}
	n.ExtendBaseWidget(n)
	n.bg = canvas.NewRectangle(color.Transparent)
	n.bg.CornerRadius = navRadius
	n.marker = canvas.NewRectangle(color.Transparent)
	n.marker.CornerRadius = 2
	n.icon = canvas.NewImageFromResource(lineIcon(iconName, TextSecondary))
	n.icon.FillMode = canvas.ImageFillContain
	n.icon.ScaleMode = canvas.ImageScaleSmooth
	n.icon.SetMinSize(fyne.NewSize(navIconSize, navIconSize))
	n.label = canvas.NewText(title, TextSecondary)
	n.label.TextSize = navLabelSize
	n.applyStyle()
	return n
}

var _ fyne.Tappable = (*navEntry)(nil)
var _ fyne.Focusable = (*navEntry)(nil)
var _ fyne.Accessible = (*navEntry)(nil)
var _ desktop.Hoverable = (*navEntry)(nil)

// SetActive 切换激活态（切页时由 Shell 调用）。
func (n *navEntry) SetActive(active bool) {
	if n.active == active {
		return
	}
	n.active = active
	n.applyStyle()
}

// Tapped 点击切页。
func (n *navEntry) Tapped(*fyne.PointEvent) {
	if c := fyne.CurrentApp().Driver().CanvasForObject(n); c != nil {
		c.Focus(n)
	}
	animateControlTap(n)
	if n.onTap != nil {
		n.onTap()
	}
}

func (n *navEntry) AccessibilityLabel() string { return n.title }
func (n *navEntry) AccessibilityRole() fyne.AccessibleRole {
	return fyne.AccessibleRoleButton
}
func (n *navEntry) Cursor() desktop.Cursor { return desktop.PointerCursor }
func (n *navEntry) TypedRune(rune)         {}
func (n *navEntry) TypedKey(event *fyne.KeyEvent) {
	if event.Name == fyne.KeySpace || event.Name == fyne.KeyReturn {
		n.Tapped(nil)
	}
}
func (n *navEntry) FocusGained() {
	n.focused = true
	n.applyStyle()
}
func (n *navEntry) FocusLost() {
	n.focused = false
	n.applyStyle()
}

// MouseIn 悬停高亮（仅未激活项，避免抢激活态视觉）。
func (n *navEntry) MouseIn(*desktop.MouseEvent) {
	if n.hover {
		return
	}
	n.hover = true
	n.applyStyle()
}

// MouseMoved 悬停移动无需处理（接口要求）。
func (n *navEntry) MouseMoved(*desktop.MouseEvent) {}

// MouseOut 取消悬停高亮。
func (n *navEntry) MouseOut() {
	if !n.hover {
		return
	}
	n.hover = false
	n.applyStyle()
}

func (n *navEntry) applyStyle() {
	switch {
	case n.active:
		n.bg.FillColor = LightBlueBG
		n.marker.FillColor = BrandBlue
		n.marker.Show()
		n.label.Color = BrandBlue
		n.label.TextStyle = fyne.TextStyle{Bold: true}
		n.icon.Resource = lineIcon(n.iconName, BrandBlue)
	case n.hover:
		n.bg.FillColor = BlockBG
		n.marker.FillColor = color.Transparent
		n.marker.Hide()
		n.label.Color = TextPrimary
		n.label.TextStyle = fyne.TextStyle{}
		n.icon.Resource = lineIcon(n.iconName, TextPrimary)
	default:
		n.bg.FillColor = color.Transparent
		n.marker.FillColor = color.Transparent
		n.marker.Hide()
		n.label.Color = TextSecondary
		n.label.TextStyle = fyne.TextStyle{}
		n.icon.Resource = lineIcon(n.iconName, TextSecondary)
	}
	if n.focused {
		n.bg.StrokeColor = BrandBlue
		n.bg.StrokeWidth = 1.5
	} else {
		n.bg.StrokeColor = color.Transparent
		n.bg.StrokeWidth = 0
	}
	if n.arrow != nil {
		if n.expanded {
			n.arrow.Resource = theme.MenuDropDownIcon()
		} else {
			n.arrow.Resource = theme.MenuExpandIcon()
		}
		n.arrow.Refresh()
	}
	n.bg.Refresh()
	n.marker.Refresh()
	n.label.Refresh()
	n.icon.Refresh()
}

// CreateRenderer 导航项渲染器。
func (n *navEntry) CreateRenderer() fyne.WidgetRenderer {
	return &navEntryRenderer{n: n}
}

type navEntryRenderer struct {
	n *navEntry
}

func (r *navEntryRenderer) Layout(size fyne.Size) {
	n := r.n
	n.bg.Move(fyne.NewPos(0, 0))
	n.bg.Resize(size)

	iconX := navInnerX
	iconY := (size.Height - navIconSize) / 2
	n.marker.Move(fyne.NewPos(0, (size.Height-18)/2))
	n.marker.Resize(fyne.NewSize(3, 18))
	n.icon.Move(fyne.NewPos(iconX, iconY))
	n.icon.Resize(fyne.NewSize(navIconSize, navIconSize))

	labelSize := n.label.MinSize()
	labelX := iconX + navIconSize + navIconGap
	labelRight := size.Width - navInnerX
	if n.arrow != nil {
		arrowX := size.Width - navInnerX - navIconSize
		n.arrow.Move(fyne.NewPos(arrowX, (size.Height-navIconSize)/2))
		n.arrow.Resize(fyne.NewSize(navIconSize, navIconSize))
		labelRight = arrowX - navIconGap
	}
	labelW := labelRight - labelX
	if labelW < 0 {
		labelW = 0
	}
	n.label.Move(fyne.NewPos(labelX, (size.Height-labelSize.Height)/2))
	n.label.Resize(fyne.NewSize(labelW, labelSize.Height))
}

func (r *navEntryRenderer) MinSize() fyne.Size {
	n := r.n
	w := navInnerX*2 + navIconSize + navIconGap + n.label.MinSize().Width
	if n.arrow != nil {
		w += navIconSize + navIconGap
	}
	return fyne.NewSize(w, navItemHeight)
}

func (r *navEntryRenderer) Refresh() { r.n.applyStyle() }

func (r *navEntryRenderer) Destroy() {}

func (r *navEntryRenderer) Objects() []fyne.CanvasObject {
	objects := []fyne.CanvasObject{r.n.bg, r.n.marker, r.n.icon, r.n.label}
	if r.n.arrow != nil {
		objects = append(objects, r.n.arrow)
	}
	return objects
}

// pageHeader 页头：标题 + 一句话说明 + 收藏与运行中任务状态，保持单一工具工作区。
type pageHeader struct {
	Root       fyne.CanvasObject
	title      *canvas.Text
	subtitle   *canvas.Text
	favorite   *appButton
	status     *widget.Label
	statusBG   *canvas.Rectangle
	statusDot  *canvas.Circle
	statusChip fyne.CanvasObject
}

// newPageHeader 构造页头（底部发丝线与内容区分隔）。
func newPageHeader() *pageHeader {
	h := &pageHeader{}
	h.title = canvas.NewText("", TextPrimary)
	h.title.TextSize = headerTitleSz
	h.title.TextStyle = fyne.TextStyle{Bold: true}
	h.subtitle = canvas.NewText("", TextSecondary)
	h.subtitle.TextSize = headerSubtitle

	h.favorite = newAppButton("收藏", "star", widget.LowImportance, nil)
	h.favorite.Hide()

	h.statusDot = canvas.NewCircle(WarningOrange)
	h.statusDot.Hide()
	h.status = widget.NewLabel("")
	h.status.Importance = widget.MediumImportance
	h.status.SizeName = theme.SizeNameCaptionText
	h.status.Hide()

	h.statusBG = canvas.NewRectangle(LightBlueBG)
	h.statusBG.CornerRadius = 999
	h.statusBG.StrokeColor = Border
	h.statusBG.StrokeWidth = 1
	h.statusBG.Hide()
	dotBox := container.NewGridWrap(fyne.NewSize(7, 22), container.NewCenter(h.statusDot))
	chip := container.NewStack(h.statusBG, padAll(4, 4, 11, 13, container.NewHBox(dotBox, h.status)))
	chip.Hide()

	left := container.NewVBox(h.title, h.subtitle)
	actions := container.NewHBox(h.favorite, chip)
	row := container.NewBorder(nil, nil, nil, actions, left)
	h.Root = container.NewVBox(
		padAll(15, 12, 20, 20, row),
		widget.NewSeparator(),
	)
	h.statusChip = chip
	return h
}

// SetFavorite 只在业务工具页显示收藏入口，功能图标与品牌 Logo 严格分离。
func (h *pageHeader) SetFavorite(pageID string, favorite bool) {
	if !shellIsBusinessTool(pageID) {
		h.favorite.Hide()
		return
	}
	h.favorite.Show()
	if favorite {
		h.favorite.SetAction("已收藏", "star")
		h.favorite.SetIcon(lineIcon("star", BrandBlue))
		return
	}
	h.favorite.SetAction("收藏", "star")
}

// Set 更新标题与说明（切页时由 Shell 调用）。
func (h *pageHeader) Set(title, subtitle string) {
	h.title.Text = title
	h.title.Refresh()
	h.subtitle.Text = subtitle
	h.subtitle.Refresh()
}

// SetRunning 显示/隐藏「N 个任务执行中」提示。
func (h *pageHeader) SetRunning(n int) {
	if n > 0 {
		h.status.SetText(fmt.Sprintf("%d 个任务执行中", n))
		h.status.Show()
		h.statusBG.Show()
		h.statusDot.Show()
		h.statusChip.Show()
		h.statusChip.Refresh()
		return
	}
	h.status.Hide()
	h.statusBG.Hide()
	h.statusDot.Hide()
	h.statusChip.Hide()
}

// stateLabel 状态行（圆点语义以「颜色 + 文字」呈现，避免依赖特殊字形）。
func stateLabel(text string, importance widget.Importance) *widget.Label {
	l := widget.NewLabel(text)
	l.Importance = importance
	l.Wrapping = fyne.TextWrapWord
	return l
}

// setStateLabel 更新状态行文案与语义色。
func setStateLabel(l *widget.Label, text string, importance widget.Importance) {
	l.Importance = importance
	l.SetText(text)
	l.Refresh()
}
