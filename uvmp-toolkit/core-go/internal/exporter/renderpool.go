package exporter

import (
	"context"
	"fmt"
	"sync"
)

// renderSlot 一个独立浏览器渲染 worker（独占 WorkDir）：
// token Generation 改变时 Close 后重建并完整官方登录；渲染错误后替换 worker。
type renderSlot struct {
	idx     int
	factory RendererFactory
	creds   CredsSource
	log     func(string, ...any)

	mu  sync.Mutex
	r   Renderer
	gen int // 当前 worker 持有的凭证代数（-1 表示未启动）
}

func newRenderSlot(idx int, factory RendererFactory, creds CredsSource, log func(string, ...any)) *renderSlot {
	return &renderSlot{idx: idx, factory: factory, creds: creds, log: log, gen: -1}
}

// get 取可用 worker：凭证代数变了就重启（完整官方登录）。
func (s *renderSlot) get(ctx context.Context) (Renderer, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	snap := s.creds.Snapshot()
	if s.r != nil && s.gen == snap.Generation {
		return s.r, nil
	}
	if s.r != nil {
		s.log("渲染 Worker %d：凭证代数 %d→%d，重启并重新官方登录", s.idx, s.gen, snap.Generation)
		s.closeLocked()
	}
	r, err := s.factory(ctx, s.idx, snap)
	if err != nil {
		return nil, fmt.Errorf("渲染 Worker %d 启动失败: %w", s.idx, err)
	}
	s.r, s.gen = r, snap.Generation
	return s.r, nil
}

// replace 渲染出错后替换 worker（原 worker 关闭，下次 get 重建）。
func (s *renderSlot) replace() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.r != nil {
		s.log("渲染 Worker %d：渲染出错，替换 worker", s.idx)
		s.closeLocked()
	}
}

func (s *renderSlot) close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closeLocked()
}

func (s *renderSlot) closeLocked() {
	if s.r != nil {
		_ = s.r.Close()
		s.r = nil
		s.gen = -1
	}
}

// renderPool 渲染 worker 池：固定 RenderWorkers 个独立浏览器 worker 并行渲染，
// 取消/收尾时 Close 全部。
type renderPool struct {
	slots []*renderSlot
}

func newRenderPool(n int, factory RendererFactory, creds CredsSource, log func(string, ...any)) *renderPool {
	p := &renderPool{}
	for i := 0; i < n; i++ {
		p.slots = append(p.slots, newRenderSlot(i, factory, creds, log))
	}
	return p
}

// close 取消/收尾：关闭全部 worker。
func (p *renderPool) close() {
	var wg sync.WaitGroup
	for _, s := range p.slots {
		wg.Add(1)
		go func(s *renderSlot) {
			defer wg.Done()
			s.close()
		}(s)
	}
	wg.Wait()
}

// workerLoop 渲染 worker 主循环（每个 slot 一个协程）：消费 jobs 下标，
// work 产出结果写 results[i]，随后调 onDone(i) 发完成信号（事件按序流出用）；
// 渲染类失败（shouldReplace）后替换 worker。
func workerLoop[T any](ctx context.Context, s *renderSlot, jobs <-chan int, results []T,
	work func(ctx context.Context, s *renderSlot, i int) T, shouldReplace func(T) bool, onDone func(i int),
) {
	for i := range jobs {
		res := work(ctx, s, i)
		if shouldReplace(res) && ctx.Err() == nil {
			s.replace()
		}
		results[i] = res
		onDone(i)
	}
}
