package exporter

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/xuri/excelize/v2"
)

// checkNoonSheet 校验 12 点报表工作表名/表头/数据行数。
func checkNoonSheet(t *testing.T, path string, wantHeaders []string, wantRows int) {
	t.Helper()
	f, err := excelize.OpenFile(path)
	if err != nil {
		t.Fatalf("打开 12 点报表失败: %v", err)
	}
	defer f.Close()
	sheets := f.GetSheetList()
	if len(sheets) != 1 || sheets[0] != "12点前结束行程" {
		t.Fatalf("工作表名不符: %v", sheets)
	}
	for i, h := range wantHeaders {
		cell, _ := excelize.CoordinatesToCellName(i+1, 1)
		got, _ := f.GetCellValue("12点前结束行程", cell)
		if got != h {
			t.Fatalf("表头 %s 不符: %q（want %q）", cell, got, h)
		}
	}
	rows, err := f.GetRows("12点前结束行程")
	if err != nil {
		t.Fatal(err)
	}
	dataRows := len(rows) - 1 // 去表头
	if dataRows < 0 {
		dataRows = 0
	}
	if dataRows != wantRows {
		t.Fatalf("数据行数不符: %d（want %d）rows=%v", dataRows, wantRows, rows)
	}
	// 校验列之后不应有多余表头
	if len(wantHeaders) < 7 {
		cell, _ := excelize.CoordinatesToCellName(len(wantHeaders)+1, 1)
		got, _ := f.GetCellValue("12点前结束行程", cell)
		if got != "" {
			t.Fatalf("表头多出列 %s: %q", cell, got)
		}
	}
}

// TestWriteNoonEndReport 12 点报表：表头加粗居中、页码空值留空、校准列可选、空表仅表头。
func TestWriteNoonEndReport(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "_12点前结束行程.xlsx")
	rows := []noonRow{
		{Seq: 2, Code: "C2", Vehicle: "晋A1", End: "2026-10-28 10:30:00", Page: 1},
		{Seq: 5, Code: "C5", Vehicle: "晋A2", End: "2026-10-28 11:00:00", Page: 0,
			CalEnd: "2026-10-28 12:00:00", CalDur: "4小时30分钟"},
	}
	if err := writeNoonEndReport(rows, path, t.Logf); err != nil {
		t.Fatal(err)
	}
	checkNoonSheet(t, path,
		[]string{"序号（合并轨迹PDF页码）", "派车单号", "车牌号", "日期", "行程结束时间", "校准后结束时间", "校准后行驶时长"}, 2)

	f, _ := excelize.OpenFile(path)
	defer f.Close()
	if v, _ := f.GetCellValue("12点前结束行程", "A2"); v != "1" {
		t.Fatalf("页码列应为 1，实际 %q", v)
	}
	if v, _ := f.GetCellValue("12点前结束行程", "A3"); v != "" {
		t.Fatalf("未进合并 PDF 的行页码应留空，实际 %q", v)
	}
	if v, _ := f.GetCellValue("12点前结束行程", "F3"); v != "2026-10-28 12:00:00" {
		t.Fatalf("校准后结束时间不符: %q", v)
	}

	// 空表仅表头
	empty := filepath.Join(dir, "empty.xlsx")
	if err := writeNoonEndReport(nil, empty, t.Logf); err != nil {
		t.Fatal(err)
	}
	checkNoonSheet(t, empty,
		[]string{"序号（合并轨迹PDF页码）", "派车单号", "车牌号", "日期", "行程结束时间"}, 0)
}

// TestWriteManifestCSV _清单.csv：BOM、表头、逗号转全角、按 seq 升序。
func TestWriteManifestCSV(t *testing.T) {
	dir := t.TempDir()
	rows := []row{
		{Seq: 2, Code: "C2", Vehicle: "晋A", Date: "2026-10-28", PcdFile: "b.pdf", PcdStatus: "成功", TrackStatus: "未导出"},
		{Seq: 1, Code: "C,1", Vehicle: "晋B", Date: "2026-10-27", PcdStatus: "失败", TrackStatus: "未导出", Note: "a，b"},
	}
	if err := writeManifestCSV(dir, rows); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "_清单.csv"))
	if err != nil {
		t.Fatal(err)
	}
	s := string(raw)
	if !strings.HasPrefix(s, csvBOM) {
		t.Fatal("缺 BOM")
	}
	if !strings.Contains(s, "2,C2,晋A,2026-10-28,b.pdf,成功,,未导出,") {
		t.Fatalf("行内容不符: %q", s)
	}
	if !strings.Contains(s, "1,C，1") {
		t.Fatalf("逗号未转全角: %q", s)
	}
	if strings.Index(s, "2,C2") < strings.Index(s, "1,C，1") {
		t.Fatalf("应按 seq 升序: %q", s)
	}
}

