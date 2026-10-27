// Package exceldoc 是 Go 全量迁移阶段 0 验证切片：Excel（xlsx）与 Word（docx）兼容处理。
// xlsx 走 Excelize 生成/回读（对齐 Node 侧 exceljs 导出口径：合并标题、样式、隐藏记录 ID 列、
// 行高列宽、多 sheet）；docx 走 archive/zip + 文本级 XML 处理（对齐 docxtemplater/pizzip 口径：
// 拆 run 占位符、原生 XML 段落注入、分页合并、费用表动态列）。
package exceldoc

import (
	"bytes"
	"fmt"

	"github.com/xuri/excelize/v2"
)

// 导出样式口径（对齐 server/src/crossing/export.js writeSheet）：
// 第 1 行合并标题（45 磅 / 20 号加粗居中，不加框线）→ 表头行（35 磅 / 12 号加粗居中 + thin 框线）
// → 数据行（25 磅 / 11 号居中自动换行 + thin 框线，文本格式 numFmt '@'）；
// 隐藏记录 ID 列（内网同步行级比对锚点）宽 10，默认列名「记录ID」。
const (
	titleRowHeight   = 45.0
	headerRowHeight  = 35.0
	dataRowHeight    = 25.0
	titleFontSize    = 20.0
	headerFontSize   = 12.0
	dataFontSize     = 11.0
	idColWidth       = 10.0
	defaultIDHeader  = "记录ID"
	numFmtText       = 49 // 内置格式 "@"：全部文本写入防数字被 Excel 吃掉
	borderStyleThin  = 1  // excelize Border.Style：1 = Continuous 1px（thin）
	horizontalCenter = "center"
	verticalCenter   = "center"
)

// Column 描述一列可见表头（不含隐藏记录 ID 列）。
type Column struct {
	Header string  // 表头文案
	Width  float64 // 列宽（Excel 字符单位）
}

// SheetSpec 描述单个 worksheet 的完整模板内容。
type SheetSpec struct {
	Name    string     // sheet 名（Excel 名约束清洗由调用方负责）
	Title   string     // 第 1 行合并标题；空串表示跳过标题行（skipTitle 口径，表头落在第 1 行）
	Columns []Column   // 可见表头列
	Rows    [][]string // 数据行，按 Columns 顺序的文本值（含序号等，由调用方排好序）
	IDs     []int64    // 隐藏记录 ID 列，与 Rows 按行对齐；0 写空单元格
}

// WorkbookSpec 描述整个工作簿（多 sheet）。
type WorkbookSpec struct {
	Sheets   []SheetSpec
	IDHeader string // 隐藏 ID 列表头文案，空则用「记录ID」
}

// MergedRange 回读用合并单元格区间。
type MergedRange struct {
	Start string
	End   string
}

// SheetData 为回读得到的单 sheet 结构。
type SheetData struct {
	Name        string
	Title       string // 合并标题文本（无标题行时为空）
	TitleMerges []MergedRange
	HeaderRow   int                // 表头所在行号（有标题行=2，无=1）
	Headers     []string           // 表头行（含隐藏 ID 列）
	Rows        [][]string         // 数据行（含隐藏 ID 列，取 GetRows 稀疏矩阵）
	IDs         []string           // 隐藏 ID 列数据行文本
	RowHeights  map[int]float64    // 已设置的行高（1 起）
	ColWidths   map[string]float64 // 已设置的列宽（列字母）
	IDColHidden bool               // 隐藏 ID 列是否隐藏
}

// WorkbookData 为回读得到的整个工作簿。
type WorkbookData struct {
	Sheets []SheetData
}

// idHeader 取隐藏 ID 列表头文案。
func (s WorkbookSpec) idHeader() string {
	if s.IDHeader != "" {
		return s.IDHeader
	}
	return defaultIDHeader
}

// thinBorders 生成四边 thin 框线。
func thinBorders() []excelize.Border {
	return []excelize.Border{
		{Type: "left", Color: "000000", Style: borderStyleThin},
		{Type: "right", Color: "000000", Style: borderStyleThin},
		{Type: "top", Color: "000000", Style: borderStyleThin},
		{Type: "bottom", Color: "000000", Style: borderStyleThin},
	}
}

