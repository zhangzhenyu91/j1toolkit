package treehazard

import (
	"context"
	"fmt"
	"math"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"shade/internal/compat/exceldoc"
)

// 导出表头（第 2 行；第 1 行为「{班组}树木统计表」合并标题）；第 17 列（Q 列）为隐藏记录ID 锚点列。
var exportHeaders = []string{
	"序号", "电压等级", "线路名称", "区段", "树种", "数量",
	"距小号侧距离（m）", "距大号侧距离（m）", "直径（cm）", "水平距离（m）", "垂直距离（m）",
	"发现人", "发现时间", "处理人", "处理情况", "处理时间",
}

// 列宽（2026-10 定稿口径）。
var exportColWidths = []float64{5, 10, 15, 15, 15, 10, 10, 10, 10, 10, 10, 15, 15, 15, 10, 15}

var (
	dateRe = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)
	numRe  = regexp.MustCompile(`(\d+(?:\.\d+)?)`)
)

// Line 运维线路（team_line）分组行。
type Line struct {
	VoltageLevel string `json:"voltage_level"`
	LineName     string `json:"line_name"`
}

// fetchLineOrder 线路顺序表：team_line 本班运维线路（sort 序）。
func (m *Module) fetchLineOrder(ctx context.Context, teamID int64) ([]Line, error) {
	rows, err := m.DB.QueryContext(ctx,
		`SELECT voltage_level, line_name FROM team_line WHERE team_id = ? ORDER BY sort, id`, teamID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]Line, 0)
	for rows.Next() {
		var l Line
		if err := rows.Scan(&l.VoltageLevel, &l.LineName); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// parseVerticalNum 垂直距离解析值：取文本中首个数字（可带小数）；空/无数字 → nil。
func parseVerticalNum(text string) *float64 {
	m := numRe.FindStringSubmatch(text)
	if m == nil {
		return nil
	}
	n, err := strconv.ParseFloat(m[1], 64)
	if err != nil {
		return nil
	}
	return &n
}

// normDateStr 日期归一为 'yyyy-MM-dd'：兼容 'yyyy.MM.dd' / 'yyyy-MM-dd' / 'yyyy/M/D'；无法识别 → ""。
func normDateStr(v string) string {
	s := strings.TrimSpace(v)
	if s == "" {
		return ""
	}
	m := regexp.MustCompile(`^(\d{4})[-./](\d{1,2})[-./](\d{1,2})`).FindStringSubmatch(s)
	if m == nil {
		return ""
	}
	month, _ := strconv.Atoi(m[2])
	day, _ := strconv.Atoi(m[3])
	return fmt.Sprintf("%s-%02d-%02d", m[1], month, day)
}

// parseSectionPoles 区段文本（如「3#-4#」）尽力解析杆号区间；解析不出 → [nil, nil]。
func parseSectionPoles(section string) (from, to *int64) {
	m := regexp.MustCompile(`(\d+)\s*#?\s*[-—~～]\s*(\d+)\s*#?`).FindStringSubmatch(section)
	if m == nil {
		return nil, nil
	}
	a, _ := strconv.ParseInt(m[1], 10, 64)
	b, _ := strconv.ParseInt(m[2], 10, 64)
	return &a, &b
}

// safeFileName 文件名非法字符清洗（Windows/COS 通用）。
func safeFileName(name string) string {
	s := strings.TrimSpace(strings.Map(func(r rune) rune {
		switch r {
		case '\\', '/', ':', '*', '?', '"', '<', '>', '|':
			return '_'
		}
		if r < 32 {
			return '_'
		}
		return r
	}, name))
	if s == "" {
		return "未命名"
	}
	return s
}

// exportFileName 导出文件名：分线路「{线路名}树木统计表.xlsx」，全量「{班组名}树木统计表.xlsx」。
func exportFileName(teamName, lineName string) string {
	base := teamName
	if lineName != "" {
		base = lineName
	}
	return safeFileName(base) + "树木统计表.xlsx"
}

// fmtExportDate 导出日期单元格文本：'YYYY-MM-DD' → 'yyyy.MM.dd'，空 → ”。
func fmtExportDate(v string) string {
	s := strings.TrimSpace(v)
	if s == "" {
		return ""
	}
	m := regexp.MustCompile(`^(\d{4})[-./](\d{1,2})[-./](\d{1,2})`).FindStringSubmatch(s)
	if m == nil {
		return s
	}
	month, _ := strconv.Atoi(m[2])
	day, _ := strconv.Atoi(m[3])
	return fmt.Sprintf("%s.%02d.%02d", m[1], month, day)
}

// fmtVoltageCell 电压等级单元格幂等格式化：纯数字才拼 kV。
func fmtVoltageCell(v string) string {
	s := strings.TrimSpace(v)
	if s == "" {
		return ""
	}
	if strings.Contains(strings.ToLower(s), "kv") {
		return s
	}
	if _, err := strconv.ParseFloat(s, 64); err == nil {
		return s + "kV"
	}
	return s
}

// numPrefix 数值前缀（电压等级按数值排序用；无数字 → +Inf 排尾）。
func numPrefix(v string) float64 {
	m := numRe.FindStringSubmatch(v)
	if m == nil {
		return math.Inf(1)
	}
	n, err := strconv.ParseFloat(m[1], 64)
	if err != nil {
		return math.Inf(1)
	}
	return n
}

// statusTier 状态档位：未处理在前 > 已处理在后。
func statusTier(r Record) int {
	if r.Status == "handled" {
		return 1
	}
	return 0
}

// compareSection 区段升序：中文序 + 数字自然序，空值排尾。
func compareSection(a, b string) int {
	sa, sb := strings.TrimSpace(a), strings.TrimSpace(b)
	if sa == "" && sb == "" {
		return 0
	}
	if sa == "" {
		return 1
	}
	if sb == "" {
		return -1
	}
	return strings.Compare(sa, sb)
}

// makeIntraComparator 组内比较器：状态档位 → 区段升序 → pole_from 升序（空排尾）→ created_at。
func makeIntraComparator() func(a, b Record) int {
	return func(a, b Record) int {
		if t := statusTier(a) - statusTier(b); t != 0 {
			return t
		}
		if s := compareSection(a.Section, b.Section); s != 0 {
			return s
		}
		pa, pb := math.Inf(1), math.Inf(1)
		if a.PoleFrom != nil {
			pa = float64(*a.PoleFrom)
		}
		if b.PoleFrom != nil {
			pb = float64(*b.PoleFrom)
		}
		if pa != pb {
			if pa < pb {
				return -1
			}
			return 1
		}
		return a.CreatedAt.Compare(b.CreatedAt)
	}
}

// makeLineGroupComparator 分组键比较器：team_line 导入序，未匹配排尾且按线路名排序。
func makeLineGroupComparator(lineOrder map[string]int) func(a, b Record) int {
	rank := func(name string) int {
		if i, ok := lineOrder[name]; ok {
			return i
		}
		return math.MaxInt
	}
	return func(a, b Record) int {
		la, lb := rank(a.LineName), rank(b.LineName)
		if la != lb {
			if la < lb {
				return -1
			}
			return 1
		}
		if la == math.MaxInt {
			return strings.Compare(a.LineName, b.LineName)
		}
		return 0
	}
}

// exportGroupComparator 单表导出分组比较器：电压等级数值降序 → 线路名称升序。
func exportGroupComparator(a, b Record) int {
	va, vb := numPrefix(a.VoltageLevel), numPrefix(b.VoltageLevel)
	if va != vb {
		if math.IsInf(va, 1) {
			return 1
		}
		if math.IsInf(vb, 1) {
			return -1
		}
		if va > vb {
			return -1
		}
		return 1
	}
	return strings.Compare(a.LineName, b.LineName)
}

// sortRecords 记录排序：分组键比较器 → 组内比较器；statusFirst=true（单表导出）把状态档位提到最前。
func sortRecords(records []Record, groupComparator func(a, b Record) int, statusFirst bool) []Record {
	intra := makeIntraComparator()
	out := append([]Record{}, records...)
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if statusFirst {
			if t := statusTier(a) - statusTier(b); t != 0 {
				return t < 0
			}
		}
		if g := groupComparator(a, b); g != 0 {
			return g < 0
		}
		return intra(a, b) < 0
	})
	return out
}

