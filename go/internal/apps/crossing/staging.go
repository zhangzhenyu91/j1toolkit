package crossing

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

// 暂存区（班管/超管）：批次列表 / 批次详情 / 暂存行编辑 / 确认导入正式表 / 批次废弃（物理删除）。
// 导入时 pole 区间由 parseSectionPoles 从区段文本解析；撞 uk_crane 改更新业务列（幂等）。

// stagingEditable 暂存行可编辑列与长度（白名单；日期列单独归一）。
var stagingEditable = map[string]int{
	"voltage_level":    16,
	"line_name":        64,
	"section":          32,
	"crossing_type":    128,
	"dist_small":       16,
	"dist_big":         16,
	"cross_horizontal": 16,
	"cross_vertical":   16,
	"cross_angle":      16,
	"has_hazard":       16,
	"finder_name":      32,
	"remark":           255,
}

// stagingBatches GET /staging/batches：本班 pending 批次列表（含记录数）。
func (m *Module) stagingBatches(w http.ResponseWriter, r *http.Request) {
	_, team, ok := m.enterAdmin(w, r, 0)
	if !ok {
		return
	}
	if team == nil {
		fail(w, http.StatusBadRequest, codeNoTeam, "无可用班组")
		return
	}
	rows, err := m.DB.QueryContext(r.Context(),
		`SELECT b.id, b.team_id, b.crane_session_id, b.group_name, b.pusher_name, b.review_note, b.xlsx_path, b.status, b.created_at,
         (SELECT COUNT(*) FROM crossing_staging_record s WHERE s.batch_id = b.id) AS record_count
       FROM crossing_staging_batch b WHERE b.team_id = ? AND b.status = 'pending' ORDER BY b.id DESC`, team.ID)
	if err != nil {
		writeErr(w, err)
		return
	}
	defer rows.Close()
	list := []map[string]any{}
	for rows.Next() {
		var id, teamID, recordCount int64
		var sessionID, groupName, pusherName, xlsxPath, status string
		var reviewNote sql.NullString
		var createdAt time.Time
		if err := rows.Scan(&id, &teamID, &sessionID, &groupName, &pusherName, &reviewNote,
			&xlsxPath, &status, &createdAt, &recordCount); err != nil {
			writeErr(w, err)
			return
		}
		list = append(list, map[string]any{
			"id": id, "team_id": teamID, "crane_session_id": sessionID, "group_name": groupName,
			"pusher_name": pusherName, "review_note": nullStr(reviewNote), "xlsx_path": xlsxPath,
			"status": status, "created_at": createdAt, "record_count": recordCount,
		})
	}
	if err := rows.Err(); err != nil {
		writeErr(w, err)
		return
	}
	writeOK(w, list)
}