// WriteWorkbook 按 spec 生成 xlsx 字节（内存内完成，不做任何落盘/联网）。
func WriteWorkbook(spec WorkbookSpec) ([]byte, error) {
	if len(spec.Sheets) == 0 {
		return nil, fmt.Errorf("exceldoc: 工作簿至少需要一个 sheet")
	}
	f := excelize.NewFile()
	defer f.Close()

	// 第一个 sheet 复用默认「Sheet1」改名，其余 NewSheet 追加
	if err := f.SetSheetName("Sheet1", spec.Sheets[0].Name); err != nil {
		return nil, fmt.Errorf("exceldoc: 设置 sheet 名 %q: %w", spec.Sheets[0].Name, err)
	}
	for _, s := range spec.Sheets[1:] {
		if _, err := f.NewSheet(s.Name); err != nil {
			return nil, fmt.Errorf("exceldoc: 新建 sheet %q 失败: %w", s.Name, err)
		}
	}

	for _, s := range spec.Sheets {
		if err := writeSheet(f, spec.idHeader(), s); err != nil {
			return nil, err
		}
	}

	buf, err := f.WriteToBuffer()
	if err != nil {
		return nil, fmt.Errorf("exceldoc: 写出 xlsx: %w", err)
	}
	return buf.Bytes(), nil
}

// writeSheet 写入单个 worksheet 的完整模板（合并标题 / 表头 / 数据区 / 隐藏 ID 列）。
func writeSheet(f *excelize.File, idHeader string, s SheetSpec) error {
	if len(s.Columns) == 0 {
		return fmt.Errorf("exceldoc: sheet %q 至少需要一列可见表头", s.Name)
	}
	if len(s.IDs) != 0 && len(s.IDs) != len(s.Rows) {
		return fmt.Errorf("exceldoc: sheet %q 记录 ID 数 %d 与数据行数 %d 不一致", s.Name, len(s.IDs), len(s.Rows))
	}
	visibleCols := len(s.Columns)
	idColIdx := visibleCols + 1 // 隐藏 ID 列列号
	lastVisible, err := excelize.CoordinatesToCellName(visibleCols, 1)
	if err != nil {
		return err
	}
	idColName, err := excelize.ColumnNumberToName(idColIdx)
	if err != nil {
		return err
	}

	// 列宽（含隐藏 ID 列）：SetColWidth 接受列字母，不接受 A1 单元格地址
	for i, c := range s.Columns {
		colName, err := excelize.ColumnNumberToName(i + 1)
		if err != nil {
			return err
		}
		if err := f.SetColWidth(s.Name, colName, colName, c.Width); err != nil {
			return fmt.Errorf("exceldoc: 设置列宽 %s: %w", colName, err)
		}
	}
	if err := f.SetColWidth(s.Name, idColName, idColName, idColWidth); err != nil {
		return err
	}
	if err := f.SetColVisible(s.Name, idColName, false); err != nil {
		return fmt.Errorf("exceldoc: 隐藏 ID 列: %w", err)
	}

	// 样式
	titleStyle, err := f.NewStyle(&excelize.Style{
		Font:      &excelize.Font{Bold: true, Size: titleFontSize},
		Alignment: &excelize.Alignment{Horizontal: horizontalCenter, Vertical: verticalCenter},
	})
	if err != nil {
		return err
	}
	headerStyle, err := f.NewStyle(&excelize.Style{
		Font:      &excelize.Font{Bold: true, Size: headerFontSize},
		Alignment: &excelize.Alignment{Horizontal: horizontalCenter, Vertical: verticalCenter},
		Border:    thinBorders(),
	})
	if err != nil {
		return err
	}
	dataStyle, err := f.NewStyle(&excelize.Style{
		Font:      &excelize.Font{Size: dataFontSize},
		Alignment: &excelize.Alignment{Horizontal: horizontalCenter, Vertical: verticalCenter, WrapText: true},
		Border:    thinBorders(),
		NumFmt:    numFmtText,
	})
	if err != nil {
		return err
	}
	idStyle, err := f.NewStyle(&excelize.Style{
		Font:      &excelize.Font{Size: dataFontSize},
		Alignment: &excelize.Alignment{Horizontal: horizontalCenter, Vertical: verticalCenter, WrapText: true},
		Border:    thinBorders(),
	})
	if err != nil {
		return err
	}

	// 第 1 行合并标题（skipTitle 口径：Title 为空则跳过）
	headerRow := 1
	if s.Title != "" {
		headerRow = 2
		if err := f.MergeCell(s.Name, "A1", lastVisible); err != nil {
			return fmt.Errorf("exceldoc: 合并标题单元格: %w", err)
		}
		if err := f.SetRowHeight(s.Name, 1, titleRowHeight); err != nil {
			return err
		}
		if err := f.SetCellValue(s.Name, "A1", s.Title); err != nil {
			return err
		}
		if err := f.SetCellStyle(s.Name, "A1", lastVisible, titleStyle); err != nil {
			return err
		}
	}

	// 表头行
	if err := f.SetRowHeight(s.Name, headerRow, headerRowHeight); err != nil {
		return err
	}
	for i, c := range s.Columns {
		cell, _ := excelize.CoordinatesToCellName(i+1, headerRow)
		if err := f.SetCellValue(s.Name, cell, c.Header); err != nil {
			return err
		}
	}
	idHeadCell, _ := excelize.CoordinatesToCellName(idColIdx, headerRow)
	if err := f.SetCellValue(s.Name, idHeadCell, idHeader); err != nil {
		return err
	}
	headEnd, _ := excelize.CoordinatesToCellName(idColIdx, headerRow)
	if err := f.SetCellStyle(s.Name, mustCellName(1, headerRow), headEnd, headerStyle); err != nil {
		return err
	}

	// 数据行
	for r, row := range s.Rows {
		rowNum := headerRow + 1 + r
		if err := f.SetRowHeight(s.Name, rowNum, dataRowHeight); err != nil {
			return err
		}
		for col := 1; col <= visibleCols; col++ {
			cell, _ := excelize.CoordinatesToCellName(col, rowNum)
			val := ""
			if col-1 < len(row) {
				val = row[col-1]
			}
			if err := f.SetCellValue(s.Name, cell, val); err != nil {
				return err
			}
		}
		rowEnd, _ := excelize.CoordinatesToCellName(idColIdx, rowNum)
		if err := f.SetCellStyle(s.Name, mustCellName(1, rowNum), rowEnd, dataStyle); err != nil {
			return err
		}
		// 隐藏 ID 列：数值写入（行级比对锚点），0 写空
		idCell, _ := excelize.CoordinatesToCellName(idColIdx, rowNum)
		if err := f.SetCellStyle(s.Name, idCell, idCell, idStyle); err != nil {
			return err
		}
		if len(s.IDs) > r && s.IDs[r] != 0 {
			if err := f.SetCellValue(s.Name, idCell, s.IDs[r]); err != nil {
				return err
			}
		}
	}
	return nil
}

