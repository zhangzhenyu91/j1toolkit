// 题库搜题：Excel 题库导入与模板生成（excelize 实现，替代渲染层 SheetJS 解析）。
// 表头口径与 bank-store.cjs / QuizSearchPage.vue 一致：
// 题干、答案、选项A~F（可选：题型、解析）；模板带三行示例（单选/多选/判断）。
package quizsearch

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/xuri/excelize/v2"
)

// templateSheetName 模板工作表名（对齐 QuizSearchPage.vue 下载模板）。
const templateSheetName = "题库模板"

// templateHeaders 模板表头行。
var templateHeaders = []string{"题干", "答案", "选项A", "选项B", "选项C", "选项D", "选项E", "选项F", "题型", "解析"}

// templateColWidths 模板列宽（对齐 Vue 侧 wch 数组）。
var templateColWidths = []float64{40, 6, 22, 22, 22, 22, 12, 12, 8, 30}

// templateExampleRows 三行示例分别覆盖 单选/多选/判断；与导入器字段口径一致，闭环可导。
var templateExampleRows = [][]string{
	{
		"示例单选：我国安全生产工作的方针是？",
		"A",
		"安全第一、预防为主、综合治理",
		"质量第一",
		"效率优先",
		"效益优先",
		"", "", "",
		"2014 年安全生产法修订确立",
	},
	{
		"示例多选：下列属于个人防护用品的有？",
		"ABD",
		"安全帽",
		"防护手套",
		"普通眼镜",
		"防护鞋",
		"", "", "多选",
		"",
	},
	{
		"示例判断：安全生产，人人有责。",
		"A",
		"正确",
		"错误",
		"", "", "", "", "判断",
		"判断题 A=正确 B=错误；选项列可留空",
	},
}

// WriteTemplate 生成题库模板 xlsx 到 path。
func WriteTemplate(path string) error {
	f := excelize.NewFile()
	defer f.Close()
	idx, err := f.NewSheet(templateSheetName)
	if err != nil {
		return fmt.Errorf("创建模板工作表失败：%w", err)
	}
	f.DeleteSheet("Sheet1")
	f.SetActiveSheet(idx)
	rows := append([][]string{templateHeaders}, templateExampleRows...)
	for r, row := range rows {
		for c, v := range row {
			cell, err := excelize.CoordinatesToCellName(c+1, r+1)
			if err != nil {
				return err
			}
			if err := f.SetCellValue(templateSheetName, cell, v); err != nil {
				return err
			}
		}
	}
	for i, w := range templateColWidths {
		col, err := excelize.ColumnNumberToName(i + 1)
		if err != nil {
			return err
		}
		if err := f.SetColWidth(templateSheetName, col, col, w); err != nil {
			return err
		}
	}
	if err := f.SaveAs(path); err != nil {
		return fmt.Errorf("保存模板失败：%w", err)
	}
	return nil
}

// ReadWorkbookRows 读取 xlsx 全部工作表并合并为二维字符串行（对齐 QuizSearchPage.vue 导入口径）：
// 首个工作表保留表头行；后续工作表首行若含「题干/答案」表头则丢弃，避免重复表头混入数据。
func ReadWorkbookRows(path string) ([][]string, error) {
	if strings.ToLower(filepath.Ext(path)) == ".xls" {
		return nil, errors.New("旧版 .xls 暂不支持，请另存为 .xlsx 后导入")
	}
	f, err := excelize.OpenFile(path)
	if err != nil {
		return nil, fmt.Errorf("打开题库文件失败：%w", err)
	}
	defer f.Close()
	merged := make([][]string, 0, 64)
	for i, name := range f.GetSheetList() {
		rs, err := f.GetRows(name)
		if err != nil {
			return nil, fmt.Errorf("读取工作表 %q 失败：%w", name, err)
		}
		if len(rs) == 0 {
			continue
		}
		if i > 0 && isHeaderRow(rs[0]) {
			rs = rs[1:]
		}
		merged = append(merged, rs...)
	}
	return merged, nil
}

// isHeaderRow 该行是否为表头行（含「题干」或「答案」字样）。
func isHeaderRow(row []string) bool {
	for _, c := range row {
		v := strings.TrimSpace(c)
		if strings.Contains(v, "题干") || strings.Contains(v, "答案") {
			return true
		}
	}
	return false
}

// ImportFile 从 xlsx 文件导入题库（读取全工作表合并后走 ImportRows）。
func (s *BankStore) ImportFile(name, path string) (ImportResult, error) {
	rows, err := ReadWorkbookRows(path)
	if err != nil {
		return ImportResult{}, err
	}
	return s.ImportRows(name, filepath.Base(path), rows)
}
