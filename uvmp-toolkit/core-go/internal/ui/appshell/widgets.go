package appshell

import (
	"image/color"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/layout"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
)

// padAll 自定义四边内边距（padTop/padBottom/padLeft/padRight；Fyne 参数顺序如此）。
func padAll(padTop, padBottom, padLeft, padRight float32, obj fyne.CanvasObject) fyne.CanvasObject {
	return container.New(layout.NewCustomPaddedLayout(padTop, padBottom, padLeft, padRight), obj)
}

// fixed 固定宽高包裹（表单中的窄输入/标签列）。
func fixed(w, h float32, obj fyne.CanvasObject) fyne.CanvasObject {
	return container.NewGridWrap(fyne.NewSize(w, h), obj)
}

// formRow 表单行：左侧固定宽度标签（不换行），中间控件占满，右侧操作按钮。
func formRow(label string, field fyne.CanvasObject, trailing ...fyne.CanvasObject) fyne.CanvasObject {
	left := fixed(76, 34, formLabel(label))
	right := container.NewHBox(trailing...)
	return container.NewBorder(nil, nil, left, right, field)
}

// formLabel 表单左侧标签：不换行（窄列里中文标签必须单行，避免竖排误读）。
func formLabel(text string) *widget.Label {
	return inlineLabel(text, widget.LowImportance)
}

// inlineLabel 定宽列内的单行标签：不换行、超宽省略（配合 fixed 宽度使用）。
// 注意：省略标签的最小宽度为 0，不要直接放进 HBox（会被压成「…」）。
func inlineLabel(text string, importance widget.Importance) *widget.Label {
	l := hbLabel(text, importance)
	l.Truncation = fyne.TextTruncateEllipsis
	return l
}

// hbLabel 横向行内标签：不换行、按文字实际宽度占位（HBox 内使用）。
func hbLabel(text string, importance widget.Importance) *widget.Label {
	l := widget.NewLabel(text)
	l.Importance = importance
	l.Wrapping = fyne.TextWrapOff
	return l
}

// reflowInScroll 找到包含 obj 的最近滚动体并刷新它。
// 运行期给 VBox 增删子项、切换分组显隐、异步回填键值后，父级最小高度不会自动重算
// （Fyne 容器 Refresh 只按当前尺寸重排，不向父级冒泡），必须刷新滚动体让它按内容
// MinSize 重新给内容区定尺寸，否则新增内容会溢出卡片压住后续元素——已踩坑。
func reflowInScroll(obj fyne.CanvasObject) {
	app := fyne.CurrentApp()
	if obj == nil || app == nil || app.Driver() == nil {
		return
	}
	c := app.Driver().CanvasForObject(obj)
	if c == nil {
		return
	}
	if sc := scrollAncestor(c.Content(), obj); sc != nil {
		sc.Refresh()
	}
}

// scrollAncestor 在对象树中查找包含 target 的最近滚动体。
// 除标准容器/滚动体外，还需穿透壳层自绘包装（minBox）：窗口根对象是它。
func scrollAncestor(node, target fyne.CanvasObject) *container.Scroll {
	switch n := node.(type) {
	case *container.Scroll:
		if n.Content != nil && containsObject(n.Content, target) {
			return n
		}
	case *fyne.Container:
		for _, child := range n.Objects {
			if sc := scrollAncestor(child, target); sc != nil {
				return sc
			}
		}
	case *minBox:
		return scrollAncestor(n.content, target)
	case *homePane:
		return scrollAncestor(n.mainScroll, target)
	case *homeBottom:
		if sc := scrollAncestor(n.background, target); sc != nil {
			return sc
		}
		return scrollAncestor(n.recent, target)
	case *pageStage:
		return scrollAncestor(n.content, target)
	case *appTooltipScope:
		return scrollAncestor(n.content, target)
	}
	return nil
}

