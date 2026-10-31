package sgccclockin

// 并发原语单测：键控互斥（同键串行/不同键并行/可取消/引用计数回收）与有界并发闸门
// （上限、热调整、可取消、释放幂等）。

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestKeyedMutexSameKeySerialized 同键串行：第二个使用者必须等第一个释放。
func TestKeyedMutexSameKeySerialized(t *testing.T) {
	var km keyedMutex
	var inFlight, peak int32
	ctx := context.Background()

	unlock1, err := km.Lock(ctx, "m11:2026-10-28")
	if err != nil {
		t.Fatalf("取锁失败: %v", err)
	}
	started := make(chan struct{})
	done := make(chan struct{})
	go func() {
		close(started)
		unlock2, lerr := km.Lock(ctx, "m11:2026-10-28")
		if lerr != nil {
			t.Errorf("取锁失败: %v", lerr)
			return
		}
		c := atomic.AddInt32(&inFlight, 1)
		if c > 1 {
			atomic.StoreInt32(&peak, c)
		}
		time.Sleep(20 * time.Millisecond)
		atomic.AddInt32(&inFlight, -1)
		unlock2()
		close(done)
	}()
	<-started
	select {
	case <-done:
		t.Fatalf("同键第二个使用者不应在首个释放前完成")
	case <-time.After(100 * time.Millisecond):
	}
	unlock1()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatalf("首个释放后第二个使用者未完成")
	}
	if atomic.LoadInt32(&peak) > 1 {
		t.Fatalf("同键不得并行")
	}
}

// TestKeyedMutexDifferentKeysParallel 不同键并行。
func TestKeyedMutexDifferentKeysParallel(t *testing.T) {
	var km keyedMutex
	var wg sync.WaitGroup
	start := make(chan struct{})
	var cur, peak int32
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			unlock, err := km.Lock(context.Background(), memberDateKey(int64(i), "2026-10-28"))
			if err != nil {
				t.Errorf("取锁失败: %v", err)
				return
			}
			defer unlock()
			c := atomic.AddInt32(&cur, 1)
			for {
				old := atomic.LoadInt32(&peak)
				if c <= old || atomic.CompareAndSwapInt32(&peak, old, c) {
					break
				}
			}
			<-start
			atomic.AddInt32(&cur, -1)
		}(i)
	}
	deadline := time.Now().Add(2 * time.Second)
	for atomic.LoadInt32(&peak) < 4 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	close(start)
	wg.Wait()
	if atomic.LoadInt32(&peak) != 4 {
		t.Fatalf("不同键应并行，实际峰值 %d", atomic.LoadInt32(&peak))
	}
}

// TestKeyedMutexCancelAndCleanup 等待可被 ctx 取消，且取消后条目引用计数归零（不残留键）。
func TestKeyedMutexCancelAndCleanup(t *testing.T) {
	var km keyedMutex
	unlock, err := km.Lock(context.Background(), "k")
	if err != nil {
		t.Fatalf("取锁失败: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if _, err := km.Lock(ctx, "k"); err == nil {
		t.Fatalf("等待中的取锁应随 ctx 取消返回错误")
	}
	unlock()
	unlock() // 幂等：重复解锁无副作用
	km.mu.Lock()
	n := len(km.locks)
	km.mu.Unlock()
	if n != 0 {
		t.Fatalf("引用计数归零应回收条目，实际残留 %d 个", n)
	}
}

// TestGateLimit 闸门并发上限：超过 limit 的任务等待，释放后放行。
func TestGateLimit(t *testing.T) {
	var g gate
	ctx := context.Background()
	release1, err := g.acquire(ctx, 2)
	if err != nil {
		t.Fatalf("取得名额失败: %v", err)
	}
	release2, err := g.acquire(ctx, 2)
	if err != nil {
		t.Fatalf("取得名额失败: %v", err)
	}
	got := make(chan struct{})
	done := make(chan struct{})
	go func() {
		r3, aerr := g.acquire(ctx, 2)
		if aerr != nil {
			t.Errorf("取得名额失败: %v", aerr)
			close(done)
			return
		}
		close(got)
		r3()
		close(done)
	}()
	select {
	case <-got:
		t.Fatalf("超出上限的任务不应立即执行")
	case <-time.After(80 * time.Millisecond):
	}
	release1()
	select {
	case <-got:
	case <-time.After(2 * time.Second):
		t.Fatalf("释放名额后等待任务未放行")
	}
	release2()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatalf("等待任务未归还名额")
	}
	if v := g.snapshot(); v != 0 {
		t.Fatalf("全部释放后应有 0 个在途，实际 %d", v)
	}
}

// TestGateLowerLimitHot g 的上限由调用方每次传入：调小后新进入者按新上限收敛（在途不打断）。
func TestGateLowerLimitHot(t *testing.T) {
	var g gate
	ctx := context.Background()
	release, err := g.acquire(ctx, 4)
	if err != nil {
		t.Fatalf("取得名额失败: %v", err)
	}
	done := make(chan struct{})
	go func() {
		r2, aerr := g.acquire(ctx, 1) // 上限调小到 1：已有 1 个在途 → 必须等待
		if aerr != nil {
			t.Errorf("取得名额失败: %v", aerr)
			return
		}
		close(done)
		r2()
	}()
	select {
	case <-done:
		t.Fatalf("上限调小后新任务不应立即执行")
	case <-time.After(80 * time.Millisecond):
	}
	release()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatalf("释放后新任务未放行")
	}
}

// TestConcurrencyConfigClamp 并发上限按运行时配置读取并收敛：同步默认 4（1–8）、照片默认 2（1–4），
// 非法值回退默认。照片管理器按 Limit 现读同一配置。
func TestConcurrencyConfigClamp(t *testing.T) {
	db, _ := openFake(t)
	defer db.Close()
	m := New(db)
	ctx := context.Background()
	cases := []struct {
		sync, photo         string
		wantSync, wantPhoto int
	}{
		{"", "", defaultSyncConcurrency, defaultPhotoConcurrency},
		{"1", "1", 1, 1},
		{"8", "4", 8, 4},
		{"99", "99", maxSyncConcurrency, maxPhotoConcurrency},
		{"0", "0", 1, 1},
		{"abc", "x", defaultSyncConcurrency, defaultPhotoConcurrency},
	}
	for _, c := range cases {
		cfg := newMapCfg()
		if c.sync != "" {
			cfg.set(CfgSyncConcurrency, c.sync)
		}
		if c.photo != "" {
			cfg.set(CfgPhotoConcurrency, c.photo)
		}
		m.Cfg = cfg
		if got := m.syncConcurrency(ctx); got != c.wantSync {
			t.Fatalf("同步并发 %q：得 %d 想 %d", c.sync, got, c.wantSync)
		}
		if got := m.photoConcurrency(ctx); got != c.wantPhoto {
			t.Fatalf("照片并发 %q：得 %d 想 %d", c.photo, got, c.wantPhoto)
		}
		if got := m.Ops.limit(); got != c.wantPhoto {
			t.Fatalf("照片管理器上限 %q：得 %d 想 %d", c.photo, got, c.wantPhoto)
		}
	}
}

// TestGateCancel 名额等待可被 ctx 取消。
func TestGateCancel(t *testing.T) {
	var g gate
	release, err := g.acquire(context.Background(), 1)
	if err != nil {
		t.Fatalf("取得名额失败: %v", err)
	}
	defer release()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if _, err := g.acquire(ctx, 1); err == nil {
		t.Fatalf("等待名额应随 ctx 取消返回错误")
	}
}
