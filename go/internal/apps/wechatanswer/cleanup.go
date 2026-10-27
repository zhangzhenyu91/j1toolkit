// 图片清理排程：未挂接图片本体/缓存条目与媒体参数缓存过期清理，
// 顺带清扫幂等事件登记（30 天）与撤回暂存（1 小时）。启动先跑一轮，此后每 12 小时。
package wechatanswer

import (
	"context"
	"os"
	"time"
)

const (
	cleanupInterval = 12 * time.Hour
	eventTTL        = 30 * 24 * time.Hour
	recallTTL       = time.Hour
	mediaTTL        = 3 * 24 * time.Hour
	imageTTLDefault = 7 * 24 * time.Hour
)

// sweepOnce 执行一轮清理；返回清理到的文件数（测试断言用）。
func (m *Module) sweepOnce(ctx context.Context) int {
	ttl := m.ImageTTL
	if ttl <= 0 {
		ttl = imageTTLDefault
	}
	now := m.now().Unix()
	files, err := m.recordStore().SweepImages(ctx, now-int64(ttl/time.Second))
	removed := 0
	if err == nil {
		for _, rel := range files {
			if rel == "" {
				continue
			}
			if os.Remove(m.photoPath(rel)) == nil {
				removed++
			}
		}
	}
	if err := m.mediaCache().SweepMedia(ctx, now-int64(mediaTTL/time.Second)); err != nil {
		m.appLogErr(ctx, "wechat.cleanup.media_failed", "媒体参数缓存清理失败", err, nil)
	}
	if err := m.eventLog().SweepEvents(ctx, now-int64(eventTTL/time.Second)); err != nil {
		m.appLogErr(ctx, "wechat.cleanup.event_failed", "幂等事件清理失败", err, nil)
	}
	if err := m.recallLog().SweepRecalls(ctx, now-int64(recallTTL/time.Second)); err != nil {
		m.appLogErr(ctx, "wechat.cleanup.recall_failed", "撤回暂存清理失败", err, nil)
	}
	return removed
}

// runCleanupLoop 清理排程：启动先跑一轮，此后每 12 小时。
func (m *Module) runCleanupLoop(ctx context.Context) {
	m.sweepOnce(ctx)
	ticker := time.NewTicker(cleanupInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.sweepOnce(ctx)
		}
	}
}
