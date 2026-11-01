package safeday

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"shade/internal/config"
)

func testCipher(t *testing.T) *config.ConfigCipher {
	t.Helper()
	c, err := config.NewConfigCipher("", "unit-test-jwt-secret")
	if err != nil {
		t.Fatalf("构造加密器失败: %v", err)
	}
	return c
}

func newTestTemplateStore(t *testing.T) (*TemplateStore, string) {
	t.Helper()
	dir := t.TempDir()
	ts, err := NewTemplateStore(dir, testCipher(t))
	if err != nil {
		t.Fatalf("构造模板库失败: %v", err)
	}
	return ts, dir
}

func testSections() []TemplateSection {
	return []TemplateSection{{Key: "part_a", Title: "章节甲"}, {Key: "part_b", Title: "章节乙"}}
}

func TestTemplateStoreCRUDWithoutBuiltinOrDocx(t *testing.T) {
	ts, dir := newTestTemplateStore(t)
	if list := ts.List(); len(list) != 0 {
		t.Fatalf("初始模板列表应为空: %+v", list)
	}
	if options := ts.Options(); len(options) != 0 {
		t.Fatalf("初始模板选项应为空: %+v", options)
	}
	if got := ts.DefaultID(); got != "" {
		t.Fatalf("零模板时不应有默认 ID: %q", got)
	}

	tpl, err := ts.Create("自定义模板", testSections(), "app-secret-key-1")
	if err != nil {
		t.Fatalf("新建模板失败: %v", err)
	}
	if tpl.ID == "" || tpl.Mode != TemplateModeDynamic || !tpl.HasAPIKey || !tpl.IsDefault {
		t.Fatalf("新建模板字段不符: %+v", tpl)
	}
	if tpl.SHA256 != "" || tpl.Size != 0 {
		t.Fatalf("新模板不应包含 docx 元数据: %+v", tpl)
	}
	if got := ts.DefaultID(); got != tpl.ID {
		t.Fatalf("首套模板应成为默认: %q", got)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "templates", "index.json"))
	if err != nil {
		t.Fatalf("读索引失败: %v", err)
	}
	if !bytes.Contains(raw, []byte(config.EncPrefix)) || bytes.Contains(raw, []byte("app-secret-key-1")) {
		t.Fatalf("索引应只保存 API Key 密文: %s", raw)
	}
	if _, err := os.Stat(filepath.Join(dir, "templates", "files")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("新模板库不应创建 docx 文件目录: %v", err)
	}
	snap, err := ts.Snapshot(tpl.ID)
	if err != nil {
		t.Fatalf("模板快照不应依赖 docx: %v", err)
	}
	if snap.ID != tpl.ID || snap.SHA256 != "" || len(snap.Sections) != 2 {
		t.Fatalf("模板快照不符: %+v", snap)
	}
	if key, err := ts.WorkflowAPIKey(tpl.ID); err != nil || key != "app-secret-key-1" {
		t.Fatalf("模板 key 解密不符: key=%q err=%v", key, err)
	}

	updated, err := ts.Update(tpl.ID, TemplateUpdate{
		Name: "改名模板", Sections: []TemplateSection{{Key: "part_c", Title: "章节丙"}}, APIKey: "app-secret-key-2",
	})
	if err != nil {
		t.Fatalf("编辑模板失败: %v", err)
	}
	if updated.Name != "改名模板" || len(updated.Sections) != 1 || updated.Sections[0].Key != "part_c" {
		t.Fatalf("编辑结果不符: %+v", updated)
	}
	if key, _ := ts.WorkflowAPIKey(tpl.ID); key != "app-secret-key-2" {
		t.Fatalf("新 key 未生效: %q", key)
	}

	second, err := ts.Create("第二套", testSections(), "app-secret-key-3")
	if err != nil {
		t.Fatalf("新建第二套模板失败: %v", err)
	}
	if got := ts.DefaultID(); got != tpl.ID {
		t.Fatalf("新增模板不应改变既有默认: %q", got)
	}
	if err := ts.SetDefault(second.ID); err != nil {
		t.Fatalf("设默认失败: %v", err)
	}
	if err := ts.Delete(second.ID); err != nil {
		t.Fatalf("删除默认模板失败: %v", err)
	}
	if got := ts.DefaultID(); got != tpl.ID {
		t.Fatalf("删除默认后应回落其他用户模板: %q", got)
	}
	if err := ts.Delete(tpl.ID); err != nil {
		t.Fatalf("删除模板失败: %v", err)
	}
	if got := ts.DefaultID(); got != "" {
		t.Fatalf("删除全部模板后默认应为空: %q", got)
	}
}

