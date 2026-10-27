package worklog

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"shade/internal/assetpath"
)

// SeedDefaultTowers 空表时把随仓分发的杆塔坐标迁入默认班组；已有数据不覆盖。
func (m *Module) SeedDefaultTowers(ctx context.Context) (int, error) {
	var count, teamID int64
	if err := m.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM worklog_tower`).Scan(&count); err != nil {
		return 0, err
	}
	if count != 0 {
		return 0, nil
	}
	if err := m.DB.QueryRowContext(ctx, `SELECT id FROM sys_team WHERE status = 1 ORDER BY sort, id LIMIT 1`).Scan(&teamID); err != nil {
		return 0, err
	}
	path := assetpath.ResolveFile("worklog/tower-coords.json")
	if path == "" {
		return 0, nil
	}
	raw, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return 0, err
	}
	var payload struct {
		Rows [][]any `json:"rows"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return 0, err
	}
	for start := 0; start < len(payload.Rows); start += 500 {
		end := min(start+500, len(payload.Rows))
		args := make([]any, 0, (end-start)*6)
		values := make([]string, 0, end-start)
		for i, row := range payload.Rows[start:end] {
			if len(row) < 5 {
				return 0, fmt.Errorf("杆塔坐标第 %d 行少于 5 列", start+i+1)
			}
			values = append(values, "(?, ?, ?, ?, ?, ?)")
			args = append(args, teamID, towerSeedText(row[0]), towerSeedText(row[1]), towerSeedText(row[2]), towerSeedText(row[3]), towerSeedText(row[4]))
			// sort 由独立参数追加，避免修改原始素材。
			args = append(args, start+i+1)
		}
		if _, err := m.DB.ExecContext(ctx, `INSERT INTO worklog_tower (team_id, voltage_level, line_name, tower_no, lng, lat, sort) VALUES `+strings.Join(values, ","), args...); err != nil {
			return 0, err
		}
	}
	return len(payload.Rows), nil
}

func towerSeedText(v any) string {
	if v == nil {
		return ""
	}
	return fmt.Sprint(v)
}
