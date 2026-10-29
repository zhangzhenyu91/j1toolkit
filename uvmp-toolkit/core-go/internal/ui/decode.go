package ui

import (
	"fmt"
	"strconv"
	"strings"
)

// 本文件是状态字典（getAppStatus 返回的 map）与配置补丁的强类型解码器/序列化器：
// 纯函数、无 IO，展示层与冒烟测试共用。

// ScheduleInfo 解码 schedule 段。
func decodeSchedule(m map[string]any) ScheduleInfo {
	return ScheduleInfo{
		Enabled: asBool(m["enabled"]),
		Time:    asString(m["time"]),
		Driver:  orDefault(asString(m["driver"]), "os"),
		NextRun: asString(m["next_run"]),
	}
}

// StampInfo 解码 stamp 段（上次成功戳）。
func decodeStamp(m map[string]any) StampInfo {
	return StampInfo{
		Time:  asString(m["time"]),
		Count: asInt(m["count"]),
		File:  asString(m["file"]),
	}
}

// LastRunInfo 解码 last_run 段（最近一次任务）。
func decodeLastRun(m map[string]any) LastRunInfo {
	return LastRunInfo{
		Time:   asString(m["time"]),
		Status: JobStatus(asString(m["status"])),
		Error:  asString(m["error"]),
	}
}

// USBInfo 解码 usb 段。
func decodeUSB(m map[string]any) USBInfo {
	return USBInfo{
		Label:       asString(m["label"]),
		Mount:       asString(m["mount"]),
		FallbackDir: asString(m["fallback_dir"]),
	}
}

// ScheduleInfo 应用状态里的调度段。
type ScheduleInfo struct {
	Enabled bool
	Time    string
	Driver  string
	NextRun string
}

// StampInfo 应用状态里的成功戳段。
type StampInfo struct {
	Time  string
	Count int
	File  string
}

// LastRunInfo 应用状态里的最近任务段。
type LastRunInfo struct {
	Time   string
	Status JobStatus
	Error  string
}

// USBInfo 应用状态里的 U 盘段。
type USBInfo struct {
	Label       string
	Mount       string
	FallbackDir string
}

// DailyStatus 派车同步状态卡（HomePage / DailySyncPage 共用）。
type DailyStatus struct {
	Stamp     StampInfo
	LastRun   LastRunInfo
	Schedule  ScheduleInfo
	USB       USBInfo
	DueReason string
}

// DailyStatusFrom 解码 getAppStatus(daily_sync) 状态字典。
func DailyStatusFrom(m map[string]any) DailyStatus {
	due, _ := m["due"].(map[string]any)
	return DailyStatus{
		Stamp:     decodeStamp(asMap(m["stamp"])),
		LastRun:   decodeLastRun(asMap(m["last_run"])),
		Schedule:  decodeSchedule(asMap(m["schedule"])),
		USB:       decodeUSB(asMap(m["usb"])),
		DueReason: asString(due["reason"]),
	}
}

// SyncStatus 树障/交跨统计表同步状态卡。
type SyncStatus struct {
	LocalPath   string
	LocalExists bool
	USBMount    string
	FileName    string
	NightlyTime string
	Stamp       StampInfo
	LastRun     LastRunInfo
}

// SyncStatusFrom 解码 getAppStatus(tree_sync/crossing_sync) 状态字典；
// 段名按应用 id 取（tree_sync / crossing_sync）。
func SyncStatusFrom(appID string, m map[string]any) SyncStatus {
	sec := asMap(m[appID])
	if sec == nil {
		sec = asMap(m["sync"]) // 兼容统一 sync 段的实现
	}
	s := SyncStatus{
		LocalPath:   asString(sec["local_path"]),
		LocalExists: asBool(sec["local_exists"]),
		FileName:    asString(sec["file_name"]),
		NightlyTime: asString(sec["nightly_time"]),
		Stamp:       decodeStamp(asMap(m["stamp"])),
		LastRun:     decodeLastRun(asMap(m["last_run"])),
	}
	s.USBMount = asString(asMap(m["usb"])["mount"])
	return s
}

// Configured 内网表路径是否已配置。
func (s SyncStatus) Configured() bool { return s.LocalPath != "" }

// CourseStatusFrom 解码 getAppStatus(course_autoplay) 状态字典。
func CourseStatusFrom(m map[string]any) CourseStatus {
	return CourseStatus{
		CDPURL:      asString(m["cdp_url"]),
		Browser:     asString(m["browser"]),
		Connected:   asBool(m["connected"]),
		Pages:       asCount(m["pages"]),
		Playwright:  asBool(m["playwright"]),
		ActiveLists: asCount(m["active_lists"]),
		StartURL:    asString(m["start_url"]),
	}
}

