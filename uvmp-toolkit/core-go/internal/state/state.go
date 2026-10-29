// Package state 运行状态目录（移植自 toolkit/state.py；遵循 XDG，Windows 用 %LOCALAPPDATA%）。
//
// 存放：定时运行戳（stamps/）、任务历史（history/<app_id>.jsonl）、任务日志（logs/）、
// 任务档案（jobs/）。root 定时器与桌面用户的 HOME 不同，状态天然分离：
// GUI 里手动跑一次不会抑制 root 定时器当日的 due-check 判定。
//
// 环境变量 UVMP_TOOLKIT_STATE_DIR 可显式指定状态根目录（Go 侧扩展，开发/测试隔离用）。
package state

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"
)

// APP_DIR_NAME 状态目录名（与 Python 侧一致）。
const APP_DIR_NAME = "uvmp-toolkit"

// Store 一个状态根目录上的操作集（测试可注入独立根目录）。
type Store struct {
	// Root 状态根目录；空则按平台默认解析并创建。
	Root string
	// Getenv 环境变量读取（测试注入）；空则 os.Getenv。
	Getenv func(string) string
}

// pkgDefault 包级默认实例（rpc/CLI 用；测试用 Store{Root: t.TempDir()} + SetRoot）。
var pkgDefault = &Store{}

// Default 返回包级共享 Store（与包级函数同状态根）。
func Default() *Store { return pkgDefault }

func (s *Store) getenv(key string) string {
	if s.Getenv != nil {
		return s.Getenv(key)
	}
	return os.Getenv(key)
}

// RootDir 状态根目录（懒创建）。优先级：显式 Root > UVMP_TOOLKIT_STATE_DIR > 平台默认。
func (s *Store) RootDir() string {
	root := s.Root
	if root == "" {
		root = strings.TrimSpace(s.getenv("UVMP_TOOLKIT_STATE_DIR"))
	}
	if root == "" {
		root = filepath.Join(stateBase(s.getenv), APP_DIR_NAME)
	}
	_ = os.MkdirAll(root, 0o755)
	return root
}

// SetRoot 显式指定状态根目录（测试/嵌入用）。
func (s *Store) SetRoot(dir string) { s.Root = dir }

func (s *Store) sub(name string) string {
	d := filepath.Join(s.RootDir(), name)
	_ = os.MkdirAll(d, 0o755)
	return d
}

// LogsDir 任务日志目录（logs/<job_id>.log）。
func (s *Store) LogsDir() string { return s.sub("logs") }

// JobsDir 任务档案目录（jobs/<job_id>.json）。
func (s *Store) JobsDir() string { return s.sub("jobs") }

// HistoryDir 应用运行历史目录（history/<app_id>.jsonl）。
func (s *Store) HistoryDir() string { return s.sub("history") }

// StampsDir 定时运行戳目录（stamps/<name>.json）。
func (s *Store) StampsDir() string { return s.sub("stamps") }

// UploadsDir 前端上传落盘目录（uploads/，pcd_export 清单用）。
func (s *Store) UploadsDir() string { return s.sub("uploads") }

// ---------------------------------------------------------------- 日志清理

// iterLogFiles logs/ 与 jobs/ 下的全部文件（任务日志 + 任务档案）。
func (s *Store) iterLogFiles() []string {
	var out []string
	for _, d := range []string{s.LogsDir(), s.JobsDir()} {
		entries, err := os.ReadDir(d)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if !e.IsDir() {
				out = append(out, filepath.Join(d, e.Name()))
			}
		}
	}
	return out
}

// ClearLogs 删除 logs/ 与 jobs/ 下全部文件；运行中任务的 .log 被占用
// （Windows 删不掉）时跳过并计数（Python state.clear_logs 同口径）。
func (s *Store) ClearLogs() map[string]any {
	removed, skipped := 0, 0
	for _, p := range s.iterLogFiles() {
		if err := os.Remove(p); err != nil {
			skipped++
		} else {
			removed++
		}
	}
	return map[string]any{"removed": removed, "skipped": skipped}
}

// PruneOldLogs 按 mtime 删除 logs/、jobs/ 中超过 days 天的文件（启动自洁用）；
// 异常静默跳过，返回删除数。
func (s *Store) PruneOldLogs(days int) int {
	cutoff := time.Now().Add(-time.Duration(days) * 24 * time.Hour)
	removed := 0
	for _, p := range s.iterLogFiles() {
		st, err := os.Stat(p)
		if err != nil {
			continue
		}
		if st.ModTime().Before(cutoff) {
			if os.Remove(p) == nil {
				removed++
			}
		}
	}
	return removed
}

// ---------------------------------------------------------------- JSON 读写

// ReadJSON 读 JSON 文件；失败返回 def。
func ReadJSON(path string, def any) any {
	data, err := os.ReadFile(path)
	if err != nil {
		return def
	}
	var out any
	if json.Unmarshal(data, &out) != nil {
		return def
	}
	return out
}

