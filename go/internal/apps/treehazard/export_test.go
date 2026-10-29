package treehazard

import (
	"archive/zip"
	"bytes"
	"context"
	"database/sql/driver"
	"encoding/xml"
	"strconv"
	"strings"
	"testing"
	"time"

	"shade/internal/apps/office"
	"shade/internal/compat/exceldoc"
)

func TestParseVerticalNum(t *testing.T) {
	cases := []struct {
		in   string
		want float64
		nil_ bool
	}{
		{in: "超高2", want: 2},
		{in: "3-4", want: 3},
		{in: "2.5m", want: 2.5},
		{in: "", nil_: true},
		{in: "无数字", nil_: true},
	}
	for _, c := range cases {
		got := parseVerticalNum(c.in)
		if c.nil_ {
			if got != nil {
				t.Fatalf("parseVerticalNum(%q) 应为 nil，实际 %v", c.in, *got)
			}
			continue
		}
		if got == nil {
			t.Fatalf("parseVerticalNum(%q) 应为 %v，实际 nil", c.in, c.want)
		}
		if *got != c.want {
			t.Fatalf("parseVerticalNum(%q) 应为 %v，实际 %v", c.in, c.want, *got)
		}
	}
}

func TestNormDateAndSectionPoles(t *testing.T) {
	if got := normDateStr("2026.10.20"); got != "2026-10-20" {
		t.Fatalf("normDateStr 应归一，实际 %q", got)
	}
	if got := normDateStr("2026/1/3"); got != "2026-01-03" {
		t.Fatalf("normDateStr 应补零，实际 %q", got)
	}
	if got := normDateStr("bad"); got != "" {
		t.Fatalf("非法日期应为空，实际 %q", got)
	}
	from, to := parseSectionPoles("3#-4#")
	if from == nil || to == nil || *from != 3 || *to != 4 {
		t.Fatalf("区段解析应为 3/4，实际 %v %v", from, to)
	}
	from, to = parseSectionPoles("无杆号")
	if from != nil || to != nil {
		t.Fatalf("解析不出应为 nil，实际 %v %v", from, to)
	}
}

func TestSplitSectionLocation(t *testing.T) {
	cases := []struct{ in, sec, note string }{
		{"18A#-19#位于××附近", "18A#-19#", "位于××附近"},
		{"12A#-15B#位于苗圃", "12A#-15B#", "位于苗圃"},
		{"3#-4#位于水厂附近", "3#-4#", "位于水厂附近"},
		{"3#-4#（位于水厂附近）", "3#-4#", "位于水厂附近"},
		{"18A#-19#", "18A#-19#", ""},
		{"18A#", "18A#", ""},
		{"3#-4#", "3#-4#", ""},
		{"3号支线中段", "3号支线中段", ""},
		{"无杆号", "无杆号", ""},
		{"位于××附近", "位于××附近", ""},
	}
	for _, c := range cases {
		sec, note := splitSectionLocation(c.in)
		if sec != c.sec || note != c.note {
			t.Fatalf("splitSectionLocation(%q) 应为 (%q, %q)，实际 (%q, %q)", c.in, c.sec, c.note, sec, note)
		}
	}
}

func TestSanitizeRecord(t *testing.T) {
	rec := sanitizeRecord(map[string]any{"line_name_raw": " 甲线 ", "vertical": "超高2", "found_at": "2026.10.20"})
	if rec == nil || rec.LineName != "甲线" {
		t.Fatalf("应回退 line_name_raw，实际 %#v", rec)
	}
	if rec.VerticalNum == nil || *rec.VerticalNum != 2 {
		t.Fatalf("vertical_num 应为 2，实际 %v", rec.VerticalNum)
	}
	if rec.FoundAt != "2026-10-20" {
		t.Fatalf("found_at 应归一，实际 %q", rec.FoundAt)
	}
	if sanitizeRecord(map[string]any{"section": "1#-2#"}) != nil {
		t.Fatalf("缺线路名应返回 nil（计 skipped）")
	}
	if r := sanitizeRecord(map[string]any{"line_name": "乙线", "crane_record_id": " c1 "}); r == nil || r.CraneRecordID == nil || *r.CraneRecordID != "c1" {
		t.Fatalf("crane_record_id 应存非空值，实际 %#v", r)
	}
	if r := sanitizeRecord(map[string]any{"line_name": "乙线", "crane_record_id": ""}); r == nil || r.CraneRecordID != nil {
		t.Fatalf("空 crane_record_id 应存 NULL，实际 %#v", r)
	}
}