// mustCellName 小工具：行列号转单元格名（调用方保证列号合法）。
func mustCellName(col, row int) string {
	name, _ := excelize.CoordinatesToCellName(col, row)
	return name
}

// ReadWorkbook 回读 xlsx 结构（标题 / 表头 / 数据 / 隐藏 ID 列 / 行高列宽 / 合并区），
// 供生成-回读闭环测试与后续内网同步行级比对使用。
func ReadWorkbook(data []byte) (*WorkbookData, error) {
	f, err := excelize.OpenReader(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("exceldoc: 打开 xlsx: %w", err)
	}
	defer f.Close()

	out := &WorkbookData{}
	for _, name := range f.GetSheetList() {
		sd := SheetData{
			Name:       name,
			RowHeights: map[int]float64{},
			ColWidths:  map[string]float64{},
		}

		merges, err := f.GetMergeCells(name)
		if err != nil {
			return nil, err
		}
		for _, m := range merges {
			sd.TitleMerges = append(sd.TitleMerges, MergedRange{Start: m.GetStartAxis(), End: m.GetEndAxis()})
		}

		rows, err := f.GetRows(name)
		if err != nil {
			return nil, err
		}
		if len(sd.TitleMerges) > 0 && len(rows) > 0 && len(rows[0]) > 0 {
			sd.Title = rows[0][0]
			sd.HeaderRow = 2
		} else {
			sd.HeaderRow = 1
		}
		if len(rows) >= sd.HeaderRow {
			sd.Headers = rows[sd.HeaderRow-1]
		}
		if len(rows) > sd.HeaderRow {
			sd.Rows = rows[sd.HeaderRow:]
		}

		// 隐藏 ID 列 = 表头行最后一个单元格之后的第一列（与 WriteWorkbook 口径一致）
		if len(sd.Headers) > 0 {
			idCol := len(sd.Headers) // 0 起列号 + 1 → GetRows 稀疏行里可能被截断
			for _, row := range sd.Rows {
				if len(row) > idCol-1 && idCol >= 1 {
					sd.IDs = append(sd.IDs, row[len(sd.Headers)-1])
				} else {
					sd.IDs = append(sd.IDs, "")
				}
			}
			idColName, err := excelize.ColumnNumberToName(len(sd.Headers))
			if err == nil {
				hidden, err := f.GetColVisible(name, idColName)
				if err == nil {
					sd.IDColHidden = !hidden
				}
				if w, err := f.GetColWidth(name, idColName); err == nil {
					sd.ColWidths[idColName] = w
				}
			}
		}

		// 列宽（前 len(headers)-1 列）
		for i := 1; i < len(sd.Headers); i++ {
			colName, err := excelize.ColumnNumberToName(i)
			if err != nil {
				continue
			}
			if w, err := f.GetColWidth(name, colName); err == nil {
				sd.ColWidths[colName] = w
			}
		}

		// 行高
		total := len(rows)
		if total == 0 {
			total = 3
		}
		for r := 1; r <= total; r++ {
			if h, err := f.GetRowHeight(name, r); err == nil {
				sd.RowHeights[r] = h
			}
		}

		out.Sheets = append(out.Sheets, sd)
	}
	return out, nil
}
