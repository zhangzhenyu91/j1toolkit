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
	sideBarWidth   float32 = 200 // 左侧导航轨宽度（图标+文字，非开发者可读）
	contentMaxW    float32 = 1120
	navItemHeight  float32 = 38
	navIconSize    float32 = 20
	navPadX        float32 = 14
	navIconGap     float32 = 10
	headerTitleSz  float32 = 19
	headerSubtitle float32 = 12.5
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
	onTap    func()

	bg     *canvas.Rectangle
	marker *canvas.Rectangle
	icon   *canvas.Image
	label  *canvas.Text
}

// newNavEntry 构造导航项（点击切页；标题与图标区域均可点）。
func newNavEntry(title, iconName string, onTap func()) *navEntry {
	n := &navEntry{title: title, iconName: iconName, onTap: onTap}
	n.ExtendBaseWidget(n)
	n.bg = canvas.NewRectangle(color.Transparent)
	n.bg.CornerRadius = 8
	n.marker = canvas.NewRectangle(color.Transparent)
	n.marker.CornerRadius = 2
	n.icon = canvas.NewImageFromResource(lineIcon(iconName, TextSecondary))
	n.icon.FillMode = canvas.ImageFillContain
	n.icon.ScaleMode = canvas.ImageScaleSmooth
	n.icon.SetMinSize(fyne.NewSize(navIconSize, navIconSize))
	n.label = canvas.NewText(title, TextSecondary)
	n.label.TextSize = 13.5
	n.applyStyle()
	return n
}

var _ fyne.Tappable = (*navEntry)(nil)
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
	if n.onTap != nil {
		n.onTap()
	}
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

	iconX := navPadX
	iconY := (size.Height - navIconSize) / 2
	n.marker.Move(fyne.NewPos(0, (size.Height-18)/2))
	n.marker.Resize(fyne.NewSize(3, 18))
	n.icon.Move(fyne.NewPos(iconX, iconY))
	n.icon.Resize(fyne.NewSize(navIconSize, navIconSize))

	labelSize := n.label.MinSize()
	labelX := iconX + navIconSize + navIconGap
	labelW := size.Width - labelX - navPadX
	if labelW < 0 {
		labelW = 0
	}
	n.label.Move(fyne.NewPos(labelX, (size.Height-labelSize.Height)/2))
	n.label.Resize(fyne.NewSize(labelW, labelSize.Height))
}

func (r *navEntryRenderer) MinSize() fyne.Size {
	n := r.n
	w := navPadX*2 + navIconSize + navIconGap + n.label.MinSize().Width
	return fyne.NewSize(w, navItemHeight)
}

func (r *navEntryRenderer) Refresh() { r.n.applyStyle() }

func (r *navEntryRenderer) Destroy() {}

func (r *navEntryRenderer) Objects() []fyne.CanvasObject {
	return []fyne.CanvasObject{r.n.bg, r.n.marker, r.n.icon, r.n.label}
}

// pageHeader 页头：标题 + 一句话说明 + 右侧运行中任务状态胶囊。
type pageHeader struct {
	Root       fyne.CanvasObject
	title      *canvas.Text
	subtitle   *canvas.Text
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
	row := container.NewBorder(nil, nil, nil, chip, left)
	h.Root = container.NewVBox(
		padAll(15, 12, 20, 20, row),
		widget.NewSeparator(),
	)
	h.statusChip = chip
	return h
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

// brandBlock 品牌区：真实产品图标 + 产品名 + 一句话定位。
func brandBlock() fyne.CanvasObject {
	icon := canvas.NewImageFromResource(ShellIcon())
	icon.FillMode = canvas.ImageFillContain
	icon.ScaleMode = canvas.ImageScaleSmooth
	iconBox := container.NewGridWrap(fyne.NewSize(34, 34), icon)

	name := canvas.NewText("Shade 壹匣", TextPrimary)
	name.TextSize = 15
	name.TextStyle = fyne.TextStyle{Bold: true}
	tag := canvas.NewText("内网工具箱", TextTertiary)
	tag.TextSize = 11

	text := container.NewVBox(name, tag)
	row := container.NewBorder(nil, nil, iconBox, nil, padAll(0, 0, 10, 0, text))
	return container.NewVBox(
		padAll(18, 15, 16, 12, row),
		widget.NewSeparator(),
	)
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
