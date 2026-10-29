package course

import (
	"path/filepath"
	"testing"
	"time"
)

func TestCleanCheckpointDefaultsAndCoerce(t *testing.T) {
	cp := CleanCheckpoint(nil)
	if cp.Version != CheckpointVersion || cp.Active || cp.ResumePage != 1 || cp.Processed != 0 {
		t.Fatalf("默认检查点 = %+v", cp)
	}
	if cp.Attempts == nil || cp.LastProgress == nil || cp.SkippedIDs == nil {
		t.Fatal("默认 map/slice 应非 nil")
	}

	raw := map[string]any{
		"version":       float64(1),
		"active":        true,
		"resume_page":   float64(3),
		"processed":     float64(7),
		"attempts":      map[string]any{"v": float64(2)},
		"last_progress": map[string]any{"v": float64(50)},
		"last_errors":   map[string]any{"s": "boom"},
		"skipped_ids":   []any{"s", float64(3), "d"},
		"skip_reasons":  map[string]any{"s": "卡住"},
		"last_result":   map[string]any{"paused": false},
		"updated_at":    "2026-10-28 10:00:00",
	}
	cp = CleanCheckpoint(raw)
	if !cp.Active || cp.ResumePage != 3 || cp.Processed != 7 {
		t.Fatalf("清洗后 = %+v", cp)
	}
	if cp.Attempts["v"] != 2 || cp.LastProgress["v"] != 50 || cp.LastErrors["s"] != "boom" {
		t.Fatalf("映射清洗错误: %+v", cp)
	}
	if len(cp.SkippedIDs) != 2 { // 非字符串被丢弃
		t.Fatalf("skipped_ids = %v", cp.SkippedIDs)
	}
	if cp.SkipReasons["s"] != "卡住" || cp.LastResult == nil {
		t.Fatalf("skip/last_result 错误: %+v", cp)
	}

	// 非法数值回默认
	cp = CleanCheckpoint(map[string]any{"resume_page": "abc", "processed": float64(-5)})
	if cp.ResumePage != 1 || cp.Processed != 0 {
		t.Fatalf("非法数值清洗 = %+v", cp)
	}
}

func TestCheckpointsStorePersistLoad(t *testing.T) {
	path := filepath.Join(t.TempDir(), "course-autoplay", "checkpoints.json")
	fixed := time.Date(2026, 10, 28, 12, 0, 0, 0, time.Local)
	store := &CheckpointsStore{Path: path, Now: func() time.Time { return fixed }}

	all := store.Load()
	if len(all) != 0 {
		t.Fatalf("初始应为空: %v", all)
	}
	cp := NewCheckpoint()
	cp.Active = true
	cp.ResumePage = 2
	cp.Processed = 5
	cp.Attempts["v"] = 1
	cp.SkippedIDs = []string{"d", "s"}
	store.Persist(all, "train:train1", cp)

	reloaded := store.Load()
	got := reloaded["train:train1"]
	if got == nil || !got.Active || got.ResumePage != 2 || got.Processed != 5 {
		t.Fatalf("重载 = %+v", got)
	}
	if got.Attempts["v"] != 1 || len(got.SkippedIDs) != 2 {
		t.Fatalf("重载明细 = %+v", got)
	}
	if got.UpdatedAt != "2026-10-28 12:00:00" {
		t.Fatalf("updated_at = %q", got.UpdatedAt)
	}
	if store.ActiveCount() != 1 {
		t.Fatalf("active_lists = %d", store.ActiveCount())
	}

	// ToMap/Clean 往返
	round := CleanCheckpoint(got.ToMap())
	if round.ResumePage != 2 || round.Attempts["v"] != 1 {
		t.Fatalf("往返 = %+v", round)
	}
}
