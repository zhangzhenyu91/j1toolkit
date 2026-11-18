package main

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"shade/internal/apps/cluster"
	"shade/internal/apps/crossing"
	"shade/internal/apps/treehazard"
	"shade/internal/apps/usbcmd"
)

// combinedSync 每夜把同一 Hub 上的树障/交跨 snapshot 指令合并到一次挂载：
// 先各推一条指令 → mount 一次 → 同批指令在同一等待窗口用 usbcmd.AwaitMany 一次取回 →
// 各模块消费预取结果后取件/回写；同 Hub 多任务按对串行。
// replace 仍由各模块取件阶段决定并各自等待，单应用失败不影响另一应用。
type combinedSync struct {
	Tree     *treehazard.Module
	Crossing *crossing.Module
	Cluster  *cluster.Store
	Link     treehazard.UsbLink
}

type combinedTask struct {
	teamID     int64
	teamName   string
	app        string
	tree       *treehazard.CyclePlan
	cross      *crossing.CyclePlan
	sessionKey string
	hub        usbcmd.HubRef
}

type combinedOutcome struct {
	team    string
	app     string
	message string
	err     error
}

func (c *combinedSync) Run(ctx context.Context) (string, error) {
	if c == nil || c.Tree == nil || c.Crossing == nil || c.Cluster == nil {
		return "合批同步未配置", errors.New("合批同步未配置")
	}
	treeTeams, err := c.Cluster.ListNightTeams(ctx, cluster.AppTreeSync)
	if err != nil {
		return "读取树障夜间班组失败: " + err.Error(), err
	}
	crossTeams, err := c.Cluster.ListNightTeams(ctx, cluster.AppCrossingSync)
	if err != nil {
		return "读取交跨夜间班组失败: " + err.Error(), err
	}
	crossByID := map[int64]string{}
	for _, team := range crossTeams {
		crossByID[team.ID] = team.Name
	}
	tasks := make([]combinedTask, 0)
	outcomes := make([]combinedOutcome, 0)
	for _, team := range treeTeams {
		name, both := crossByID[team.ID]
		if !both {
			continue
		}
		if name == "" {
			name = team.Name
		}
		tp, terr := c.Tree.PrepareCycle(ctx, team.ID, name, "night")
		if terr != nil {
			c.Tree.RecordCycleFailure(ctx, team.ID, name, "night", terr)
			outcomes = append(outcomes, combinedOutcome{team: name, app: "tree_sync", message: terr.Error(), err: terr})
		} else {
			tasks = append(tasks, combinedTask{
				teamID: team.ID, teamName: name, app: cluster.AppTreeSync,
				tree: tp, sessionKey: tp.SessionKey, hub: tp.Hub,
			})
		}
		cp, cerr := c.Crossing.PrepareCycle(ctx, team.ID, name, "night")
		if cerr != nil {
			c.Crossing.RecordCycleFailure(ctx, team.ID, name, "night", cerr)
			outcomes = append(outcomes, combinedOutcome{team: name, app: cluster.AppCrossingSync, message: cerr.Error(), err: cerr})
		} else {
			tasks = append(tasks, combinedTask{
				teamID: team.ID, teamName: name, app: cluster.AppCrossingSync,
				cross: cp, sessionKey: cp.SessionKey, hub: cp.Hub,
			})
		}
	}
	if len(tasks) == 0 {
		if len(outcomes) == 0 {
			return "夜间同步未启用或未绑定节点", nil
		}
		return combinedSummary(outcomes), firstCombinedError(outcomes)
	}
	if c.Link == nil {
		return "合批同步 U 盘链路未接入", errors.New("U 盘链路未接入")
	}

	groups := map[string][]combinedTask{}
	for _, task := range tasks {
		groups[task.sessionKey] = append(groups[task.sessionKey], task)
	}
	keys := make([]string, 0, len(groups))
	for key := range groups {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		group := groups[key]
		hub := group[0].hub
		if err := usbcmd.DefaultSessions.Acquire(ctx, key); err != nil {
			for _, task := range group {
				task.recordFailure(ctx, c, err)
				outcomes = append(outcomes, combinedOutcome{team: task.teamName, app: task.app, message: err.Error(), err: err})
			}
			continue
		}
		groupCtx := ctx
		if hub.ID != "" {
			groupCtx = usbcmd.WithHub(groupCtx, hub)
		}
		for _, batch := range splitCombinedTasks(group) {
			outcomes = c.runBatch(groupCtx, batch, outcomes)
		}
		usbcmd.RestoreShare(groupCtx, c.Link)
		usbcmd.DefaultSessions.Release(key)
	}
	return combinedSummary(outcomes), firstCombinedError(outcomes)
}

