// 派车单对齐：解析派车系统导出表（.xlsx，每个驾驶员一份、可多份合并），按「日期＋用车人」与当天出车卡片配对，
// 比对车牌号码、用车人集合、目的地与派车单号，产出差异清单（本模块只读对齐不自动改；更正走 PUT /logs/{id}，
// 表格有系统无的补建走 POST /logs，车牌入字典走 POST /admin/vehicles）。
// 对齐口径（同 server/src/worklog/dispatch.js）：
//
//	· 仅「派车单类型＝用车申请调度」的行参与（维保调度等忽略）；关键四列固定列位 B=车牌号 / F=用车人 / G=预计用车时间 / K=目的地
//	· 导入行再按班组字典过滤：用车人中无本班成员（启用中）的行整行剔除，全剔空报 400
//	· 匹配键：日期（预计用车时间只取到天）＋用车人（空格分隔姓名集合）；同日内按用车人重合度贪心配对
//	· 目的地模糊比对（去「中国/省/市/县/区」后互相包含）；表格目的地/派车单号为空不约束
//	· 差异类型：matched（车牌/用车人多出·缺少/目的地/派车单号）、sheetOnly、entryOnly；只读对齐不写库
//
// 旧版 .xls（BIFF）暂无 Go 解析库，导入即报错提示另存为 .xlsx（不假成功）。
package worklog

import (
	"bytes"
	"errors"
	"fmt"
	"mime/multipart"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/xuri/excelize/v2"
)

// 派车单类型过滤值（仅该类型参与对齐）。
const dispatchRowType = "用车申请调度"

// 关键列固定列位（0 基）：B=车牌号 / F=用车人 / G=预计用车时间 / K=目的地（用户指定口径，不按表头名定位）。
const (
	colPlate = 1
	colUsers = 5
	colTime  = 6
	colTo    = 10
)

// sheetRow 派车导出表有效行。
type sheetRow struct {
	File    string   `json:"file"`
	Date    string   `json:"date"`
	Plate   string   `json:"plate"`
	Driver  string   `json:"driver"`
	Members []string `json:"members"`
	From    string   `json:"from"`
	To      string   `json:"to"`
	Reason  string   `json:"reason"`
	OrderNo string   `json:"orderNo"`
}

// alignEntry 对齐用出车卡片。
type alignEntry struct {
	ID          int64      `json:"id"`
	VehicleID   int64      `json:"vehicle_id"`
	Plate       string     `json:"plate"`
	DestID      int64      `json:"destination_id"`
	Destination string     `json:"destination"`
	OrderNo     string     `json:"order_no"`
	Patrol      string     `json:"patrol_content"`
	Members     []alignMem `json:"members"`
	LogDate     string     `json:"-"`
}

type alignMem struct {
	MemberID int64  `json:"member_id"`
	Name     string `json:"name"`
}

// alignDiffs 差异标记。
type alignDiffs struct {
	Plate   bool     `json:"plate"`
	Missing []string `json:"missing"`
	Extra   []string `json:"extra"`
	Dest    bool     `json:"dest"`
	OrderNo bool     `json:"orderNo"`
}

// alignItem 差异清单行。
type alignItem struct {
	Kind  string      `json:"kind"`
	Date  string      `json:"date"`
	Sheet *sheetRow   `json:"sheet"`
	Entry *alignEntry `json:"entry"`
	Diffs *alignDiffs `json:"diffs"`
}

// alignSummary 对齐概览。
type alignSummary struct {
	Imported    int `json:"imported"`
	Skipped     int `json:"skipped"`
	Outside     int `json:"outside"`
	Files       int `json:"files"`
	Consistent  int `json:"consistent"`
	Plate       int `json:"plate"`
	Members     int `json:"members"`
	Destination int `json:"destination"`
	OrderNo     int `json:"orderNo"`
	SheetOnly   int `json:"sheetOnly"`
	EntryOnly   int `json:"entryOnly"`
}

// parseSheetDate 预计用车时间 → YYYY-MM-DD（只取到天，忽略时分）。
// 导出表该列格式不固定（2026-08-19 14:30 / 2026/8/19 / 2026年8月19日 / Excel 序列数），逐格式归一。
var (
	sheetDateTextRe   = regexp.MustCompile(`(\d{4})\s*[-/年.]\s*(\d{1,2})\s*[-/月.]\s*(\d{1,2})`)
	sheetDateSerialRe = regexp.MustCompile(`^\d{5}(\.\d+)?$`)
)

