package appshell

import (
	"sync"
	"testing"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/test"

	"github.com/zhangzhenyu91/j1toolkit/uvmp-toolkit/core-go/internal/ui"
)

func TestNativeFrameCornerRadius(t *testing.T) {
	if got := nativeFrameCornerRadius(true, 1.5); got != 0 {
		t.Fatalf("最大化必须恢复直角：%.1f", got)
	}
	if got := nativeFrameCornerRadius(false, 0); got != 12 {
		t.Fatalf("无效缩放值下还原文应为 12px 圆角：%.1f", got)
	}
	if got := nativeFrameCornerRadius(false, 1.5); got != 18 {
		t.Fatalf("圆角未随画布缩放：%.1f", got)
	}
}

type frameScheduleWindow struct {
	fyne.Window
	calls int
}

func (w *frameScheduleWindow) scheduleNativeFrame() { w.calls++ }

func TestScheduleNativeFrameUpdateUsesShellScheduler(t *testing.T) {
	win := &frameScheduleWindow{}
	scheduleNativeFrameUpdate(win)
	if win.calls != 1 {
		t.Fatalf("WM 尺寸事件未进入壳层圆角调度：%d", win.calls)
	}
}

type fakeNativeFrameWatchSource struct {
	events    chan nativeFrameWatchEvent
	nextCalls int
	closes    int
	closeOnce sync.Once
}

func newFakeNativeFrameWatchSource(events ...nativeFrameWatchEvent) *fakeNativeFrameWatchSource {
	capacity := len(events)
	if capacity == 0 {
		capacity = 1
	}
	source := &fakeNativeFrameWatchSource{events: make(chan nativeFrameWatchEvent, capacity)}
	for _, event := range events {
		source.events <- event
	}
	return source
}

func (s *fakeNativeFrameWatchSource) Next() nativeFrameWatchEvent {
	s.nextCalls++
	return <-s.events
}

func (s *fakeNativeFrameWatchSource) Close() {
	s.closeOnce.Do(func() { s.closes++ })
}

func waitNativeFrameWatch(t *testing.T, done <-chan struct{}) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("监听读循环未退出")
	}
}

func TestNativeFrameWatchOpensSynchronouslyAndReleasesOnDestroy(t *testing.T) {
	win := &frameScheduleWindow{}
	first := newFakeNativeFrameWatchSource(nativeFrameWatchDestroy)
	opened := 0
	done := startNativeFrameWatch(win, 9101, func() nativeFrameWatchSource {
		opened++
		return first
	})
	if opened != 1 {
		t.Fatalf("监听连接未在启动读循环前同步建立：%d", opened)
	}
	waitNativeFrameWatch(t, done)
	if first.nextCalls != 1 || first.closes != 1 {
		t.Fatalf("Destroy 后资源未正确退出：Next=%d Close=%d", first.nextCalls, first.closes)
	}

	second := newFakeNativeFrameWatchSource(nativeFrameWatchDestroy)
	done = startNativeFrameWatch(win, 9101, func() nativeFrameWatchSource {
		opened++
		return second
	})
	waitNativeFrameWatch(t, done)
	if opened != 2 || second.nextCalls != 1 || second.closes != 1 {
		t.Fatalf("Destroy 后监听注册未释放，无法重建：open=%d Next=%d Close=%d", opened, second.nextCalls, second.closes)
	}
}

func TestNativeFrameWatchClassifiesWMEvents(t *testing.T) {
	win := &frameScheduleWindow{}
	source := newFakeNativeFrameWatchSource(
		nativeFrameWatchConfigure,
		nativeFrameWatchIgnore,
		nativeFrameWatchState,
		nativeFrameWatchDestroy,
	)
	done := startNativeFrameWatch(win, 9102, func() nativeFrameWatchSource { return source })
	waitNativeFrameWatch(t, done)
	if win.calls != 2 {
		t.Fatalf("WM 事件分类错误，Configure/State 应各调度一次：%d", win.calls)
	}
	if source.nextCalls != 4 || source.closes != 1 {
		t.Fatalf("WM 事件未完整读取或连接未关闭：Next=%d Close=%d", source.nextCalls, source.closes)
	}
}

type frameStateWindow struct {
	frameScheduleWindow
	states int
}

func (w *frameStateWindow) notifyNativeState() { w.states++ }