// splitCombinedTasks 同一 Hub 内树障/交跨一一配对；多出的任务各成一批。
func splitCombinedTasks(tasks []combinedTask) [][]combinedTask {
	var trees, crosses []combinedTask
	for _, task := range tasks {
		if task.app == cluster.AppTreeSync {
			trees = append(trees, task)
		} else {
			crosses = append(crosses, task)
		}
	}
	pairCount := len(trees)
	if len(crosses) < pairCount {
		pairCount = len(crosses)
	}
	batches := make([][]combinedTask, 0, len(tasks))
	for i := 0; i < pairCount; i++ {
		batches = append(batches, []combinedTask{trees[i], crosses[i]})
	}
	for i := pairCount; i < len(trees); i++ {
		batches = append(batches, []combinedTask{trees[i]})
	}
	for i := pairCount; i < len(crosses); i++ {
		batches = append(batches, []combinedTask{crosses[i]})
	}
	return batches
}

func (c *combinedSync) runBatch(ctx context.Context, batch []combinedTask, outcomes []combinedOutcome) []combinedOutcome {
	pushed := make([]combinedTask, 0, len(batch))
	for _, task := range batch {
		if err := task.push(ctx, c); err != nil {
			task.recordFailure(ctx, c, err)
			outcomes = append(outcomes, combinedOutcome{team: task.teamName, app: task.app, message: err.Error(), err: err})
			continue
		}
		pushed = append(pushed, task)
	}
	if len(pushed) == 0 {
		return outcomes
	}
	if err := c.Link.MountToHost(ctx); err != nil {
		for _, task := range pushed {
			task.recordFailure(ctx, c, err)
			outcomes = append(outcomes, combinedOutcome{team: task.teamName, app: task.app, message: err.Error(), err: err})
		}
		return outcomes
	}
	policy := c.snapshotPolicy(pushed)
	outcomes = runPushedCombinedTasks(ctx, pushed,
		func(ctx context.Context, refs []usbcmd.CommandRef) map[usbcmd.CommandRef]usbcmd.AwaitOutcome {
			return usbcmd.AwaitMany(ctx, c.Link, refs, policy)
		},
		func(ctx context.Context, task combinedTask, snapshot usbcmd.Result) (treehazard.IntranetSyncResult, error) {
			return task.run(ctx, c, snapshot)
		},
		func(ctx context.Context, task combinedTask, err error) {
			task.recordFailure(ctx, c, err)
		},
		outcomes)
	// 下一批 push 前把盘回到被控机；同 Hub 会话仍由外层持有。
	usbcmd.RestoreShare(ctx, c.Link)
	return outcomes
}

// runPushedCombinedTasks 合批收尾：同一批次的所有 snapshot 只等一个窗口、取回一次，
// 再按批次顺序逐个收尾；等待失败的任务独立记录，不影响同批其他任务。
func runPushedCombinedTasks(ctx context.Context, tasks []combinedTask,
	await func(context.Context, []usbcmd.CommandRef) map[usbcmd.CommandRef]usbcmd.AwaitOutcome,
	run func(context.Context, combinedTask, usbcmd.Result) (treehazard.IntranetSyncResult, error),
	fail func(context.Context, combinedTask, error),
	outcomes []combinedOutcome) []combinedOutcome {
	refs := make([]usbcmd.CommandRef, 0, len(tasks))
	for _, task := range tasks {
		refs = append(refs, task.ref())
	}
	snapshots := await(ctx, refs)
	for _, task := range tasks {
		out, ok := snapshots[task.ref()]
		if !ok {
			err := fmt.Errorf("未取到指令 %s 的执行结果", task.ref().ID)
			fail(ctx, task, err)
			outcomes = append(outcomes, combinedOutcome{team: task.teamName, app: task.app, message: err.Error(), err: err})
			continue
		}
		if out.Err != nil {
			// 沿用单应用失败的记录口径，日志/通知关键字与串行流程一致。
			err := fmt.Errorf("等待被控机执行结果失败：%w", out.Err)
			fail(ctx, task, err)
			outcomes = append(outcomes, combinedOutcome{team: task.teamName, app: task.app, message: err.Error(), err: err})
			continue
		}
		res, err := run(ctx, task, out.Result)
		outcomes = append(outcomes, combinedOutcome{team: task.teamName, app: task.app, message: res.Message, err: err})
	}
	return outcomes
}

