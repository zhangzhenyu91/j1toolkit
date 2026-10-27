package treehazard

import (
	"database/sql"
	"fmt"
	"net/http"
	"strings"
)

// stagingBatch 暂存批次 JSON（与 Node 行同形）。
type stagingBatch struct {
	ID             int64  `json:"id"`
	TeamID         int64  `json:"team_id"`
	CraneSessionID string `json:"crane_session_id"`
	GroupName      string `json:"group_name"`
	PusherName     string `json:"pusher_name"`
	ReviewNote     string `json:"review_note"`
	XLSXPath       string `json:"xlsx_path"`
	Status         string `json:"status"`
	RecordCount    int64  `json:"record_count,omitempty"`
	CreatedAt      any    `json:"created_at"`
}

const stagingRecordCols = `id, batch_id, crane_record_id, voltage_level, line_name, section, tree_species, tree_count,
  dist_small, dist_big, diameter, horizontal, vertical, vertical_num, finder_name,
  DATE_FORMAT(found_at, '%Y-%m-%d') AS found_at, handler_name, handle_note,
  DATE_FORMAT(handled_at, '%Y-%m-%d') AS handled_at, created_at`

// stagingEditable 暂存行可编辑列与长度（白名单；日期列单独归一）。
var stagingEditable = map[string]int{
	"voltage_level": 16, "line_name": 64, "section": 32, "tree_species": 32,
	"tree_count": 16, "dist_small": 16, "dist_big": 16, "diameter": 16,
	"horizontal": 16, "vertical": 16, "finder_name": 32, "handler_name": 32, "handle_note": 64,
}

// handleStagingBatches GET /staging/batches：本班 pending 批次列表（含记录数）。
func (m *Module) handleStagingBatches(w http.ResponseWriter, r *http.Request) {
	team, _, ok := requireManagerNow(w, r)
	if !ok {
		return
	}
	rows, err := m.DB.QueryContext(r.Context(),
		`SELECT b.id, b.team_id, b.crane_session_id, b.group_name, b.pusher_name, b.review_note, b.xlsx_path, b.status, b.created_at,
		   (SELECT COUNT(*) FROM treehazard_staging_record s WHERE s.batch_id = b.id)
		 FROM treehazard_staging_batch b WHERE b.team_id = ? AND b.status = 'pending' ORDER BY b.id DESC`, team.ID)
	if err != nil {
		writeFail(w, http.StatusInternalServerError, codeParam, "暂存批次读取失败", nil)
		return
	}
	defer rows.Close()
	list := make([]map[string]any, 0)
	for rows.Next() {
		var (
			b    stagingBatch
			note sql.NullString
		)
		if err := rows.Scan(&b.ID, &b.TeamID, &b.CraneSessionID, &b.GroupName, &b.PusherName,
			&note, &b.XLSXPath, &b.Status, &b.CreatedAt, &b.RecordCount); err != nil {
			writeFail(w, http.StatusInternalServerError, codeParam, "暂存批次读取失败", nil)
			return
		}
		b.ReviewNote = note.String
		list = append(list, map[string]any{
			"id": b.ID, "team_id": b.TeamID, "crane_session_id": b.CraneSessionID,
			"group_name": b.GroupName, "pusher_name": b.PusherName, "review_note": b.ReviewNote,
			"xlsx_path": b.XLSXPath, "status": b.Status, "created_at": b.CreatedAt,
			"record_count": b.RecordCount,
		})
	}
	writeOK(w, list, "")
}