func TestNativeFrameWatchStateNotifiesMaximizeCallback(t *testing.T) {
	win := &frameStateWindow{}
	source := newFakeNativeFrameWatchSource(
		nativeFrameWatchConfigure,
		nativeFrameWatchState,
		nativeFrameWatchState,
		nativeFrameWatchDestroy,
	)
	done := startNativeFrameWatch(win, 9105, func() nativeFrameWatchSource { return source })
	waitNativeFrameWatch(t, done)
	if win.states != 2 {
		t.Fatalf("_NET_WM_STATE 事件应逐次驱动最大化同步回调：%d", win.states)
	}
	if win.calls != 3 {
		t.Fatalf("Configure/State 仍应调度圆角更新：%d", win.calls)
	}
}

func TestShellWindowRegistersNativeStateCallback(t *testing.T) {
	a := test.NewApp()
	defer a.Quit()
	win := &shellWindow{}
	called := false
	win.setNativeStateCallback(func() { called = true })
	win.notifyNativeState()
	if !called {
		t.Fatal("原生状态事件未调用注册的最大化同步回调")
	}
}

func TestShellRegistersNativeStateCallback(t *testing.T) {
	a := test.NewApp()
	defer a.Quit()
	s := newTestShell(t, a, ui.NewFakeBackend(), Options{DisableTray: true, DisableScheduler: true})
	window, ok := s.Win.(*shellWindow)
	if !ok {
		t.Fatalf("Shell 窗口类型不符：%T", s.Win)
	}
	window.stateMu.Lock()
	callback := window.onNativeState
	window.stateMu.Unlock()
	if callback == nil {
		t.Fatal("buildLayout 未注册原生状态最大化同步回调")
	}
}

func TestWindowMaximizedCacheLateBackfillDoesNotOverwriteEventValue(t *testing.T) {
	key := uintptr(0x6ad1)
	forgetWindowMaximized(key)
	t.Cleanup(func() { forgetWindowMaximized(key) })
	// 状态事件先写入新值 true。
	storeWindowMaximized(key, true)
	// 迟到的 Maximized miss 查询返回旧值 false，只能回填缺失项，不能覆盖事件值。
	if got := loadOrStoreWindowMaximized(key, false); got != true {
		t.Fatalf("迟到旧查询覆盖了状态事件新值：%v", got)
	}
	if value, ok := cachedWindowMaximized(key); !ok || !value {
		t.Fatalf("缓存最终值错误：%v/%v", value, ok)
	}
	forgetWindowMaximized(key)
	if got := loadOrStoreWindowMaximized(key, false); got != false {
		t.Fatalf("缺失缓存应回填查询值：%v", got)
	}
}

func TestNativeFrameWatchActiveKeyDoesNotReopen(t *testing.T) {
	win := &frameScheduleWindow{}
	first := newFakeNativeFrameWatchSource()
	opened := 0
	firstDone := startNativeFrameWatch(win, 9103, func() nativeFrameWatchSource {
		opened++
		return first
	})
	second := newFakeNativeFrameWatchSource(nativeFrameWatchDestroy)
	secondDone := startNativeFrameWatch(win, 9103, func() nativeFrameWatchSource {
		opened++
		return second
	})
	waitNativeFrameWatch(t, secondDone)
	if opened != 1 || second.nextCalls != 0 || second.closes != 0 {
		t.Fatalf("活动监听被重复建立：open=%d Next=%d Close=%d", opened, second.nextCalls, second.closes)
	}
	first.events <- nativeFrameWatchDestroy
	waitNativeFrameWatch(t, firstDone)
	if first.closes != 1 {
		t.Fatalf("原监听连接未关闭：%d", first.closes)
	}
}

func TestNativeFrameWatchOpenFailureCanRetry(t *testing.T) {
	win := &frameScheduleWindow{}
	done := startNativeFrameWatch(win, 9104, func() nativeFrameWatchSource { return nil })
	waitNativeFrameWatch(t, done)
	source := newFakeNativeFrameWatchSource(nativeFrameWatchDestroy)
	done = startNativeFrameWatch(win, 9104, func() nativeFrameWatchSource { return source })
	waitNativeFrameWatch(t, done)
	if source.nextCalls != 1 || source.closes != 1 {
		t.Fatalf("建立失败残留监听注册：Next=%d Close=%d", source.nextCalls, source.closes)
	}
}
