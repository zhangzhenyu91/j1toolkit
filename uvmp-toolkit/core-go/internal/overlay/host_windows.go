// overlay：Windows 原生实现（Win32 分层窗 + GDI 截屏）。
// 仅用 stdlib syscall 直调 user32/gdi32/kernel32/shcore，零新增模块依赖（CGO_ENABLED=0 可构建）。
//
// 要点：
//   - 透明置顶悬浮窗 = WS_POPUP + WS_EX_LAYERED|TOOLWINDOW|TOPMOST|NOACTIVATE（跳任务栏、不抢焦点），
//     内容逐像素预乘 BGRA 经 UpdateLayeredWindow 合成（全窗透明区不挡点击以外的屏幕）；
//   - 全局不透明度烘进像素 alpha（ULW 与 SetLayeredWindowAttributes 互斥——已踩坑）；
//   - 进程开 Per-Monitor V2 DPI 感知：接口层 DIP 坐标 × Display.Scale = 物理像素；
//   - 窗口操作全部串行回窗口线程执行（跨线程 SetWindowPos/ULW 在部分环境不稳）。
package overlay

import (
	"errors"
	"math"
	"runtime"
	"sync"
	"syscall"
	"unsafe"
)

var (
	user32   = syscall.NewLazyDLL("user32.dll")
	gdi32    = syscall.NewLazyDLL("gdi32.dll")
	kernel32 = syscall.NewLazyDLL("kernel32.dll")
	shcore   = syscall.NewLazyDLL("shcore.dll")

	procRegisterClassExW      = user32.NewProc("RegisterClassExW")
	procCreateWindowExW       = user32.NewProc("CreateWindowExW")
	procDefWindowProcW        = user32.NewProc("DefWindowProcW")
	procDestroyWindow         = user32.NewProc("DestroyWindow")
	procShowWindow            = user32.NewProc("ShowWindow")
	procSetWindowPos          = user32.NewProc("SetWindowPos")
	procUpdateLayeredWindow   = user32.NewProc("UpdateLayeredWindow")
	procGetMessageW           = user32.NewProc("GetMessageW")
	procTranslateMessage      = user32.NewProc("TranslateMessage")
	procDispatchMessageW      = user32.NewProc("DispatchMessageW")
	procPostQuitMessage       = user32.NewProc("PostQuitMessage")
	procPostThreadMessageW    = user32.NewProc("PostThreadMessageW")
	procGetCursorPos          = user32.NewProc("GetCursorPos")
	procGetAsyncKeyState      = user32.NewProc("GetAsyncKeyState")
	procMonitorFromRect       = user32.NewProc("MonitorFromRect")
	procGetMonitorInfoW       = user32.NewProc("GetMonitorInfoW")
	procEnumDisplayMonitors   = user32.NewProc("EnumDisplayMonitors")
	procLoadCursorW           = user32.NewProc("LoadCursorW")
	procGetDC                 = user32.NewProc("GetDC")
	procReleaseDC             = user32.NewProc("ReleaseDC")
	procSetProcessDpiAwareCtx = user32.NewProc("SetProcessDpiAwarenessContext")
	procSetProcessDPIAware    = user32.NewProc("SetProcessDPIAware")

	procCreateCompatibleDC = gdi32.NewProc("CreateCompatibleDC")
	procDeleteDC           = gdi32.NewProc("DeleteDC")
	procCreateDIBSection   = gdi32.NewProc("CreateDIBSection")
	procSelectObject       = gdi32.NewProc("SelectObject")
	procDeleteObject       = gdi32.NewProc("DeleteObject")
	procBitBlt             = gdi32.NewProc("BitBlt")

	procGetCurrentThreadId = kernel32.NewProc("GetCurrentThreadId")
	procGetModuleHandleW   = kernel32.NewProc("GetModuleHandleW")
	procGetDpiForMonitor   = shcore.NewProc("GetDpiForMonitor")
)