// handleStagingBatchGet GET /staging/batches/:id：批次 + 全部记录行。
func (m *Module) handleStagingBatchGet(w http.ResponseWriter, r *http.Request) {
	team, _, ok := requireManagerNow(w, r)
	if !ok {
		return
	}
	batchID, ok := pathID(r)
	if !ok {
		writeFail(w, http.StatusBadRequest, codeParam, "id 无效", nil)
		return
	}
	var (
		b    stagingBatch
		note sql.NullString
	)
	err := m.DB.QueryRowContext(r.Context(),
		`SELECT id, team_id, crane_session_id, group_name, pusher_name, review_note, xlsx_path, status, created_at
		 FROM treehazard_staging_batch WHERE id = ? AND team_id = ?`, batchID, team.ID).
		Scan(&b.ID, &b.TeamID, &b.CraneSessionID, &b.GroupName, &b.PusherName, &note, &b.XLSXPath, &b.Status, &b.CreatedAt)
	if err != nil {
		writeFail(w, http.StatusNotFound, codeNotFound, "批次不存在", nil)
		return
	}
	b.ReviewNote = note.String
	rows, err := m.DB.QueryContext(r.Context(),
		`SELECT `+stagingRecordCols+` FROM treehazard_staging_record WHERE batch_id = ? ORDER BY id`, b.ID)
	if err != nil {
		writeFail(w, http.StatusInternalServerError, codeParam, "暂存记录读取失败", nil)
		return
	}
	defer rows.Close()
	records := make([]map[string]any, 0)
	for rows.Next() {
		var (
			id, batch                         int64
			craneRecordID                     sql.NullString
			voltage, line, section            string
			species, count, dS, dB, dia, h, v string
			verticalNum                       sql.NullFloat64
			finder, handler, noteText         string
			foundAt, handledAt                sql.NullString
			createdAt                         any
		)
		if err := rows.Scan(&id, &batch, &craneRecordID, &voltage, &line, &section, &species, &count,
			&dS, &dB, &dia, &h, &v, &verticalNum, &finder, &foundAt, &handler, &noteText, &handledAt, &createdAt); err != nil {
			writeFail(w, http.StatusInternalServerError, codeParam, "暂存记录读取失败", nil)
			return
		}
		records = append(records, map[string]any{
			"id": id, "batch_id": batch, "crane_record_id": craneRecordID.String,
			"voltage_level": voltage, "line_name": line, "section": section,
			"tree_species": species, "tree_count": count, "dist_small": dS, "dist_big": dB,
			"diameter": dia, "horizontal": h, "vertical": v,
			"vertical_num": numOrNil(verticalNum),
			"finder_name":  finder, "found_at": foundAt.String,
			"handler_name": handler, "handle_note": noteText, "handled_at": handledAt.String,
			"created_at": createdAt,
		})
	}
	writeOK(w, map[string]any{"batch": b, "records": records}, "")
}

func numOrNil(v sql.NullFloat64) any {
	if v.Valid {
		return v.Float64
	}
	return nil
}

// handleStagingRecordUpdate PUT /staging/records/:id：更新暂存行业务列（白名单；vertical 变更时重算 vertical_num）。
func (m *Module) handleStagingRecordUpdate(w http.ResponseWriter, r *http.Request) {
	team, _, ok := requireManagerNow(w, r)
	if !ok {
		return
	}
	recordID, ok := pathID(r)
	if !ok {
		writeFail(w, http.StatusBadRequest, codeParam, "id 无效", nil)
		return
	}
	var exist int64
	if err := m.DB.QueryRowContext(r.Context(),
		`SELECT s.id FROM treehazard_staging_record s JOIN treehazard_staging_batch b ON b.id = s.batch_id
		 WHERE s.id = ? AND b.team_id = ?`, recordID, team.ID).Scan(&exist); err != nil {
		writeFail(w, http.StatusNotFound, codeNotFound, "记录不存在", nil)
		return
	}
	var body map[string]any
	if err := decodeJSON(r, &body); err != nil {
		writeFail(w, http.StatusBadRequest, codeParam, "请求体不是合法 JSON", nil)
		return
	}
	sets := make([]string, 0)
	vals := make([]any, 0)
	for col, maxLen := range stagingEditable {
		if raw, present := body[col]; present {
			sets = append(sets, col+" = ?")
			vals = append(vals, truncateRunes(strings.TrimSpace(asString(raw)), maxLen))
		}
	}
	for _, col := range []string{"found_at", "handled_at"} {
		if raw, present := body[col]; present {
			sets = append(sets, col+" = ?")
			vals = append(vals, nullIfEmpty(normDateStr(asString(raw))))
		}
	}
	if raw, present := body["vertical"]; present {
		sets = append(sets, "vertical_num = ?")
		vals = append(vals, parseVerticalNum(asString(raw)))
	}
	if len(sets) == 0 {
		writeFail(w, http.StatusBadRequest, codeParam, "没有可更新的字段", nil)
		return
	}
	vals = append(vals, recordID)
	if _, err := m.DB.ExecContext(r.Context(),
		`UPDATE treehazard_staging_record SET `+strings.Join(sets, ", ")+` WHERE id = ?`, vals...); err != nil {
		writeFail(w, http.StatusInternalServerError, codeParam, "暂存记录更新失败", nil)
		return
	}
	writeOK(w, map[string]any{"id": recordID}, "")
}

