package course

import (
	"path/filepath"
	"sort"
	"time"

	"github.com/zhangzhenyu91/j1toolkit/uvmp-toolkit/core-go/internal/state"
)

// CheckpointVersion 检查点 schema 版本（backend.py _clean_checkpoint 的 version=1）。
const CheckpointVersion = 1

// Checkpoint 单个培训班列表的续刷检查点（schema v1）：
// 续刷页、真实失败次数、已处理数、跳过项及原因，随进度实时持久化。
type Checkpoint struct {
	Version      int                `json:"version"`
	Active       bool               `json:"active"`
	ResumePage   int                `json:"resume_page"`
	Processed    int                `json:"processed"`
	Attempts     map[string]int     `json:"attempts"`
	LastProgress map[string]float64 `json:"last_progress"`
	LastErrors   map[string]string  `json:"last_errors"`
	SkippedIDs   []string           `json:"skipped_ids"`
	SkipReasons  map[string]string  `json:"skip_reasons"`
	// LastResult 上一轮结果摘要（backend.py 只在 dict 时保留）。
	LastResult map[string]any `json:"last_result,omitempty"`
	UpdatedAt  string         `json:"updated_at,omitempty"`
}

// NewCheckpoint 返回 schema v1 空检查点（非 nil map/slice，可直接改）。
func NewCheckpoint() *Checkpoint {
	return &Checkpoint{
		Version:      CheckpointVersion,
		ResumePage:   1,
		Attempts:     map[string]int{},
		LastProgress: map[string]float64{},
		LastErrors:   map[string]string{},
		SkippedIDs:   []string{},
		SkipReasons:  map[string]string{},
	}
}

// CleanCheckpoint 清洗持久化载荷为合法检查点（backend.py _clean_checkpoint 同口径）：
// 键类型非法则回默认；resume_page≥1、processed≥0；active/last_result 宽松保留。
func CleanCheckpoint(raw map[string]any) *Checkpoint {
	c := NewCheckpoint()
	if raw == nil {
		return c
	}
	if m, ok := raw["attempts"].(map[string]any); ok {
		for k, v := range m {
			if n, ok := toFloat(v); ok {
				c.Attempts[k] = int(n)
			}
		}
	}
	if m, ok := raw["last_progress"].(map[string]any); ok {
		for k, v := range m {
			if n, ok := toFloat(v); ok {
				c.LastProgress[k] = n
			}
		}
	}
	if m, ok := raw["last_errors"].(map[string]any); ok {
		for k, v := range m {
			if s, ok := v.(string); ok {
				c.LastErrors[k] = s
			}
		}
	}
	if m, ok := raw["skip_reasons"].(map[string]any); ok {
		for k, v := range m {
			if s, ok := v.(string); ok {
				c.SkipReasons[k] = s
			}
		}
	}
	if list, ok := raw["skipped_ids"].([]any); ok {
		for _, v := range list {
			if s, ok := v.(string); ok {
				c.SkippedIDs = append(c.SkippedIDs, s)
			}
		}
	}
	if n, ok := toFloat(raw["resume_page"]); ok && int(n) >= 1 {
		c.ResumePage = int(n)
	}
	if n, ok := toFloat(raw["processed"]); ok && int(n) >= 0 {
		c.Processed = int(n)
	}
	c.Active = toBool(raw["active"])
	if m, ok := raw["last_result"].(map[string]any); ok {
		c.LastResult = m
	}
	if s, ok := raw["updated_at"].(string); ok {
		c.UpdatedAt = s
	}
	return c
}

// ToMap 持久化形状（键名与 backend.py checkpoints.json 一致，供兼容旧数据与 GUI 展示）。
func (c *Checkpoint) ToMap() map[string]any {
	attempts := map[string]any{}
	for k, v := range c.Attempts {
		attempts[k] = v
	}
	lastProgress := map[string]any{}
	for k, v := range c.LastProgress {
		lastProgress[k] = v
	}
	lastErrors := map[string]any{}
	for k, v := range c.LastErrors {
		lastErrors[k] = v
	}
	skipReasons := map[string]any{}
	for k, v := range c.SkipReasons {
		skipReasons[k] = v
	}
	skipped := append([]string(nil), c.SkippedIDs...)
	sort.Strings(skipped)
	out := map[string]any{
		"version":       CheckpointVersion,
		"active":        c.Active,
		"resume_page":   c.ResumePage,
		"processed":     c.Processed,
		"attempts":      attempts,
		"last_progress": lastProgress,
		"last_errors":   lastErrors,
		"skipped_ids":   skipped,
		"skip_reasons":  skipReasons,
	}
	if c.LastResult != nil {
		out["last_result"] = c.LastResult
	}
	if c.UpdatedAt != "" {
		out["updated_at"] = c.UpdatedAt
	}
	return out
}

// CheckpointsStore 按 list_key 持久化的检查点集合（state/course-autoplay/checkpoints.json）。
type CheckpointsStore struct {
	Path string
	// Now 时间源（测试注入）；nil 取 time.Now。
	Now func() time.Time
}

// NewCheckpointsStore 缺省路径 state-dir/course-autoplay/checkpoints.json（backend.py _checkpoint_path）。
func NewCheckpointsStore() *CheckpointsStore {
	return &CheckpointsStore{Path: filepath.Join(state.StateDir(), "course-autoplay", "checkpoints.json")}
}

// Load 读全部检查点（缺失/损坏返回空集合）。
func (s *CheckpointsStore) Load() map[string]*Checkpoint {
	raw := state.ReadJSONMap(s.Path)
	out := map[string]*Checkpoint{}
	for k, v := range raw {
		if m, ok := v.(map[string]any); ok {
			out[k] = CleanCheckpoint(m)
		}
	}
	return out
}

// Persist 落盘单个检查点（backend.py _persist：盖 updated_at 后整文件写回）。
func (s *CheckpointsStore) Persist(all map[string]*Checkpoint, listKey string, cp *Checkpoint) {
	now := time.Now
	if s.Now != nil {
		now = s.Now
	}
	cp.Version = CheckpointVersion
	cp.UpdatedAt = now().Format("2006-01-02 15:04:05")
	all[listKey] = cp
	payload := map[string]any{}
	for k, v := range all {
		payload[k] = v.ToMap()
	}
	_ = state.WriteJSON(s.Path, payload)
}

// ActiveCount active=true 的培训班数（backend.py status 的 active_lists）。
func (s *CheckpointsStore) ActiveCount() int {
	n := 0
	for _, cp := range s.Load() {
		if cp.Active {
			n++
		}
	}
	return n
}

// toBool 宽松布尔（bool/数值/字符串）。
func toBool(v any) bool {
	switch t := v.(type) {
	case bool:
		return t
	case float64:
		return t != 0
	case int:
		return t != 0
	case string:
		return t == "true" || t == "1" || t == "True"
	default:
		return false
	}
}
