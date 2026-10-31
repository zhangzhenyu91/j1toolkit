package safeday

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// TestRecordStoreCopies 缓存副本语义：List/Get/Create/Remove 返回或接收的切片
// 与存储内部数据隔离，外部改写不得影响后续读取。
func TestRecordStoreCopies(t *testing.T) {
	store, err := NewRecordStore(t.TempDir())
	if err != nil {
		t.Fatalf("建存储失败: %v", err)
	}
	input := Record{Name: "A", Date: "2026.10.01", FileName: "a.docx", Team: "T", Sources: []string{"one"}}
	rec, _, err := store.Create(input)
	if err != nil {
		t.Fatalf("建记录失败: %v", err)
	}
	if len(rec.Sources) != 1 || rec.Sources[0] != "one" {
		t.Fatalf("返回值副本不符: %+v", rec)
	}
	// 修改 Create 入参的 Sources 不应影响已存记录（深拷贝）
	input.Sources[0] = "mutated-input"
	if got, _ := store.Get(rec.ID); got.Sources[0] != "one" {
		t.Fatalf("Create 应深拷贝 Sources: %+v", got)
	}
	// 修改 List 返回值不应影响存储
	list := store.List("")
	if len(list) != 1 {
		t.Fatalf("列表条数不符: %d", len(list))
	}
	list[0].Name = "mutated"
	list[0].Sources[0] = "mutated-list"
	if got, _ := store.Get(rec.ID); got.Name != "A" || got.Sources[0] != "one" {
		t.Fatalf("List 应返回副本: %+v", got)
	}
	// 修改 Get 返回值不应影响存储
	got, _ := store.Get(rec.ID)
	got.Sources[0] = "mutated-get"
	if again, _ := store.Get(rec.ID); again.Sources[0] != "one" {
		t.Fatalf("Get 应返回副本: %+v", again)
	}
	// Remove 返回的被删记录同样为副本
	removed, ok := store.Remove(rec.ID)
	if !ok {
		t.Fatalf("删除应成功")
	}
	removed.Sources[0] = "mutated-removed"
	if list := store.List(""); len(list) != 0 {
		t.Fatalf("删除后列表应为空: %+v", list)
	}
}

// TestRecordSourcesAlwaysArray 空 Sources 序列化为 [] 而非 null（新建与旧数据重载同口径）。
func TestRecordSourcesAlwaysArray(t *testing.T) {
	dir := t.TempDir()
	store, err := NewRecordStore(dir)
	if err != nil {
		t.Fatalf("建存储失败: %v", err)
	}
	rec, _, err := store.Create(Record{Name: "A", Date: "2026.10.03", FileName: "a.docx", Team: "T"})
	if err != nil {
		t.Fatalf("建记录失败: %v", err)
	}
	if rec.Sources == nil {
		t.Fatalf("新建记录 Sources 应为非 nil 空切片")
	}
	raw, _ := json.Marshal(rec)
	if !strings.Contains(string(raw), `"sources":[]`) {
		t.Fatalf("新建记录应序列化 sources:[]，got %s", raw)
	}
	if list := store.List(""); len(list) != 1 || list[0].Sources == nil {
		t.Fatalf("List 空 Sources 应为非 nil: %+v", list)
	}

	// 旧 records.json 中的 null 在重载后归一为 []
	legacy := []Record{{ID: "legacy-1", Name: "旧", Date: "2026.10.04", FileName: "b.docx", Team: "T", Status: "done"}}
	legacyRaw, _ := json.Marshal(legacy)
	if !strings.Contains(string(legacyRaw), `"sources":null`) {
		t.Fatalf("夹具应包含 null Sources: %s", legacyRaw)
	}
	if err := os.WriteFile(filepath.Join(dir, "records.json"), legacyRaw, 0o644); err != nil {
		t.Fatalf("写旧记录失败: %v", err)
	}
	reloaded, err := NewRecordStore(dir)
	if err != nil {
		t.Fatalf("重载失败: %v", err)
	}
	got, ok := reloaded.Get("legacy-1")
	if !ok || got.Sources == nil {
		t.Fatalf("旧记录 null Sources 应归一为 []: %+v ok=%v", got, ok)
	}
	out, _ := json.Marshal(got)
	if !strings.Contains(string(out), `"sources":[]`) {
		t.Fatalf("旧记录应序列化 sources:[]，got %s", out)
	}
}

// TestRecordStoreReload 落盘重载：新实例从同一目录读回记录与表单默认值。
func TestRecordStoreReload(t *testing.T) {
	dir := t.TempDir()
	store, err := NewRecordStore(dir)
	if err != nil {
		t.Fatalf("建存储失败: %v", err)
	}
	rec, _, err := store.Create(Record{
		Name: "回载", Date: "2026.10.02", FileName: "b.docx", Team: "T",
		Sources: []string{"s1", "s2"},
		Form:    RecordForm{Host: "主持人"},
	})
	if err != nil {
		t.Fatalf("建记录失败: %v", err)
	}
	store.SaveFormDefaults("T", FormDefaults{Superior: "甲", Recorder: "乙", TemplateID: "tpl-x"})

	reloaded, err := NewRecordStore(dir)
	if err != nil {
		t.Fatalf("重载存储失败: %v", err)
	}
	got, ok := reloaded.Get(rec.ID)
	if !ok || got.Name != "回载" || got.Form.Host != "主持人" || len(got.Sources) != 2 {
		t.Fatalf("落盘重载不符: %+v", got)
	}
	defs := reloaded.GetFormDefaults("T")
	if defs.Superior != "甲" || defs.Recorder != "乙" || defs.TemplateID != "tpl-x" {
		t.Fatalf("表单默认值重载不符: %+v", defs)
	}
}

// TestRecordStoreConcurrent 并发读写：缓存 + RWMutex 下无数据竞争/丢失更新。
func TestRecordStoreConcurrent(t *testing.T) {
	store, err := NewRecordStore(t.TempDir())
	if err != nil {
		t.Fatalf("建存储失败: %v", err)
	}
	const workers = 12
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 20; j++ {
				rec, _, err := store.Create(Record{
					Name: fmt.Sprintf("w%d-%d", i, j), Date: fmt.Sprintf("2026.11.%02d", i),
					FileName: "x.docx", Team: "T", Status: "processing",
				})
				if err != nil {
					t.Errorf("并发建记录失败: %v", err)
					return
				}
				done := "done"
				store.Update(rec.ID, RecordPatch{Status: &done})
				_ = store.List("T")
				_ = store.List("")
				store.Get(rec.ID)
			}
		}()
	}
	wg.Wait()
	if n := len(store.List("")); n != workers {
		t.Fatalf("同班组同日期应各保留 1 条，got %d", n)
	}
	// 并发改名/回填不丢记录
	var wg2 sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg2.Add(1)
		go func() { defer wg2.Done(); store.RenameTeam("T", "T2") }()
	}
	wg2.Wait()
	if n := len(store.List("T2")); n != workers {
		t.Fatalf("并发改名后条数不符: %d", n)
	}
}
