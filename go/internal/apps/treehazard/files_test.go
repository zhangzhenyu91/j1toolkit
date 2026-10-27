package treehazard

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestLocalBatchFiles 随批 xlsx 落盘：路径口径、幂等清理、路径穿越防护。
func TestLocalBatchFiles(t *testing.T) {
	base := t.TempDir()
	store := LocalBatchFiles{Base: base}
	rel, err := store.Save(context.Background(), 3, "sess/1", []byte("xlsx"))
	if err != nil {
		t.Fatalf("落盘失败: %v", err)
	}
	if rel != "data/treehazard/inbox/3/sess_1.xlsx" {
		t.Fatalf("相对路径不对：%q", rel)
	}
	abs := filepath.Join(base, filepath.FromSlash(rel))
	if _, err := os.Stat(abs); err != nil {
		t.Fatalf("文件应已写入: %v", err)
	}
	if err := store.Remove(context.Background(), rel); err != nil {
		t.Fatalf("清理失败: %v", err)
	}
	if err := store.Remove(context.Background(), rel); err != nil {
		t.Fatalf("重复清理应幂等: %v", err)
	}
	err = store.Remove(context.Background(), "../../etc/passwd")
	if err == nil || !strings.Contains(err.Error(), "非法路径") {
		t.Fatalf("路径穿越应拒绝，实际 %v", err)
	}
}
