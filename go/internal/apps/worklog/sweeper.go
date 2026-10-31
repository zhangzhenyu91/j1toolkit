// pending 照片超时清扫：服务端在 Dify 回调途中重启导致回写丢失时自愈。
// 启动即跑一次，此后按周期执行；不在列表等请求路径上顺手 UPDATE。
package worklog

import (
	"context"
	"database/sql"
	"log"
	"sort"
	"sync"
	"time"

	"shade/internal/logging"
)

// defaultPhotoSweepInterval 默认清扫周期（启动即跑 + 每 10 分钟）。
const defaultPhotoSweepInterval = 10 * time.Minute

// inFlightPhotoIDs 在飞验证照片 id 快照（升序，便于 SQL 与测试稳定）。
func (m *Module) inFlightPhotoIDs() []int64 {
	m.verifyMu.Lock()
	defer m.verifyMu.Unlock()
	ids := make([]int64, 0, len(m.verifyInFlight))
	for id := range m.verifyInFlight {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return ids
}

// sweepPendingPhotos 把超时 10 分钟仍 pending 的照片置 failed；在飞照片排除，
// 避免识别中的照片被误置失败（回写随后覆盖）；随后回收过期未引用的备注附件占位。
func (m *Module) sweepPendingPhotos(ctx context.Context) error {
	query := `UPDATE worklog_photo SET verify_status = 'failed'
 WHERE verify_status = 'pending' AND created_at < DATE_SUB(NOW(), INTERVAL 10 MINUTE)`
	args := make([]any, 0)
	if ids := m.inFlightPhotoIDs(); len(ids) > 0 {
		query += ` AND id NOT IN (` + placeholders(len(ids)) + `)`
		args = idsToAny(ids)
	}
	if _, err := m.DB.ExecContext(ctx, query, args...); err != nil {
		return err
	}
	if err := m.sweepRemarkPlaceholders(ctx); err != nil {
		log.Printf("[出工日志] 备注附件占位清理失败：%v", err)
		return err
	}
	return nil
}

// remarkPlaceholderExpired 过期且未被卡片 remark_files 引用的占位条件。
// JSON_VALID/IF 防老脏数据令 JSON_CONTAINS 报错；只有确认未引用的行才可回收。
const remarkPlaceholderExpired = `r.created_at < DATE_SUB(NOW(), INTERVAL 24 HOUR)
   AND NOT JSON_CONTAINS(COALESCE(IF(JSON_VALID(e.remark_files), e.remark_files, NULL), JSON_ARRAY()), JSON_QUOTE(r.cos_key))`

// sweepRemarkPlaceholders 回收所有卡片过期未引用占位（对象 + 登记行）。
func (m *Module) sweepRemarkPlaceholders(ctx context.Context) error {
	rows, err := m.DB.QueryContext(ctx,
		`SELECT r.id, r.cos_key FROM worklog_remark_file r
		 JOIN worklog_entry e ON e.id = r.entry_id
		 WHERE `+remarkPlaceholderExpired)
	if err != nil {
		return err
	}
	return m.deleteRemarkPlaceholders(ctx, rows)
}

// sweepRemarkPlaceholdersForEntry 保存附件时回收本卡过期未引用占位。
func (m *Module) sweepRemarkPlaceholdersForEntry(ctx context.Context, entryID int64) error {
	rows, err := m.DB.QueryContext(ctx,
		`SELECT r.id, r.cos_key FROM worklog_remark_file r
		 JOIN worklog_entry e ON e.id = r.entry_id
		 WHERE r.entry_id = ? AND `+remarkPlaceholderExpired, entryID)
	if err != nil {
		return err
	}
	return m.deleteRemarkPlaceholders(ctx, rows)
}

// deleteRemarkPlaceholders 删除占位对象与登记行；对象删除失败保留登记行待下轮重试。
func (m *Module) deleteRemarkPlaceholders(ctx context.Context, rows *sql.Rows) error {
	type placeholder struct {
		id  int64
		key string
	}
	items := make([]placeholder, 0)
	for rows.Next() {
		var p placeholder
		if rows.Scan(&p.id, &p.key) == nil {
			items = append(items, p)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, p := range items {
		if m.Store != nil && p.key != "" {
			if err := m.Store.DeleteObject(ctx, p.key); err != nil {
				log.Printf("[出工日志] 备注附件占位对象清理失败（保留登记待重试，key=%s）：%v", p.key, err)
				continue
			}
		}
		if _, err := m.DB.ExecContext(ctx,
			`DELETE FROM worklog_remark_file WHERE id = ?`, p.id); err != nil {
			log.Printf("[出工日志] 备注附件占位登记清理失败（id=%d）：%v", p.id, err)
			return err
		}
	}
	return nil
}

// StartPhotoSweeper 启动 pending 照片清扫：立即执行一次，此后每 SweepInterval（0=10 分钟）执行。
// 返回的 stop 幂等（可重复调用）；ctx 取消同样停止。接入方（main）负责在退出时调用 stop。
// Task 11 在 main.go 装配：sweepStop := wl.StartPhotoSweeper(ctx)，cleanup 中调用 sweepStop()。
func (m *Module) StartPhotoSweeper(ctx context.Context) (stop func()) {
	interval := m.SweepInterval
	if interval <= 0 {
		interval = defaultPhotoSweepInterval
	}
	stopCh := make(chan struct{})
	var once sync.Once
	stop = func() { once.Do(func() { close(stopCh) }) }
	go func() {
		_ = m.sweepPendingPhotos(logging.WithNewTraceID(ctx))
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-stopCh:
				return
			case <-ticker.C:
				_ = m.sweepPendingPhotos(logging.WithNewTraceID(ctx))
			}
		}
	}()
	return stop
}
