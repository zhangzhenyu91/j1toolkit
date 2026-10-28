// 出工日志：卡片 CRUD（POST /logs、PUT /logs/{id}、DELETE /logs/{id}、打卡切换）与备注更新。
package worklog

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
)

// validDictID 校验字典 id 归属班组（needEnabled 时须启用）。
func (m *Module) validDictID(r *http.Request, table string, id int64, needEnabled bool, teamID int64) bool {
	if id == 0 {
		return true
	}
	var status int
	err := m.DB.QueryRowContext(r.Context(),
		`SELECT status FROM `+table+` WHERE id = ? AND team_id = ?`, id, teamID).Scan(&status)
	if err != nil {
		return false
	}
	return !needEnabled || status == 1
}

// failIfDateMemberDup 同日期同人唯一（一个成员同日只能作为一张出车卡片的用车人；全局口径）。
// 有重复时写 40020 响应并返回 true。
func (m *Module) failIfDateMemberDup(w http.ResponseWriter, r *http.Request, logDate string, memberIDs []int64, excludeEntryID int64) bool {
	if len(memberIDs) == 0 {
		return false
	}
	cond := `e.log_date = ?`
	params := []any{logDate}
	if excludeEntryID > 0 {
		cond += ` AND e.id <> ?`
		params = append(params, excludeEntryID)
	}
	params = append(params, idsToAny(memberIDs)...)
	rows, err := m.DB.QueryContext(r.Context(),
		`SELECT m.name FROM worklog_entry_member em
		 JOIN worklog_entry e ON e.id = em.entry_id
		 JOIN worklog_member m ON m.id = em.member_id
		 WHERE `+cond+` AND em.member_id IN (`+placeholders(len(memberIDs))+`)`, params...)
	if err != nil {
		writeErr(w, err)
		return true
	}
	defer rows.Close()
	names := make([]string, 0)
	for rows.Next() {
		var name string
		if rows.Scan(&name) == nil {
			names = append(names, name)
		}
	}
	if len(names) == 0 {
		return false
	}
	fail(w, http.StatusBadRequest, codeFileCount,
		"「"+strings.Join(names, "、")+"」当日已是其他出车卡片的用车人，同一人同日只能挂一张卡片")
	return true
}

// loadMemberRows 取启用成员的 id+sort（校验数量与去重后一致）。
func (m *Module) loadMemberRows(r *http.Request, memberIDs []int64, teamID int64) (map[int64]int, bool) {
	out := map[int64]int{}
	if len(memberIDs) == 0 {
		return out, true
	}
	ids := dedupIDs(memberIDs)
	rows, err := m.DB.QueryContext(r.Context(),
		`SELECT id, sort FROM worklog_member WHERE id IN (`+placeholders(len(ids))+`) AND status = 1 AND team_id = ?`,
		append(idsToAny(ids), teamID)...)
	if err != nil {
		return nil, false
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var sort int
		if rows.Scan(&id, &sort) == nil {
			out[id] = sort
		}
	}
	return out, len(out) == len(ids)
}

