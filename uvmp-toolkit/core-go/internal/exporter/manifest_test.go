package exporter

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// TestManifestResumeAndAtomic manifest：has/get/put 语义与并发落盘（文件始终合法 JSON）。
func TestManifestResumeAndAtomic(t *testing.T) {
	path := filepath.Join(t.TempDir(), "manifest.json")
	m := LoadManifest(path)
	if m.Has("pcd", "x") {
		t.Fatal("空 manifest 不应命中")
	}
	m.Put("pcd", "x", "01_派车单.pdf")

	m2 := LoadManifest(path) // 重跑读回
	if !m2.Has("pcd", "x") || m2.Get("pcd", "x") != "01_派车单.pdf" {
		t.Fatalf("续传读回不符: %+v", m2.data)
	}

	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			m2.Put("track", string(rune('a'+i)), "f.pdf")
		}(i)
	}
	wg.Wait()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var kv map[string]map[string]string
	if err := json.Unmarshal(raw, &kv); err != nil {
		t.Fatalf("并发写后文件损坏: %v", err)
	}
	if len(kv["track"]) != 20 {
		t.Fatalf("并发条目缺失: %d", len(kv["track"]))
	}
}

// TestManifestToleratesCorrupt 损坏的 manifest 按空处理不报错。
func TestManifestToleratesCorrupt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "manifest.json")
	_ = os.WriteFile(path, []byte("{not json"), 0o644)
	m := LoadManifest(path)
	if m.Has("pcd", "x") {
		t.Fatal("损坏文件应按空处理")
	}
	m.Put("track", "y", "f.pdf")
	if !m.Has("track", "y") {
		t.Fatal("损坏后仍可写")
	}
}