// stagingBatchDetail GET /staging/batches/:id：批次 + 全部记录行。
func (m *Module) stagingBatchDetail(w http.ResponseWriter, r *http.Request) {
	_, team, ok := m.enterAdmin(w, r, 0)
	if !ok {
		return
	}
	if team == nil {
		fail(w, http.StatusBadRequest, codeNoTeam, "无可用班组")
		return
	}
	id := pathID(r)
	var batchID, teamID int64
	var sessionID, groupName, pusherName, xlsxPath, status string
	var reviewNote sql.NullString
	var createdAt time.Time
	err := m.DB.QueryRowContext(r.Context(),
		"SELECT id, team_id, crane_session_id, group_name, pusher_name, review_note, xlsx_path, status, created_at FROM crossing_staging_batch WHERE id = ? AND team_id = ?",
		id, team.ID).Scan(&batchID, &teamID, &sessionID, &groupName, &pusherName, &reviewNote,
		&xlsxPath, &status, &createdAt)
	if err != nil {
		fail(w, http.StatusNotFound, codeMissing, "批次不存在")
		return
	}
	rows, err := m.DB.QueryContext(r.Context(),
		`SELECT id, batch_id, crane_record_id, voltage_level, line_name, section, crossing_type,
         dist_small, dist_big, cross_horizontal, cross_vertical, cross_angle, has_hazard, finder_name,
         DATE_FORMAT(inspect_at, '%Y-%m-%d') AS inspect_at, remark, created_at
       FROM crossing_staging_record WHERE batch_id = ? ORDER BY id`, batchID)
	if err != nil {
		writeErr(w, err)
		return
	}
	defer rows.Close()
	records := []map[string]any{}
	for rows.Next() {
		var recID, recBatchID int64
		var craneRecordID, inspectAt sql.NullString
		var voltage, line, section, crossingType, distSmall, distBig string
		var crossH, crossV, crossAngle, hasHazard, finderName, remark string
		var recCreated time.Time
		if err := rows.Scan(&recID, &recBatchID, &craneRecordID, &voltage, &line, &section, &crossingType,
			&distSmall, &distBig, &crossH, &crossV, &crossAngle, &hasHazard, &finderName,
			&inspectAt, &remark, &recCreated); err != nil {
			writeErr(w, err)
			return
		}
		records = append(records, map[string]any{
			"id": recID, "batch_id": recBatchID, "crane_record_id": nullStr(craneRecordID),
			"voltage_level": voltage, "line_name": line, "section": section, "crossing_type": crossingType,
			"dist_small": distSmall, "dist_big": distBig, "cross_horizontal": crossH,
			"cross_vertical": crossV, "cross_angle": crossAngle, "has_hazard": hasHazard,
			"finder_name": finderName, "inspect_at": nullStr(inspectAt), "remark": remark,
			"created_at": recCreated,
		})
	}
	if err := rows.Err(); err != nil {
		writeErr(w, err)
		return
	}
	writeOK(w, map[string]any{
		"batch": map[string]any{
			"id": batchID, "team_id": teamID, "crane_session_id": sessionID, "group_name": groupName,
			"pusher_name": pusherName, "review_note": nullStr(reviewNote), "xlsx_path": xlsxPath,
			"status": status, "created_at": createdAt,
		},
		"records": records,
	})
}

// stagingRecordUpdate PUT /staging/records/:id：更新暂存行业务列（白名单字段；inspect_at 单独归一）。
func (m *Module) stagingRecordUpdate(w http.ResponseWriter, r *http.Request) {
	_, team, ok := m.enterAdmin(w, r, 0)
	if !ok {
		return
	}
	if team == nil {
		fail(w, http.StatusBadRequest, codeNoTeam, "无可用班组")
		return
	}
	id := pathID(r)
	body, err := decodeBody(r)
	if err != nil {
		fail(w, http.StatusBadRequest, codeParam, "请求体不是合法 JSON")
		return
	}
	var exist int64
	if err := m.DB.QueryRowContext(r.Context(),
		`SELECT s.id FROM crossing_staging_record s JOIN crossing_staging_batch b ON b.id = s.batch_id
       WHERE s.id = ? AND b.team_id = ?`, id, team.ID).Scan(&exist); err != nil {
		fail(w, http.StatusNotFound, codeMissing, "记录不存在")
		return
	}
	sets := []string{}
	args := []any{}
	for _, col := range []string{
		"voltage_level", "line_name", "section", "crossing_type", "dist_small", "dist_big",
		"cross_horizontal", "cross_vertical", "cross_angle", "has_hazard", "finder_name", "remark",
	} {
		if raw, present := body[col]; present {
			sets = append(sets, col+" = ?")
			args = append(args, truncate(rawString(raw), stagingEditable[col]))
		}
	}
	if raw, present := body["inspect_at"]; present {
		sets = append(sets, "inspect_at = ?")
		args = append(args, nullIfEmpty(normDateStr(rawString(raw))))
	}
	if len(sets) == 0 {
		fail(w, http.StatusBadRequest, codeParam, "没有可更新的字段")
		return
	}
	args = append(args, id)
	if _, err := m.DB.ExecContext(r.Context(),
		"UPDATE crossing_staging_record SET "+joinComma(sets)+" WHERE id = ?", args...); err != nil {
		writeErr(w, err)
		return
	}
	writeOK(w, map[string]any{"id": id})
}