// stagingRow 暂存行（导入用）。
type stagingRow struct {
	ID            int64
	CraneRecordID *string
	VoltageLevel  string
	LineName      string
	Section       string
	TreeSpecies   string
	TreeCount     string
	DistSmall     string
	DistBig       string
	Diameter      string
	Horizontal    string
	Vertical      string
	VerticalNum   *float64
	FinderName    string
	FoundAt       string
	HandlerName   string
	HandleNote    string
	HandledAt     string
}

// handleStagingImport POST /staging/batches/:id/import：导入正式表（body { record_ids? } 缺省全部；事务）：
// pole 区间由区段文本解析；逐行写入（source='staging'，撞 uk_crane 改更新发现侧列），
// 辅证照片随转入归挂；删除已导入行；批次行空 → status=imported。
func (m *Module) handleStagingImport(w http.ResponseWriter, r *http.Request) {
	team, _, ok := requireManagerNow(w, r)
	if !ok {
		return
	}
	batchID, ok := pathID(r)
	if !ok {
		writeFail(w, http.StatusBadRequest, codeParam, "id 无效", nil)
		return
	}
	var (
		sessionID string
		status    string
	)
	err := m.DB.QueryRowContext(r.Context(),
		`SELECT crane_session_id, status FROM treehazard_staging_batch WHERE id = ? AND team_id = ? AND status = 'pending'`,
		batchID, team.ID).Scan(&sessionID, &status)
	if err != nil {
		writeFail(w, http.StatusNotFound, codeNotFound, "批次不存在或已处理", nil)
		return
	}
	var body struct {
		RecordIDs []any `json:"record_ids"`
	}
	_ = decodeJSON(r, &body)
	ids := make([]int64, 0)
	for _, raw := range body.RecordIDs {
		if n, ok := asInt64(raw); ok && n > 0 {
			ids = append(ids, n)
		}
	}
	idFilter := ""
	idArgs := []any{batchID}
	if len(body.RecordIDs) > 0 {
		if len(ids) == 0 {
			writeFail(w, http.StatusBadRequest, codeParam, "record_ids 无效", nil)
			return
		}
		marks := make([]string, 0, len(ids))
		for _, id := range ids {
			marks = append(marks, "?")
			idArgs = append(idArgs, id)
		}
		idFilter = " AND id IN (" + strings.Join(marks, ",") + ")"
	}

	tx, err := m.DB.BeginTx(r.Context(), nil)
	if err != nil {
		writeFail(w, http.StatusInternalServerError, codeParam, "导入失败", nil)
		return
	}
	rows, err := tx.QueryContext(r.Context(),
		`SELECT id, batch_id, crane_record_id, voltage_level, line_name, section, tree_species, tree_count,
		   dist_small, dist_big, diameter, horizontal, vertical, vertical_num, finder_name,
		   DATE_FORMAT(found_at, '%Y-%m-%d'), handler_name, handle_note, DATE_FORMAT(handled_at, '%Y-%m-%d')
		 FROM treehazard_staging_record WHERE batch_id = ?`+idFilter+` ORDER BY id`, idArgs...)
	if err != nil {
		_ = tx.Rollback()
		writeFail(w, http.StatusInternalServerError, codeParam, "导入失败", nil)
		return
	}
	stagingRows := make([]stagingRow, 0)
	for rows.Next() {
		var s stagingRow
		var batch int64
		var craneID, foundAt, handledAt sql.NullString
		var verticalNum sql.NullFloat64
		if err := rows.Scan(&s.ID, &batch, &craneID, &s.VoltageLevel, &s.LineName, &s.Section, &s.TreeSpecies,
			&s.TreeCount, &s.DistSmall, &s.DistBig, &s.Diameter, &s.Horizontal, &s.Vertical, &verticalNum,
			&s.FinderName, &foundAt, &s.HandlerName, &s.HandleNote, &handledAt); err != nil {
			rows.Close()
			_ = tx.Rollback()
			writeFail(w, http.StatusInternalServerError, codeParam, "导入失败", nil)
			return
		}
		if craneID.Valid && craneID.String != "" {
			id := craneID.String
			s.CraneRecordID = &id
		}
		if verticalNum.Valid {
			v := verticalNum.Float64
			s.VerticalNum = &v
		}
		s.FoundAt = foundAt.String
		s.HandledAt = handledAt.String
		stagingRows = append(stagingRows, s)
	}
	rows.Close()

	// 旧数据回退安全条件：本批仅一条空 crane_record_id 暂存行时，空 id 旧照片归属无歧义。
	var emptyIDCnt int64
	_ = tx.QueryRowContext(r.Context(),
		`SELECT COUNT(*) FROM treehazard_staging_record WHERE batch_id = ? AND (crane_record_id IS NULL OR crane_record_id = '')`,
		batchID).Scan(&emptyIDCnt)
	emptyIDSafe := emptyIDCnt == 1

	imported := 0
	for _, s := range stagingRows {
		poleFrom, poleTo := parseSectionPoles(s.Section)
		var existingID int64
		found := false
		if s.CraneRecordID != nil {
			err := tx.QueryRowContext(r.Context(),
				`SELECT id FROM treehazard_record WHERE team_id = ? AND crane_session_id = ? AND crane_record_id = ?`,
				team.ID, sessionID, *s.CraneRecordID).Scan(&existingID)
			found = err == nil
		}
		var recordID int64
		if found {
			if _, err := tx.ExecContext(r.Context(),
				`UPDATE treehazard_record SET voltage_level = ?, line_name = ?, section = ?, pole_from = ?, pole_to = ?,
				   tree_species = ?, tree_count = ?, dist_small = ?, dist_big = ?, diameter = ?, horizontal = ?, vertical = ?,
				   vertical_num = ?, finder_name = ?, found_at = ? WHERE id = ?`,
				s.VoltageLevel, s.LineName, s.Section, poleFrom, poleTo, s.TreeSpecies, s.TreeCount,
				s.DistSmall, s.DistBig, s.Diameter, s.Horizontal, s.Vertical, s.VerticalNum,
				s.FinderName, nullIfEmpty(s.FoundAt), existingID); err != nil {
				_ = tx.Rollback()
				writeFail(w, http.StatusInternalServerError, codeParam, "导入失败", nil)
				return
			}
			recordID = existingID
		} else {
			statusNew := "unhandled"
			if s.HandledAt != "" {
				statusNew = "handled"
			}
			res, err := tx.ExecContext(r.Context(),
				`INSERT INTO treehazard_record (team_id, voltage_level, line_name, section, pole_from, pole_to,
				   tree_species, tree_count, dist_small, dist_big, diameter, horizontal, vertical, vertical_num,
				   finder_name, found_at, handler_name, handle_note, handled_at, status, source, crane_session_id, crane_record_id)
				 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'staging', ?, ?)`,
				team.ID, s.VoltageLevel, s.LineName, s.Section, poleFrom, poleTo, s.TreeSpecies, s.TreeCount,
				s.DistSmall, s.DistBig, s.Diameter, s.Horizontal, s.Vertical, s.VerticalNum, s.FinderName,
				nullIfEmpty(s.FoundAt), s.HandlerName, s.HandleNote, nullIfEmpty(s.HandledAt), statusNew,
				sessionID, s.CraneRecordID)
			if err != nil {
				_ = tx.Rollback()
				writeFail(w, http.StatusInternalServerError, codeParam, "导入失败", nil)
				return
			}
			recordID, _ = res.LastInsertId()
		}

		// 辅证照片随转入归挂（COS 对象不动、仅表行归挂）；更新路径整组替换既有 evidence。
		// 归挂口径：优先按 staging_record_id；旧数据（该列为 NULL）回退按 crane_record_id，
		// 其中空 id 照片仅本批单空记录（emptyIDSafe）时归属无歧义才回退。
		photoConds := []string{"staging_record_id = ?"}
		photoArgs := []any{batchID, s.ID}
		if s.CraneRecordID != nil {
			photoConds = append(photoConds, "(staging_record_id IS NULL AND crane_record_id = ?)")
			photoArgs = append(photoArgs, *s.CraneRecordID)
		} else if emptyIDSafe {
			photoConds = append(photoConds, "(staging_record_id IS NULL AND crane_record_id = '')")
		}
		photoRows, err := tx.QueryContext(r.Context(),
			`SELECT id, cos_key, url FROM treehazard_staging_photo WHERE batch_id = ? AND (`+
				strings.Join(photoConds, " OR ")+`)`, photoArgs...)
		type stagePhoto struct {
			id     int64
			cosKey string
			url    string
		}
		sps := make([]stagePhoto, 0)
		if err == nil {
			for photoRows.Next() {
				var p stagePhoto
				if photoRows.Scan(&p.id, &p.cosKey, &p.url) == nil {
					sps = append(sps, p)
				}
			}
			photoRows.Close()
		}
		if len(sps) > 0 {
			if found {
				oldRows, err := tx.QueryContext(r.Context(),
					`SELECT id, cos_key FROM treehazard_photo WHERE record_id = ? AND kind = 'evidence'`, recordID)
				if err == nil {
					for oldRows.Next() {
						var oldKey string
						var oldID int64
						if oldRows.Scan(&oldID, &oldKey) == nil && m.Objects != nil && oldKey != "" {
							_ = m.Objects.Delete(r.Context(), oldKey)
						}
					}
					oldRows.Close()
				}
				if _, err := tx.ExecContext(r.Context(),
					`DELETE FROM treehazard_photo WHERE record_id = ? AND kind = 'evidence'`, recordID); err != nil {
					_ = tx.Rollback()
					writeFail(w, http.StatusInternalServerError, codeParam, "导入失败", nil)
					return
				}
			}
			for _, p := range sps {
				if _, err := tx.ExecContext(r.Context(),
					`INSERT INTO treehazard_photo (record_id, kind, cos_key, url, uploaded_by) VALUES (?, 'evidence', ?, ?, NULL)`,
					recordID, p.cosKey, m.currentPhotoUrl(p.cosKey, p.url)); err != nil {
					_ = tx.Rollback()
					writeFail(w, http.StatusInternalServerError, codeParam, "导入失败", nil)
					return
				}
			}
			photoIDs := make([]any, 0, len(sps))
			marks := make([]string, 0, len(sps))
			for _, p := range sps {
				photoIDs = append(photoIDs, p.id)
				marks = append(marks, "?")
			}
			if _, err := tx.ExecContext(r.Context(),
				`DELETE FROM treehazard_staging_photo WHERE id IN (`+strings.Join(marks, ",")+`)`, photoIDs...); err != nil {
				_ = tx.Rollback()
				writeFail(w, http.StatusInternalServerError, codeParam, "导入失败", nil)
				return
			}
		}
		if _, err := tx.ExecContext(r.Context(),
			`DELETE FROM treehazard_staging_record WHERE id = ?`, s.ID); err != nil {
			_ = tx.Rollback()
			writeFail(w, http.StatusInternalServerError, codeParam, "导入失败", nil)
			return
		}
		imported++
	}
	var left int64
	_ = tx.QueryRowContext(r.Context(),
		`SELECT COUNT(*) FROM treehazard_staging_record WHERE batch_id = ?`, batchID).Scan(&left)
	if left == 0 {
		if _, err := tx.ExecContext(r.Context(),
			`UPDATE treehazard_staging_batch SET status = 'imported' WHERE id = ?`, batchID); err != nil {
			_ = tx.Rollback()
			writeFail(w, http.StatusInternalServerError, codeParam, "导入失败", nil)
			return
		}
	}
	if err := tx.Commit(); err != nil {
		writeFail(w, http.StatusInternalServerError, codeParam, "导入失败", nil)
		return
	}
	writeOK(w, map[string]any{"imported": imported}, "")
}