func parseSheetDate(raw string) string {
	s := strings.TrimSpace(raw)
	if s == "" {
		return ""
	}
	if m := sheetDateTextRe.FindStringSubmatch(s); m != nil {
		y, _ := strconv.Atoi(m[1])
		mo, _ := strconv.Atoi(m[2])
		d, _ := strconv.Atoi(m[3])
		return fmt.Sprintf("%04d-%02d-%02d", y, mo, d)
	}
	if sheetDateSerialRe.MatchString(s) {
		v, err := strconv.ParseFloat(s, 64)
		if err == nil {
			// Excel 序列数（1900 日期系统）：基准 1899-12-30
			t := time.Date(1899, 12, 30, 0, 0, 0, 0, time.UTC).AddDate(0, 0, int(v))
			return t.Format("2006-01-02")
		}
	}
	return ""
}

// destSame 目的地模糊一致：表格目的地常省略行政区字样或带上级前缀（如卡片「孝义市」表格写「吕梁市孝义」），
// 两侧去除「中国/省/市/县/区」后互相包含即视为一致；归一化后不足 2 字回退原文包含；
// 表格目的地为空不约束，表格有而卡片未选 → 不一致。
func destSame(cardDest, sheetTo string) bool {
	card := strings.TrimSpace(cardDest)
	sheet := strings.TrimSpace(sheetTo)
	if sheet == "" {
		return true
	}
	if card == "" {
		return false
	}
	if card == sheet || strings.Contains(card, sheet) || strings.Contains(sheet, card) {
		return true
	}
	reg := strings.NewReplacer("中国", "", "省", "", "市", "", "县", "", "区", "")
	nc := reg.Replace(card)
	ns := reg.Replace(sheet)
	if len([]rune(nc)) < 2 || len([]rune(ns)) < 2 {
		return false
	}
	return strings.Contains(nc, ns) || strings.Contains(ns, nc)
}

// parseDispatchFile 解析单份派车导出表 → 有效行 + 时间无法识别跳过数；缺「派车单类型」表头列报错（消息带文件名）。
func parseDispatchFile(data []byte, fileName string) ([]sheetRow, int, error) {
	f, err := excelize.OpenReader(bytes.NewReader(data))
	if err != nil {
		return nil, 0, fmt.Errorf("「%s」解析失败，请使用派车系统原始导出表", fileName)
	}
	defer f.Close()
	sheets := f.GetSheetList()
	if len(sheets) == 0 {
		return nil, 0, fmt.Errorf("「%s」为空表", fileName)
	}
	rows, err := f.GetRows(sheets[0])
	if err != nil {
		return nil, 0, fmt.Errorf("「%s」解析失败，请使用派车系统原始导出表", fileName)
	}
	if len(rows) == 0 {
		return nil, 0, fmt.Errorf("「%s」为空表", fileName)
	}
	cell := func(row []string, i int) string {
		if i < 0 || i >= len(row) {
			return ""
		}
		return strings.TrimSpace(row[i])
	}
	header := rows[0]
	aux := map[string]int{"驾驶员": -1, "用车事由": -1, "派车单号": -1, "派车单类型": -1}
	for i, h := range header {
		if _, ok := aux[strings.TrimSpace(h)]; ok {
			aux[strings.TrimSpace(h)] = i
		}
	}
	if aux["派车单类型"] < 0 {
		return nil, 0, fmt.Errorf("「%s」缺少必需列「派车单类型」，请使用派车系统原始导出表", fileName)
	}
	out := make([]sheetRow, 0)
	skipped := 0
	for _, row := range rows[1:] {
		empty := true
		for _, c := range row {
			if strings.TrimSpace(c) != "" {
				empty = false
				break
			}
		}
		if empty {
			continue
		}
		if cell(row, aux["派车单类型"]) != dispatchRowType {
			continue // 维保调度等非出工行忽略
		}
		date := parseSheetDate(cell(row, colTime))
		if date == "" {
			skipped++ // 预计用车时间无法识别
			continue
		}
		out = append(out, sheetRow{
			File:    fileName,
			Date:    date,
			Plate:   cell(row, colPlate),
			Driver:  cell(row, aux["驾驶员"]),
			Members: strings.Fields(cell(row, colUsers)), // F 列空格分隔姓名
			To:      cell(row, colTo),
			Reason:  cell(row, aux["用车事由"]),
			OrderNo: cell(row, aux["派车单号"]),
		})
	}
	return out, skipped, nil
}

