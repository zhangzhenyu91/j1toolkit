// Package xlsx 无第三方依赖的 .xlsx 读写（移植自 toolkit/xlsx_util.py；
// archive/zip + 手写 XML / encoding/xml 解析）。
//
// 写侧 WriteDispatchXlsx 逐字移植自 export_dispatch_orders.py 的 json_to_excel：
// 列结构与顺序、runState 中文映射、时间格式、样式索引全部保持一致——
// 该文件格式是壹匣服务端「派车单每日自动同步」的解析契约，禁止改动。
// 读侧用于「按派车单号导出」应用解析用户提供的单号清单 xlsx。
package xlsx

import (
	"archive/zip"
	"fmt"
	"io"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Column 派车单列契约的一列（字段名, 表头）。
type Column struct {
	Field   string
	Caption string
}

// DispatchColumns 每日派车单同步的列契约（字段, 表头），顺序固定。
var DispatchColumns = []Column{
	{"vehicleNumber", "车牌号码"},
	{"licensePlateColor", "车牌颜色"},
	{"driverName", "驾驶员"},
	{"driverTel", "驾驶员电话"},
	{"userName", "用车人"},
	{"planSendTime", "预计用车时间"},
	{"planBackTime", "预计返回时间"},
	{"planUseTime", "预计用车时间(小时)"},
	{"frmAddr", "出发地"},
	{"toAddr", "目的地"},
	{"departName", "用车部门"},
	{"dispatchName", "派车人"},
	{"runCode", "派车单号"},
	{"runState", "派车单状态"},
	{"vehicleType", "车辆类型"},
	{"powerType", "能源类型"},
	{"userTel", "用车人电话"},
	{"useReason", "用车事由"},
	{"remark", "备注"},
	{"dispatchTime", "创建时间"},
}

// RunStateMap 派车单状态中文映射（契约，禁止改动）。
var RunStateMap = map[string]string{
	"0": "待派车",
	"1": "已派车",
	"2": "已完成",
	"3": "已取消",
}

// dateFields 需要时间格式化的字段（epoch 毫秒 → 本地时间字符串）。
var dateFields = map[string]bool{
	"planSendTime": true,
	"planBackTime": true,
	"dispatchTime": true,
}

// ---------------------------------------------------------------- 工具

var ctrlRe = regexp.MustCompile("[\x00-\x08\x0b\x0c\x0e-\x1f\x7f]")

// escapeXML 转义 XML 特殊字符，并过滤 XML 1.0 非法控制字符。
func escapeXML(s string) string {
	s = ctrlRe.ReplaceAllString(s, "")
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	s = strings.ReplaceAll(s, "\"", "&quot;")
	s = strings.ReplaceAll(s, "'", "&apos;")
	return s
}

// intToColLetter 列号（1 起）→ Excel 列字母。
func intToColLetter(col int) string {
	result := ""
	for col > 0 {
		rem := (col - 1) % 26
		col = (col - 1) / 26
		result = string(rune('A'+rem)) + result
	}
	return result
}

// excelRef 单元格引用（如 B3）。
func excelRef(row, col int) string {
	return fmt.Sprintf("%s%d", intToColLetter(col), row)
}

// numStr 数值单元格文本（Python "%s" % value 的口径：整数不带小数点，
// 整数值浮点保留 ".0"）。
func numStr(v any) string {
	switch t := v.(type) {
	case int:
		return strconv.Itoa(t)
	case int32:
		return strconv.FormatInt(int64(t), 10)
	case int64:
		return strconv.FormatInt(t, 10)
	case float32:
		return numStr(float64(t))
	case float64:
		if t == float64(int64(t)) {
			return strconv.FormatFloat(t, 'f', 1, 64)
		}
		return strconv.FormatFloat(t, 'f', -1, 64)
	default:
		return fmt.Sprintf("%v", v)
	}
}

// isNumeric 判断是否数值单元格（bool 不算）。
func isNumeric(v any) bool {
	switch v.(type) {
	case int, int32, int64, float32, float64:
		return true
	}
	return false
}

// formatCellValue 写侧单元格值规整（日期字段格式化 / runState 映射）。
func formatCellValue(field string, value any) any {
	if value == nil {
		return ""
	}
	if dateFields[field] && value != "" {
		switch t := value.(type) {
		case int, int32, int64, float32, float64:
			ms := toFloat(t)
			// epoch 毫秒用本地时区格式化（与「当日」过滤口径一致）
			tt := time.UnixMilli(int64(ms)).In(time.Local)
			return tt.Format("2006-01-02 15:04:05")
		case string:
			s := t
			if len(s) > 19 {
				s = s[:19]
			}
			if tt, err := time.ParseInLocation("2006-01-02 15:04:05", s, time.Local); err == nil {
				return tt.Format("2006-01-02 15:04:05")
			}
			return fmt.Sprintf("%v", value)
		default:
			return fmt.Sprintf("%v", value)
		}
	}
	if field == "runState" {
		s := fmt.Sprintf("%v", value)
		if mapped, ok := RunStateMap[s]; ok {
			return mapped
		}
	}
	return value
}

func toFloat(v any) float64 {
	switch t := v.(type) {
	case int:
		return float64(t)
	case int32:
		return float64(t)
	case int64:
		return float64(t)
	case float32:
		return float64(t)
	case float64:
		return t
	}
	return 0
}

// ---------------------------------------------------------------- 写侧（移植，勿改格式）

// WriteDispatchXlsx 生成每日派车单同步 xlsx（格式与旧脚本 json_to_excel 完全一致），返回记录数。
// items 每条为「字段名 → 值」（值可为 string / 数值 / nil）。
func WriteDispatchXlsx(items []map[string]any, outputPath string, log func(string)) (int, error) {
	columns := DispatchColumns
	numCols := len(columns)
	numRows := len(items)

	contentTypes := `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types">
<Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/>
<Default Extension="xml" ContentType="application/xml"/>
<Override PartName="/xl/workbook.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.sheet.main+xml"/>
<Override PartName="/xl/worksheets/sheet1.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.worksheet+xml"/>
<Override PartName="/xl/sharedStrings.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.sharedStrings+xml"/>
<Override PartName="/xl/styles.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.styles+xml"/>
</Types>`

	rels := `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">
<Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="xl/workbook.xml"/>
</Relationships>`

	workbook := `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<workbook xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships">
<bookPr filterSpecialContents="1"/>
<sheets>
<sheet name="派车单" sheetId="1" r:id="rId1"/>
</sheets>
</workbook>`

	workbookRels := `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">
<Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="worksheets/sheet1.xml"/>
<Relationship Id="rId2" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/styles" Target="styles.xml"/>
<Relationship Id="rId3" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/sharedStrings" Target="sharedStrings.xml"/>
</Relationships>`

	allStrings := []string{}
	stringIndex := map[string]int{}
	getSSTIdx := func(s string) int {
		if s == "" {
			return -1 // empty, use <v/> directly
		}
		if idx, ok := stringIndex[s]; ok {
			return idx
		}
		idx := len(allStrings)
		stringIndex[s] = idx
		allStrings = append(allStrings, s)
		return idx
	}

	styles := `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<styleSheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main">
<numFmts count="1">
<numFmt numFmtId="164" formatCode="yyyy-mm-dd h:mm:ss"/>
</numFmts>
<fonts count="2">
<font><name val="微软雅黑"/><family val="2"/><size val="11"/></font>
<font b="1"><name val="微软雅黑"/><family val="2"/><size val="11"/><color rgb="FFFFFFFF"/></font>
</fonts>
<fills count="2">
<fill><patternFill patternType="none"/></fill>
<fill><patternFill patternType="solid"><fgColor rgb="FF4472C4"/></patternFill></fill>
</fills>
<borders count="1">
<border>
<left/><right/><top/><bottom/>
</border>
</borders>
<cellStyleXfs count="1">
<xf/>
</cellStyleXfs>
<cellXfs count="3">
<xf applyAlignment="1" applyBorder="1" applyFill="1" applyFont="1" borderId="0" fillId="1"
    fontId="1" numFmtId="0" xfId="0">
<alignment horizontal="center" textRotation="0" vertical="center" wrapText="1"/>
</xf>
<xf applyAlignment="1" applyBorder="1" applyFill="1" applyFont="1" borderId="0" fillId="0"
    fontId="0" numFmtId="0" xfId="0">
<alignment textRotation="0" vertical="center" wrapText="1"/>
</xf>
<xf applyAlignment="1" applyBorder="1" applyFill="1" applyFont="1" borderId="0" fillId="0"
    fontId="0" numFmtId="164" xfId="0">
<alignment textRotation="0" vertical="center" wrapText="1"/>
</xf>
</cellXfs>
</styleSheet>`

	sheetRows := []string{}
	// 表头用 inlineStr（避免 openpyxl 解析问题），不加样式引用
	headerCells := []string{}
	for colIdx, col := range columns {
		ref := excelRef(1, colIdx+1)
		headerCells = append(headerCells,
			fmt.Sprintf(`<c r="%s" s="0" t="inlineStr"><is><t>%s</t></is></c>`, ref, escapeXML(col.Caption)))
	}
	sheetRows = append(sheetRows,
		fmt.Sprintf(`<row r="1" spans="1:%d">%s</row>`, numCols, strings.Join(headerCells, "")))

	for rowIdx, item := range items {
		r := rowIdx + 2
		cells := []string{}
		for colIdx, col := range columns {
			value := formatCellValue(col.Field, item[col.Field])
			ref := excelRef(r, colIdx+1)
			switch {
			case value == nil || value == "":
				cells = append(cells, fmt.Sprintf(`<c r="%s"/>`, ref))
			case isNumeric(value) && !dateFields[col.Field]:
				cells = append(cells, fmt.Sprintf(`<c r="%s"><v>%s</v></c>`, ref, numStr(value)))
			default:
				valueStr := fmt.Sprintf("%v", value)
				siIdx := getSSTIdx(valueStr)
				if siIdx >= 0 {
					cells = append(cells,
						fmt.Sprintf(`<c r="%s" s="1" t="s"><v>%d</v></c>`, ref, siIdx))
				} else {
					cells = append(cells,
						fmt.Sprintf(`<c r="%s" s="1"><v>%s</v></c>`, ref, escapeXML(valueStr)))
				}
			}
		}
		sheetRows = append(sheetRows,
			fmt.Sprintf(`<row r="%d" spans="1:%d">%s</row>`, r, numCols, strings.Join(cells, "")))
	}

	sheetData := strings.Join(sheetRows, "")

	siElements := []string{}
	for _, s := range allStrings {
		siElements = append(siElements, "<si><t>"+escapeXML(s)+"</t></si>")
	}
	sstContent := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<sst xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" uniqueCount="%d" count="%d">
%s
</sst>`, len(allStrings), len(allStrings), strings.Join(siElements, "\n"))

	colsXML := []string{}
	for c := 1; c <= numCols; c++ {
		colsXML = append(colsXML,
			fmt.Sprintf(`<col collapsed="0" hidden="0" max="%d" min="%d" style="0" width="15"/>`, c, c))
	}
	sheet1 := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships">
<dimension ref="A1:%s%d"/>
<sheetViews>
<sheetView workbookViewId="0" tabSelected="1">
<selection activeCell="A2" sqref="A2"/>
</sheetView>
</sheetViews>
<sheetFormatPr baseColWidth="10" defaultColWidth="15" defaultRowHeight="13" x14ac:dyDescent="0.15" xmlns:x14ac="http://schemas.microsoft.com/office/spreadsheetml/2009/9/main"/>
<cols>
%s
</cols>
<sheetData>
%s
</sheetData>
<pageMargins left="0.7" right="0.7" top="1.08" footer="0.78" header="0.78"/>
<pageSetUpPr fitToPage="1"/>
</worksheet>`, intToColLetter(numCols), numRows+1, strings.Join(colsXML, ""), sheetData)

	files := []struct{ name, body string }{
		{"[Content_Types].xml", contentTypes},
		{"_rels/.rels", rels},
		{"xl/workbook.xml", workbook},
		{"xl/_rels/workbook.xml.rels", workbookRels},
		{"xl/styles.xml", styles},
		{"xl/sharedStrings.xml", sstContent},
		{"xl/worksheets/sheet1.xml", sheet1},
	}
	if err := writeZip(outputPath, files); err != nil {
		return 0, err
	}
	if log != nil {
		log(fmt.Sprintf("  Excel 文件已保存: %s (%d 条记录)", outputPath, numRows))
	}
	return numRows, nil
}

