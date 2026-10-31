package teamdata

import (
	"archive/zip"
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/xuri/excelize/v2"
)

// Excel 导入口径（对齐 server/src/teamdata 的 multer + xlsx 解析）：单文件 .xlsx、10MB 限值。
const maxImportBytes = 10 * 1024 * 1024

// XLSX 解析预算：ZIP 结构（条目/解压总量/单条目/深度）与行数在解析/入库前限制。
var (
	maxXLSXEntries      = 5000
	maxXLSXEntryBytes   = int64(64 << 20)
	maxXLSXUncompressed = int64(256 << 20)
	maxXLSXDepth        = 8
	maxImportRows       = 20000
)

// errTooManyRows Excel 行数超出预算。
var errTooManyRows = errors.New("Excel 行数超出限制")

// checkXLSXArchive 在 excelize 解析前检查 ZIP 结构预算（防解压炸弹/超深目录）：
// 先按中央目录声明快速拒绝，再对每个条目实际限读上限+1（声明可被伪造，不能只信 UncompressedSize64）。
func checkXLSXArchive(data []byte) error {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return err
	}
	if len(zr.File) > maxXLSXEntries {
		return fmt.Errorf("xlsx 条目数超出限制（最多 %d）", maxXLSXEntries)
	}
	var total int64
	for _, f := range zr.File {
		name := strings.Trim(f.Name, "/")
		if name != "" && strings.Count(name, "/")+1 > maxXLSXDepth {
			return fmt.Errorf("xlsx 目录层级超出限制（最多 %d 层）", maxXLSXDepth)
		}
		if f.UncompressedSize64 > uint64(maxXLSXEntryBytes) {
			return fmt.Errorf("xlsx 单条目解压后超出限制（最多 %d 字节）", maxXLSXEntryBytes)
		}
		total += int64(f.UncompressedSize64)
		if total > maxXLSXUncompressed {
			return fmt.Errorf("xlsx 解压总量超出限制（最多 %d 字节）", maxXLSXUncompressed)
		}
	}
	// 实际解压复核：逐 entry 限读 min(单条目, 剩余总量)+1，超限即拒绝。
	var actual int64
	for _, f := range zr.File {
		rc, err := f.Open()
		if err != nil {
			return err
		}
		limit := maxXLSXEntryBytes
		if remaining := maxXLSXUncompressed - actual; remaining < limit {
			limit = remaining
		}
		n, readErr := io.Copy(io.Discard, io.LimitReader(rc, limit+1))
		_ = rc.Close()
		if int64(n) > limit {
			if limit == maxXLSXEntryBytes {
				return fmt.Errorf("xlsx 单条目实际解压超出限制（最多 %d 字节）", maxXLSXEntryBytes)
			}
			return fmt.Errorf("xlsx 实际解压总量超出限制（最多 %d 字节）", maxXLSXUncompressed)
		}
		actual += int64(n)
		if readErr != nil && !errors.Is(readErr, io.ErrUnexpectedEOF) && !errors.Is(readErr, zip.ErrChecksum) {
			return readErr
		}
	}
	return nil
}

// towerTemplate GET /towers/template：杆塔坐标导入模板（表头 + 示例行）。
func (m *Module) towerTemplate(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := m.enter(w, r, 0, false); !ok {
		return
	}
	buf, err := buildTemplate([][]string{
		{"电压等级", "线路名称", "杆塔号", "经度", "纬度"},
		{"110kV", "示例线路", "N1", "111.123456", "37.123456"},
	}, "杆塔坐标", []float64{12, 20, 12, 14, 14})
	if err != nil {
		writeErr(w, err)
		return
	}
	writeXLSX(w, buf, "tower-template.xlsx", "杆塔坐标导入模板.xlsx")
}

// oplineTemplate GET /oplines/template：运维线路导入模板（仅表头行）。
func (m *Module) oplineTemplate(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := m.enter(w, r, 0, false); !ok {
		return
	}
	buf, err := buildTemplate([][]string{
		{"电压等级", "线路名称"},
	}, "运维线路", []float64{12, 20})
	if err != nil {
		writeErr(w, err)
		return
	}
	writeXLSX(w, buf, "opline-template.xlsx", "运维线路导入模板.xlsx")
}