// Win32 常量
const (
	wsPopup        = 0x80000000
	wsExLayered    = 0x00080000
	wsExToolWindow = 0x00000080
	wsExTopMost    = 0x00000008
	wsExNoActivate = 0x08000000

	hwndTopmost     = ^uintptr(0) // HWND_TOPMOST = -1
	swpNoSize       = 0x0001
	swpNoMove       = 0x0002
	swpNoActivate   = 0x0010
	swpShowWindow   = 0x0040
	swHide          = 0
	swShowNoAct     = 4
	wmDestroy       = 0x0002
	wmLButtonDown   = 0x0201
	wmLButtonUp     = 0x0202
	wmRButtonDown   = 0x0204
	wmRButtonUp     = 0x0205
	wmAppJob        = 0x8001
	wmQuit          = 0x0012
	ulwAlpha        = 0x2
	acSrcOver       = 0
	acSrcAlpha      = 1
	biRGB           = 0
	dibRGBColors    = 0
	srcCopy         = 0x00CC0020
	captureBlt      = 0x40000000
	mdTDefault      = 0
	monitorDefaultT = 2
)

type winPoint struct{ X, Y int32 }
type winRect struct{ L, T, R, B int32 }
type winSize struct{ CX, CY int32 }

type wndClassExW struct {
	CbSize        uint32
	Style         uint32
	WndProc       uintptr
	ClsExtra      int32
	WndExtra      int32
	HInstance     uintptr
	HIcon         uintptr
	HCursor       uintptr
	HbrBackground uintptr
	MenuName      *uint16
	ClassName     *uint16
	HIconSm       uintptr
}

type winMsg struct {
	Hwnd    uintptr
	Message uint32
	WParam  uintptr
	LParam  uintptr
	Time    uint32
	Pt      winPoint
}

type monitorInfoExW struct {
	CbSize    uint32
	RcMonitor winRect
	RcWork    winRect
	Flags     uint32
	SzDevice  [32]uint16
}

type bitmapInfoHeader struct {
	Size          uint32
	Width         int32
	Height        int32
	Planes        uint16
	BitCount      uint16
	Compression   uint32
	SizeImage     uint32
	XPelsPerMeter int32
	YPelsPerMeter int32
	ClrUsed       uint32
	ClrImportant  uint32
}

type blendFunction struct {
	BlendOp             uint8
	BlendFlags          uint8
	SourceConstantAlpha uint8
	AlphaFormat         uint8
}

// winDisplay 显示器（DIP 接口 + 物理像素缓存）。
type winDisplay struct {
	Display
	px winRect
}

// winHost Windows 宿主。
type winHost struct {
	mu       sync.Mutex
	displays []winDisplay
	thread   *winThread
	closed   bool
}

// winThread 窗口线程：消息循环 + 串行任务队列（窗口创建/挪动/绘制都跑在这条线程）。
type winThread struct {
	tid   uintptr
	ready chan struct{}
	jobs  chan func()
	done  chan struct{}
}

// NewHost 创建 Windows 悬浮窗宿主（幂等初始化 DPI 感知与窗口线程）。
func NewHost() (Host, error) {
	// Per-Monitor V2：坐标不被虚拟化，DIP 换算才有意义
	if r, _, _ := procSetProcessDpiAwareCtx.Call(^uintptr(3)); r == 0 { // DPI_AWARENESS_CONTEXT_PER_MONITOR_AWARE_V2
		procSetProcessDPIAware.Call()
	}
	t := &winThread{
		ready: make(chan struct{}),
		jobs:  make(chan func(), 64),
		done:  make(chan struct{}),
	}
	go t.run()
	<-t.ready
	h := &winHost{thread: t}
	h.refreshDisplays()
	return h, nil
}