// handleLogCreate POST /logs：新建卡片（一卡片一派车；vehicle_id 空=未出车）。
func (m *Module) handleLogCreate(w http.ResponseWriter, r *http.Request) {
	ident, team, ok := m.enter(w, r)
	if !ok {
		return
	}
	if team == nil {
		fail(w, http.StatusForbidden, codeNoTeam, "未分配班组，请联系管理员分配")
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
	vehicleID := rawNumberOr0(body["vehicle_id"])
	destID := rawNumberOr0(body["destination_id"])
	patrol := truncate(strings.TrimSpace(rawString(body["patrol_content"])), 500)
	orderNo := truncate(strings.TrimSpace(rawString(body["dispatch_order_no"])), 64)
	memberIDs := parseIDList(body["member_ids"])

	if vehicleID == 0 {
		if destID != 0 || len(memberIDs) > 0 {
			fail(w, http.StatusBadRequest, codeInput, "未出车时不可填写目的地与用车人")
			return
		}
	} else {
		if !m.validDictID(r, "worklog_vehicle", vehicleID, true, team.ID) {
			fail(w, http.StatusBadRequest, codeVehicle, "车牌号无效或已停用")
			return
		}
		if destID != 0 && !m.validDictID(r, "worklog_destination", destID, true, team.ID) {
			fail(w, http.StatusBadRequest, codeDestination, "目的地无效或已停用")
			return
		}
	}
	memberSorts, valid := m.loadMemberRows(r, memberIDs, team.ID)
	if !valid {
		fail(w, http.StatusBadRequest, codeMember, "存在无效或已停用的成员")
		return
	}
	if m.failIfDateMemberDup(w, r, logDate, memberIDs, 0) {
		return
	}

	orderVal := any(nil)
	if vehicleID != 0 && orderNo != "" {
		orderVal = orderNo
	}
	destVal := any(nil)
	if vehicleID != 0 {
		destVal = nullIfZero(destID)
	}
	tx, err := m.DB.BeginTx(r.Context(), nil)
	if err != nil {
		writeErr(w, err)
		return
	}
	res, err := tx.ExecContext(r.Context(),
		`INSERT INTO worklog_entry (team_id, log_date, patrol_content, vehicle_id, destination_id, dispatch_order_no, created_by)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		team.ID, logDate, patrol, nullIfZero(vehicleID), destVal, orderVal, ident.ID)
	if err != nil {
		tx.Rollback()
		writeErr(w, err)
		return
	}
	entryID, _ := res.LastInsertId()
	for id, sort := range memberSorts {
		if _, err := tx.ExecContext(r.Context(),
			`INSERT INTO worklog_entry_member (entry_id, member_id, sort) VALUES (?, ?, ?)`,
			entryID, id, sort); err != nil {
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

// handleLogUpdate PUT /logs/{id}：修改卡片（用车人全量替换，保留仍在名单者的打卡状态）。
func (m *Module) handleLogUpdate(w http.ResponseWriter, r *http.Request) {
	_, team, ok := m.enter(w, r)
	if !ok {
		return
	}
	if team == nil {
		fail(w, http.StatusForbidden, codeNoTeam, "未分配班组，请联系管理员分配")
		return
	}
	entryID, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	var curTeamID, curVehicleID, curDestID, curOrderNo, logDate string
	var crossTeam int
	err := m.DB.QueryRowContext(r.Context(),
		`SELECT e.team_id, COALESCE(e.vehicle_id, 0), COALESCE(e.destination_id, 0), e.cross_team,
		        COALESCE(e.dispatch_order_no, ''), DATE_FORMAT(e.log_date, '%Y-%m-%d')
		 FROM worklog_entry e WHERE e.id = ? AND `+entryVisibleSQL,
		entryID, team.ID, team.ID).
		Scan(&curTeamID, &curVehicleID, &curDestID, &crossTeam, &curOrderNo, &logDate)
	if err != nil {
		fail(w, http.StatusNotFound, codeMissing, "日志不存在")
		return
	}
	body, err := decodeBody(r)
	if err != nil {
		fail(w, http.StatusBadRequest, codeInput, "参数错误")
		return
	}
	vehicleID := rawNumberOr0(body["vehicle_id"])
	destID := rawNumberOr0(body["destination_id"])
	patrol := truncate(strings.TrimSpace(rawString(body["patrol_content"])), 500)
	hasOrderNo := false
	if _, exists := body["dispatch_order_no"]; exists {
		hasOrderNo = true
	}
	orderNo := curOrderNo
	if hasOrderNo {
		orderNo = truncate(strings.TrimSpace(rawString(body["dispatch_order_no"])), 64)
	}
	if vehicleID == 0 {
		orderNo = ""
	}
	memberIDs := parseIDList(body["member_ids"])

	if crossTeam == 1 {
		// 跨班卡：派车三件套有变化仅超管可经「跨班日志」编辑
		sameVehicle := atoi64(curVehicleID) == vehicleID
		wantDest := int64(0)
		if vehicleID != 0 {
			wantDest = destID
		}
		sameDest := atoi64(curDestID) == wantDest
		sameMembers := m.sameMembersNow(r, entryID, memberIDs)
		if !sameVehicle || !sameDest || !sameMembers {
			fail(w, http.StatusForbidden, codeForbidden, "跨班日志仅超级管理员可修改派车，请使用「跨班日志」编辑")
			return
		}
		if _, err := m.DB.ExecContext(r.Context(),
			`UPDATE worklog_entry SET patrol_content = ?, dispatch_order_no = ? WHERE id = ?`,
			patrol, nullIfEmpty(orderNo), entryID); err != nil {
			writeErr(w, err)
			return
		}
		if !m.updateRemark(w, r, entryID, body) {
			return
		}
		writeOK(w, map[string]any{"id": entryID})
		return
	}

	if vehicleID == 0 {
		if destID != 0 {
			fail(w, http.StatusBadRequest, codeInput, "未出车时不可填写目的地")
			return
		}
		var photoCnt int
		_ = m.DB.QueryRowContext(r.Context(),
			`SELECT COUNT(*) FROM worklog_photo WHERE entry_id = ?`, entryID).Scan(&photoCnt)
		if photoCnt > 0 {
			fail(w, http.StatusBadRequest, codeHasPhoto, "存在水印照片，不可改为未出车，请先删除照片")
			return
		}
		memberIDs = nil
	} else {
		if !m.validDictID(r, "worklog_vehicle", vehicleID, true, team.ID) {
			fail(w, http.StatusBadRequest, codeVehicle, "车牌号无效或已停用")
			return
		}
		if destID != 0 && !m.validDictID(r, "worklog_destination", destID, true, team.ID) {
			fail(w, http.StatusBadRequest, codeDestination, "目的地无效或已停用")
			return
		}
	}

	memberSorts, valid := m.loadMemberRows(r, memberIDs, team.ID)
	if !valid {
		fail(w, http.StatusBadRequest, codeMember, "存在无效或已停用的成员")
		return
	}
	if m.failIfDateMemberDup(w, r, logDate, memberIDs, entryID) {
		return
	}
	// 被移出名单的成员若已有照片，拒绝
	keepNames := map[string]bool{}
	for id := range memberSorts {
		var name string
		if m.DB.QueryRowContext(r.Context(), `SELECT name FROM worklog_member WHERE id = ?`, id).Scan(&name) == nil {
			keepNames[name] = true
		}
	}
	if m.memberHasPhotoOutside(w, r, entryID, keepNames) {
		return
	}

	newDestID := any(nil)
	if vehicleID != 0 {
		newDestID = nullIfZero(destID)
	}
	tx, err := m.DB.BeginTx(r.Context(), nil)
	if err != nil {
		writeErr(w, err)
		return
	}
	if _, err := tx.ExecContext(r.Context(),
		`UPDATE worklog_entry SET patrol_content = ?, vehicle_id = ?, destination_id = ?, dispatch_order_no = ? WHERE id = ?`,
		patrol, nullIfZero(vehicleID), newDestID, nullIfEmpty(orderNo), entryID); err != nil {
		tx.Rollback()
		writeErr(w, err)
		return
	}
	// 目的地变更联动照片地点核验（仅库内重算，不重调 Dify）
	if atoi64(curDestID) != nullAsInt64(newDestID) {
		destName := ""
		if vehicleID != 0 && destID != 0 {
			_ = tx.QueryRowContext(r.Context(), `SELECT name FROM worklog_destination WHERE id = ?`, destID).Scan(&destName)
		}
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
	// 成员行重写（保留仍在名单者的打卡状态）
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
	for id, sort := range memberSorts {
		if _, err := tx.ExecContext(r.Context(),
			`INSERT INTO worklog_entry_member (entry_id, member_id, checked, sort) VALUES (?, ?, ?, ?)`,
			entryID, id, checkedMap[id], sort); err != nil {
			tx.Rollback()
			writeErr(w, err)
			return
		}
	}
	if err := tx.Commit(); err != nil {
		writeErr(w, err)
		return
	}
	if !m.updateRemark(w, r, entryID, body) {
		return
	}
	writeOK(w, map[string]any{"id": entryID})
}

// sameMembersNow 跨班卡派车比对（Node 同口径）：提交成员数量与库内现值一致，且库内每个成员都在提交集合中。
func (m *Module) sameMembersNow(r *http.Request, entryID int64, memberIDs []int64) bool {
	rows, err := m.DB.QueryContext(r.Context(),
		`SELECT member_id FROM worklog_entry_member WHERE entry_id = ?`, entryID)
	if err != nil {
		return false
	}
	defer rows.Close()
	cur := make([]int64, 0)
	for rows.Next() {
		var id int64
		if rows.Scan(&id) == nil {
			cur = append(cur, id)
		}
	}
	if len(cur) != len(memberIDs) {
		return false
	}
	submitted := map[int64]bool{}
	for _, id := range memberIDs {
		submitted[id] = true
	}
	for _, id := range cur {
		if !submitted[id] {
			return false
		}
	}
	return true
}

// memberHasPhotoOutside 被移出名单的成员若已有水印照片，拒绝（需先调整照片人名）。
func (m *Module) memberHasPhotoOutside(w http.ResponseWriter, r *http.Request, entryID int64, keepNames map[string]bool) bool {
	rows, err := m.DB.QueryContext(r.Context(),
		`SELECT members FROM worklog_photo WHERE entry_id = ?`, entryID)
	if err != nil {
		writeErr(w, err)
		return true
	}
	defer rows.Close()
	for rows.Next() {
		var membersJSON string
		if rows.Scan(&membersJSON) != nil {
			continue
		}
		for _, n := range parseNames(membersJSON) {
			if !keepNames[n] {
				fail(w, http.StatusBadRequest, codeMemberPhoto,
					"成员「"+n+"」已有水印照片，不可移出用车人，请先调整照片人名")
				return true
			}
		}
	}
	return false
}

// handleLogDelete DELETE /logs/{id}：删除卡片（先删 COS 对象再删行；跨班卡仅超管可删）。
func (m *Module) handleLogDelete(w http.ResponseWriter, r *http.Request) {
	ident, team, ok := m.enter(w, r)
	if !ok {
		return
	}
	if team == nil {
		fail(w, http.StatusForbidden, codeNoTeam, "未分配班组，请联系管理员分配")
		return
	}
	entryID, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	var entryTeamID int64
	var crossTeam int
	var remarkFiles string
	err := m.DB.QueryRowContext(r.Context(),
		`SELECT e.team_id, e.cross_team, COALESCE(e.remark_files, '')
		 FROM worklog_entry e WHERE e.id = ? AND `+entryVisibleSQL,
		entryID, team.ID, team.ID).Scan(&entryTeamID, &crossTeam, &remarkFiles)
	if err != nil {
		fail(w, http.StatusNotFound, codeMissing, "日志不存在")
		return
	}
	if crossTeam == 1 && ident.Role != "admin" {
		fail(w, http.StatusForbidden, codeForbidden, "跨班日志仅超级管理员可删除")
		return
	}
	if m.SgccOps != nil && m.SgccOps.ActivePhotoOp(entryTeamID, entryID) {
		fail(w, http.StatusConflict, codeOpRunning, "该卡片有商旅同步任务进行中，请完成后再删除")
		return
	}
	rows, err := m.DB.QueryContext(r.Context(),
		`SELECT cos_key FROM worklog_photo WHERE entry_id = ?`, entryID)
	keys := make([]string, 0)
	if err == nil {
		for rows.Next() {
			var key string
			if rows.Scan(&key) == nil && key != "" {
				keys = append(keys, key)
			}
		}
		rows.Close()
	}
	for _, f := range parseRemarkFiles(remarkFiles) {
		if f.CosKey != "" {
			keys = append(keys, f.CosKey)
		}
	}
	if m.Store != nil {
		for _, key := range keys {
			_ = m.Store.DeleteObject(r.Context(), key)
		}
	}
	tx, err := m.DB.BeginTx(r.Context(), nil)
	if err != nil {
		writeErr(w, err)
		return
	}
	if _, err := tx.ExecContext(r.Context(), `DELETE FROM worklog_photo WHERE entry_id = ?`, entryID); err != nil {
		tx.Rollback()
		writeErr(w, err)
		return
	}
	if _, err := tx.ExecContext(r.Context(), `DELETE FROM worklog_entry_member WHERE entry_id = ?`, entryID); err != nil {
		tx.Rollback()
		writeErr(w, err)
		return
	}
	res, err := tx.ExecContext(r.Context(), `DELETE FROM worklog_entry WHERE id = ?`, entryID)
	if err != nil {
		tx.Rollback()
		writeErr(w, err)
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		tx.Rollback()
		fail(w, http.StatusNotFound, codeMissing, "日志不存在")
		return
	}
	if err := tx.Commit(); err != nil {
		writeErr(w, err)
		return
	}
	writeOK(w, nil)
}

// handleMemberCheck PUT /logs/{id}/members/{mid}/check：打卡切换。
func (m *Module) handleMemberCheck(w http.ResponseWriter, r *http.Request) {
	_, team, ok := m.enter(w, r)
	if !ok {
		return
	}
	if team == nil {
		fail(w, http.StatusForbidden, codeNoTeam, "未分配班组，请联系管理员分配")
		return
	}
	entryID, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	memberRowID, _ := strconv.ParseInt(chi.URLParam(r, "mid"), 10, 64)
	res, err := m.DB.ExecContext(r.Context(),
		`UPDATE worklog_entry_member em JOIN worklog_entry e ON e.id = em.entry_id
		 SET em.checked = 1 - em.checked WHERE em.id = ? AND em.entry_id = ? AND `+entryVisibleSQL,
		memberRowID, entryID, team.ID, team.ID)
	if err != nil {
		writeErr(w, err)
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		fail(w, http.StatusNotFound, codeMissing, "打卡记录不存在")
		return
	}
	var checked int
	_ = m.DB.QueryRowContext(r.Context(),
		`SELECT checked FROM worklog_entry_member WHERE id = ?`, memberRowID).Scan(&checked)
	writeOK(w, map[string]any{"checked": checked})
}

// updateRemark 备注与附件更新（仅请求显式携带对应字段时更新，避免误清）；
// 失败已写响应返回 false，调用方不得再写成功响应。
func (m *Module) updateRemark(w http.ResponseWriter, r *http.Request, entryID int64, body map[string]json.RawMessage) bool {
	_, hasRemark := body["remark"]
	_, hasFiles := body["remark_files"]
	if !hasRemark && !hasFiles {
		return true
	}
	var oldRemark, oldFilesRaw string
	if err := m.DB.QueryRowContext(r.Context(),
		`SELECT COALESCE(remark, ''), COALESCE(remark_files, '') FROM worklog_entry WHERE id = ?`, entryID).
		Scan(&oldRemark, &oldFilesRaw); err != nil {
		writeErr(w, err)
		return false
	}
	oldFiles := parseRemarkFiles(oldFilesRaw)
	newRemark := oldRemark
	if hasRemark {
		newRemark = truncate(strings.TrimSpace(rawString(body["remark"])), 500)
	}
	newFiles := oldFiles
	if hasFiles {
		raw := bytes.TrimSpace(body["remark_files"])
		var arr []json.RawMessage
		// Node 口径：非数组（含 null）一律 40020，数组超 9 个同码
		if len(raw) == 0 || raw[0] != '[' || jsonUnmarshal(raw, &arr) != nil || len(arr) > 9 {
			fail(w, http.StatusBadRequest, codeFileCount, "备注附件最多 9 个")
			return false
		}
		cleaned := make([]RemarkFile, 0, len(arr))
		for _, item := range arr {
			clean, valid := sanitizeRemarkFile(item)
			if !valid {
				fail(w, http.StatusBadRequest, codeFileBad, "备注附件数据不完整或格式不支持")
				return false
			}
			cleaned = append(cleaned, clean)
		}
		newFiles = cleaned
	}
	filesJSON, _ := json.Marshal(newFiles)
	if _, err := m.DB.ExecContext(r.Context(),
		`UPDATE worklog_entry SET remark = ?, remark_files = ? WHERE id = ?`,
		newRemark, string(filesJSON), entryID); err != nil {
		writeErr(w, err)
		return false
	}
	// 被移除附件同步删 COS 对象（删除集合只来自库内旧值）
	kept := map[string]bool{}
	for _, f := range newFiles {
		kept[f.CosKey] = true
	}
	if m.Store != nil {
		for _, f := range oldFiles {
			if f.CosKey != "" && !kept[f.CosKey] {
				_ = m.Store.DeleteObject(r.Context(), f.CosKey)
			}
		}
	}
	return true
}

// sanitizeRemarkFile 备注附件入库前清洗（Node sanitizeRemarkFile 同口径）：
// 字段形状校验 + 长度截断；不合法返回 false（cos_key 仅作留存，删除集合只取库内旧值）。
func sanitizeRemarkFile(raw json.RawMessage) (RemarkFile, bool) {
	var obj map[string]json.RawMessage
	if len(raw) == 0 || jsonUnmarshal(raw, &obj) != nil || obj == nil {
		return RemarkFile{}, false
	}
	f := RemarkFile{
		Name:   truncate(strings.TrimSpace(rawString(obj["name"])), 128),
		URL:    truncate(strings.TrimSpace(rawString(obj["url"])), 512),
		CosKey: truncate(strings.TrimSpace(rawString(obj["cos_key"])), 255),
		Type:   rawString(obj["type"]),
	}
	f.Size = rawNumberOr0(obj["size"])
	lowerURL := strings.ToLower(f.URL)
	if f.Name == "" || (!strings.HasPrefix(lowerURL, "http://") && !strings.HasPrefix(lowerURL, "https://")) ||
		f.CosKey == "" || strings.Contains(f.CosKey, "..") {
		return RemarkFile{}, false
	}
	if f.Type != "image" && f.Type != "video" && f.Type != "doc" {
		return RemarkFile{}, false
	}
	return f, true
}

// parseIDList 解析 id 数组字段（number[]，兼容字符串数字；显式 0 保留，交由成员校验按无效拦截，Node 同口径）。
func parseIDList(raw json.RawMessage) []int64 {
	if len(raw) == 0 {
		return nil
	}
	var arr []json.RawMessage
	if err := json.Unmarshal(raw, &arr); err != nil {
		return nil
	}
	out := make([]int64, 0, len(arr))
	for _, item := range arr {
		var n float64
		if json.Unmarshal(item, &n) == nil {
			out = append(out, int64(n))
			continue
		}
		var s string
		if json.Unmarshal(item, &s) == nil {
			if v, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64); err == nil {
				out = append(out, v)
			}
		}
	}
	return out
}

// nullIfZero 0 转 NULL。
func nullIfZero(v int64) any {
	if v == 0 {
		return nil
	}
	return v
}

// nullIfEmpty 空串转 NULL。
func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// atoi64 字符串数字转 int64（库内以 string 扫描的列）。
func atoi64(s string) int64 {
	n, _ := strconv.ParseInt(s, 10, 64)
	return n
}

// nullAsInt64 any（int64 或 nil）转 int64。
func nullAsInt64(v any) int64 {
	switch t := v.(type) {
	case int64:
		return t
	case nil:
		return 0
	}
	return 0
}
