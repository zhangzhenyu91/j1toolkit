package exporter

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/xuri/excelize/v2"
)

// csvBOM _清单.csv 与轨迹点 CSV 的 utf-8-sig 前缀（Excel 直接打开不乱码）。
const csvBOM = "\ufeff"

// csvCell CSV 单元格清洗（backend._write_reports 口径：逗号转全角、换行转空格）。
func csvCell(v any) string {
	s := strAny(v)
	s = strings.ReplaceAll(s, ",", "，")
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.ReplaceAll(s, "\r", " ")
	return s
}

// writeManifestCSV 写 _清单.csv（utf-8-sig，backend._write_reports 同口径：按 seq 升序）。
func writeManifestCSV(outDir string, rows []row) error {
	sorted := append([]row(nil), rows...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].Seq < sorted[j].Seq })
	var b strings.Builder
	b.WriteString(csvBOM)
	b.WriteString("序号,派车单号,车牌,用车日期,派车单PDF,派车单状态,轨迹PDF,轨迹状态,备注\n")
	for _, r := range sorted {
		cells := []any{r.Seq, r.Code, r.Vehicle, r.Date, r.PcdFile, r.PcdStatus, r.TrackFile, r.TrackStatus, r.Note}
		parts := make([]string, 0, len(cells))
		for _, c := range cells {
			parts = append(parts, csvCell(c))
		}
		b.WriteString(strings.Join(parts, ","))
		b.WriteString("\n")
	}
	return os.WriteFile(filepath.Join(outDir, "_清单.csv"), []byte(b.String()), 0o644)
}

// writeNotFound 写 _未找到.txt（仅未找到时出）。
func writeNotFound(outDir string, notFound []CodeItem, log func(string, ...any)) error {
	if len(notFound) == 0 {
		return nil
	}
	codes := make([]string, 0, len(notFound))
	for _, c := range notFound {
		codes = append(codes, c.Code)
	}
	body := fmt.Sprintf("以下 %d 个派车单号未查询到：\n%s\n", len(notFound), strings.Join(codes, "\n"))
	if err := os.WriteFile(filepath.Join(outDir, "_未找到.txt"), []byte(body), 0o644); err != nil {
		return err
	}
	log("未找到清单已写入 _未找到.txt（%d 个）", len(notFound))
	return nil
}

// noonRow _12点前结束行程.xlsx 行（backend._export_track noon_rows 同口径）。
type noonRow struct {
	Seq     int    // 清单序号
	Code    string // 派车单号
	Vehicle string // 车牌号
	End     string // 行程结束时间串
	CalEnd  string // 校准后结束时间（未校准为空）
	CalDur  string // 校准后行驶时长
	Page    int    // 合并轨迹 PDF 页码（0 = 该单未进合并 PDF，序号列留空）
}

// writeNoonEndReport 行程结束时间在当日 12 点前的轨迹清单（xlsx，表头加粗居中；
// 无记录也出仅表头的表）。任一行带校准列则追加「校准后结束时间/校准后行驶时长」两列。
func writeNoonEndReport(rows []noonRow, outPath string, log func(string, ...any)) error {
	hasCal := false
	for _, r := range rows {
		if r.CalEnd != "" {
			hasCal = true
			break
		}
	}
	headers := []string{"序号（合并轨迹PDF页码）", "派车单号", "车牌号", "日期", "行程结束时间"}
	if hasCal {
		headers = append(headers, "校准后结束时间", "校准后行驶时长")
	}

	f := excelize.NewFile()
	const sheet = "12点前结束行程"
	f.SetSheetName("Sheet1", sheet)
	style, err := f.NewStyle(&excelize.Style{
		Font:      &excelize.Font{Bold: true},
		Alignment: &excelize.Alignment{Horizontal: "center", Vertical: "center"},
	})
	if err != nil {
		return err
	}
	for i, h := range headers {
		cell, _ := excelize.CoordinatesToCellName(i+1, 1)
		if err := f.SetCellValue(sheet, cell, h); err != nil {
			return err
		}
	}
	lastCol, _ := excelize.CoordinatesToCellName(len(headers), 1)
	_ = f.SetCellStyle(sheet, "A1", lastCol, style)

	for ri, r := range rows {
		page := any("")
		if r.Page > 0 {
			page = r.Page
		}
		vals := []any{page, r.Code, r.Vehicle, cutLen(r.End, 10), r.End}
		if hasCal {
			vals = append(vals, r.CalEnd, r.CalDur)
		}
		for ci, v := range vals {
			cell, _ := excelize.CoordinatesToCellName(ci+1, ri+2)
			if err := f.SetCellValue(sheet, cell, v); err != nil {
				return err
			}
		}
	}
	widths := []float64{22, 22, 14, 12, 20, 20, 18}
	for i := range headers {
		col, _ := excelize.ColumnNumberToName(i + 1)
		_ = f.SetColWidth(sheet, col, col, widths[i])
	}
	if err := f.SaveAs(outPath); err != nil {
		return err
	}
	log("12 点前结束行程清单：%s（%d 条）", filepath.Base(outPath), len(rows))
	return nil
}

// writeTrackPointsCSV 写 NNN_轨迹点_<code>.csv（utf-8-sig，backend._export_track 口径：
// 时间列优先 locatetime/time/gpstime，epoch 毫秒自动转时间串）。
func writeTrackPointsCSV(path string, points []map[string]any) error {
	var b strings.Builder
	b.WriteString(csvBOM)
	b.WriteString("时间,经度,纬度,速度,方向\n")
	for _, p := range points {
		t := firstNonEmpty(p["locatetime"], p["time"], p["gpstime"])
		if f, ok := t.(float64); ok && f > 1e12 {
			t = fmtEpochMs(t)
		}
		b.WriteString(strings.Join([]string{
			csvTimeCell(t),
			csvTimeCell(p["dx"]),
			csvTimeCell(p["dy"]),
			csvTimeCell(p["speed"]),
			csvTimeCell(p["direction"]),
		}, ","))
		b.WriteString("\n")
	}
	return os.WriteFile(path, []byte(b.String()), 0o644)
}

func csvTimeCell(v any) string {
	return strings.ReplaceAll(strAny(v), "\n", " ")
}

func firstNonEmpty(vals ...any) any {
	for _, v := range vals {
		if v == nil {
			continue
		}
		if s, ok := v.(string); ok && s == "" {
			continue
		}
		return v
	}
	return ""
}