func (t *winThread) run() {
	runtime.LockOSThread()
	tid, _, _ := procGetCurrentThreadId.Call()
	t.tid = tid
	// 注册窗口类（顺带建好消息队列）
	className, _ := syscall.UTF16PtrFromString("UvmpOverlayWindow")
	cursor, _, _ := procLoadCursorW.Call(0, 32512) // IDC_ARROW
	var cls wndClassExW
	cls.CbSize = uint32(unsafe.Sizeof(cls))
	cls.WndProc = syscall.NewCallback(winWndProc)
	cls.HInstance = hInstance()
	cls.HCursor = cursor
	cls.ClassName = className
	if r, _, _ := procRegisterClassExW.Call(uintptr(unsafe.Pointer(&cls))); r == 0 {
		close(t.ready)
		close(t.done)
		return
	}
	close(t.ready)
	var m winMsg
	for {
		r, _, _ := procGetMessageW.Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0)
		if int32(r) <= 0 {
			break
		}
		if m.Message == wmAppJob {
			for {
				select {
				case fn := <-t.jobs:
					fn()
					continue
				default:
				}
				break
			}
			continue
		}
		procTranslateMessage.Call(uintptr(unsafe.Pointer(&m)))
		procDispatchMessageW.Call(uintptr(unsafe.Pointer(&m)))
	}
	close(t.done)
}

// do 在窗口线程执行 fn 并等待完成。
func (t *winThread) do(fn func()) {
	done := make(chan struct{})
	t.jobs <- func() {
		defer close(done)
		fn()
	}
	procPostThreadMessageW.Call(t.tid, wmAppJob, 0, 0)
	<-done
}

func hInstance() uintptr {
	r, _, _ := procGetModuleHandleW.Call(0)
	return r
}

// winWndProc 窗口过程：输入事件转发到对应窗口事件通道。
func winWndProc(hwnd, msg, wparam, lparam uintptr) uintptr {
	switch msg {
	case wmLButtonDown, wmLButtonUp, wmRButtonDown, wmRButtonUp:
		w := windowFromHwnd(hwnd)
		if w == nil {
			break
		}
		ev := WindowEvent{
			X:      int(int16(lparam & 0xffff)),
			Y:      int(int16((lparam >> 16) & 0xffff)),
			Button: 1,
		}
		if msg == wmRButtonDown || msg == wmRButtonUp {
			ev.Button = 3
		}
		if msg == wmLButtonDown || msg == wmRButtonDown {
			ev.Kind = EventPointerPress
		} else {
			ev.Kind = EventPointerRelease
		}
		// 窗内坐标为物理像素 → DIP
		s := w.dpiScale()
		ev.X = int(math.Round(float64(ev.X) / s))
		ev.Y = int(math.Round(float64(ev.Y) / s))
		select {
		case w.events <- ev:
		default:
		}
	case wmDestroy:
		if w := windowFromHwnd(hwnd); w != nil {
			w.markClosed()
		}
	}
	r, _, _ := procDefWindowProcW.Call(hwnd, msg, wparam, lparam)
	return r
}

var (
	regMu     sync.Mutex
	regByHwnd = map[uintptr]*winWindow{}
)

func registerWindow(w *winWindow) {
	regMu.Lock()
	defer regMu.Unlock()
	regByHwnd[w.hwnd] = w
}

func unregisterWindow(hwnd uintptr) {
	regMu.Lock()
	defer regMu.Unlock()
	delete(regByHwnd, hwnd)
}

func windowFromHwnd(hwnd uintptr) *winWindow {
	regMu.Lock()
	defer regMu.Unlock()
	return regByHwnd[hwnd]
}

// ---------------------------------------------------------------- 显示器与坐标