func sampleRecords() []Record {
	pole := func(n int64) *int64 { return &n }
	num := func(f float64) *float64 { return &f }
	return []Record{
		{ID: 1, VoltageLevel: "110", LineName: "甲线", Section: "3#-4#", TreeSpecies: "杨树", TreeCount: "2",
			DistSmall: "5", DistBig: "6", Diameter: "20", Horizontal: "3", Vertical: "超高2", VerticalNum: num(2),
			FinderName: "张三", FoundAt: "2026-10-20", Status: "unhandled", PoleFrom: pole(3), PoleTo: pole(4),
			CreatedAt: time.Date(2026, 10, 20, 8, 0, 0, 0, time.UTC)},
		{ID: 2, VoltageLevel: "110", LineName: "甲线", Section: "10#-11#", TreeSpecies: "柳树", TreeCount: "1",
			FinderName: "李四", FoundAt: "2026-10-21", Status: "handled", HandlerName: "王五", HandleNote: "已砍伐",
			HandledAt: "2026-10-22", CreatedAt: time.Date(2026, 10, 21, 8, 0, 0, 0, time.UTC)},
		{ID: 3, VoltageLevel: "220", LineName: "乙线", Section: "1#-2#", TreeSpecies: "松树", TreeCount: "3",
			FinderName: "赵六", FoundAt: "2026-10-19", Status: "unhandled", CreatedAt: time.Date(2026, 10, 19, 8, 0, 0, 0, time.UTC)},
		{ID: 4, VoltageLevel: "110", LineName: "丙线", Section: "7#-8#", TreeSpecies: "柏树", TreeCount: "1",
			FinderName: "钱七", FoundAt: "2026-10-18", Status: "unhandled", CreatedAt: time.Date(2026, 10, 18, 8, 0, 0, 0, time.UTC)},
	}
}

// TestBuildWorkbookSingle 单表导出：标题/表头/行序（未处理在前 > 电压降序 > 线路 > 区段）/隐藏 ID/日期格式。
func TestBuildWorkbookSingle(t *testing.T) {
	buf, err := buildWorkbook("检修一班", sampleRecords(), false, map[string]int{"甲线": 0, "乙线": 1})
	if err != nil {
		t.Fatalf("生成工作簿失败: %v", err)
	}
	wb, err := exceldoc.ReadWorkbook(buf)
	if err != nil {
		t.Fatalf("回读工作簿失败: %v", err)
	}
	if len(wb.Sheets) != 1 {
		t.Fatalf("单表导出应 1 个 sheet，实际 %d", len(wb.Sheets))
	}
	sheet := wb.Sheets[0]
	if sheet.Name != "树障" || sheet.Title != "检修一班树木统计表" {
		t.Fatalf("sheet 名/标题不对：%q %q", sheet.Name, sheet.Title)
	}
	if !sheet.IDColHidden {
		t.Fatalf("记录 ID 列应隐藏")
	}
	if len(sheet.Rows) != 4 {
		t.Fatalf("应 4 行数据，实际 %d", len(sheet.Rows))
	}
	// 行序：未处理（3 条）按电压降序（220 乙线 → 110 甲线/丙线按线路名/区段）在前，已处理最后。
	if sheet.Rows[0][2] != "乙线" {
		t.Fatalf("首行应为电压更高的乙线，实际 %v", sheet.Rows[0])
	}
	last := sheet.Rows[3]
	if last[2] != "甲线" || last[13] != "王五" {
		t.Fatalf("末行应为已处理记录，实际 %v", last)
	}
	if sheet.Rows[0][12] != "2026.10.19" {
		t.Fatalf("发现时间应为 yyyy.MM.dd，实际 %q", sheet.Rows[0][12])
	}
	if sheet.Rows[0][1] != "220kV" {
		t.Fatalf("电压应拼 kV，实际 %q", sheet.Rows[0][1])
	}
	if sheet.Rows[0][16] != "3" {
		t.Fatalf("隐藏 ID 列应为记录 id 3，实际 %q", sheet.Rows[0][16])
	}
}