// CourseStatusToMap 编码回状态字典（FakeBackend 用）。
func CourseStatusToMap(c CourseStatus) map[string]any {
	return map[string]any{
		"cdp_url":      c.CDPURL,
		"browser":      c.Browser,
		"connected":    c.Connected,
		"pages":        c.Pages,
		"playwright":   c.Playwright,
		"active_lists": c.ActiveLists,
		"start_url":    c.StartURL,
	}
}

// CourseCountText 课程结果计数文案。
func CourseCountText(r CourseResult) string {
	return fmt.Sprintf("总数 %d；完成 %d；未完成 %d；跳过 %d", r.Total, r.Completed, r.Pending, r.Skipped)
}

// CourseResultFrom 从任务 result 解码课程计数。
func CourseResultFrom(m map[string]any) CourseResult {
	return CourseResult{
		Total:     asInt(m["total"]),
		Completed: asInt(m["completed"]),
		Pending:   asInt(m["pending"]),
		Skipped:   asInt(m["skipped"]),
		Processed: asInt(m["processed"]),
		Paused:    asBool(m["paused"]),
	}
}

// PcdResultFrom 轨迹导出任务 result 摘要字段。
type PcdResult struct {
	Total     int
	PCDOk     int
	PCDFail   int
	TrackOk   int
	TrackFail int
	NotFound  []string
	Dir       string
}

// PcdResultFrom 解码 pcd_export 任务 result。
func PcdResultFrom(m map[string]any) PcdResult {
	r := PcdResult{
		Total:     asInt(m["total"]),
		PCDOk:     asInt(m["pcd_ok"]),
		PCDFail:   asInt(m["pcd_fail"]),
		TrackOk:   asInt(m["track_ok"]),
		TrackFail: asInt(m["track_fail"]),
		Dir:       asString(m["dir"]),
	}
	if nf, ok := m["not_found"].([]any); ok {
		for _, v := range nf {
			r.NotFound = append(r.NotFound, asString(v))
		}
	}
	if nf, ok := m["not_found"].([]string); ok {
		r.NotFound = nf
	}
	return r
}

// DailyResultFrom 派车同步任务 result 摘要字段。
type DailyResult struct {
	Count int
	File  string
	Dest  string
	Dir   string
}

// DailyResultFrom 解码 daily_sync 任务 result。
func DailyResultFrom(m map[string]any) DailyResult {
	return DailyResult{
		Count: asInt(m["count"]),
		File:  asString(m["file"]),
		Dest:  asString(m["dest"]),
		Dir:   asString(m["dir"]),
	}
}

// SyncRunResultFrom 树障/交跨同步任务 result 摘要字段。
type SyncRunResult struct {
	Skipped string
	Kind    string
	Nightly bool
	CycleID string
	File    string
}

// SyncRunResultFrom 解码 tree_sync/crossing_sync 任务 result。
func SyncRunResultFrom(m map[string]any) SyncRunResult {
	return SyncRunResult{
		Skipped: asString(m["skipped"]),
		Kind:    orDefault(asString(m["kind"]), asString(m["action"])),
		Nightly: asBool(m["nightly"]),
		CycleID: asString(m["cycle_id"]),
		File:    asString(m["file"]),
	}
}

// PcdSummary 轨迹导出历史摘要文案。
func PcdSummary(r PcdResult) string {
	parts := []string{
		fmt.Sprintf("共 %d 单", r.Total),
		fmt.Sprintf("派车单 成功 %d/失败 %d", r.PCDOk, r.PCDFail),
		fmt.Sprintf("轨迹 成功 %d/失败 %d", r.TrackOk, r.TrackFail),
	}
	if len(r.NotFound) > 0 {
		parts = append(parts, fmt.Sprintf("未找到 %d", len(r.NotFound)))
	}
	return strings.Join(parts, "；")
}

// DailySummary 派车同步历史摘要文案。
func DailySummary(r DailyResult) string {
	if r.Count == 0 {
		return "当日无派车数据"
	}
	s := fmt.Sprintf("%d 条 → %s", r.Count, r.File)
	if r.Dest != "" {
		s += "（" + r.Dest + "）"
	}
	return s
}