// refreshDisplays 枚举显示器（物理矩形 + DPI 缩放 → DIP 边界）。
func (h *winHost) refreshDisplays() {
	var list []winDisplay
	cb := syscall.NewCallback(func(monitor, _ uintptr, _ uintptr, _ uintptr) uintptr {
		var mi monitorInfoExW
		mi.CbSize = uint32(unsafe.Sizeof(mi))
		if r, _, _ := procGetMonitorInfoW.Call(monitor, uintptr(unsafe.Pointer(&mi))); r == 0 {
			return 1
		}
		scale := 1.0
		var dpi uint32
		if r, _, _ := procGetDpiForMonitor.Call(monitor, mdTDefault, uintptr(unsafe.Pointer(&dpi)), 0); r == 0 && dpi != 0 {
			scale = float64(dpi) / 96.0
		}
		px := mi.RcMonitor
		list = append(list, winDisplay{
			Display: Display{
				ID:     len(list) + 1,
				Bounds: px.toDip(scale),
				Scale:  scale,
			},
			px: px,
		})
		if mi.Flags&1 == 1 { // 主显示器排前
			n := len(list)
			list[n-1], list[0] = list[0], list[n-1]
		}
		return 1
	})
	procEnumDisplayMonitors.Call(0, 0, cb, 0)
	if len(list) == 0 {
		list = append(list, winDisplay{
			Display: Display{ID: 1, Bounds: Rect{X: 0, Y: 0, W: 1920, H: 1080}, WorkArea: Rect{X: 0, Y: 0, W: 1920, H: 1080}, Scale: 1},
			px:      winRect{L: 0, T: 0, R: 1920, B: 1080},
		})
	}
	for i := range list {
		list[i].WorkArea = list[i].Bounds // 工作区差异（任务栏）由 GetMonitorInfo RcWork 二次刷新
	}
	// 补齐工作区（RcWork）
	cb2 := syscall.NewCallback(func(monitor, _ uintptr, _ uintptr, _ uintptr) uintptr {
		var mi monitorInfoExW
		mi.CbSize = uint32(unsafe.Sizeof(mi))
		if r, _, _ := procGetMonitorInfoW.Call(monitor, uintptr(unsafe.Pointer(&mi))); r == 0 {
			return 1
		}
		for i := range list {
			if list[i].px == mi.RcMonitor {
				list[i].WorkArea = mi.RcWork.toDip(list[i].Scale)
			}
		}
		return 1
	})
	procEnumDisplayMonitors.Call(0, 0, cb2, 0)
	h.mu.Lock()
	h.displays = list
	h.mu.Unlock()
}

// toDip 物理矩形 → DIP 矩形。
func (r winRect) toDip(scale float64) Rect {
	if scale <= 0 {
		scale = 1
	}
	return Rect{
		X: int(math.Round(float64(r.L) / scale)),
		Y: int(math.Round(float64(r.T) / scale)),
		W: int(math.Round(float64(r.R-r.L) / scale)),
		H: int(math.Round(float64(r.B-r.T) / scale)),
	}
}

// Displays 全部显示器。
func (h *winHost) Displays() []Display {
	h.refreshDisplays()
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]Display, len(h.displays))
	for i, d := range h.displays {
		out[i] = d.Display
	}
	return out
}

// Primary 主显示器。
func (h *winHost) Primary() Display {
	ds := h.Displays()
	for _, d := range ds {
		if d.Bounds.X == 0 && d.Bounds.Y == 0 {
			return d
		}
	}
	return ds[0]
}

