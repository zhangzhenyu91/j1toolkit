package exceldoc

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"strconv"
	"strings"
	"testing"
)

// unzipEntry 取 docx/xlsx 内单个条目内容。
func unzipEntry(t *testing.T, data []byte, name string) string {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatalf("打开 zip: %v", err)
	}
	for _, f := range zr.File {
		if f.Name != name {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			t.Fatalf("打开条目 %s: %v", name, err)
		}
		defer rc.Close()
		var buf bytes.Buffer
		if _, err := buf.ReadFrom(rc); err != nil {
			t.Fatalf("读条目 %s: %v", name, err)
		}
		return buf.String()
	}
	t.Fatalf("zip 缺少条目 %s", name)
	return ""
}

// mustContain 断言 haystack 含全部子串。
func mustContain(t *testing.T, haystack string, wants ...string) {
	t.Helper()
	for _, w := range wants {
		if !strings.Contains(haystack, w) {
			t.Fatalf("缺少期望内容 %q", w)
		}
	}
}

// mustNotContain 断言 haystack 不含任一子串。
func mustNotContain(t *testing.T, haystack string, bans ...string) {
	t.Helper()
	for _, b := range bans {
		if strings.Contains(haystack, b) {
			t.Fatalf("不应包含 %q", b)
		}
	}
}

func TestWriteWorkbookXMLStructure(t *testing.T) {
	spec := WorkbookSpec{
		Sheets: []SheetSpec{
			{
				Name:  "交跨",
				Title: "检修一班交跨统计表",
				Columns: []Column{
					{Header: "序号", Width: 5},
					{Header: "电压等级", Width: 10},
					{Header: "备注", Width: 24},
				},
				Rows: [][]string{
					{"1", "110kV", "测试&<>"},
					{"2", "", "第二行"},
				},
				IDs: []int64{101, 102},
			},
			{
				Name:  "无标题",
				Title: "",
				Columns: []Column{
					{Header: "名称", Width: 12},
				},
				Rows: [][]string{{"仅表头"}},
				IDs:  []int64{201},
			},
		},
	}
	data, err := WriteWorkbook(spec)
	if err != nil {
		t.Fatalf("WriteWorkbook: %v", err)
	}
	if len(data) == 0 {
		t.Fatal("产物为空")
	}

	// xlsx 是 zip：检查 sheet1.xml 关键结构（合并标题 / 隐藏 ID 列 / 行高 / 列宽）
	sheet1 := unzipEntry(t, data, "xl/worksheets/sheet1.xml")
	mustContain(t, sheet1,
		`ref="A1:C1"`,   // 合并标题 A1:C1
		`hidden="true"`, // 隐藏记录 ID 列
		`ht="45"`,       // 标题行高
		`ht="35"`,       // 表头行高
		`ht="25"`,       // 数据行高
		`width="24"`,    // 备注列宽
		`customHeight`,  // 行高自定义标记
	)
	sheet2 := unzipEntry(t, data, "xl/worksheets/sheet2.xml")
	if strings.Contains(sheet2, `ref="A1:`) {
		t.Fatal("skipTitle 口径不应产生合并标题")
	}
	mustContain(t, sheet2, `ht="35"`, `hidden="true"`)

	// 回读闭环
	wb, err := ReadWorkbook(data)
	if err != nil {
		t.Fatalf("ReadWorkbook: %v", err)
	}
	if len(wb.Sheets) != 2 {
		t.Fatalf("sheet 数 = %d，期望 2", len(wb.Sheets))
	}
	s0 := wb.Sheets[0]
	if s0.Name != "交跨" || s0.Title != "检修一班交跨统计表" {
		t.Fatalf("sheet0 名称/标题不符: %+v", s0)
	}
	if len(s0.TitleMerges) != 1 || s0.TitleMerges[0].Start != "A1" || s0.TitleMerges[0].End != "C1" {
		t.Fatalf("合并区不符: %+v", s0.TitleMerges)
	}
	if s0.HeaderRow != 2 {
		t.Fatalf("表头行 = %d，期望 2", s0.HeaderRow)
	}
	mustContain(t, strings.Join(s0.Headers, "|"), "序号", "电压等级", "备注", "记录ID")
	if len(s0.Rows) != 2 {
		t.Fatalf("数据行 = %d，期望 2", len(s0.Rows))
	}
	mustContain(t, strings.Join(s0.Rows[0], "|"), "110kV", "测试&<>")
	if !s0.IDColHidden {
		t.Fatal("记录 ID 列应隐藏")
	}
	if len(s0.IDs) != 2 || s0.IDs[0] != "101" || s0.IDs[1] != "102" {
		t.Fatalf("记录 ID 列不符: %v", s0.IDs)
	}
	if s0.RowHeights[1] != 45 || s0.RowHeights[2] != 35 || s0.RowHeights[3] != 25 {
		t.Fatalf("行高不符: %v", s0.RowHeights)
	}
	if w := s0.ColWidths["A"]; w != 5 {
		t.Fatalf("A 列宽 = %v，期望 5", w)
	}

	s1 := wb.Sheets[1]
	if s1.Title != "" || s1.HeaderRow != 1 {
		t.Fatalf("skipTitle sheet 不应有标题: %+v", s1)
	}
	mustContain(t, strings.Join(s1.Headers, "|"), "名称")
}

