// 任务单 / 费用汇总 / 派车汇总：范围内出车卡片生成 docx/xlsx（模板见 assets/worklog/，
// 渲染复用 compat/exceldoc：RenderDocument + MergeDocx / RenderFeeTableXML / WriteWorkbook）。
// 任务单与费用汇总生成前核验（未通过记录拦截 40901 + failures 清单，force=1 跳过）；派车汇总不核验。
package worklog

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"shade/internal/assetpath"
	"shade/internal/compat/exceldoc"
	"shade/internal/httpapi"
)

// sheetRange 解析日期范围：from/to 或 date 单日；上限 31 天。
// 失败返回可读原因（Node 同口径两分支：参数错 / 超 31 天分列）。
func sheetRange(q map[string]string) (from, to string, errMsg string) {
	from, to = q["from"], q["to"]
	if from == "" && to == "" && q["date"] != "" {
		from, to = q["date"], q["date"]
	}
	if !dateRe.MatchString(from) || !dateRe.MatchString(to) || from > to {
		return "", "", "日期格式应为 YYYY-MM-DD，且 from 不晚于 to"
	}
	d1, e1 := time.Parse("2006-01-02", from)
	d2, e2 := time.Parse("2006-01-02", to)
	if e1 != nil || e2 != nil {
		return "", "", "日期格式应为 YYYY-MM-DD，且 from 不晚于 to"
	}
	if d2.Sub(d1).Hours()/24+1 > 31 {
		return "", "", "日期范围不能超过 31 天"
	}
	return from, to, ""
}

// sheetVerifyFailures 汇总前核验：范围内未通过记录清单（无则 nil）。
func (m *Module) sheetVerifyFailures(r *http.Request, teamID int64, from, to string) []map[string]any {
	list, err := m.loadEntries(r.Context(), `e.log_date BETWEEN ? AND ? AND e.team_id = ?`,
		[]any{from, to, teamID})
	if err != nil {
		return nil
	}
	out := make([]map[string]any, 0)
	for i := range list {
		e := &list[i]
		if e.VerifyPassed != "failed" {
			continue
		}
		names := make([]string, 0, len(e.Members))
		for _, mem := range e.Members {
			names = append(names, mem.Name)
		}
		plate := e.PlateNo
		if plate == "" {
			plate = "未出车"
		}
		out = append(out, map[string]any{
			"id": e.ID, "log_date": e.LogDate, "plate_no": plate,
			"members": names, "reasons": e.VerifyReasons,
		})
	}
	return out
}

