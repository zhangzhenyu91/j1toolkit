package logging

import (
	"bytes"
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"shade/internal/config"
)

// ---- 队列专用假驱动（纯标准库；可阻塞、可注入错误/panic，不连真实库）----

type queueFakeDB struct {
	mu      sync.Mutex
	calls   []fakeCall
	execErr error
	gate    chan struct{} // 非 nil 时 ExecContext 阻塞至 gate 关闭
	started chan struct{} // 非 nil 时每次进入 ExecContext 非阻塞通知
	panicN  int           // 前 panicN 次调用 panic
	panics  int
}

func (d *queueFakeDB) block(gate, started chan struct{}) {
	d.mu.Lock()
	d.gate, d.started = gate, started
	d.mu.Unlock()
}

func (d *queueFakeDB) setExecErr(err error) {
	d.mu.Lock()
	d.execErr = err
	d.mu.Unlock()
}

func (d *queueFakeDB) panicFirst(n int) {
	d.mu.Lock()
	d.panicN = n
	d.mu.Unlock()
}

// inserts 取全部 app_log 写入记录。
func (d *queueFakeDB) inserts() []fakeCall {
	d.mu.Lock()
	defer d.mu.Unlock()
	var out []fakeCall
	for _, c := range d.calls {
		if strings.Contains(c.query, "INSERT INTO app_log") {
			out = append(out, c)
		}
	}
	return out
}

type queueConn struct{ db *queueFakeDB }

func (c *queueConn) Prepare(string) (driver.Stmt, error) { return nil, errors.New("unsupported") }
func (c *queueConn) Close() error                        { return nil }
func (c *queueConn) Begin() (driver.Tx, error)           { return nil, errors.New("unsupported") }

func (c *queueConn) ExecContext(_ context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	vals := make([]driver.Value, len(args))
	for i, a := range args {
		vals[i] = a.Value
	}
	c.db.mu.Lock()
	c.db.calls = append(c.db.calls, fakeCall{query: query, args: vals})
	gate, started := c.db.gate, c.db.started
	err := c.db.execErr
	doPanic := c.db.panics < c.db.panicN
	if doPanic {
		c.db.panics++
	}
	c.db.mu.Unlock()
	if started != nil {
		select {
		case started <- struct{}{}:
		default:
		}
	}
	if gate != nil {
		<-gate
	}
	if doPanic {
		panic("fake driver panic")
	}
	if err != nil {
		return nil, err
	}
	return fakeResult(1), nil
}

type queueDriver struct{ db *queueFakeDB }

func (d queueDriver) Open(string) (driver.Conn, error) { return &queueConn{db: d.db}, nil }

var queueSeq int

func newQueueFakeDB(t *testing.T) (*sql.DB, *queueFakeDB) {
	t.Helper()
	f := &queueFakeDB{}
	queueSeq++
	name := fmt.Sprintf("logging-queue-fake-%d", queueSeq)
	sql.Register(name, queueDriver{db: f})
	db, err := sql.Open(name, "")
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db, f
}

// queueTestEntry 构造测试日志条目。
func queueTestEntry(level, message string) config.AppLogEntry {
	return config.AppLogEntry{
		AppCode: "quiz", Level: level, Category: "business",
		Event: "ev.test", Message: message, MessageCN: message,
	}
}

// gatePair 返回可重复释放的 gate 与一键释放函数。
func gatePair() (chan struct{}, chan struct{}, func()) {
	gate, started := make(chan struct{}), make(chan struct{}, 8)
	var once sync.Once
	release := func() { once.Do(func() { close(gate) }) }
	return gate, started, release
}

func flushTimeout(t *testing.T, q *Queue) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := q.Flush(ctx); err != nil {
		t.Fatalf("Flush: %v", err)
	}
}

// ---- 异步入队 / Flush ----