// SyncSummary 树障/交跨同步历史摘要文案。
func SyncSummary(r SyncRunResult) string {
	switch r.Skipped {
	case "usb_absent":
		return "U盘未挂载，跳过"
	case "no_command":
		return "U盘无指令，跳过"
	case "unknown_kind":
		return fmt.Sprintf("未知指令 kind=%s（已跳过）", r.Kind)
	case "bad_file":
		return "指令 file 字段非法（已跳过）"
	}
	act := map[string]string{"snapshot": "内网表 → U盘", "replace": "U盘新表 → 内网"}[r.Kind]
	if act == "" {
		act = r.Kind
	}
	if r.Nightly {
		act += "（夜间兜底）"
	}
	if r.CycleID != "" {
		act += " cycle=" + r.CycleID
	}
	if r.File != "" {
		act += " → " + r.File
	}
	return act
}

// Map 序列化为 RPC saveConfig 的嵌套局部更新（密码空=不修改，剥除 password 键）。
func (p ConfigPatch) Map() map[string]any {
	out := map[string]any{}
	put := func(key string, v any) {
		if v != nil {
			out[key] = v
		}
	}
	if s := p.SSO; s != nil {
		m := map[string]any{"username": s.Username}
		if s.Password != "" {
			m["password"] = s.Password
		}
		put("sso", m)
	}
	if r := p.Render; r != nil {
		put("render", map[string]any{
			"chrome":                    r.Chrome,
			"builtin":                   r.Builtin,
			"ignore_certificate_errors": r.IgnoreCertificateErrors,
			"certificate_spki_pins":     r.CertificateSPKIPins,
		})
	}
	if o := p.OrderExport; o != nil {
		put("order_export", map[string]any{
			"query_workers":  o.QueryWorkers,
			"render_workers": o.RenderWorkers,
		})
	}
	if s := p.Schedule; s != nil {
		put("schedule", map[string]any{
			"enabled": s.Enabled,
			"time":    s.Time,
			"driver":  s.Driver,
		})
	}
	if u := p.UI; u != nil {
		put("ui", map[string]any{
			"close_to_tray": u.CloseToTray,
			"autostart":     u.Autostart,
		})
	}
	if d := p.Dev; d != nil {
		put("dev", map[string]any{"mock": d.Mock})
	}
	if u := p.USB; u != nil {
		put("usb", map[string]any{"label": u.Label, "fallback_dir": u.FallbackDir})
	}
	if s := p.TreeSync; s != nil {
		put("tree_sync", map[string]any{
			"local_path":   s.LocalPath,
			"file_name":    s.FileName,
			"nightly_time": s.NightlyTime,
		})
	}
	if s := p.CrossingSync; s != nil {
		put("crossing_sync", map[string]any{
			"local_path":   s.LocalPath,
			"file_name":    s.FileName,
			"nightly_time": s.NightlyTime,
		})
	}
	if b := p.BrowserAutomation; b != nil {
		put("browser_automation", map[string]any{
			"cdp_url":      b.CDPURL,
			"profile_dir":  b.ProfileDir,
			"start_url":    b.StartURL,
			"step_timeout": b.StepTimeout,
		})
	}
	return out
}

// PatchSchedule 构造调度段补丁。
func PatchSchedule(enabled bool, timeHHMM, driver string) ConfigPatch {
	return ConfigPatch{Schedule: &ScheduleConfig{Enabled: enabled, Time: timeHHMM, Driver: driver}}
}

// ---- 小型 any 取值助手 ----

func asString(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case fmt.Stringer:
		return t.String()
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	case int:
		return strconv.Itoa(t)
	case int64:
		return strconv.FormatInt(t, 10)
	case bool:
		return strconv.FormatBool(t)
	default:
		return ""
	}
}

func asBool(v any) bool {
	switch t := v.(type) {
	case bool:
		return t
	case string:
		return t == "true" || t == "1"
	case float64:
		return t != 0
	case int:
		return t != 0
	default:
		return false
	}
}

func asInt(v any) int {
	switch t := v.(type) {
	case int:
		return t
	case int64:
		return int(t)
	case float64:
		return int(t)
	case string:
		n, _ := strconv.Atoi(strings.TrimSpace(t))
		return n
	default:
		return 0
	}
}

func asMap(v any) map[string]any {
	m, _ := v.(map[string]any)
	return m
}

// asCount 兼容数字或数组两种形状（course_autoplay 的 pages / active_lists）。
func asCount(v any) int {
	switch t := v.(type) {
	case []any:
		return len(t)
	case []string:
		return len(t)
	default:
		return asInt(v)
	}
}

func orDefault(v, def string) string {
	if v == "" {
		return def
	}
	return v
}
