package treehazard

import (
	"testing"
	"time"

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
