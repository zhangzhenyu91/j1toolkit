package appshell

import (
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"

	"fyne.io/fyne/v2"
)

// 本文件：UI 执行体串行化。
//
// 生产驱动的 fyne.Do 把闭包切到主线程执行（天然串行）；Fyne 测试驱动则在调用
// 协程内联执行（test/driver.go DoFromGoroutine）——Async 回填与测试协程并发
// 触碰控件会撞 Fyne 内部缓存（concurrent map writes，已踩坑）。runUI 在
// fyne.Do 外层加串行锁把全部 UI 更新排队；同协程嵌套调用（fn 体内再调
// Toast/对话框等助手）按 goroutine id 放行，避免测试驱动内联语义下自锁。
//
// 约定：包内控件更新一律经 runUI（助手函数可自包一层，嵌套安全）。
var (
	uiMu    sync.Mutex
	uiOwner atomic.Int64 // 当前持锁执行 UI 体的 goroutine id（0=空闲；仅持锁者写入）
)

// runUI 在 UI 执行体中运行 fn（测试驱动=内联完成；生产驱动=入队主线程后返回）。
func runUI(fn func()) {
	gid := goID()
	if gid != 0 && uiOwner.Load() == gid {
		// 同协程已在 UI 体内（嵌套助手）：直接入队/内联，不再取锁
		fyne.Do(fn)
		return
	}
	uiMu.Lock()
	uiOwner.Store(gid)
	defer func() {
		uiOwner.Store(0)
		uiMu.Unlock()
	}()
	fyne.Do(fn)
}

// goID 当前 goroutine id（栈帧解析；runUI 每次调用一次，开销可忽略）。
func goID() int64 {
	var buf [48]byte
	n := runtime.Stack(buf[:], false)
	s := strings.TrimPrefix(string(buf[:n]), "goroutine ")
	if i := strings.IndexByte(s, ' '); i > 0 {
		if id, err := strconv.ParseInt(s[:i], 10, 64); err == nil {
			return id
		}
	}
	return 0
}
