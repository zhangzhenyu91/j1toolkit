//go:build windows

package appshell

import (
	"unsafe"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/driver"
	"golang.org/x/sys/windows"
)

var (
	user32Window        = windows.NewLazySystemDLL("user32.dll")
	procSetWindowPos    = user32Window.NewProc("SetWindowPos")
	procShowWindow      = user32Window.NewProc("ShowWindowAsync")
	procIsZoomed        = user32Window.NewProc("IsZoomed")
	procIsIconic        = user32Window.NewProc("IsIconic")
	procReleaseCapture  = user32Window.NewProc("ReleaseCapture")
	procSendMessage     = user32Window.NewProc("SendMessageW")
	procGetCursorPos    = user32Window.NewProc("GetCursorPos")
	procGetWindowRect   = user32Window.NewProc("GetWindowRect")
	procGetDPIForWindow = user32Window.NewProc("GetDpiForWindow")
	procGetWindowLong   = user32Window.NewProc("GetWindowLongPtrW")
	procSetWindowLong   = user32Window.NewProc("SetWindowLongPtrW")
)

const (
	gwlStyle        = ^uintptr(15) // (uintptr)-16
	wsMaximizeBox   = uintptr(0x00010000)
	swpNoSize       = uintptr(0x0001)
	swpNoMove       = uintptr(0x0002)
	swpNoZOrder     = uintptr(0x0004)
	swpNoActivate   = uintptr(0x0010)
	swpFrameChanged = uintptr(0x0020)
	swMinimize      = uintptr(6)
	swMaximize      = uintptr(3)
	swRestore       = uintptr(9)
	wmNCLButtonDown = uintptr(0x00A1)
	htCaption       = uintptr(2)
)

type nativeWindowPoint struct{ X, Y int32 }
type nativeWindowRect struct{ Left, Top, Right, Bottom int32 }

type windowsWindowOps struct {
	win       fyne.Window
	resizing  bool
	direction int
	start     nativeWindowPoint
	frame     nativeWindowRect
	maximized bool
}

func newNativeWindowOps(win fyne.Window) nativeWindowOps {
	return &windowsWindowOps{win: win}
}

func (w *windowsWindowOps) withHWND(fn func(uintptr)) {
	native, ok := w.win.(driver.NativeWindow)
	if !ok {
		return
	}
	native.RunNative(func(raw any) {
		ctx, ok := raw.(driver.WindowsWindowContext)
		if ok && ctx.HWND != 0 {
			fn(ctx.HWND)
		}
	})
}

// Prepare 保留 Fyne 原生无边框窗口，不装系统标题栏或厚边框。
func (w *windowsWindowOps) Prepare() {}

func (w *windowsWindowOps) Minimize() {
	w.withHWND(func(hwnd uintptr) { procShowWindow.Call(hwnd, swMinimize) })
}

// ToggleMaximize 仅补充最大化能力标志，由系统原生改变尺寸，确保 GLFW 收到重排事件。
// 不增加 WS_CAPTION / WS_THICKFRAME，因此不会带回系统标题栏或灰白边框。
func (w *windowsWindowOps) ToggleMaximize() {
	w.withHWND(func(hwnd uintptr) {
		if w.maximized {
			procShowWindow.Call(hwnd, swRestore)
			w.maximized = false
			return
		}
		if zoomed, _, _ := procIsZoomed.Call(hwnd); zoomed != 0 {
			procShowWindow.Call(hwnd, swRestore)
			return
		}
		style, _, _ := procGetWindowLong.Call(hwnd, gwlStyle)
		procSetWindowLong.Call(hwnd, gwlStyle, style|wsMaximizeBox)
		procSetWindowPos.Call(hwnd, 0, 0, 0, 0, 0,
			swpNoSize|swpNoMove|swpNoZOrder|swpNoActivate|swpFrameChanged)
		procShowWindow.Call(hwnd, swMaximize)
		w.maximized = true
	})
}

