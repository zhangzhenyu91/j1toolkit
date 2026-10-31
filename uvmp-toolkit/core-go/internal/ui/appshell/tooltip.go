package appshell

import (
	"sync"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/driver/desktop"
	"fyne.io/fyne/v2/widget"
)

const appTooltipDelay = 450 * time.Millisecond

type appTooltip struct {
	owner *appButton
	label string

	mu         sync.Mutex
	timer      *time.Timer
	host       *appTooltipHost
	generation uint64
}

func newAppTooltip(owner *appButton, label string) *appTooltip {
	return &appTooltip{owner: owner, label: label}
}

// appTooltipHost 是窗口根 Stack 的兄弟层，不使用会切换 FocusManager 的 overlay。
type appTooltipHost struct {
	widget.BaseWidget
	panel      *fyne.Container
	text       *widget.Label
	background *canvas.Rectangle

	mu     sync.Mutex
	items  map[*appTooltip]struct{}
	active *appTooltip
}

func newAppTooltipHost(content fyne.CanvasObject) *fyne.Container {
	background := canvas.NewRectangle(CardBG)
	background.CornerRadius = 6
	background.StrokeColor = Border
	background.StrokeWidth = 1
	text := widget.NewLabel("")
	panel := container.NewStack(background, container.NewPadded(text))
	panel.Hide()
	host := &appTooltipHost{
		panel:      panel,
		text:       text,
		background: background,
		items:      map[*appTooltip]struct{}{},
	}
	host.ExtendBaseWidget(host)
	return container.NewStack(content, host)
}

func (h *appTooltipHost) CreateRenderer() fyne.WidgetRenderer {
	return &appTooltipHostRenderer{host: h}
}

type appTooltipHostRenderer struct{ host *appTooltipHost }

func (r *appTooltipHostRenderer) Layout(fyne.Size)   {}
func (r *appTooltipHostRenderer) MinSize() fyne.Size { return fyne.Size{} }
func (r *appTooltipHostRenderer) Refresh()           { r.host.panel.Refresh() }
func (r *appTooltipHostRenderer) Destroy()           { r.host.clear() }
func (r *appTooltipHostRenderer) Objects() []fyne.CanvasObject {
	return []fyne.CanvasObject{r.host.panel}
}

// appTooltipScope 为需要直接 Hide/Show/Remove、Objects 重建或行重绑定的容器提供确定性生命周期通知。
type appTooltipScope struct {
	widget.BaseWidget
	content *fyne.Container
}

func newAppTooltipScope(content *fyne.Container) *appTooltipScope {
	scope := &appTooltipScope{content: content}
	scope.ExtendBaseWidget(scope)
	return scope
}

// Reset 在内容重建或行重绑定前清理当前窗口的 pending/active tooltip。
func (s *appTooltipScope) Reset() {
	clearAppTooltipForObject(s)
}

func (s *appTooltipScope) resetForShell(shell *Shell) {
	if appTooltipScopeInShellWindow(s, shell) {
		s.Reset()
	}
}

func (s *appTooltipScope) hideForShell(shell *Shell) {
	s.resetForShell(shell)
	s.BaseWidget.Hide()
}

func (s *appTooltipScope) showForShell(shell *Shell) {
	s.resetForShell(shell)
	s.BaseWidget.Show()
}

// appTooltipScopeInShellWindow 防止旧 Shell 的异步回调清理后续窗口 tooltip。
func appTooltipScopeInShellWindow(scope *appTooltipScope, shell *Shell) bool {
	if scope == nil || shell == nil || shell.App == nil || shell.Win == nil || fyne.CurrentApp() != shell.App {
		return false
	}
	driver := shell.App.Driver()
	return driver != nil && driver.CanvasForObject(scope) == shell.Win.Canvas()
}

func (s *appTooltipScope) Hide() {
	s.Reset()
	s.BaseWidget.Hide()
}

func (s *appTooltipScope) Show() {
	s.Reset()
	s.BaseWidget.Show()
}

func (s *appTooltipScope) Remove(object fyne.CanvasObject) {
	clearAppTooltipForObject(object)
	s.content.Remove(object)
}

func (s *appTooltipScope) CreateRenderer() fyne.WidgetRenderer {
	return &appTooltipScopeRenderer{scope: s}
}

type appTooltipScopeRenderer struct{ scope *appTooltipScope }

func (r *appTooltipScopeRenderer) Layout(size fyne.Size) { r.scope.content.Resize(size) }
func (r *appTooltipScopeRenderer) MinSize() fyne.Size    { return r.scope.content.MinSize() }
func (r *appTooltipScopeRenderer) Refresh()              { r.scope.content.Refresh() }
func (r *appTooltipScopeRenderer) Destroy()              {}
func (r *appTooltipScopeRenderer) Objects() []fyne.CanvasObject {
	return []fyne.CanvasObject{r.scope.content}
}

func clearAppTooltipForObject(object fyne.CanvasObject) {
	app := fyne.CurrentApp()
	if app == nil || app.Driver() == nil {
		return
	}
	if host := findAppTooltipHost(app.Driver().CanvasForObject(object)); host != nil {
		host.clear()
	}
}

func clearAppTooltipForWindow(window fyne.Window) {
	if window == nil {
		return
	}
	if host := findAppTooltipHost(window.Canvas()); host != nil {
		host.clear()
	}
}

