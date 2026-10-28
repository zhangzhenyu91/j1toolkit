package main

// 装配胶水测试：派车每日排程编排（dispatchPair，fake 作业）与 HH:MM 解析 / 下次触发时刻纯函数。
// 不连任何外部服务。

import (
	"context"
	"errors"
	"testing"
	"time"
)

// TestDispatchPairRun 准备/取件两段各自触发一次；失败段仅记日志不 panic。
func TestDispatchPairRun(t *testing.T) {
	prepared, fetched := 0, 0
	p := dispatchPair{
		Prepare: func(context.Context) (string, error) { prepared++; return "已锁定", nil },
		Fetch:   func(context.Context) (string, error) { fetched++; return "", errors.New("取件失败") },
	}
	p.RunPrepare(context.Background())
	p.RunFetch(context.Background())
	if prepared != 1 || fetched != 1 {
		t.Fatalf("prepared=%d fetched=%d", prepared, fetched)
	}
	// 成功路径同样各触发一次（fake 作业返回摘要）。
	p2 := dispatchPair{
		Prepare: func(context.Context) (string, error) { prepared++; return "设备已锁定", nil },
		Fetch:   func(context.Context) (string, error) { fetched++; return "新建 1 张出车卡片", nil },
	}
	p2.RunPrepare(context.Background())
	p2.RunFetch(context.Background())
	if prepared != 2 || fetched != 2 {
		t.Fatalf("prepared=%d fetched=%d", prepared, fetched)
	}
}

// TestParseHHMMCn 配置时刻解析（非法回退缺省 09:10/09:20 口径）。
func TestParseHHMMCn(t *testing.T) {
	if h, m := parseHHMMCn("09:10", 23, 50); h != 9 || m != 10 {
		t.Fatalf("got %d:%02d", h, m)
	}
	if h, m := parseHHMMCn("09:20", 9, 10); h != 9 || m != 20 {
		t.Fatalf("got %d:%02d", h, m)
	}
	if h, m := parseHHMMCn("bad", 9, 20); h != 9 || m != 20 {
		t.Fatalf("fallback got %d:%02d", h, m)
	}
	if h, m := parseHHMMCn("25:99", 9, 20); h != 9 || m != 20 {
		t.Fatalf("range fallback got %d:%02d", h, m)
	}
}

// TestNextDailyRunCn 当日未到点取当日、过点取次日（北京时间固定 UTC+8 口径）。
func TestNextDailyRunCn(t *testing.T) {
	cst := time.FixedZone("CST", 8*3600)
	next := nextDailyRunCn(time.Date(2026, 10, 28, 8, 0, 0, 0, cst), 9, 10)
	if next.Day() != 28 || next.Hour() != 9 || next.Minute() != 10 {
		t.Fatalf("next = %v", next)
	}
	next = nextDailyRunCn(time.Date(2026, 10, 28, 10, 0, 0, 0, cst), 9, 20)
	if next.Day() != 29 || next.Hour() != 9 || next.Minute() != 20 {
		t.Fatalf("next = %v", next)
	}
}
