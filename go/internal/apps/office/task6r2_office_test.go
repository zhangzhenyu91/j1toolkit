package office

// Task 6 fix round 2：Office staging 默认指向持久数据目录，启动/周期清理崩溃残留。

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// task6Config 最小配置桩。
type task6Config map[string]string

func (c task6Config) Get(_ context.Context, key string) (string, error) { return c[key], nil }

// TestSaveTempDirDefaultPersistent 未配置时默认使用持久数据目录，而非系统临时目录。
func TestSaveTempDirDefaultPersistent(t *testing.T) {
	m := New(nil)
	if got := m.saveTempDir(context.Background()); got != DefaultSaveTempDir {
		t.Fatalf("默认 staging = %q，期望 %q", got, DefaultSaveTempDir)
	}
	m.SaveTempDir = t.TempDir()
	if got := m.saveTempDir(context.Background()); got != m.SaveTempDir {
		t.Fatalf("显式 SaveTempDir 应优先：%q", got)
	}
	m.SaveTempDir = ""
	m.Cfg = task6Config{"OO_SAVE_TEMP_DIR": "/data/oo-staging"}
	if got := m.saveTempDir(context.Background()); got != "/data/oo-staging" {
		t.Fatalf("运行时配置应生效：%q", got)
	}
}

// TestCleanupSaveTempStale 只删超期 oo-save-* 文件，保留新文件与无关文件。
func TestCleanupSaveTempStale(t *testing.T) {
	dir := t.TempDir()
	old := filepath.Join(dir, "oo-save-old.bin")
	fresh := filepath.Join(dir, "oo-save-fresh.bin")
	other := filepath.Join(dir, "keep.txt")
	for _, p := range []string{old, fresh, other} {
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
	past := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(old, past, past); err != nil {
		t.Fatalf("chtimes: %v", err)
	}
	if n := cleanupSaveTemp(dir, time.Hour); n != 1 {
		t.Fatalf("删除数量 = %d，期望 1", n)
	}
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Fatal("超期文件应删除")
	}
	for _, p := range []string{fresh, other} {
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("不应删除 %s: %v", p, err)
		}
	}
}

// TestStartSaveTempJanitorStartup 启动即清理超期残留，ctx 取消后停止。
func TestStartSaveTempJanitorStartup(t *testing.T) {
	dir := t.TempDir()
	stale := filepath.Join(dir, "oo-save-crash.bin")
	if err := os.WriteFile(stale, []byte("x"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	past := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(stale, past, past); err != nil {
		t.Fatalf("chtimes: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	m := New(nil)
	m.SaveTempDir = dir
	m.StartSaveTempJanitor(ctx)
	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, err := os.Stat(stale); os.IsNotExist(err) {
			break
		}
		if time.Now().After(deadline) {
			cancel()
			t.Fatal("启动清理未删除残留")
		}
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
}
