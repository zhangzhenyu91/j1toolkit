package appshell

import (
	"image/color"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/driver/desktop"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
)

// collapsibleSection 可折叠分组卡片（设置页分组、长说明共用）：
// 标题行与右侧箭头均可点击展开/收起，收起时内容不占高度。
// 与其它 Fyne 控件一致，SetExpanded/Toggle 须在 UI 线程调用（内部点击回调已在 UI 线程）。
type collapsibleSection struct {
	// Root 卡片根对象（白卡 + 标题行 + 内容），由调用方直接放入页面。
	Root fyne.CanvasObject

	title    string
	content  fyne.CanvasObject
	expanded bool
	header   *sectionHeader
	body     *fyne.Container
	box      *fyne.Container
}

// newCollapsibleSection 构造可折叠分组卡片；initiallyExpanded=false 时默认收起。
func newCollapsibleSection(title string, content fyne.CanvasObject, initiallyExpanded bool) *collapsibleSection {
	if content == nil {
		content = container.NewVBox()
	}
	s := &collapsibleSection{title: title, content: content, expanded: initiallyExpanded}
	s.header = newSectionHeader(title, s.Toggle)
	// 内容内边距并入 body，收起时一并隐藏（不残留空白）。
	s.body = container.NewVBox(padAll(0, 12, 14, 14, content))
	if !initiallyExpanded {
		s.body.Hide()
	}
	s.header.setExpanded(initiallyExpanded)

	bg := canvas.NewRectangle(CardBG)
	bg.CornerRadius = 12
	bg.StrokeColor = Border
	bg.StrokeWidth = 1
	s.box = container.NewStack(bg, container.NewPadded(container.NewVBox(s.header, s.body)))
	s.Root = s.box
	return s
}

// SetExpanded 展开/收起分组。
func (s *collapsibleSection) SetExpanded(expanded bool) {
	if s == nil || s.expanded == expanded {
		return
	}
	s.expanded = expanded
	if expanded {
		s.body.Show()
	} else {
		s.body.Hide()
	}
	s.header.setExpanded(expanded)
	s.box.Refresh()
	// 展开/收起改变卡片高度：刷新外层滚动体，避免内容溢出或留白。
	reflowInScroll(s.box)
}

// Expanded 当前是否展开。
func (s *collapsibleSection) Expanded() bool { return s != nil && s.expanded }

// Toggle 切换展开状态（点击标题或箭头触发）。
func (s *collapsibleSection) Toggle() { s.SetExpanded(!s.expanded) }

// sectionHeader 折叠分组标题行：标题 + 箭头，整行可点，hover 浅灰底提示可点。
type sectionHeader struct {
	widget.BaseWidget

	title    string
	expanded bool
	hover    bool
	onTap    func()

	bg    *canvas.Rectangle
	label *canvas.Text
	arrow *canvas.Image
}

const sectionHeaderHeight float32 = 44

// newSectionHeader 构造折叠分组标题行。
func newSectionHeader(title string, onTap func()) *sectionHeader {
	h := &sectionHeader{title: title, onTap: onTap}
	h.ExtendBaseWidget(h)
	h.bg = canvas.NewRectangle(color.Transparent)
	h.bg.CornerRadius = 8
	h.label = canvas.NewText(title, TextPrimary)
	h.label.TextSize = 15
	h.label.TextStyle = fyne.TextStyle{Bold: true}
	h.arrow = canvas.NewImageFromResource(theme.MenuExpandIcon())
	h.arrow.FillMode = canvas.ImageFillContain
	h.arrow.ScaleMode = canvas.ImageScaleSmooth
	h.arrow.SetMinSize(fyne.NewSize(16, 16))
	h.applyStyle()
	return h
}

var _ fyne.Tappable = (*sectionHeader)(nil)
var _ desktop.Hoverable = (*sectionHeader)(nil)

// Tapped 点击标题行切换展开状态。
func (h *sectionHeader) Tapped(*fyne.PointEvent) {
	if h.onTap != nil {
		h.onTap()
	}
}

// MouseIn 悬停浅灰底。
func (h *sectionHeader) MouseIn(*desktop.MouseEvent) {
	if h.hover {
		return
	}
	h.hover = true
	h.applyStyle()
}

// MouseMoved 悬停移动无需处理（接口要求）。
func (h *sectionHeader) MouseMoved(*desktop.MouseEvent) {}

// MouseOut 取消悬停底色。
func (h *sectionHeader) MouseOut() {
	if !h.hover {
		return
	}
	h.hover = false
	h.applyStyle()
}

func (h *sectionHeader) setExpanded(expanded bool) {
	h.expanded = expanded
	h.applyStyle()
}

func (h *sectionHeader) applyStyle() {
	if h.hover {
		h.bg.FillColor = BlockBG
	} else {
		h.bg.FillColor = color.Transparent
	}
	if h.expanded {
		h.arrow.Resource = theme.MenuDropDownIcon()
	} else {
		h.arrow.Resource = theme.MenuExpandIcon()
	}
	h.bg.Refresh()
	h.label.Refresh()
	h.arrow.Refresh()
}

// CreateRenderer 标题行渲染器。
func (h *sectionHeader) CreateRenderer() fyne.WidgetRenderer {
	return &sectionHeaderRenderer{h: h}
}

type sectionHeaderRenderer struct {
	h *sectionHeader
}

func (r *sectionHeaderRenderer) Layout(size fyne.Size) {
	h := r.h
	h.bg.Move(fyne.NewPos(0, 0))
	h.bg.Resize(size)

	sizeArrow := float32(16)
	arrowX := size.Width - sizeArrow - 6
	h.arrow.Move(fyne.NewPos(arrowX, (size.Height-sizeArrow)/2))
	h.arrow.Resize(fyne.NewSize(sizeArrow, sizeArrow))

	labelSize := h.label.MinSize()
	h.label.Move(fyne.NewPos(6, (size.Height-labelSize.Height)/2))
	labelW := arrowX - 12
	if labelW < 0 {
		labelW = 0
	}
	h.label.Resize(fyne.NewSize(labelW, labelSize.Height))
}

func (r *sectionHeaderRenderer) MinSize() fyne.Size {
	h := r.h
	return fyne.NewSize(h.label.MinSize().Width+40, sectionHeaderHeight)
}

func (r *sectionHeaderRenderer) Refresh() { r.h.applyStyle() }

func (r *sectionHeaderRenderer) Destroy() {}

func (r *sectionHeaderRenderer) Objects() []fyne.CanvasObject {
	return []fyne.CanvasObject{r.h.bg, r.h.label, r.h.arrow}
}
