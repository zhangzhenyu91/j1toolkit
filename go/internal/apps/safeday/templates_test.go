package safeday

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"shade/internal/compat/exceldoc"
	"shade/internal/config"
)

// testCipher 测试用 ConfigCipher（主密钥从 JWT_SECRET 派生，不落真实密钥）。
func testCipher(t *testing.T) *config.ConfigCipher {
	t.Helper()
	c, err := config.NewConfigCipher("", "unit-test-jwt-secret")
	if err != nil {
		t.Fatalf("构造加密器失败: %v", err)
	}
	return c
}

// buildTestDocx 合成含指定段落的最小 docx。
func buildTestDocx(t *testing.T, paras []string) []byte {
	t.Helper()
	xml := `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:body>` +
		strings.Join(paras, "") +
		`<w:sectPr></w:sectPr></w:body></w:document>`
	parts := map[string][]byte{
		"[Content_Types].xml": []byte(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"><Default Extension="xml" ContentType="application/xml"/><Override PartName="/word/document.xml" ContentType="application/vnd.openxmlformats-officedocument.wordprocessingml.document.main+xml"/></Types>`),
		"word/document.xml": []byte(xml),
	}
	data, err := exceldoc.BuildDocx(parts)
	if err != nil {
		t.Fatalf("合成 docx 失败: %v", err)
	}
	return data
}

// headerParas 安全日模板通用字段段落。
func headerParas() []string {
	var paras []string
	for _, key := range []string{"班组名称", "学习内容", "活动时间", "主持人", "上级参加人员", "本班组参加人员", "缺席人员", "缺席人员原因", "记录人"} {
		paras = append(paras, `<w:p><w:r><w:t>{`+key+`}</w:t></w:r></w:p>`)
	}
	return paras
}

// dynamicDocxBytes 自定义动态模板（含独占 {@章节区}）。
func dynamicDocxBytes(t *testing.T) []byte {
	t.Helper()
	paras := append([]string{`<w:p><w:r><w:t>安全日活动</w:t></w:r></w:p>`}, headerParas()...)
	paras = append(paras, `<w:p><w:r><w:t>{@章节区}</w:t></w:r></w:p>`)
	return buildTestDocx(t, paras)
}

// staticDocxBytes 内置静态模板（六段 {@标题}）。
func staticDocxBytes(t *testing.T) []byte {
	t.Helper()
	paras := headerParas()
	for _, key := range rawXMLKeys {
		paras = append(paras, `<w:p><w:r><w:t>{@`+key+`}</w:t></w:r></w:p>`)
	}
	return buildTestDocx(t, paras)
}

// writeBytesFile 写临时文件并返回路径。
func writeBytesFile(t *testing.T, data []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "tpl.docx")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("写文件失败: %v", err)
	}
	return path
}

// newTestTemplateStore 构造测试模板库（内置路径指向合成静态模板）。
func newTestTemplateStore(t *testing.T) (*TemplateStore, string) {
	t.Helper()
	dir := t.TempDir()
	ts, err := NewTemplateStore(dir, testCipher(t))
	if err != nil {
		t.Fatalf("构造模板库失败: %v", err)
	}
	ts.BuiltinPath = writeBytesFile(t, staticDocxBytes(t))
	ts.SamplePath = writeBytesFile(t, dynamicDocxBytes(t))
	return ts, dir
}

func testSections() []TemplateSection {
	return []TemplateSection{{Key: "part_a", Title: "章节甲"}, {Key: "part_b", Title: "章节乙"}}
}

