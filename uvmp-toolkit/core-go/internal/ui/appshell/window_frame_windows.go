//go:build windows

package appshell

import (
	"sync"

	"fyne.io/fyne/v2"
	"golang.org/x/sys/windows"
)

// 仅监听原生尺寸变化；不处理非客户区、边框或普通鼠标消息。
type nativeFrameHook struct {
	win      fyne.Window
	previous uintptr
}

var (
	nativeFrameHooks    sync.Map // HWND -> nativeFrameHook
	nativeFrameUpdates  sync.Map // HWND -> 是否正在同步圆角，防止区域更新重入
	procCallWindowProc  = user32Window.NewProc("CallWindowProcW")
	nativeFrameCallback uintptr
)

func init() {
	nativeFrameCallback = windows.NewCallback(nativeFrameProc)
}

const (
	gwlpWndProc = ^uintptr(3) // (uintptr)-4
	wmSize      = uint32(0x0005)
	wmNCDestroy = uint32(0x0082)
)

func installNativeFrameHook(win fyne.Window, hwnd uintptr) {
	if _, loaded := nativeFrameHooks.Load(hwnd); loaded {
		return
	}
	old, _, _ := procSetWindowLong.Call(hwnd, gwlpWndProc, nativeFrameCallback)
	if old == 0 {
		return
	}
	if previous, loaded := nativeFrameHooks.LoadOrStore(hwnd, nativeFrameHook{win: win, previous: old}); loaded {
		procSetWindowLong.Call(hwnd, gwlpWndProc, previous.(nativeFrameHook).previous)
	}
}

func nativeFrameProc(hwnd uintptr, message uint32, wParam, lParam uintptr) uintptr {
	value, ok := nativeFrameHooks.Load(hwnd)
	if !ok {
		return 0
	}
	hook := value.(nativeFrameHook)
	if message == wmNCDestroy {
		nativeFrameHooks.Delete(hwnd)
		procSetWindowLong.Call(hwnd, gwlpWndProc, hook.previous)
	}
	result, _, _ := procCallWindowProc.Call(hook.previous, hwnd, uintptr(message), wParam, lParam)
	if message == wmSize {
		if _, updating := nativeFrameUpdates.Load(hwnd); !updating {
			nativeFrameUpdates.Store(hwnd, true)
			updateNativeFrame(hook.win)
			nativeFrameUpdates.Delete(hwnd)
		}
	}
	return result
}