// DisplayFor 相交面积最大的显示器（无交集取主显示器）。
func (h *winHost) DisplayFor(r Rect) Display {
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

// scaleOf 物理点所在显示器的缩放。
func (h *winHost) scaleOfPx(x, y int32) float64 {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, d := range h.displays {
		if x >= d.px.L && x < d.px.R && y >= d.px.T && y < d.px.B {
			return d.Scale
		}
	}
	return 1
}

// PointerState 光标位置（DIP）与左键按下态。
func (h *winHost) PointerState() (Point, bool, error) {
	var p winPoint
	if r, _, _ := procGetCursorPos.Call(uintptr(unsafe.Pointer(&p))); r == 0 {
		return Point{}, false, errors.New("GetCursorPos 失败")
	}
	s := h.scaleOfPx(p.X, p.Y)
	down, _, _ := procGetAsyncKeyState.Call(0x01) // VK_LBUTTON
	return Point{X: int(math.Round(float64(p.X) / s)), Y: int(math.Round(float64(p.Y) / s))}, down&0x8000 != 0, nil
}

// CaptureDisplay GDI 截取显示器整屏帧（稠密 BGRA8888，物理像素；maxThumb 长边封顶）。
func (h *winHost) CaptureDisplay(d Display, maxThumb int) (*Frame, error) {
	h.mu.Lock()
	px := winRect{
		L: int32(math.Round(float64(d.Bounds.X) * d.Scale)),
		T: int32(math.Round(float64(d.Bounds.Y) * d.Scale)),
		R: int32(math.Round(float64(d.Bounds.X+d.Bounds.W) * d.Scale)),
		B: int32(math.Round(float64(d.Bounds.Y+d.Bounds.H) * d.Scale)),
	}
	for _, wdisp := range h.displays {
		if wdisp.ID == d.ID {
			px = wdisp.px
			break
		}
	}
	h.mu.Unlock()
	w := int(px.R - px.L)
	hh := int(px.B - px.T)
	if w <= 0 || hh <= 0 {
		return nil, errors.New("显示器尺寸非法")
	}
	screenDC, _, _ := procGetDC.Call(0)
	if screenDC == 0 {
		return nil, errors.New("GetDC 失败")
	}
	defer procReleaseDC.Call(0, screenDC)
	memDC, _, _ := procCreateCompatibleDC.Call(screenDC)
	if memDC == 0 {
		return nil, errors.New("CreateCompatibleDC 失败")
	}
	defer procDeleteDC.Call(memDC)

	var bmi struct {
		Header bitmapInfoHeader
		Masks  [3]uint32
	}
	bmi.Header.Size = uint32(unsafe.Sizeof(bmi.Header))
	bmi.Header.Width = int32(w)
	bmi.Header.Height = -int32(hh) // 负高 = 自顶向下
	bmi.Header.Planes = 1
	bmi.Header.BitCount = 32
	bmi.Header.Compression = biRGB
	var bits unsafe.Pointer
	dib, _, _ := procCreateDIBSection.Call(screenDC, uintptr(unsafe.Pointer(&bmi)), dibRGBColors,
		uintptr(unsafe.Pointer(&bits)), 0, 0)
	if dib == 0 || bits == nil {
		return nil, errors.New("CreateDIBSection 失败")
	}
	defer procDeleteObject.Call(dib)
	old, _, _ := procSelectObject.Call(memDC, dib)
	defer procSelectObject.Call(memDC, old)

	if r, _, _ := procBitBlt.Call(memDC, 0, 0, uintptr(w), uintptr(hh), screenDC,
		uintptr(px.L), uintptr(px.T), srcCopy|captureBlt); r == 0 {
		return nil, errors.New("BitBlt 失败")
	}
	n := w * hh * 4
	src := unsafe.Slice((*byte)(bits), n)
	pix := make([]byte, n)
	copy(pix, src)
	for i := 3; i < n; i += 4 {
		pix[i] = 255 // 屏幕像素无 alpha
	}
	if maxThumb > 0 {
		tw, th := ThumbSize(w, hh, maxThumb)
		if tw != w || th != hh {
			return scaleFrameBGRA(pix, w, hh, tw, th, d.ID), nil
		}
	}
	return &Frame{Pix: pix, Width: w, Height: hh, DisplayID: d.ID}, nil
}

// scaleFrameBGRA 见 buffer.go（与 fake/截屏共用）。

// Close 停消息循环。
func (h *winHost) Close() {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return
	}
	h.closed = true
	procPostThreadMessageW.Call(h.thread.tid, wmQuit, 0, 0)
}

// ---------------------------------------------------------------- 悬浮窗

// winWindow Windows 悬浮窗。
type winWindow struct {
	host      *winHost
	hwnd      uintptr
	events    chan WindowEvent
	closed    chan struct{}
	closeOnce sync.Once
	evtOnce   sync.Once
	mu        sync.Mutex
	bounds    Rect // DIP
	opacity   float64
	visible   bool
	lastBuf   *Buffer
	scale     float64
}