// ReadJSONMap 读 JSON 对象文件；失败返回 nil。
func ReadJSONMap(path string) map[string]any {
	v, _ := ReadJSON(path, nil).(map[string]any)
	return v
}

// WriteJSON 先写临时文件再替换，避免半截文件（Python state.write_json 同口径）。
func WriteJSON(path string, data any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(data, "", " ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// ---------------------------------------------------------------- 定时运行戳

// mirrorDir 镜像状态目录（环境变量 UVMP_TOOLKIT_MIRROR_STATE）。
// deb 的 systemd timer 以 root 跑 daily-export，其状态在 /root/.local/share 下，
// 桌面用户的 GUI 读不到；安装脚本把桌面用户的状态目录写进服务的该环境变量，
// root 跑完把历史/成功戳镜像过来（Python state._mirror_dir 同口径）。
func (s *Store) mirrorDir() string {
	m := strings.TrimSpace(s.getenv("UVMP_TOOLKIT_MIRROR_STATE"))
	if m == "" {
		return ""
	}
	if err := os.MkdirAll(m, 0o755); err != nil {
		return ""
	}
	return m
}

// mirrorChmod root 镜像写入的文件默认 644，桌面用户后续追加/替换需要写权限。
func mirrorChmod(path string) {
	_ = os.Chmod(path, 0o666)
}

// GetStamp 读取运行戳（dict 或 nil）。约定字段：date=YYYY-MM-DD，time=ISO，其余任意。
func (s *Store) GetStamp(name string) map[string]any {
	return ReadJSONMap(filepath.Join(s.StampsDir(), name+".json"))
}

// SetStamp 写运行戳，并镜像到 UVMP_TOOLKIT_MIRROR_STATE（若配置）。
func (s *Store) SetStamp(name string, data map[string]any) {
	_ = WriteJSON(filepath.Join(s.StampsDir(), name+".json"), data)
	if md := s.mirrorDir(); md != "" {
		p := filepath.Join(md, "stamps", name+".json")
		if WriteJSON(p, data) == nil {
			mirrorChmod(p)
		}
	}
}

// ---------------------------------------------------------------- 应用运行历史（jsonl，每行一条）

// AppendHistory 追加一条运行历史；超过 300 行粗修剪只留最近 200 行；
// 随即镜像到 UVMP_TOOLKIT_MIRROR_STATE（若配置）。
func (s *Store) AppendHistory(appID string, record map[string]any) {
	rec := map[string]any{}
	for k, v := range record {
		rec[k] = v
	}
	if _, ok := rec["time"]; !ok {
		rec["time"] = time.Now().Format("2006-01-02 15:04:05")
	}
	line := marshalLine(rec)
	f := filepath.Join(s.HistoryDir(), appID+".jsonl")
	fp, err := os.OpenFile(f, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err == nil {
		_, _ = fp.WriteString(line + "\n")
		_ = fp.Close()
	}
	// 粗修剪：超过 300 行时只留最近 200 行
	if data, rerr := os.ReadFile(f); rerr == nil {
		lines := splitLines(string(data))
		if len(lines) > 300 {
			_ = os.WriteFile(f, []byte(strings.Join(lines[len(lines)-200:], "\n")+"\n"), 0o644)
		}
	}
	if md := s.mirrorDir(); md != "" {
		mf := filepath.Join(md, "history", appID+".jsonl")
		_ = os.MkdirAll(filepath.Dir(mf), 0o755)
		if fp2, err2 := os.OpenFile(mf, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644); err2 == nil {
			_, _ = fp2.WriteString(line + "\n")
			_ = fp2.Close()
			mirrorChmod(mf)
		}
	}
}

// RecentRuns 最近 n 条运行历史（新→旧）。
func (s *Store) RecentRuns(appID string, n int) []map[string]any {
	f := filepath.Join(s.HistoryDir(), appID+".jsonl")
	data, err := os.ReadFile(f)
	if err != nil {
		return nil
	}
	lines := splitLines(string(data))
	out := []map[string]any{}
	for i := len(lines) - 1; i >= 0 && len(out) < n; i-- {
		var m map[string]any
		if json.Unmarshal([]byte(lines[i]), &m) == nil {
			out = append(out, m)
		}
	}
	return out
}

// LastRun 最近一条运行历史（无则 nil）。
func (s *Store) LastRun(appID string) map[string]any {
	runs := s.RecentRuns(appID, 1)
	if len(runs) == 0 {
		return nil
	}
	return runs[0]
}

// ---------------------------------------------------------------- 运行历史删除

var (
	appIDRe = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)
	jobIDRe = regexp.MustCompile(`^\d{14}-\d{2}$`)
)

// DeleteHistoryRecords 删除应用运行历史记录，连带对应任务日志（logs/）与任务档案（jobs/）。
// jobIDs 为 nil 清空该应用全部记录；否则只删指定 job_id 的记录（非法 job_id 忽略）。
// 运行中任务被占用的文件跳过并计数（Windows 删不掉）。
// 返回 {"records": 删的记录数, "files": 删的文件数, "skipped": 占用跳过数}。
func (s *Store) DeleteHistoryRecords(appID string, jobIDs []string) (map[string]any, error) {
	if !appIDRe.MatchString(appID) {
		return nil, fmt.Errorf("app_id 非法")
	}
	var want map[string]bool
	if jobIDs != nil {
		want = map[string]bool{}
		for _, j := range jobIDs {
			if jobIDRe.MatchString(j) {
				want[j] = true
			}
		}
	}
	records := 0
	doomed := map[string]bool{}
	f := filepath.Join(s.HistoryDir(), appID+".jsonl")
	if data, err := os.ReadFile(f); err == nil {
		kept := []string{}
		for _, line := range splitLines(string(data)) {
			jid := ""
			var m map[string]any
			if json.Unmarshal([]byte(line), &m) == nil {
				if s, ok := m["job_id"].(string); ok {
					jid = s
				}
			}
			if want == nil || want[jid] {
				records++
				if jobIDRe.MatchString(jid) {
					doomed[jid] = true
				}
			} else {
				kept = append(kept, line)
			}
		}
		if len(kept) > 0 {
			_ = os.WriteFile(f, []byte(strings.Join(kept, "\n")+"\n"), 0o644)
		} else {
			_ = os.Remove(f)
		}
	}
	files, skipped := 0, 0
	for jid := range doomed {
		for _, p := range []string{
			filepath.Join(s.LogsDir(), jid+".log"),
			filepath.Join(s.JobsDir(), jid+".json"),
		} {
			err := os.Remove(p)
			switch {
			case err == nil:
				files++
			case os.IsNotExist(err):
			default:
				skipped++
			}
		}
	}
	return map[string]any{"records": records, "files": files, "skipped": skipped}, nil
}

// ---------------------------------------------------------------- 便捷包级函数（默认 Store）

// SetRoot 设定包级默认状态根目录（测试隔离用）。
func SetRoot(dir string) { pkgDefault.SetRoot(dir) }

// StateDir 包级默认状态根目录。
func StateDir() string { return pkgDefault.RootDir() }

// LogsDir 包级默认 logs 目录。
func LogsDir() string { return pkgDefault.LogsDir() }

// JobsDir 包级默认 jobs 目录。
func JobsDir() string { return pkgDefault.JobsDir() }

// ClearLogs 包级默认 ClearLogs。
func ClearLogs() map[string]any { return pkgDefault.ClearLogs() }

// PruneOldLogs 包级默认 PruneOldLogs。
func PruneOldLogs(days int) int { return pkgDefault.PruneOldLogs(days) }

// GetStamp 包级默认 GetStamp。
func GetStamp(name string) map[string]any { return pkgDefault.GetStamp(name) }

// SetStamp 包级默认 SetStamp。
func SetStamp(name string, data map[string]any) { pkgDefault.SetStamp(name, data) }

// AppendHistory 包级默认 AppendHistory。
func AppendHistory(appID string, record map[string]any) { pkgDefault.AppendHistory(appID, record) }

// RecentRuns 包级默认 RecentRuns。
func RecentRuns(appID string, n int) []map[string]any { return pkgDefault.RecentRuns(appID, n) }

// LastRun 包级默认 LastRun。
func LastRun(appID string) map[string]any { return pkgDefault.LastRun(appID) }

// DeleteHistoryRecords 包级默认 DeleteHistoryRecords。
func DeleteHistoryRecords(appID string, jobIDs []string) (map[string]any, error) {
	return pkgDefault.DeleteHistoryRecords(appID, jobIDs)
}

// ---------------------------------------------------------------- 内部工具

// marshalLine JSON 单行（Python json.dumps(ensure_ascii=False) 同口径：非 ASCII 原文）。
func marshalLine(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return "null"
	}
	return string(b)
}

// splitLines 按行拆分并去掉空行。
func splitLines(s string) []string {
	out := []string{}
	for _, l := range strings.Split(s, "\n") {
		l = strings.TrimRight(l, "\r")
		if strings.TrimSpace(l) != "" {
			out = append(out, l)
		}
	}
	return out
}

// stateBase 平台默认状态基目录（Windows %LOCALAPPDATA%，其余 XDG_DATA_HOME）。
func stateBase(getenv func(string) string) string {
	if getenv == nil {
		getenv = os.Getenv
	}
	if runtime.GOOS == "windows" {
		if v := getenv("LOCALAPPDATA"); v != "" {
			return v
		}
		home, _ := os.UserHomeDir()
		return filepath.Join(home, "AppData", "Local")
	}
	if v := getenv("XDG_DATA_HOME"); v != "" {
		return v
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".local", "share")
}
