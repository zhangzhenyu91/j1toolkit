// 出工日志：卡片全量装配（loadEntries）与视图接口（meta / logs / day-status / stats / report）。
package worklog

import (
	"context"
	"database/sql"
	"net/http"
	"sort"
	"strings"

	"shade/internal/auth"
)

// loadEntries 装配指定条件下卡片全量（用车人、照片、商旅打卡/费用、feeStd、verify 状态）。
func (m *Module) loadEntries(ctx context.Context, where string, params []any) ([]Entry, error) {
	// 超时兜底：pending 超 10 分钟视为验证失败（服务端在 Dify 回调途中重启导致回写丢失的自愈）
	_, _ = m.DB.ExecContext(ctx,
		`UPDATE worklog_photo SET verify_status = 'failed'
		 WHERE verify_status = 'pending' AND created_at < DATE_SUB(NOW(), INTERVAL 10 MINUTE)`)

	rows, err := m.DB.QueryContext(ctx,
		`SELECT e.id, e.team_id, DATE_FORMAT(e.log_date, '%Y-%m-%d'), COALESCE(e.patrol_content, ''),
		        COALESCE(e.remark, ''), COALESCE(e.remark_files, ''), e.cross_team,
		        COALESCE(e.vehicle_id, 0), COALESCE(v.plate_no, ''), COALESCE(e.destination_id, 0),
		        COALESCE(d.name, ''), COALESCE(d.out_of_city, 0), COALESCE(d.stationed, 0),
		        COALESCE(e.dispatch_order_no, ''), e.created_by, DATE_FORMAT(e.created_at, '%Y-%m-%d %H:%i:%s')
		 FROM worklog_entry e
		 LEFT JOIN worklog_vehicle v ON v.id = e.vehicle_id
		 LEFT JOIN worklog_destination d ON d.id = e.destination_id
		 WHERE `+where+` ORDER BY e.created_at, e.id`, params...)
	if err != nil {
		return nil, err
	}
	entries := make([]Entry, 0)
	for rows.Next() {
		var e Entry
		var remarkFiles string
		if err := rows.Scan(&e.ID, &e.TeamID, &e.LogDate, &e.PatrolContent, &e.Remark, &remarkFiles,
			&e.CrossTeam, &e.VehicleID, &e.PlateNo, &e.DestinationID, &e.DestinationName,
			&e.DestinationOutOfCity, &e.DestinationStationed, &e.DispatchOrderNo, &e.CreatedBy, &e.CreatedAt); err != nil {
			rows.Close()
			return nil, err
		}
		e.RemarkFiles = parseRemarkFiles(remarkFiles)
		entries = append(entries, e)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(entries) == 0 {
		return entries, nil
	}

	ids := make([]any, len(entries))
	idSet := map[int64]bool{}
	for i := range entries {
		ids[i] = entries[i].ID
		idSet[entries[i].ID] = true
	}
	ph := placeholders(len(entries))

	// 用车人
	memRows, err := m.DB.QueryContext(ctx,
		`SELECT em.id, em.entry_id, em.member_id, m.name, em.checked, em.sort
		 FROM worklog_entry_member em JOIN worklog_member m ON m.id = em.member_id
		 WHERE em.entry_id IN (`+ph+`) ORDER BY em.sort, em.id`, ids...)
	if err != nil {
		return nil, err
	}
	membersByEntry := map[int64][]MemberRow{}
	memberIDs := map[int64]bool{}
	for memRows.Next() {
		var mr MemberRow
		var entryID int64
		if err := memRows.Scan(&mr.ID, &entryID, &mr.MemberID, &mr.Name, &mr.Checked, &mr.Sort); err == nil {
			membersByEntry[entryID] = append(membersByEntry[entryID], mr)
			memberIDs[mr.MemberID] = true
		}
	}
	memRows.Close()

	// 照片
	photoRows, err := m.DB.QueryContext(ctx,
		`SELECT id, entry_id, cos_key, url, members, verify_status, work_content, shot_time, weather,
		        location, lng, lat, date_ok, dest_ok, is_watermark, source, sgcc_synced
		 FROM worklog_photo WHERE entry_id IN (`+ph+`) ORDER BY id`, ids...)
	if err != nil {
		return nil, err
	}
	photosByEntry := map[int64][]PhotoItem{}
	for photoRows.Next() {
		var p PhotoItem
		var entryID int64
		var cosKey, url, membersJSON string
		if err := photoRows.Scan(&p.ID, &entryID, &cosKey, &url, &membersJSON, &p.VerifyStatus,
			&p.WorkContent, &p.ShotTime, &p.Weather, &p.Location, &p.Lng, &p.Lat,
			&p.DateOK, &p.DestOK, &p.IsWatermark, &p.Source, &p.SgccSynced); err == nil {
			p.URL = m.photoURL(cosKey, url)
			p.Members = parseNames(membersJSON)
			photosByEntry[entryID] = append(photosByEntry[entryID], p)
		}
	}
	photoRows.Close()

	// 商旅绑定 / 打卡 / 费用（规则 a/f/g）
	sgccByMember := map[int64]*int{}
	clockinByDate := map[string]map[string]map[int]ClockinEntry{}
	feeByDate := map[string]map[string]FeeEntry{}
	var feeStdRows []FeeStdRow
	if len(memberIDs) > 0 {
		memAny := make([]any, 0, len(memberIDs))
		for id := range memberIDs {
			memAny = append(memAny, id)
		}
		memPh := placeholders(len(memAny))
		accRows, err := m.DB.QueryContext(ctx,
			`SELECT m.id, a.token_status
			 FROM worklog_member m
			 JOIN worklog_sgcc_account a ON a.member_id = m.id OR (m.user_id IS NOT NULL AND a.user_id = m.user_id)
			 WHERE m.id IN (`+memPh+`)
			 ORDER BY (a.member_id = m.id) DESC`, memAny...)
		if err == nil {
			for accRows.Next() {
				var cardMemberID int64
				var ts int
				if accRows.Scan(&cardMemberID, &ts) == nil {
					if _, ok := sgccByMember[cardMemberID]; !ok {
						v := ts
						sgccByMember[cardMemberID] = &v
					}
				}
			}
			accRows.Close()
		}

		dates := map[string]bool{}
		for i := range entries {
			dates[entries[i].LogDate] = true
		}
		dateAny := make([]any, 0, len(dates))
		for d := range dates {
			dateAny = append(dateAny, d)
		}
		datePh := placeholders(len(dateAny))
		ckRows, err := m.DB.QueryContext(ctx,
			`SELECT member_id, DATE_FORMAT(clock_date, '%Y-%m-%d'), seq, detail_id,
			        COALESCE(DATE_FORMAT(clock_time, '%Y-%m-%d %H:%i:%s'), ''), position, longitude, latitude,
			        city_code, city_name, work_hours
			 FROM worklog_clockin WHERE member_id IN (`+memPh+`) AND clock_date IN (`+datePh+`)`,
			append(append([]any{}, memAny...), dateAny...)...)
		if err == nil {
			for ckRows.Next() {
				var memberID int64
				var clockDate string
				var seq int
				var ce ClockinEntry
				if ckRows.Scan(&memberID, &clockDate, &seq, &ce.DetailID, &ce.Time, &ce.Position,
					&ce.Lng, &ce.Lat, &ce.CityCode, &ce.CityName, &ce.WorkHours) == nil {
					d := clockinByDate[clockDate]
					if d == nil {
						d = map[string]map[int]ClockinEntry{}
						clockinByDate[clockDate] = d
					}
					k := itoa(memberID)
					if d[k] == nil {
						d[k] = map[int]ClockinEntry{}
					}
					d[k][seq] = ce
				}
			}
			ckRows.Close()
		}
		feeRows, err := m.DB.QueryContext(ctx,
			`SELECT member_id, DATE_FORMAT(fee_date, '%Y-%m-%d'), food_fee, transit_fee
			 FROM worklog_fee WHERE member_id IN (`+memPh+`) AND fee_date IN (`+datePh+`)`,
			append(append([]any{}, memAny...), dateAny...)...)
		if err == nil {
			for feeRows.Next() {
				var memberID int64
				var feeDate string
				var fe FeeEntry
				if feeRows.Scan(&memberID, &feeDate, &fe.FoodFee, &fe.TransitFee) == nil {
					d := feeByDate[feeDate]
					if d == nil {
						d = map[string]FeeEntry{}
						feeByDate[feeDate] = d
					}
					d[itoa(memberID)] = fe
				}
			}
			feeRows.Close()
		}
		stdRows, err := m.DB.QueryContext(ctx,
			`SELECT scope, food_fee, transit_fee, DATE_FORMAT(effective_from, '%Y-%m-%d') FROM worklog_fee_std`)
		if err == nil {
			for stdRows.Next() {
				var r FeeStdRow
				if stdRows.Scan(&r.Scope, &r.FoodFee, &r.TransitFee, &r.Eff) == nil {
					feeStdRows = append(feeStdRows, r)
				}
			}
			stdRows.Close()
		}
	}

	for i := range entries {
		e := &entries[i]
		e.Members = membersByEntry[e.ID]
		if e.Members == nil {
			e.Members = []MemberRow{}
		}
		for j := range e.Members {
			if ts, ok := sgccByMember[e.Members[j].MemberID]; ok {
				e.Members[j].SgccBound = true
				e.Members[j].SgccTokenStat = ts
			}
		}
		e.Photos = photosByEntry[e.ID]
		if e.Photos == nil {
			e.Photos = []PhotoItem{}
		}
		for j := range e.RemarkFiles {
			if e.RemarkFiles[j].CosKey != "" && m.Store != nil {
				e.RemarkFiles[j].URL = m.Store.PublicURL(e.RemarkFiles[j].CosKey)
			}
		}
		e.ClockinMap = clockinByDate[e.LogDate]
		if e.ClockinMap == nil {
			e.ClockinMap = map[string]map[int]ClockinEntry{}
		}
		e.FeeMap = feeByDate[e.LogDate]
		if e.FeeMap == nil {
			e.FeeMap = map[string]FeeEntry{}
		}
		scope := 0
		switch {
		case e.DestinationStationed != 0:
			scope = 2
		case e.DestinationOutOfCity != 0:
			scope = 1
		}
		e.FeeStd = ResolveFeeStd(feeStdRows, scope, e.LogDate)
		e.VerifyPassed = ComputeVerifyPassed(e)
		e.VerifyReasons = ComputeFailReasons(e)
	}
	return entries, nil
}

// parseRemarkFiles 备注附件 JSON 解析（兼容字符串/对象回传；与 Node 同口径宽松解析，读侧不丢弃历史条目）。
func parseRemarkFiles(raw string) []RemarkFile {
	if strings.TrimSpace(raw) == "" {
		return []RemarkFile{}
	}
	out := make([]RemarkFile, 0)
	// 逐个对象宽松解析：[{...},{...}]
	for _, seg := range splitJSONObjects(raw) {
		var f RemarkFile
		if err := jsonUnmarshal([]byte(seg), &f); err == nil {
			out = append(out, f)
		}
	}
	if out == nil {
		return []RemarkFile{}
	}
	return out
}

// myMember 当前登录用户对应的出工成员（按昵称班组内匹配，个人视图用）。
func (m *Module) myMember(ctx context.Context, ident auth.Identity, teamID int64) (MemberRow, bool) {
	nickname := strings.TrimSpace(ident.Nickname)
	if nickname == "" || teamID == 0 {
		return MemberRow{}, false
	}
	var mr MemberRow
	err := m.DB.QueryRowContext(ctx,
		`SELECT id, name FROM worklog_member WHERE name = ? AND team_id = ?`, nickname, teamID).
		Scan(&mr.ID, &mr.Name)
	if err != nil {
		return MemberRow{}, false
	}
	mr.MemberID = mr.ID
	return mr, true
}

// handleMeta GET /meta：下拉/点亮数据源；按生效班组出数。
func (m *Module) handleMeta(w http.ResponseWriter, r *http.Request) {
	_, team, ok := m.enter(w, r)
	if !ok {
		return
	}
	if team == nil {
		writeOK(w, map[string]any{"vehicles": []any{}, "destinations": []any{}, "members": []any{}})
		return
	}
	ctx := r.Context()
	vehicles := queryDictRows(ctx, m.DB,
		`SELECT id, plate_no FROM worklog_vehicle WHERE status = 1 AND team_id = ? ORDER BY sort, id`, team.ID)
	destinations := queryDictRows(ctx, m.DB,
		`SELECT id, name FROM worklog_destination WHERE status = 1 AND team_id = ? ORDER BY sort, id`, team.ID)
	members := queryDictRows(ctx, m.DB,
		`SELECT id, name, sort FROM worklog_member WHERE status = 1 AND team_id = ? ORDER BY sort, id`, team.ID)
	writeOK(w, map[string]any{"vehicles": vehicles, "destinations": destinations, "members": members})
}

// handleLogsList GET /logs?date=YYYY-MM-DD&scope=all|mine：某日卡片全量。
func (m *Module) handleLogsList(w http.ResponseWriter, r *http.Request) {
	ident, team, ok := m.enter(w, r)
	if !ok {
		return
	}
	date := r.URL.Query().Get("date")
	if !dateRe.MatchString(date) {
		fail(w, http.StatusBadRequest, codeParam, "日期格式应为 YYYY-MM-DD")
		return
	}
	if team == nil {
		writeOK(w, map[string]any{"list": []any{}})
		return
	}
	where := `e.log_date = ? AND ` + entryVisibleSQL
	params := []any{date, team.ID, team.ID}
	if r.URL.Query().Get("scope") == "mine" {
		me, found := m.myMember(r.Context(), ident, team.ID)
		if !found {
			writeOK(w, map[string]any{"list": []any{}})
			return
		}
		where += ` AND e.id IN (SELECT entry_id FROM worklog_entry_member WHERE member_id = ?)`
		params = append(params, me.MemberID)
	}
	list, err := m.loadEntries(r.Context(), where, params)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeOK(w, map[string]any{"list": list})
}

// handleDayStatus GET /day-status?month=YYYY-MM&scope=all|mine：当月每日验证状态映射（failed 红 > remark 黄 > passed 绿）。
func (m *Module) handleDayStatus(w http.ResponseWriter, r *http.Request) {
	ident, team, ok := m.enter(w, r)
	if !ok {
		return
	}
	month := r.URL.Query().Get("month")
	if !monthRe.MatchString(month) {
		fail(w, http.StatusBadRequest, codeParam, "月份格式应为 YYYY-MM")
		return
	}
	if team == nil {
		writeOK(w, map[string]any{"map": map[string]string{}})
		return
	}
	ctx := r.Context()
	statusMap := map[string]string{}

	if r.URL.Query().Get("scope") == "mine" {
		me, found := m.myMember(ctx, ident, team.ID)
		if !found {
			writeOK(w, map[string]any{"map": statusMap})
			return
		}
		list, err := m.loadEntries(ctx,
			`DATE_FORMAT(e.log_date, '%Y-%m') = ? AND `+entryVisibleSQL+` AND e.id IN (SELECT entry_id FROM worklog_entry_member WHERE member_id = ?)`,
			[]any{month, team.ID, team.ID, me.MemberID})
		if err != nil {
			writeErr(w, err)
			return
		}
		for i := range list {
			e := &list[i]
			if statusMap[e.LogDate] == "failed" {
				continue
			}
			st := "passed"
			if len(MyReportReasons(e, me)) > 0 {
				st = "failed"
			}
			if st == "passed" && (e.Remark != "" || len(e.RemarkFiles) > 0) {
				st = "remark"
			}
			if st == "passed" && statusMap[e.LogDate] == "remark" {
				continue
			}
			statusMap[e.LogDate] = st
		}
		writeOK(w, map[string]any{"map": statusMap})
		return
	}

	list, err := m.loadEntries(ctx, `DATE_FORMAT(e.log_date, '%Y-%m') = ? AND `+entryVisibleSQL,
		[]any{month, team.ID, team.ID})
	if err != nil {
		writeErr(w, err)
		return
	}
	for i := range list {
		e := &list[i]
		if e.VerifyPassed == "exempt" {
			continue
		}
		if statusMap[e.LogDate] == "failed" {
			continue
		}
		st := "passed"
		if e.VerifyPassed == "failed" {
			st = "failed"
		}
		if st == "passed" && (e.Remark != "" || len(e.RemarkFiles) > 0) {
			st = "remark"
		}
		if st == "passed" && statusMap[e.LogDate] == "remark" {
			continue
		}
		statusMap[e.LogDate] = st
	}
	writeOK(w, map[string]any{"map": statusMap})
}

// handleStats GET /stats?month=YYYY-MM：当月卡片「通过/全部」+ 我当月缺水印照片条数。
func (m *Module) handleStats(w http.ResponseWriter, r *http.Request) {
	ident, team, ok := m.enter(w, r)
	if !ok {
		return
	}
	month := r.URL.Query().Get("month")
	if !monthRe.MatchString(month) {
		fail(w, http.StatusBadRequest, codeParam, "月份格式应为 YYYY-MM")
		return
	}
	if team == nil {
		writeOK(w, map[string]any{"total": 0, "passed": 0, "needPhotos": 0})
		return
	}
	ctx := r.Context()
	list, err := m.loadEntries(ctx, `DATE_FORMAT(e.log_date, '%Y-%m') = ? AND `+entryVisibleSQL,
		[]any{month, team.ID, team.ID})
	if err != nil {
		writeErr(w, err)
		return
	}
	total := len(list)
	passed := 0
	for i := range list {
		if list[i].VerifyPassed == "passed" {
			passed++
		}
	}
	needPhotos := 0
	if me, found := m.myMember(ctx, ident, team.ID); found {
		for i := range list {
			e := &list[i]
			amMember := false
			for _, mem := range e.Members {
				if mem.MemberID == me.MemberID {
					amMember = true
				}
			}
			if !amMember {
				continue
			}
			for _, reason := range MyReportReasons(e, me) {
				if reason == "我未上传水印照片" {
					needPhotos++
					break
				}
			}
		}
	}
	writeOK(w, map[string]any{"total": total, "passed": passed, "needPhotos": needPhotos})
}

// handleReport GET /report?from=&to=&scope=all|mine：验证报告（不通过记录 ∪ 有备注的记录）。
func (m *Module) handleReport(w http.ResponseWriter, r *http.Request) {
	ident, team, ok := m.enter(w, r)
	if !ok {
		return
	}
	from, to := r.URL.Query().Get("from"), r.URL.Query().Get("to")
	if !dateRe.MatchString(from) || !dateRe.MatchString(to) {
		fail(w, http.StatusBadRequest, codeParam, "日期格式应为 YYYY-MM-DD")
		return
	}
	if from > to {
		fail(w, http.StatusBadRequest, codeDateRange, "开始日期不能晚于结束日期")
		return
	}
	if team == nil {
		writeOK(w, map[string]any{"list": []any{}, "total": 0})
		return
	}
	ctx := r.Context()
	var me MemberRow
	meFound := false
	where := `e.log_date BETWEEN ? AND ? AND e.team_id = ?`
	params := []any{from, to, team.ID}
	if r.URL.Query().Get("scope") == "mine" {
		me, meFound = m.myMember(ctx, ident, team.ID)
		if !meFound {
			writeOK(w, map[string]any{"list": []any{}, "total": 0})
			return
		}
		where += ` AND e.id IN (SELECT entry_id FROM worklog_entry_member WHERE member_id = ?)`
		params = append(params, me.MemberID)
	}
	list, err := m.loadEntries(ctx, where, params)
	if err != nil {
		writeErr(w, err)
		return
	}
	type reportItem struct {
		ID             int64    `json:"id"`
		LogDate        string   `json:"log_date"`
		PlateNo        string   `json:"plate_no"`
		Members        []string `json:"members"`
		Verify         string   `json:"verify"`
		Reasons        []string `json:"reasons"`
		Remark         string   `json:"remark"`
		RemarkHasFiles bool     `json:"remark_has_files"`
	}
	items := make([]reportItem, 0)
	for i := range list {
		e := &list[i]
		hasRemark := e.Remark != "" || len(e.RemarkFiles) > 0
		var reasons []string
		amMember := false
		for _, mem := range e.Members {
			if meFound && mem.MemberID == me.MemberID {
				amMember = true
			}
		}
		if meFound {
			reasons = MyReportReasons(e, me)
			if len(reasons) == 0 && !(amMember && hasRemark) {
				continue
			}
		} else {
			if e.VerifyPassed == "failed" {
				reasons = e.VerifyReasons
			}
			if len(reasons) == 0 && !hasRemark {
				continue
			}
		}
		if reasons == nil {
			reasons = []string{}
		}
		plate := e.PlateNo
		if plate == "" {
			plate = "未出车"
		}
		names := make([]string, 0, len(e.Members))
		for _, mem := range e.Members {
			names = append(names, mem.Name)
		}
		items = append(items, reportItem{
			ID: e.ID, LogDate: e.LogDate, PlateNo: plate, Members: names,
			Verify: e.VerifyPassed, Reasons: reasons, Remark: e.Remark,
			RemarkHasFiles: len(e.RemarkFiles) > 0,
		})
	}
	// 报告清单按日期 + 创建序排列（Node 同口径；loadEntries 为 created_at 序，范围查询需重排）
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].LogDate != items[j].LogDate {
			return items[i].LogDate < items[j].LogDate
		}
		return items[i].ID < items[j].ID
	})
	writeOK(w, map[string]any{"list": items, "total": len(list)})
}

