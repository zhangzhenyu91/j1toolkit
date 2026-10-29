//go:build windows

package appshell

import (
	"unsafe"

	"fyne.io/fyne/v2"
	"golang.org/x/sys/windows"
)

var (
	gdi32Window           = windows.NewLazySystemDLL("gdi32.dll")
	procCreateRoundRegion = gdi32Window.NewProc("CreateRoundRectRgn")
	procDeleteRegion      = gdi32Window.NewProc("DeleteObject")
	procSetWindowRegion   = user32Window.NewProc("SetWindowRgn")
)

// updateNativeFrame 用 Win32 区域裁切 12px 圆角；Windows 10 即可使用，不依赖 Windows 11 DWM 属性。
func updateNativeFrame(win fyne.Window) {
	ops := &windowsWindowOps{win: win}
	ops.withHWND(func(hwnd uintptr) {
		installNativeFrameHook(win, hwnd)
		zoomed, _, _ := procIsZoomed.Call(hwnd)
		var rect nativeWindowRect
		if ok, _, _ := procGetWindowRect.Call(hwnd, uintptr(unsafe.Pointer(&rect))); ok == 0 {
			return
		}
		if zoomed != 0 {
			// 最大化恢复直角，避免遮挡工作区；还原窗口时重新应用圆角。
			procSetWindowRegion.Call(hwnd, 0, 1)
			return
		}
		dpi := uintptr(96)
		if procGetDPIForWindow.Find() == nil {
			if value, _, _ := procGetDPIForWindow.Call(hwnd); value != 0 {
				dpi = value
			}
		}
		ellipse := int32(24 * float64(dpi) / 96)
		region, _, _ := procCreateRoundRegion.Call(0, 0,
			uintptr(rect.Right-rect.Left+1), uintptr(rect.Bottom-rect.Top+1),
			uintptr(ellipse), uintptr(ellipse))
		if region == 0 {
			return
		}
		if ok, _, _ := procSetWindowRegion.Call(hwnd, region, 1); ok == 0 {
			procDeleteRegion.Call(region) // 成功时区域由系统接管，不能重复释放
		}
	})
}
