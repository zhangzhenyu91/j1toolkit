// 有界异步日志队列（兼容保留，生产装配已改用文件优先的 DurableSink；
// 入库只由 DurableSink 的唯一 Importer 负责，本队列不再位于主写入路径）：
// 容量 8192、4 writer 并发落库，吸收高频 info 访问日志；
// info 满队非阻塞丢弃，warn/error 满队等待 20ms 仍无槽位则转 stderr（关键日志不丢）；
// 写库失败重试一次，单条 panic 隔离不拖垮 worker，Close 冲刷已入队条目且幂等。
// 请求 ctx 取消不影响落库（worker 用脱离请求的后台 ctx），trace_id 在入队时固化。
package logging

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"shade/internal/config"
)

const (
	// defaultQueueCapacity 队列有界容量（条目数）。
	defaultQueueCapacity = 8192
	// defaultQueueWorkers 并发写库 worker 数。
	defaultQueueWorkers = 4
	// warnEnqueueTimeout warn/error 满队时的兜底等待：等不到槽位即转 stderr。
	warnEnqueueTimeout = 20 * time.Millisecond
	// queueWriteTimeout 单次落库超时（失败重试一次，各自独立计时）。
	queueWriteTimeout = 5 * time.Second
)

var (
	// ErrQueueFull 队列已满且条目已按级别降级（info 丢弃）。
	ErrQueueFull = errors.New("logging: 日志队列已满，已丢弃")
	// ErrQueueClosed 队列已关闭，不再接受写入（关闭顺序：先停生产者，再 Close 队列，最后关库）。
	ErrQueueClosed = errors.New("logging: 日志队列已关闭")
)

// Queue 有界异步日志写入队列；实现 Sink，生产各应用 logger、integrations recorder
// 与 HTTP 访问日志共用同一实例。
type Queue struct {
	db      *sql.DB
	ch      chan config.AppLogEntry
	stop    chan struct{}
	workers int
	stderr  io.Writer

	mu     sync.RWMutex
	closed bool

	// stderrMu 串行化降级/异常输出：stderr 可能是测试注入的 bytes.Buffer 等非并发安全 writer。
	stderrMu sync.Mutex

	closeOnce sync.Once
	done      chan struct{}
	wg        sync.WaitGroup
	pending   atomic.Int64
	dropped   atomic.Int64
}

// NewQueue 构造生产队列（容量 8192、4 writer、降级与异常信息写 os.Stderr）。
func NewQueue(db *sql.DB) *Queue {
	return newQueue(db, defaultQueueCapacity, defaultQueueWorkers, os.Stderr)
}

// newQueue 可注入容量/worker/stderr 的构造（测试与小容量场景）。
func newQueue(db *sql.DB, capacity, workers int, stderr io.Writer) *Queue {
	if capacity <= 0 {
		capacity = defaultQueueCapacity
	}
	if workers <= 0 {
		workers = defaultQueueWorkers
	}
	q := &Queue{
		db:      db,
		ch:      make(chan config.AppLogEntry, capacity),
		stop:    make(chan struct{}),
		workers: workers,
		stderr:  stderr,
	}
	q.wg.Add(workers)
	for i := 0; i < workers; i++ {
		go q.run()
	}
	return q
}

// Write 异步入队（非阻塞）：info 满队直接丢弃并返回 ErrQueueFull；
// warn/error 满队最多等 warnEnqueueTimeout，仍无槽位转 stderr（返回 nil，事件未丢）。
// trace_id 在此固化，之后请求 ctx 取消不影响落库。
func (q *Queue) Write(ctx context.Context, e config.AppLogEntry) error {
	if q == nil || q.db == nil {
		return ErrNoDatabase
	}
	if e.TraceID == "" {
		e.TraceID = TraceIDFromContext(ctx)
	}
	fillActor(&e, ctx)
	sanitizeEntry(&e)
	q.pending.Add(1)
	// 持读锁完成入队，与 Close 的写锁互斥：通道永不关闭，避免 send on closed channel。
	q.mu.RLock()
	if q.closed {
		q.mu.RUnlock()
		q.pending.Add(-1)
		return ErrQueueClosed
	}
	select {
	case q.ch <- e:
		q.mu.RUnlock()
		return nil
	default:
	}
	if !urgentLevel(e.Level) {
		q.mu.RUnlock()
		q.pending.Add(-1)
		q.dropped.Add(1)
		return ErrQueueFull
	}
	timer := time.NewTimer(warnEnqueueTimeout)
	defer timer.Stop()
	select {
	case q.ch <- e:
		q.mu.RUnlock()
		return nil
	case <-timer.C:
	}
	q.mu.RUnlock()
	q.pending.Add(-1)
	q.reportStderr("队列已满，warn/error 转 stderr", e, "")
	return nil
}

