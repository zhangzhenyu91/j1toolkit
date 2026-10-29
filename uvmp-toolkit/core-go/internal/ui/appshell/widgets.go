package appshell

import (
	"image/color"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/widget"
)

// 白卡：12px 圆角 + 发丝线边框（数据卡形态，禁止大面积渐变/浮雕）。
func newCard(title string, objs ...fyne.CanvasObject) fyne.CanvasObject {
	body := container.NewVBox()
	if title != "" {
		body.Objects = append(body.Objects, secTitle(title))
		body.Objects = append(body.Objects, widget.NewSeparator())
	}
	for _, o := range objs {
		body.Objects = append(body.Objects, o)
	}
	bg := canvas.NewRectangle(CardBG)
	bg.CornerRadius = 12
	bg.StrokeColor = Border
	bg.StrokeWidth = 1
	return container.NewStack(bg, container.NewPadded(container.NewPadded(body)))
}

// secTitle 板块标题（15–16px/600，不加几何记号）。
func secTitle(text string) *widget.Label {
	l := widget.NewLabel(text)
	l.TextStyle = fyne.TextStyle{Bold: true}
	l.Importance = widget.HighImportance
	return l
}

// muted 辅助文字（#86909C）。
func muted(text string) *widget.Label {
	l := widget.NewLabel(text)
	l.Importance = widget.LowImportance
	l.Wrapping = fyne.TextWrapWord
	return l
}

// bodyText 正文标签（可换行）。
func bodyText(text string) *widget.Label {
	l := widget.NewLabel(text)
	l.Wrapping = fyne.TextWrapWord
	return l
}

// kvGrid 键值网格（两列，key 为辅助文字）。
func kvGrid(pairs ...[2]string) *fyne.Container {
	left := container.NewVBox()
	right := container.NewVBox()
	for _, p := range pairs {
		left.Objects = append(left.Objects, muted(p[0]))
		v := widget.NewLabel(p[1])
		v.Wrapping = fyne.TextWrapWord
		right.Objects = append(right.Objects, v)
	}
	return container.NewGridWithColumns(2, left, right)
}

// hrow 水平排布。
func hrow(objs ...fyne.CanvasObject) *fyne.Container {
	return container.NewHBox(objs...)
}

// hrowGrow 水平排布：中间对象占满剩余宽度，trailing 靠右。
func hrowGrow(mid fyne.CanvasObject, trailing ...fyne.CanvasObject) *fyne.Container {
	return container.NewBorder(nil, nil, nil, container.NewHBox(trailing...), mid)
}

// statusBar 顶部横幅（提示/警示/错误，直角浅底 + 左色条一族）。
// kind: info / warning / error。
func statusBar(kind, text string) fyne.CanvasObject {
	var bg, bar color.NRGBA
	switch kind {
	case "error":
		bg, bar = DangerRedBG, DangerRed
	case "warning":
		bg, bar = WarningOrangeBG, WarningOrange
	default:
		bg, bar = LightBlueBG, BrandBlue
	}
	rect := canvas.NewRectangle(bg)
	label := widget.NewLabel(text)
	label.Wrapping = fyne.TextWrapWord
	strip := canvas.NewRectangle(bar)
	strip.SetMinSize(fyne.NewSize(4, 0))
	return container.NewStack(rect,
		container.NewBorder(nil, nil, container.NewPadded(strip), nil, container.NewPadded(label)))
}

// minBox 为内容强制最小尺寸的容器（窗口最小尺寸由内容 MinSize 派生）。
type minBox struct {
	widget.BaseWidget
	content fyne.CanvasObject
	min     fyne.Size
}

// newMinBox 包裹内容并给定最小尺寸。
func newMinBox(min fyne.Size, content fyne.CanvasObject) *minBox {
	b := &minBox{content: content, min: min}
	b.ExtendBaseWidget(b)
	return b
}

func (b *minBox) CreateRenderer() fyne.WidgetRenderer {
	return widget.NewSimpleRenderer(b.content)
}

// MinSize 取内容最小尺寸与给定下限的较大者。
func (b *minBox) MinSize() fyne.Size {
	inner := b.content.MinSize()
	w, h := b.min.Width, b.min.Height
	if inner.Width > w {
		w = inner.Width
	}
	if inner.Height > h {
		h = inner.Height
	}
	return fyne.NewSize(w, h)
}
