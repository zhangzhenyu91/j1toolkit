// 题库存储测试：表头别名、答案归一、题型推断、库内去重与 JSON 兼容（fake 表格行，临时目录落盘）。
package quizsearch

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func testStore(t *testing.T) *BankStore {
	t.Helper()
	s, err := NewBankStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	s.now = func() time.Time { return time.Date(2026, 10, 28, 8, 0, 0, 0, time.UTC) }
	return s
}

func TestMapHeaders(t *testing.T) {
	col := MapHeaders([]string{"题目", "答案", "选项Ａ", "选项 b", "解析", "题型", ""})
	want := map[string]int{
		"content":  0,
		"answer":   1,
		"optionA":  2,
		"optionB":  3,
		"analysis": 4,
		"type":     5,
	}
	for k, v := range want {
		if col[k] != v {
			t.Errorf("col[%s] = %d, want %d", k, col[k], v)
		}
	}
	// 同字段多列以靠后列为准（同 .cjs）
	col2 := MapHeaders([]string{"question", "题干", "答案"})
	if col2["content"] != 1 {
		t.Errorf("content col = %d, want 1（后列覆盖）", col2["content"])
	}
}

func TestNormAnswerAndJudge(t *testing.T) {
	cases := []struct{ in, want string }{
		// .cjs 仅剔除「，、」与空白（正则 [，、\s] 原文如此）：半角逗号保留并参与排序
		{"b, a、c ", ",ABC"},
		{"b、a c", "ABC"},
		{"AB", "AB"},
		{"a　b", "AB"},
		{"对", "对"},
	}
	for _, c := range cases {
		if got := NormAnswer(c.in); got != c.want {
			t.Errorf("NormAnswer(%q) = %q, want %q", c.in, got, c.want)
		}
	}
	judge := []struct{ in, want string }{
		{"对", "A"}, {"√", "A"}, {"T", "A"}, {"TRUE", "A"}, {"YES", "A"}, {"正确", "A"},
		{"错", "B"}, {"×", "B"}, {"X", "B"}, {"F", "B"}, {"FALSE", "B"}, {"错误", "B"},
		{"Z", "Z"},
	}
	for _, c := range judge {
		if got := NormJudgeAnswer(c.in); got != c.want {
			t.Errorf("NormJudgeAnswer(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestImportRowsInferenceAndDedupe(t *testing.T) {
	s := testStore(t)
	rows := [][]string{
		{"题干", "答案", "选项A", "选项B", "选项C", "选项D", "题型", "解析"},
		{"单选题干一", "a", "甲", "乙", "丙", "丁", "", "解析一"},
		{"多选题干二", "c、b", "甲", "乙", "丙", "丁", "", ""},
		{"判断题干三", "对", "", "", "", "", "", ""},       // 答案词推断判断题
		{"判断题干四", "B", "", "", "", "", "", ""},       // 无选项 A/B → 判断题
		{"显式题干五", "A", "正确", "错误", "", "", "判断", ""}, // 题型列显式
		{"单选题干一", "A", "甲", "乙", "丙", "丁", "", ""},   // 重复题干+答案 → 去重
		{"", "A", "甲", "", "", "", "", ""},           // 空题干 → dropped
		{"空答案题干", "", "甲", "", "", "", "", ""},       // 空答案 → dropped
	}
	r, err := s.ImportRows("测试库", "测试.xlsx", rows)
	if err != nil {
		t.Fatal(err)
	}
	if r.Count != 5 || r.Dropped != 3 {
		t.Fatalf("count=%d dropped=%d, want 5/3", r.Count, r.Dropped)
	}
	bank := s.GetBank(r.ID)
	if bank == nil {
		t.Fatal("bank file missing")
	}
	q := bank.Questions
	if q[0].Type != "single" || q[0].Answer != "A" || q[0].Analysis != "解析一" {
		t.Errorf("q0 = %+v", q[0])
	}
	if q[1].Type != "multiple" || q[1].Answer != "BC" {
		t.Errorf("q1 = %+v", q[1])
	}
	if q[2].Type != "judge" || q[2].Answer != "A" || len(q[2].Options) != 2 || q[2].Options[0] != "正确" {
		t.Errorf("q2 = %+v", q[2])
	}
	if q[3].Type != "judge" || q[3].Answer != "B" {
		t.Errorf("q3 = %+v", q[3])
	}
	if q[4].Type != "judge" || q[4].Answer != "A" {
		t.Errorf("q4 = %+v", q[4])
	}
	if bank.ImportedAt != "2026-10-28T08:00:00.000Z" {
		t.Errorf("importedAt = %q", bank.ImportedAt)
	}
}

func TestImportRowsErrors(t *testing.T) {
	s := testStore(t)
	if _, err := s.ImportRows("x", "x", [][]string{{"只有表头"}}); err == nil {
		t.Error("want 缺少数据行 error")
	}
	if _, err := s.ImportRows("x", "x", [][]string{{"列一", "列二"}, {"1", "2"}}); err == nil {
		t.Error("want 未识别表头 error")
	}
	if _, err := s.ImportRows("x", "x", [][]string{{"题干", "答案"}, {"", ""}}); err == nil {
		t.Error("want 未解析到有效题目 error")
	}
}

func TestJSONCompatWithElectronLayout(t *testing.T) {
	dir := t.TempDir()
	// 按 .cjs 写盘形状造夹具：banks-index.json 缩进 2 空格；banks/<id>.json 紧凑单行
	index := `{"banks":[{"id":"abc123","name":"旧库","sourceFile":"旧.xlsx","importedAt":"2026-01-02T03:04:05.678Z","count":1,"enabled":false}]}`
	bank := `{"id":"abc123","name":"旧库","sourceFile":"旧.xlsx","importedAt":"2026-01-02T03:04:05.678Z","questions":[{"type":"single","content":"旧题干","options":["甲","乙"],"answer":"A","analysis":"","sort":0}]}`
	if err := os.WriteFile(filepath.Join(dir, "banks-index.json"), []byte(index), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "banks"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "banks", "abc123.json"), []byte(bank), 0o644); err != nil {
		t.Fatal(err)
	}
	s, err := NewBankStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	list := s.List()
	if len(list) != 1 || list[0].ID != "abc123" || list[0].Count != 1 || list[0].Enabled {
		t.Fatalf("list = %+v", list)
	}
	b := s.GetBank("abc123")
	if b == nil || len(b.Questions) != 1 || b.Questions[0].Content != "旧题干" {
		t.Fatalf("bank = %+v", b)
	}
	// 停用库不进合并
	if n := len(s.AllEnabledQuestions()); n != 0 {
		t.Fatalf("enabled merged = %d, want 0", n)
	}
	// Go 写盘后字段名/形状仍可被 .cjs 读取（键集合比对）
	s.now = func() time.Time { return time.Date(2026, 10, 28, 8, 0, 0, 0, time.UTC) }
	r, err := s.ImportRows("新库", "新.xlsx", [][]string{{"题干", "答案"}, {"题干文本", "A"}})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "banks", r.ID+".json"))
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"id", "name", "sourceFile", "importedAt", "questions"} {
		if _, ok := m[k]; !ok {
			t.Errorf("bank json missing key %q", k)
		}
	}
	q0 := m["questions"].([]any)[0].(map[string]any)
	for _, k := range []string{"type", "content", "options", "answer", "analysis", "sort"} {
		if _, ok := q0[k]; !ok {
			t.Errorf("question json missing key %q", k)
		}
	}
	idxRaw, err := os.ReadFile(filepath.Join(dir, "banks-index.json"))
	if err != nil {
		t.Fatal(err)
	}
	var idx struct {
		Banks []map[string]any `json:"banks"`
	}
	if err := json.Unmarshal(idxRaw, &idx); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"id", "name", "sourceFile", "importedAt", "count", "enabled"} {
		if _, ok := idx.Banks[len(idx.Banks)-1][k]; !ok {
			t.Errorf("index json missing key %q", k)
		}
	}
}