func TestQueueWritesAsynchronouslyAndFlushes(t *testing.T) {
	db, fake := newQueueFakeDB(t)
	gate, started, release := gatePair()
	fake.block(gate, started)
	q := newQueue(db, 8, 1, io.Discard)
	defer func() { release(); _ = q.Close() }()

	ctx := WithTraceID(context.Background(), "trace-queue")
	done := make(chan error, 1)
	go func() { done <- q.Write(ctx, queueTestEntry("info", "异步写入")) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Write: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Write 阻塞：写入未异步入队")
	}
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("worker 未开始落库")
	}
	if got := q.Dropped(); got != 0 {
		t.Fatalf("未满队丢弃计数 = %d", got)
	}
	release()
	flushTimeout(t, q)
	rows := fake.inserts()
	if len(rows) != 1 {
		t.Fatalf("Flush 后写入 %d 条，期望 1 条", len(rows))
	}
	if !strings.Contains(argsText(rows[0].args), "trace-queue") {
		t.Fatalf("入队时未固化 trace：%s", argsText(rows[0].args))
	}
	if got := q.pendingCount(); got != 0 {
		t.Fatalf("Flush 后 pending = %d", got)
	}
}

// ---- 满队降级 ----

func TestQueueDropsInfoWhenFullWithoutBlocking(t *testing.T) {
	db, fake := newQueueFakeDB(t)
	gate, started, release := gatePair()
	fake.block(gate, started)
	q := newQueue(db, 1, 1, io.Discard)
	defer func() { release(); _ = q.Close() }()

	ctx := context.Background()
	if err := q.Write(ctx, queueTestEntry("info", "first")); err != nil {
		t.Fatalf("Write first: %v", err)
	}
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("worker 未开始落库")
	}
	if err := q.Write(ctx, queueTestEntry("info", "second")); err != nil {
		t.Fatalf("Write second: %v", err)
	}
	start := time.Now()
	err := q.Write(ctx, queueTestEntry("info", "third"))
	elapsed := time.Since(start)
	if !errors.Is(err, ErrQueueFull) {
		t.Fatalf("满队 info 应返回 ErrQueueFull，得到 %v", err)
	}
	if elapsed > 50*time.Millisecond {
		t.Fatalf("满队 info 丢弃阻塞了 %v", elapsed)
	}
	if got := q.Dropped(); got != 1 {
		t.Fatalf("丢弃计数 = %d，期望 1", got)
	}
	release()
	flushTimeout(t, q)
	if n := len(fake.inserts()); n != 2 {
		t.Fatalf("写入 %d 条，期望 2（第三条被丢弃）", n)
	}
}

func TestQueueWarnFallsBackToStderrWhenFull(t *testing.T) {
	for _, level := range []string{"warn", "error"} {
		t.Run(level, func(t *testing.T) {
			db, fake := newQueueFakeDB(t)
			gate, started, release := gatePair()
			fake.block(gate, started)
			stderr := &bytes.Buffer{}
			q := newQueue(db, 1, 1, stderr)
			defer func() { release(); _ = q.Close() }()

			ctx := context.Background()
			if err := q.Write(ctx, queueTestEntry("info", "first")); err != nil {
				t.Fatalf("Write first: %v", err)
			}
			select {
			case <-started:
			case <-time.After(2 * time.Second):
				t.Fatal("worker 未开始落库")
			}
			if err := q.Write(ctx, queueTestEntry("info", "second")); err != nil {
				t.Fatalf("Write second: %v", err)
			}
			start := time.Now()
			err := q.Write(ctx, queueTestEntry(level, "队列告警"))
			elapsed := time.Since(start)
			if err != nil {
				t.Fatalf("%s 转 stderr 不应返回错误：%v", level, err)
			}
			if elapsed < warnEnqueueTimeout {
				t.Fatalf("%s 应等待 %v 再转 stderr，实际 %v", level, warnEnqueueTimeout, elapsed)
			}
			text := stderr.String()
			for _, want := range []string{level, "quiz", "ev.test", "队列告警"} {
				if !strings.Contains(text, want) {
					t.Fatalf("stderr 缺 %q：%s", want, text)
				}
			}
			release()
			flushTimeout(t, q)
			if n := len(fake.inserts()); n != 2 {
				t.Fatalf("写入 %d 条，期望 2（%s 转 stderr 未落库）", n, level)
			}
		})
	}
}

