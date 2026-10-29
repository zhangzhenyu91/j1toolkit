// overlay：fake 宿主/悬浮窗（无真实窗口的测试与联调实现；同进程内模拟几何/输入/截帧）。
package overlay

import (
	"errors"
	"sync"
)

// FakeWindow fake 悬浮窗：记录几何/不透明度/显隐/绘制帧，事件可注入。
type FakeWindow struct {
	mu         sync.Mutex
	bounds     Rect
	opacity    float64
	visible    bool
	closed     bool
	painted    []*Buffer
	events     chan WindowEvent
	lastBuf    *Buffer
	boundsSets int
}

// NewFakeWindow 创建 fake 悬浮窗。
func NewFakeWindow(opts WindowOpts) *FakeWindow {
	return &FakeWindow{
		bounds:  opts.Bounds,
		opacity: opts.Opacity,
		visible: true,
		events:  make(chan WindowEvent, 16),
	}
}

// Bounds 当前几何。
func (w *FakeWindow) Bounds() Rect {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.bounds
}

// SetBounds 设置几何。
func (w *FakeWindow) SetBounds(r Rect) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.bounds = r
	w.boundsSets++
}

// SetBoundsCount SetBounds 调用次数（fake 扩展；拖拽轮询启动同步用）。
func (w *FakeWindow) SetBoundsCount() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.boundsSets
}

// SetOpacity 设置不透明度。
func (w *FakeWindow) SetOpacity(a float64) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.opacity = a
}

// Opacity 当前不透明度（fake 扩展）。
func (w *FakeWindow) Opacity() float64 {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.opacity
}

// SetVisible 显示/隐藏。
func (w *FakeWindow) SetVisible(v bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.visible = v
}

// Visible 是否显示（fake 扩展）。
func (w *FakeWindow) Visible() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.visible
}

// Paint 记录绘制内容。
func (w *FakeWindow) Paint(b *Buffer) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return errors.New("窗口已关闭")
	}
	w.lastBuf = b
	w.painted = append(w.painted, b)
	return nil
}

// LastBuffer 最近一次绘制内容（fake 扩展）。
func (w *FakeWindow) LastBuffer() *Buffer {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.lastBuf
}

// Events 输入事件流。
func (w *FakeWindow) Events() <-chan WindowEvent { return w.events }

// InjectEvent 注入输入事件（fake 扩展）。
func (w *FakeWindow) InjectEvent(ev WindowEvent) { w.events <- ev }

// Close 销毁（幂等）。
func (w *FakeWindow) Close() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.closed {
		w.closed = true
		close(w.events)
	}
}

// Closed 是否已关闭（fake 扩展）。
func (w *FakeWindow) Closed() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.closed
}

// FakeHost fake 宿主：预置显示器/截帧，窗口与光标可控。
type FakeHost struct {
	mu        sync.Mutex
	displays  []Display
	windows   []*FakeWindow
	cursor    Point
	leftDown  bool
	frames    map[int]*Frame // DisplayID → 帧；显式 nil 表示截屏为空
	captureN  map[int]int    // 截屏次数（断言 shotCache 用）
	captureCh chan struct{}  // 可选：阻塞截屏（忙门闩测试用）
}

// NewFakeHost 创建 fake 宿主（默认单显示器 1920×1080@1x，工作区贴边）。
func NewFakeHost() *FakeHost {
	d := Display{
		ID:     1,
		Bounds: Rect{X: 0, Y: 0, W: 1920, H: 1080},
		Scale:  1,
	}
	d.WorkArea = d.Bounds
	return &FakeHost{
		displays: []Display{d},
		frames:   map[int]*Frame{},
		captureN: map[int]int{},
	}
}

// AddDisplay 追加显示器。
func (h *FakeHost) AddDisplay(d Display) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.displays = append(h.displays, d)
}

// SetFrame 设置显示器截帧（nil 表示截屏为空）。
func (h *FakeHost) SetFrame(displayID int, f *Frame) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.frames[displayID] = f
}

// SetCaptureBlock 设置截屏阻塞通道（测试忙门闩）。
func (h *FakeHost) SetCaptureBlock(ch chan struct{}) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.captureCh = ch
}

// CaptureCount 显示器截屏次数。
func (h *FakeHost) CaptureCount(displayID int) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.captureN[displayID]
}

// SetPointer 设置光标位置与按键态。
func (h *FakeHost) SetPointer(p Point, leftDown bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.cursor = p
	h.leftDown = leftDown
}

// NewWindow 创建 fake 悬浮窗。
func (h *FakeHost) NewWindow(opts WindowOpts) (Window, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	w := NewFakeWindow(opts)
	h.windows = append(h.windows, w)
	return w, nil
}

// Windows 已创建的 fake 悬浮窗（fake 扩展）。
func (h *FakeHost) Windows() []*FakeWindow {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]*FakeWindow, len(h.windows))
	copy(out, h.windows)
	return out
}

// PointerState 光标位置与左键态。
func (h *FakeHost) PointerState() (Point, bool, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.cursor, h.leftDown, nil
}

// Displays 全部显示器。
func (h *FakeHost) Displays() []Display {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]Display, len(h.displays))
	copy(out, h.displays)
	return out
}

// Primary 主显示器（第 1 个）。
func (h *FakeHost) Primary() Display { return h.Displays()[0] }

// DisplayFor 相交面积最大的显示器（无交集取主显示器）。
func (h *FakeHost) DisplayFor(r Rect) Display {
	ds := h.Displays()
	best := ds[0]
	bestArea := 0
	for _, d := range ds {
		ix := minInt(r.X+r.W, d.Bounds.X+d.Bounds.W) - maxInt(r.X, d.Bounds.X)
		iy := minInt(r.Y+r.H, d.Bounds.Y+d.Bounds.H) - maxInt(r.Y, d.Bounds.Y)
		area := 0
		if ix > 0 && iy > 0 {
			area = ix * iy
		}
		if area > bestArea {
			bestArea = area
			best = d
		}
	}
	if bestArea == 0 {
		return ds[0]
	}
	return best
}

// CaptureDisplay 截取显示器整屏帧。
func (h *FakeHost) CaptureDisplay(d Display, maxThumb int) (*Frame, error) {
	h.mu.Lock()
	ch := h.captureCh
	h.captureN[d.ID]++
	f := h.frames[d.ID]
	h.mu.Unlock()
	if ch != nil {
		<-ch // 模拟慢截屏（忙门闩测试）
	}
	if f == nil {
		return nil, nil
	}
	if maxThumb > 0 {
		tw, th := ThumbSize(f.Width, f.Height, maxThumb)
		if tw != f.Width || th != f.Height {
			return scaleFrame(f, tw, th), nil
		}
	}
	return f, nil
}

// Close 释放（无资源）。
func (h *FakeHost) Close() {}

// scaleFrame 最近邻缩放（fake 用；真实平台由系统出缩略图）。
func scaleFrame(f *Frame, w, h int) *Frame {
	out := make([]byte, w*h*4)
	for y := 0; y < h; y++ {
		sy := y * f.Height / h
		for x := 0; x < w; x++ {
			sx := x * f.Width / w
			si := (sy*f.Width + sx) * 4
			di := (y*w + x) * 4
			copy(out[di:di+4], f.Pix[si:si+4])
		}
	}
	return &Frame{Pix: out, Width: w, Height: h, DisplayID: f.DisplayID}
}