// pairOneDay 同日内贪心配对：候选对按用车人重合人数降序，依次成对（双方均未配对时生效）；0 重合不配对。
func pairOneDay(rows []sheetRow, entries []alignEntry) (matched [][2]int, sheetOnly, entryOnly []int) {
	type cand struct{ r, e, overlap int }
	cands := make([]cand, 0)
	for r := range rows {
		names := map[string]bool{}
		for _, n := range rows[r].Members {
			names[n] = true
		}
		for e := range entries {
			overlap := 0
			for _, m := range entries[e].Members {
				if names[m.Name] {
					overlap++
				}
			}
			if overlap > 0 {
				cands = append(cands, cand{r, e, overlap})
			}
		}
	}
	sort.SliceStable(cands, func(i, j int) bool {
		if cands[i].overlap != cands[j].overlap {
			return cands[i].overlap > cands[j].overlap
		}
		if cands[i].r != cands[j].r {
			return cands[i].r < cands[j].r
		}
		return cands[i].e < cands[j].e
	})
	rowUsed := map[int]bool{}
	entryUsed := map[int]bool{}
	for _, c := range cands {
		if rowUsed[c.r] || entryUsed[c.e] {
			continue
		}
		rowUsed[c.r] = true
		entryUsed[c.e] = true
		matched = append(matched, [2]int{c.r, c.e})
	}
	for i := range rows {
		if !rowUsed[i] {
			sheetOnly = append(sheetOnly, i)
		}
	}
	for i := range entries {
		if !entryUsed[i] {
			entryOnly = append(entryOnly, i)
		}
	}
	return matched, sheetOnly, entryOnly
}

// 派车单导入限制：单文件 10MB、一次最多 20 个（与 Node multer 同口径）。
const (
	dispatchMaxFileBytes = 10 << 20
	dispatchMaxFiles     = 20
)

// errDispatchTooManyFiles 派车单文件数量超限。
var errDispatchTooManyFiles = errors.New("一次最多导入 20 个文件")

// checkDispatchUploadBudget 按 multipart FileHeader 元数据预检文件数/单文件大小（不读取内容）。
func checkDispatchUploadBudget(files []*multipart.FileHeader) error {
	if len(files) > dispatchMaxFiles {
		return errDispatchTooManyFiles
	}
	for _, fh := range files {
		if fh.Size > dispatchMaxFileBytes {
			return errors.New("单个文件大小应在 10MB 以内")
		}
	}
	return nil
}

