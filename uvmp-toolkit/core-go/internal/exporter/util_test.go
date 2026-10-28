package exporter

import (
	"testing"
	"time"
)

// TestSafeName safe_name：路径/空白字符替换为下划线，截断 80。
func TestSafeName(t *testing.T) {
	cases := map[string]string{
		"晋AD10001":              "晋AD10001",
		"a/b\\c:d*e?f\"g<h>i|j": "a_b_c_d_e_f_g_h_i_j",
		"2026-10-28":            "2026-10-28",
		"a b\tc":                "a_b_c",
		"":                      "",
	}
	for in, want := range cases {
		if got := safeName(in); got != want {
			t.Fatalf("safeName(%q)=%q want %q", in, got, want)
		}
	}
	long := ""
	for i := 0; i < 100; i++ {
		long += "x"
	}
	if len(safeName(long)) != 80 {
		t.Fatal("应截断 80 字符")
	}
}

// TestCleanTime 尾部毫秒去除。
func TestCleanTime(t *testing.T) {
	cases := map[string]string{
		"2026-09-01 08:57:06.0":   "2026-09-01 08:57:06",
		"2026-09-01 08:57:06.123": "2026-09-01 08:57:06",
		"2026-09-01 08:57:06":     "2026-09-01 08:57:06",
		" 2026-09-01 08:57:06.0 ": "2026-09-01 08:57:06",
		"":                        "",
		"2026-09-01 08:57:06.0.1": "2026-09-01 08:57:06.0.1",
	}
	for in, want := range cases {
		if got := cleanTime(in); got != want {
			t.Fatalf("cleanTime(%q)=%q want %q", in, got, want)
		}
	}
}

// TestFmtDurCN 时长中文格式（Python round 口径）。
func TestFmtDurCN(t *testing.T) {
	cases := map[int]string{
		0:     "0分钟",
		59:    "1分钟",
		60:    "1分钟",
		3600:  "1小时0分钟",
		3660:  "1小时1分钟",
		16200: "4小时30分钟",
		5400:  "1小时30分钟",
	}
	for sec, want := range cases {
		if got := fmtDurCN(sec); got != want {
			t.Fatalf("fmtDurCN(%d)=%q want %q", sec, got, want)
		}
	}
}

// TestSegEndBeforeNoon 12 点前判定边界。
func TestSegEndBeforeNoon(t *testing.T) {
	if got := segEndBeforeNoon(map[string]any{"endtime": "2026-10-28 11:59:59.0"}); got != "2026-10-28 11:59:59" {
		t.Fatalf("11:59:59 应命中: %q", got)
	}
	if got := segEndBeforeNoon(map[string]any{"endtime": "2026-10-28 12:00:00"}); got != "" {
		t.Fatalf("12:00:00 不应命中: %q", got)
	}
	if got := segEndBeforeNoon(nil); got != "" {
		t.Fatalf("空段不应命中: %q", got)
	}
	if got := segEndBeforeNoon(map[string]any{"endtime": "bad"}); got != "" {
		t.Fatalf("非法时间不应命中: %q", got)
	}
}

// TestCalibrateNoonEnd 自动校准：按 1 小时逐次叠加至 12 点后，时长同步增加。
func TestCalibrateNoonEnd(t *testing.T) {
	seg := map[string]any{"starttime": "2026-10-28 08:00:00", "endtime": "2026-10-28 10:30:00"}
	newDT, oldDur, newDur, ok := calibrateNoonEnd(seg)
	if !ok {
		t.Fatal("应可校准")
	}
	if newDT.Format("2006-01-02 15:04:05") != "2026-10-28 12:30:00" {
		t.Fatalf("校准后结束时间不符: %v", newDT)
	}
	if oldDur != 9000 || newDur != 16200 {
		t.Fatalf("时长不符: %d %d", oldDur, newDur)
	}
	seg["endtime"] = "2026-10-28 13:00:00"
	if _, _, _, ok := calibrateNoonEnd(seg); ok {
		t.Fatal("12 点后无需校准")
	}
	seg["endtime"] = "2026-10-28 11:00:00"
	newDT, _, _, ok = calibrateNoonEnd(seg)
	if !ok || newDT.Hour() < 12 {
		t.Fatalf("校准后应过 12 点: %v", newDT)
	}
}

// TestRoundHalfEven Python round() 半值取偶。
func TestRoundHalfEven(t *testing.T) {
	cases := map[float64]float64{
		0.5: 0, 1.5: 2, 2.5: 2, 3.5: 4, 0.4: 0, 0.6: 1,
	}
	for in, want := range cases {
		if got := roundHalfEven(in); got != want {
			t.Fatalf("roundHalfEven(%v)=%v want %v", in, got, want)
		}
	}
}

// TestExpandUser ~ 展开。
func TestExpandUser(t *testing.T) {
	got := expandUser("~/x")
	if got == "~/x" {
		t.Fatal("~ 应展开")
	}
	if expandUser("/abs/path") != "/abs/path" {
		t.Fatal("绝对路径不应变化")
	}
}

// TestMsFieldWaitMapping 30s 地图 / 6s 派车单等待配置映射（含下限）。
func TestMsFieldWaitMapping(t *testing.T) {
	exp := map[string]any{"map_min_wait": "30000", "pcd_render_wait": "6000", "map_tiles_wait": "1000"}
	if got := msField(exp, "map_min_wait", 30000); got != 30*time.Second {
		t.Fatalf("map_min_wait 映射不符: %v", got)
	}
	if got := msField(exp, "pcd_render_wait", 6000); got != 6*time.Second {
		t.Fatalf("pcd_render_wait 映射不符: %v", got)
	}
	if got := maxDur(3*time.Second, msField(exp, "map_tiles_wait", 12000)); got != 3*time.Second {
		t.Fatalf("map_tiles_wait 下限不符: %v", got)
	}
	if got := msField(nil, "map_min_wait", 30000); got != 30*time.Second {
		t.Fatalf("缺省 30s 不符: %v", got)
	}
}