// TestBuildWorkbookSplit 分表导出：按线路分 sheet + 未匹配排尾「未匹配线路」。
func TestBuildWorkbookSplit(t *testing.T) {
	buf, err := buildWorkbook("检修一班", sampleRecords(), true, map[string]int{"甲线": 0, "乙线": 1})
	if err != nil {
		t.Fatalf("生成工作簿失败: %v", err)
	}
	wb, err := exceldoc.ReadWorkbook(buf)
	if err != nil {
		t.Fatalf("回读工作簿失败: %v", err)
	}
	names := make([]string, 0, len(wb.Sheets))
	for _, s := range wb.Sheets {
		names = append(names, s.Name)
	}
	if len(wb.Sheets) != 3 {
		t.Fatalf("应 3 个 sheet（甲线/乙线/未匹配），实际 %v", names)
	}
	if names[0] != "110kV甲线" || names[1] != "220kV乙线" || names[2] != "未匹配线路" {
		t.Fatalf("sheet 顺序/命名不对：%v", names)
	}
	if wb.Sheets[2].Title != "未匹配线路" {
		t.Fatalf("未匹配 sheet 标题不对：%q", wb.Sheets[2].Title)
	}
	// 甲线 sheet 内：未处理在前（区段 3#-4#）→ 已处理（10#-11#）。
	ji := wb.Sheets[0]
	if len(ji.Rows) != 2 || ji.Rows[0][3] != "3#-4#" || ji.Rows[1][13] != "王五" {
		t.Fatalf("甲线 sheet 内行序不对：%v", ji.Rows)
	}
}

// TestSortRecordsGroupOrder 分组键：team_line 导入序，未匹配排尾。
func TestSortRecordsGroupOrder(t *testing.T) {
	records := sampleRecords()
	lineOrder := map[string]int{"乙线": 0, "甲线": 1}
	sorted := sortRecords(records, makeLineGroupComparator(lineOrder), false)
	if sorted[0].LineName != "乙线" {
		t.Fatalf("导入序应乙线在前，实际 %s", sorted[0].LineName)
	}
	last := sorted[len(sorted)-1]
	if last.LineName != "丙线" {
		t.Fatalf("未匹配应排尾，实际 %s", last.LineName)
	}
	// 组内区段数字自然序（2#-3# 在 10#-20# 前，对齐 Node localeCompare zh-CN numeric）。
	if compareSection("2-10", "10-20") >= 0 {
		t.Fatal("2-10 应排在 10-20 前（数字自然序）")
	}
}

// TestSheetNameOf sheet 名清洗：禁字符转全角、重名追加序号。
func TestSheetNameOf(t *testing.T) {
	used := map[string]bool{}
	name := sheetNameOf("110kV甲/线:1", used)
	if name != "110kV甲／线：1" {
		t.Fatalf("禁字符应转全角，实际 %q", name)
	}
	again := sheetNameOf("110kV甲/线:1", used)
	if again != "110kV甲／线：1（2）" {
		t.Fatalf("重名应追加序号，实际 %q", again)
	}
}

// TestExportFileName 导出文件名：分线路/全量口径。
func TestExportFileName(t *testing.T) {
	if got := exportFileName("检修一班", ""); got != "检修一班树木统计表.xlsx" {
		t.Fatalf("全量文件名不对：%q", got)
	}
	if got := exportFileName("检修一班", "甲线"); got != "甲线树木统计表.xlsx" {
		t.Fatalf("分线路文件名不对：%q", got)
	}
}

// ===== 导出标红（垂距 ≤ 阈值整行红字）=====

