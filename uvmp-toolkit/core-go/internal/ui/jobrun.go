package ui

import (
	"context"
	"errors"
	"sync"
)

// JobHandler 任务事件回调（在订阅事件协程上触发；UI 层须自行切回 UI 线程）。
// OnError 只报「启动失败」；任务终态一律走 OnDone。
type JobHandler struct {
	OnLog      func(line string)
	OnProgress func(p JobProgress)
	OnDone     func(job Job)
	OnError    func(err error)
}

// JobHandle 一次任务启动的句柄。
type JobHandle struct {
	w *jobWatch
}

// ID 任务 id（RunJob 响应到达后才可读；未知时返回空串）。
func (h *JobHandle) ID() string {
	h.w.mu.Lock()
	defer h.w.mu.Unlock()
	return h.w.id
}

// Done 返回终态通道：任务到终态时投递终态 Job 并关闭（OnDone 回调先于投递执行）；
// 启动失败或 runner 关闭时关闭且不投递。
func (h *JobHandle) Done() <-chan Job { return h.w.done }

// Cancel 取消任务（课程任务语义为暂停）。job_id 未到达时等待其到达后转调；
// 启动失败或已终态时为空操作。
func (h *JobHandle) Cancel() { h.w.runner.cancel(h.w, nil) }

// CancelNotify 同 Cancel，但取消请求失败（如任务档案不存在）时经 onErr 异步回报
// （独立协程回调，UI 层自行切线程）；onErr 可为 nil 等价 Cancel。
func (h *JobHandle) CancelNotify(onErr func(error)) { h.w.runner.cancel(h.w, onErr) }

// JobRunner 把 Backend 任务通道与事件流接线到回调。
// 语义与桌面壳 api.js runJob 对齐（内网工具箱开发指南 §4.3）：
//   - 快速失败的任务可能在 RunJob 响应到达前就发完事件——先缓冲，job_id 到达后按 id 回放；
//   - 同一 runner 可并发跑多个任务，事件按 job_id 路由；
//   - Close 幂等，之后事件不再分发（不取消任务本身）。
type JobRunner struct {
	backend Backend
	sub     Subscription

	mu      sync.Mutex
	watches map[*jobWatch]struct{}
	closed  bool
	wg      sync.WaitGroup
}

type jobWatch struct {
	runner  *JobRunner
	handler JobHandler

	mu       sync.Mutex
	id       string
	known    bool
	buffered []Event
	done     chan Job
	ended    bool
	ready    chan struct{} // 关闭表示 id 已到达或启动已失败
	readyOne sync.Once
}

// NewJobRunner 创建并启动事件分发协程。sub 由调用方提供（通常来自 Backend.Subscribe）。
func NewJobRunner(backend Backend, sub Subscription) *JobRunner {
	r := &JobRunner{
		backend: backend,
		sub:     sub,
		watches: map[*jobWatch]struct{}{},
	}
	r.wg.Add(1)
	go r.dispatch()
	return r
}

// Start 启动任务并返回句柄（非阻塞）。
func (r *JobRunner) Start(appID string, params map[string]any, h JobHandler) *JobHandle {
	w := &jobWatch{runner: r, handler: h, done: make(chan Job, 1), ready: make(chan struct{})}

	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		r.finishError(w, errors.New("JobRunner 已关闭"))
		return &JobHandle{w: w}
	}
	r.watches[w] = struct{}{}
	r.mu.Unlock()

	go func() {
		id, err := r.backend.RunJob(context.Background(), appID, params)
		if err != nil {
			r.finishError(w, err)
			return
		}
		w.mu.Lock()
		w.id = id
		w.known = true
		pending := w.buffered
		w.buffered = nil
		w.mu.Unlock()
		w.markReady()
		// 回放缓冲事件（含本任务先到的事件），route 内按 id 过滤
		for _, ev := range pending {
			r.route(ev)
		}
	}()
	return &JobHandle{w: w}
}

// Close 停止事件分发并释放句柄（不取消任务本身）。
func (r *JobRunner) Close() error {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return nil
	}
	r.closed = true
	r.mu.Unlock()
	_ = r.sub.Close()
	r.wg.Wait()
	return nil
}

func (r *JobRunner) dispatch() {
	defer r.wg.Done()
	for ev := range r.sub.Events() {
		r.route(ev)
	}
}

func (r *JobRunner) route(ev Event) {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return
	}
	watches := make([]*jobWatch, 0, len(r.watches))
	for w := range r.watches {
		watches = append(watches, w)
	}
	r.mu.Unlock()

	for _, w := range watches {
		w.mu.Lock()
		if w.ended {
			w.mu.Unlock()
			continue
		}
		if !w.known {
			// job_id 未到达：缓冲，待回放时按 id 过滤
			w.buffered = append(w.buffered, ev)
			w.mu.Unlock()
			continue
		}
		match := ev.JobID == w.id
		w.mu.Unlock()
		if match {
			r.deliver(w, ev)
		}
	}
}

func (r *JobRunner) deliver(w *jobWatch, ev Event) {
	h := w.handler
	switch ev.Kind {
	case EventJobLog:
		if h.OnLog != nil {
			h.OnLog(ev.Line)
		}
	case EventJobProgress:
		if h.OnProgress != nil && ev.Progress != nil {
			h.OnProgress(*ev.Progress)
		}
	case EventJobDone:
		if ev.Job != nil {
			r.finishDone(w, *ev.Job)
		}
	}
}

func (r *JobRunner) finishDone(w *jobWatch, job Job) {
	w.mu.Lock()
	if w.ended {
		w.mu.Unlock()
		return
	}
	w.ended = true
	w.mu.Unlock()

	w.markReady()
	r.remove(w)
	// OnDone 先于 Done 通道投递：Done 收到时终态回调已执行完，消费方可安全读回调侧状态
	if w.handler.OnDone != nil {
		w.handler.OnDone(job)
	}
	select {
	case w.done <- job:
	default:
	}
	close(w.done)
}

func (r *JobRunner) finishError(w *jobWatch, err error) {
	w.mu.Lock()
	if w.ended {
		w.mu.Unlock()
		return
	}
	w.ended = true
	w.mu.Unlock()

	w.markReady()
	r.remove(w)
	close(w.done)
	if w.handler.OnError != nil {
		w.handler.OnError(err)
	}
}

func (w *jobWatch) markReady() {
	w.readyOne.Do(func() { close(w.ready) })
}

func (r *JobRunner) remove(w *jobWatch) {
	r.mu.Lock()
	delete(r.watches, w)
	r.mu.Unlock()
}

func (r *JobRunner) cancel(w *jobWatch, onErr func(error)) {
	go func() {
		// 等 job_id 到达（或启动失败）后转调 CancelJob，与 api.js 的 cancelReady 同语义
		<-w.ready
		w.mu.Lock()
		id, known, ended := w.id, w.known, w.ended
		w.mu.Unlock()
		if ended || !known || id == "" {
			return
		}
		if err := r.backend.CancelJob(context.Background(), id); err != nil && onErr != nil {
			onErr(err)
		}
	}()
}