// buildTemplate 生成模板 xlsx（文本单元格 + 列宽）。
func buildTemplate(rows [][]string, sheet string, widths []float64) ([]byte, error) {
	f := excelize.NewFile()
	defer f.Close()
	if err := f.SetSheetName("Sheet1", sheet); err != nil {
		return nil, err
	}
	for i, w := range widths {
		col, err := excelize.ColumnNumberToName(i + 1)
		if err != nil {
			return nil, err
		}
		if err := f.SetColWidth(sheet, col, col, w); err != nil {
			return nil, err
		}
	}
	for ri, row := range rows {
		for ci, val := range row {
			cell, err := excelize.CoordinatesToCellName(ci+1, ri+1)
			if err != nil {
				return nil, err
			}
			if err := f.SetCellValue(sheet, cell, val); err != nil {
				return nil, err
			}
		}
	}
	buf, err := f.WriteToBuffer()
	if err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// writeXLSX 写出 xlsx 下载响应（对齐 Node 的 Content-Disposition 双文件名口径）。
func writeXLSX(w http.ResponseWriter, buf []byte, fallbackName, utf8Name string) {
	w.Header().Set("Content-Type", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet")
	w.Header().Set("Content-Disposition",
		fmt.Sprintf(`attachment; filename="%s"; filename*=UTF-8''%s`, fallbackName, url.PathEscape(utf8Name)))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(buf)
}

// towerImport POST /towers/import：导入杆塔坐标 Excel，全量替换本班组坐标（事务；跳过行计数）。
func (m *Module) towerImport(w http.ResponseWriter, r *http.Request) {
	if !parseImportForm(w, r) {
		return
	}
	_, team, ok := m.enter(w, r, formTeamID(r), true)
	if !ok {
		return
	}
	if team == nil {
		fail(w, http.StatusBadRequest, codeFile, "无可用班组")
		return
	}
	buf, ok := readUploadFile(w, r)
	if !ok {
		return
	}
	rows, err := readSheetRows(buf)
	if err != nil {
		fail(w, http.StatusBadRequest, codeFile, "Excel 解析失败，请使用模板文件填写")
		return
	}
	data := [][]string{}
	skipped := 0
	for _, cells := range rows {
		c := normalizeCells(cells, 5)
		if allEmpty(c) {
			continue
		}
		voltage, line, towerNo, lng, lat := c[0], c[1], c[2], c[3], c[4]
		if strings.Contains(voltage, "电压") || strings.Contains(towerNo, "杆塔") {
			continue // 表头行
		}
		if voltage == "" || line == "" || towerNo == "" || lng == "" || lat == "" ||
			!isNumeric(lng) || !isNumeric(lat) {
			skipped++
			continue
		}
		data = append(data, []string{
			truncate(voltage, 32), truncate(line, 64), truncate(towerNo, 64),
			truncate(lng, 32), truncate(lat, 32),
		})
	}
	if len(data) == 0 {
		fail(w, http.StatusBadRequest, codeFile, "未识别到有效坐标行，请按模板列填写")
		return
	}
	tx, err := m.DB.BeginTx(r.Context(), nil)
	if err != nil {
		writeErr(w, err)
		return
	}
	defer tx.Rollback() //nolint:errcheck // 提交后回滚为空操作
	if _, err := tx.ExecContext(r.Context(), "DELETE FROM worklog_tower WHERE team_id = ?", team.ID); err != nil {
		writeErr(w, err)
		return
	}
	for i, row := range data {
		if _, err := tx.ExecContext(r.Context(),
			"INSERT INTO worklog_tower (team_id, voltage_level, line_name, tower_no, lng, lat, sort) VALUES (?, ?, ?, ?, ?, ?, ?)",
			team.ID, row[0], row[1], row[2], row[3], row[4], i+1); err != nil {
			writeErr(w, err)
			return
		}
	}
	if err := tx.Commit(); err != nil {
		writeErr(w, err)
		return
	}
	if m.OnTowersChanged != nil {
		m.OnTowersChanged(team.ID) // 与 worklog 共用的杆塔缓存失效重载
	}
	writeOK(w, map[string]any{"imported": len(data), "skipped": skipped},
		fmt.Sprintf("已导入 %d 条坐标", len(data)))
}

// oplineImport POST /oplines/import：导入运维线路 Excel，全量替换本班组线路（事务；
// 空行/表头行跳过、缺电压或线路名、文件内 line_name 重复计跳过防撞 uk_team_line）。
func (m *Module) oplineImport(w http.ResponseWriter, r *http.Request) {
	if !parseImportForm(w, r) {
		return
	}
	_, team, ok := m.enter(w, r, formTeamID(r), true)
	if !ok {
		return
	}
	if team == nil {
		fail(w, http.StatusBadRequest, codeFile, "无可用班组")
		return
	}
	buf, ok := readUploadFile(w, r)
	if !ok {
		return
	}
	rows, err := readSheetRows(buf)
	if err != nil {
		fail(w, http.StatusBadRequest, codeFile, "Excel 解析失败，请使用模板文件填写")
		return
	}
	data := [][]string{}
	seen := map[string]bool{}
	skipped := 0
	for _, cells := range rows {
		c := normalizeCells(cells, 2)
		if allEmpty(c) {
			continue
		}
		voltage, line := c[0], c[1]
		if strings.Contains(voltage, "电压") || strings.Contains(line, "线路") {
			continue // 表头行
		}
		if voltage == "" || line == "" || seen[line] {
			skipped++
			continue
		}
		seen[line] = true
		data = append(data, []string{truncate(voltage, 16), truncate(line, 64)})
	}
	if len(data) == 0 {
		fail(w, http.StatusBadRequest, codeFile, "未识别到有效线路行，请按模板列填写")
		return
	}
	tx, err := m.DB.BeginTx(r.Context(), nil)
	if err != nil {
		writeErr(w, err)
		return
	}
	defer tx.Rollback() //nolint:errcheck // 提交后回滚为空操作
	if _, err := tx.ExecContext(r.Context(), "DELETE FROM team_line WHERE team_id = ?", team.ID); err != nil {
		writeErr(w, err)
		return
	}
	for i, row := range data {
		if _, err := tx.ExecContext(r.Context(),
			"INSERT INTO team_line (team_id, voltage_level, line_name, sort) VALUES (?, ?, ?, ?)",
			team.ID, row[0], row[1], i+1); err != nil {
			writeErr(w, err)
			return
		}
	}
	if err := tx.Commit(); err != nil {
		writeErr(w, err)
		return
	}
	writeOK(w, map[string]any{"imported": len(data), "skipped": skipped},
		fmt.Sprintf("已导入 %d 条线路", len(data)))
}

// parseImportForm 解析 multipart 表单（10MB 限值对齐 multer limits.fileSize）。
func parseImportForm(w http.ResponseWriter, r *http.Request) bool {
	r.Body = http.MaxBytesReader(w, r.Body, maxImportBytes+64<<10)
	if err := r.ParseMultipartForm(4 << 20); err != nil {
		fail(w, http.StatusBadRequest, codeFile, "文件大小应在 10MB 以内")
		return false
	}
	return true
}

// readUploadFile 读取 multipart 上传的单个 .xlsx 文件（失败时写出响应并返回 false）。
func readUploadFile(w http.ResponseWriter, r *http.Request) ([]byte, bool) {
	file, header, err := r.FormFile("file")
	if err != nil {
		fail(w, http.StatusBadRequest, codeFile, "请选择要上传的 Excel 文件")
		return nil, false
	}
	defer file.Close()
	buf, err := io.ReadAll(io.LimitReader(file, maxImportBytes+1))
	if err != nil {
		fail(w, http.StatusBadRequest, codeFile, "文件大小应在 10MB 以内")
		return nil, false
	}
	if len(buf) > maxImportBytes {
		fail(w, http.StatusBadRequest, codeFile, "文件大小应在 10MB 以内")
		return nil, false
	}
	if len(buf) == 0 {
		fail(w, http.StatusBadRequest, codeFile, "请选择要上传的 Excel 文件")
		return nil, false
	}
	name := fixLatin1Name(header.Filename)
	if !strings.HasSuffix(strings.ToLower(name), ".xlsx") {
		fail(w, http.StatusBadRequest, codeFile, "仅支持 .xlsx 文件，请使用模板填写")
		return nil, false
	}
	return buf, true
}

// formTeamID 取表单携带的 team_id（导入接口表单优先于 query）。
func formTeamID(r *http.Request) int64 {
	if vals, ok := r.PostForm["team_id"]; ok && len(vals) > 0 {
		return parseWantedTeamID(vals[0])
	}
	return 0
}

// readSheetRows 读取首个 sheet 的全部行（空白单元格补 ”）；解析前做 ZIP 实际解压预算，
// 解析后按行流式读取并限制行数（不先 GetRows 整表再拒绝）。
func readSheetRows(data []byte) ([][]string, error) {
	if err := checkXLSXArchive(data); err != nil {
		return nil, err
	}
	f, err := excelize.OpenReader(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	defer f.Close()
	sheets := f.GetSheetList()
	if len(sheets) == 0 {
		return nil, fmt.Errorf("工作簿没有 sheet")
	}
	iter, err := f.Rows(sheets[0])
	if err != nil {
		return nil, err
	}
	defer iter.Close()
	rows := make([][]string, 0, 256)
	for iter.Next() {
		row, err := iter.Columns()
		if err != nil {
			return nil, err
		}
		rows = append(rows, row)
		if len(rows) > maxImportRows {
			return nil, errTooManyRows
		}
	}
	if err := iter.Error(); err != nil {
		return nil, err
	}
	return rows, nil
}

// normalizeCells 单元格 trim 并补足 n 列（短行/空单元格按空串取值，防越界）。
func normalizeCells(cells []string, n int) []string {
	out := make([]string, n)
	for i := 0; i < n && i < len(cells); i++ {
		out[i] = strings.TrimSpace(cells[i])
	}
	return out
}

// allEmpty 判定整行为空。
func allEmpty(cells []string) bool {
	for _, c := range cells {
		if c != "" {
			return false
		}
	}
	return true
}

// isNumeric 判定 JS Number(x) 非 NaN（空串已在前置条件排除）。
func isNumeric(s string) bool {
	_, err := strconv.ParseFloat(s, 64)
	return err == nil
}

// fixLatin1Name 修复 multipart 文件名被 latin1 误解的 UTF-8 乱码（对齐 server/src/utils/file.js）。
func fixLatin1Name(name string) string {
	mangled := false
	bs := make([]byte, 0, len(name))
	for _, r := range name {
		if r > 0xFF {
			return name
		}
		if r >= 0x80 {
			mangled = true
		}
		bs = append(bs, byte(r))
	}
	if mangled && utf8.Valid(bs) {
		return string(bs)
	}
	return name
}