// groupByLine 分表归组：按 line_name 保序归组，未匹配归末尾「未匹配线路」组。
func groupByLine(records []Record, lineOrder map[string]int) []lineGroup {
	groups := make([]lineGroup, 0)
	byName := map[string]*lineGroup{}
	unmatched := lineGroup{LineName: "未匹配线路", Unmatched: true, Records: []Record{}}
	unmatchedPtr := &unmatched
	for _, rec := range records {
		if _, known := lineOrder[rec.LineName]; known {
			g, ok := byName[rec.LineName]
			if !ok {
				g = &lineGroup{LineName: rec.LineName, Voltage: rec.VoltageLevel, Records: []Record{}}
				byName[rec.LineName] = g
				groups = append(groups, *g)
			}
			g.Records = append(g.Records, rec)
			// 同步回 groups（值语义）
			for i := range groups {
				if groups[i].LineName == g.LineName {
					groups[i] = *g
				}
			}
		} else {
			unmatchedPtr.Records = append(unmatchedPtr.Records, rec)
		}
	}
	if len(unmatchedPtr.Records) > 0 {
		groups = append(groups, *unmatchedPtr)
	}
	return groups
}

type lineGroup struct {
	LineName  string
	Voltage   string
	Records   []Record
	Unmatched bool
}