// NewWindow 创建透明置顶悬浮窗。
func (h *winHost) NewWindow(opts WindowOpts) (Window, error) {
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return nil, errors.New("宿主已关闭")
	}
	h.mu.Unlock()
	if opts.Opacity <= 0 {
		opts.Opacity = 1
	}
	scale := h.DisplayFor(opts.Bounds).Scale
	if scale <= 0 {
		scale = 1
	}
	w := &winWindow{
		host:    h,
		events:  make(chan WindowEvent, 32),
		closed:  make(chan struct{}),
		bounds:  opts.Bounds,
		opacity: opts.Opacity,
		visible: true,
		scale:   scale,
	}
	title, _ := syscall.UTF16PtrFromString(opts.Title)
	className, _ := syscall.UTF16PtrFromString("UvmpOverlayWindow")
	pxX := int32(math.Round(float64(opts.Bounds.X) * scale))
	pxY := int32(math.Round(float64(opts.Bounds.Y) * scale))
	pxW := int32(math.Round(float64(opts.Bounds.W) * scale))
	pxH := int32(math.Round(float64(opts.Bounds.H) * scale))
	var hwnd uintptr
	h.thread.do(func() {
		r, _, _ := procCreateWindowExW.Call(
			wsExLayered|wsExToolWindow|wsExTopMost|wsExNoActivate,
			uintptr(unsafe.Pointer(className)),
			uintptr(unsafe.Pointer(title)),
			wsPopup,
			uintptr(pxX), uintptr(pxY), uintptr(pxW), uintptr(pxH),
			0, 0, hInstance(), 0)
		hwnd = r
	})
	if hwnd == 0 {
		return nil, errors.New("CreateWindowExW 失败")
	}
	w.hwnd = hwnd
	registerWindow(w)
	h.thread.do(func() {
		procSetWindowPos.Call(hwnd, hwndTopmost, 0, 0, 0, 0, swpNoMove|swpNoSize|swpNoActivate)
		procShowWindow.Call(hwnd, swShowNoAct)
	})
	// 首帧空内容（全透明）完成分层化
	buf := NewBuffer(maxInt(opts.Bounds.W, 1), maxInt(opts.Bounds.H, 1))
	_ = w.Paint(buf)
	return w, nil
}

func (w *winWindow) dpiScale() float64 {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.scale <= 0 {
		return 1
	}
	return w.scale
}

// Bounds 当前几何（DIP）。
func (w *winWindow) Bounds() Rect {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.bounds
}

// SetBounds 移动+缩放一步到位（透明窗单独缩宽/高在 Windows 会静默失灵——已踩坑，故整体设界）。
func (w *winWindow) SetBounds(r Rect) {
	w.mu.Lock()
	w.bounds = r
	scale := w.scale
	w.mu.Unlock()
	pxX := int32(math.Round(float64(r.X) * scale))
	pxY := int32(math.Round(float64(r.Y) * scale))
	pxW := int32(math.Round(float64(r.W) * scale))
	pxH := int32(math.Round(float64(r.H) * scale))
	w.host.thread.do(func() {
		procSetWindowPos.Call(w.hwnd, 0, uintptr(pxX), uintptr(pxY), uintptr(pxW), uintptr(pxH), swpNoActivate)
	})
}

// SetOpacity 设置全局不透明度（烘进像素 alpha 重绘）。
func (w *winWindow) SetOpacity(a float64) {
	if a < 0.3 {
		a = 0.3
	}
	if a > 1 {
		a = 1
	}
	w.mu.Lock()
	w.opacity = a
	buf := w.lastBuf
	w.mu.Unlock()
	if buf != nil {
		_ = w.Paint(buf)
	}
}

// SetVisible 显示/隐藏（显示不抢焦点）。
func (w *winWindow) SetVisible(v bool) {
	w.mu.Lock()
	w.visible = v
	w.mu.Unlock()
	cmd := uintptr(swHide)
	if v {
		cmd = swShowNoAct
	}
	w.host.thread.do(func() {
		procShowWindow.Call(w.hwnd, cmd)
	})
}

