// 派车每日排程装配（迁移 server/src/worklog/dispatch-sync.js start 的排程口径：
// 09:10 准备「锁定设备 + 挂载 U 盘至被控机」→ 09:20 取件「下载建卡 + 解禁」）。
// 执行器复用 Wire 注入 worklog.Dispatcher 的同一 DispatchSyncer（重入守卫共用，定时与
// 手动 sync-now 不并发）；本文件只做作业形态适配，排程时刻由 main scheduleDailyCn 注入。
package bridge

import (
	"context"
	"fmt"

	"shade/internal/apps/worklog"
)

// DispatchJobs 派车每日排程作业体（每段返回摘要 + error，交编排层记日志）。
type DispatchJobs struct {
	// Prepare 锁定设备并挂载 U 盘至被控机（Node prepareJob；失败自行解禁并通知，error 供日志）。
	Prepare func(ctx context.Context) (string, error)
	// Fetch 取件建卡并解禁（Node fetchJob；逐班组结果通知，摘要为新建卡片合计）。
	Fetch func(ctx context.Context) (string, error)
}

// NewDispatchJobs 装配派车每日排程作业；syncer 为 Wire 注入 worklog.Dispatcher 的执行器
// （nil 或未接入实现时作业触发即报可读错误，不假成功）。
func NewDispatchJobs(syncer worklog.DispatchSyncer) DispatchJobs {
	return DispatchJobs{
		Prepare: func(ctx context.Context) (string, error) {
			if syncer == nil {
				return "", fmt.Errorf("派车单同步服务未接入")
			}
			if err := syncer.PrepareNow(ctx); err != nil {
				return "", err
			}
			return "设备已锁定，U 盘已挂载至被控机端", nil
		},
		Fetch: func(ctx context.Context) (string, error) {
			if syncer == nil {
				return "", fmt.Errorf("派车单同步服务未接入")
			}
			n, err := syncer.SyncNow(ctx, 0)
			if err != nil {
				return "", err
			}
			return fmt.Sprintf("新建 %d 张出车卡片", n), nil
		},
	}
}
