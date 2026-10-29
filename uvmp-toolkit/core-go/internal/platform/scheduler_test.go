package platform

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

func TestScheduler_RunOnceOrder(t *testing.T) {
	var kinds []TickKind
	s := NewScheduler(func(ctx context.Context, k TickKind) TickOutcome {
		kinds = append(kinds, k)
		return TickOutcome{Started: k == TickDaily, JobID: "j-1"}
	})
	out := s.RunOnce(context.Background())
	if len(out) != 3 {
		t.Fatalf("单轮应跑 3 个判定: %d", len(out))
	}
	want := []TickKind{TickDaily, TickTreeSync, TickCrossingSync}
	for i, k := range want {
		if out[i].Kind != k || kinds[i] != k {
			t.Fatalf("判定顺序不符: %v", out)
		}
	}
	if !out[0].Started || out[0].JobID != "j-1" {
		t.Fatalf("结果透传不符: %+v", out[0])
	}
}

func TestScheduler_StartRunsInitialThenTicker(t *testing.T) {
	var n atomic.Int32
	s := NewScheduler(func(ctx context.Context, k TickKind) TickOutcome {
		n.Add(1)
		return TickOutcome{}
	})
	s.InitialDelay = 10 * time.Millisecond
	s.Interval = 30 * time.Millisecond

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s.Start(ctx)

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if n.Load() >= 6 { // 至少两轮（10ms 后首查 + 30ms 周期）
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if n.Load() < 6 {
		t.Fatalf("调度轮次不足: %d", n.Load())
	}
	s.Stop()
	s.Stop() // 幂等
	after := n.Load()
	time.Sleep(60 * time.Millisecond)
	if n.Load() != after {
		t.Fatalf("Stop 后仍继续判定: %d → %d", after, n.Load())
	}
}

func TestScheduler_StartIdempotent(t *testing.T) {
	var n atomic.Int32
	s := NewScheduler(func(ctx context.Context, k TickKind) TickOutcome {
		n.Add(1)
		return TickOutcome{}
	})
	s.InitialDelay = 5 * time.Millisecond
	s.Interval = 20 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s.Start(ctx)
	s.Start(ctx) // 不应叠加
	time.Sleep(80 * time.Millisecond)
	s.Stop()
	if got := n.Load(); got == 0 {
		t.Fatal("调度器未运行")
	}
}
