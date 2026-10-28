package exporter

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
)

// Manifest 批次断点续传记录（manifest.json，vehicle_export.Manifest 同形状：
// {"pcd":{单据id:文件名},"track":{单据id:文件名}}）。并发互斥 + 原子写。
type Manifest struct {
	mu   sync.Mutex
	path string
	data map[string]map[string]string
}

// LoadManifest 读入既有 manifest（缺失/损坏按空处理，不报错）。
func LoadManifest(path string) *Manifest {
	m := &Manifest{
		path: path,
		data: map[string]map[string]string{"pcd": {}, "track": {}},
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return m
	}
	var loaded map[string]map[string]string
	if json.Unmarshal(raw, &loaded) == nil {
		for kind, kv := range loaded {
			if kv == nil {
				continue
			}
			if m.data[kind] == nil {
				m.data[kind] = map[string]string{}
			}
			for k, v := range kv {
				m.data[kind][k] = v
			}
		}
	}
	return m
}

// Has 该单据是否已导出过（断点续传跳过依据）。
func (m *Manifest) Has(kind, key string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.data[kind][key]
	return ok
}

// Get 取已导出的文件名。
func (m *Manifest) Get(kind, key string) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.data[kind][key]
}

// Put 记录一次成功导出并立即落盘（互斥 + 临时文件原子替换）。
func (m *Manifest) Put(kind, key, fname string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.data[kind] == nil {
		m.data[kind] = map[string]string{}
	}
	m.data[kind][key] = fname
	m.saveLocked()
}

func (m *Manifest) saveLocked() {
	raw, err := json.MarshalIndent(m.data, "", " ")
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(m.path), 0o755); err != nil {
		return
	}
	tmp := m.path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o644); err != nil {
		return
	}
	_ = os.Rename(tmp, m.path) // Windows 上 os.Rename 覆盖既有文件
}
