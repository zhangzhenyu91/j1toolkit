// 失败补推技术队列：CaptureSink 失败时载荷快照入队（崩溃安全），后台按退避补推。
package wechatanswer

import (
	"context"
	"log"
	"time"
)

// pushMaxAttempts 补推放弃阈值（超过置 dead）。
const pushMaxAttempts = 10

// deliverToSink 调 CaptureSink 并判定成功（2xx 且无错误）。
func (m *Module) deliverToSink(ctx context.Context, kind string, payload []byte) error {
	if m.Sink == nil {
		return errNoSink
	}
	status, body, err := m.Sink.Process(ctx, kind, payload)
	if err != nil {
		return err
	}
	if status < 200 || status >= 300 {
		return &sinkError{status: status, body: truncate(string(body), 200)}
	}
	return nil
}

type sinkError struct {
	status int
	body   string
}

func (e *sinkError) Error() string {
	return "CaptureSink 返回 HTTP " + itoa(e.status) + "：" + e.body
}

var errNoSink = &sinkError{status: 0, body: "未接入 CaptureSink"}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

// pushRecordToSink 单条记录上送：成功回写 push_status；失败入补推队列（不丢）并返回错误。
func (m *Module) pushRecordToSink(ctx context.Context, rec *Record, teamName string) error {
	payload, err := m.buildUpsertPayload(rec, teamName)
	if err != nil {
		return err
	}
	err = m.deliverToSink(ctx, rec.Kind, payload)
	if err == nil {
		rec.PushStatus = "ok"
		rec.PushError = ""
		_ = m.recordStore().SaveRecord(ctx, rec)
		return nil
	}
	rec.PushStatus = "failed"
	rec.PushError = truncate(err.Error(), 500)
	_ = m.recordStore().SaveRecord(ctx, rec)
	if qerr := m.pushQueue().Enqueue(ctx, rec.Kind, payload); qerr != nil {
		log.Printf("[微信智能应答] 记录 %s 补推入队失败：%v", rec.Key, qerr)
	}
	return err
}

// pushDeleteToSink 删除事件上送：失败入补推队列（事件已快照，本地删除照常）。
func (m *Module) pushDeleteToSink(ctx context.Context, rec *Record, teamName string) error {
	payload, err := m.buildDeletePayload(rec, teamName)
	if err != nil {
		return err
	}
	err = m.deliverToSink(ctx, rec.Kind, payload)
	if err == nil {
		return nil
	}
	if qerr := m.pushQueue().Enqueue(ctx, rec.Kind, payload); qerr != nil {
		log.Printf("[微信智能应答] 删除事件 %s 补推入队失败：%v", rec.Key, qerr)
	}
	return err
}

// processQueueOnce 处理一轮到期补推（测试可直接调用）；返回处理条数。
func (m *Module) processQueueOnce(ctx context.Context) (int, error) {
	if m.Sink == nil || m.Queue == nil {
		return 0, nil
	}
	items, err := m.pushQueue().Due(ctx, m.now(), 20)
	if err != nil {
		return 0, err
	}
	for _, it := range items {
		err := m.deliverToSink(ctx, it.Kind, it.Payload)
		if err == nil {
			if serr := m.pushQueue().Settle(ctx, it.ID, true, "", time.Time{}, false); serr != nil {
				return 0, serr
			}
			continue
		}
		dead := it.Attempts+1 >= pushMaxAttempts
		next := m.now().Add(time.Duration(1<<min(it.Attempts, 6)) * 30 * time.Second)
		if serr := m.pushQueue().Settle(ctx, it.ID, false, err.Error(), next, dead); serr != nil {
			return 0, serr
		}
		if dead {
			log.Printf("[微信智能应答] 补推条目 %d 连续失败 %d 次，放弃：%v", it.ID, it.Attempts+1, err)
			m.appLogErr(ctx, "wechat.push.dead", "补推条目连续失败已放弃", err, map[string]any{
				"queue_id": it.ID, "kind": it.Kind, "attempts": it.Attempts + 1,
			})
		}
	}
	return len(items), nil
}

// runQueueLoop 补推轮询。
func (m *Module) runQueueLoop(ctx context.Context) {
	interval := m.QueuePoll
	if interval <= 0 {
		interval = 30 * time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if _, err := m.processQueueOnce(ctx); err != nil && ctx.Err() == nil {
				log.Printf("[微信智能应答] 补推轮询失败：%v", err)
			}
		}
	}
}