// redSampleRecords 标红样例：垂距 2.5（低于阈值）/ 3（等于阈值）/ 4（高于阈值）/ 空值。
func redSampleRecords() []Record {
	num := func(f float64) *float64 { return &f }
	return []Record{
		{ID: 11, VoltageLevel: "110", LineName: "甲线", Section: "1#-2#", TreeSpecies: "杨树", TreeCount: "2",
			Vertical: "2.5", VerticalNum: num(2.5), FinderName: "张三", FoundAt: "2026-10-20", Status: "unhandled",
			CreatedAt: time.Date(2026, 10, 20, 8, 0, 0, 0, time.UTC)},
		{ID: 12, VoltageLevel: "110", LineName: "甲线", Section: "3#-4#", TreeSpecies: "柳树", TreeCount: "1",
			Vertical: "3", VerticalNum: num(3), FinderName: "李四", FoundAt: "2026-10-21", Status: "unhandled",
			CreatedAt: time.Date(2026, 10, 21, 8, 0, 0, 0, time.UTC)},
		{ID: 13, VoltageLevel: "220", LineName: "乙线", Section: "1#-2#", TreeSpecies: "松树", TreeCount: "3",
			Vertical: "4", VerticalNum: num(4), FinderName: "赵六", FoundAt: "2026-10-19", Status: "unhandled",
			CreatedAt: time.Date(2026, 10, 19, 8, 0, 0, 0, time.UTC)},
		{ID: 14, VoltageLevel: "110", LineName: "丙线", Section: "7#-8#", TreeSpecies: "柏树", TreeCount: "1",
			FinderName: "钱七", FoundAt: "2026-10-18", Status: "unhandled",
			CreatedAt: time.Date(2026, 10, 18, 8, 0, 0, 0, time.UTC)},
	}
}

// TestOverThreshold 标红判定：垂距 ≤ 阈值（含等于）标红；未配置阈值/垂距空值不标红。
func TestOverThreshold(t *testing.T) {
	num := func(f float64) *float64 { return &f }
	th := 3.0
	cases := []struct {
		name string
		rec  Record
		th   *float64
		want bool
	}{
		{"低于阈值", Record{VerticalNum: num(2.5)}, &th, true},
		{"等于阈值", Record{VerticalNum: num(3)}, &th, true},
		{"高于阈值", Record{VerticalNum: num(3.5)}, &th, false},
		{"垂距空值", Record{VerticalNum: nil}, &th, false},
		{"未配置阈值", Record{VerticalNum: num(2.5)}, nil, false},
	}
	for _, c := range cases {
		if got := overThreshold(c.rec, c.th); got != c.want {
			t.Fatalf("%s: overThreshold = %v，期望 %v", c.name, got, c.want)
		}
	}
}