func TestTemplateStoreLegacyIndexIgnoresBuiltinDefaultAndDocx(t *testing.T) {
	ts, dir := newTestTemplateStore(t)
	tpl, err := ts.Create("旧自定义模板", testSections(), "legacy-key")
	if err != nil {
		t.Fatalf("建模板失败: %v", err)
	}
	idxPath := filepath.Join(dir, "templates", "index.json")
	var idx templateIndex
	if err := json.Unmarshal(mustReadFile(t, idxPath), &idx); err != nil {
		t.Fatalf("解析索引失败: %v", err)
	}
	idx.DefaultID = "builtin"
	idx.Templates[0].SHA256 = strings.Repeat("a", 64)
	idx.Templates[0].Size = 12345
	raw, _ := json.Marshal(idx)
	if err := os.WriteFile(idxPath, raw, 0o644); err != nil {
		t.Fatalf("写旧索引失败: %v", err)
	}
	reloaded, err := NewTemplateStore(dir, testCipher(t))
	if err != nil {
		t.Fatalf("重载旧索引失败: %v", err)
	}
	if got := reloaded.DefaultID(); got != tpl.ID {
		t.Fatalf("旧 builtin 默认应回落用户模板: %q", got)
	}
	if _, err := reloaded.Snapshot(tpl.ID); err != nil {
		t.Fatalf("旧上传文件缺失不得影响快照: %v", err)
	}
	if _, ok := reloaded.Get("builtin"); ok {
		t.Fatalf("builtin 不应再作为模板条目")
	}
}

func TestTemplateStorePersistence(t *testing.T) {
	ts, dir := newTestTemplateStore(t)
	tpl, err := ts.Create("持久化", testSections(), "persist-key")
	if err != nil {
		t.Fatalf("新建失败: %v", err)
	}
	reloaded, err := NewTemplateStore(dir, testCipher(t))
	if err != nil {
		t.Fatalf("重载失败: %v", err)
	}
	if got, ok := reloaded.Get(tpl.ID); !ok || got.Name != "持久化" || !got.IsDefault {
		t.Fatalf("重载模板不符: %+v ok=%v", got, ok)
	}
	if key, _ := reloaded.WorkflowAPIKey(tpl.ID); key != "persist-key" {
		t.Fatalf("重载后 key 不符: %q", key)
	}
}

func TestTemplateStoreValidation(t *testing.T) {
	ts, _ := newTestTemplateStore(t)
	key := "app-validation-secret"
	cases := []struct {
		name     string
		tplName  string
		sections []TemplateSection
		apiKey   string
		want     string
	}{
		{"名称空", "  ", testSections(), key, "模板名称不能为空"},
		{"章节空", "X", nil, key, "模板章节不能为空"},
		{"章节 key 非法", "X", []TemplateSection{{Key: "Bad-Key", Title: "甲"}}, key, "章节标识不合法"},
		{"章节 key 重复", "X", []TemplateSection{{Key: "a", Title: "甲"}, {Key: "a", Title: "乙"}}, key, "章节标识重复"},
		{"章节标题空", "X", []TemplateSection{{Key: "a", Title: " "}}, key, "章节标题不能为空"},
		{"保留 key", "X", []TemplateSection{{Key: "date", Title: "甲"}}, key, "章节标识不合法"},
		{"记录 ID 保留 key", "X", []TemplateSection{{Key: "record_id", Title: "甲"}}, key, "章节标识不合法"},
		{"批次 ID 保留 key", "X", []TemplateSection{{Key: "batch_id", Title: "甲"}}, key, "章节标识不合法"},
		{"API Key 空", "X", testSections(), "", "请填写 Dify API Key"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ts.Create(tc.tplName, tc.sections, tc.apiKey)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("错误文案不符: %v", err)
			}
			if tc.apiKey != "" && strings.Contains(err.Error(), tc.apiKey) {
				t.Fatalf("错误文案不得包含 API Key: %v", err)
			}
		})
	}
	bare, err := NewTemplateStore(t.TempDir(), nil)
	if err != nil {
		t.Fatalf("构造无加密器模板库失败: %v", err)
	}
	if _, err := bare.Create("X", testSections(), key); !errors.Is(err, ErrTemplateCipherMissing) {
		t.Fatalf("无加密器应拒绝新建: %v", err)
	}
	if _, err := ts.Update("missing", TemplateUpdate{Name: "改"}); !errors.Is(err, ErrTemplateNotFound) {
		t.Fatalf("未知模板编辑应返回不存在: %v", err)
	}
	if err := ts.SetDefault("missing"); !errors.Is(err, ErrTemplateNotFound) {
		t.Fatalf("未知模板设默认应返回不存在: %v", err)
	}
	if err := ts.Delete("missing"); !errors.Is(err, ErrTemplateNotFound) {
		t.Fatalf("未知模板删除应返回不存在: %v", err)
	}
}