func findAppTooltipHost(canvas fyne.Canvas) *appTooltipHost {
	if canvas == nil {
		return nil
	}
	root, ok := canvas.Content().(*fyne.Container)
	if !ok {
		return nil
	}
	for _, object := range root.Objects {
		if host, ok := object.(*appTooltipHost); ok {
			return host
		}
	}
	return nil
}

func (t *appTooltip) findHost() *appTooltipHost {
	app := fyne.CurrentApp()
	if app == nil || app.Driver() == nil {
		return nil
	}
	return findAppTooltipHost(app.Driver().CanvasForObject(t.owner))
}

func (t *appTooltip) schedule() {
	if t == nil || t.label == "" || t.owner.Disabled() || !t.owner.Visible() {
		return
	}
	t.cancel()
	host := t.findHost()
	if host == nil {
		return
	}
	host.clear()
	t.mu.Lock()
	t.generation++
	generation := t.generation
	t.host = host
	t.timer = time.AfterFunc(appTooltipDelay, func() {
		t.show(generation)
	})
	host.mu.Lock()
	host.items[t] = struct{}{}
	host.mu.Unlock()
	t.mu.Unlock()
}

func (t *appTooltip) show(generation uint64) {
	runUI(func() {
		t.mu.Lock()
		if generation != t.generation || t.host == nil {
			t.mu.Unlock()
			return
		}
		t.timer = nil
		if t.owner.Disabled() || !t.owner.Visible() || !t.inCanvas() {
			t.mu.Unlock()
			t.cancel()
			return
		}
		host := t.host
		t.mu.Unlock()
		host.show(t)
	})
}

func (t *appTooltip) inCanvas() bool {
	app := fyne.CurrentApp()
	return app != nil && app.Driver() != nil && app.Driver().CanvasForObject(t.owner) != nil
}

func (t *appTooltip) invalidate() {
	t.mu.Lock()
	t.generation++
	if t.timer != nil {
		t.timer.Stop()
		t.timer = nil
	}
	t.host = nil
	t.mu.Unlock()
}

func (t *appTooltip) cancel() {
	if t == nil {
		return
	}
	t.mu.Lock()
	t.generation++
	if t.timer != nil {
		t.timer.Stop()
		t.timer = nil
	}
	host := t.host
	t.host = nil
	t.mu.Unlock()
	if host != nil {
		host.remove(t)
	}
}

func (h *appTooltipHost) show(t *appTooltip) {
	h.mu.Lock()
	if _, ok := h.items[t]; !ok {
		h.mu.Unlock()
		return
	}
	h.active = t
	h.mu.Unlock()

	app := fyne.CurrentApp()
	if app == nil || app.Driver() == nil {
		return
	}
	canvas := app.Driver().CanvasForObject(t.owner)
	if canvas == nil {
		return
	}
	h.text.SetText(t.label)
	panelSize := h.panel.MinSize()
	buttonPos := app.Driver().AbsolutePositionForObject(t.owner)
	hostPos := app.Driver().AbsolutePositionForObject(h)
	globalX := buttonPos.X + (t.owner.Size().Width-panelSize.Width)/2
	globalY := buttonPos.Y + t.owner.Size().Height + 4
	canvasSize := canvas.Size()
	if globalY+panelSize.Height > canvasSize.Height {
		globalY = buttonPos.Y - panelSize.Height - 4
	}
	maxX := canvasSize.Width - panelSize.Width - 4
	if maxX >= 4 {
		globalX = fyne.Max(4, fyne.Min(globalX, maxX))
	}
	globalY = fyne.Max(4, globalY)
	h.panel.Move(fyne.NewPos(globalX-hostPos.X, globalY-hostPos.Y))
	h.panel.Resize(panelSize)
	h.background.FillColor = CardBG
	h.background.StrokeColor = Border
	h.panel.Show()
	h.panel.Refresh()
}

func (h *appTooltipHost) remove(t *appTooltip) {
	h.mu.Lock()
	delete(h.items, t)
	hide := h.active == t
	if hide {
		h.active = nil
	}
	h.mu.Unlock()
	if hide {
		runUI(func() { h.panel.Hide() })
	}
}

func (h *appTooltipHost) clear() {
	h.mu.Lock()
	items := make([]*appTooltip, 0, len(h.items))
	for item := range h.items {
		items = append(items, item)
	}
	h.items = map[*appTooltip]struct{}{}
	h.active = nil
	h.mu.Unlock()
	for _, item := range items {
		item.invalidate()
	}
	runUI(func() { h.panel.Hide() })
}

func (b *appButton) MouseIn(event *desktop.MouseEvent) {
	b.Button.MouseIn(event)
	b.tooltip.schedule()
}

func (b *appButton) MouseOut() {
	b.Button.MouseOut()
	b.tooltip.cancel()
}

func (b *appButton) Tapped(event *fyne.PointEvent) {
	b.tooltip.cancel()
	b.Button.Tapped(event)
}

func (b *appButton) Disable() {
	b.tooltip.cancel()
	b.Button.Disable()
}

func (b *appButton) Hide() {
	b.tooltip.cancel()
	b.Button.Hide()
}

var _ desktop.Hoverable = (*appButton)(nil)