// loadSheetRows 任务单行：出车卡片（车牌+目的地+用车人+施工内容）。
func (m *Module) loadSheetRows(r *http.Request, teamID int64, from, to string) ([]map[string]string, error) {
	rows, err := m.DB.QueryContext(r.Context(),
		`SELECT e.id, DATE_FORMAT(e.log_date, '%Y-%m-%d'), COALESCE(v.plate_no, ''), COALESCE(d.name, '')
		 FROM worklog_entry e
		 LEFT JOIN worklog_vehicle v ON v.id = e.vehicle_id
		 LEFT JOIN worklog_destination d ON d.id = e.destination_id
		 WHERE e.log_date BETWEEN ? AND ? AND e.team_id = ? AND e.vehicle_id IS NOT NULL
		 ORDER BY e.log_date, e.created_at, e.id`, from, to, teamID)
	if err != nil {
		return nil, err
	}
	type row struct {
		id          int64
		date        string
		plate       string
		destination string
	}
	list := make([]row, 0)
	for rows.Next() {
		var r0 row
		if rows.Scan(&r0.id, &r0.date, &r0.plate, &r0.destination) == nil {
			list = append(list, r0)
		}
	}
	rows.Close()
	if len(list) == 0 {
		return nil, nil
	}
	ids := make([]any, len(list))
	for i := range list {
		ids[i] = list[i].id
	}
	ph := placeholders(len(list))

	memRows, err := m.DB.QueryContext(r.Context(),
		`SELECT em.entry_id, m.name FROM worklog_entry_member em JOIN worklog_member m ON m.id = em.member_id
		 WHERE em.entry_id IN (`+ph+`) ORDER BY em.sort, em.id`, ids...)
	if err != nil {
		return nil, err
	}
	namesByEntry := map[int64][]string{}
	for memRows.Next() {
		var entryID int64
		var name string
		if memRows.Scan(&entryID, &name) == nil {
			namesByEntry[entryID] = append(namesByEntry[entryID], name)
		}
	}
	memRows.Close()

	pRows, err := m.DB.QueryContext(r.Context(),
		`SELECT entry_id, work_content FROM worklog_photo
		 WHERE entry_id IN (`+ph+`) AND work_content <> '' ORDER BY id`, ids...)
	if err != nil {
		return nil, err
	}
	contentByEntry := map[int64][]string{}
	for pRows.Next() {
		var entryID int64
		var content string
		if pRows.Scan(&entryID, &content) == nil {
			arr := contentByEntry[entryID]
			dup := false
			for _, c := range arr {
				if c == content {
					dup = true
				}
			}
			if !dup {
				contentByEntry[entryID] = append(arr, content)
			}
		}
	}
	pRows.Close()

	out := make([]map[string]string, 0, len(list))
	for _, r0 := range list {
		out = append(out, map[string]string{
			"date":        r0.date,
			"plate":       r0.plate,
			"destination": r0.destination,
			"names":       strings.Join(namesByEntry[r0.id], " "),
			"contents":    strings.Join(contentByEntry[r0.id], "、"),
		})
	}
	return out, nil
}

// taskSheetTemplate 任务单模板字节（AssetsDir 下读取；测试可放最小模板）。
func (m *Module) taskSheetTemplate() ([]byte, error) {
	dir := m.assetsDir()
	return os.ReadFile(filepath.Join(dir, "task-sheet-template.docx"))
}

// feeSheetTemplate 费用汇总模板字节。
func (m *Module) feeSheetTemplate() ([]byte, error) {
	dir := m.assetsDir()
	return os.ReadFile(filepath.Join(dir, "fee-summary-template.docx"))
}

// assetsDir 模板目录：运行配置 WORKLOG_ASSETS_DIR 优先（env 兜底），缺省按 assetpath 探测
// （容器 assets/worklog 优先、仓根 server/assets/worklog 回退）。
func (m *Module) assetsDir() string {
	if v := m.configValue(context.Background(), "WORKLOG_ASSETS_DIR"); v != "" {
		return v
	}
	if v := os.Getenv("WORKLOG_ASSETS_DIR"); v != "" {
		return v
	}
	if dir := assetpath.Resolve("worklog"); dir != "" {
		return dir
	}
	return filepath.Join("assets", "worklog")
}

// buildTaskSheet 生成任务单 docx（一卡一页）；无出车卡片返回 nil。
func (m *Module) buildTaskSheet(teamName string, rows []map[string]string) ([]byte, error) {
	if len(rows) == 0 {
		return nil, nil
	}
	tpl, err := m.taskSheetTemplate()
	if err != nil {
		return nil, err
	}
	files, err := exceldoc.ReadDocx(tpl)
	if err != nil {
		return nil, err
	}
	xml := string(files["word/document.xml"])
	buffers := make([][]byte, 0, len(rows))
	for _, row := range rows {
		date := row["date"]
		parts := strings.Split(date, "-")
		y, mo, d := "", "", ""
		if len(parts) == 3 {
			y, mo, d = parts[0], parts[1], parts[2]
		}
		rendered, err := exceldoc.RenderDocument(xml, map[string]string{
			"线路杆塔号": row["contents"],
			"工作班组":  teamName,
			"工作负责人": firstWord(row["names"]),
			"年":     y,
			"月":     mo,
			"日":     d,
			"工作地点":  row["destination"],
			"工作班成员": row["names"],
			"派车情况":  row["plate"],
		}, nil)
		if err != nil {
			return nil, err
		}
		docx, err := rebuildDocx(files, rendered)
		if err != nil {
			return nil, err
		}
		buffers = append(buffers, docx)
	}
	return exceldoc.MergeDocx(buffers)
}