// stagingImport POST /staging/batches/:id/import：导入正式表（body { record_ids? } 缺省全部；事务）。
// 暂存行不含 pole_from/pole_to：pole 区间由 parseSectionPoles 从区段文本解析；
// 逐行写入（source='staging'，crane_session_id/crane_record_id 从批次与行带过来；撞 uk_crane 改更新业务列），
// 删除已导入行；批次行空 → status=imported。
func (m *Module) stagingImport(w http.ResponseWriter, r *http.Request) {
	_, team, ok := m.enterAdmin(w, r, 0)
	if !ok {
		return
	}
	if team == nil {
		fail(w, http.StatusBadRequest, codeNoTeam, "无可用班组")
		return
	}
	batchID := pathID(r)
	body, err := decodeBody(r)
	if err != nil {
		fail(w, http.StatusBadRequest, codeParam, "请求体不是合法 JSON")
		return
	}
	ids := map[int64]bool{}
	filter := false
	if raw, present := body["record_ids"]; present {
		filter = true
		// record_ids 为数字数组（对齐 Node：map(Number).filter(int > 0)）
		var numList []float64
		if e := json.Unmarshal(raw, &numList); e != nil {
			fail(w, http.StatusBadRequest, codeParam, "record_ids 无效")
			return
		}
		for _, f := range numList {
			if f > 0 && f == float64(int64(f)) {
				ids[int64(f)] = true
			}
		}
		if len(ids) == 0 {
			fail(w, http.StatusBadRequest, codeParam, "record_ids 无效")
			return
		}
	}
	var sessionID string
	var exists int64
	err = m.DB.QueryRowContext(r.Context(),
		"SELECT id, crane_session_id FROM crossing_staging_batch WHERE id = ? AND team_id = ? AND status = 'pending'",
		batchID, team.ID).Scan(&exists, &sessionID)
	if err != nil {
		fail(w, http.StatusNotFound, codeMissing, "批次不存在或已处理")
		return
	}
	tx, err := m.DB.BeginTx(r.Context(), nil)
	if err != nil {
		writeErr(w, err)
		return
	}
	defer tx.Rollback() //nolint:errcheck // 提交后回滚为空操作
	rows, err := tx.QueryContext(r.Context(),
		`SELECT id, crane_record_id, voltage_level, line_name, section, crossing_type,
         dist_small, dist_big, cross_horizontal, cross_vertical, cross_angle, has_hazard,
         finder_name, DATE_FORMAT(inspect_at, '%Y-%m-%d') AS inspect_at, remark
       FROM crossing_staging_record WHERE batch_id = ? ORDER BY id`, batchID)
	if err != nil {
		writeErr(w, err)
		return
	}
	type stagingRow struct {
		id            int64
		craneRecordID *string
		voltage       string
		line          string
		section       string
		crossingType  string
		distSmall     string
		distBig       string
		crossH        string
		crossV        string
		crossAngle    string
		hasHazard     string
		finderName    string
		inspectAt     *string
		remark        string
	}
	pending := []stagingRow{}
	for rows.Next() {
		var sr stagingRow
		var craneID, inspectAt sql.NullString
		if err := rows.Scan(&sr.id, &craneID, &sr.voltage, &sr.line, &sr.section, &sr.crossingType,
			&sr.distSmall, &sr.distBig, &sr.crossH, &sr.crossV, &sr.crossAngle, &sr.hasHazard,
			&sr.finderName, &inspectAt, &sr.remark); err != nil {
			rows.Close()
			writeErr(w, err)
			return
		}
		if craneID.Valid && craneID.String != "" {
			sr.craneRecordID = &craneID.String
		}
		if inspectAt.Valid {
			sr.inspectAt = &inspectAt.String
		}
		if filter && !ids[sr.id] {
			continue
		}
		pending = append(pending, sr)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		writeErr(w, err)
		return
	}
	imported := 0
	for _, sr := range pending {
		poleFrom, poleTo := parseSectionPoles(sr.section)
		existingID := int64(0)
		found := false
		if sr.craneRecordID != nil {
			err := tx.QueryRowContext(r.Context(),
				"SELECT id FROM crossing_record WHERE team_id = ? AND crane_session_id = ? AND crane_record_id = ?",
				team.ID, sessionID, *sr.craneRecordID).Scan(&existingID)
			switch {
			case err == nil:
				found = true
			case errors.Is(err, sql.ErrNoRows):
			default:
				writeErr(w, err)
				return
			}
		}
		if found {
			if _, err := tx.ExecContext(r.Context(),
				`UPDATE crossing_record SET voltage_level = ?, line_name = ?, section = ?, pole_from = ?, pole_to = ?,
               crossing_type = ?, dist_small = ?, dist_big = ?, cross_horizontal = ?, cross_vertical = ?, cross_angle = ?,
               has_hazard = ?, finder_name = ?, inspect_at = ?, remark = ?
             WHERE id = ?`,
				sr.voltage, sr.line, sr.section, poleFrom, poleTo, sr.crossingType,
				sr.distSmall, sr.distBig, sr.crossH, sr.crossV, sr.crossAngle,
				sr.hasHazard, sr.finderName, sr.inspectAt, sr.remark, existingID); err != nil {
				writeErr(w, err)
				return
			}
		} else {
			if _, err := tx.ExecContext(r.Context(),
				`INSERT INTO crossing_record (team_id, voltage_level, line_name, section, pole_from, pole_to, crossing_type,
               dist_small, dist_big, cross_horizontal, cross_vertical, cross_angle, has_hazard,
               finder_name, inspect_at, remark, source, crane_session_id, crane_record_id)
             VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'staging', ?, ?)`,
				team.ID, sr.voltage, sr.line, sr.section, poleFrom, poleTo, sr.crossingType,
				sr.distSmall, sr.distBig, sr.crossH, sr.crossV, sr.crossAngle,
				sr.hasHazard, sr.finderName, sr.inspectAt, sr.remark, sessionID, sr.craneRecordID); err != nil {
				writeErr(w, err)
				return
			}
		}
		if _, err := tx.ExecContext(r.Context(),
			"DELETE FROM crossing_staging_record WHERE id = ?", sr.id); err != nil {
			writeErr(w, err)
			return
		}
		imported++
	}
	var left int64
	if err := tx.QueryRowContext(r.Context(),
		"SELECT COUNT(*) AS cnt FROM crossing_staging_record WHERE batch_id = ?", batchID).Scan(&left); err != nil {
		writeErr(w, err)
		return
	}
	if left == 0 {
		if _, err := tx.ExecContext(r.Context(),
			"UPDATE crossing_staging_batch SET status = 'imported' WHERE id = ?", batchID); err != nil {
			writeErr(w, err)
			return
		}
	}
	if err := tx.Commit(); err != nil {
		writeErr(w, err)
		return
	}
	writeOK(w, map[string]any{"imported": imported})
}

