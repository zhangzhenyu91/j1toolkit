package crossing

import (
	"database/sql/driver"
	"strings"
	"testing"
	"time"

	"shade/internal/auth"
	"shade/internal/compat/exceldoc"
)

func TestNormDateStrAndSectionPoles(t *testing.T) {
	cases := map[string]string{
		"2026.03.04": "2026-03-04",
		"2026-3-4":   "2026-03-04",
		"2026/3/4":   "2026-03-04",
		"abc":        "",
		"":           "",
	}
	for in, want := range cases {
		if got := normDateStr(in); got != want {
			t.Fatalf("normDateStr(%q) = %q，应 %q", in, got, want)
		}
	}
	from, to := parseSectionPoles("15#-16#")
	if from == nil || *from != 15 || to == nil || *to != 16 {
		t.Fatalf("15#-16# 应解析 [15,16]，实际 %v %v", from, to)
	}
	from, to = parseSectionPoles("无杆号区段")
	if from != nil || to != nil {
		t.Fatalf("解析不出应 [null,null]，实际 %v %v", from, to)
	}
}

func TestWeekStartTextAt(t *testing.T) {
	// 2026-10-28（周三）北京时间 → 本周一 2026-10-26 00:00 北京 = 2026-10-25 16:00 UTC 墙钟
	got := weekStartTextAt(time.Date(2026, 10, 28, 10, 0, 0, 0, time.UTC))
	if got != "2026-10-25 16:00:00" {
		t.Fatalf("周起点应 2026-10-25 16:00:00（UTC 墙钟），实际 %s", got)
	}
}

func TestSafeFileNameAndVoltageCell(t *testing.T) {
	if got := safeFileName(`甲/线:*?"<>|`); got != "甲_线"+strings.Repeat("_", 7) {
		t.Fatalf("非法字符应替换为 _，实际 %q", got)
	}
	if got := safeFileName("  "); got != "未命名" {
		t.Fatalf("空名应兜底 未命名，实际 %q", got)
	}
	if got := fmtVoltageCell("110"); got != "110kV" {
		t.Fatalf("纯数字应拼 kV，实际 %q", got)
	}
	if got := fmtVoltageCell("110kV"); got != "110kV" {
		t.Fatalf("已含 kV 不应重复追加，实际 %q", got)
	}
	if got := fmtVoltageCell("未知等级"); got != "未知等级" {
		t.Fatalf("非数字应原样，实际 %q", got)
	}
	if got := exportFileName("检修一班", "甲线"); got != "甲线交跨统计表.xlsx" {
		t.Fatalf("分线路导出文件名不符，实际 %q", got)
	}
}

func TestSheetNameOf(t *testing.T) {
	used := map[string]bool{}
	name := sheetNameOf("110kV甲[线]:跨/越", used)
	if strings.ContainsAny(name, "[]:*/\\") {
		t.Fatalf("sheet 名应替换 Excel 禁字符，实际 %q", name)
	}
	name2 := sheetNameOf("110kV甲[线]:跨/越", used)
	if name2 == name {
		t.Fatal("重名应追加序号区分")
	}
	if got := sheetNameOf("", used); got != "未命名线路" {
		t.Fatalf("空名应兜底，实际 %q", got)
	}
}

func TestSortRecordsOrder(t *testing.T) {
	mk := func(id int64, voltage, line, section string, pole *int64) Record {
		return Record{
			ID: id, VoltageLevel: voltage, LineName: line, Section: section,
			PoleFrom: pole, CreatedAt: time.Unix(id, 0).UTC(),
		}
	}
	p := func(n int64) *int64 { return &n }
	records := []Record{
		mk(1, "110", "乙线", "5#-6#", p(5)),
		mk(2, "220", "甲线", "10#-20#", p(10)),
		mk(3, "110", "乙线", "2#-3#", p(2)),
		mk(4, "110", "乙线", "2#-3#", p(1)),
	}
	// 单表导出口径：电压降序 → 线路升序 → 区段升序 → pole_from 升序
	sorted := sortRecords(records, exportGroupComparator)
	wantOrder := []int64{2, 4, 3, 1}
	for i, want := range wantOrder {
		if sorted[i].ID != want {
			t.Fatalf("单表排序第 %d 位应 %d，实际 %d", i, want, sorted[i].ID)
		}
	}
	// 分表口径：team_line 导入序
	lineOrder := map[string]int{"甲线": 0, "乙线": 1}
	sorted2 := sortRecords(records, makeLineGroupComparator(lineOrder))
	if sorted2[0].LineName != "甲线" {
		t.Fatalf("分表排序甲线应排首，实际 %v", sorted2[0].LineName)
	}
	// 组内：区段自然序（2#-3# 在 10#-20# 前）已在单表排序中覆盖；再验证数字自然序
	if compareSection("2-10", "10-20") >= 0 {
		t.Fatal("2-10 应排在 10-20 前（数字自然序）")
	}
}