// Paint 提交整窗内容（RGBA 预乘 → BGRA 预乘 + 不透明度，经 UpdateLayeredWindow 合成）。
// 缓冲为 DIP 分辨率；DPI>1 时按窗口物理尺寸最近邻放大后送显。
func (w *winWindow) Paint(b *Buffer) error {
	if b == nil {
		return errors.New("空内容缓冲")
	}
	w.mu.Lock()
	w.lastBuf = b
	bb := w.bounds
	opacity := w.opacity
	scale := w.scale
	w.mu.Unlock()
	if b.W != bb.W || b.H != bb.H {
		return errors.New("内容缓冲尺寸与窗口不一致")
	}
	pxW := int32(math.Round(float64(bb.W) * scale))
	pxH := int32(math.Round(float64(bb.H) * scale))
	if pxW < 1 {
		pxW = 1
	}
	if pxH < 1 {
		pxH = 1
	}
	bgra := b.BGRA()
	if int(pxW) != b.W || int(pxH) != b.H {
		bgra = scaleFrameBGRA(bgra, b.W, b.H, int(pxW), int(pxH), 0).Pix
	}
	if opacity < 1 {
		f := uint8(math.Round(opacity * 255))
		for i := 0; i < len(bgra); i += 4 {
			bgra[i] = uint8(uint32(bgra[i]) * uint32(f) / 255)
			bgra[i+1] = uint8(uint32(bgra[i+1]) * uint32(f) / 255)
			bgra[i+2] = uint8(uint32(bgra[i+2]) * uint32(f) / 255)
			bgra[i+3] = uint8(uint32(bgra[i+3]) * uint32(f) / 255)
		}
	}
	var err error
	w.host.thread.do(func() {
		screenDC, _, _ := procGetDC.Call(0)
		if screenDC == 0 {
			err = errors.New("GetDC 失败")
			return
		}
		defer procReleaseDC.Call(0, screenDC)
		memDC, _, _ := procCreateCompatibleDC.Call(screenDC)
		if memDC == 0 {
			err = errors.New("CreateCompatibleDC 失败")
			return
		}
		defer procDeleteDC.Call(memDC)
		var bmi struct {
			Header bitmapInfoHeader
			Masks  [3]uint32
		}
		bmi.Header.Size = uint32(unsafe.Sizeof(bmi.Header))
		bmi.Header.Width = pxW
		bmi.Header.Height = -pxH
		bmi.Header.Planes = 1
		bmi.Header.BitCount = 32
		bmi.Header.Compression = biRGB
		var bits unsafe.Pointer
		dib, _, _ := procCreateDIBSection.Call(screenDC, uintptr(unsafe.Pointer(&bmi)), dibRGBColors,
			uintptr(unsafe.Pointer(&bits)), 0, 0)
		if dib == 0 || bits == nil {
			err = errors.New("CreateDIBSection 失败")
			return
		}
		defer procDeleteObject.Call(dib)
		dst := unsafe.Slice((*byte)(bits), len(bgra))
		copy(dst, bgra)
		old, _, _ := procSelectObject.Call(memDC, dib)
		defer procSelectObject.Call(memDC, old)
		var dstPt winPoint
		src := winPoint{}
		size := winSize{CX: pxW, CY: pxH}
		blend := blendFunction{BlendOp: acSrcOver, SourceConstantAlpha: 255, AlphaFormat: acSrcAlpha}
		if r, _, _ := procUpdateLayeredWindow.Call(w.hwnd, screenDC,
			uintptr(unsafe.Pointer(&dstPt)), uintptr(unsafe.Pointer(&size)),
			memDC, uintptr(unsafe.Pointer(&src)), 0,
			uintptr(unsafe.Pointer(&blend)), ulwAlpha); r == 0 {
			err = errors.New("UpdateLayeredWindow 失败")
		}
	})
	return err
}

// Events 输入事件流。
func (w *winWindow) Events() <-chan WindowEvent { return w.events }

// Close 销毁窗口（幂等）。
func (w *winWindow) Close() {
	w.closeOnce.Do(func() {
		w.host.thread.do(func() {
			procDestroyWindow.Call(w.hwnd)
		})
		unregisterWindow(w.hwnd)
		w.markClosed()
	})
}

// markClosed 关闭事件通道（DestroyWindow 的 WM_DESTROY 与 Close 双路径幂等）。
func (w *winWindow) markClosed() {
	w.evtOnce.Do(func() {
		close(w.events)
		close(w.closed)
	})
}
