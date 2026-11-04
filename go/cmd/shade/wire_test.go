package main

// 装配胶水测试：派车每日排程编排（dispatchPair，fake 作业）与 HH:MM 解析 / 下次触发时刻纯函数。
// 不连任何外部服务。

import (
	"context"
	"errors"
	"testing"
	"time"
)

// TestDispatchPairRun 单轮作业触发一次；失败只记日志不 panic。
func TestDispatchPairRun(t *testing.T) {
	prepared := 0
	p := dispatchPair{
		Prepare: func(context.Context) (string, error) { prepared++; return "已完成", nil },
	}
	p.RunPrepare(context.Background())
	if prepared != 1 {
		t.Fatalf("prepared=%d", prepared)
	}
	// 失败路径同样只触发一次并记录，不 panic。
	p2 := dispatchPair{
		Prepare: func(context.Context) (string, error) { prepared++; return "", errors.New("取件失败") },
	}
	p2.RunPrepare(context.Background())
	if prepared != 2 {
		t.Fatalf("prepared=%d", prepared)
	}
}

// TestParseHHMMCn 配置时刻解析（非法回退传入缺省口径）。
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