// firstWord 取首个名字（工作任务单「工作负责人」= 排序最前的用车人）。
func firstWord(names string) string {
	fields := strings.Fields(names)
	if len(fields) == 0 {
		return ""
	}
	return fields[0]
}

// rebuildDocx 以模板文件集为底替换 document.xml 后打包。
func rebuildDocx(files map[string][]byte, xml string) ([]byte, error) {
	cp := map[string][]byte{}
	for k, v := range files {
		cp[k] = v
	}
	cp["word/document.xml"] = []byte(xml)
	return exceldoc.BuildDocx(cp)
}

// buildFeeSheet 生成费用汇总 docx（人 × 日矩阵）；无出车卡片返回 nil。
func (m *Module) buildFeeSheet(r *http.Request, teamID int64, from, to string) ([]byte, error) {
	rows, err := m.DB.QueryContext(r.Context(),
		`SELECT e.id, DATE_FORMAT(e.log_date, '%Y-%m-%d')
		 FROM worklog_entry e
		 WHERE e.log_date BETWEEN ? AND ? AND e.team_id = ? AND e.vehicle_id IS NOT NULL
		 ORDER BY e.log_date, e.created_at, e.id`, from, to, teamID)
	if err != nil {
		return nil, err
	}
	entries := make([]exceldoc.FeeEntry, 0)
	entryIDs := make([]int64, 0)
	for rows.Next() {
		var fe exceldoc.FeeEntry
		var id int64
		if rows.Scan(&id, &fe.Date) == nil {
			fe.ID = id
			entries = append(entries, fe)
			entryIDs = append(entryIDs, id)
		}
	}
	rows.Close()
	if len(entries) == 0 {
		return nil, nil
	}
	pRows, err := m.DB.QueryContext(r.Context(),
		`SELECT DISTINCT m.id, m.name, m.sort
		 FROM worklog_entry_member em
		 JOIN worklog_member m ON m.id = em.member_id
		 JOIN worklog_entry e ON e.id = em.entry_id
		 WHERE e.log_date BETWEEN ? AND ? AND e.team_id = ? AND e.vehicle_id IS NOT NULL
		 ORDER BY m.sort, m.id`, from, to, teamID)
	if err != nil {
		return nil, err
	}
	persons := make([]exceldoc.FeePerson, 0)
	personIDs := make([]int64, 0)
	for pRows.Next() {
		var fp exceldoc.FeePerson
		if pRows.Scan(&fp.ID, &fp.Name) == nil {
			persons = append(persons, fp)
			personIDs = append(personIDs, fp.ID)
		}
	}
	pRows.Close()
	if len(persons) == 0 {
		return nil, nil
	}

	phEntries := placeholders(len(entryIDs))
	emRows, err := m.DB.QueryContext(r.Context(),
		`SELECT entry_id, member_id FROM worklog_entry_member WHERE entry_id IN (`+phEntries+`)`,
		idsToAny(entryIDs)...)
	if err != nil {
		return nil, err
	}
	memberSet := map[int64]map[int64]bool{}
	for emRows.Next() {
		var entryID, memberID int64
		if emRows.Scan(&entryID, &memberID) == nil {
			if memberSet[entryID] == nil {
				memberSet[entryID] = map[int64]bool{}
			}
			memberSet[entryID][memberID] = true
		}
	}
	emRows.Close()

	phPersons := placeholders(len(personIDs))
	feeRows, err := m.DB.QueryContext(r.Context(),
		`SELECT member_id, DATE_FORMAT(fee_date, '%Y-%m-%d'), food_fee, transit_fee
		 FROM worklog_fee WHERE member_id IN (`+phPersons+`) AND fee_date BETWEEN ? AND ?`,
		append(idsToAny(personIDs), from, to)...)
	if err != nil {
		return nil, err
	}
	feeMap := map[string]float64{}
	for feeRows.Next() {
		var memberID int64
		var feeDate string
		var food, transit float64
		if feeRows.Scan(&memberID, &feeDate, &food, &transit) == nil {
			feeMap[itoa(memberID)+"|"+feeDate] = food + transit
		}
	}
	feeRows.Close()

	tpl, err := m.feeSheetTemplate()
	if err != nil {
		return nil, err
	}
	files, err := exceldoc.ReadDocx(tpl)
	if err != nil {
		return nil, err
	}
	xml, err := exceldoc.RenderFeeTableXML(string(files["word/document.xml"]),
		exceldoc.FeeTableData{
			Persons:   persons,
			Entries:   entries,
			MemberSet: memberSet,
			FeeMap:    feeMap,
		})
	if err != nil {
		return nil, err
	}
	return rebuildDocx(files, xml)
}