func (w *windowsWindowOps) BeginMove() {
	w.withHWND(func(hwnd uintptr) {
		var point nativeWindowPoint
		if ok, _, _ := procGetCursorPos.Call(uintptr(unsafe.Pointer(&point))); ok == 0 {
			return
		}
		coordinates := uintptr(uint16(point.X)) | uintptr(uint16(point.Y))<<16
		procReleaseCapture.Call()
		procSendMessage.Call(hwnd, wmNCLButtonDown, htCaption, coordinates)
	})
}

// BeginResize 在透明边缘热区内按实际屏幕坐标调整窗口，不依赖系统边框命中。
// 首次拖动记录窗口与鼠标位置，后续复用同一基准，避免高 DPI 或多屏下反复跳变。
func (w *windowsWindowOps) BeginResize(direction int) {
	if direction < resizeTopLeft || direction > resizeLeft {
		return
	}
	w.withHWND(func(hwnd uintptr) {
		var point nativeWindowPoint
		if ok, _, _ := procGetCursorPos.Call(uintptr(unsafe.Pointer(&point))); ok == 0 {
			return
		}
		if !w.resizing {
			w.resizing = true
			w.direction = direction
			w.start = point
			if ok, _, _ := procGetWindowRect.Call(hwnd, uintptr(unsafe.Pointer(&w.frame))); ok == 0 {
				w.resizing = false
				return
			}
		}
		left, top := w.frame.Left, w.frame.Top
		right, bottom := w.frame.Right, w.frame.Bottom
		dx, dy := point.X-w.start.X, point.Y-w.start.Y
		switch w.direction {
		case resizeTopLeft:
			left, top = left+dx, top+dy
		case resizeTop:
			top += dy
		case resizeTopRight:
			right, top = right+dx, top+dy
		case resizeRight:
			right += dx
		case resizeBottomRight:
			right, bottom = right+dx, bottom+dy
		case resizeBottom:
			bottom += dy
		case resizeBottomLeft:
			left, bottom = left+dx, bottom+dy
		case resizeLeft:
			left += dx
		}
		dpi := uintptr(96)
		if procGetDPIForWindow.Find() == nil {
			if value, _, _ := procGetDPIForWindow.Call(hwnd); value != 0 {
				dpi = value
			}
		}
		minWidth := int32(900 * float64(dpi) / 96)
		minHeight := int32(600 * float64(dpi) / 96)
		if right-left < minWidth {
			switch w.direction {
			case resizeTopLeft, resizeBottomLeft, resizeLeft:
				left = right - minWidth
			case resizeTopRight, resizeRight, resizeBottomRight:
				right = left + minWidth
			}
		}
		if bottom-top < minHeight {
			switch w.direction {
			case resizeTopLeft, resizeTop, resizeTopRight:
				top = bottom - minHeight
			case resizeBottomLeft, resizeBottom, resizeBottomRight:
				bottom = top + minHeight
			}
		}
		procSetWindowPos.Call(hwnd, 0, uintptr(left), uintptr(top),
			uintptr(right-left), uintptr(bottom-top), swpNoZOrder|swpNoActivate)
	})
}

func (w *windowsWindowOps) EndResize()               { w.resizing = false }
func (w *windowsWindowOps) SupportsEdgeResize() bool { return true }

func (w *windowsWindowOps) Maximized() bool {
	if w.maximized {
		return true
	}
	maximized := false
	w.withHWND(func(hwnd uintptr) {
		zoomed, _, _ := procIsZoomed.Call(hwnd)
		maximized = zoomed != 0
	})
	return maximized
}

func (w *windowsWindowOps) Minimized() bool {
	minimized := false
	w.withHWND(func(hwnd uintptr) {
		iconic, _, _ := procIsIconic.Call(hwnd)
		minimized = iconic != 0
	})
	return minimized
}