// handleStagingDelete DELETE /staging/batches/:id：物理删除批次与全部记录行
// （随批 xlsx 与暂存辅证照片一并清理，失败仅记日志）。
func (m *Module) handleStagingDelete(w http.ResponseWriter, r *http.Request) {
	team, _, ok := requireManagerNow(w, r)
	if !ok {
		return
	}
	batchID, ok := pathID(r)
	if !ok {
		writeFail(w, http.StatusBadRequest, codeParam, "id 无效", nil)
		return
	}
	var xlsxPath string
	err := m.DB.QueryRowContext(r.Context(),
		`SELECT xlsx_path FROM treehazard_staging_batch WHERE id = ? AND team_id = ?`, batchID, team.ID).Scan(&xlsxPath)
	if err != nil {
		writeFail(w, http.StatusNotFound, codeNotFound, "批次不存在", nil)
		return
	}
	m.clearStagingPhotos(r.Context(), batchID)
	if _, err := m.DB.ExecContext(r.Context(),
		`DELETE FROM treehazard_staging_record WHERE batch_id = ?`, batchID); err != nil {
		writeFail(w, http.StatusInternalServerError, codeParam, "批次删除失败", nil)
		return
	}
	if _, err := m.DB.ExecContext(r.Context(),
		`DELETE FROM treehazard_staging_batch WHERE id = ?`, batchID); err != nil {
		writeFail(w, http.StatusInternalServerError, codeParam, "批次删除失败", nil)
		return
	}
	if xlsxPath != "" && m.Files != nil {
		if err := m.Files.Remove(r.Context(), xlsxPath); err != nil {
			fmt.Printf("[树障] 清理暂存 xlsx 失败：%v\n", err)
		}
	}
	writeOK(w, map[string]any{"id": batchID}, "")
}