// ---- 失败重试 / panic 隔离 ----

func TestQueueRetriesOnceAfterWriteFailure(t *testing.T) {
	db, fake := newQueueFakeDB(t)
	fake.setExecErr(errors.New("db down"))
	stderr := &bytes.Buffer{}
	q := newQueue(db, 4, 1, stderr)
	defer func() { _ = q.Close() }()

	if err := q.Write(context.Background(), queueTestEntry("info", "重试")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	flushTimeout(t, q)
	rows := fake.inserts()
	if len(rows) != 2 {
		t.Fatalf("失败应重试一次，实际尝试 %d 次", len(rows))
	}
	if !strings.Contains(stderr.String(), "db down") {
		t.Fatalf("重试仍失败未落 stderr：%s", stderr.String())
	}
}

func TestQueueRecoversFromWriterPanic(t *testing.T) {
	db, fake := newQueueFakeDB(t)
	fake.panicFirst(1)
	stderr := &bytes.Buffer{}
	q := newQueue(db, 4, 1, stderr)
	defer func() { _ = q.Close() }()

	ctx := context.Background()
	if err := q.Write(ctx, queueTestEntry("info", "会 panic")); err != nil {
		t.Fatalf("Write panic entry: %v", err)
	}
	flushTimeout(t, q)
	if err := q.Write(ctx, queueTestEntry("info", "panic 之后仍可写")); err != nil {
		t.Fatalf("Write after panic: %v", err)
	}
	flushTimeout(t, q)

	var all strings.Builder
	for _, row := range fake.inserts() {
		all.WriteString(argsText(row.args))
		all.WriteString("\n")
	}
	if !strings.Contains(all.String(), "panic 之后仍可写") {
		t.Fatalf("panic 后 worker 失效：%s", all.String())
	}
	if !strings.Contains(stderr.String(), "panic") {
		t.Fatalf("panic 未上报 stderr：%s", stderr.String())
	}
}

// ---- Close 冲刷 / 幂等 / 并发安全 ----

func TestQueueCloseFlushesAndIsIdempotent(t *testing.T) {
	db, fake := newQueueFakeDB(t)
	q := newQueue(db, 64, 4, io.Discard)
	for i := 0; i < 20; i++ {
		if err := q.Write(context.Background(), queueTestEntry("info", fmt.Sprintf("关闭冲刷 %d", i))); err != nil {
			t.Fatalf("Write %d: %v", i, err)
		}
	}
	if err := q.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if n := len(fake.inserts()); n != 20 {
		t.Fatalf("Close 应冲刷全部条目，实际写入 %d 条", n)
	}
	if err := q.Close(); err != nil {
		t.Fatalf("重复 Close 应幂等：%v", err)
	}
	if err := q.Write(context.Background(), queueTestEntry("info", "关闭后")); !errors.Is(err, ErrQueueClosed) {
		t.Fatalf("关闭后写入应返回 ErrQueueClosed，得到 %v", err)
	}
	if n := len(fake.inserts()); n != 20 {
		t.Fatalf("关闭后仍写入 %d 条", n)
	}
}

func TestQueueCloseConcurrentWithWrites(t *testing.T) {
	db, fake := newQueueFakeDB(t)
	q := newQueue(db, 16, 4, io.Discard)

	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				err := q.Write(context.Background(), queueTestEntry("info", fmt.Sprintf("并发 %d-%d", id, j)))
				if err != nil && !errors.Is(err, ErrQueueClosed) && !errors.Is(err, ErrQueueFull) {
					t.Errorf("写入返回意外错误: %v", err)
					return
				}
			}
		}(i)
	}
	time.Sleep(2 * time.Millisecond)
	if err := q.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	wg.Wait()
	if n := len(fake.inserts()); n > 400 {
		t.Fatalf("写入超出上限：%d", n)
	}
	if err := q.Write(context.Background(), queueTestEntry("info", "after close")); !errors.Is(err, ErrQueueClosed) {
		t.Fatalf("关闭后写入应返回 ErrQueueClosed，得到 %v", err)
	}
}