// WriteTableXlsx 通用单工作表 xlsx 写出（不绑定派车单列契约，供各类统计报表用）。
// headers 为表头字符串列表；rows 为与表头等长的值列表——nil/"" 出空单元格，
// 数值出数值单元格，其余按文本（inlineStr）写出；表头加粗居中（s=0）、数据 s=1。
// 列宽按表头与内容的显示宽度（CJK 计 2）取 10~30 的缺省值。返回记录数。
func WriteTableXlsx(path, sheetName string, headers []string, rows [][]any, log func(string)) (int, error) {
	numCols := len(headers)
	numRows := len(rows)

	contentTypes := `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types">
<Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/>
<Default Extension="xml" ContentType="application/xml"/>
<Override PartName="/xl/workbook.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.sheet.main+xml"/>
<Override PartName="/xl/worksheets/sheet1.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.worksheet+xml"/>
<Override PartName="/xl/styles.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.styles+xml"/>
</Types>`

	rels := `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">
<Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="xl/workbook.xml"/>
</Relationships>`

	workbook := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<workbook xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships">
<sheets>
<sheet name="%s" sheetId="1" r:id="rId1"/>
</sheets>
</workbook>`, escapeXML(sheetName))

	workbookRels := `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">
<Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="worksheets/sheet1.xml"/>
<Relationship Id="rId2" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/styles" Target="styles.xml"/>
</Relationships>`

	styles := `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<styleSheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main">
<fonts count="2">
<font><name val="微软雅黑"/><family val="2"/><size val="11"/></font>
<font b="1"><name val="微软雅黑"/><family val="2"/><size val="11"/><color rgb="FFFFFFFF"/></font>
</fonts>
<fills count="2">
<fill><patternFill patternType="none"/></fill>
<fill><patternFill patternType="solid"><fgColor rgb="FF4472C4"/></patternFill></fill>
</fills>
<borders count="1">
<border>
<left/><right/><top/><bottom/>
</border>
</borders>
<cellStyleXfs count="1">
<xf/>
</cellStyleXfs>
<cellXfs count="2">
<xf applyAlignment="1" applyFill="1" applyFont="1" borderId="0" fillId="1"
    fontId="1" numFmtId="0" xfId="0">
<alignment horizontal="center" vertical="center" wrapText="1"/>
</xf>
<xf applyAlignment="1" borderId="0" fillId="0" fontId="0" numFmtId="0" xfId="0">
<alignment vertical="center" wrapText="1"/>
</xf>
</cellXfs>
</styleSheet>`

	dispLen := func(v any) int {
		s := ""
		if v != nil {
			s = fmt.Sprintf("%v", v)
		}
		n := 0
		for _, ch := range s {
			if ch > 0x7F {
				n += 2
			} else {
				n++
			}
		}
		return n
	}

	widths := []int{}
	for c := 0; c < numCols; c++ {
		w := dispLen(headers[c])
		for _, row := range rows {
			if c < len(row) {
				if d := dispLen(row[c]); d > w {
					w = d
				}
			}
		}
		w += 2
		if w > 30 {
			w = 30
		}
		if w < 10 {
			w = 10
		}
		widths = append(widths, w)
	}

	sheetRows := []string{}
	headerCells := []string{}
	for colIdx, caption := range headers {
		ref := excelRef(1, colIdx+1)
		headerCells = append(headerCells,
			fmt.Sprintf(`<c r="%s" s="0" t="inlineStr"><is><t>%s</t></is></c>`, ref, escapeXML(caption)))
	}
	sheetRows = append(sheetRows,
		fmt.Sprintf(`<row r="1" spans="1:%d">%s</row>`, numCols, strings.Join(headerCells, "")))

	for rowIdx, row := range rows {
		r := rowIdx + 2
		cells := []string{}
		for colIdx := 0; colIdx < numCols; colIdx++ {
			var value any
			if colIdx < len(row) {
				value = row[colIdx]
			}
			ref := excelRef(r, colIdx+1)
			switch {
			case value == nil || value == "":
				cells = append(cells, fmt.Sprintf(`<c r="%s"/>`, ref))
			case isNumeric(value):
				cells = append(cells,
					fmt.Sprintf(`<c r="%s" s="1"><v>%s</v></c>`, ref, numStr(value)))
			default:
				cells = append(cells,
					fmt.Sprintf(`<c r="%s" s="1" t="inlineStr"><is><t>%s</t></is></c>`,
						ref, escapeXML(fmt.Sprintf("%v", value))))
			}
		}
		sheetRows = append(sheetRows,
			fmt.Sprintf(`<row r="%d" spans="1:%d">%s</row>`, r, numCols, strings.Join(cells, "")))
	}

	colsXML := []string{}
	for c := 1; c <= numCols; c++ {
		colsXML = append(colsXML,
			fmt.Sprintf(`<col collapsed="0" hidden="0" max="%d" min="%d" style="0" width="%d"/>`, c, c, widths[c-1]))
	}
	sheet1 := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships">
<dimension ref="A1:%s%d"/>
<sheetViews>
<sheetView workbookViewId="0" tabSelected="1">
<selection activeCell="A2" sqref="A2"/>
</sheetView>
</sheetViews>
<sheetFormatPr baseColWidth="10" defaultColWidth="15" defaultRowHeight="13" x14ac:dyDescent="0.15" xmlns:x14ac="http://schemas.microsoft.com/office/spreadsheetml/2009/9/main"/>
<cols>
%s
</cols>
<sheetData>
%s
</sheetData>
<pageMargins left="0.7" right="0.7" top="1.08" footer="0.78" header="0.78"/>
</worksheet>`, intToColLetter(numCols), numRows+1, strings.Join(colsXML, ""), strings.Join(sheetRows, ""))

	files := []struct{ name, body string }{
		{"[Content_Types].xml", contentTypes},
		{"_rels/.rels", rels},
		{"xl/workbook.xml", workbook},
		{"xl/_rels/workbook.xml.rels", workbookRels},
		{"xl/styles.xml", styles},
		{"xl/worksheets/sheet1.xml", sheet1},
	}
	if err := writeZip(path, files); err != nil {
		return 0, err
	}
	if log != nil {
		log(fmt.Sprintf("  Excel 文件已保存: %s (%d 条记录)", path, numRows))
	}
	return numRows, nil
}

// writeZip 打包 zip（ZIP_DEFLATED）。
func writeZip(path string, files []struct{ name, body string }) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	zw := zip.NewWriter(f)
	for _, file := range files {
		w, err := zw.Create(file.name)
		if err != nil {
			return err
		}
		if _, err := io.WriteString(w, file.body); err != nil {
			return err
		}
	}
	return zw.Close()
}