func TestBuildWorkbookSingleSheet(t *testing.T) {
	p := func(n int64) *int64 { return &n }
	ins := "2026-02-01"
	records := []Record{
		{ID: 11, VoltageLevel: "110", LineName: "甲线", Section: "2#-3#", CrossingType: "10kV线路",
			DistSmall: "5", DistBig: "6", CrossVertical: "7", CrossAngle: "30", FinderName: "张三",
			InspectAt: &ins, Remark: "备注A", PoleFrom: p(2), PoleTo: p(3)},
		{ID: 12, VoltageLevel: "220", LineName: "乙线", Section: "1#-2#", CrossingType: "厂区",
			FinderName: "李四", InspectAt: &ins},
	}
	buf, err := buildWorkbook("检修一班", records, false, nil)
	if err != nil {
		t.Fatalf("生成工作簿失败: %v", err)
	}
	wb, err := exceldoc.ReadWorkbook(buf)
	if err != nil {
		t.Fatalf("回读工作簿失败: %v", err)
	}
	if len(wb.Sheets) != 1 || wb.Sheets[0].Name != "交跨" {
		t.Fatalf("单表应 sheet=交跨，实际 %v", wb.Sheets)
	}
	sh := wb.Sheets[0]
	if sh.Title != "检修一班交跨统计表" {
		t.Fatalf("标题应「检修一班交跨统计表」，实际 %q", sh.Title)
	}
	if len(sh.Rows) != 2 {
		t.Fatalf("应 2 行数据，实际 %d", len(sh.Rows))
	}
	// 电压降序：220 在前
	if sh.Rows[0][2] != "乙线" || sh.Rows[1][2] != "甲线" {
		t.Fatalf("单表应电压降序，实际 %v / %v", sh.Rows[0][2], sh.Rows[1][2])
	}
	// 日期格式化 yyyy.MM.dd
	if sh.Rows[1][12] != "2026.02.01" {
		t.Fatalf("巡视时间应 2026.02.01，实际 %q", sh.Rows[1][12])
	}
	// 隐藏 ID 列
	if !sh.IDColHidden || sh.IDs[1] != "11" {
		t.Fatalf("隐藏记录 ID 列应对齐行序，IDs=%v hidden=%v", sh.IDs, sh.IDColHidden)
	}
}

func TestBuildWorkbookSplitSheets(t *testing.T) {
	records := []Record{
		{ID: 1, VoltageLevel: "110", LineName: "甲线", Section: "1#-2#"},
		{ID: 2, VoltageLevel: "220", LineName: "乙线", Section: "1#-2#"},
		{ID: 3, VoltageLevel: "35", LineName: "丙线", Section: "1#-2#"}, // 未匹配线路
	}
	lineOrder := map[string]int{"甲线": 0, "乙线": 1}
	buf, err := buildWorkbook("检修一班", records, true, lineOrder)
	if err != nil {
		t.Fatalf("生成工作簿失败: %v", err)
	}
	wb, err := exceldoc.ReadWorkbook(buf)
	if err != nil {
		t.Fatalf("回读工作簿失败: %v", err)
	}
	if len(wb.Sheets) != 3 {
		t.Fatalf("应 3 个 sheet（2 线路 + 未匹配），实际 %d", len(wb.Sheets))
	}
	if wb.Sheets[0].Title != "110kV甲线交跨统计表" {
		t.Fatalf("首 sheet 标题不符：%q", wb.Sheets[0].Title)
	}
	last := wb.Sheets[2]
	if last.Title != "未匹配线路" || len(last.Rows) != 1 {
		t.Fatalf("未匹配 sheet 不符：%q rows=%d", last.Title, len(last.Rows))
	}
}

func TestExportEndpointDownloadsXLSX(t *testing.T) {
	db, fdb := openFake(t)
	teamStub(fdb, 5, "检修一班")
	fdb.onRowsAlways("FROM team_line WHERE team_id", []string{"voltage_level", "line_name"},
		[]driver.Value{"110", "甲线"})
	fdb.onRowsAlways("FROM crossing_record WHERE team_id = ?", recordColsOf(),
		recordRow(1, 5, "110", "甲线", "2#-3#", 2, 3))
	m := New(db, fakeCfg{})
	h := newTestRouter(m, auth.Identity{ID: 9, Role: "admin", TeamID: 5})
	rec := doJSON(t, h, "GET", "/crossing/export", "t", "")
	if rec.Code != 200 {
		t.Fatalf("导出应 200，实际 %d %s", rec.Code, rec.Body.String())
	}
	ct := rec.Header().Get("Content-Type")
	if !strings.Contains(ct, "spreadsheetml") {
		t.Fatalf("Content-Type 不符：%q", ct)
	}
	cd := rec.Header().Get("Content-Disposition")
	if !strings.Contains(cd, `filename="crossing.xlsx"`) || !strings.Contains(cd, "filename*=UTF-8''") {
		t.Fatalf("Content-Disposition 不符：%q", cd)
	}
	if !strings.HasPrefix(rec.Body.String(), "PK") {
		t.Fatalf("导出应为 xlsx（PK 头），实际 %q", rec.Body.String()[:4])
	}
}
