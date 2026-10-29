package state

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// timeAddDays 当前时刻偏移天数（PruneOldLogs 测试造老文件用）。
func timeAddDays(days int) time.Time {
	return time.Now().Add(time.Duration(days) * 24 * time.Hour)
}

func newStore(t *testing.T) *Store {
	t.Helper()
	return &Store{Root: t.TempDir()}
}

func TestStampRoundtrip(t *testing.T) {
	s := newStore(t)
	if got := s.GetStamp("daily_export"); got != nil {
		t.Fatalf("空戳应为 nil, got %v", got)
	}
	s.SetStamp("daily_export", map[string]any{"date": "2026-10-28", "time": "2026-10-28 09:15:00", "count": 3})
	got := s.GetStamp("daily_export")
	if got["date"] != "2026-10-28" || got["count"] != float64(3) {
		t.Fatalf("戳内容错误: %v", got)
	}
}

func TestStampMirror(t *testing.T) {
	s := newStore(t)
	mirror := filepath.Join(t.TempDir(), "mirror")
	s.Getenv = func(k string) string {
		if k == "UVMP_TOOLKIT_MIRROR_STATE" {
			return mirror
		}
		return ""
	}
	s.SetStamp("daily_export", map[string]any{"date": "2026-10-28"})
	mf := filepath.Join(mirror, "stamps", "daily_export.json")
	if _, err := os.Stat(mf); err != nil {
		t.Fatalf("镜像戳缺失: %v", err)
	}
	if got := ReadJSONMap(mf); got["date"] != "2026-10-28" {
		t.Fatalf("镜像戳内容错误: %v", got)
	}
}

func TestHistoryAppendRecentTrim(t *testing.T) {
	s := newStore(t)
	s.AppendHistory("daily_sync", map[string]any{"job_id": "20261028091500-01", "status": "success"})
	s.AppendHistory("daily_sync", map[string]any{"job_id": "20261028091600-02", "status": "failed", "time": "2026-10-28 09:16:00"})
	runs := s.RecentRuns("daily_sync", 10)
	if len(runs) != 2 {
		t.Fatalf("历史条数 = %d, want 2", len(runs))
	}
	if runs[0]["job_id"] != "20261028091600-02" {
		t.Fatalf("新记录应在前: %v", runs[0])
	}
	if runs[1]["time"] == "" {
		t.Fatal("缺省 time 应自动补")
	}
	last := s.LastRun("daily_sync")
	if last["job_id"] != "20261028091600-02" {
		t.Fatalf("LastRun = %v", last)
	}
	// 粗修剪：超过 300 行留最近 200
	for i := 0; i < 320; i++ {
		s.AppendHistory("big", map[string]any{"job_id": "20261028091500-01", "i": i})
	}
	data, err := os.ReadFile(filepath.Join(s.HistoryDir(), "big.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	n := len(splitLines(string(data)))
	// 与 Python 同口径：超 300 行才砍到 200，之后逐行回升（320 次追加 → 219 行）
	if n != 219 {
		t.Fatalf("修剪后行数 = %d, want 219（Python 同口径）", n)
	}
}

func TestHistoryMirror(t *testing.T) {
	s := newStore(t)
	mirror := filepath.Join(t.TempDir(), "m")
	s.Getenv = func(k string) string {
		if k == "UVMP_TOOLKIT_MIRROR_STATE" {
			return mirror
		}
		return ""
	}
	s.AppendHistory("daily_sync", map[string]any{"job_id": "20261028091500-01"})
	mf := filepath.Join(mirror, "history", "daily_sync.jsonl")
	if _, err := os.Stat(mf); err != nil {
		t.Fatalf("镜像历史缺失: %v", err)
	}
}

func TestClearLogsAndPrune(t *testing.T) {
	s := newStore(t)
	_ = os.WriteFile(filepath.Join(s.LogsDir(), "a.log"), []byte("x"), 0o644)
	_ = os.WriteFile(filepath.Join(s.JobsDir(), "a.json"), []byte("{}"), 0o644)
	st := s.ClearLogs()
	if st["removed"] != 2 || st["skipped"] != 0 {
		t.Fatalf("ClearLogs = %v", st)
	}
	// prune：老文件删除、新文件保留
	old := filepath.Join(s.LogsDir(), "old.log")
	_ = os.WriteFile(old, []byte("x"), 0o644)
	oldTime := timeAddDays(-40)
	_ = os.Chtimes(old, oldTime, oldTime)
	_ = os.WriteFile(filepath.Join(s.LogsDir(), "new.log"), []byte("x"), 0o644)
	if n := s.PruneOldLogs(30); n != 1 {
		t.Fatalf("PruneOldLogs = %d, want 1", n)
	}
	if _, err := os.Stat(filepath.Join(s.LogsDir(), "new.log")); err != nil {
		t.Fatal("新日志不应被删")
	}
}

func TestDeleteHistoryRecords(t *testing.T) {
	s := newStore(t)
	s.AppendHistory("daily_sync", map[string]any{"job_id": "20261028091500-01"})
	s.AppendHistory("daily_sync", map[string]any{"job_id": "20261028091600-02"})
	_ = os.WriteFile(filepath.Join(s.LogsDir(), "20261028091500-01.log"), []byte("x"), 0o644)
	_ = os.WriteFile(filepath.Join(s.JobsDir(), "20261028091500-01.json"), []byte("{}"), 0o644)

	// 删单条
	got, err := s.DeleteHistoryRecords("daily_sync", []string{"20261028091500-01"})
	if err != nil {
		t.Fatal(err)
	}
	if got["records"] != 1 || got["files"] != 2 {
		t.Fatalf("删除结果 = %v", got)
	}
	if len(s.RecentRuns("daily_sync", 10)) != 1 {
		t.Fatal("应剩 1 条历史")
	}
	// 清空
	got, err = s.DeleteHistoryRecords("daily_sync", nil)
	if err != nil {
		t.Fatal(err)
	}
	if got["records"] != 1 {
		t.Fatalf("清空结果 = %v", got)
	}
	if _, err := os.Stat(filepath.Join(s.HistoryDir(), "daily_sync.jsonl")); !os.IsNotExist(err) {
		t.Fatal("历史文件应已删除")
	}
	// 非法 app_id
	if _, err := s.DeleteHistoryRecords("../evil", nil); err == nil {
		t.Fatal("非法 app_id 应报错")
	}
}

func TestWriteJSONAtomic(t *testing.T) {
	s := newStore(t)
	p := filepath.Join(s.JobsDir(), "x.json")
	if err := WriteJSON(p, map[string]any{"a": 1}); err != nil {
		t.Fatal(err)
	}
	if got := ReadJSONMap(p); got["a"] != float64(1) {
		t.Fatalf("读回 = %v", got)
	}
	if _, err := os.Stat(p + ".tmp"); !os.IsNotExist(err) {
		t.Fatal("临时文件应已替换")
	}
}