// TestTemplateStoreBuiltinAndCRUD 内置模板 + 自定义模板 CRUD/默认/内容寻址文件。
func TestTemplateStoreBuiltinAndCRUD(t *testing.T) {
	ts, dir := newTestTemplateStore(t)

	list := ts.List()
	if len(list) != 1 || list[0].ID != BuiltinTemplateID || list[0].Mode != TemplateModeStatic {
		t.Fatalf("初始应只有内置模板: %+v", list)
	}
	if len(list[0].Sections) != 6 {
		t.Fatalf("内置模板应含六段章节: %+v", list[0].Sections)
	}
	if ts.DefaultID() != BuiltinTemplateID {
		t.Fatalf("默认模板应为 builtin: %s", ts.DefaultID())
	}
	snap, err := ts.Snapshot(BuiltinTemplateID)
	if err != nil {
		t.Fatalf("内置快照失败: %v", err)
	}
	if snap.Mode != TemplateModeStatic || snap.SHA256 == "" || len(snap.Sections) != 6 {
		t.Fatalf("内置快照不符: %+v", snap)
	}
	if key, err := ts.WorkflowAPIKey(BuiltinTemplateID); err != nil || key != "" {
		t.Fatalf("内置模板应使用全局 key（返回空）: key=%q err=%v", key, err)
	}

	docx := dynamicDocxBytes(t)
	tpl, err := ts.Create("自定义模板", testSections(), "app-secret-key-1", docx)
	if err != nil {
		t.Fatalf("新建模板失败: %v", err)
	}
	if tpl.ID == "" || tpl.ID == BuiltinTemplateID || tpl.Mode != TemplateModeDynamic || !tpl.HasAPIKey {
		t.Fatalf("新建模板字段不符: %+v", tpl)
	}
	if tpl.SHA256 == "" || tpl.Size != int64(len(docx)) {
		t.Fatalf("新建模板文件元数据不符: %+v", tpl)
	}
	if _, ok := ts.Get(tpl.ID); !ok {
		t.Fatalf("新建模板应可查询")
	}
	// index.json 只允许密文，绝不含明文 key
	raw, err := os.ReadFile(filepath.Join(dir, "templates", "index.json"))
	if err != nil {
		t.Fatalf("读索引失败: %v", err)
	}
	if !bytes.Contains(raw, []byte(config.EncPrefix)) {
		t.Fatalf("索引应保存 enc:v1: 密文: %s", raw)
	}
	if bytes.Contains(raw, []byte("app-secret-key-1")) {
		t.Fatalf("索引不得含明文 API Key: %s", raw)
	}
	// 内容寻址文件落盘
	if _, err := os.Stat(filepath.Join(dir, "templates", "files", tpl.SHA256+".docx")); err != nil {
		t.Fatalf("模板 docx 应按 sha 落盘: %v", err)
	}
	if key, err := ts.WorkflowAPIKey(tpl.ID); err != nil || key != "app-secret-key-1" {
		t.Fatalf("模板 key 解密不符: key=%q err=%v", key, err)
	}

	// 编辑：空 key 保留原 key
	updated, err := ts.Update(tpl.ID, TemplateUpdate{Name: "改名模板", Sections: []TemplateSection{{Key: "part_c", Title: "章节丙"}}})
	if err != nil {
		t.Fatalf("编辑模板失败: %v", err)
	}
	if updated.Name != "改名模板" || len(updated.Sections) != 1 || updated.Sections[0].Key != "part_c" || !updated.HasAPIKey {
		t.Fatalf("编辑结果不符: %+v", updated)
	}
	if key, _ := ts.WorkflowAPIKey(tpl.ID); key != "app-secret-key-1" {
		t.Fatalf("留空 key 应保留原值: %q", key)
	}
	// 编辑：新 key 覆盖
	if _, err := ts.Update(tpl.ID, TemplateUpdate{APIKey: "app-secret-key-2"}); err != nil {
		t.Fatalf("更新 key 失败: %v", err)
	}
	if key, _ := ts.WorkflowAPIKey(tpl.ID); key != "app-secret-key-2" {
		t.Fatalf("新 key 未生效: %q", key)
	}

	// 替换 docx：sha 变化，旧文件保留（在途记录可继续渲染）
	oldSHA := updated.SHA256
	newDocx := buildTestDocx(t, append(headerParas(),
		`<w:p><w:r><w:t>{@章节区}</w:t></w:r></w:p>`,
		`<w:p><w:r><w:t>替换后附加段落</w:t></w:r></w:p>`))
	replaced, err := ts.ReplaceDocx(tpl.ID, newDocx)
	if err != nil {
		t.Fatalf("替换 docx 失败: %v", err)
	}
	if replaced.SHA256 == oldSHA {
		t.Fatalf("替换不同内容后 sha 应变化")
	}
	if _, err := os.Stat(filepath.Join(dir, "templates", "files", oldSHA+".docx")); err != nil {
		t.Fatalf("旧 sha 文件应保留: %v", err)
	}

	// 设默认 + 删除：默认回落 builtin，文件保留
	if err := ts.SetDefault(tpl.ID); err != nil {
		t.Fatalf("设默认失败: %v", err)
	}
	if ts.DefaultID() != tpl.ID {
		t.Fatalf("默认模板未生效: %s", ts.DefaultID())
	}
	list = ts.List()
	if !list[1].IsDefault {
		t.Fatalf("列表应标记默认模板: %+v", list)
	}
	if err := ts.Delete(tpl.ID); err != nil {
		t.Fatalf("删除模板失败: %v", err)
	}
	if _, ok := ts.Get(tpl.ID); ok {
		t.Fatalf("删除后不应可查")
	}
	if ts.DefaultID() != BuiltinTemplateID {
		t.Fatalf("删除默认模板后应回落 builtin: %s", ts.DefaultID())
	}
	if _, err := os.Stat(filepath.Join(dir, "templates", "files", replaced.SHA256+".docx")); err != nil {
		t.Fatalf("删除模板不得清理内容文件: %v", err)
	}

	// 重载：库内自定义模板持久化
	tpl2, err := ts.Create("持久化", testSections(), "app-secret-key-3", docx)
	if err != nil {
		t.Fatalf("再次新建失败: %v", err)
	}
	reloaded, err := NewTemplateStore(dir, testCipher(t))
	if err != nil {
		t.Fatalf("重载模板库失败: %v", err)
	}
	reloaded.BuiltinPath = ts.BuiltinPath
	if got, ok := reloaded.Get(tpl2.ID); !ok || got.Name != "持久化" {
		t.Fatalf("模板库重载不符: %+v ok=%v", got, ok)
	}
	if key, _ := reloaded.WorkflowAPIKey(tpl2.ID); key != "app-secret-key-3" {
		t.Fatalf("重载后 key 解密不符: %q", key)
	}
}

