package main

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"shade/internal/apps/cluster"
	"shade/internal/apps/crossing"
	"shade/internal/apps/treehazard"
	"shade/internal/apps/usbcmd"
)

func TestSplitCombinedTasksPairsTreeAndCrossing(t *testing.T) {
	tasks := []combinedTask{
		{teamName: "A", app: cluster.AppTreeSync},
		{teamName: "B", app: cluster.AppTreeSync},
		{teamName: "A", app: cluster.AppCrossingSync},
	}
	batches := splitCombinedTasks(tasks)
	if len(batches) != 2 {
		t.Fatalf("应配对 1 组 + 1 个单任务，got %d", len(batches))
	}
	if len(batches[0]) != 2 || batches[0][0].app != cluster.AppTreeSync || batches[0][1].app != cluster.AppCrossingSync {
		t.Fatalf("首组应为树障+交跨配对: %+v", batches[0])
	}
	if len(batches[1]) != 1 || batches[1][0].teamName != "B" {
		t.Fatalf("剩余树障任务应单独一批: %+v", batches[1])
	}
}

func TestSplitCombinedTasksCrossingOnly(t *testing.T) {
	tasks := []combinedTask{
		{teamName: "A", app: cluster.AppCrossingSync},
		{teamName: "B", app: cluster.AppCrossingSync},
	}
	batches := splitCombinedTasks(tasks)
	if len(batches) != 2 {
		t.Fatalf("无树障时应各自单批，got %d", len(batches))
	}
}

// batchAwait 记录 await 调用轨迹并回放给定结果。
type batchAwait struct {
	trace []string
	refs  [][]usbcmd.CommandRef
	out   map[usbcmd.CommandRef]usbcmd.AwaitOutcome
}

func (b *batchAwait) call(_ context.Context, refs []usbcmd.CommandRef) map[usbcmd.CommandRef]usbcmd.AwaitOutcome {
	b.trace = append(b.trace, "await")
	b.refs = append(b.refs, refs)
	return b.out
}

// batchRun 记录收尾调用轨迹。
type batchRun struct {
	trace []string
}

func (b *batchRun) call(_ context.Context, task combinedTask, _ usbcmd.Result) (treehazard.IntranetSyncResult, error) {
	b.trace = append(b.trace, "run:"+task.app)
	return treehazard.IntranetSyncResult{OK: true, Message: "done"}, nil
}

func (b *batchRun) fail(_ context.Context, task combinedTask, _ error) {
	b.trace = append(b.trace, "fail:"+task.app)
}

func batchOfTwo() []combinedTask {
	return []combinedTask{
		{teamName: "A", app: cluster.AppTreeSync,
			tree: &treehazard.CyclePlan{TeamID: 1, TeamName: "A", Cycle: "tree-1"}},
		{teamName: "A", app: cluster.AppCrossingSync,
			cross: &crossing.CyclePlan{TeamID: 1, TeamName: "A", Cycle: "cross-1"}},
	}
}

// 合批 snapshot 必须同批一次等待：先一次 AwaitMany 带上树障/交跨两个 ref，再逐个收尾，
// 不得出现「树障完整轮次跑完才首次等待交跨结果」。
func TestRunPushedCombinedTasksDoesNotSerializeSnapshotWaits(t *testing.T) {
	await := &batchAwait{out: map[usbcmd.CommandRef]usbcmd.AwaitOutcome{
		{App: cluster.AppTreeSync, ID: "tree-1"}:      {Result: usbcmd.Result{ID: "tree-1", App: cluster.AppTreeSync, OK: true}},
		{App: cluster.AppCrossingSync, ID: "cross-1"}: {Result: usbcmd.Result{ID: "cross-1", App: cluster.AppCrossingSync, OK: true}},
	}}
	run := &batchRun{}
	outcomes := runPushedCombinedTasks(context.Background(), batchOfTwo(), await.call, run.call, run.fail, nil)
	trace := append(await.trace, run.trace...)
	if len(trace) != 3 || trace[0] != "await" {
		t.Fatalf("应先一次 await 再逐个 run，got %v", trace)
	}
	if trace[1] != "run:"+cluster.AppTreeSync || trace[2] != "run:"+cluster.AppCrossingSync {
		t.Fatalf("收尾顺序应沿用现有批次顺序，got %v", trace)
	}
	if len(await.refs) != 1 || len(await.refs[0]) != 2 ||
		await.refs[0][0].App != cluster.AppTreeSync || await.refs[0][1].App != cluster.AppCrossingSync {
		t.Fatalf("同一次 await 应同时带树障与交跨 ref，got %v", await.refs)
	}
	if len(outcomes) != 2 {
		t.Fatalf("两个任务各一条结果，got %+v", outcomes)
	}
}

// 一条等待失败不阻断另一条：失败任务只走 fail，成功任务仍走 run。
func TestRunPushedCombinedTasksKeepsPartialFailureIndependent(t *testing.T) {
	await := &batchAwait{out: map[usbcmd.CommandRef]usbcmd.AwaitOutcome{
		{App: cluster.AppTreeSync, ID: "tree-1"}:      {Err: errors.New("等待指令 tree-1 结果超时")},
		{App: cluster.AppCrossingSync, ID: "cross-1"}: {Result: usbcmd.Result{ID: "cross-1", App: cluster.AppCrossingSync, OK: true}},
	}}
	run := &batchRun{}
	outcomes := runPushedCombinedTasks(context.Background(), batchOfTwo(), await.call, run.call, run.fail, nil)
	if len(run.trace) != 2 || run.trace[0] != "fail:"+cluster.AppTreeSync || run.trace[1] != "run:"+cluster.AppCrossingSync {
		t.Fatalf("失败任务只记失败、成功任务继续收尾，got %v", run.trace)
	}
	if len(outcomes) != 2 || outcomes[0].err == nil || outcomes[1].err != nil {
		t.Fatalf("outcomes 应各一条且失败标记独立: %+v", outcomes)
	}
}

