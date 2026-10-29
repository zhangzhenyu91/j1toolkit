package platform

import (
	"context"
	"sync"
	"time"
)

// TickKind 驻留调度判定种类（内网工具箱开发指南 §4.4 驻留调度段）。
type TickKind int

const (
	// TickDaily 每日派车单同步到点判定（树障/交跨夜间兜底同挂此判定）。
	TickDaily TickKind = iota
	// TickTreeSync 树障统计表 U 盘指令轮询。
	TickTreeSync
	// TickCrossingSync 交跨统计表 U 盘指令轮询。
	TickCrossingSync
)

// String 诊断文案。
func (k TickKind) String() string {
	switch k {
	case TickDaily:
		return "daily"
	case TickTreeSync:
		return "tree_sync"
	case TickCrossingSync:
		return "crossing_sync"
	default:
		return "unknown"
	}
}

// TickOutcome 单次判定结果。
type TickOutcome struct {
	Kind    TickKind
	Started bool
	JobID   string
	Err     error
}

// TickFunc 壳→核心的判定调用（driver=app 时核心才真正执行，due_check 门闸在核心侧）。
type TickFunc func(ctx context.Context, kind TickKind) TickOutcome

// Scheduler 客户端驻留调度：启动 InitialDelay 后先查一次（补跑错过的到点），
// 之后每 Interval 一轮、每轮顺序跑三个判定（镜像 electron/main.cjs startInAppScheduler：
// 启动 10s 先查 + 每 60s 轮询）。判定错误不中断轮询。
type Scheduler struct {
	Interval     time.Duration // 默认 60s
	InitialDelay time.Duration // 默认 10s
	fn           TickFunc

	mu      sync.Mutex
	started bool
	cancel  context.CancelFunc
	done    chan struct{}
}

// NewScheduler 构造驻留调度器。
func NewScheduler(fn TickFunc) *Scheduler {
	return &Scheduler{
		Interval:     60 * time.Second,
		InitialDelay: 10 * time.Second,
		fn:           fn,
	}
}

// Start 启动调度协程（幂等：重复调用不叠加）。Stop 或 ctx 取消时结束。
func (s *Scheduler) Start(ctx context.Context) {
	s.mu.Lock()
	if s.started {
		s.mu.Unlock()
		return
	}
	s.started = true
	runCtx, cancel := context.WithCancel(ctx)
	s.cancel = cancel
	s.done = make(chan struct{})
	s.mu.Unlock()

	go s.loop(runCtx)
}

// Stop 停止调度并等待协程退出；幂等。
func (s *Scheduler) Stop() {
	s.mu.Lock()
	cancel, done := s.cancel, s.done
	s.started = false
	s.cancel = nil
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if done != nil {
		<-done
	}
}

func (s *Scheduler) loop(ctx context.Context) {
	closeDone := func() {
		s.mu.Lock()
		if s.done != nil {
			close(s.done)
			s.done = nil
		}
		s.mu.Unlock()
	}
	defer closeDone()

	delay := s.InitialDelay
	if delay < 0 {
		delay = 0
	}
	if delay > 0 {
		select {
		case <-ctx.Done():
			return
		case <-time.After(delay):
		}
	}
	s.RunOnce(ctx)
	t := time.NewTicker(s.Interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.RunOnce(ctx)
		}
	}
}

// RunOnce 单轮判定：daily → tree_sync → crossing_sync，顺序执行、逐项独立容错。
func (s *Scheduler) RunOnce(ctx context.Context) []TickOutcome {
	kinds := []TickKind{TickDaily, TickTreeSync, TickCrossingSync}
	out := make([]TickOutcome, 0, len(kinds))
	for _, k := range kinds {
		o := s.fn(ctx, k)
		o.Kind = k
		out = append(out, o)
	}
	return out
}