// snapshotPolicy 合批窗口策略：按批次顺序取第一个已配置的 SyncCmdPolicy（同源规则：
// 两应用共用一个等待窗口，不再各自计时），两应用皆未配置时回落 Once(DefaultResultWait)。
// replace 不受此策略影响，仍各用本模块的 SyncCmdPolicy 串行等待。
func (c *combinedSync) snapshotPolicy(tasks []combinedTask) usbcmd.Policy {
	for _, task := range tasks {
		if task.app == cluster.AppTreeSync {
			if c.Tree.SyncCmdPolicy != nil && len(c.Tree.SyncCmdPolicy.Waits) > 0 {
				return *c.Tree.SyncCmdPolicy
			}
			continue
		}
		if c.Crossing.SyncCmdPolicy != nil && len(c.Crossing.SyncCmdPolicy.Waits) > 0 {
			return *c.Crossing.SyncCmdPolicy
		}
	}
	return usbcmd.Once(usbcmd.DefaultResultWait)
}

// ref 该任务在 U 盘上的 snapshot 指令引用。
func (t combinedTask) ref() usbcmd.CommandRef {
	if t.app == cluster.AppTreeSync {
		return usbcmd.CommandRef{App: cluster.AppTreeSync, ID: t.tree.Cycle}
	}
	return usbcmd.CommandRef{App: cluster.AppCrossingSync, ID: t.cross.Cycle}
}

func (t combinedTask) push(ctx context.Context, c *combinedSync) error {
	if t.app == cluster.AppTreeSync {
		if t.tree == nil {
			return errors.New("树障同步计划缺失")
		}
		return c.Link.PushFile(ctx, t.tree.CmdFile, t.tree.Command)
	}
	if t.cross == nil {
		return errors.New("交跨同步计划缺失")
	}
	return c.Link.PushFile(ctx, t.cross.CmdFile, t.cross.Command)
}

func (t combinedTask) run(ctx context.Context, c *combinedSync, snapshot usbcmd.Result) (res treehazard.IntranetSyncResult, err error) {
	if t.app == cluster.AppTreeSync {
		return c.Tree.RunPreparedCycleWithSnapshot(ctx, t.tree, snapshot)
	}
	cres, cerr := c.Crossing.RunPreparedCycleWithSnapshot(ctx, t.cross, snapshot)
	return treehazard.IntranetSyncResult{
		OK: cres.OK, Message: cres.Message, Counts: treehazard.IntranetSyncCounts{
			Updates: cres.Counts.Updates, News: cres.Counts.News, Conflicts: cres.Counts.Conflicts,
			Skips: cres.Counts.Skips, Adjudicated: cres.Counts.Adjudicated,
			Missing: cres.Counts.Missing, Pushed: cres.Counts.Pushed, NoFile: cres.Counts.NoFile,
		},
	}, cerr
}

func (t combinedTask) recordFailure(ctx context.Context, c *combinedSync, err error) {
	if t.app == cluster.AppTreeSync {
		c.Tree.RecordCycleFailure(ctx, t.teamID, t.teamName, "night", err)
		return
	}
	c.Crossing.RecordCycleFailure(ctx, t.teamID, t.teamName, "night", err)
}

func combinedSummary(outcomes []combinedOutcome) string {
	okCount, failCount := 0, 0
	parts := make([]string, 0, len(outcomes))
	for _, item := range outcomes {
		if item.err != nil {
			failCount++
			parts = append(parts, fmt.Sprintf("%s/%s：失败（%s）", item.team, appName(item.app), item.message))
			continue
		}
		okCount++
		parts = append(parts, fmt.Sprintf("%s/%s：%s", item.team, appName(item.app), item.message))
	}
	return fmt.Sprintf("合批同步 %d 个任务：成功 %d，失败 %d；%s", len(outcomes), okCount, failCount, strings.Join(parts, "；"))
}

func firstCombinedError(outcomes []combinedOutcome) error {
	for _, item := range outcomes {
		if item.err != nil {
			return item.err
		}
	}
	return nil
}

func appName(app string) string {
	if app == cluster.AppTreeSync {
		return "树障"
	}
	return "交跨"
}
