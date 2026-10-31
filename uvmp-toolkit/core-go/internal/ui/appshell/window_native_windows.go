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

type windowsWindowOps struct{ win fyne.Window }

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
		if zoomed, _, _ := procIsZoomed.Call(hwnd); zoomed != 0 {
			procShowWindow.Call(hwnd, swRestore)
			return
		}
		style, _, _ := procGetWindowLong.Call(hwnd, gwlStyle)
		procSetWindowLong.Call(hwnd, gwlStyle, style|wsMaximizeBox)
		procSetWindowPos.Call(hwnd, 0, 0, 0, 0, 0,
			swpNoSize|swpNoMove|swpNoZOrder|swpNoActivate|swpFrameChanged)
		procShowWindow.Call(hwnd, swMaximize)
	})
}

func (w *windowsWindowOps) beginNonClientDrag(hitTest uintptr) {
	w.withHWND(func(hwnd uintptr) {
		var point nativeWindowPoint
		if ok, _, _ := procGetCursorPos.Call(uintptr(unsafe.Pointer(&point))); ok == 0 {
			return
		}
		coordinates := uintptr(uint16(point.X)) | uintptr(uint16(point.Y))<<16
		procReleaseCapture.Call()
		procSendMessage.Call(hwnd, wmNCLButtonDown, hitTest, coordinates)
	})
}

func (w *windowsWindowOps) BeginMove() { w.beginNonClientDrag(htCaption) }

func windowsResizeHitTest(direction int) (uintptr, bool) {
	hitTests := [...]uintptr{13, 12, 14, 11, 17, 15, 16, 10}
	if direction < resizeTopLeft || direction > resizeLeft {
		return 0, false
	}
	return hitTests[direction], true
}

func (w *windowsWindowOps) BeginResize(direction int) {
	hitTest, ok := windowsResizeHitTest(direction)
	if ok {
		w.beginNonClientDrag(hitTest)
	}
}

func (w *windowsWindowOps) EndResize()               {}
func (w *windowsWindowOps) SupportsEdgeResize() bool { return true }

func (w *windowsWindowOps) Maximized() bool {
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
