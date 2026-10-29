package job

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zhangzhenyu91/j1toolkit/uvmp-toolkit/core-go/internal/state"
)

func newTestManager(t *testing.T) (*JobManager, *state.Store) {
	t.Helper()
	st := &state.Store{Root: t.TempDir()}
	return NewManager(st), st
}

func TestJobLogProgressAndFile(t *testing.T) {
	m, st := newTestManager(t)
	j := m.Create("daily_sync", "测试任务")
	var gotLines []string
	j.OnLog = func(line string) { gotLines = append(gotLines, line) }
	var midLines [][2]any
	var midMaxSeq int
	m.RunSync(j, func(jj *Job) (map[string]any, error) {
		jj.Log("第一行")
		jj.SetProgress(1, 4, "步骤一")
		jj.Log("第二行")
		// 运行中查询侧接口（终态收尾会清内存日志缓冲，与 Python 一致）
		midLines, midMaxSeq = jj.LogsSince(0)
		return nil, nil
	}, false)

	if midMaxSeq != 2 || len(midLines) != 2 {
		t.Fatalf("LogsSince = %d 条 / maxSeq %d", len(midLines), midMaxSeq)
	}
	if len(gotLines) != 2 || !strings.Contains(gotLines[0], "第一行") {
		t.Fatalf("OnLog 回调 = %v", gotLines)
	}
	p := j.Progress()
	if p["current"] != 1 || p["label"] != "步骤一" {
		t.Fatalf("Progress = %v", p)
	}
	// 日志文件全文
	data, err := os.ReadFile(filepath.Join(st.LogsDir(), j.ID+".log"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "第一行") || !strings.Contains(string(data), "第二行") {
		t.Fatalf("日志文件内容不全: %s", data)
	}
	// 偏移过滤（运行中）
	var tail [][2]any
	m2 := NewManager(st)
	j2 := m2.Create("daily_sync", "偏移")
	m2.RunSync(j2, func(jj *Job) (map[string]any, error) {
		jj.Log("a")
		jj.Log("b")
		tail, _ = jj.LogsSince(1)
		return nil, nil
	}, false)
	if len(tail) != 1 {
		t.Fatalf("LogsSince(1) = %d 条", len(tail))
	}
}

func TestCancelAndHooks(t *testing.T) {
	m, _ := newTestManager(t)
	j := m.Create("pcd_export", "取消测试")
	hookFired := 0
	j.AddCancelHook(func() { hookFired++ })
	if j.Cancelled() {
		t.Fatal("初始不应已取消")
	}
	if err := j.CheckCancel(); err != nil {
		t.Fatal("初始 CheckCancel 应通过")
	}
	j.Cancel()
	if !j.Cancelled() {
		t.Fatal("应已取消")
	}
	if hookFired != 1 {
		t.Fatalf("取消钩子触发 = %d, want 1", hookFired)
	}
	// 注册前已取消：立即补触发
	j.AddCancelHook(func() { hookFired++ })
	if hookFired != 2 {
		t.Fatalf("补触发 = %d, want 2", hookFired)
	}
	if err := j.CheckCancel(); !errors.Is(err, ErrCancelled) {
		t.Fatalf("CheckCancel = %v, want ErrCancelled", err)
	}
}

func TestRunSyncSuccessAndArchive(t *testing.T) {
	m, st := newTestManager(t)
	j := m.Create("daily_sync", "每日派车单同步")
	var doneCalled bool
	j.OnDone = func(jj *Job) { doneCalled = true }
	res := map[string]any{"count": 3}
	m.RunSync(j, func(jj *Job) (map[string]any, error) {
		jj.Log("干活")
		return res, nil
	}, false)

	if j.Status != StatusSuccess {
		t.Fatalf("status = %s, want success", j.Status)
	}
	if j.Result["count"] != 3 {
		t.Fatalf("result = %v", j.Result)
	}
	if !doneCalled {
		t.Fatal("OnDone 未调用")
	}
	if j.FinishedAt == "" {
		t.Fatal("finished_at 应已填")
	}
	// 任务档案（含 log_tail）
	summary := state.ReadJSONMap(filepath.Join(st.JobsDir(), j.ID+".json"))
	if summary == nil || summary["status"] != "success" {
		t.Fatalf("任务档案 = %v", summary)
	}
	tail, _ := summary["log_tail"].([]any)
	if len(tail) == 0 {
		t.Fatal("log_tail 应有内容")
	}
	// 历史
	runs := st.RecentRuns("daily_sync", 10)
	if len(runs) != 1 || runs[0]["job_id"] != j.ID {
		t.Fatalf("历史 = %v", runs)
	}
}

func TestRunSyncFailure(t *testing.T) {
	m, st := newTestManager(t)
	j := m.Create("daily_sync", "失败任务")
	m.RunSync(j, func(jj *Job) (map[string]any, error) {
		return nil, errors.New("U盘不可用")
	}, false)
	if j.Status != StatusFailed {
		t.Fatalf("status = %s, want failed", j.Status)
	}
	if j.Error != "U盘不可用" {
		t.Fatalf("error = %q", j.Error)
	}
	runs := st.RecentRuns("daily_sync", 10)
	if len(runs) != 1 || runs[0]["status"] != "failed" {
		t.Fatalf("历史 = %v", runs)
	}
}

func TestRunSyncCancelled(t *testing.T) {
	m, _ := newTestManager(t)
	j := m.Create("pcd_export", "取消任务")
	m.RunSync(j, func(jj *Job) (map[string]any, error) {
		jj.Cancel()
		return nil, jj.CheckCancel()
	}, false)
	if j.Status != StatusCancelled {
		t.Fatalf("status = %s, want cancelled", j.Status)
	}
	// 取消抛错不算 failed
	if j.Error != "" {
		t.Fatalf("取消不应记 error, got %q", j.Error)
	}
}

func TestStartAsync(t *testing.T) {
	m, _ := newTestManager(t)
	j := m.Create("daily_sync", "异步")
	done := make(chan struct{})
	j.OnDone = func(*Job) { close(done) }
	m.Start(j, func(jj *Job) (map[string]any, error) {
		return map[string]any{"ok": true}, nil
	})
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("异步任务超时")
	}
	if j.Status != StatusSuccess {
		t.Fatalf("status = %s", j.Status)
	}
}