// sheetNameOf sheet 名清洗（Excel 约束）：禁字符换全角、≤31 字符、重名追加序号。
func sheetNameOf(base string, used map[string]bool) string {
	name := strings.TrimSpace(strings.Map(func(r rune) rune {
		switch r {
		case '[':
			return '【'
		case ']':
			return '】'
		case ':':
			return '：'
		case '*':
			return '＊'
		case '?':
			return '？'
		case '/':
			return '／'
		case '\\':
			return '＼'
		}
		return r
	}, base))
	if name == "" {
		name = "未命名线路"
	}
	name = truncateRunes(name, 31)
	if !used[name] {
		used[name] = true
		return name
	}
	for i := 2; ; i++ {
		suffix := fmt.Sprintf("（%d）", i)
		cand := truncateRunes(name, 31-len([]rune(suffix))) + suffix
		if !used[cand] {
			used[cand] = true
			return cand
		}
	}
}

// buildWorkbook 生成统计表（exceldoc 与 exceljs 导出口径一致：合并标题 + 隐藏记录ID 列）：
// split=false 单 sheet「树障」，行序 = 未处理在前 > 电压降序 → 线路名 → 区段；
// split=true 按线路分 sheet（team_line 导入序，未匹配排尾），sheet 内未处理在前 > 区段升序。
func buildWorkbook(teamName string, records []Record, split bool, lineOrder map[string]int) ([]byte, error) {
	sorted := sortRecords(records, makeLineGroupComparator(lineOrder), false)
	if !split {
		sorted = sortRecords(records, exportGroupComparator, true)
		return exceldoc.WriteWorkbook(exceldoc.WorkbookSpec{
			Sheets: []exceldoc.SheetSpec{buildSheet("树障", teamName+"树木统计表", sorted)},
		})
	}
	groups := groupByLine(sorted, lineOrder)
	if len(groups) == 0 {
		return exceldoc.WriteWorkbook(exceldoc.WorkbookSpec{
			Sheets: []exceldoc.SheetSpec{buildSheet("树障", teamName+"树木统计表", nil)},
		})
	}
	used := map[string]bool{}
	sheets := make([]exceldoc.SheetSpec, 0, len(groups))
	for _, g := range groups {
		label := fmtVoltageCell(g.Voltage) + g.LineName
		title := label + "树木统计表"
		if g.Unmatched {
			label = "未匹配线路"
			title = "未匹配线路"
		}
		sheets = append(sheets, buildSheet(sheetNameOf(label, used), title, g.Records))
	}
	return exceldoc.WriteWorkbook(exceldoc.WorkbookSpec{Sheets: sheets})
}