// ---- 取消的请求 ctx 不影响落库 ----

func TestQueuePersistsWithCanceledRequestContext(t *testing.T) {
	db, fake := newQueueFakeDB(t)
	q := newQueue(db, 4, 1, io.Discard)
	defer func() { _ = q.Close() }()

	ctx, cancel := context.WithCancel(WithTraceID(context.Background(), "trace-canceled"))
	cancel()
	if err := q.Write(ctx, queueTestEntry("info", "取消后仍落库")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	flushTimeout(t, q)
	rows := fake.inserts()
	if len(rows) != 1 {
		t.Fatalf("取消 ctx 后写入 %d 条，期望 1 条", len(rows))
	}
	if !strings.Contains(argsText(rows[0].args), "trace-canceled") {
		t.Fatalf("trace 未固化：%s", argsText(rows[0].args))
	}
}

// ---- 默认装配与 Sink 抽象 ----

func TestNewQueueDefaults(t *testing.T) {
	db, _ := newQueueFakeDB(t)
	q := NewQueue(db)
	defer func() { _ = q.Close() }()
	if got := cap(q.ch); got != defaultQueueCapacity {
		t.Fatalf("队列容量 = %d，期望 %d", got, defaultQueueCapacity)
	}
	if got := q.workers; got != defaultQueueWorkers {
		t.Fatalf("worker 数 = %d，期望 %d", got, defaultQueueWorkers)
	}
	if defaultQueueCapacity != 8192 || defaultQueueWorkers != 4 {
		t.Fatalf("默认参数被改动：容量 %d、worker %d", defaultQueueCapacity, defaultQueueWorkers)
	}
}

func TestNewQueueNilDB(t *testing.T) {
	q := NewQueue(nil)
	defer func() { _ = q.Close() }()
	if err := q.Write(context.Background(), queueTestEntry("info", "无库")); err == nil {
		t.Fatal("nil 数据库应报错")
	}
	var nilQueue *Queue
	if err := nilQueue.Write(context.Background(), queueTestEntry("info", "nil")); err == nil {
		t.Fatal("nil Queue 应报错")
	}
	if err := nilQueue.Flush(context.Background()); err != nil {
		t.Fatalf("nil Queue Flush: %v", err)
	}
	if err := nilQueue.Close(); err != nil {
		t.Fatalf("nil Queue Close: %v", err)
	}
}

// captureSink 测试用 Sink：按真实 Sink 语义从 ctx 补 trace_id 后记录。
type captureSink struct {
	mu      sync.Mutex
	entries []config.AppLogEntry
}

func (s *captureSink) Write(ctx context.Context, e config.AppLogEntry) error {
	if e.TraceID == "" {
		e.TraceID = TraceIDFromContext(ctx)
	}
	s.mu.Lock()
	s.entries = append(s.entries, e)
	s.mu.Unlock()
	return nil
}

func (s *captureSink) all() []config.AppLogEntry {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]config.AppLogEntry, len(s.entries))
	copy(out, s.entries)
	return out
}

func TestNewWithSinkUsesInjectedSink(t *testing.T) {
	sink := &captureSink{}
	l := NewWithSink(sink, "quiz")
	ctx := WithTraceID(context.Background(), "trace-sink")
	if err := l.Info(ctx, "", "", "", "原始消息", map[string]any{"n": 1}); err != nil {
		t.Fatalf("Info: %v", err)
	}
	entries := sink.all()
	if len(entries) != 1 {
		t.Fatalf("Sink 收到 %d 条，期望 1 条", len(entries))
	}
	e := entries[0]
	if e.AppCode != "quiz" || e.Category != "business" || e.Event != "runtime.event" {
		t.Fatalf("条目默认值未归一：%+v", e)
	}
	if e.Message != "原始消息" || e.MessageCN != "原始消息" {
		t.Fatalf("中文说明/原文 = %q/%q", e.MessageCN, e.Message)
	}
	if e.TraceID != "trace-sink" {
		t.Fatalf("trace_id = %q", e.TraceID)
	}
	if !strings.Contains(e.Detail, `"n":1`) {
		t.Fatalf("fields 未序列化进 detail：%q", e.Detail)
	}
	if err := NewWithSink(nil, "quiz").Record(context.Background(), "info", "", "", "", "x", nil); err == nil {
		t.Fatal("nil Sink 应报错")
	}
}