// TestTemplateStoreValidation 模板参数与 docx 校验；错误文案不得含 API Key。
func TestTemplateStoreValidation(t *testing.T) {
	ts, _ := newTestTemplateStore(t)
	docx := dynamicDocxBytes(t)
	noPlaceholder := buildTestDocx(t, headerParas())
	mixedPlaceholder := buildTestDocx(t, append(headerParas(), `<w:p><w:r><w:t>前 {@章节区} 后</w:t></w:r></w:p>`))
	invalidDocx := []byte("not-a-docx")
	key := "app-validation-secret"

	cases := []struct {
		name     string
		tplName  string
		sections []TemplateSection
		apiKey   string
		docx     []byte
		want     string
	}{
		{"名称空", "  ", testSections(), key, docx, "模板名称不能为空"},
		{"章节空", "X", nil, key, docx, "模板章节不能为空"},
		{"章节 key 非法", "X", []TemplateSection{{Key: "Bad-Key", Title: "甲"}}, key, docx, "章节标识不合法"},
		{"章节 key 重复", "X", []TemplateSection{{Key: "a", Title: "甲"}, {Key: "a", Title: "乙"}}, key, docx, "章节标识重复"},
		{"章节标题空", "X", []TemplateSection{{Key: "a", Title: " "}}, key, docx, "章节标题不能为空"},
		{"保留 key", "X", []TemplateSection{{Key: "date", Title: "甲"}}, key, docx, "章节标识不合法"},
		{"API Key 空", "X", testSections(), "", docx, "请填写 Dify API Key"},
		{"docx 空", "X", testSections(), key, nil, "请上传模板 docx 文件"},
		{"docx 非法", "X", testSections(), key, invalidDocx, "不是有效的 docx"},
		{"缺占位符", "X", testSections(), key, noPlaceholder, "{@章节区}"},
		{"占位符非独占", "X", testSections(), key, mixedPlaceholder, "独占一行"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ts.Create(tc.tplName, tc.sections, tc.apiKey, tc.docx)
			if err == nil {
				t.Fatalf("应校验失败")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("错误文案不符: %v", err)
			}
			if tc.apiKey != "" && strings.Contains(err.Error(), tc.apiKey) {
				t.Fatalf("错误文案不得包含 API Key: %v", err)
			}
		})
	}

	// 加密器缺失时拒绝保存 Key
	bare, err := NewTemplateStore(t.TempDir(), nil)
	if err != nil {
		t.Fatalf("构造无加密器模板库失败: %v", err)
	}
	bare.BuiltinPath = ts.BuiltinPath
	if _, err := bare.Create("X", testSections(), key, docx); err == nil || !strings.Contains(err.Error(), "加密器") {
		t.Fatalf("无加密器应拒绝新建: %v", err)
	}

	// 内置模板只读
	if _, err := ts.Update(BuiltinTemplateID, TemplateUpdate{Name: "改"}); !errors.Is(err, ErrTemplateBuiltinReadonly) {
		t.Fatalf("内置模板应只读: %v", err)
	}
	if _, err := ts.ReplaceDocx(BuiltinTemplateID, docx); !errors.Is(err, ErrTemplateBuiltinReadonly) {
		t.Fatalf("内置模板 docx 应只读: %v", err)
	}
	if err := ts.Delete(BuiltinTemplateID); !errors.Is(err, ErrTemplateBuiltinReadonly) {
		t.Fatalf("内置模板不可删除: %v", err)
	}
	if _, ok := ts.Get("missing"); ok {
		t.Fatalf("未知模板不应可查")
	}
}

