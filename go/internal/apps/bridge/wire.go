// 派车每日排程装配（09:10 准备「锁定设备 + 下发导出指令 + 等一次结果 + 取件建卡 + 解禁」；
// 不再有 09:20 固定取件）。执行器复用 Wire 注入 worklog.Dispatcher 的同一 DispatchSyncer
// （重入守卫共用，定时与手动 sync-now 不并发）；本文件只做作业形态适配，排程时刻由
// main scheduleDailyCn 注入。
package bridge

import (
	"context"
	"fmt"

	"shade/internal/apps/worklog"
)

// DispatchJobs 派车每日排程作业体（返回摘要 + error，交编排层记日志）。
type DispatchJobs struct {
	// Prepare 锁定设备、下发导出指令、等一次结果并立即取件建卡（失败自行解禁并通知，error 供日志）。
	Prepare func(ctx context.Context) (string, error)
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
			return "设备已锁定，导出指令已确认执行并完成取件建卡", nil
		},
	}
}