// buildDispatchSheet 派车汇总 xlsx（派车单号 + 用车日期两列）；无记录返回 nil。
func (m *Module) buildDispatchSheet(r *http.Request, teamID int64, from, to string) ([]byte, error) {
	rows, err := m.DB.QueryContext(r.Context(),
		`SELECT e.dispatch_order_no, DATE_FORMAT(e.log_date, '%Y-%m-%d')
		 FROM worklog_entry e
		 WHERE e.team_id = ? AND e.log_date BETWEEN ? AND ?
		   AND e.vehicle_id IS NOT NULL
		   AND e.dispatch_order_no IS NOT NULL AND e.dispatch_order_no <> ''
		 ORDER BY e.log_date, e.created_at, e.id`, teamID, from, to)
	if err != nil {
		return nil, err
	}
	data := make([][]string, 0)
	for rows.Next() {
		var orderNo, logDate string
		if rows.Scan(&orderNo, &logDate) == nil {
			data = append(data, []string{orderNo, logDate})
		}
	}
	rows.Close()
	if len(data) == 0 {
		return nil, nil
	}
	return exceldoc.WriteWorkbook(exceldoc.WorkbookSpec{
		Sheets: []exceldoc.SheetSpec{{
			Name:    "派车汇总",
			Columns: []exceldoc.Column{{Header: "派车单号", Width: 24}, {Header: "用车日期", Width: 14}},
			Rows:    data,
		}},
	})
}

// sheetFileName 汇总表产物文件名（下载/预览/存网盘共用口径）：单日带单日期，跨天带范围。
func sheetFileName(kind, from, to string) string {
	base, ext := "工作任务单", "docx"
	switch kind {
	case "fee":
		base = "费用汇总"
	case "dispatch":
		base, ext = "派车汇总", "xlsx"
	}
	if from == to {
		return base + "-" + from + "." + ext
	}
	return base + "-" + from + "至" + to + "." + ext
}

// sheetHasRows 生成前轻量预检：范围内是否有可生成内容（task/fee=出车卡片；dispatch=派车单号记录）。
func (m *Module) sheetHasRows(r *http.Request, teamID int64, from, to, kind string) bool {
	query := `SELECT COUNT(*) FROM worklog_entry e WHERE e.log_date BETWEEN ? AND ? AND e.team_id = ?
		  AND e.vehicle_id IS NOT NULL`
	if kind == "dispatch" {
		query += ` AND e.dispatch_order_no IS NOT NULL AND e.dispatch_order_no <> ''`
	}
	var cnt int
	if err := m.DB.QueryRowContext(r.Context(), query, from, to, teamID).Scan(&cnt); err != nil {
		return false
	}
	return cnt > 0
}