// stagingBatchDelete DELETE /staging/batches/:id：物理删除批次与全部记录行（随批 xlsx 一并清理，失败仅记日志）。
func (m *Module) stagingBatchDelete(w http.ResponseWriter, r *http.Request) {
	_, team, ok := m.enterAdmin(w, r, 0)
	if !ok {
		return
	}
	if team == nil {
		fail(w, http.StatusBadRequest, codeNoTeam, "无可用班组")
		return
	}
	batchID := pathID(r)
	var xlsxPath string
	err := m.DB.QueryRowContext(r.Context(),
		"SELECT xlsx_path FROM crossing_staging_batch WHERE id = ? AND team_id = ?", batchID, team.ID).Scan(&xlsxPath)
	if err != nil {
		fail(w, http.StatusNotFound, codeMissing, "批次不存在")
		return
	}
	if _, err := m.DB.ExecContext(r.Context(),
		"DELETE FROM crossing_staging_record WHERE batch_id = ?", batchID); err != nil {
		writeErr(w, err)
		return
	}
	if _, err := m.DB.ExecContext(r.Context(),
		"DELETE FROM crossing_staging_batch WHERE id = ?", batchID); err != nil {
		writeErr(w, err)
		return
	}
	if xlsxPath != "" {
		m.unlinkBatchXLSX(xlsxPath) // 清理失败仅记日志（返回值忽略），不影响删除结果
	}
	writeOK(w, map[string]any{"id": batchID})
}

// unlinkBatchXLSX 清理随批 xlsx 落盘文件（失败忽略：文件可能已不存在）。
func (m *Module) unlinkBatchXLSX(rel string) {
	root := m.ServerDir
	if root == "" {
		root = "."
	}
	abs := filepath.Join(root, filepath.FromSlash(rel))
	_ = os.Remove(abs)
}

// nullStr 把 sql.NullString 映射为 string/nil（JSON null）。
func nullStr(ns sql.NullString) any {
	if ns.Valid {
		return ns.String
	}
	return nil
}

// nullIfEmpty 空串 → nil（SQL NULL）。
func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// joinComma 逗号连接 SET 片段。
func joinComma(parts []string) string {
	out := ""
	for i, p := range parts {
		if i > 0 {
			out += ", "
		}
		out += p
	}
	return out
}