// queryDictRows 通用字典行查询（列名作键）。
// 按语句实际占位符传参：每个 ? 传一个 teamID，无占位符不传参——
// 真 MySQL 对参数个数敏感，多传会直接报「expected 0 arguments got 1」导致整块返回空。
func queryDictRows(ctx context.Context, db *sql.DB, query string, teamID int64) []map[string]any {
	n := sqlPlaceholderCount(query)
	args := make([]any, n)
	for i := range args {
		args[i] = teamID
	}
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return []map[string]any{}
	}
	defer rows.Close()
	cols, _ := rows.Columns()
	out := make([]map[string]any, 0)
	for rows.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if rows.Scan(ptrs...) != nil {
			continue
		}
		item := map[string]any{}
		for i, c := range cols {
			item[c] = normalizeSQLValue(vals[i])
		}
		out = append(out, item)
	}
	return out
}

// sqlPlaceholderCount 统计 SQL 语句的 ? 占位符个数（跳过单引号字符串与反引号标识符）。
func sqlPlaceholderCount(query string) int {
	n := 0
	var quote byte
	for i := 0; i < len(query); i++ {
		c := query[i]
		if quote != 0 {
			if quote == '\'' && c == '\\' {
				i++
				continue
			}
			if c == quote {
				quote = 0
			}
			continue
		}
		switch c {
		case '\'', '`':
			quote = c
		case '?':
			n++
		}
	}
	return n
}