func TestTemplateStoreMissingKey(t *testing.T) {
	ts, dir := newTestTemplateStore(t)
	tpl, err := ts.Create("X", testSections(), "app-key-missing")
	if err != nil {
		t.Fatalf("新建模板失败: %v", err)
	}
	idxPath := filepath.Join(dir, "templates", "index.json")
	var idx templateIndex
	if err := json.Unmarshal(mustReadFile(t, idxPath), &idx); err != nil {
		t.Fatalf("解析索引失败: %v", err)
	}
	idx.Templates[0].APIKeyEnc = ""
	raw, _ := json.Marshal(idx)
	if err := os.WriteFile(idxPath, raw, 0o644); err != nil {
		t.Fatalf("写索引失败: %v", err)
	}
	reloaded, err := NewTemplateStore(dir, testCipher(t))
	if err != nil {
		t.Fatalf("重载失败: %v", err)
	}
	if _, err := reloaded.WorkflowAPIKey(tpl.ID); !errors.Is(err, ErrTemplateKeyMissing) {
		t.Fatalf("缺 key 应返回明确错误: %v", err)
	}
}

func TestTemplateStoreConcurrentReads(t *testing.T) {
	ts, _ := newTestTemplateStore(t)
	tpl, err := ts.Create("并发", testSections(), "app-key-concurrent")
	if err != nil {
		t.Fatalf("新建模板失败: %v", err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 20; j++ {
				if _, ok := ts.Get(tpl.ID); !ok {
					t.Errorf("并发读取模板失败")
					return
				}
				if key, err := ts.WorkflowAPIKey(tpl.ID); err != nil || key != "app-key-concurrent" {
					t.Errorf("并发解密失败: %q %v", key, err)
					return
				}
				if _, err := ts.Snapshot(tpl.ID); err != nil {
					t.Errorf("并发取快照失败: %v", err)
					return
				}
				_ = ts.List()
			}
		}()
	}
	wg.Wait()
}

func TestTemplateStoreSectionQuota(t *testing.T) {
	ts, _ := newTestTemplateStore(t)
	makeSections := func(n int) []TemplateSection {
		out := make([]TemplateSection, n)
		for i := range out {
			out[i] = TemplateSection{Key: fmt.Sprintf("part_%d", i), Title: fmt.Sprintf("章节%d", i+1)}
		}
		return out
	}
	if _, err := ts.Create("十二段", makeSections(12), "key-12"); err != nil {
		t.Fatalf("12 段应允许: %v", err)
	}
	if _, err := ts.Create("十三段", makeSections(13), "key-13"); err == nil || !strings.Contains(err.Error(), "最多 12 段") {
		t.Fatalf("13 段应拒绝: %v", err)
	}
}

func TestTemplateStoreCountQuota(t *testing.T) {
	ts, _ := newTestTemplateStore(t)
	sections := []TemplateSection{{Key: "part_a", Title: "章节甲"}}
	for i := 0; i < 50; i++ {
		if _, err := ts.Create(fmt.Sprintf("模板%d", i+1), sections, fmt.Sprintf("key-%d", i+1)); err != nil {
			t.Fatalf("第 %d 套应可建: %v", i+1, err)
		}
	}
	if _, err := ts.Create("第51套", sections, "key-51"); err == nil || !strings.Contains(err.Error(), "最多 50 套") {
		t.Fatalf("第 51 套应拒绝: %v", err)
	}
}

func mustReadFile(t *testing.T, path string) []byte {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读文件失败: %v", err)
	}
	return raw
}