func TestWriteWorkbookRejectsBadSpec(t *testing.T) {
	if _, err := WriteWorkbook(WorkbookSpec{}); err == nil {
		t.Fatal("空工作簿应报错")
	}
	_, err := WriteWorkbook(WorkbookSpec{Sheets: []SheetSpec{{
		Name:    "s",
		Columns: []Column{{Header: "a", Width: 5}},
		Rows:    [][]string{{"1"}, {"2"}},
		IDs:     []int64{1},
	}}})
	if err == nil {
		t.Fatal("ID 数与行数不一致应报错")
	}
}

// TestWorkbookRerunDeterministic 同一 spec 两次生成的 sheet XML 应一致（结构稳定可比对）。
func TestWorkbookRerunDeterministic(t *testing.T) {
	spec := WorkbookSpec{Sheets: []SheetSpec{{
		Name:    "s",
		Title:   "标题",
		Columns: []Column{{Header: "a", Width: 5}},
		Rows:    [][]string{{"x"}},
		IDs:     []int64{1},
	}}}
	a, err := WriteWorkbook(spec)
	if err != nil {
		t.Fatal(err)
	}
	b, err := WriteWorkbook(spec)
	if err != nil {
		t.Fatal(err)
	}
	if unzipEntry(t, a, "xl/worksheets/sheet1.xml") != unzipEntry(t, b, "xl/worksheets/sheet1.xml") {
		t.Fatal("同 spec 两次生成 sheet XML 不一致")
	}
}

// stylesXMLView styles.xml 解析视图（仅取标红校验所需字段）。
type stylesXMLView struct {
	Fonts struct {
		Font []struct {
			Color struct {
				RGB string `xml:"rgb,attr"`
			} `xml:"color"`
		} `xml:"font"`
	} `xml:"fonts"`
	CellXfs struct {
		Xf []struct {
			FontID int `xml:"fontId,attr"`
		} `xml:"xf"`
	} `xml:"cellXfs"`
}

// sheetXMLView worksheet 解析视图（行/单元格样式索引与单元格值）。
type sheetXMLView struct {
	SheetData struct {
		Rows []struct {
			R     int `xml:"r,attr"`
			Cells []struct {
				R string `xml:"r,attr"`
				S string `xml:"s,attr"`
				V string `xml:"v"`
			} `xml:"c"`
		} `xml:"row"`
	} `xml:"sheetData"`
}

// redXfIDs 解析 styles.xml：返回「字体为红色 rgb="FFFF0000"」的 cellXfs 样式索引集合。
func redXfIDs(t *testing.T, stylesXML string) map[string]bool {
	t.Helper()
	var v stylesXMLView
	if err := xml.Unmarshal([]byte(stylesXML), &v); err != nil {
		t.Fatalf("解析 styles.xml: %v", err)
	}
	redFonts := map[int]bool{}
	for i, f := range v.Fonts.Font {
		if strings.EqualFold(strings.TrimSpace(f.Color.RGB), "FFFF0000") {
			redFonts[i] = true
		}
	}
	out := map[string]bool{}
	for i, xf := range v.CellXfs.Xf {
		if redFonts[xf.FontID] {
			out[strconv.Itoa(i)] = true
		}
	}
	return out
}

// parseSheetXML 解析 worksheet XML。
func parseSheetXML(t *testing.T, sheetXML string) sheetXMLView {
	t.Helper()
	var v sheetXMLView
	if err := xml.Unmarshal([]byte(sheetXML), &v); err != nil {
		t.Fatalf("解析 worksheet XML: %v", err)
	}
	return v
}

