package crossing

import (
	"database/sql"
	"fmt"
	"math"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"

	"github.com/go-chi/chi/v5"

	"shade/internal/compat/exceldoc"
)

// 统计表导出与排序/命名/线路顺序工具（对齐 server/src/crossing/export.js）。
// 隐藏记录 ID 列是内网同步行级比对锚点；split=true 按运维线路分 sheet，未匹配记录归末尾「未匹配线路」sheet。

// lineOrderRow team_line 一条运维线路（分组顺序源）。
type lineOrderRow struct {
	VoltageLevel string
	LineName     string
}

// fetchLineOrder 线路顺序表：team_line 本班运维线路（sort 序）；空表 → 空序列，记录全部落 unmatched。
func fetchLineOrder(db *sql.DB, teamID int64) ([]lineOrderRow, error) {
	rows, err := db.Query(
		"SELECT voltage_level, line_name FROM team_line WHERE team_id = ? ORDER BY sort, id", teamID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []lineOrderRow{}
	for rows.Next() {
		var l lineOrderRow
		if err := rows.Scan(&l.VoltageLevel, &l.LineName); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// fmtExportDate 导出日期单元格文本：'YYYY-MM-DD' → 'yyyy.MM.dd'，空 → ”。
func fmtExportDate(v *string) string {
	if v == nil {
		return ""
	}
	s := strings.TrimSpace(*v)
	m := dateRe.FindStringSubmatch(s)
	if m == nil {
		return s
	}
	month, _ := strconv.Atoi(m[2])
	day, _ := strconv.Atoi(m[3])
	return fmt.Sprintf("%s.%02d.%02d", m[1], month, day)
}

// safeFileName 文件名非法字符清洗（Windows/COS 通用：\ / : * ? " < > | 与控制字符一律替换为 _）。
func safeFileName(name string) string {
	var b strings.Builder
	for _, r := range name {
		switch {
		case strings.ContainsRune(`\/:*?"<>|`, r):
			b.WriteRune('_')
		case r < 0x20:
			b.WriteRune('_')
		default:
			b.WriteRune(r)
		}
	}
	s := strings.TrimSpace(b.String())
	if s == "" {
		return "未命名"
	}
	return s
}

// exportFileName 导出文件名：分线路「{线路名}交跨统计表.xlsx」，全量「{班组名}交跨统计表.xlsx」。
func exportFileName(teamName, lineName string) string {
	base := teamName
	if lineName != "" {
		base = lineName
	}
	return safeFileName(base) + "交跨统计表.xlsx"
}

var numRe = regexp.MustCompile(`\d+(?:\.\d+)?`)

// numPrefix 数值前缀（电压等级等按数值排序用；无数字 → +Inf 排尾）。
func numPrefix(v string) float64 {
	m := numRe.FindString(v)
	if m == "" {
		return math.Inf(1)
	}
	f, err := strconv.ParseFloat(m, 64)
	if err != nil {
		return math.Inf(1)
	}
	return f
}

// cmpFloat 比较两个数值（含 ±Inf）。
func cmpFloat(a, b float64) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	default:
		return 0
	}
}

// compareSection 区段升序：中文序 + 数字自然序（「2-10」排在「10-20」前），空值排尾。
// Go 标准库无 zh-CN localeCompare，按 数字自然序 + 码点序 近似（中文按 Unicode 码点）。
func compareSection(a, b string) int {
	sa := strings.TrimSpace(a)
	sb := strings.TrimSpace(b)
	if sa == "" && sb == "" {
		return 0
	}
	if sa == "" {
		return 1
	}
	if sb == "" {
		return -1
	}
	return naturalCompare(sa, sb)
}

// naturalCompare 数字自然序比较（连续数字段按数值比较，其余按 rune 码点）。
func naturalCompare(a, b string) int {
	ra, rb := []rune(a), []rune(b)
	i, j := 0, 0
	for i < len(ra) || j < len(rb) {
		if i < len(ra) && j < len(rb) && unicode.IsDigit(ra[i]) && unicode.IsDigit(rb[j]) {
			si, sj := i, j
			for i < len(ra) && unicode.IsDigit(ra[i]) {
				i++
			}
			for j < len(rb) && unicode.IsDigit(rb[j]) {
				j++
			}
			na, _ := strconv.ParseFloat(string(ra[si:i]), 64)
			nb, _ := strconv.ParseFloat(string(rb[sj:j]), 64)
			if c := cmpFloat(na, nb); c != 0 {
				return c
			}
			continue
		}
		ca, cb := rune(0), rune(0)
		if i < len(ra) {
			ca = ra[i]
		}
		if j < len(rb) {
			cb = rb[j]
		}
		if ca != cb {
			if ca < cb {
				return -1
			}
			return 1
		}
		i++
		j++
	}
	return 0
}

// makeIntraComparator 组内比较器：区段升序 → pole_from 数值升序（NULL 排尾）→ created_at。
func makeIntraComparator() func(a, b Record) int {
	return func(a, b Record) int {
		if c := compareSection(a.Section, b.Section); c != 0 {
			return c
		}
		pa := math.Inf(1)
		if a.PoleFrom != nil {
			pa = float64(*a.PoleFrom)
		}
		pb := math.Inf(1)
		if b.PoleFrom != nil {
			pb = float64(*b.PoleFrom)
		}
		if c := cmpFloat(pa, pb); c != 0 {
			return c
		}
		switch {
		case a.CreatedAt.Before(b.CreatedAt):
			return -1
		case a.CreatedAt.After(b.CreatedAt):
			return 1
		default:
			return 0
		}
	}
}

// makeLineGroupComparator 分组键比较器（记录列表 / 分表导出用）：team_line 运维线路导入序
// （lineOrder: line_name → index），未匹配排尾、未匹配间按线路名排序。
func makeLineGroupComparator(lineOrder map[string]int) func(a, b Record) int {
	rank := func(name string) float64 {
		if i, ok := lineOrder[name]; ok {
			return float64(i)
		}
		return math.Inf(1)
	}
	return func(a, b Record) int {
		la, lb := rank(a.LineName), rank(b.LineName)
		if la != lb {
			return cmpFloat(la, lb)
		}
		if math.IsInf(la, 1) {
			return naturalCompare(a.LineName, b.LineName)
		}
		return 0
	}
}

// exportGroupComparator 分组键比较器（单表导出用）：电压等级数值降序（无数字排尾）→ 线路名称升序。
func exportGroupComparator(a, b Record) int {
	va, vb := numPrefix(a.VoltageLevel), numPrefix(b.VoltageLevel)
	if va != vb {
		if math.IsInf(va, 1) {
			return 1
		}
		if math.IsInf(vb, 1) {
			return -1
		}
		return cmpFloat(vb, va) // 降序
	}
	return naturalCompare(a.LineName, b.LineName)
}

// sortRecords 记录排序（纯函数；调用方先做 lineName 过滤）：分组键比较器 → 组内比较器。
func sortRecords(records []Record, groupComparator func(a, b Record) int) []Record {
	intra := makeIntraComparator()
	out := append([]Record(nil), records...)
	sort.SliceStable(out, func(i, j int) bool {
		if c := groupComparator(out[i], out[j]); c != 0 {
			return c < 0
		}
		return intra(out[i], out[j]) < 0
	})
	return out
}

var (
	kvRe     = regexp.MustCompile(`(?i)kv`)
	plainNum = regexp.MustCompile(`^\d+(?:\.\d+)?$`)
)

// fmtVoltageCell 电压等级单元格幂等格式化：值已含 kV 不重复追加，纯数字才拼 kV，其余原样。
func fmtVoltageCell(v string) string {
	s := strings.TrimSpace(v)
	if s == "" {
		return ""
	}
	if kvRe.MatchString(s) {
		return s
	}
	if plainNum.MatchString(s) {
		return s + "kV"
	}
	return s
}

// lineGroup 分表导出的一个 sheet 组。
type lineGroup struct {
	LineName  string
	Voltage   string
	Records   []Record
	Unmatched bool
}

// groupByLine 分表归组：按 line_name 保序归组，组序=首现序（team_line 导入序 + 未匹配排尾）。
func groupByLine(records []Record, lineOrder map[string]int) []lineGroup {
	groups := []lineGroup{}
	byName := map[string]int{}
	unmatched := []Record{}
	for _, rec := range records {
		if _, ok := lineOrder[rec.LineName]; ok {
			idx, seen := byName[rec.LineName]
			if !seen {
				groups = append(groups, lineGroup{LineName: rec.LineName, Voltage: rec.VoltageLevel})
				idx = len(groups) - 1
				byName[rec.LineName] = idx
			}
			groups[idx].Records = append(groups[idx].Records, rec)
		} else {
			unmatched = append(unmatched, rec)
		}
	}
	if len(unmatched) > 0 {
		groups = append(groups, lineGroup{LineName: "未匹配线路", Records: unmatched, Unmatched: true})
	}
	return groups
}

// sheetNameOf sheet 名清洗（Excel 约束）：禁字符 [ ] : * ? / \ 换全角、≤31 字符、空名兜底「未命名线路」、重名追加序号（n）。
func sheetNameOf(base string, used map[string]bool) string {
	replacer := strings.NewReplacer(
		"[", "【", "]", "】", ":", "：", "*", "＊", "?", "？", "/", "／", "\\", "＼",
	)
	name := strings.TrimSpace(replacer.Replace(base))
	if name == "" {
		name = "未命名线路"
	}
	name = truncate(name, 31)
	if !used[name] {
		used[name] = true
		return name
	}
	for i := 2; ; i++ {
		suffix := fmt.Sprintf("（%d）", i)
		cand := truncate(name, 31-len([]rune(suffix))) + suffix
		if !used[cand] {
			used[cand] = true
			return cand
		}
	}
}

// exportHeaders 表头（第 2 行；第 1 行为「{班组}交跨统计表」合并标题）；第 15 列（O 列）为隐藏记录ID 锚点列。
var exportHeaders = []string{
	"序号", "电压等级", "线路名称", "跨越区段", "跨越类型",
	"交叉点距小号侧距离（m）", "交叉点距大号侧距离（m）", "交叉物距边线水平距离（m）",
	"交叉垂直距离（m）", "交叉角（°）", "是否存在隐患", "发现人", "巡视时间", "备注",
}

// exportColWidths 列宽（与 Node COL_WIDTHS 一致）。
var exportColWidths = []float64{5, 10, 15, 15, 20, 14, 14, 16, 12, 10, 12, 12, 12, 24}

// sheetSpecOf 单 sheet 模板内容（含隐藏记录 ID 列）。
func sheetSpecOf(name, title string, records []Record) exceldoc.SheetSpec {
	cols := make([]exceldoc.Column, len(exportHeaders))
	for i, h := range exportHeaders {
		cols[i] = exceldoc.Column{Header: h, Width: exportColWidths[i]}
	}
	rows := make([][]string, len(records))
	ids := make([]int64, len(records))
	for i, rec := range records {
		rows[i] = []string{
			strconv.Itoa(i + 1),
			fmtVoltageCell(rec.VoltageLevel),
			rec.LineName,
			rec.Section,
			rec.CrossingType,
			rec.DistSmall,
			rec.DistBig,
			rec.CrossHorizontal,
			rec.CrossVertical,
			rec.CrossAngle,
			rec.HasHazard,
			rec.FinderName,
			fmtExportDate(rec.InspectAt),
			rec.Remark,
		}
		ids[i] = rec.ID
	}
	return exceldoc.SheetSpec{Name: name, Title: title, Columns: cols, Rows: rows, IDs: ids}
}

// buildWorkbook 生成统计表工作簿（内部按出口口径排序，调用方传未排序记录即可）：
// split=false 单 sheet「交跨」，行序 = 电压等级数值降序 → 线路名称升序 → 区段升序，标题「{班组}交跨统计表」；
// split=true 按线路分 sheet（sheet 划分与顺序 = team_line 导入序，sheet 内 区段升序），
// sheet 名 = fmtVoltageCell(电压)+线路名（Excel 名约束清洗），未匹配记录归末尾「未匹配线路」sheet。
func buildWorkbook(teamName string, records []Record, split bool, lineOrder map[string]int) ([]byte, error) {
	var sorted []Record
	if split {
		sorted = sortRecords(records, makeLineGroupComparator(lineOrder))
	} else {
		sorted = sortRecords(records, exportGroupComparator)
	}
	if !split {
		return exceldoc.WriteWorkbook(exceldoc.WorkbookSpec{
			Sheets: []exceldoc.SheetSpec{sheetSpecOf("交跨", teamName+"交跨统计表", sorted)},
		})
	}
	groups := groupByLine(sorted, lineOrder)
	if len(groups) == 0 {
		return exceldoc.WriteWorkbook(exceldoc.WorkbookSpec{
			Sheets: []exceldoc.SheetSpec{sheetSpecOf("交跨", teamName+"交跨统计表", nil)},
		})
	}
	used := map[string]bool{}
	sheets := make([]exceldoc.SheetSpec, 0, len(groups))
	for _, g := range groups {
		label := fmtVoltageCell(g.Voltage) + g.LineName
		title := label + "交跨统计表"
		if g.Unmatched {
			label = "未匹配线路"
			title = "未匹配线路"
		}
		sheets = append(sheets, sheetSpecOf(sheetNameOf(label, used), title, g.Records))
	}
	return exceldoc.WriteWorkbook(exceldoc.WorkbookSpec{Sheets: sheets})
}

// splitOn query/body 的 split 开关归一（'1'/'true' 为开，对齐 Node splitOn 的字符串面）。
func splitOn(v string) bool {
	return v == "1" || v == "true"
}

// exportXLSX GET /export?line=&split=：统计表 xlsx 下载（:name 文件名后缀变体供查看器按扩展名识别格式；
// 二进制响应不走 {code,data} 信封）。
func (m *Module) exportXLSX(w http.ResponseWriter, r *http.Request) {
	_, team, ok := m.enter(w, r, 0)
	if !ok {
		return
	}
	if team == nil {
		fail(w, http.StatusBadRequest, codeNoTeam, "无可用班组")
		return
	}
	line := strings.TrimSpace(r.URL.Query().Get("line"))
	split := splitOn(r.URL.Query().Get("split"))
	records, err := m.loadTeamRecords(team.ID)
	if err != nil {
		writeErr(w, err)
		return
	}
	lineRows, err := fetchLineOrder(m.DB, team.ID)
	if err != nil {
		writeErr(w, err)
		return
	}
	lineOrder := map[string]int{}
	for i, l := range lineRows {
		lineOrder[l.LineName] = i
	}
	if line != "" {
		filtered := []Record{}
		for _, rec := range records {
			if rec.LineName == line {
				filtered = append(filtered, rec)
			}
		}
		records = filtered
	}
	buf, err := buildWorkbook(team.Name, records, split, lineOrder)
	if err != nil {
		writeErr(w, err)
		return
	}
	fileName := exportFileName(team.Name, line)
	w.Header().Set("Content-Type", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet")
	w.Header().Set("Content-Disposition",
		fmt.Sprintf(`attachment; filename="crossing.xlsx"; filename*=UTF-8''%s`, urlEncode(fileName)))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(buf)
}

// urlEncode RFC3986 百分号编码（与 Node encodeURIComponent 对中文文件名的口径一致）。
func urlEncode(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') ||
			c == '-' || c == '_' || c == '.' || c == '~' {
			b.WriteByte(c)
		} else {
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}
	return b.String()
}

// pathID 取路由参数 id（非法值按 0 处理，后续查询自然落 40400，同 JS Number(param) 口径）。
func pathID(r *http.Request) int64 {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil || id < 0 {
		return 0
	}
	return id
}