// TestWriteTrackPointsCSV 轨迹点 CSV：BOM、epoch 毫秒转时间、字段口径。
func TestWriteTrackPointsCSV(t *testing.T) {
	path := filepath.Join(t.TempDir(), "1_轨迹点_X.csv")
	points := []map[string]any{
		{"locatetime": "2026-10-28 08:00:00", "dx": 112.5, "dy": 37.8, "speed": 30, "direction": 90},
		{"locatetime": float64(1761619200000), "dx": 112.6, "dy": 37.9, "speed": 31, "direction": 91}, // epoch 毫秒
		{"time": "2026-10-28 08:04:00", "dx": 112.7, "dy": 38.0, "speed": 32, "direction": 92},
	}
	if err := writeTrackPointsCSV(path, points); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(path)
	s := string(raw)
	if !strings.HasPrefix(s, csvBOM) {
		t.Fatal("缺 BOM")
	}
	if !strings.Contains(s, "时间,经度,纬度,速度,方向\n") {
		t.Fatalf("表头不符: %q", s)
	}
	lines := strings.Split(strings.TrimPrefix(strings.TrimSuffix(s, "\n"), csvBOM), "\n")
	if len(lines) != 4 {
		t.Fatalf("应 3 行数据: %q", s)
	}
	if lines[1] != "2026-10-28 08:00:00,112.5,37.8,30,90" {
		t.Fatalf("点行不符: %q", lines[1])
	}
	wantEpoch := time.UnixMilli(1761619200000).Format("2006-01-02 15:04:05")
	if !strings.HasPrefix(lines[2], wantEpoch) {
		t.Fatalf("epoch 毫秒未转时间串: %q（want 前缀 %q）", lines[2], wantEpoch)
	}
	if lines[3] != "2026-10-28 08:04:00,112.7,38,32,92" {
		t.Fatalf("time 字段兜底不符: %q", lines[3])
	}
}

// TestMergePDFs 合并：不足 2 个不出；2 个以上按序合成（stub PDF 可被 pdfcpu 解析）。
func TestMergePDFs(t *testing.T) {
	dir := t.TempDir()
	var files []string
	for i, name := range []string{"a.pdf", "b.pdf", "c.pdf"} {
		p := filepath.Join(dir, name)
		if err := writeStubPDF(p, "stub"+string(rune('1'+i))); err != nil {
			t.Fatal(err)
		}
		files = append(files, p)
	}
	out := filepath.Join(dir, "_合并.pdf")
	if mergePDFs(context.Background(), files[:1], out, t.Logf) {
		t.Fatal("不足 2 个不应合并")
	}
	if _, err := os.Stat(out); !os.IsNotExist(err) {
		t.Fatal("不足 2 个不应出合并文件")
	}
	if !mergePDFs(context.Background(), files, out, t.Logf) {
		t.Fatal("3 个 stub 应合并成功")
	}
	if st, err := os.Stat(out); err != nil || st.Size() == 0 {
		t.Fatalf("合并产物异常: %v", err)
	}
}

// TestWriteStubPDF stub PDF 基本合法（头/尾/xref 结构）。
func TestWriteStubPDF(t *testing.T) {
	p := filepath.Join(t.TempDir(), "s.pdf")
	if err := writeStubPDF(p, "[mock] pcd 中文"); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(p)
	s := string(raw)
	if !strings.HasPrefix(s, "%PDF-1.4\n") || !strings.Contains(s, "%%EOF") ||
		!strings.Contains(s, "xref") || !strings.Contains(s, "BT /F1 14 Tf 60 780 Td ([mock] pcd ??)") {
		t.Fatalf("stub PDF 结构异常: %q", s)
	}
	if strings.Contains(s, "中文") {
		t.Fatal("非 ASCII 应替换为 ?")
	}
}
