// Excel 导入/模板测试：模板闭环可导、多工作表合并去重复表头、.xls 明确报错。
package quizsearch

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/xuri/excelize/v2"
)

func TestWriteTemplateRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "题库模板.xlsx")
	if err := WriteTemplate(path); err != nil {
		t.Fatal(err)
	}
	rows, err := ReadWorkbookRows(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 4 {
		t.Fatalf("rows = %d, want 4（表头+3 示例）", len(rows))
	}
	for i, h := range templateHeaders {
		if rows[0][i] != h {
			t.Errorf("header[%d] = %q, want %q", i, rows[0][i], h)
		}
	}
	// 模板闭环可导：示例行按导入器口径应解析出 3 题（单选/多选/判断）
	s := testStore(t)
	r, err := s.ImportFile("", path)
	if err != nil {
		t.Fatal(err)
	}
	if r.Count != 3 || r.Dropped != 0 {
		t.Fatalf("import template: count=%d dropped=%d, want 3/0", r.Count, r.Dropped)
	}
	bank := s.GetBank(r.ID)
	if bank.Questions[0].Type != "single" || bank.Questions[1].Type != "multiple" || bank.Questions[2].Type != "judge" {
		t.Errorf("types = %s/%s/%s", bank.Questions[0].Type, bank.Questions[1].Type, bank.Questions[2].Type)
	}
}

func TestReadWorkbookRowsMergesSheets(t *testing.T) {
	path := filepath.Join(t.TempDir(), "multi.xlsx")
	f := excelize.NewFile()
	defer f.Close()
	if err := f.SetSheetName("Sheet1", "第一"); err != nil {
		t.Fatal(err)
	}
	for r, row := range [][]string{
		{"题干", "答案"},
		{"甲题", "A"},
	} {
		for c, v := range row {
			cell, _ := excelize.CoordinatesToCellName(c+1, r+1)
			if err := f.SetCellValue("第一", cell, v); err != nil {
				t.Fatal(err)
			}
		}
	}
	if _, err := f.NewSheet("第二"); err != nil {
		t.Fatal(err)
	}
	for r, row := range [][]string{
		{"题干", "答案"}, // 重复表头 → 丢弃
		{"乙题", "B"},
	} {
		for c, v := range row {
			cell, _ := excelize.CoordinatesToCellName(c+1, r+1)
			if err := f.SetCellValue("第二", cell, v); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := f.SaveAs(path); err != nil {
		t.Fatal(err)
	}
	rows, err := ReadWorkbookRows(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 3 {
		t.Fatalf("merged rows = %d, want 3（两表数据 + 仅首个表头）", len(rows))
	}
	if rows[0][0] != "题干" || rows[1][0] != "甲题" || rows[2][0] != "乙题" {
		t.Errorf("rows = %v", rows)
	}
}

func TestReadWorkbookRowsRejectsXls(t *testing.T) {
	_, err := ReadWorkbookRows(filepath.Join(t.TempDir(), "旧.xls"))
	if err == nil || !strings.Contains(err.Error(), "xlsx") {
		t.Fatalf("want .xls 提示另存为 .xlsx, got %v", err)
	}
}
