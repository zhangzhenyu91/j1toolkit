// 安全日生成记录超时清扫：新链路要求 Dify 工作流回传 record_id + batch_id，
// 工作流未升级/回调丢失时记录会永久 processing。启动即扫一次，此后周期清扫，
// 超时置 failed 并写明确 error + 结构化日志，用户可重新生成；CAS 保证不覆盖已终态记录。
package safeday

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	defaultProcessingTimeout       = 30 * time.Minute
	defaultProcessingSweepInterval = 10 * time.Minute
)

// processingTimeout 生效超时：DIFY_TIMEOUT_MS 更大时跟随（避免用户调大工作流超时后
// 30 分钟误杀活跃任务），最小 30 分钟；配置不可读/非法回退 30m；显式字段优先。
func (m *Module) processingTimeout() time.Duration {
	if m.ProcessingTimeout > 0 {
		return m.ProcessingTimeout
	}
	timeout := defaultProcessingTimeout
	if m.Cfg != nil {
		if raw, err := m.Cfg.Get(context.Background(), "DIFY_TIMEOUT_MS"); err == nil {
			if ms, err := strconv.Atoi(strings.TrimSpace(raw)); err == nil && ms > 0 {
				if derived := time.Duration(ms) * time.Millisecond; derived > timeout {
					timeout = derived
				}
			}
		}
	}
	return timeout
}

// processingSweepInterval 生效清扫周期。
func (m *Module) processingSweepInterval() time.Duration {
	if m.ProcessingSweepInterval > 0 {
		return m.ProcessingSweepInterval
	}
	return defaultProcessingSweepInterval
}

// StartProcessingSweeper 启动超时清扫：立即执行一次，此后按周期执行；返回幂等停止函数。
func (m *Module) StartProcessingSweeper(ctx context.Context) func() {
	stop := make(chan struct{})
	var once sync.Once
	go func() {
		m.sweepProcessing(ctx)
		ticker := time.NewTicker(m.processingSweepInterval())
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				m.sweepProcessing(ctx)
			case <-stop:
				return
			}
		}
	}()
	return func() {
		once.Do(func() { close(stop) })
	}
}

// sweepProcessing 扫描并置失败超时的 processing 记录，返回失败条数。
// 以记录创建时间为基准、CAS 仅更新仍为 processing 的记录，绝不误伤新批次或已终态记录。
func (m *Module) sweepProcessing(ctx context.Context) int {
	if m.Store == nil {
		return 0
	}
	timeout := m.processingTimeout()
	now := time.Now()
	failedCount := 0
	for _, record := range m.Store.List("") {
		if record.Status != "processing" || record.CreatedAt == "" {
			continue
		}
		created, err := time.Parse(time.RFC3339, record.CreatedAt)
		if err != nil {
			continue
		}
		age := now.Sub(created)
		if age < timeout {
			continue
		}
		msg := fmt.Sprintf("生成超时：超过 %s 未收到 Dify 回调（可能工作流未升级或回调丢失），请重新生成",
			timeout.Round(time.Minute))
		failed := "failed"
		updated, ok, err := m.Store.UpdateIfStatus(record.ID, "processing", RecordPatch{
			Status: &failed, SetError: true, Error: &msg,
		})
		if err != nil {
			m.appLog(ctx, "error", "safeday.processing.sweep_failed",
				"安全日生成超时清扫写盘失败，记录保持原状等待下轮",
				map[string]any{
					"record_id": record.ID, "batch_id": record.BatchID,
					"team": record.Team, "date": record.Date, "error": err.Error(),
				})
			continue
		}
		if !ok {
			continue // 状态已被回调推进/记录已删除：CAS 正常放弃，非错误
		}
		failedCount++
		m.appLog(ctx, "error", "safeday.processing.timeout",
			"安全日生成超时，记录已置失败，可重新生成",
			map[string]any{
				"record_id":       record.ID,
				"batch_id":        record.BatchID,
				"team":            record.Team,
				"date":            record.Date,
				"age_seconds":     int64(age / time.Second),
				"timeout_minutes": int64(timeout / time.Minute),
			})
		m.deleteSourceFromCos(ctx, updated)
	}
	return failedCount
}
