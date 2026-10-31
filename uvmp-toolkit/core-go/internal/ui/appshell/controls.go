package appshell

import (
	"image/color"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/driver/desktop"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
)

// appSelect 保留标准 Select 的渲染与数据行为，并替换无法扩展光标的私有菜单项弹层。
type appSelect struct {
	*widget.Select
	popUp *widget.PopUp
}

func newAppSelect(options []string, changed func(string)) *appSelect {
	s := &appSelect{Select: &widget.Select{Options: options, OnChanged: changed}}
	s.Select.ExtendBaseWidget(s)
	return s
}

func (s *appSelect) Cursor() desktop.Cursor {
	if s.Disabled() {
		return desktop.DefaultCursor
	}
	return desktop.PointerCursor
}

func (s *appSelect) Tapped(*fyne.PointEvent) {
	if s.Disabled() {
		return
	}
	if c := fyne.CurrentApp().Driver().CanvasForObject(s); c != nil {
		c.Focus(s)
	}
	animateControlTap(s)
	s.showPopUp()
}

func (s *appSelect) TypedKey(event *fyne.KeyEvent) {
	if s.Disabled() {
		return
	}
	switch event.Name {
	case fyne.KeySpace, fyne.KeyUp, fyne.KeyDown:
		s.showPopUp()
	default:
		s.Select.TypedKey(event)
	}
}

func (s *appSelect) Hide() {
	s.hidePopUp()
	s.Select.Hide()
}

func (s *appSelect) Move(pos fyne.Position) {
	s.Select.Move(pos)
	s.layoutPopUp()
}

func (s *appSelect) Resize(size fyne.Size) {
	s.Select.Resize(size)
	s.layoutPopUp()
}

func (s *appSelect) showPopUp() {
	if s.Disabled() {
		return
	}
	s.hidePopUp()
	c := fyne.CurrentApp().Driver().CanvasForObject(s)
	if c == nil {
		return
	}
	menu := newAppSelectMenu(s.Options, s.SelectedIndex(), s.Alignment, func(text string) {
		s.hidePopUp()
		s.SetSelected(text)
	}, s.hidePopUp)
	s.popUp = widget.NewPopUp(menu, c)
	size, pos := s.popUpLayout(c)
	s.popUp.Resize(size)
	s.popUp.ShowAtPosition(pos)
	c.Focus(menu)
}

func (s *appSelect) hidePopUp() {
	if s.popUp == nil {
		return
	}
	popUp := s.popUp
	s.popUp = nil
	popUp.Hide()
}

func (s *appSelect) layoutPopUp() {
	if s.popUp == nil || !s.popUp.Visible() {
		return
	}
	c := fyne.CurrentApp().Driver().CanvasForObject(s)
	if c == nil {
		return
	}
	size, pos := s.popUpLayout(c)
	s.popUp.Resize(size)
	s.popUp.Move(pos)
}

func (s *appSelect) popUpLayout(c fyne.Canvas) (fyne.Size, fyne.Position) {
	_, areaSize := c.InteractiveArea()
	desiredHeight := s.popUp.MinSize().Height
	if menu, ok := s.popUp.Content.(*appSelectMenu); ok {
		desiredHeight = menu.content.MinSize().Height
	}
	size := fyne.NewSize(s.Size().Width, fyne.Min(desiredHeight, areaSize.Height))
	pos := fyne.CurrentApp().Driver().AbsolutePositionForObject(s).Add(
		fyne.NewPos(0, s.Size().Height-s.Theme().Size(theme.SizeNameInputBorder)),
	)
	if pos.X+size.Width > areaSize.Width {
		pos.X = fyne.Max(0, areaSize.Width-size.Width)
	}
	if pos.Y+size.Height > areaSize.Height {
		pos.Y = fyne.Max(0, areaSize.Height-size.Height)
	}
	return size, pos
}

// appSelectMenu 用公开弹层和 appButton 提供选项图标、手型、纵向滚动及键盘导航。
type appSelectMenu struct {
	widget.BaseWidget
	content   *fyne.Container
	scroll    *container.Scroll
	options   []*appButton
	active    int
	onDismiss func()
}

func newAppSelectMenu(options []string, selectedIndex int, alignment fyne.TextAlign, choose func(string), dismiss func()) *appSelectMenu {
	m := &appSelectMenu{active: -1, onDismiss: dismiss}
	objects := make([]fyne.CanvasObject, len(options))
	m.options = make([]*appButton, len(options))
	for i, text := range options {
		text := text
		buttonAlignment := widget.ButtonAlignLeading
		switch alignment {
		case fyne.TextAlignCenter:
			buttonAlignment = widget.ButtonAlignCenter
		case fyne.TextAlignTrailing:
			buttonAlignment = widget.ButtonAlignTrailing
		}
		iconName := "chevron-right"
		iconColor := TextSecondary
		if i == selectedIndex {
			iconName = "check"
			iconColor = BrandBlue
		}
		option := &appButton{
			Button: &widget.Button{
				Text:       text,
				Icon:       lineIcon(iconName, iconColor),
				Importance: widget.LowImportance,
				Alignment:  buttonAlignment,
			},
			accessibilityLabel: text,
		}
		option.Button.ExtendBaseWidget(option)
		option.SetOnTapped(func() { choose(text) })
		m.options[i] = option
		objects[i] = option
	}
	m.content = container.NewVBox(objects...)
	m.scroll = container.NewVScroll(m.content)
	m.ExtendBaseWidget(m)
	return m
}

