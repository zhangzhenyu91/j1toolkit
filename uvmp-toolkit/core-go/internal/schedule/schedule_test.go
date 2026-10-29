package schedule

import (
	"strings"
	"testing"
	"time"

	"github.com/zhangzhenyu91/j1toolkit/uvmp-toolkit/core-go/internal/config"
	"github.com/zhangzhenyu91/j1toolkit/uvmp-toolkit/core-go/internal/state"
)

func setupState(t *testing.T) {
	t.Helper()
	state.SetRoot(t.TempDir())
}

func cfgWith(enabled bool, tm, driver string) config.Dict {
	return config.Dict{
		"schedule": config.Dict{"enabled": enabled, "time": tm, "driver": driver},
	}
}

func TestParseHHMM(t *testing.T) {
	cases := []struct {
		in   string
		h, m int
	}{
		{"09:15", 9, 15},
		{" 23:55 ", 23, 55},
		{"9:05", 9, 5},
		{"24:00", 9, 15}, // 非法回退
		{"09:60", 9, 15},
		{"abc", 9, 15},
		{"", 9, 15},
	}
	for _, c := range cases {
		h, m := ParseHHMM(c.in)
		if h != c.h || m != c.m {
			t.Fatalf("ParseHHMM(%q) = %d:%02d, want %d:%02d", c.in, h, m, c.h, c.m)
		}
	}
}

func TestDueCheckDriverGate(t *testing.T) {
	setupState(t)
	now := time.Date(2026, 10, 28, 10, 0, 0, 0, time.Local)
	due, reason, today := DueCheck(cfgWith(true, "09:15", "app"), now, "os", "", nil)
	if due || reason != "由客户端驻留调度" || today != "2026-10-28" {
		t.Fatalf("driver 门闸: due=%v reason=%q today=%q", due, reason, today)
	}
	due, reason, _ = DueCheck(cfgWith(true, "09:15", "os"), now, "app", "", nil)
	if due || reason != "由系统定时器调度" {
		t.Fatalf("driver 门闸(反向): due=%v reason=%q", due, reason)
	}
}

func TestDueCheckDisabledAndNotYet(t *testing.T) {
	setupState(t)
	now := time.Date(2026, 10, 28, 10, 0, 0, 0, time.Local)
	due, reason, _ := DueCheck(cfgWith(false, "09:15", "os"), now, "os", "", nil)
	if due || reason != "定时已停用" {
		t.Fatalf("disabled: due=%v reason=%q", due, reason)
	}
	now = time.Date(2026, 10, 28, 9, 0, 0, 0, time.Local)
	due, reason, _ = DueCheck(cfgWith(true, "09:15", "os"), now, "os", "", nil)
	if due || !strings.Contains(reason, "未到设定时间 09:15") {
		t.Fatalf("not yet: due=%v reason=%q", due, reason)
	}
}

func TestDueCheckSuccessStampBlocks(t *testing.T) {
	setupState(t)
	now := time.Date(2026, 10, 28, 10, 0, 0, 0, time.Local)
	MarkSuccess("2026-10-28", "", map[string]any{"count": 1})
	due, reason, _ := DueCheck(cfgWith(true, "09:15", "os"), now, "os", "", nil)
	if due || !strings.Contains(reason, "今日已成功执行") {
		t.Fatalf("已成功: due=%v reason=%q", due, reason)
	}
	// 换一天不受影响
	due, _, _ = DueCheck(cfgWith(true, "09:15", "os"), time.Date(2026, 10, 29, 10, 0, 0, 0, time.Local), "os", "", nil)
	if !due {
		t.Fatal("次日应放行")
	}
}

func TestDueCheckAttemptInterval(t *testing.T) {
	setupState(t)
	base := time.Date(2026, 10, 28, 9, 20, 0, 0, time.Local)
	MarkAttempt("2026-10-28", "")
	due, reason, _ := DueCheck(cfgWith(true, "09:15", "os"), base, "os", "", nil)
	if due || !strings.Contains(reason, "距上次尝试不足 10 分钟") {
		t.Fatalf("间隔闸: due=%v reason=%q", due, reason)
	}
	// 超过 10 分钟放行（now 是 base 的 11 分钟后；MarkAttempt 用的是真实时钟，
	// 这里改写戳时间为过去值模拟）
	state.SetStamp("daily_export_attempt", map[string]any{
		"date": "2026-10-28",
		"time": base.Add(-11 * time.Minute).Format("2006-01-02 15:04:05"),
	})
	due, _, _ = DueCheck(cfgWith(true, "09:15", "os"), base, "os", "", nil)
	if !due {
		t.Fatalf("超 10 分钟应放行, reason=%q", mustReason(cfgWith(true, "09:15", "os"), base))
	}
}

func mustReason(cfg config.Dict, now time.Time) string {
	_, reason, _ := DueCheck(cfg, now, "os", "", nil)
	return reason
}

func TestDueCheckCustomStampName(t *testing.T) {
	setupState(t)
	now := time.Date(2026, 10, 28, 23, 56, 0, 0, time.Local)
	sch := Schedule{Enabled: true, Time: "23:55", Driver: "os"}
	due, _, today := DueCheck(nil, now, "os", "tree_sync_nightly", &sch)
	if !due {
		t.Fatal("夜间兜底戳初始应放行")
	}
	MarkSuccess(today, "tree_sync_nightly", map[string]any{"kind": "snapshot"})
	due, _, _ = DueCheck(nil, now, "os", "tree_sync_nightly", &sch)
	if due {
		t.Fatal("写戳后应跳过")
	}
	// 不共用 daily_export 戳
	due, _, _ = DueCheck(nil, now, "os", "", nil)
	if !due {
		t.Fatal("daily_export 戳不应受影响")
	}
}

func TestNextRunText(t *testing.T) {
	setupState(t)
	if got := NextRunText(cfgWith(false, "09:15", "os"), time.Now()); got != "已停用" {
		t.Fatalf("停用文案 = %q", got)
	}
	now := time.Date(2026, 10, 28, 8, 0, 0, 0, time.Local)
	got := NextRunText(cfgWith(true, "09:15", "os"), now)
	if got != "2026-10-28 09:15" {
		t.Fatalf("NextRunText = %q", got)
	}
	// 过点未跑：随时
	now = time.Date(2026, 10, 28, 10, 0, 0, 0, time.Local)
	got = NextRunText(cfgWith(true, "09:15", "os"), now)
	if got != "随时（已到过点，等待定时器触发）" {
		t.Fatalf("NextRunText 过点 = %q", got)
	}
	// 过点已跑：明天
	MarkSuccess("2026-10-28", "", nil)
	got = NextRunText(cfgWith(true, "09:15", "os"), now)
	if got != "2026-10-29 09:15" {
		t.Fatalf("NextRunText 次日 = %q", got)
	}
}

func TestFromConfigDefaults(t *testing.T) {
	s := FromConfig(config.Dict{})
	if !s.Enabled || s.Time != "09:15" || s.Driver != "os" {
		t.Fatalf("缺省时刻表 = %+v", s)
	}
}
