// main 装配胶水：树障/交跨夜间同步排程（内网 U 盘轮次 + WeKnora 知识库同步，含启停生命周期）
// 与小配置解析。站内通知/人工审核通知/设备链路/派车单同步等一律走 go/internal/apps/bridge 的
// 真实适配，不在本文件重复实现；此处只补 bridge 未覆盖的编排层。
package main

import (
	"context"
	"log"
	"strconv"
	"strings"
	"sync"
	"time"

	"shade/internal/logging"
)

// syncPair 夜间同步编排（同 Node 同时刻串行序：先内网 U 盘取件回写，再 WeKnora 知识库同步）。
type syncPair struct {
	Logger  *logging.Logger
	Cycle   func(context.Context) (string, error) // 内网 U 盘同步轮次（含指令窗口），返回摘要
	WeKnora func(context.Context) (string, error) // WeKnora 知识库快照同步一轮，返回摘要
}

// Run 执行一轮编排：逐段记录结果；未配置/链路未接入的错误在此明确落日志（不假成功、不静默跳过）。
func (p syncPair) Run(ctx context.Context) {
	if summary, err := p.Cycle(ctx); err != nil {
		p.record(ctx, "warn", "sync.intranet.failed", "内网 U 盘同步未完成", err, summary)
	} else {
		p.record(ctx, "info", "sync.intranet.completed", "内网 U 盘同步完成", nil, summary)
	}
	if summary, err := p.WeKnora(ctx); err != nil {
		p.record(ctx, "warn", "sync.weknora.failed", "知识库同步未完成", err, summary)
	} else {
		p.record(ctx, "info", "sync.weknora.completed", "知识库同步完成", nil, summary)
	}
}

// record 结构化应用日志（无 logger 时回退标准日志）。
func (p syncPair) record(ctx context.Context, level, event, messageCN string, err error, summary string) {
	raw := summary
	fields := map[string]any{}
	if err != nil {
		raw = err.Error()
		fields["summary"] = summary
	}
	if p.Logger == nil {
		log.Printf("[夜间同步] %s：%s（%s）", messageCN, raw, event)
		return
	}
	_ = p.Logger.Record(ctx, level, "scheduler", event, messageCN, raw, fields)
}

// scheduleDailyCn 每日 HH:MM（北京时间固定 UTC+8，同 sgccclockin 排程口径）执行 job，
// 执行完自排次日；时刻每次现读（配置变更下一轮生效）。返回停止函数（幂等）。
func scheduleDailyCn(readTime func(context.Context) string, defH, defM int, job func(context.Context)) (stop func()) {
	stopCh := make(chan struct{})
	var once sync.Once
	go func() {
		for {
			h, m := parseHHMMCn(readTime(context.Background()), defH, defM)
			timer := time.NewTimer(time.Until(nextDailyRunCn(time.Now(), h, m)))
			select {
			case <-stopCh:
				timer.Stop()
				return
			case <-timer.C:
			}
			job(context.Background())
		}
	}()
	return func() { once.Do(func() { close(stopCh) }) }
}

// nextDailyRunCn 下次 HH:MM 触发时刻（北京时间固定 UTC+8）。
func nextDailyRunCn(now time.Time, hh, mm int) time.Time {
	cst := time.FixedZone("CST", 8*3600)
	local := now.In(cst)
	next := time.Date(local.Year(), local.Month(), local.Day(), hh, mm, 0, 0, cst)
	if !next.After(local) {
		next = next.Add(24 * time.Hour)
	}
	return next
}

// parseHHMMCn 解析 "HH:MM"（非法回退缺省）。
func parseHHMMCn(s string, defH, defM int) (int, int) {
	parts := strings.Split(strings.TrimSpace(s), ":")
	if len(parts) != 2 {
		return defH, defM
	}
	h, err1 := strconv.Atoi(strings.TrimSpace(parts[0]))
	m, err2 := strconv.Atoi(strings.TrimSpace(parts[1]))
	if err1 != nil || err2 != nil || h < 0 || h > 23 || m < 0 || m > 59 {
		return defH, defM
	}
	return h, m
}

// parseInt64Cfg 运行配置整数值（空/非法回退 fallback）。
func parseInt64Cfg(raw string, fallback int64) int64 {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return fallback
	}
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return fallback
	}
	return n
}
