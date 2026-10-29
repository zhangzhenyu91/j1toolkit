package appshell

import (
	"fmt"
	"image/color"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/driver/desktop"
	"fyne.io/fyne/v2/widget"
)

// enterpriseCheck 复用 widget.Check 的 Checked/Text/OnChanged 数据字段，
// 仅替换绘制：20px 方框、清晰勾选、统一文字基线、完整焦点与禁用态。
type enterpriseCheck struct {
	*widget.Check

	box     *canvas.Rectangle
	tick    *canvas.Image
	label   *canvas.Text
	focus   *canvas.Rectangle
	hovered bool
	focused bool
}

func newEnterpriseCheck(label string, changed func(bool)) *enterpriseCheck {
	// 不走 NewCheck：它已把 impl 绑定到默认渲染器；先初始化字段，再绑定本控件。
	c := &enterpriseCheck{Check: &widget.Check{Text: label, OnChanged: changed}}
	c.Check.ExtendBaseWidget(c)
	c.box = canvas.NewRectangle(CardBG)
	c.box.CornerRadius = 4
	c.box.StrokeColor = Border
	c.box.StrokeWidth = 1.2
	c.tick = canvas.NewImageFromResource(checkMarkIcon())
	c.tick.FillMode = canvas.ImageFillContain
	c.tick.ScaleMode = canvas.ImageScaleSmooth
	c.tick.Hide()
	c.label = canvas.NewText(label, TextPrimary)
	c.label.TextSize = 14
	c.focus = canvas.NewRectangle(color.Transparent)
	c.focus.CornerRadius = 6
	return c
}

func checkMarkIcon() fyne.Resource {
	svg := fmt.Sprintf(lineIconTemplate, "#FFFFFF", `<path d="m5 12.5 4.2 4.2L19 7"/>`)
	return fyne.NewStaticResource("enterprise-check.svg", []byte(svg))
}

func (c *enterpriseCheck) SetChecked(checked bool) {
	c.Check.Checked = checked
	c.Refresh()
	if c.Check.OnChanged != nil {
		c.Check.OnChanged(checked)
	}
}

func (c *enterpriseCheck) SetText(text string) {
	c.Check.Text = text
	c.label.Text = text
	c.Refresh()
}

func (c *enterpriseCheck) Disable() {
	c.Check.Disable()
	c.Refresh()
}

func (c *enterpriseCheck) Enable() {
	c.Check.Enable()
	c.Refresh()
}

func (c *enterpriseCheck) Tapped(*fyne.PointEvent) {
	if c.Disabled() {
		return
	}
	if cv := fyne.CurrentApp().Driver().CanvasForObject(c); cv != nil {
		cv.Focus(c)
	}
	animateControlTap(c)
	c.SetChecked(!c.Checked)
}

func (c *enterpriseCheck) TypedRune(key rune) {
	if key == ' ' && !c.Disabled() {
		animateControlTap(c)
		c.SetChecked(!c.Checked)
	}
}
func (c *enterpriseCheck) TypedKey(*fyne.KeyEvent) {}
func (c *enterpriseCheck) FocusGained() {
	c.focused = true
	c.Refresh()
}
func (c *enterpriseCheck) FocusLost() {
	c.focused = false
	c.Refresh()
}
func (c *enterpriseCheck) MouseIn(*desktop.MouseEvent) {
	c.hovered = true
	c.Refresh()
}
func (c *enterpriseCheck) MouseMoved(*desktop.MouseEvent) {}
func (c *enterpriseCheck) MouseOut() {
	c.hovered = false
	c.Refresh()
}
func (c *enterpriseCheck) Cursor() desktop.Cursor { return desktop.DefaultCursor }
func (c *enterpriseCheck) AccessibilityLabel() string {
	if c.Text == "" {
		return "启用"
	}
	return c.Text
}
func (c *enterpriseCheck) AccessibilityRole() fyne.AccessibleRole {
	return fyne.AccessibleRoleButton
}

func (c *enterpriseCheck) applyStyle() {
	c.label.Color = TextPrimary
	c.box.FillColor = CardBG
	c.box.StrokeColor = Border
	if c.Checked {
		c.box.FillColor = BrandBlue
		c.box.StrokeColor = BrandBlue
		c.tick.Show()
	} else {
		c.tick.Hide()
	}
	c.focus.FillColor = color.Transparent
	if c.hovered {
		c.focus.FillColor = LightBlueBG
	}
	c.focus.StrokeColor = color.Transparent
	c.focus.StrokeWidth = 0
	if c.focused {
		c.focus.StrokeColor = BrandBlue
		c.focus.StrokeWidth = 1.5
	}
	if c.Disabled() {
		c.label.Color = TextTertiary
		c.box.FillColor = BlockBG
		c.box.StrokeColor = Border
		c.focus.FillColor = color.Transparent
		c.focus.StrokeColor = color.Transparent
		c.focus.StrokeWidth = 0
	}
	c.box.Refresh()
	c.tick.Refresh()
	c.label.Refresh()
	c.focus.Refresh()
}

func (c *enterpriseCheck) CreateRenderer() fyne.WidgetRenderer {
	return &enterpriseCheckRenderer{c: c}
}

var _ fyne.Tappable = (*enterpriseCheck)(nil)
var _ fyne.Focusable = (*enterpriseCheck)(nil)
var _ fyne.Accessible = (*enterpriseCheck)(nil)
var _ desktop.Hoverable = (*enterpriseCheck)(nil)

type enterpriseCheckRenderer struct{ c *enterpriseCheck }

func (r *enterpriseCheckRenderer) Layout(size fyne.Size) {
	r.c.focus.Move(fyne.NewPos(0, (size.Height-26)/2))
	r.c.focus.Resize(fyne.NewSquareSize(26))
	r.c.box.Move(fyne.NewPos(3, (size.Height-20)/2))
	r.c.box.Resize(fyne.NewSquareSize(20))
	r.c.tick.Move(fyne.NewPos(5, (size.Height-16)/2))
	r.c.tick.Resize(fyne.NewSquareSize(16))
	labelHeight := r.c.label.MinSize().Height
	r.c.label.Move(fyne.NewPos(34, (size.Height-labelHeight)/2))
	r.c.label.Resize(fyne.NewSize(fyne.Max(0, size.Width-34), labelHeight))
}
func (r *enterpriseCheckRenderer) MinSize() fyne.Size {
	m := r.c.label.MinSize()
	return fyne.NewSize(m.Width+40, fyne.Max(28, m.Height))
}
func (r *enterpriseCheckRenderer) Refresh() { r.c.applyStyle() }
func (r *enterpriseCheckRenderer) Destroy() {}
func (r *enterpriseCheckRenderer) Objects() []fyne.CanvasObject {
	return []fyne.CanvasObject{r.c.focus, r.c.box, r.c.tick, r.c.label}
}