// 等待失败写日志/通知时保留单应用口径前缀，便于既有排障关键字继续命中。
func TestRunPushedCombinedTasksKeepsAwaitFailurePrefix(t *testing.T) {
	await := &batchAwait{out: map[usbcmd.CommandRef]usbcmd.AwaitOutcome{
		{App: cluster.AppTreeSync, ID: "tree-1"}:      {Err: errors.New("等待指令 tree-1 结果超时（被控机未上报状态）")},
		{App: cluster.AppCrossingSync, ID: "cross-1"}: {Result: usbcmd.Result{ID: "cross-1", App: cluster.AppCrossingSync, OK: true}},
	}}
	run := &batchRun{}
	var failedErr error
	fail := func(_ context.Context, task combinedTask, err error) {
		if task.app == cluster.AppTreeSync {
			failedErr = err
		}
	}
	outcomes := runPushedCombinedTasks(context.Background(), batchOfTwo(), await.call, run.call, fail, nil)
	const prefix = "等待被控机执行结果失败："
	if failedErr == nil || !strings.Contains(failedErr.Error(), prefix) {
		t.Fatalf("RecordCycleFailure 应收到单应用前缀，got %v", failedErr)
	}
	if !strings.Contains(failedErr.Error(), "等待指令 tree-1 结果超时") {
		t.Fatalf("前缀不得吞掉原始原因，got %v", failedErr)
	}
	if len(outcomes) != 2 || !strings.Contains(outcomes[0].message, prefix) {
		t.Fatalf("合批摘要也应带同一前缀，got %+v", outcomes)
	}
}

// await 结果里缺该任务 ref 时不得带零值结果继续收尾。
func TestRunPushedCombinedTasksFailsTaskWithoutSnapshot(t *testing.T) {
	var runCalls int
	batch := []combinedTask{
		{teamName: "A", app: cluster.AppTreeSync,
			tree: &treehazard.CyclePlan{TeamID: 1, TeamName: "A", Cycle: "tree-1"}},
	}
	outcomes := runPushedCombinedTasks(context.Background(), batch,
		func(context.Context, []usbcmd.CommandRef) map[usbcmd.CommandRef]usbcmd.AwaitOutcome { return nil },
		func(context.Context, combinedTask, usbcmd.Result) (treehazard.IntranetSyncResult, error) {
			runCalls++
			return treehazard.IntranetSyncResult{OK: true, Message: "done"}, nil
		},
		func(context.Context, combinedTask, error) {},
		nil)
	if runCalls != 0 {
		t.Fatalf("缺预取结果不应继续收尾，run 调用 %d 次", runCalls)
	}
	if len(outcomes) != 1 || outcomes[0].err == nil {
		t.Fatalf("缺预取结果应判失败，got %+v", outcomes)
	}
}

// 合批窗口策略同源规则：取批次内首个已配置的 SyncCmdPolicy（按批次顺序），
// 两应用皆未配置时回落到 Once(DefaultResultWait)；replace 不受此策略影响。
func TestSnapshotPolicyUsesFirstConfiguredPolicy(t *testing.T) {
	crossPolicy := usbcmd.Policy{Waits: []time.Duration{time.Minute}, Total: 3 * time.Minute}
	c := &combinedSync{
		Tree:     &treehazard.Module{},
		Crossing: &crossing.Module{SyncCmdPolicy: &crossPolicy},
	}
	got := c.snapshotPolicy(batchOfTwo())
	if len(got.Waits) != 1 || got.Waits[0] != time.Minute || got.Total != 3*time.Minute {
		t.Fatalf("树障未配置时应取交跨策略，got %+v", got)
	}
}

func TestSnapshotPolicyPrefersFirstConfiguredInBatchOrder(t *testing.T) {
	treePolicy := usbcmd.Policy{Waits: []time.Duration{2 * time.Minute}, Total: 4 * time.Minute}
	crossPolicy := usbcmd.Policy{Waits: []time.Duration{time.Minute}, Total: 3 * time.Minute}
	c := &combinedSync{
		Tree:     &treehazard.Module{SyncCmdPolicy: &treePolicy},
		Crossing: &crossing.Module{SyncCmdPolicy: &crossPolicy},
	}
	got := c.snapshotPolicy(batchOfTwo())
	if len(got.Waits) != 1 || got.Waits[0] != 2*time.Minute || got.Total != 4*time.Minute {
		t.Fatalf("两应用都配置时应取批次内首个（树障），got %+v", got)
	}
	reversed := c.snapshotPolicy([]combinedTask{batchOfTwo()[1], batchOfTwo()[0]})
	if len(reversed.Waits) != 1 || reversed.Waits[0] != time.Minute {
		t.Fatalf("批次顺序以交跨在前时应取交跨策略，got %+v", reversed)
	}
}

func TestSnapshotPolicyFallsBackToOnceWhenBothUnconfigured(t *testing.T) {
	c := &combinedSync{Tree: &treehazard.Module{}, Crossing: &crossing.Module{}}
	got := c.snapshotPolicy(batchOfTwo())
	if len(got.Waits) != 1 || got.Waits[0] != usbcmd.DefaultResultWait {
		t.Fatalf("两应用皆未配置应回落 Once(%s)，got %+v", usbcmd.DefaultResultWait, got)
	}
	if got.Total != usbcmd.DefaultResultWait+time.Minute {
		t.Fatalf("Once 应保留 1 分钟余量，got %+v", got)
	}
}