// sheetVerifyGate 汇总前核验拦截（task/fee 且非 force）：有未通过记录写 40901 + failures 清单并返回 true。
func (m *Module) sheetVerifyGate(w http.ResponseWriter, r *http.Request, teamID int64, from, to string, force bool) bool {
	if force {
		return false
	}
	failures := m.sheetVerifyFailures(r, teamID, from, to)
	if len(failures) == 0 {
		return false
	}
	failData(w, http.StatusConflict, codeVerifyFailed,
		"所选范围内有 "+strconv.Itoa(len(failures))+" 条记录存在未通过项，请处理后再操作",
		map[string]any{"failures": failures})
	return true
}

// serveSheetRange 汇总表各入口共用参数解析：入参映射 → 日期范围 + 生效班组；失败已写响应。
func (m *Module) serveSheetRange(w http.ResponseWriter, r *http.Request, q map[string]string) (string, string, *httpapi.Team, bool) {
	from, to, errMsg := sheetRange(q)
	if errMsg != "" {
		fail(w, http.StatusBadRequest, codeParam, errMsg)
		return "", "", nil, false
	}
	_, team, ok := m.enterDict(w, r)
	if !ok {
		return "", "", nil, false
	}
	if team == nil {
		fail(w, http.StatusBadRequest, codeNoUsableTeam, "无可用班组")
		return "", "", nil, false
	}
	return from, to, team, true
}

// handleTaskSheetPreview GET /task-sheet/preview?from=&to=：拼在线预览地址（OO 承载页 / 微软查看器）。
func (m *Module) handleTaskSheetPreview(w http.ResponseWriter, r *http.Request) {
	m.serveSheetPreview(w, r, "task")
}

// handleFeeSheetPreview GET /fee-sheet/preview?from=&to=：拼在线预览地址。
func (m *Module) handleFeeSheetPreview(w http.ResponseWriter, r *http.Request) {
	m.serveSheetPreview(w, r, "fee")
}

// handleDispatchSheetPreview GET /dispatch-sheet/preview?from=&to=：拼在线预览地址（不核验）。
func (m *Module) handleDispatchSheetPreview(w http.ResponseWriter, r *http.Request) {
	m.serveSheetPreview(w, r, "dispatch")
}

// serveSheetPreview 汇总表在线预览共用出口：
// OO 启用（OO_PUBLIC_URL）时返回 /office.html?src=worklog-sheet 承载页地址（office 签发端点生成并回源下载接口）；
// 否则微软 Office 查看器地址（凭下载接口地址回源）。task/fee 生成前核验同下载口径（force=1 跳过）。
func (m *Module) serveSheetPreview(w http.ResponseWriter, r *http.Request, kind string) {
	q := map[string]string{
		"from": r.URL.Query().Get("from"),
		"to":   r.URL.Query().Get("to"),
		"date": r.URL.Query().Get("date"),
	}
	from, to, team, ok := m.serveSheetRange(w, r, q)
	if !ok {
		return
	}
	force := r.URL.Query().Get("force") == "1"
	if kind != "dispatch" && m.sheetVerifyGate(w, r, team.ID, from, to, force) {
		return
	}
	// 预检：无可生成内容时直接报错，避免预览服务回源拉到错误响应
	if !m.sheetHasRows(r, team.ID, from, to, kind) {
		msg := "该日期范围没有出车记录，无可生成的卡片"
		if kind == "fee" {
			msg = "该日期范围没有出车记录，无可生成的汇总"
		} else if kind == "dispatch" {
			msg = "该日期范围没有派车单记录，无可生成的汇总"
		}
		fail(w, http.StatusNotFound, codeNoPhotos, msg)
		return
	}
	fileName := sheetFileName(kind, from, to)
	var qParts []string
	qParts = append(qParts, "from="+encURI(from), "to="+encURI(to))
	if teamID := strings.TrimSpace(r.URL.Query().Get("team_id")); teamID != "" {
		qParts = append(qParts, "team_id="+encURI(teamID))
	}
	if force && kind != "dispatch" {
		qParts = append(qParts, "force=1")
	}
	// OO 启用：改返回 office.html 承载页地址（参数按签发时生效值编码；name 供承载页标题显示）
	if m.configValue(r.Context(), "OO_PUBLIC_URL") != "" {
		writeOK(w, map[string]any{
			"url": "/office.html?src=worklog-sheet&sheet=" + kind + "&" + strings.Join(qParts, "&") +
				"&name=" + encURI(fileName) + "&back=/worklog.html",
		})
		return
	}
	// 微软 Office 查看器：文件名缀在下载路径尾（查看器按 URL 路径扩展名识别格式）
	viewer := m.configValue(r.Context(), "PREVIEW_VIEWER_URL")
	if viewer == "" {
		viewer = "https://view.officeapps.live.com/op/view.aspx"
	}
	proto := r.Header.Get("X-Forwarded-Proto")
	if proto == "" {
		proto = "http"
	}
	srcPath := "/api/v1/worklog/" + kind + "-sheet/" + encURI(fileName)
	downloadURL := proto + "://" + r.Host + srcPath + "?" + strings.Join(qParts, "&") +
		"&token=" + encURI(bearerToken(r))
	writeOK(w, map[string]any{"url": viewer + "?src=" + encURI(downloadURL)})
}

