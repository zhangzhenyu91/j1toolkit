package exporter

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// safeName 文件名安全化（vehicle_export.safe_name 逐字语义）：
// 反斜杠/冒号/星号/问号/引号/尖括号/竖线/空白 → 下划线，截断 80 字符。
func safeName(s any) string {
	re := regexp.MustCompile(`[\\/:*?"<>|\s]+`)
	out := re.ReplaceAllString(strAny(s), "_")
	if len(out) > 80 {
		out = out[:80]
	}
	return out
}

// cleanTime '2026-09-01 08:57:06.0' → '2026-09-01 08:57:06'（vehicle_export.clean_time）。
func cleanTime(v any) string {
	s := strings.TrimSpace(strAny(v))
	re := regexp.MustCompile(`(\d{2}:\d{2}:\d{2})\.\d+$`)
	return re.ReplaceAllString(s, "$1")
}

// fmtEpochMs epoch 毫秒 → "YYYY-MM-DD HH:MM:SS"（本地时区）；非法输入原样返回。
func fmtEpochMs(v any) string {
	var ms int64
	switch t := v.(type) {
	case float64:
		ms = int64(t)
	case int64:
		ms = t
	case int:
		ms = int64(t)
	case json.Number:
		n, err := t.Int64()
		if err != nil {
			return strAny(v)
		}
		ms = n
	default:
		return strAny(v)
	}
	return time.UnixMilli(ms).Format("2006-01-02 15:04:05")
}

// roundHalfEven Python round() 口径（四舍六入五成双）。
func roundHalfEven(x float64) float64 {
	f := math.Floor(x)
	d := x - f
	switch {
	case d > 0.5:
		return f + 1
	case d < 0.5:
		return f
	default:
		if math.Mod(f, 2) == 0 {
			return f
		}
		return f + 1
	}
}

// fmtDurCN 时长秒 → "H小时M分钟"（不足 1 小时为 "M分钟"，vehicle_export.fmt_dur_cn）。
func fmtDurCN(seconds int) string {
	h := seconds / 3600
	m := int(roundHalfEven(float64(seconds%3600) / 60))
	if m == 60 {
		h, m = h+1, 0
	}
	if h > 0 {
		return strconv.Itoa(h) + "小时" + strconv.Itoa(m) + "分钟"
	}
	return strconv.Itoa(m) + "分钟"
}

// segEndBeforeNoon 行程段 endtime 在当日 12:00 前则返回规范化时间串，否则 ""。
func segEndBeforeNoon(seg map[string]any) string {
	end := cleanTime(seg["endtime"])
	if t, err := time.ParseInLocation("2006-01-02 15:04:05", cutLen(end, 19), time.Local); err == nil && t.Hour() < 12 {
		return end
	}
	return ""
}

// calibrateNoonEnd 结束时间早于当日 12:00 时按 1 小时逐次叠加至 12 点后，
// 返回（校准后结束时间, 校准前时长秒, 校准后时长秒）；无需校准或不可解析返回 nil。
func calibrateNoonEnd(seg map[string]any) (time.Time, int64, int64, bool) {
	start := cleanTime(seg["starttime"])
	end := cleanTime(seg["endtime"])
	sdt, err1 := time.ParseInLocation("2006-01-02 15:04:05", cutLen(start, 19), time.Local)
	edt, err2 := time.ParseInLocation("2006-01-02 15:04:05", cutLen(end, 19), time.Local)
	if err1 != nil || err2 != nil {
		return time.Time{}, 0, 0, false
	}
	if edt.Hour() >= 12 {
		return time.Time{}, 0, 0, false
	}
	nedt := edt
	for nedt.Hour() < 12 {
		nedt = nedt.Add(time.Hour)
	}
	return nedt, int64(edt.Sub(sdt).Seconds()), int64(nedt.Sub(sdt).Seconds()), true
}

// ---------------------------------------------------------------- map 取值（宽松）

// strAny 任意值 → 字符串（JSON 数值走最短表示，与 Python str 口径一致）。
func strAny(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	case bool:
		if t {
			return "true"
		}
		return "false"
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	case int:
		return strconv.Itoa(t)
	case int64:
		return strconv.FormatInt(t, 10)
	case json.Number:
		return t.String()
	default:
		return ""
	}
}

func mapField(m map[string]any, key string) map[string]any {
	if m == nil {
		return nil
	}
	v, _ := m[key].(map[string]any)
	return v
}

func strField(m map[string]any, key string) string {
	if m == nil {
		return ""
	}
	return strings.TrimSpace(strAny(m[key]))
}

// boolField 宽松布尔：bool 直取；字符串/数值解析；缺省取 def。
func boolField(m map[string]any, key string, def bool) bool {
	if m == nil {
		return def
	}
	v, ok := m[key]
	if !ok || v == nil {
		return def
	}
	switch t := v.(type) {
	case bool:
		return t
	case string:
		s := strings.TrimSpace(strings.ToLower(t))
		switch s {
		case "true", "1", "yes", "on":
			return true
		case "false", "0", "no", "off", "":
			return false
		}
		return def
	case float64:
		return t != 0
	default:
		return def
	}
}

// intField 宽松整数（JSON 数值 / ini 字符串均可）；缺省取 def。
func intField(m map[string]any, key string, def int) int {
	if m == nil {
		return def
	}
	v, ok := m[key]
	if !ok || v == nil {
		return def
	}
	switch t := v.(type) {
	case float64:
		return int(t)
	case int:
		return t
	case int64:
		return int(t)
	case json.Number:
		if n, err := t.Int64(); err == nil {
			return int(n)
		}
		return def
	case string:
		s := strings.TrimSpace(t)
		if n, err := strconv.Atoi(s); err == nil {
			return n
		}
		if f, err := strconv.ParseFloat(s, 64); err == nil {
			return int(f)
		}
		return def
	default:
		return def
	}
}

// msField 毫秒字符串/数值 → Duration（config.ini [export] 各等待参数口径）。
func msField(m map[string]any, key string, defMS int) time.Duration {
	ms := intField(m, key, defMS)
	return time.Duration(ms) * time.Millisecond
}

func cutLen(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

// clampInt 把 v 钳到 [lo, hi]。
func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// ---------------------------------------------------------------- 状态目录

// stateDir 运行状态目录（toolkit/state.py state_dir 同口径）：
// Windows %LOCALAPPDATA%\uvmp-toolkit，其余 $XDG_DATA_HOME 或 ~/.local/share 下 uvmp-toolkit。
func stateDir() string {
	var base string
	if isWindows() {
		base = os.Getenv("LOCALAPPDATA")
		if base == "" {
			home, _ := os.UserHomeDir()
			base = filepath.Join(home, "AppData", "Local")
		}
	} else {
		base = os.Getenv("XDG_DATA_HOME")
		if base == "" {
			home, _ := os.UserHomeDir()
			base = filepath.Join(home, ".local", "share")
		}
	}
	return filepath.Join(base, "uvmp-toolkit")
}

func isWindows() bool {
	return os.PathSeparator == '\\' && os.PathListSeparator == ';'
}

// expandUser 路径开头 ~ 展开为用户主目录（Python Path.expanduser 口径）。
func expandUser(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") || strings.HasPrefix(p, `~\`) {
		home, err := os.UserHomeDir()
		if err == nil {
			return filepath.Join(home, p[1:])
		}
	}
	return p
}
