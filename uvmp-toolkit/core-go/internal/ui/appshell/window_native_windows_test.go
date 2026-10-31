//go:build windows

package appshell

import (
	"runtime"
	"testing"
	"time"
	"unsafe"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/driver"
	"golang.org/x/sys/windows"
)

func TestWindowsResizeHitTest(t *testing.T) {
	tests := []struct {
		direction int
		want      uintptr
	}{
		{resizeTopLeft, 13},
		{resizeTop, 12},
		{resizeTopRight, 14},
		{resizeRight, 11},
		{resizeBottomRight, 17},
		{resizeBottom, 15},
		{resizeBottomLeft, 16},
		{resizeLeft, 10},
	}
	for _, tt := range tests {
		got, ok := windowsResizeHitTest(tt.direction)
		if !ok || got != tt.want {
			t.Errorf("EWMH 方向 %d 命中映射错误：got=(%d, %v) want=(%d, true)", tt.direction, got, ok, tt.want)
		}
	}
	for _, direction := range []int{-1, resizeLeft + 1} {
		if got, ok := windowsResizeHitTest(direction); ok || got != 0 {
			t.Errorf("非法方向 %d 应拒绝：got=(%d, %v)", direction, got, ok)
		}
	}
}

var (
	procTestCreateWindowEx   = user32Window.NewProc("CreateWindowExW")
	procTestDestroyWindow    = user32Window.NewProc("DestroyWindow")
	procTestShowWindow       = user32Window.NewProc("ShowWindow")
	procTestPeekMessage      = user32Window.NewProc("PeekMessageW")
	procTestTranslateMessage = user32Window.NewProc("TranslateMessage")
	procTestDispatchMessage  = user32Window.NewProc("DispatchMessageW")
)

type windowsNativeTestWindow struct {
	fyne.Window
	hwnd uintptr
}

func (w *windowsNativeTestWindow) RunNative(fn func(any)) {
	fn(driver.WindowsWindowContext{HWND: w.hwnd})
}

func newWindowsNativeTestWindow(t *testing.T) *windowsNativeTestWindow {
	t.Helper()
	className := windows.StringToUTF16Ptr("Static")
	title := windows.StringToUTF16Ptr("Task5 native maximize state")
	const wsOverlappedWindow = uintptr(0x00CF0000)
	hwnd, _, err := procTestCreateWindowEx.Call(
		0,
		uintptr(unsafe.Pointer(className)),
		uintptr(unsafe.Pointer(title)),
		wsOverlappedWindow,
		0, 0, 320, 240,
		0, 0, 0, 0,
	)
	if hwnd == 0 {
		t.Fatalf("创建测试 HWND 失败：%v", err)
	}
	t.Cleanup(func() { procTestDestroyWindow.Call(hwnd) })
	return &windowsNativeTestWindow{hwnd: hwnd}
}

type windowsNativeTestMessage struct {
	HWND     uintptr
	Message  uint32
	WParam   uintptr
	LParam   uintptr
	Time     uint32
	Point    nativeWindowPoint
	LPrivate uint32
}

func waitWindowsNativeZoom(t *testing.T, hwnd uintptr, want bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		for {
			var message windowsNativeTestMessage
			ptr := uintptr(unsafe.Pointer(&message))
			if result, _, _ := procTestPeekMessage.Call(ptr, hwnd, 0, 0, 1); result == 0 {
				break
			}
			procTestTranslateMessage.Call(ptr)
			procTestDispatchMessage.Call(ptr)
		}
		zoomed, _, _ := procIsZoomed.Call(hwnd)
		if (zoomed != 0) == want {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("等待 HWND IsZoomed=%v 超时", want)
}

func TestWindowsWindowOps_MaximizeTracksNativeZoomAfterExternalRestore(t *testing.T) {
	runtime.LockOSThread()
	t.Cleanup(runtime.UnlockOSThread)
	win := newWindowsNativeTestWindow(t)
	ops := &windowsWindowOps{win: win}

	ops.ToggleMaximize()
	waitWindowsNativeZoom(t, win.hwnd, true)
	if !ops.Maximized() {
		t.Fatal("应用内最大化后未读取到原生最大化状态")
	}
	style, _, _ := procGetWindowLong.Call(win.hwnd, gwlStyle)
	if style&wsMaximizeBox == 0 {
		t.Fatal("最大化后丢失 WS_MAXIMIZEBOX")
	}

	procTestShowWindow.Call(win.hwnd, swRestore)
	waitWindowsNativeZoom(t, win.hwnd, false)
	if ops.Maximized() {
		t.Fatal("外部 WM 还原后仍返回缓存的最大化状态")
	}

	ops.ToggleMaximize()
	waitWindowsNativeZoom(t, win.hwnd, true)
	if !ops.Maximized() {
		t.Fatal("外部还原后再次最大化失败")
	}
}