// bearerToken 取当前请求 JWT（Authorization: Bearer 或 ?token= 回源），
// 供微软 Office 查看器回源地址内嵌 ?token= 鉴权（同 Node utils/preview.js 口径）。
func bearerToken(r *http.Request) string {
	v := strings.TrimSpace(r.Header.Get("Authorization"))
	if len(v) >= 7 && strings.EqualFold(v[:7], "Bearer ") {
		return strings.TrimSpace(v[7:])
	}
	return strings.TrimSpace(r.URL.Query().Get("token"))
}

// handleSheetSaveNetdisk POST /sheet/save-netdisk {sheet,from,to,force?,dir?,space?}：
// 任务单/费用汇总/派车汇总生成并保存到网盘（生成前核验口径与各 sheet 下载/预览一致；dir 缺省 出工日志/）。
func (m *Module) handleSheetSaveNetdisk(w http.ResponseWriter, r *http.Request) {
	ident, team, ok := m.enterDict(w, r)
	if !ok {
		return
	}
	body, err := decodeBody(r)
	if err != nil {
		fail(w, http.StatusBadRequest, codeInput, "参数错误")
		return
	}
	kind := rawString(body["sheet"])
	if kind != "task" && kind != "fee" && kind != "dispatch" {
		fail(w, http.StatusBadRequest, codeInput, "参数错误：sheet（task/fee/dispatch）")
		return
	}
	q := map[string]string{
		"from": rawString(body["from"]),
		"to":   rawString(body["to"]),
		"date": rawString(body["date"]),
	}
	from, to, errMsg := sheetRange(q)
	if errMsg != "" {
		fail(w, http.StatusBadRequest, codeParam, errMsg)
		return
	}
	if team == nil {
		fail(w, http.StatusBadRequest, codeNoUsableTeam, "无可用班组")
		return
	}
	force := rawBool(body["force"])
	if kind != "dispatch" && m.sheetVerifyGate(w, r, team.ID, from, to, force) {
		return
	}
	var buf []byte
	switch kind {
	case "task":
		var rows []map[string]string
		rows, err = m.loadSheetRows(r, team.ID, from, to)
		if err == nil {
			buf, err = m.buildTaskSheet(team.Name, rows)
		}
	case "fee":
		buf, err = m.buildFeeSheet(r, team.ID, from, to)
	case "dispatch":
		buf, err = m.buildDispatchSheet(r, team.ID, from, to)
	}
	if err != nil {
		writeErr(w, err)
		return
	}
	if len(buf) == 0 {
		fail(w, http.StatusNotFound, codeNoPhotos, "该日期范围没有出车记录，无可生成的内容")
		return
	}
	space := "my"
	if rawString(body["space"]) == "public" {
		space = "public"
	}
	dir := "出工日志"
	if raw, has := body["dir"]; has && string(raw) != "null" {
		dir = rawString(raw)
	}
	path, err := m.saveToNetdisk(r.Context(), ident, NetdiskSaveRequest{
		Space: space, Dir: dir, Name: sheetFileName(kind, from, to), Body: buf,
	})
	if err != nil {
		writeNetdiskErr(w, err)
		return
	}
	writeOK(w, map[string]any{"path": path}, "已保存到网盘")
}