// xlsxStylesView styles.xml 解析视图（仅取标红校验所需字段）。
type xlsxStylesView struct {
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

// xlsxSheetView worksheet 解析视图（行/单元格样式索引与单元格值）。
type xlsxSheetView struct {
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

// xlsxEntry 取 xlsx（zip）内单个条目内容。
func xlsxEntry(t *testing.T, data []byte, name string) string {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatalf("打开 xlsx zip: %v", err)
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
	t.Fatalf("xlsx 缺少条目 %s", name)
	return ""
}

// redXfIDs 解析 styles.xml：返回「字体为红色 rgb="FFFF0000"」的 cellXfs 样式索引集合。
func redXfIDs(t *testing.T, stylesXML string) map[string]bool {
	t.Helper()
	var v xlsxStylesView
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

// redByID 解析 sheet XML：隐藏 ID 列（Q 列）值 → 该数据行是否整行红样式（含隐藏 ID 列，全有或全无）。
func redByID(t *testing.T, sheetXML string, reds map[string]bool) map[int64]bool {
	t.Helper()
	var v xlsxSheetView
	if err := xml.Unmarshal([]byte(sheetXML), &v); err != nil {
		t.Fatalf("解析 worksheet XML: %v", err)
	}
	out := map[int64]bool{}
	for _, row := range v.SheetData.Rows {
		if row.R < 3 { // 行 1 标题 / 行 2 表头
			continue
		}
		var id int64
		found := false
		redCount := 0
		for _, c := range row.Cells {
			if strings.HasPrefix(c.R, "Q") && c.V != "" {
				n, err := strconv.ParseInt(c.V, 10, 64)
				if err != nil {
					t.Fatalf("行 %d 隐藏 ID 列值非法 %q", row.R, c.V)
				}
				id, found = n, true
			}
			if reds[c.S] {
				redCount++
			}
		}
		if !found {
			t.Fatalf("行 %d 缺隐藏 ID 列值", row.R)
		}
		if redCount != 0 && redCount != len(row.Cells) {
			t.Fatalf("行 %d 标红应整行一致，实际 %d/%d 单元格红", row.R, redCount, len(row.Cells))
		}
		out[id] = redCount == len(row.Cells)
	}
	return out
}

// TestBuildWorkbookRedRows 单表导出标红：解包 xlsx 核对 styles.xml 红字体与行级样式（含隐藏 ID 列）。
func TestBuildWorkbookRedRows(t *testing.T) {
	th := 3.0
	buf, err := buildWorkbookWithThreshold("检修一班", redSampleRecords(), false, map[string]int{"甲线": 0, "乙线": 1}, &th)
	if err != nil {
		t.Fatalf("生成工作簿失败: %v", err)
	}
	styles := xlsxEntry(t, buf, "xl/styles.xml")
	if !strings.Contains(styles, `rgb="FFFF0000"`) {
		t.Fatal("styles.xml 应含红色字体")
	}
	reds := redXfIDs(t, styles)
	if len(reds) == 0 {
		t.Fatal("应生成红色样式")
	}
	byID := redByID(t, xlsxEntry(t, buf, "xl/worksheets/sheet1.xml"), reds)
	want := map[int64]bool{11: true, 12: true, 13: false, 14: false}
	for id, w := range want {
		if byID[id] != w {
			t.Fatalf("记录 %d 标红 = %v，期望 %v（实际全部 %v）", id, byID[id], w, byID)
		}
	}
	// 表结构与隐藏 ID 列兼容不变
	wb, err := exceldoc.ReadWorkbook(buf)
	if err != nil {
		t.Fatalf("回读工作簿失败: %v", err)
	}
	sheet := wb.Sheets[0]
	if !sheet.IDColHidden {
		t.Fatal("记录 ID 列应隐藏")
	}
	if len(sheet.Headers) != 17 {
		t.Fatalf("表头列数应 17（含隐藏 ID 列），实际 %d", len(sheet.Headers))
	}
	if sheet.Rows[0][16] == "" || sheet.Rows[3][16] == "" {
		t.Fatalf("隐藏 ID 列应逐行写入，实际 %v %v", sheet.Rows[0][16], sheet.Rows[3][16])
	}
	gotIDs := map[string]bool{}
	for _, r := range sheet.Rows {
		gotIDs[r[16]] = true
	}
	for _, id := range []string{"11", "12", "13", "14"} {
		if !gotIDs[id] {
			t.Fatalf("隐藏 ID 列缺记录 %s，实际 %v", id, gotIDs)
		}
	}
}

// TestBuildWorkbookRedRowsNoThreshold 未配置阈值（或旧 buildWorkbook 入口）：整簿不标红、不出红字体样式。
func TestBuildWorkbookRedRowsNoThreshold(t *testing.T) {
	builders := map[string]func() ([]byte, error){
		"带阈值入参但阈值 nil": func() ([]byte, error) {
			return buildWorkbookWithThreshold("检修一班", redSampleRecords(), false, map[string]int{"甲线": 0, "乙线": 1}, nil)
		},
		"旧入口 buildWorkbook": func() ([]byte, error) {
			return buildWorkbook("检修一班", redSampleRecords(), false, map[string]int{"甲线": 0, "乙线": 1})
		},
	}
	for name, build := range builders {
		buf, err := build()
		if err != nil {
			t.Fatalf("%s: 生成工作簿失败: %v", name, err)
		}
		styles := xlsxEntry(t, buf, "xl/styles.xml")
		if strings.Contains(styles, `rgb="FFFF0000"`) {
			t.Fatalf("%s: 未配置阈值不应生成红字体", name)
		}
		byID := redByID(t, xlsxEntry(t, buf, "xl/worksheets/sheet1.xml"), redXfIDs(t, styles))
		for id, red := range byID {
			if red {
				t.Fatalf("%s: 未配置阈值记录 %d 不应标红", name, id)
			}
		}
	}
}

// TestBuildWorkbookRedRowsSplit 分表导出标红：各 sheet 独立判定，未匹配组同样生效。
func TestBuildWorkbookRedRowsSplit(t *testing.T) {
	th := 3.0
	buf, err := buildWorkbookWithThreshold("检修一班", redSampleRecords(), true, map[string]int{"甲线": 0, "乙线": 1}, &th)
	if err != nil {
		t.Fatalf("生成工作簿失败: %v", err)
	}
	reds := redXfIDs(t, xlsxEntry(t, buf, "xl/styles.xml"))
	if len(reds) == 0 {
		t.Fatal("应生成红色样式")
	}
	// sheet 顺序：甲线（11/12）→ 乙线（13）→ 未匹配线路（14）
	wants := []map[int64]bool{
		{11: true, 12: true},
		{13: false},
		{14: false},
	}
	for i, want := range wants {
		byID := redByID(t, xlsxEntry(t, buf, "xl/worksheets/sheet"+strconv.Itoa(i+1)+".xml"), reds)
		for id, w := range want {
			if byID[id] != w {
				t.Fatalf("sheet%d 记录 %d 标红 = %v，期望 %v（实际 %v）", i+1, id, byID[id], w, byID)
			}
		}
	}
}

// fetchRecordRow Fetch 测试用记录行（29 列，vertical/vertical_num 可配）。
func fetchRecordRow(id int64, line, vertical string, verticalNum any) []driver.Value {
	now := driver.Value(time.Date(2026, 10, 1, 8, 0, 0, 0, time.UTC))
	return []driver.Value{
		id, int64(1), "10", line, "3#-4#", nil, nil, "", "杨树", "3", "5", "5", "20", "2", vertical, verticalNum,
		"张三", "2026-10-01", "", "", nil, "unhandled", "crane", nil, nil, nil, nil, now, now,
	}
}

// TestTeamExportFetchRedRows ONLYOFFICE 回源（teamExport.Fetch）标红与下载同口径：
// 阈值 3 时垂距 2.5 整行标红，垂距 4 / 空值不标红；未配置阈值整簿不标红。
func TestTeamExportFetchRedRows(t *testing.T) {
	cols := []string{
		"id", "team_id", "voltage_level", "line_name", "section", "pole_from", "pole_to", "position_note",
		"tree_species", "tree_count", "dist_small", "dist_big", "diameter", "horizontal", "vertical",
		"vertical_num", "finder_name", "found_at", "handler_name", "handle_note", "handled_at",
		"status", "source", "crane_session_id", "crane_record_id", "synced_at", "created_by",
		"created_at", "updated_at",
	}
	rows := [][]driver.Value{
		fetchRecordRow(11, "甲线", "2.5", float64(2.5)),
		fetchRecordRow(13, "乙线", "4", float64(4)),
		fetchRecordRow(14, "丙线", "", nil),
	}
	newEnv := func(threshold [][]driver.Value) (*Module, *fakeDB) {
		db, fake := openFake(t)
		rule := teamRule()
		fake.on(rule.match, rule.cols, rule.rows)
		fake.on("FROM treehazard_record WHERE team_id = ?", cols, rows)
		if threshold != nil {
			fake.on("SELECT vertical_threshold FROM treehazard_team_setting",
				[]string{"vertical_threshold"}, threshold)
		}
		m := New(db)
		m.Cfg = memConfig{}
		return m, fake
	}

	m, _ := newEnv([][]driver.Value{{float64(3)}})
	buf, err := (&teamExport{m: m}).Fetch(context.Background(), office.LineRef{Team: 1})
	if err != nil {
		t.Fatalf("Fetch 失败: %v", err)
	}
	reds := redXfIDs(t, xlsxEntry(t, buf, "xl/styles.xml"))
	if len(reds) == 0 {
		t.Fatal("应生成红色样式")
	}
	byID := redByID(t, xlsxEntry(t, buf, "xl/worksheets/sheet1.xml"), reds)
	want := map[int64]bool{11: true, 13: false, 14: false}
	for id, w := range want {
		if byID[id] != w {
			t.Fatalf("记录 %d 标红 = %v，期望 %v（实际 %v）", id, byID[id], w, byID)
		}
	}

	// 未配置阈值（settings 无行）→ 整簿不标红、不出红字体
	m2, fake2 := newEnv(nil)
	buf2, err := (&teamExport{m: m2}).Fetch(context.Background(), office.LineRef{Team: 1})
	if err != nil {
		t.Fatalf("Fetch 失败: %v", err)
	}
	if !strings.Contains(strings.Join(fake2.allQueries(), "\n"), "vertical_threshold") {
		t.Fatal("Fetch 应读取班组阈值配置")
	}
	styles2 := xlsxEntry(t, buf2, "xl/styles.xml")
	if strings.Contains(styles2, `rgb="FFFF0000"`) {
		t.Fatal("未配置阈值不应生成红字体")
	}
	byID2 := redByID(t, xlsxEntry(t, buf2, "xl/worksheets/sheet1.xml"), redXfIDs(t, styles2))
	for id, red := range byID2 {
		if red {
			t.Fatalf("未配置阈值记录 %d 不应标红", id)
		}
	}
}