// containsObject 判断 target 是否位于 node 子树内（含自身）。
func containsObject(node, target fyne.CanvasObject) bool {
	if node == nil {
		return false
	}
	if node == target {
		return true
	}
	switch n := node.(type) {
	case *container.Scroll:
		return n.Content != nil && containsObject(n.Content, target)
	case *fyne.Container:
		for _, child := range n.Objects {
			if containsObject(child, target) {
				return true
			}
		}
	case *minBox:
		return containsObject(n.content, target)
	case *homePane:
		return containsObject(n.mainScroll, target)
	case *homeBottom:
		return containsObject(n.background, target) || containsObject(n.recent, target)
	case *pageStage:
		return containsObject(n.content, target)
	case *appTooltipScope:
		return containsObject(n.content, target)
	}
	return false
}

// 白卡：12px 圆角 + 发丝线边框（数据卡形态，禁止大面积渐变/浮雕）。
func newCard(title string, objs ...fyne.CanvasObject) fyne.CanvasObject {
	body := container.NewVBox()
	if title != "" {
		body.Objects = append(body.Objects, secTitle(title))
		body.Objects = append(body.Objects, spacer(2))
		body.Objects = append(body.Objects, widget.NewSeparator())
	}
	for i, o := range objs {
		if i == 0 && title != "" {
			body.Objects = append(body.Objects, spacer(8))
		}
		body.Objects = append(body.Objects, o)
	}
	return cardSurface(body)
}

const (
	cardRadius      float32 = 12
	cardStrokeWidth float32 = 1
	cardPadding     float32 = 14
	cardGap         float32 = 12
	columnGap       float32 = 16
	// pageTopGap 侧栏搜索框与页面内容区共用的顶部留白，避免顶部贴边。
	pageTopGap float32 = 12
)

// panelSurface 面板与卡片共用的统一 12px 圆角细边界表面；
// 左侧导航轨例外，使用 sidebarSurface（仅左下圆角）。
func panelSurface(content fyne.CanvasObject) *fyne.Container {
	bg := canvas.NewRectangle(CardBG)
	bg.CornerRadius = cardRadius
	bg.StrokeColor = Border
	bg.StrokeWidth = cardStrokeWidth
	return container.NewStack(bg, content)
}

// sidebarSurface 左侧导航轨专属表面：上左、上右、下右直角，仅左下保留 12px 圆角。
// CornerRadius 必须保持 0，否则独立角半径 0 的角会回退到统一圆角。
func sidebarSurface(content fyne.CanvasObject) *fyne.Container {
	bg := canvas.NewRectangle(CardBG)
	bg.CornerRadius = 0
	bg.BottomLeftCornerRadius = cardRadius
	bg.StrokeColor = Border
	bg.StrokeWidth = cardStrokeWidth
	return container.NewStack(bg, content)
}

// cardSurface 给任意内容套白卡底（统一 14px 内边距 + 12px 圆角 + 发丝线）。
func cardSurface(content fyne.CanvasObject) *fyne.Container {
	return panelSurface(padAll(cardPadding, cardPadding, cardPadding, cardPadding, content))
}

// secTitle 板块标题（15–16px/600，正文色；品牌蓝只留给操作与激活态）。
func secTitle(text string) *widget.Label {
	l := widget.NewLabel(text)
	l.TextStyle = fyne.TextStyle{Bold: true}
	l.Importance = widget.MediumImportance
	l.SizeName = theme.SizeNameSubHeadingText
	return l
}

// muted 辅助说明（TextSecondary、13px、按任意字符换行）。
// 用普通 Label 而非自绘 RichText：Label 的换行/MinSize 由 Fyne 统一处理，
// 长中文说明在 900 宽窗口下也能正常折行，不会撑宽卡片后被裁切。
func muted(text string) *widget.Label {
	l := widget.NewLabel(text)
	l.Importance = widget.LowImportance // 主题把禁用色映射到 TextSecondary
	l.SizeName = theme.SizeNameCaptionText
	l.Wrapping = fyne.TextWrapBreak
	return l
}

// bodyText 正文标签（可换行）。
func bodyText(text string) *widget.Label {
	l := widget.NewLabel(text)
	l.Wrapping = fyne.TextWrapWord
	return l
}