func TestRenameRemoveSetEnabled(t *testing.T) {
	s := testStore(t)
	r, err := s.ImportRows("", "demo.xlsx", [][]string{{"题干", "答案"}, {"题干文本", "A"}})
	if err != nil {
		t.Fatal(err)
	}
	if r.Name != "demo" {
		t.Errorf("default name = %q, want demo（源文件名去扩展名）", r.Name)
	}
	if err := s.Rename(r.ID, "改名库"); err != nil {
		t.Fatal(err)
	}
	if got := s.List()[0].Name; got != "改名库" {
		t.Errorf("index name = %q", got)
	}
	if got := s.GetBank(r.ID).Name; got != "改名库" {
		t.Errorf("bank name = %q", got)
	}
	if err := s.SetEnabled(r.ID, false); err != nil {
		t.Fatal(err)
	}
	if len(s.AllEnabledQuestions()) != 0 {
		t.Error("disabled bank should not merge")
	}
	if err := s.Remove(r.ID); err != nil {
		t.Fatal(err)
	}
	if len(s.List()) != 0 {
		t.Error("index should be empty after remove")
	}
	if s.GetBank(r.ID) != nil {
		t.Error("bank file should be gone")
	}
}

func TestAllEnabledQuestionsCarryBankName(t *testing.T) {
	s := testStore(t)
	r, err := s.ImportRows("来源库", "a.xlsx", [][]string{{"题干", "答案"}, {"题干文本", "A"}})
	if err != nil {
		t.Fatal(err)
	}
	qs := s.AllEnabledQuestions()
	if len(qs) != 1 || qs[0].BankName != "来源库" {
		t.Fatalf("merged = %+v", qs)
	}
	// 合并输出不回写 bankName 入库文件
	b := s.GetBank(r.ID)
	if b.Questions[0].BankName != "" {
		t.Errorf("bank file question should not carry bankName, got %q", b.Questions[0].BankName)
	}
}