// assertRowRed 断言某数据行整行（含隐藏 ID 列）样式是否全部为红：want=true 时行内全部单元格
// 须落在红样式集；want=false 时不得出现红样式。
func assertRowRed(t *testing.T, v sheetXMLView, rowNum int, reds map[string]bool, want bool) {
	t.Helper()
	for _, row := range v.SheetData.Rows {
		if row.R != rowNum {
			continue
		}
		if len(row.Cells) == 0 {
			t.Fatalf("行 %d 无单元格", rowNum)
		}
		redCount := 0
		for _, c := range row.Cells {
			if reds[c.S] {
				redCount++
			}
		}
		if want && redCount != len(row.Cells) {
			t.Fatalf("行 %d 应整行标红，实际 %d/%d 单元格红：%+v", rowNum, redCount, len(row.Cells), row.Cells)
		}
		if !want && redCount != 0 {
			t.Fatalf("行 %d 不应标红，实际 %d 单元格红：%+v", rowNum, redCount, row.Cells)
		}
		return
	}
	t.Fatalf("未找到行 %d", rowNum)
}

// TestWriteWorkbookRedRows 标红行：styles.xml 出红字体，行级样式整行（含隐藏 ID 列）命中，未标红行不沾红。
func TestWriteWorkbookRedRows(t *testing.T) {
	spec := WorkbookSpec{Sheets: []SheetSpec{{
		Name:  "树障",
		Title: "检修一班树木统计表",
		Columns: []Column{
			{Header: "序号", Width: 5},
			{Header: "区段", Width: 15},
		},
		Rows:    [][]string{{"1", "3#-4#"}, {"2", "10#-11#"}, {"3", "1#-2#"}},
		IDs:     []int64{11, 12, 13},
		RedRows: []bool{false, true, false},
	}}}
	data, err := WriteWorkbook(spec)
	if err != nil {
		t.Fatalf("WriteWorkbook: %v", err)
	}
	styles := unzipEntry(t, data, "xl/styles.xml")
	mustContain(t, styles, `rgb="FFFF0000"`)
	reds := redXfIDs(t, styles)
	if len(reds) != 2 {
		t.Fatalf("应生成数据行 + 隐藏 ID 列两套红样式，实际 %v", reds)
	}
	v := parseSheetXML(t, unzipEntry(t, data, "xl/worksheets/sheet1.xml"))
	// 行号：1 标题 / 2 表头 / 3..5 数据；第 2 条数据行（r=4）标红
	assertRowRed(t, v, 3, reds, false)
	assertRowRed(t, v, 4, reds, true)
	assertRowRed(t, v, 5, reds, false)
	// 表头行不沾红
	assertRowRed(t, v, 2, reds, false)
}

// TestWriteWorkbookRedRowsLazy 无标红行时不生成红字体样式（其他模块导出 styles.xml 不变）。
func TestWriteWorkbookRedRowsLazy(t *testing.T) {
	spec := WorkbookSpec{Sheets: []SheetSpec{{
		Name:    "s",
		Title:   "标题",
		Columns: []Column{{Header: "a", Width: 5}},
		Rows:    [][]string{{"x"}, {"y"}},
		IDs:     []int64{1, 2},
		RedRows: []bool{false, false},
	}}}
	data, err := WriteWorkbook(spec)
	if err != nil {
		t.Fatal(err)
	}
	styles := unzipEntry(t, data, "xl/styles.xml")
	mustNotContain(t, styles, `rgb="FFFF0000"`)
	if reds := redXfIDs(t, styles); len(reds) != 0 {
		t.Fatalf("无标红行不应有红样式，实际 %v", reds)
	}
}

// TestWriteWorkbookRedRowsMismatch RedRows 长度与数据行数不一致应报错。
func TestWriteWorkbookRedRowsMismatch(t *testing.T) {
	_, err := WriteWorkbook(WorkbookSpec{Sheets: []SheetSpec{{
		Name:    "s",
		Columns: []Column{{Header: "a", Width: 5}},
		Rows:    [][]string{{"x"}, {"y"}},
		IDs:     []int64{1, 2},
		RedRows: []bool{true},
	}}})
	if err == nil {
		t.Fatal("标红行数与数据行数不一致应报错")
	}
}
