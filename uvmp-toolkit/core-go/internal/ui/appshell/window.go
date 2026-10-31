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
	frameMu    sync.Mutex
	frameTimer *time.Timer

	stateMu       sync.Mutex
	onNativeState func()
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
	w.frameMu.Lock()
	defer w.frameMu.Unlock()
	if w.frameTimer != nil {
		w.frameTimer.Stop()
	}
	w.frameTimer = time.AfterFunc(50*time.Millisecond, func() {
		runUI(func() { updateNativeFrame(w) })
	})
}

// setNativeStateCallback 注册 _NET_WM_STATE 事件回调（在 UI 线程执行）。
func (w *shellWindow) setNativeStateCallback(fn func()) {
	w.stateMu.Lock()
	w.onNativeState = fn
	w.stateMu.Unlock()
}

// notifyNativeState 由原生监听协程调用，切回 UI 线程后再同步标题栏/页面模式。
func (w *shellWindow) notifyNativeState() {
	w.stateMu.Lock()
	fn := w.onNativeState
	w.stateMu.Unlock()
	if fn != nil {
		runUI(fn)
	}
}

// notifyNativeState 把 WM 状态事件转给实现该扩展的壳层窗口；普通窗口忽略。
func notifyNativeState(win fyne.Window) {
	if w, ok := win.(interface{ notifyNativeState() }); ok {
		w.notifyNativeState()
	}
}

func scheduleNativeFrameUpdate(win fyne.Window) {
	if w, ok := win.(interface{ scheduleNativeFrame() }); ok {
		w.scheduleNativeFrame()
		return
	}
	updateNativeFrame(win)
}

type nativeFrameWatchEvent int

const (
	nativeFrameWatchIgnore nativeFrameWatchEvent = iota
	nativeFrameWatchConfigure
	nativeFrameWatchState
	nativeFrameWatchDestroy
)

type nativeFrameWatchSource interface {
	Next() nativeFrameWatchEvent
	Close()
}

var nativeFrameWatches sync.Map

// windowMaximizedStates 缓存原生窗口最大化状态：X11 由 _NET_WM_STATE 事件刷新，
// Maximized 布局路径优先读缓存，避免每帧 XOpenDisplay。
var windowMaximizedStates sync.Map // map[uintptr]bool

func cachedWindowMaximized(key uintptr) (bool, bool) {
	if key == 0 {
		return false, false
	}
	value, ok := windowMaximizedStates.Load(key)
	if !ok {
		return false, false
	}
	maximized, ok := value.(bool)
	return maximized, ok
}

func storeWindowMaximized(key uintptr, maximized bool) {
	if key != 0 {
		windowMaximizedStates.Store(key, maximized)
	}
}

// loadOrStoreWindowMaximized 仅在缓存缺失时回填查询结果，避免迟到旧查询覆盖状态事件新值。
func loadOrStoreWindowMaximized(key uintptr, maximized bool) bool {
	if key == 0 {
		return maximized
	}
	actual, _ := windowMaximizedStates.LoadOrStore(key, maximized)
	value, ok := actual.(bool)
	return ok && value
}

func forgetWindowMaximized(key uintptr) {
	if key != 0 {
		windowMaximizedStates.Delete(key)
	}
}

func startNativeFrameWatch(win fyne.Window, key uintptr, open func() nativeFrameWatchSource) <-chan struct{} {
	done := make(chan struct{})
	if _, loaded := nativeFrameWatches.Load(key); loaded {
		close(done)
		return done
	}
	source := open()
	if source == nil {
		close(done)
		return done
	}
	if _, loaded := nativeFrameWatches.LoadOrStore(key, struct{}{}); loaded {
		source.Close()
		close(done)
		return done
	}
	go func() {
		defer close(done)
		defer nativeFrameWatches.Delete(key)
		defer source.Close()
		for {
			switch source.Next() {
			case nativeFrameWatchConfigure:
				scheduleNativeFrameUpdate(win)
			case nativeFrameWatchState:
				// WM 最大化/还原事件除刷新圆角外，还要在 UI 线程同步标题栏与页面模式，
				// 不能只依赖之后可能不再发生的 Layout。
				notifyNativeState(win)
				scheduleNativeFrameUpdate(win)
			case nativeFrameWatchDestroy:
				return
			}
		}
	}()
	return done
}

func nativeFrameCornerRadius(maximized bool, scale float32) float32 {
	if maximized {
		return 0
	}
	if scale <= 0 {
		scale = 1
	}
	return 12 * scale
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