func TestDirectSinkWritesAndImplementsSink(t *testing.T) {
	db, fake := newQueueFakeDB(t)
	var _ Sink = DirectSink{}
	var _ Sink = (*Queue)(nil)

	ctx := WithTraceID(context.Background(), "trace-direct")
	if err := (DirectSink{DB: db}).Write(ctx, config.AppLogEntry{AppCode: "x", Level: "info", Event: "ev", Message: "直写"}); err != nil {
		t.Fatalf("DirectSink.Write: %v", err)
	}
	rows := fake.inserts()
	if len(rows) != 1 {
		t.Fatalf("直写 %d 条，期望 1 条", len(rows))
	}
	if !strings.Contains(argsText(rows[0].args), "trace-direct") {
		t.Fatalf("直写未补 trace：%s", argsText(rows[0].args))
	}
	if err := (DirectSink{}).Write(context.Background(), config.AppLogEntry{}); err == nil {
		t.Fatal("nil 数据库应报错")
	}
}

// ---- 关闭预算（CloseContext）----

func TestQueueCloseContextDrainsWithinBudget(t *testing.T) {
	db, fake := newQueueFakeDB(t)
	q := newQueue(db, 64, 4, io.Discard)
	for i := 0; i < 10; i++ {
		if err := q.Write(context.Background(), queueTestEntry("info", fmt.Sprintf("预算内排空 %d", i))); err != nil {
			t.Fatalf("Write %d: %v", i, err)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := q.CloseContext(ctx); err != nil {
		t.Fatalf("CloseContext: %v", err)
	}
	if n := len(fake.inserts()); n != 10 {
		t.Fatalf("CloseContext 应冲刷全部条目，实际 %d 条", n)
	}
	if err := q.CloseContext(context.Background()); err != nil {
		t.Fatalf("重复 CloseContext 应幂等：%v", err)
	}
	if err := q.Close(); err != nil {
		t.Fatalf("CloseContext 后 Close 应幂等：%v", err)
	}
}

func TestQueueCloseContextTimesOutWithoutKillingWorkers(t *testing.T) {
	db, fake := newQueueFakeDB(t)
	gate, started, release := gatePair()
	fake.block(gate, started)
	q := newQueue(db, 8, 1, io.Discard)
	defer release()

	if err := q.Write(context.Background(), queueTestEntry("info", "阻塞落库")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("worker 未开始落库")
	}

	budget := 50 * time.Millisecond
	ctx, cancel := context.WithTimeout(context.Background(), budget)
	defer cancel()
	start := time.Now()
	err := q.CloseContext(ctx)
	elapsed := time.Since(start)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("CloseContext = %v，期望 DeadlineExceeded", err)
	}
	if elapsed < budget || elapsed > 2*time.Second {
		t.Fatalf("CloseContext 耗时 %v，期望约 %v", elapsed, budget)
	}
	// 已关闭：拒绝新写入，但 worker 未被强制杀死。
	if err := q.Write(context.Background(), queueTestEntry("info", "关闭后")); !errors.Is(err, ErrQueueClosed) {
		t.Fatalf("关闭后写入应返回 ErrQueueClosed，得到 %v", err)
	}

	// 释放 DB 后，后续 Close 仍能等待剩余条目排空。
	release()
	done := make(chan error, 1)
	go func() { done <- q.Close() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("后续 Close: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("释放 DB 后 Close 未返回")
	}
	if n := len(fake.inserts()); n != 1 {
		t.Fatalf("排空后写入 %d 条，期望 1 条", n)
	}
}

func TestQueueCloseContextUnderPersistentDBFailure(t *testing.T) {
	db, fake := newQueueFakeDB(t)
	fake.setExecErr(errors.New("db down"))
	stderr := &bytes.Buffer{}
	q := newQueue(db, 256, 4, stderr)
	for i := 0; i < 100; i++ {
		if err := q.Write(context.Background(), queueTestEntry("info", fmt.Sprintf("故障 %d", i))); err != nil {
			t.Fatalf("Write %d: %v", i, err)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	start := time.Now()
	if err := q.CloseContext(ctx); err != nil {
		t.Fatalf("持续 DB 故障时 CloseContext 应在预算内完成（不无限重试）：%v", err)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("CloseContext 超出预算：%v", elapsed)
	}
	if n := len(fake.inserts()); n != 200 {
		t.Fatalf("写入尝试 %d 次，期望 200（每条约 2 次）", n)
	}
	if !strings.Contains(stderr.String(), "db down") {
		t.Fatalf("持续故障未上报 stderr：%s", stderr.String())
	}
}

// ---- Record 对满队丢弃不报错 ----

// errSink 固定返回指定错误的 Sink。
type errSink struct{ err error }

func (s errSink) Write(context.Context, config.AppLogEntry) error { return s.err }

func TestLoggerRecordSwallowsQueueFull(t *testing.T) {
	l := NewWithSink(errSink{err: ErrQueueFull}, "quiz")
	// info 满队属有意降级：不应向业务模块报错（避免其再放大到 stderr）。
	if err := l.Info(context.Background(), "business", "ev", "中文", "raw", nil); err != nil {
		t.Fatalf("info 满队丢弃应返回 nil，得到 %v", err)
	}
	if err := l.Warn(context.Background(), "business", "ev", "中文", "raw", nil); err != nil {
		t.Fatalf("warn 满队（stderr 兜底）应返回 nil，得到 %v", err)
	}
	if err := l.Error(context.Background(), "business", "ev", "中文", "raw", nil); err != nil {
		t.Fatalf("error 满队（stderr 兜底）应返回 nil，得到 %v", err)
	}
}

// TestQueueCloseLogsDroppedCount 关闭完成/超时时用标准 log 输出满队丢弃计数（非 0 才输出）。
func TestQueueCloseLogsDroppedCount(t *testing.T) {
	t.Run("完成", func(t *testing.T) {
		db, fake := newQueueFakeDB(t)
		gate, started, release := gatePair()
		fake.block(gate, started)
		var buf bytes.Buffer
		q := newQueue(db, 1, 1, &buf)
		defer func() { release(); _ = q.Close() }()
		writeDroppedQueue(t, q, started)
		release()
		if err := q.CloseContext(context.Background()); err != nil {
			t.Fatalf("CloseContext: %v", err)
		}
		out := buf.String()
		if !strings.Contains(out, "丢弃") || !strings.Contains(out, "1") {
			t.Fatalf("关闭完成未输出丢弃计数: %q", out)
		}
	})

	t.Run("超时", func(t *testing.T) {
		db, fake := newQueueFakeDB(t)
		gate, started, release := gatePair()
		fake.block(gate, started)
		var buf bytes.Buffer
		q := newQueue(db, 1, 1, &buf)
		defer func() { release(); _ = q.Close() }()
		writeDroppedQueue(t, q, started)
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
		defer cancel()
		if err := q.CloseContext(ctx); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("CloseContext 应超时，得到 %v", err)
		}
		out := buf.String()
		if !strings.Contains(out, "丢弃") || !strings.Contains(out, "1") {
			t.Fatalf("关闭超时未输出丢弃计数: %q", out)
		}
	})
}

// writeDroppedQueue 制造 1 条阻塞、1 条入队、1 条满队丢弃。
func writeDroppedQueue(t *testing.T, q *Queue, started <-chan struct{}) {
	t.Helper()
	ctx := context.Background()
	if err := q.Write(ctx, queueTestEntry("info", "阻塞")); err != nil {
		t.Fatalf("Write 阻塞条目: %v", err)
	}
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("worker 未开始落库")
	}
	if err := q.Write(ctx, queueTestEntry("info", "入队")); err != nil {
		t.Fatalf("Write 入队条目: %v", err)
	}
	if err := q.Write(ctx, queueTestEntry("info", "丢弃")); !errors.Is(err, ErrQueueFull) {
		t.Fatalf("满队 info 应返回 ErrQueueFull，得到 %v", err)
	}
	if got := q.Dropped(); got != 1 {
		t.Fatalf("丢弃计数 = %d，期望 1", got)
	}
}

func TestLoggerRecordPropagatesSinkErrors(t *testing.T) {
	boom := errors.New("boom")
	l := NewWithSink(errSink{err: boom}, "quiz")
	if err := l.Info(context.Background(), "", "", "", "raw", nil); !errors.Is(err, boom) {
		t.Fatalf("普通写入错误应上抛，得到 %v", err)
	}
}

func TestLoggerInfoDropDoesNotReachStderr(t *testing.T) {
	db, fake := newQueueFakeDB(t)
	gate, started, release := gatePair()
	fake.block(gate, started)
	stderr := &bytes.Buffer{}
	q := newQueue(db, 1, 1, stderr)
	l := NewWithSink(q, "quiz")
	defer func() { release(); _ = q.Close() }()

	if err := l.Info(context.Background(), "business", "ev", "阻塞", "阻塞", nil); err != nil {
		t.Fatalf("Info 阻塞条目: %v", err)
	}
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("worker 未开始落库")
	}
	if err := l.Info(context.Background(), "business", "ev", "填满", "填满", nil); err != nil {
		t.Fatalf("Info 填满队列: %v", err)
	}
	if err := l.Info(context.Background(), "business", "ev", "丢弃", "丢弃", nil); err != nil {
		t.Fatalf("info 满队丢弃应返回 nil：%v", err)
	}
	if stderr.Len() != 0 {
		t.Fatalf("info 丢弃不应写 stderr：%s", stderr.String())
	}
	if got := q.Dropped(); got != 1 {
		t.Fatalf("丢弃计数 = %d，期望 1", got)
	}
}

// ---- stderr 并发写 ----

func TestQueueConcurrentStderrWritesAreSerialized(t *testing.T) {
	db, fake := newQueueFakeDB(t)
	gate, started, release := gatePair()
	fake.block(gate, started)
	stderr := &bytes.Buffer{}
	q := newQueue(db, 1, 1, stderr)
	defer func() { release(); _ = q.Close() }()

	if err := q.Write(context.Background(), queueTestEntry("info", "阻塞")); err != nil {
		t.Fatalf("Write 阻塞条目: %v", err)
	}
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("worker 未开始落库")
	}
	if err := q.Write(context.Background(), queueTestEntry("info", "填满")); err != nil {
		t.Fatalf("Write 填满队列: %v", err)
	}

	const n = 8
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_ = q.Write(context.Background(), queueTestEntry("warn", fmt.Sprintf("并发告警 %d", i)))
		}(i)
	}
	wg.Wait()

	text := strings.TrimRight(stderr.String(), "\n")
	lines := strings.Split(text, "\n")
	if len(lines) != n {
		t.Fatalf("stderr 行数 = %d，期望 %d：%s", len(lines), n, text)
	}
	seen := map[string]bool{}
	for _, line := range lines {
		if !strings.HasPrefix(line, "[logging] ") {
			t.Fatalf("stderr 行被并发写破坏：%q", line)
		}
		start := strings.Index(line, `message="`)
		if start < 0 || !strings.HasSuffix(line, `"`) {
			t.Fatalf("stderr 行被并发写破坏：%q", line)
		}
		seen[line[start:]] = true
	}
	if len(seen) != n {
		t.Fatalf("并发告警条目丢失或重复：%d/%d", len(seen), n)
	}
}