// kvGrid 键值列表（左侧固定标签列、右侧值列自动换行；比等宽两列更省横向空间）。
func kvGrid(pairs ...[2]string) *fyne.Container {
	v := container.NewVBox()
	for _, p := range pairs {
		val := widget.NewLabel(p[1])
		val.Wrapping = fyne.TextWrapWord
		v.Add(container.NewBorder(nil, nil, fixed(120, 28, formLabel(p[0])), nil, val))
	}
	return v
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

// statusLine 状态行：语义色圆点 + 正文（不再用整行亮色底/亮色字表达状态）。
// 自绘布局让圆点固定 7px 且与文字间距稳定（HBox+Center 会把 Circle 压成 1px）。
type statusLine struct {
	widget.BaseWidget

	Root  fyne.CanvasObject
	dot   *canvas.Circle
	label *widget.Label
}

// newStatusLine 构造状态行；size 为状态文字字号 token（hero 用正文、列表用小字）。
func newStatusLine(text string, imp widget.Importance, size fyne.ThemeSizeName) *statusLine {
	s := &statusLine{dot: canvas.NewCircle(Placeholder)}
	s.ExtendBaseWidget(s)
	s.label = widget.NewLabel(text)
	s.label.Importance = widget.MediumImportance
	if size != "" {
		s.label.SizeName = size
	}
	s.label.Wrapping = fyne.TextWrapWord
	s.Root = s
	s.Set(text, imp)
	return s
}

// Set 更新文案与圆点语义色。
func (s *statusLine) Set(text string, imp widget.Importance) {
	s.label.SetText(text)
	s.dot.FillColor = statusColor(imp)
	s.dot.Refresh()
	s.Refresh()
}

// CreateRenderer 状态行渲染器。
func (s *statusLine) CreateRenderer() fyne.WidgetRenderer { return &statusLineRenderer{s: s} }

type statusLineRenderer struct{ s *statusLine }

func (r *statusLineRenderer) Layout(size fyne.Size) {
	r.s.dot.Move(fyne.NewPos(0, (size.Height-7)/2))
	r.s.dot.Resize(fyne.NewSize(7, 7))
	r.s.label.Move(fyne.NewPos(12, 0))
	r.s.label.Resize(fyne.NewSize(size.Width-12, size.Height))
}

func (r *statusLineRenderer) MinSize() fyne.Size {
	m := r.s.label.MinSize()
	h := m.Height
	if h < 20 {
		h = 20
	}
	return fyne.NewSize(m.Width+12, h)
}

func (r *statusLineRenderer) Refresh() {
	r.s.dot.Refresh()
	r.s.label.Refresh()
}

func (r *statusLineRenderer) Destroy() {}

func (r *statusLineRenderer) Objects() []fyne.CanvasObject {
	return []fyne.CanvasObject{r.s.dot, r.s.label}
}

// statusColor 语义档位 → 圆点颜色。
func statusColor(imp widget.Importance) color.Color {
	switch imp {
	case widget.SuccessImportance:
		return SuccessGreen
	case widget.DangerImportance:
		return DangerRed
	case widget.WarningImportance:
		return WarningOrange
	case widget.MediumImportance:
		return BrandBlue
	}
	return TextTertiary
}

// tag 小标签胶囊（浅底 + 彩色文字，4px 圆角）。
func tag(text string, fg, bg color.Color) fyne.CanvasObject {
	t := canvas.NewText(text, fg)
	t.TextSize = 11
	t.TextStyle = fyne.TextStyle{Bold: true}
	rect := canvas.NewRectangle(bg)
	rect.CornerRadius = 4
	return container.NewStack(rect, padAll(3, 3, 7, 7, container.NewCenter(t)))
}

// statusDot 单独的状态圆点（最近结果行等）。
func statusDot(imp widget.Importance, size float32) fyne.CanvasObject {
	c := canvas.NewCircle(statusColor(imp))
	return container.NewGridWrap(fyne.NewSize(size, size), c)
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