// buildSheet 单 worksheet 模板：序号自 1 重排，日期 'yyyy.MM.dd'，隐藏记录 ID 列。
func buildSheet(name, title string, records []Record) exceldoc.SheetSpec {
	cols := make([]exceldoc.Column, 0, len(exportHeaders))
	for i, h := range exportHeaders {
		cols = append(cols, exceldoc.Column{Header: h, Width: exportColWidths[i]})
	}
	rows := make([][]string, 0, len(records))
	ids := make([]int64, 0, len(records))
	for i, rec := range records {
		rows = append(rows, []string{
			strconv.Itoa(i + 1),
			fmtVoltageCell(rec.VoltageLevel),
			rec.LineName,
			rec.Section,
			rec.TreeSpecies,
			rec.TreeCount,
			rec.DistSmall,
			rec.DistBig,
			rec.Diameter,
			rec.Horizontal,
			rec.Vertical,
			rec.FinderName,
			fmtExportDate(rec.FoundAt),
			rec.HandlerName,
			rec.HandleNote,
			fmtExportDate(rec.HandledAt),
		})
		ids = append(ids, rec.ID)
	}
	return exceldoc.SheetSpec{Name: name, Title: title, Columns: cols, Rows: rows, IDs: ids}
}

// handleExport GET /export?line=&split=1：统计表 xlsx 下载（二进制响应不走 {code,data} 信封）。
func (m *Module) handleExport(w http.ResponseWriter, r *http.Request) {
	team, ok := teamFrom(r.Context())
	if !ok || team.ID == 0 {
		writeFail(w, http.StatusBadRequest, 40010, "无可用班组", nil)
		return
	}
	line := strings.TrimSpace(r.URL.Query().Get("line"))
	split := splitOn(r.URL.Query().Get("split"))
	records, err := m.loadTeamRecords(r.Context(), team.ID)
	if err != nil {
		writeFail(w, http.StatusInternalServerError, codeParam, "导出失败", nil)
		return
	}
	lineRows, err := m.fetchLineOrder(r.Context(), team.ID)
	if err != nil {
		writeFail(w, http.StatusInternalServerError, codeParam, "导出失败", nil)
		return
	}
	lineOrder := map[string]int{}
	for i, l := range lineRows {
		lineOrder[l.LineName] = i
	}
	if line != "" {
		filtered := make([]Record, 0)
		for _, rec := range records {
			if rec.LineName == line {
				filtered = append(filtered, rec)
			}
		}
		records = filtered
	}
	buf, err := buildWorkbook(team.Name, records, split, lineOrder)
	if err != nil {
		writeFail(w, http.StatusInternalServerError, codeParam, "导出失败", nil)
		return
	}
	fileName := exportFileName(team.Name, line)
	w.Header().Set("Content-Type", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet")
	w.Header().Set("Content-Disposition",
		fmt.Sprintf(`attachment; filename="treehazard.xlsx"; filename*=UTF-8''%s`, encodeRFC5987(fileName)))
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(buf)
}

// splitOn split 开关归一（'1'/'true' 为开）。
func splitOn(v string) bool {
	return v == "1" || v == "true"
}

func encodeRFC5987(s string) string {
	var sb strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') ||
			c == '-' || c == '_' || c == '.' || c == '~' {
			sb.WriteByte(c)
			continue
		}
		fmt.Fprintf(&sb, "%%%02X", c)
	}
	return sb.String()
}

// ===== 小工具 =====

func truncateRunes(s string, n int) string {
	if n <= 0 {
		return ""
	}
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return string(runes[:n])
}

func asString(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	case bool:
		if t {
			return "true"
		}
		return "false"
	default:
		return strings.TrimSpace(fmt.Sprint(t))
	}
}

func asInt64(v any) (int64, bool) {
	switch t := v.(type) {
	case float64:
		if t == math.Trunc(t) {
			return int64(t), true
		}
	case string:
		n, err := strconv.ParseInt(strings.TrimSpace(t), 10, 64)
		return n, err == nil
	case int64:
		return t, true
	case int:
		return int64(t), true
	}
	return 0, false
}

func parseFloat(s string) (float64, error) {
	return strconv.ParseFloat(strings.TrimSpace(s), 64)
}

// beijingToday 北京时间当日 'YYYY-MM-DD'（与 Node todayCn 同口径）。
func beijingToday() string {
	return time.Now().UTC().Add(8 * time.Hour).Format("2006-01-02")
}