// TestTemplateStoreMissingKeyAndFile 缺失 key/docx 明确报错且不泄漏密文。
func TestTemplateStoreMissingKeyAndFile(t *testing.T) {
	ts, dir := newTestTemplateStore(t)
	tpl, err := ts.Create("X", testSections(), "app-key-missing", dynamicDocxBytes(t))
	if err != nil {
		t.Fatalf("新建模板失败: %v", err)
	}
	// 手写索引模拟历史模板缺 key
	idxPath := filepath.Join(dir, "templates", "index.json")
	var idx templateIndex
	if err := json.Unmarshal(mustReadFile(t, idxPath), &idx); err != nil {
		t.Fatalf("解析索引失败: %v", err)
	}
	for i := range idx.Templates {
		idx.Templates[i].APIKeyEnc = ""
	}
	raw, _ := json.Marshal(idx)
	if err := os.WriteFile(idxPath, raw, 0o644); err != nil {
		t.Fatalf("写索引失败: %v", err)
	}
	ts2, err := NewTemplateStore(dir, testCipher(t))
	if err != nil {
		t.Fatalf("重载模板库失败: %v", err)
	}
	if _, err := ts2.WorkflowAPIKey(tpl.ID); !errors.Is(err, ErrTemplateKeyMissing) {
		t.Fatalf("缺 key 应返回明确错误: %v", err)
	}
	// 恢复索引后删 docx 文件
	if err := os.WriteFile(idxPath, mustReadFile(t, idxPath), 0o644); err != nil {
		_ = err
	}
	ts3, _ := NewTemplateStore(dir, testCipher(t))
	if err := os.Remove(filepath.Join(dir, "templates", "files", tpl.SHA256+".docx")); err != nil {
		t.Fatalf("删除模板文件失败: %v", err)
	}
	if _, err := ts3.Snapshot(tpl.ID); !errors.Is(err, ErrTemplateFileMissing) {
		t.Fatalf("缺 docx 应返回明确错误: %v", err)
	}
	if _, err := ts3.DocxBySHA(tpl.SHA256); !errors.Is(err, ErrTemplateFileMissing) {
		t.Fatalf("按 sha 取缺失文件应返回明确错误: %v", err)
	}
	if _, err := ts3.DocxBySHA("../../etc/passwd"); err == nil {
		t.Fatalf("非法 sha 应拒绝")
	}
}

// TestTemplateStoreConcurrentReads 并发读取/渲染不互斥阻塞、结果一致。
func TestTemplateStoreConcurrentReads(t *testing.T) {
	ts, _ := newTestTemplateStore(t)
	tpl, err := ts.Create("并发", testSections(), "app-key-concurrent", dynamicDocxBytes(t))
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
				if _, err := ts.DocxBySHA(tpl.SHA256); err != nil {
					t.Errorf("并发取模板文件失败: %v", err)
					return
				}
				_ = ts.List()
			}
		}()
	}
	wg.Wait()
}

