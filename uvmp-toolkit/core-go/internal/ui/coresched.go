package ui

import (
	"context"
	"strings"

	"github.com/zhangzhenyu91/j1toolkit/uvmp-toolkit/core-go/internal/config"
	"github.com/zhangzhenyu91/j1toolkit/uvmp-toolkit/core-go/internal/schedule"
)

// 本文件：CoreBackend 的驻留调度通道（ScheduleAPI）——与 toolkit RPC
// maybeRunDaily / maybeRunTreeSyncTick / maybeRunCrossingSyncTick 同口径：
// 到点判定在核心侧 due-check 门闸内完成，接口只回报是否启动了任务。

// MaybeRunDaily 每日派车单同步到点判定（树障/交跨夜间兜底同挂此调用）。
func (b *CoreBackend) MaybeRunDaily(_ context.Context) (DailyTickResult, error) {
	cfg := b.load()
	res := DailyTickResult{
		TreeSyncNightly:     b.maybeTableSyncNightly(cfg, "tree_sync"),
		CrossingSyncNightly: b.maybeTableSyncNightly(cfg, "crossing_sync"),
	}
	due, reason, today := schedule.DueCheck(cfg, b.now(), "app", "", nil)
	res.Reason = reason
	if !due {
		return res, nil
	}
	schedule.MarkAttempt(today, "") // 与 OS 定时器同口径：拉开失败重试间隔
	id, err := b.RunJob(context.Background(), "daily_sync", map[string]any{"date": ""})
	if err != nil {
		res.Reason = err.Error()
		return res, nil
	}
	res.Started = true
	res.JobID = id
	return res, nil
}

// maybeTableSyncNightly 统计表同步夜间兜底（driver=app 驻留调度侧，与 OS 定时器的
// --nightly --if-due 同口径）：启用条件=已配置 local_path；时刻=nightly_time；今日已成功则跳过。
func (b *CoreBackend) maybeTableSyncNightly(cfg config.Dict, appID string) *TickResult {
	out := &TickResult{}
	ts := config.MapField(cfg, appID)
	if strings.TrimSpace(config.StrField(ts, "local_path")) == "" {
		return out
	}
	sch := schedule.Schedule{
		Enabled: true,
		Time:    config.StrFieldDefault(ts, "nightly_time", "23:55"),
		Driver:  config.StrFieldDefault(config.MapField(cfg, "schedule"), "driver", "os"),
	}
	stamp := appID + "_nightly"
	due, _, today := schedule.DueCheck(cfg, b.now(), "app", stamp, &sch)
	if !due {
		return out
	}
	schedule.MarkAttempt(today, stamp)
	id, err := b.RunJob(context.Background(), appID, map[string]any{"nightly": true})
	if err != nil {
		return out
	}
	out.Started = true
	out.JobID = id
	return out
}

// MaybeRunTreeSyncTick 客户端驻留调度入口（树障统计表同步，每分钟轮询）：
// 不做 due-check——U 盘不在时后端静默秒退；有指令立即执行。
func (b *CoreBackend) MaybeRunTreeSyncTick(_ context.Context) (TickResult, error) {
	return b.maybeTableSyncTick("tree_sync"), nil
}

// MaybeRunCrossingSyncTick 交跨统计表同步轮询；同 tree_sync tick。
func (b *CoreBackend) MaybeRunCrossingSyncTick(_ context.Context) (TickResult, error) {
	return b.maybeTableSyncTick("crossing_sync"), nil
}

func (b *CoreBackend) maybeTableSyncTick(appID string) TickResult {
	cfg := b.load()
	if config.StrFieldDefault(config.MapField(cfg, "schedule"), "driver", "os") != "app" {
		return TickResult{}
	}
	if strings.TrimSpace(config.StrField(config.MapField(cfg, appID), "local_path")) == "" {
		return TickResult{}
	}
	id, err := b.RunJob(context.Background(), appID, map[string]any{"tick": true})
	if err != nil {
		return TickResult{}
	}
	return TickResult{Started: true, JobID: id}
}
