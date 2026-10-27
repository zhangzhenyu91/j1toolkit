// 跨班日志（仅超级管理员）：用车人可跨班组；卡片在用车人所在各班组展示并可如常互动，
// 但汇总类功能仅参与归属班组；仅超管可删除、修改派车。
package worklog

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
)

// crossBody 跨班表单：归属班组 + 车牌/目的地/成员名（按名称精确命中）。
type crossBody struct {
	OwnerID     int64
	Plate       string
	DestName    string
	MemberNames []string
	Patrol      string
	OrderNo     string
}

// parseCrossBody 解析跨班表单；失败已写响应并返回 nil。
func parseCrossBody(w http.ResponseWriter, body map[string]json.RawMessage) *crossBody {
	p := &crossBody{
		OwnerID:  rawNumberOr0(body["team_id"]),
		Plate:    truncate(strings.TrimSpace(rawString(body["plate_no"])), 32),
		DestName: truncate(strings.TrimSpace(rawString(body["destination"])), 64),
		Patrol:   truncate(strings.TrimSpace(rawString(body["patrol_content"])), 500),
	}
	if p.OwnerID == 0 {
		p.OwnerID = rawNumberOr0(body["owner_team_id"])
	}
	if raw, exists := body["member_names"]; exists {
		var names []string
		if err := jsonUnmarshal(raw, &names); err != nil {
			fail(w, http.StatusBadRequest, codeInput, "参数错误")
			return nil
		}
		seen := map[string]bool{}
		for _, n := range names {
			n = strings.TrimSpace(n)
			if n != "" && !seen[n] {
				seen[n] = true
				p.MemberNames = append(p.MemberNames, n)
			}
		}
	}
	if p.Plate != "" {
		p.OrderNo = truncate(strings.TrimSpace(rawString(body["dispatch_order_no"])), 64)
	}
	if p.Plate == "" && (p.DestName != "" || len(p.MemberNames) > 0) {
		fail(w, http.StatusBadRequest, codeInput, "未出车时不可填写目的地与用车人")
		return nil
	}
	if p.OwnerID <= 0 {
		fail(w, http.StatusBadRequest, codeNoTeam, "归属班组无效或已停用")
		return nil
	}
	return p
}

