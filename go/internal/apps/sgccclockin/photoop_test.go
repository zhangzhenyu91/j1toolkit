package sgccclockin

// 照片任务并发回归：跨卡片并行（默认 2，1–4）、同卡 activeLock 互斥、panic 收尾并解锁。

import (
	"sync"
	"testing"
	"time"
)

// TestPhotoOpCrossCardParallel 不同卡片的照片任务并行（旧实现全局 tail 串行，峰值恒为 1）。
func TestPhotoOpCrossCardParallel(t *testing.T) {
	ops := NewPhotoOpManager()
	release := make(chan struct{})
	entered := make(chan struct{}, 8)
	var mu sync.Mutex
	cur, peak := 0, 0
	var wg sync.WaitGroup

	for i := 0; i < 3; i++ {
		op := ops.New("resync", 1, int64(100+i), 0)
		if op == nil {
			t.Fatalf("卡片 %d 应可登记任务", i)
		}
		wg.Add(1)
		ops.enqueueRun(func() {
			defer wg.Done()
			ops.Run(op.ID, func(*PhotoOp) error {
				mu.Lock()
				cur++
				if cur > peak {
					peak = cur
				}
				mu.Unlock()
				entered <- struct{}{}
				<-release
				mu.Lock()
				cur--
				mu.Unlock()
				return nil
			})
		})
	}

	// 默认上限 2：应恰有 2 个任务进入，第 3 个在闸门外等待
	for i := 0; i < 2; i++ {
		select {
		case <-entered:
		case <-time.After(2 * time.Second):
			close(release)
			t.Fatalf("第 %d 个照片任务未进入：跨卡片未并行", i+1)
		}
	}
	select {
	case <-entered:
		close(release)
		t.Fatalf("第 3 个任务不应越过默认上限 2 同时执行")
	case <-time.After(150 * time.Millisecond):
	}
	close(release)
	wg.Wait()

	mu.Lock()
	got := peak
	mu.Unlock()
	if got != 2 {
		t.Fatalf("跨卡片并行峰值应为默认上限 2，实际 %d", got)
	}
}

// TestPhotoOpSameCardExclusive 同卡片同时只允许一个任务（activeLock 保留）。
func TestPhotoOpSameCardExclusive(t *testing.T) {
	ops := NewPhotoOpManager()
	first := ops.New("resync", 1, 100, 0)
	if first == nil {
		t.Fatalf("首次登记应成功")
	}
	if second := ops.New("resync", 1, 100, 0); second != nil {
		t.Fatalf("同卡片进行中不得再登记任务")
	}
	if !ops.ActivePhotoOp(1, 100) {
		t.Fatalf("进行中任务应可见")
	}
}

// TestPhotoOpPanicFinalizesAndUnlocks 任务 panic 必须收敛为失败终态并释放卡片与并发名额
// （旧实现 panic 直接抛出 goroutine，既崩进程又不解锁）。
func TestPhotoOpPanicFinalizesAndUnlocks(t *testing.T) {
	ops := NewPhotoOpManager()
	op := ops.New("resync", 1, 100, 0)
	if op == nil {
		t.Fatalf("登记任务应成功")
	}
	done := make(chan struct{})
	ops.enqueueRun(func() {
		defer close(done)
		ops.Run(op.ID, func(*PhotoOp) error { panic("boom") })
	})
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatalf("panic 任务未收尾")
	}

	st, ok := ops.Status(op.ID)
	if !ok || !st.Failed || st.Step != "failed" || st.Error == "" {
		t.Fatalf("panic 应收敛为失败终态: %+v", st)
	}
	if ops.ActivePhotoOp(1, 100) {
		t.Fatalf("panic 后必须解锁卡片")
	}
	// 并发名额已释放：同卡可重新登记，且后续任务能真正执行
	second := ops.New("resync", 1, 100, 0)
	if second == nil {
		t.Fatalf("panic 后应可重新登记同卡任务")
	}
	ran := make(chan struct{})
	ops.enqueueRun(func() {
		ops.Run(second.ID, func(*PhotoOp) error {
			close(ran)
			return nil
		})
	})
	select {
	case <-ran:
	case <-time.After(2 * time.Second):
		t.Fatalf("panic 后并发名额未释放，后续任务无法执行")
	}
}