// handleDispatchAlign POST /dispatch/align：导入派车单（multipart 字段 files，多文件合并解析）按「日期＋用车人」
// 对齐当天出车卡片，返回差异清单（只读对齐不写库）。
func (m *Module) handleDispatchAlign(w http.ResponseWriter, r *http.Request) {
	_, team, ok := m.enterDict(w, r)
	if !ok {
		return
	}
	if team == nil {
		fail(w, http.StatusBadRequest, codeDispatchFile, "无可用班组")
		return
	}
	// 请求体在解析前限总量（20×10MB + 表单开销），避免超大请求整体落盘/占内存。
	r.Body = http.MaxBytesReader(w, r.Body, dispatchMaxFiles*dispatchMaxFileBytes+1<<20)
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		fail(w, http.StatusBadRequest, codeDispatchFile, "单个文件大小应在 10MB 以内")
		return
	}
	files := r.MultipartForm.File["files"]
	if len(files) == 0 {
		fail(w, http.StatusBadRequest, codeDispatchFile, "请选择派车单文件")
		return
	}
	if err := checkDispatchUploadBudget(files); err != nil {
		if err == errDispatchTooManyFiles {
			fail(w, http.StatusBadRequest, codeDispatchFile, "一次最多导入 20 个文件")
			return
		}
		fail(w, http.StatusBadRequest, codeDispatchFile, "单个文件大小应在 10MB 以内")
		return
	}
	type namedBuf struct {
		name string
		buf  []byte
	}
	inputs := make([]namedBuf, 0, len(files))
	for _, fh := range files {
		name := fixLatin1Name(fh.Filename)
		lower := strings.ToLower(name)
		if !strings.HasSuffix(lower, ".xls") && !strings.HasSuffix(lower, ".xlsx") {
			fail(w, http.StatusBadRequest, codeDispatchFile,
				"「"+name+"」不是 Excel 文件（仅支持 .xls / .xlsx）")
			return
		}
		if strings.HasSuffix(lower, ".xls") {
			// 旧版 BIFF 格式 Go 侧无解析库（Node SheetJS 支持），如实报错不假成功
			fail(w, http.StatusBadRequest, codeDispatchFile,
				"「"+name+"」旧版 .xls 解析依赖未接入，请另存为 .xlsx 后导入")
			return
		}
		fp, err := fh.Open()
		if err != nil {
			writeErr(w, err)
			return
		}
		buf, err := readAllLimit(fp, dispatchMaxFileBytes)
		_ = fp.Close()
		if err != nil {
			fail(w, http.StatusBadRequest, codeDispatchFile, "单个文件大小应在 10MB 以内")
			return
		}
		inputs = append(inputs, namedBuf{name: name, buf: buf})
	}

	allRows := make([]sheetRow, 0)
	skipped := 0
	for _, in := range inputs {
		rows, skip, err := parseDispatchFile(in.buf, in.name)
		if err != nil {
			fail(w, http.StatusBadRequest, codeDispatchFile, err.Error())
			return
		}
		allRows = append(allRows, rows...)
		skipped += skip
	}
	if len(allRows) == 0 {
		fail(w, http.StatusBadRequest, codeDispatchFile,
			"未识别到有效派车行（仅「派车单类型＝用车申请调度」的行参与对齐）")
		return
	}

	// 剔除「用车人中无本班成员（启用中字典）」的行：他班派车混入导出件时不进清单
	memRows, err := m.DB.QueryContext(r.Context(),
		`SELECT name FROM worklog_member WHERE status = 1 AND team_id = ?`, team.ID)
	if err != nil {
		writeErr(w, err)
		return
	}
	teamNames := map[string]bool{}
	for memRows.Next() {
		var name string
		if memRows.Scan(&name) == nil {
			teamNames[name] = true
		}
	}
	memRows.Close()
	teamRows := make([]sheetRow, 0, len(allRows))
	for _, row := range allRows {
		for _, n := range row.Members {
			if teamNames[n] {
				teamRows = append(teamRows, row)
				break
			}
		}
	}
	outside := len(allRows) - len(teamRows)
	if len(teamRows) == 0 {
		fail(w, http.StatusBadRequest, codeDispatchFile,
			fmt.Sprintf("导入行剔除后无剩余：全部 %d 行的用车人均不含本班成员，请确认导出件属于本班组", len(allRows)))
		return
	}

	dates := make([]string, 0, len(teamRows))
	for _, row := range teamRows {
		dates = append(dates, row.Date)
	}
	sort.Strings(dates)
	entries, err := m.loadAlignEntries(r, team.ID, dates[0], dates[len(dates)-1])
	if err != nil {
		writeErr(w, err)
		return
	}

	// 按日分组配对
	type dayBucket struct {
		rows    []sheetRow
		entries []alignEntry
	}
	byDate := map[string]*dayBucket{}
	for _, row := range teamRows {
		b := byDate[row.Date]
		if b == nil {
			b = &dayBucket{}
			byDate[row.Date] = b
		}
		b.rows = append(b.rows, row)
	}
	for _, e := range entries {
		b := byDate[e.LogDate]
		if b == nil {
			b = &dayBucket{}
			byDate[e.LogDate] = b
		}
		b.entries = append(b.entries, e)
	}

	summary := alignSummary{Imported: len(teamRows), Skipped: skipped, Outside: outside, Files: len(inputs)}
	items := make([]alignItem, 0)
	for date, b := range byDate {
		matched, sheetOnly, entryOnly := pairOneDay(b.rows, b.entries)
		for _, p := range matched {
			row := b.rows[p[0]]
			entry := b.entries[p[1]]
			entryNames := make([]string, 0, len(entry.Members))
			for _, mem := range entry.Members {
				entryNames = append(entryNames, mem.Name)
			}
			missing := make([]string, 0) // 表格有、卡片缺
			for _, n := range row.Members {
				found := false
				for _, en := range entryNames {
					if en == n {
						found = true
						break
					}
				}
				if !found {
					missing = append(missing, n)
				}
			}
			extra := make([]string, 0) // 卡片有、表格无
			for _, n := range entryNames {
				found := false
				for _, sn := range row.Members {
					if sn == n {
						found = true
						break
					}
				}
				if !found {
					extra = append(extra, n)
				}
			}
			plateDiff := row.Plate != entry.Plate
			destDiff := !destSame(entry.Destination, row.To)
			orderNoDiff := row.OrderNo != "" && row.OrderNo != entry.OrderNo
			if !plateDiff && len(missing) == 0 && len(extra) == 0 && !destDiff && !orderNoDiff {
				summary.Consistent++
				continue
			}
			if plateDiff {
				summary.Plate++
			}
			if len(missing) > 0 || len(extra) > 0 {
				summary.Members++
			}
			if destDiff {
				summary.Destination++
			}
			if orderNoDiff {
				summary.OrderNo++
			}
			rowCopy := row
			entryCopy := entry
			items = append(items, alignItem{
				Kind:  "matched",
				Date:  date,
				Sheet: &rowCopy,
				Entry: &entryCopy,
				Diffs: &alignDiffs{Plate: plateDiff, Missing: missing, Extra: extra, Dest: destDiff, OrderNo: orderNoDiff},
			})
		}
		for _, i := range sheetOnly {
			rowCopy := b.rows[i]
			items = append(items, alignItem{Kind: "sheetOnly", Date: date, Sheet: &rowCopy})
		}
		for _, i := range entryOnly {
			entryCopy := b.entries[i]
			items = append(items, alignItem{Kind: "entryOnly", Date: date, Entry: &entryCopy})
		}
	}
	// 差异清单按日期倒序
	sort.SliceStable(items, func(i, j int) bool { return items[i].Date > items[j].Date })
	for _, it := range items {
		switch it.Kind {
		case "sheetOnly":
			summary.SheetOnly++
		case "entryOnly":
			summary.EntryOnly++
		}
	}
	writeOK(w, map[string]any{"summary": summary, "items": items},
		"对齐完成："+strconv.Itoa(len(items))+" 条不一致")
}