// TestSnapshotRejectsCorruptedDocx 快照必须真读 docx 并校验 sha/结构，不能只 Stat。
func TestSnapshotRejectsCorruptedDocx(t *testing.T) {
	ts, dir := newTestTemplateStore(t)
	tpl, err := ts.Create("损坏模板", testSections(), "key-corrupt", dynamicDocxBytes(t))
	if err != nil {
		t.Fatalf("建模板失败: %v", err)
	}
	// 同 sha 文件名写入损坏内容：sha 不一致且不是合法 docx
	path := filepath.Join(dir, "templates", "files", tpl.SHA256+".docx")
	if err := os.WriteFile(path, []byte("corrupted-not-docx"), 0o644); err != nil {
		t.Fatalf("写损坏文件失败: %v", err)
	}
	if _, err := ts.Snapshot(tpl.ID); err == nil || !strings.Contains(err.Error(), "模板文件损坏") {
		t.Fatalf("损坏 docx 应明确报错: %v", err)
	}
	if _, err := ts.DocxBySHA(tpl.SHA256); err == nil || !strings.Contains(err.Error(), "模板文件损坏") {
		t.Fatalf("按 sha 读取损坏文件应报错: %v", err)
	}

	// sha 与索引一致但结构不合法（缺独占 {@章节区}）也要在快照阶段拒绝
	noPlaceholder := buildTestDocx(t, headerParas())
	sum := sha256.Sum256(noPlaceholder)
	badSHA := hex.EncodeToString(sum[:])
	if err := os.WriteFile(filepath.Join(dir, "templates", "files", badSHA+".docx"), noPlaceholder, 0o644); err != nil {
		t.Fatalf("写结构不合法文件失败: %v", err)
	}
	ts.mu.Lock()
	for i := range ts.index.Templates {
		if ts.index.Templates[i].ID == tpl.ID {
			ts.index.Templates[i].SHA256 = badSHA
		}
	}
	if err := ts.writeIndexLocked(); err != nil {
		ts.mu.Unlock()
		t.Fatalf("写索引失败: %v", err)
	}
	ts.mu.Unlock()
	if _, err := ts.Snapshot(tpl.ID); err == nil || !strings.Contains(err.Error(), "模板文件损坏") {
		t.Fatalf("结构不合法模板应在快照阶段报错: %v", err)
	}

	// 内置模板也至少要做 docx 解析校验
	ts.BuiltinPath = writeBytesFile(t, []byte("not-a-docx"))
	if _, err := ts.Snapshot(BuiltinTemplateID); err == nil || !strings.Contains(err.Error(), "模板文件损坏") {
		t.Fatalf("损坏的内置模板应明确报错: %v", err)
	}
}

// TestReplaceDocxUnknownIDNoOrphan 未知模板替换 docx 不得先落内容文件产生孤儿。
func TestReplaceDocxUnknownIDNoOrphan(t *testing.T) {
	ts, _ := newTestTemplateStore(t)
	countFiles := func() int {
		entries, err := os.ReadDir(ts.filesDir)
		if err != nil {
			t.Fatalf("读模板文件目录失败: %v", err)
		}
		return len(entries)
	}
	before := countFiles()
	if _, err := ts.ReplaceDocx("tpl-missing", dynamicDocxBytes(t)); !errors.Is(err, ErrTemplateNotFound) {
		t.Fatalf("未知模板应返回 ErrTemplateNotFound: %v", err)
	}
	if after := countFiles(); after != before {
		t.Fatalf("未知模板不应落内容文件: before=%d after=%d", before, after)
	}
}

// TestTemplateStoreSectionQuota 章节数 1–12：12 段可用，13 段拒绝。
func TestTemplateStoreSectionQuota(t *testing.T) {
	ts, _ := newTestTemplateStore(t)
	makeSections := func(n int) []TemplateSection {
		out := make([]TemplateSection, n)
		for i := range out {
			out[i] = TemplateSection{Key: fmt.Sprintf("part_%d", i), Title: fmt.Sprintf("章节%d", i+1)}
		}
		return out
	}
	if _, err := ts.Create("十二段", makeSections(12), "key-12", dynamicDocxBytes(t)); err != nil {
		t.Fatalf("12 段应允许: %v", err)
	}
	if _, err := ts.Create("十三段", makeSections(13), "key-13", dynamicDocxBytes(t)); err == nil || !strings.Contains(err.Error(), "最多 12 段") {
		t.Fatalf("13 段应拒绝: %v", err)
	}
}

// TestTemplateStoreDocxSizeQuota docx 上限 10MB。
func TestTemplateStoreDocxSizeQuota(t *testing.T) {
	ts, _ := newTestTemplateStore(t)
	big := make([]byte, 10<<20+1)
	if _, err := ts.Create("超大", testSections(), "key-big", big); err == nil || !strings.Contains(err.Error(), "10MB") {
		t.Fatalf("超过 10MB 应拒绝: %v", err)
	}
}

// TestTemplateStoreCountQuota 模板总数上限 50 套。
func TestTemplateStoreCountQuota(t *testing.T) {
	ts, _ := newTestTemplateStore(t)
	docx := dynamicDocxBytes(t)
	sections := []TemplateSection{{Key: "part_a", Title: "章节甲"}}
	for i := 0; i < 50; i++ {
		if _, err := ts.Create(fmt.Sprintf("模板%d", i+1), sections, fmt.Sprintf("key-%d", i+1), docx); err != nil {
			t.Fatalf("第 %d 套应可建: %v", i+1, err)
		}
	}
	if _, err := ts.Create("第51套", sections, "key-51", docx); err == nil || !strings.Contains(err.Error(), "最多 50 套") {
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