func (m *appSelectMenu) CreateRenderer() fyne.WidgetRenderer {
	return widget.NewSimpleRenderer(m.scroll)
}

func (m *appSelectMenu) FocusGained() { m.Refresh() }
func (m *appSelectMenu) FocusLost()   { m.Refresh() }
func (m *appSelectMenu) TypedRune(rune) {
}

func (m *appSelectMenu) TypedKey(event *fyne.KeyEvent) {
	switch event.Name {
	case fyne.KeyDown:
		m.activate(m.active + 1)
	case fyne.KeyUp:
		if m.active < 0 {
			m.activate(len(m.options) - 1)
		} else {
			m.activate(m.active - 1)
		}
	case fyne.KeyEnter, fyne.KeyReturn, fyne.KeySpace:
		if m.active < 0 {
			m.dismiss()
		} else {
			m.options[m.active].Tapped(nil)
		}
	case fyne.KeyEscape:
		m.dismiss()
	}
}

func (m *appSelectMenu) activate(index int) {
	if len(m.options) == 0 {
		return
	}
	if index < 0 {
		index = len(m.options) - 1
	} else if index >= len(m.options) {
		index = 0
	}
	if m.active >= 0 {
		m.options[m.active].Importance = widget.LowImportance
		m.options[m.active].Refresh()
	}
	m.active = index
	m.options[m.active].Importance = widget.MediumImportance
	m.options[m.active].Refresh()

	option := m.options[m.active]
	offsetY := m.scroll.Offset.Y
	optionTop := option.Position().Y
	optionBottom := optionTop + option.Size().Height
	if optionTop < offsetY {
		offsetY = optionTop
	} else if optionBottom > offsetY+m.scroll.Size().Height {
		offsetY = optionBottom - m.scroll.Size().Height
	}
	maxOffset := fyne.Max(0, m.content.MinSize().Height-m.scroll.Size().Height)
	offsetY = fyne.Max(0, fyne.Min(offsetY, maxOffset))
	if offsetY != m.scroll.Offset.Y {
		m.scroll.ScrollToOffset(fyne.NewPos(m.scroll.Offset.X, offsetY))
	}
}

func (m *appSelectMenu) dismiss() {
	if m.onDismiss != nil {
		m.onDismiss()
	}
}

var _ fyne.Focusable = (*appSelect)(nil)
var _ fyne.Focusable = (*appSelectMenu)(nil)
var _ desktop.Cursorable = (*appSelect)(nil)

// appButton 保留标准按钮渲染、焦点和禁用行为，统一图标、指针与按压反馈。
type appButton struct {
	*widget.Button
	accessibilityLabel string
	compact            bool
	tooltip            *appTooltip
}

func newAppButton(label, iconName string, importance widget.Importance, tapped func()) *appButton {
	button := &appButton{
		Button: &widget.Button{
			Text:       label,
			Icon:       lineIcon(iconName, appButtonIconColor(importance)),
			Importance: importance,
		},
		accessibilityLabel: label,
	}
	button.Button.ExtendBaseWidget(button)
	button.SetOnTapped(tapped)
	return button
}

func newAppIconButton(accessibilityLabel, iconName string, importance widget.Importance, tapped func()) *appButton {
	button := &appButton{
		Button: &widget.Button{
			Icon:       lineIcon(iconName, appButtonIconColor(importance)),
			Importance: importance,
		},
		accessibilityLabel: accessibilityLabel,
		compact:            true,
	}
	button.Button.ExtendBaseWidget(button)
	button.SetOnTapped(tapped)
	button.tooltip = newAppTooltip(button, accessibilityLabel)
	return button
}

func appButtonIconColor(importance widget.Importance) color.Color {
	switch importance {
	case widget.HighImportance, widget.DangerImportance, widget.SuccessImportance, widget.WarningImportance:
		return CardBG
	case widget.MediumImportance:
		return BrandBlue
	default:
		return TextSecondary
	}
}

func (b *appButton) MinSize() fyne.Size {
	size := b.Button.MinSize()
	if b.compact {
		size.Width = fyne.Max(size.Width, 36)
		size.Height = fyne.Max(size.Height, 36)
	}
	return size
}

func (b *appButton) SetOnTapped(tapped func()) {
	b.Button.OnTapped = func() {
		animateControlTap(b)
		if tapped != nil {
			tapped()
		}
	}
}

func (b *appButton) SetAction(label, iconName string) {
	b.accessibilityLabel = label
	if !b.compact {
		b.Button.SetText(label)
	}
	b.Button.SetIcon(lineIcon(iconName, appButtonIconColor(b.Importance)))
	if b.tooltip != nil {
		b.tooltip.cancel()
		b.tooltip.label = label
	}
}

func (b *appButton) Cursor() desktop.Cursor {
	if b.Disabled() {
		return desktop.DefaultCursor
	}
	return desktop.PointerCursor
}

func (b *appButton) AccessibilityLabel() string {
	if b.accessibilityLabel != "" {
		return b.accessibilityLabel
	}
	return b.Button.AccessibilityLabel()
}

func (b *appButton) TypedKey(event *fyne.KeyEvent) {
	switch event.Name {
	case fyne.KeySpace, fyne.KeyReturn, fyne.KeyEnter:
		b.Tapped(nil)
	}
}

var _ fyne.Focusable = (*appButton)(nil)
var _ fyne.Accessible = (*appButton)(nil)
var _ desktop.Cursorable = (*appButton)(nil)