// loadAlignEntries 取日期范围内全部出车卡片（未出车卡片无车牌/用车人，不参与对齐）。
func (m *Module) loadAlignEntries(r *http.Request, teamID int64, minDate, maxDate string) ([]alignEntry, error) {
	rows, err := m.DB.QueryContext(r.Context(),
		`SELECT e.id, DATE_FORMAT(e.log_date, '%Y-%m-%d'), COALESCE(e.vehicle_id, 0),
		        COALESCE(v.plate_no, ''), COALESCE(e.destination_id, 0), COALESCE(d.name, ''),
		        COALESCE(e.dispatch_order_no, ''), COALESCE(e.patrol_content, '')
		 FROM worklog_entry e
		 LEFT JOIN worklog_vehicle v ON v.id = e.vehicle_id
		 LEFT JOIN worklog_destination d ON d.id = e.destination_id
		 WHERE e.team_id = ? AND e.log_date BETWEEN ? AND ? AND e.vehicle_id IS NOT NULL
		 ORDER BY e.created_at, e.id`, teamID, minDate, maxDate)
	if err != nil {
		return nil, err
	}
	entries := make([]alignEntry, 0)
	ids := make([]int64, 0)
	for rows.Next() {
		var e alignEntry
		if rows.Scan(&e.ID, &e.LogDate, &e.VehicleID, &e.Plate, &e.DestID, &e.Destination,
			&e.OrderNo, &e.Patrol) == nil {
			entries = append(entries, e)
			ids = append(ids, e.ID)
		}
	}
	rows.Close()
	if len(entries) == 0 {
		return entries, nil
	}
	mrows, err := m.DB.QueryContext(r.Context(),
		`SELECT em.entry_id, em.member_id, m.name FROM worklog_entry_member em
		 JOIN worklog_member m ON m.id = em.member_id
		 WHERE em.entry_id IN (`+placeholders(len(ids))+`) ORDER BY em.sort, em.id`,
		idsToAny(ids)...)
	if err != nil {
		return nil, err
	}
	byEntry := map[int64][]alignMem{}
	for mrows.Next() {
		var entryID, memberID int64
		var name string
		if mrows.Scan(&entryID, &memberID, &name) == nil {
			byEntry[entryID] = append(byEntry[entryID], alignMem{MemberID: memberID, Name: name})
		}
	}
	mrows.Close()
	for i := range entries {
		entries[i].Members = byEntry[entries[i].ID]
	}
	return entries, nil
}
