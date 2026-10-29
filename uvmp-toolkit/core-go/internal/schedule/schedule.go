// Package schedule due-check 调度判定（移植自 toolkit/scheduler.py）。
//
// 设计要点（见《内网工具箱开发指南.md》调度规范）：
// OS 级定时器（systemd timer / Windows 计划任务）每分钟唤醒一次
// `uvmp-core daily-export --if-due`，本模块判定「已到配置时刻且今日未成功」才执行。
// 好处：GUI 改时间只写 config.json，不需要 root 改 unit 文件；
// 机器关机错过到点后，开机下一 tick 自动补跑。
//
// 分钟级 tick 下失败任务若每分钟重跑会高频打 SSO 登录（有锁定风险），
// 故加「距上次尝试 ≥10 分钟」尝试间隔闸（daily_export_attempt 戳）。
package schedule

import (
	"fmt"
	"regexp"
	"strconv"
	"time"

	"github.com/zhangzhenyu91/j1toolkit/uvmp-toolkit/core-go/internal/config"
	"github.com/zhangzhenyu91/j1toolkit/uvmp-toolkit/core-go/internal/state"
)

// STAMP_NAME 每日派车单同步的成功戳。
const STAMP_NAME = "daily_export"

// ATTEMPT_STAMP 上次尝试戳（无论成败，到点放行前记录）。
const ATTEMPT_STAMP = "daily_export_attempt"

// ATTEMPT_MIN_INTERVAL 两次自动尝试的最小间隔（秒），与原 10 分钟轮询节奏一致。
const ATTEMPT_MIN_INTERVAL = 600

// Schedule 判定用时刻表（键位同 config["schedule"]：enabled/time/driver）。
type Schedule struct {
	Enabled bool
	Time    string // "HH:MM"
	Driver  string // "os"=系统定时器 / "app"=客户端驻留
}

// FromConfig 从配置字典取时刻表（键位 enabled/time/driver，缺省 true/"09:15"/"os"）。
func FromConfig(cfg config.Dict) Schedule {
	return FromMap(config.MapField(cfg, "schedule"))
}

// FromMap 从 schedule 子字典取时刻表。
func FromMap(m config.Dict) Schedule {
	return Schedule{
		Enabled: config.BoolField(m, "enabled", true),
		Time:    config.StrFieldDefault(m, "time", "09:15"),
		Driver:  config.StrFieldDefault(m, "driver", "os"),
	}
}

var hhmmRe = regexp.MustCompile(`^\s*(\d{1,2}):(\d{2})\s*$`)

// ParseHHMM 解析 'HH:MM'，非法输入回退 09:15（Python scheduler.parse_hhmm 同口径）。
func ParseHHMM(s string) (h, mi int) {
	if m := hhmmRe.FindStringSubmatch(s); m != nil {
		hh, _ := strconv.Atoi(m[1])
		mm, _ := strconv.Atoi(m[2])
		if hh >= 0 && hh <= 23 && mm >= 0 && mm <= 59 {
			return hh, mm
		}
	}
	return 9, 15
}

// DueCheck 返回 (是否应执行, 原因, 今日日期串)。
// driver 门闸：schedule.Driver=os（系统定时器执行）/ app（客户端驻留执行）。
// 客户端驻留模式下 OS 定时器的到点调用一律跳过（反之亦然），杜绝双跑。
// stampName：新定时应用换自己的戳名（指南调度规范），空保持 daily_export 兼容。
// sch 为 nil 时取 cfg["schedule"]。
func DueCheck(cfg config.Dict, now time.Time, driver, stampName string, sch *Schedule) (bool, string, string) {
	if now.IsZero() {
		now = time.Now()
	}
	today := now.Format("2006-01-02")
	if stampName == "" {
		stampName = STAMP_NAME
	}
	var s Schedule
	if sch != nil {
		s = *sch
	} else {
		s = FromConfig(cfg)
	}
	want := s.Driver
	if want == "" {
		want = "os"
	}
	if want != driver {
		if want == "app" {
			return false, "由客户端驻留调度", today
		}
		return false, "由系统定时器调度", today
	}
	if !s.Enabled {
		return false, "定时已停用", today
	}
	st := state.GetStamp(stampName)
	if st != nil {
		if d, _ := st["date"].(string); d == today {
			tm, _ := st["time"].(string)
			return false, fmt.Sprintf("今日已成功执行（%s）", tm), today
		}
	}
	h, mi := ParseHHMM(s.Time)
	if now.Hour() < h || (now.Hour() == h && now.Minute() < mi) {
		return false, fmt.Sprintf("未到设定时间 %02d:%02d", h, mi), today
	}
	att := state.GetStamp(stampName + "_attempt")
	if att != nil {
		if d, _ := att["date"].(string); d == today {
			if tm, _ := att["time"].(string); tm != "" {
				if last, err := time.ParseInLocation("2006-01-02 15:04:05", tm, time.Local); err == nil {
					if now.Sub(last).Seconds() < ATTEMPT_MIN_INTERVAL {
						return false,
							fmt.Sprintf("距上次尝试不足 %d 分钟（%s）", ATTEMPT_MIN_INTERVAL/60, tm),
							today
					}
				}
			}
		}
	}
	return true, fmt.Sprintf("已到设定时间 %02d:%02d", h, mi), today
}

// MarkSuccess 写成功戳（date/time + extra），stampName 空保持 daily_export 兼容。
func MarkSuccess(today, stampName string, extra map[string]any) {
	data := map[string]any{"date": today, "time": time.Now().Format("2006-01-02 15:04:05")}
	for k, v := range extra {
		data[k] = v
	}
	name := stampName
	if name == "" {
		name = STAMP_NAME
	}
	state.SetStamp(name, data)
}

// MarkAttempt 到点放行后、执行前调用：拉开分钟级 tick 下的失败重试间隔。
func MarkAttempt(today, stampName string) {
	name := stampName
	if name == "" {
		name = STAMP_NAME
	}
	state.SetStamp(name+"_attempt", map[string]any{
		"date": today,
		"time": time.Now().Format("2006-01-02 15:04:05"),
	})
}

// NextRunText GUI 状态卡显示用：下一次定时执行的时间描述。
func NextRunText(cfg config.Dict, now time.Time) string {
	s := FromConfig(cfg)
	if !s.Enabled {
		return "已停用"
	}
	if now.IsZero() {
		now = time.Now()
	}
	h, mi := ParseHHMM(s.Time)
	// 与 Python next_run_text 一致：due-check 以 driver="os" 探测「过点且今日未跑」
	due, _, _ := DueCheck(cfg, now, "os", "", nil)
	if due {
		return "随时（已到过点，等待定时器触发）"
	}
	target := time.Date(now.Year(), now.Month(), now.Day(), h, mi, 0, 0, now.Location())
	if !target.After(now) {
		target = target.Add(24 * time.Hour)
	}
	return target.Format("2006-01-02 15:04")
}