func TestJobIDFormat(t *testing.T) {
	m, _ := newTestManager(t)
	j := m.Create("a", "t")
	if len(j.ID) != 17 || j.ID[14] != '-' {
		t.Fatalf("job_id 格式 = %q, want YYYYMMDDHHMMSS-NN", j.ID)
	}
}

func TestTerminalPruneKeeps50(t *testing.T) {
	m, st := newTestManager(t)
	for i := 0; i < 55; i++ {
		j := m.Create("bulk", "批量")
		m.RunSync(j, func(jj *Job) (map[string]any, error) { return nil, nil }, false)
	}
	if n := m.Count(); n != MaxTerminalJobs {
		t.Fatalf("内存任务数 = %d, want %d", n, MaxTerminalJobs)
	}
	// 档案文件不受影响
	entries, _ := os.ReadDir(st.JobsDir())
	if len(entries) != 55 {
		t.Fatalf("任务档案数 = %d, want 55", len(entries))
	}
}

func TestHasActive(t *testing.T) {
	m, _ := newTestManager(t)
	j := m.Create("daily_sync", "x")
	done := make(chan struct{})
	j.OnDone = func(*Job) { close(done) }
	m.Start(j, func(jj *Job) (map[string]any, error) {
		time.Sleep(50 * time.Millisecond)
		return nil, nil
	})
	if !m.HasActive("daily_sync") {
		t.Fatal("应有活跃任务")
	}
	// OnDone 在 finalize 之后触发：日志句柄已关闭，TempDir 清理不会与收尾竞态。
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("等待任务收尾超时")
	}
	if m.HasActive("daily_sync") {
		t.Fatal("任务结束后不应有活跃任务")
	}
}

func TestListFilterAndOrder(t *testing.T) {
	m, _ := newTestManager(t)
	j1 := m.Create("a", "1")
	m.RunSync(j1, func(*Job) (map[string]any, error) { return nil, nil }, false)
	j2 := m.Create("b", "2")
	m.RunSync(j2, func(*Job) (map[string]any, error) { return nil, nil }, false)
	all := m.List("", 20)
	if len(all) != 2 {
		t.Fatalf("List = %d 条", len(all))
	}
	onlyA := m.List("a", 20)
	if len(onlyA) != 1 || onlyA[0]["app_id"] != "a" {
		t.Fatalf("List(a) = %v", onlyA)
	}
}
