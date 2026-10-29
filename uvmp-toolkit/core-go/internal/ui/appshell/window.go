package appshell

import (
	"sync"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/driver"
	"fyne.io/fyne/v2/driver/desktop"
)

// shellWindow 在真实显示和尺寸稳定后同步无边框窗口的原生圆角。
// Fyne 的 CreateSplashWindow 是官方无边框入口；普通窗口保留给测试驱动等不支持的平台。
type shellWindow struct {
	fyne.Window
	once       sync.Once
	prepare    func()
	frameTimer *time.Timer
}

func newShellWindow(a fyne.App) fyne.Window {
	var base fyne.Window
	if d, ok := a.Driver().(desktop.Driver); ok {
		base = d.CreateSplashWindow()
		base.SetTitle(AppTitle)
	} else {
		base = a.NewWindow(AppTitle)
		base.SetPadded(false)
	}
	w := &shellWindow{Window: base}
	w.prepare = w.scheduleNativeFrame
	return w
}

// RunNative 显式转发扩展接口：嵌入 fyne.Window 接口不会自动提升其实现者的扩展方法。
// 缺少原生窗口的测试驱动按 Fyne 约定收到 UnknownContext。
func (w *shellWindow) RunNative(fn func(any)) {
	if native, ok := w.Window.(driver.NativeWindow); ok {
		native.RunNative(fn)
		return
	}
	fn(driver.UnknownContext{})
}

// scheduleNativeFrame 合并连续的尺寸事件，在最大化/还原落定后更新圆角区域。
// 不在原生窗口尺寸回调中立即裁剪，避免新增区域被旧圆角区域覆盖成黑块。
func (w *shellWindow) scheduleNativeFrame() {
	if w.frameTimer != nil {
		w.frameTimer.Stop()
	}
	w.frameTimer = time.AfterFunc(50*time.Millisecond, func() {
		runUI(func() { updateNativeFrame(w) })
	})
}

func (w *shellWindow) Show() {
	w.Window.Show()
	w.once.Do(w.prepare)
	w.scheduleNativeFrame()
}

func (w *shellWindow) Resize(size fyne.Size) {
	w.Window.Resize(size)
	w.scheduleNativeFrame()
}

func (w *shellWindow) ShowAndRun() {
	w.Show()
	fyne.CurrentApp().Run()
}

// windowBase 给需要驱动具体窗口类型的系统托盘接口解包。
// 普通 Fyne 窗口操作与 NativeWindow 则继续经 shellWindow 转发。
func windowBase(win fyne.Window) fyne.Window {
	if w, ok := win.(*shellWindow); ok {
		return w.Window
	}
	return win
}
