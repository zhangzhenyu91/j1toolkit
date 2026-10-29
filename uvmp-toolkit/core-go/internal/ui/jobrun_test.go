package ui

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// JobRunner 接线冒烟：事件回放、取消、启动失败、并发任务路由。

func newRunnerForTest(t *testing.T, f *FakeBackend) *JobRunner {
	t.Helper()
	sub, err := f.Subscribe(context.Background())
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	r := NewJobRunner(f, sub)
	t.Cleanup(func() { _ = r.Close() })
	return r
}

func TestJobRunner_ReplaysEarlyEvents(t *testing.T) {
	f := NewFakeBackend()
	f.StepDelay = 0 // 任务可能在 RunJob 返回前就发完事件
	r := newRunnerForTest(t, f)

	var mu sync.Mutex
	var logs []string
	var last JobProgress
	var doneJob *Job
	var errSeen error

	h := JobHandler{
		OnLog: func(line string) {
			mu.Lock()
			logs = append(logs, line)
			mu.Unlock()
		},
		OnProgress: func(p JobProgress) {
			mu.Lock()
			last = p
			mu.Unlock()
		},
		OnDone: func(job Job) {
			mu.Lock()
			doneJob = &job
			mu.Unlock()
		},
		OnError: func(err error) {
			mu.Lock()
			errSeen = err
			mu.Unlock()
		},
	}
	handle := r.Start("daily_sync", map[string]any{}, h)

	select {
	case job := <-handle.Done():
		if job.Status != JobSuccess {
			t.Fatalf("终态不符: %+v", job)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("等待 Done 超时")
	}

	mu.Lock()
	defer mu.Unlock()
	if errSeen != nil {
		t.Fatalf("不应有启动错误: %v", errSeen)
	}
	if doneJob == nil || doneJob.ID != handle.ID() {
		t.Fatalf("OnDone 未触发或 id 不符: %+v id=%s", doneJob, handle.ID())
	}
	if len(logs) == 0 {
		t.Fatal("日志事件丢失（回放失败）")
	}
	if last.Total <= 0 {
		t.Fatalf("进度事件丢失: %+v", last)
	}
}

func TestJobRunner_Cancel(t *testing.T) {
	f := NewFakeBackend()
	f.StepDelay = 4 * time.Millisecond
	r := newRunnerForTest(t, f)

	doneCh := make(chan Job, 1)
	handle := r.Start("pcd_export", map[string]any{}, JobHandler{
		OnDone: func(job Job) { doneCh <- job },
	})
	handle.Cancel()

	select {
	case job := <-doneCh:
		if job.Status != JobCancelled {
			t.Fatalf("取消后状态不符: %+v", job)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("等待取消终态超时")
	}
}

func TestJobRunner_StartError(t *testing.T) {
	f := NewFakeBackend()
	f.FailNextRun = errors.New("核心未运行")
	r := newRunnerForTest(t, f)

	errCh := make(chan error, 1)
	handle := r.Start("daily_sync", map[string]any{}, JobHandler{
		OnError: func(err error) { errCh <- err },
		OnDone:  func(Job) { t.Error("启动失败不应触发 OnDone") },
	})
	select {
	case err := <-errCh:
		if err == nil {
			t.Fatal("应报启动错误")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("等待 OnError 超时")
	}
	select {
	case _, ok := <-handle.Done():
		if ok {
			t.Fatal("启动失败的 Done 不应投递任务")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Done 未关闭")
	}
}

func TestJobRunner_RoutesConcurrentJobs(t *testing.T) {
	f := NewFakeBackend()
	f.StepDelay = time.Millisecond
	r := newRunnerForTest(t, f)

	var mu sync.Mutex
	perJob := map[string]int{}
	h1 := JobHandler{
		OnLog: func(line string) {
			mu.Lock()
			perJob["a"]++
			mu.Unlock()
		},
	}
	h2 := JobHandler{
		OnLog: func(line string) {
			mu.Lock()
			perJob["b"]++
			mu.Unlock()
		},
	}
	ha := r.Start("daily_sync", map[string]any{}, h1)
	hb := r.Start("tree_sync", map[string]any{}, h2)
	if ha.ID() == "" || hb.ID() == "" {
		// id 异步到达，稍等
		time.Sleep(50 * time.Millisecond)
	}
	if ha.ID() == "" || hb.ID() == "" || ha.ID() == hb.ID() {
		t.Fatalf("任务 id 不符: %q %q", ha.ID(), hb.ID())
	}

	waitBothDone(t, ha, hb)
	mu.Lock()
	defer mu.Unlock()
	if perJob["a"] == 0 || perJob["b"] == 0 {
		t.Fatalf("事件路由不完整: %v", perJob)
	}
}

func waitBothDone(t *testing.T, handles ...*JobHandle) {
	t.Helper()
	for _, h := range handles {
		select {
		case <-h.Done():
		case <-time.After(3 * time.Second):
			t.Fatal("等待任务终态超时")
		}
	}
}