// handleTaskSheet GET /task-sheet?from=&to=：任务单 docx 二进制。
func (m *Module) handleTaskSheet(w http.ResponseWriter, r *http.Request) {
	m.serveSheet(w, r, "task")
}

// handleFeeSheet GET /fee-sheet?from=&to=：费用汇总 docx 二进制。
func (m *Module) handleFeeSheet(w http.ResponseWriter, r *http.Request) {
	m.serveSheet(w, r, "fee")
}

// handleDispatchSheet GET /dispatch-sheet?from=&to=：派车汇总 xlsx 二进制（不核验）。
func (m *Module) handleDispatchSheet(w http.ResponseWriter, r *http.Request) {
	m.serveSheet(w, r, "dispatch")
}

// serveSheet 汇总表下载共用出口（task/fee 生成前核验；force=1 跳过）。
func (m *Module) serveSheet(w http.ResponseWriter, r *http.Request, kind string) {
	_, team, ok := m.enterDict(w, r)
	if !ok {
		return
	}
	q := map[string]string{
		"from": r.URL.Query().Get("from"),
		"to":   r.URL.Query().Get("to"),
		"date": r.URL.Query().Get("date"),
	}
	from, to, errMsg := sheetRange(q)
	if errMsg != "" {
		fail(w, http.StatusBadRequest, codeParam, errMsg)
		return
	}
	if team == nil {
		fail(w, http.StatusBadRequest, codeNoUsableTeam, "无可用班组")
		return
	}
	force := r.URL.Query().Get("force") == "1"
	if kind != "dispatch" && !force {
		if failures := m.sheetVerifyFailures(r, team.ID, from, to); len(failures) > 0 {
			failData(w, http.StatusConflict, codeVerifyFailed,
				"所选范围内有 "+strconv.Itoa(len(failures))+" 条记录存在未通过项，请处理后再操作",
				map[string]any{"failures": failures})
			return
		}
	}

	var buf []byte
	var err error
	var fileName, contentType, ext string
	switch kind {
	case "task":
		var rows []map[string]string
		rows, err = m.loadSheetRows(r, team.ID, from, to)
		if err == nil {
			buf, err = m.buildTaskSheet(team.Name, rows)
		}
		fileName, contentType, ext = "工作任务单", "application/vnd.openxmlformats-officedocument.wordprocessingml.document", "docx"
	case "fee":
		buf, err = m.buildFeeSheet(r, team.ID, from, to)
		fileName, contentType, ext = "费用汇总", "application/vnd.openxmlformats-officedocument.wordprocessingml.document", "docx"
	case "dispatch":
		buf, err = m.buildDispatchSheet(r, team.ID, from, to)
		fileName, contentType, ext = "派车汇总", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet", "xlsx"
	}
	if err != nil {
		writeErr(w, err)
		return
	}
	if len(buf) == 0 {
		msg := "该日期范围没有出车记录，无可生成的卡片"
		if kind == "fee" {
			msg = "该日期范围没有出车记录，无可生成的汇总"
		} else if kind == "dispatch" {
			msg = "该日期范围没有派车单记录，无可生成的汇总"
		}
		fail(w, http.StatusNotFound, codeNoPhotos, msg)
		return
	}
	fullName := fileName + "-" + from + "." + ext
	if from != to {
		fullName = fileName + "-" + from + "至" + to + "." + ext
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Disposition",
		`attachment; filename="`+kind+`-sheet-`+from+`.`+ext+`"; filename*=UTF-8''`+encURI(fullName))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(buf)
}