// resolveOwnerDict 跨班字典解析：归属班字典按名称精确命中（含停用则重新启用），未命中自动补建。
func (m *Module) resolveOwnerDict(r *http.Request, table, field string, teamID int64, name string) (int64, error) {
	var id int64
	var status int
	err := m.DB.QueryRowContext(r.Context(),
		`SELECT id, status FROM `+table+` WHERE team_id = ? AND `+field+` = ?`, teamID, name).Scan(&id, &status)
	if err == nil {
		if status != 1 {
			_, _ = m.DB.ExecContext(r.Context(), `UPDATE `+table+` SET status = 1 WHERE id = ?`, id)
		}
		return id, nil
	}
	var maxSort int
	_ = m.DB.QueryRowContext(r.Context(),
		`SELECT COALESCE(MAX(sort), 0) FROM `+table+` WHERE team_id = ?`, teamID).Scan(&maxSort)
	res, err := m.DB.ExecContext(r.Context(),
		`INSERT INTO `+table+` (`+field+`, team_id, sort) VALUES (?, ?, ?)`, name, teamID, maxSort+1)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// crossMember 跨班成员解析结果行。
type crossMember struct {
	ID     int64
	Name   string
	Sort   int
	TeamID int64
	UserID int64
}

// resolveCrossMembers 全部启用班组启用成员中按名取候选，优先 归属班 > 已绑账号 > id 最小。
func (m *Module) resolveCrossMembers(r *http.Request, names []string, ownerTeamID int64) (rows []crossMember, missing []string) {
	if len(names) == 0 {
		return nil, nil
	}
	qrows, err := m.DB.QueryContext(r.Context(),
		`SELECT m.id, m.name, m.sort, m.team_id, COALESCE(m.user_id, 0)
		 FROM worklog_member m JOIN sys_team t ON t.id = m.team_id AND t.status = 1
		 WHERE m.status = 1 AND m.name IN (`+placeholders(len(names))+`)`, strsToAny(names)...)
	if err != nil {
		return nil, names
	}
	defer qrows.Close()
	byName := map[string][]crossMember{}
	for qrows.Next() {
		var cm crossMember
		if qrows.Scan(&cm.ID, &cm.Name, &cm.Sort, &cm.TeamID, &cm.UserID) == nil {
			byName[cm.Name] = append(byName[cm.Name], cm)
		}
	}
	out := make([]crossMember, 0, len(names))
	for _, n := range names {
		cands := byName[n]
		if len(cands) == 0 {
			missing = append(missing, n)
			continue
		}
		best := cands[0]
		for _, c := range cands[1:] {
			if betterCrossCandidate(c, best, ownerTeamID) {
				best = c
			}
		}
		out = append(out, best)
	}
	if len(missing) > 0 {
		return nil, missing
	}
	return out, nil
}

// betterCrossCandidate 优先级：归属班 > 已绑账号 > id 最小。
func betterCrossCandidate(a, b crossMember, ownerTeamID int64) bool {
	aOwner, bOwner := a.TeamID == ownerTeamID, b.TeamID == ownerTeamID
	if aOwner != bOwner {
		return aOwner
	}
	aBound, bBound := a.UserID != 0, b.UserID != 0
	if aBound != bBound {
		return aBound
	}
	return a.ID < b.ID
}

// handleCrossMeta GET /logs/cross/meta：跨班表单数据源（全部启用班组字典按名称去重合并）。
func (m *Module) handleCrossMeta(w http.ResponseWriter, r *http.Request) {
	ident, _, ok := m.enter(w, r)
	if !ok {
		return
	}
	if ident.Role != "admin" {
		fail(w, http.StatusForbidden, codeForbidden, "仅超级管理员可执行此操作")
		return
	}
	teams := queryDictRows(r.Context(), m.DB,
		`SELECT id, name FROM sys_team WHERE status = 1 ORDER BY sort, id`, 0)
	vehicles := dedupByName(queryDictRows(r.Context(), m.DB,
		`SELECT v.plate_no AS name, t.name AS team_name FROM worklog_vehicle v
		 JOIN sys_team t ON t.id = v.team_id AND t.status = 1 WHERE v.status = 1 ORDER BY v.plate_no, v.id`, 0))
	destinations := dedupByName(queryDictRows(r.Context(), m.DB,
		`SELECT d.name AS name, t.name AS team_name FROM worklog_destination d
		 JOIN sys_team t ON t.id = d.team_id AND t.status = 1 WHERE d.status = 1 ORDER BY d.name, d.id`, 0))
	members := dedupByName(queryDictRows(r.Context(), m.DB,
		`SELECT m.name AS name, t.name AS team_name FROM worklog_member m
		 JOIN sys_team t ON t.id = m.team_id AND t.status = 1 WHERE m.status = 1 ORDER BY m.name, m.id`, 0))
	writeOK(w, map[string]any{
		"teams": teams, "vehicles": vehicles, "destinations": destinations, "members": members,
	})
}

// dedupByName 按名称去重合并来源班组（保持先见顺序）。
func dedupByName(rows []map[string]any) []map[string]any {
	type item struct {
		name  string
		teams []string
	}
	order := make([]string, 0)
	byName := map[string]*item{}
	for _, row := range rows {
		name, _ := row["name"].(string)
		teamName, _ := row["team_name"].(string)
		if name == "" {
			continue
		}
		it := byName[name]
		if it == nil {
			it = &item{name: name}
			byName[name] = it
			order = append(order, name)
		}
		dup := false
		for _, t := range it.teams {
			if t == teamName {
				dup = true
			}
		}
		if !dup {
			it.teams = append(it.teams, teamName)
		}
	}
	out := make([]map[string]any, 0, len(order))
	for _, n := range order {
		out = append(out, map[string]any{"name": byName[n].name, "teams": byName[n].teams})
	}
	return out
}

// handleCrossCreate POST /logs/cross：新建跨班日志（字典缺则自动补建；跨班标志按成员构成派生）。
func (m *Module) handleCrossCreate(w http.ResponseWriter, r *http.Request) {
	ident, _, ok := m.enter(w, r)
	if !ok {
		return
	}
	if ident.Role != "admin" {
		fail(w, http.StatusForbidden, codeForbidden, "仅超级管理员可执行此操作")
		return
	}
	body, err := decodeBody(r)
	if err != nil {
		fail(w, http.StatusBadRequest, codeInput, "参数错误")
		return
	}
	logDate := rawString(body["log_date"])
	if !dateRe.MatchString(logDate) {
		fail(w, http.StatusBadRequest, codeParam, "日期格式应为 YYYY-MM-DD")
		return
	}
	parsed := parseCrossBody(w, body)
	if parsed == nil {
		return
	}
	var ownerName string
	var ownerStatus int
	if err := m.DB.QueryRowContext(r.Context(),
		`SELECT name, status FROM sys_team WHERE id = ?`, parsed.OwnerID).Scan(&ownerName, &ownerStatus); err != nil || ownerStatus != 1 {
		fail(w, http.StatusBadRequest, codeNoTeam, "归属班组无效或已停用")
		return
	}

	var vehicleID, destID int64
	memberRows := make([]crossMember, 0)
	if parsed.Plate != "" {
		vehicleID, err = m.resolveOwnerDict(r, "worklog_vehicle", "plate_no", parsed.OwnerID, parsed.Plate)
		if err != nil {
			writeErr(w, err)
			return
		}
		if parsed.DestName != "" {
			destID, err = m.resolveOwnerDict(r, "worklog_destination", "name", parsed.OwnerID, parsed.DestName)
			if err != nil {
				writeErr(w, err)
				return
			}
		}
		var missing []string
		memberRows, missing = m.resolveCrossMembers(r, parsed.MemberNames, parsed.OwnerID)
		if len(missing) > 0 {
			fail(w, http.StatusBadRequest, codeMember, "成员「"+strings.Join(missing, "、")+"」不存在或已停用")
			return
		}
		mids := make([]int64, 0, len(memberRows))
		for _, mr := range memberRows {
			mids = append(mids, mr.ID)
		}
		if m.failIfDateMemberDup(w, r, logDate, mids, 0) {
			return
		}
	}
	crossTeam := 0
	for _, mr := range memberRows {
		if mr.TeamID != parsed.OwnerID {
			crossTeam = 1
		}
	}
	orderVal := any(nil)
	if vehicleID != 0 && parsed.OrderNo != "" {
		orderVal = parsed.OrderNo
	}
	tx, err := m.DB.BeginTx(r.Context(), nil)
	if err != nil {
		writeErr(w, err)
		return
	}
	res, err := tx.ExecContext(r.Context(),
		`INSERT INTO worklog_entry (team_id, log_date, patrol_content, vehicle_id, destination_id, cross_team, dispatch_order_no, created_by)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		parsed.OwnerID, logDate, parsed.Patrol, nullIfZero(vehicleID), nullIfZero(destID), crossTeam, orderVal, ident.ID)
	if err != nil {
		tx.Rollback()
		writeErr(w, err)
		return
	}
	entryID, _ := res.LastInsertId()
	for _, mr := range memberRows {
		if _, err := tx.ExecContext(r.Context(),
			`INSERT INTO worklog_entry_member (entry_id, member_id, sort) VALUES (?, ?, ?)`,
			entryID, mr.ID, mr.Sort); err != nil {
			tx.Rollback()
			writeErr(w, err)
			return
		}
	}
	if err := tx.Commit(); err != nil {
		writeErr(w, err)
		return
	}
	writeOK(w, map[string]any{"id": entryID})
}

// handleCrossUpdate PUT /logs/cross/{id}：修改跨班日志派车（不改日期；保存后重算 cross_team）。
func (m *Module) handleCrossUpdate(w http.ResponseWriter, r *http.Request) {
	ident, _, ok := m.enter(w, r)
	if !ok {
		return
	}
	if ident.Role != "admin" {
		fail(w, http.StatusForbidden, codeForbidden, "仅超级管理员可执行此操作")
		return
	}
	entryID, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	var ownerTeamID, vehicleID, destID int64
	var crossTeam int
	var logDate string
	err := m.DB.QueryRowContext(r.Context(),
		`SELECT team_id, COALESCE(vehicle_id, 0), COALESCE(destination_id, 0), cross_team,
		        DATE_FORMAT(log_date, '%Y-%m-%d')
		 FROM worklog_entry WHERE id = ?`, entryID).
		Scan(&ownerTeamID, &vehicleID, &destID, &crossTeam, &logDate)
	if err != nil {
		fail(w, http.StatusNotFound, codeMissing, "日志不存在")
		return
	}
	if crossTeam != 1 {
		fail(w, http.StatusBadRequest, codeParam, "该日志非跨班日志，请使用普通编辑")
		return
	}
	body, err := decodeBody(r)
	if err != nil {
		fail(w, http.StatusBadRequest, codeInput, "参数错误")
		return
	}
	parsed := parseCrossBody(w, body)
	if parsed == nil {
		return
	}
	var ownerStatus int
	if err := m.DB.QueryRowContext(r.Context(),
		`SELECT status FROM sys_team WHERE id = ?`, parsed.OwnerID).Scan(&ownerStatus); err != nil || ownerStatus != 1 {
		fail(w, http.StatusBadRequest, codeNoTeam, "归属班组无效或已停用")
		return
	}
	if parsed.Plate == "" {
		var photoCnt int
		_ = m.DB.QueryRowContext(r.Context(),
			`SELECT COUNT(*) FROM worklog_photo WHERE entry_id = ?`, entryID).Scan(&photoCnt)
		if photoCnt > 0 {
			fail(w, http.StatusBadRequest, codeHasPhoto, "存在水印照片，不可改为未出车，请先删除照片")
			return
		}
	}
	var newVehicleID, newDestID int64
	memberRows := make([]crossMember, 0)
	if parsed.Plate != "" {
		newVehicleID, err = m.resolveOwnerDict(r, "worklog_vehicle", "plate_no", parsed.OwnerID, parsed.Plate)
		if err != nil {
			writeErr(w, err)
			return
		}
		if parsed.DestName != "" {
			newDestID, err = m.resolveOwnerDict(r, "worklog_destination", "name", parsed.OwnerID, parsed.DestName)
			if err != nil {
				writeErr(w, err)
				return
			}
		}
		var missing []string
		memberRows, missing = m.resolveCrossMembers(r, parsed.MemberNames, parsed.OwnerID)
		if len(missing) > 0 {
			fail(w, http.StatusBadRequest, codeMember, "成员「"+strings.Join(missing, "、")+"」不存在或已停用")
			return
		}
		mids := make([]int64, 0, len(memberRows))
		for _, mr := range memberRows {
			mids = append(mids, mr.ID)
		}
		if m.failIfDateMemberDup(w, r, logDate, mids, entryID) {
			return
		}
	}
	// 被移出名单的成员若已有照片，拒绝
	keepNames := map[string]bool{}
	for _, mr := range memberRows {
		keepNames[mr.Name] = true
	}
	if m.memberHasPhotoOutside(w, r, entryID, keepNames) {
		return
	}

	newCross := 0
	for _, mr := range memberRows {
		if mr.TeamID != parsed.OwnerID {
			newCross = 1
		}
	}
	tx, err := m.DB.BeginTx(r.Context(), nil)
	if err != nil {
		writeErr(w, err)
		return
	}
	if _, err := tx.ExecContext(r.Context(),
		`UPDATE worklog_entry SET team_id = ?, patrol_content = ?, vehicle_id = ?, destination_id = ?, cross_team = ? WHERE id = ?`,
		parsed.OwnerID, parsed.Patrol, nullIfZero(newVehicleID), nullIfZero(newDestID), newCross, entryID); err != nil {
		tx.Rollback()
		writeErr(w, err)
		return
	}
	// 目的地变更联动照片地点核验（同 PUT /logs/{id} 口径）
	if destID != newDestID {
		destName := parsed.DestName
		prows, err := tx.QueryContext(r.Context(),
			`SELECT id, date_ok, location FROM worklog_photo WHERE entry_id = ? AND verify_status IN ('passed', 'mismatch')`,
			entryID)
		if err == nil {
			type pw struct {
				id     int64
				dateOK *int
				loc    string
			}
			list := make([]pw, 0)
			for prows.Next() {
				var p pw
				if prows.Scan(&p.id, &p.dateOK, &p.loc) == nil {
					list = append(list, p)
				}
			}
			prows.Close()
			for _, p := range list {
				destOK := destName == "" || strings.Contains(p.loc, destName)
				dateOK := p.dateOK == nil || *p.dateOK != 0
				status := "mismatch"
				if dateOK && destOK {
					status = "passed"
				}
				_, _ = tx.ExecContext(r.Context(),
					`UPDATE worklog_photo SET dest_ok = ?, verify_status = ? WHERE id = ?`,
					boolToInt(destOK), status, p.id)
			}
		}
	}
	crows, err := tx.QueryContext(r.Context(),
		`SELECT member_id, checked FROM worklog_entry_member WHERE entry_id = ?`, entryID)
	checkedMap := map[int64]int{}
	if err == nil {
		for crows.Next() {
			var id int64
			var checked int
			if crows.Scan(&id, &checked) == nil {
				checkedMap[id] = checked
			}
		}
		crows.Close()
	}
	if _, err := tx.ExecContext(r.Context(),
		`DELETE FROM worklog_entry_member WHERE entry_id = ?`, entryID); err != nil {
		tx.Rollback()
		writeErr(w, err)
		return
	}
	for _, mr := range memberRows {
		if _, err := tx.ExecContext(r.Context(),
			`INSERT INTO worklog_entry_member (entry_id, member_id, checked, sort) VALUES (?, ?, ?, ?)`,
			entryID, mr.ID, checkedMap[mr.ID], mr.Sort); err != nil {
			tx.Rollback()
			writeErr(w, err)
			return
		}
	}
	if err := tx.Commit(); err != nil {
		writeErr(w, err)
		return
	}
	writeOK(w, map[string]any{"id": entryID})
}
