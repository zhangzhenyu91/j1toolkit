package course

import (
	"context"
	"fmt"
)

// RunJobOptions 一次刷课任务编排参数（backend.py run 的 cfg/params 合并结果）。
type RunJobOptions struct {
	// Options Runner 参数（Timeout/MaxAttempts 必须>0，与 backend.py 校验同口径）。
	Options
	// ScanOnly 只扫描全部分页，不进入课程（params.scan_only）。
	ScanOnly bool
	// Dir 结果摘要里的日志目录（state/logs）。
	Dir string
	// Store 检查点持久化（state/course-autoplay/checkpoints.json）。
	Store *CheckpointsStore
}

// RunJob 一次学堂刷课任务（backend.py run 移植）：接管课程列表→清筛选→续刷/跳过/汇总。
// 按 trainid/列表地址分别持久化检查点（续刷页、真实失败次数、已处理数、跳过项及原因）；
// ctx 取消=暂停：回滚当次尝试、保留受控浏览器与登录态，返回 paused=true 结果（不算失败）。
func RunJob(ctx context.Context, browser Browser, opts RunJobOptions) (map[string]any, error) {
	if opts.Timeout <= 0 || opts.MaxAttempts <= 0 {
		return nil, fmt.Errorf("单步超时和单课尝试次数必须大于 0")
	}
	store := opts.Store
	if store == nil {
		store = NewCheckpointsStore()
	}
	checkpoints := store.Load()
	listKey := ""
	var checkpoint *Checkpoint
	finished := false

	// 进度即持久化（backend.py on_progress：先落盘再上抛）
	innerProgress := opts.Progress
	opts.Progress = func(current, total int, label string) {
		if checkpoint != nil && listKey != "" {
			store.Persist(checkpoints, listKey, checkpoint)
		}
		if innerProgress != nil {
			innerProgress(current, total, label)
		}
	}
	runner := NewRunner(browser, opts.Options)

	pausedResult := func() map[string]any {
		if opts.Log != nil {
			opts.Log("学习已暂停：受控浏览器和登录状态已保留，可切换课程列表后再次开始学习")
		}
		resumePage, processed := 0, 0
		if checkpoint != nil {
			resumePage = checkpoint.ResumePage
			processed = checkpoint.Processed
		}
		return map[string]any{
			"paused":      true,
			"list_key":    listKey,
			"resume_page": resumePage,
			"processed":   processed,
			"dir":         opts.Dir,
		}
	}

	result, runErr := func() (map[string]any, error) {
		if err := runner.FindListPage(ctx); err != nil {
			return nil, err
		}
		firstPage, err := runner.ResetFilters(ctx)
		if err != nil {
			return nil, err
		}
		if opts.ScanOnly {
			summary, err := runner.ScanCourses(ctx, false, firstPage, nil, 1)
			if err != nil {
				return nil, err
			}
			runner.ReportScan(summary, nil)
			if opts.Log != nil {
				opts.Log("只扫描模式结束，未进入任何课程")
			}
			finished = true
			return summaryResult(summary, map[string]string{}, "", opts.Dir), nil
		}

		listKey = CourseListKey(runner.ListPage().URL())
		previous := checkpoints[listKey]
		resumed := previous != nil && previous.Active
		if resumed {
			checkpoint = previous
		} else {
			checkpoint = NewCheckpoint()
		}
		checkpoint.Active = true
		if resumed {
			if opts.Log != nil {
				opts.Log(fmt.Sprintf("恢复当前培训班：第 %d 页，已处理 %d 门次，跳过 %d 门",
					checkpoint.ResumePage, checkpoint.Processed, len(checkpoint.SkippedIDs)))
			}
		} else if opts.Log != nil {
			opts.Log("当前培训班开始新一轮学习")
		}
		store.Persist(checkpoints, listKey, checkpoint)

		summary, err := runner.Run(ctx, firstPage, checkpoint)
		if err != nil {
			return nil, err
		}
		result := summaryResult(summary, checkpoint.SkipReasons, listKey, opts.Dir)
		checkpoint.LastResult = result
		finished = true
		return result, nil
	}()

	// 暂停（ctx 取消）→ paused 结果；其他错误原样上抛（backend.py except 分支）
	if runErr != nil && ctx.Err() != nil {
		result = pausedResult()
		runErr = nil
	}
	if checkpoint != nil && listKey != "" {
		checkpoint.Active = !finished
		store.Persist(checkpoints, listKey, checkpoint)
	}
	browser.Close()
	return result, runErr
}

// summaryResult 正式学习/扫描结果形状（backend.py _summary）。
func summaryResult(summary *ScanSummary, skipReasons map[string]string, listKey, dir string) map[string]any {
	if summary == nil {
		summary = &ScanSummary{}
	}
	if skipReasons == nil {
		skipReasons = map[string]string{}
	}
	pendingCourses := []any{}
	for i, entry := range summary.Pending {
		if i >= 100 {
			break
		}
		id := entry.ResourceID()
		reason := entry.Reason
		if reason == "" {
			reason = skipReasons[id]
		}
		var progress any
		if p, ok := CourseProgress(entry.Item); ok {
			progress = p
		}
		pendingCourses = append(pendingCourses, map[string]any{
			"name":     entry.Name(),
			"page":     entry.Page,
			"progress": progress,
			"reason":   reason,
		})
	}
	return map[string]any{
		"paused":          false,
		"list_key":        listKey,
		"total":           summary.Total,
		"completed":       summary.Completed,
		"pending":         len(summary.Pending),
		"skipped":         len(skipReasons),
		"pending_courses": pendingCourses,
		"dir":             dir,
	}
}
