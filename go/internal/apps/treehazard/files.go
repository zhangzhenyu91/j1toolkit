package treehazard

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// LocalBatchFiles 随批 xlsx 本地落盘（生产默认实现）：写 Base/data/treehazard/inbox/{team_id}/{session}.xlsx，
// 返回相对路径 data/treehazard/inbox/...（与 Node xlsx_path 口径一致，供暂存批次删除时清理）。
type LocalBatchFiles struct {
	// Base server 目录（容器内 /app/server）；空串取当前工作目录。
	Base string
}

func (f LocalBatchFiles) Save(_ context.Context, teamID int64, sessionID string, data []byte) (string, error) {
	if len(data) == 0 {
		return "", nil
	}
	name := safeFileName(sessionID)
	if !strings.HasSuffix(strings.ToLower(name), ".xlsx") {
		name += ".xlsx"
	}
	rel := fmt.Sprintf("data/treehazard/inbox/%d/%s", teamID, name)
	abs := filepath.Join(f.Base, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(abs, data, 0o644); err != nil {
		return "", err
	}
	return rel, nil
}

func (f LocalBatchFiles) Remove(_ context.Context, relPath string) error {
	rel := filepath.FromSlash(strings.TrimPrefix(relPath, "/"))
	abs := filepath.Join(f.Base, rel)
	// 防路径穿越：只允许清理 inbox 目录内文件。
	if !strings.Contains(filepath.ToSlash(rel), "data/treehazard/inbox/") {
		return fmt.Errorf("非法路径: %s", relPath)
	}
	err := os.Remove(abs)
	if err != nil && os.IsNotExist(err) {
		return nil
	}
	return err
}

var _ BatchFileStore = LocalBatchFiles{}