// Flush 等待当前已入队（pending）条目落库，不关闭队列、不阻塞新写入；ctx 到期返回其错误。
// 实际语义是「观察到 pending 为 0 即返回」：并发新写入可能延长等待，也可能在两次观察之间
// 返回，因此 Flush 不是写入屏障。需要「不再接收写入并最终排空」请用 Close / CloseContext。
func (q *Queue) Flush(ctx context.Context) error {
	if q == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	ticker := time.NewTicker(200 * time.Microsecond)
	defer ticker.Stop()
	for {
		if q.pending.Load() == 0 {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

// Close 停止接收新写入并等待已入队条目全部落库；可重复调用（幂等）。
// 无关闭预算（等同 CloseContext(context.Background())）；需要预算时用 CloseContext。
// 调用方须先停止生产者（scheduler / 微信 / HTTP 等）再 Close，最后关闭数据库。
func (q *Queue) Close() error {
	return q.CloseContext(context.Background())
}

// CloseContext 在 ctx 预算内关闭队列：停止接收新写入并等待已入队条目落库。
// 预算耗尽返回 ctx 错误（worker 不被强制终止，队列保持已关闭、不再接收写入）；
// 数据库仍可用时可再次调用 Close/CloseContext 继续等待排空（幂等）。关停流程若在超时后
// 继续 db.Close，剩余未落库条目会丢失——这是有界关停的明确取舍，只输出丢弃告警。
func (q *Queue) CloseContext(ctx context.Context) error {
	if q == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	q.closeOnce.Do(func() {
		q.mu.Lock()
		q.closed = true
		close(q.stop)
		q.mu.Unlock()
		q.done = make(chan struct{})
		go func() {
			q.wg.Wait()
			close(q.done)
		}()
	})
	select {
	case <-q.done:
		q.logDropped("完成")
		return nil
	case <-ctx.Done():
		q.logDropped("超时")
		return ctx.Err()
	}
}

// logDropped 关闭完成/超时时输出满队丢弃计数（仅非 0 输出）。
// 写注入的 stderr（独立 writer），不回到标准库 log，避免 log→Queue→log 递归/死锁。
func (q *Queue) logDropped(reason string) {
	if n := q.Dropped(); n > 0 {
		w := q.stderr
		if w == nil {
			w = os.Stderr
		}
		q.stderrMu.Lock()
		_, _ = fmt.Fprintf(w, "[日志] 队列关闭%s，满队丢弃 info %d 条\n", reason, n)
		q.stderrMu.Unlock()
	}
}

// Dropped 返回因满队被丢弃的 info 条数（诊断用）。
func (q *Queue) Dropped() int64 {
	if q == nil {
		return 0
	}
	return q.dropped.Load()
}

// pendingCount 已入队未落库条目数（Flush 判定与测试探针）。
func (q *Queue) pendingCount() int64 {
	if q == nil {
		return 0
	}
	return q.pending.Load()
}

// run worker 主循环：正常取条目处理；收到停止信号后把通道剩余条目排空再退出。
func (q *Queue) run() {
	defer q.wg.Done()
	for {
		select {
		case e := <-q.ch:
			q.process(e)
		case <-q.stop:
			for {
				select {
				case e := <-q.ch:
					q.process(e)
				default:
					return
				}
			}
		}
	}
}

// process 落库单条并维护 pending；失败与 panic 只上报 stderr，不影响 worker。
func (q *Queue) process(e config.AppLogEntry) {
	defer q.pending.Add(-1)
	defer func() {
		if r := recover(); r != nil {
			q.reportStderr("写入 app_log panic", e, fmt.Sprint(r))
		}
	}()
	if err := q.persist(e); err != nil {
		q.reportStderr("写入 app_log 失败（已重试一次）", e, err.Error())
	}
}

// persist 用脱离请求的后台 ctx 落库：请求取消/结束不影响已入队日志；失败重试一次。
func (q *Queue) persist(e config.AppLogEntry) error {
	var err error
	for attempt := 0; attempt < 2; attempt++ {
		ctx, cancel := context.WithTimeout(context.Background(), queueWriteTimeout)
		err = config.InsertAppLog(ctx, q.db, e)
		cancel()
		if err == nil {
			return nil
		}
	}
	return err
}

// urgentLevel 关键级别：满队时不允许静默丢弃。
func urgentLevel(level string) bool {
	switch strings.ToLower(level) {
	case "warn", "warning", "error":
		return true
	}
	return false
}

// reportStderr 降级/异常信息写 stderr：加锁串行化（writer 可能非并发安全），
// 绝不阻塞业务，也不再入队（防止日志自激）。
func (q *Queue) reportStderr(reason string, e config.AppLogEntry, detail string) {
	q.stderrMu.Lock()
	defer q.stderrMu.Unlock()
	w := q.stderr
	if w == nil {
		w = os.Stderr
	}
	line := fmt.Sprintf("[logging] %s: app=%s level=%s event=%s trace=%s message=%q",
		reason, e.AppCode, e.Level, e.Event, e.TraceID, e.Message)
	if detail != "" {
		line += " detail=" + detail
	}
	fmt.Fprintln(w, line)
}
