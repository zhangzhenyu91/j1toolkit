package treehazard

// 冲突（班管/超管；内网同步产生，人工二选一解决，与 server/src/treehazard/index.js 同口径）：
// GET /conflicts 本班 pending 冲突列表（带 record 当前值与双方快照）；
// POST /conflicts/:id/resolve body { keep: 'server'|'intranet' }——keep=intranet 用 intranet_snapshot
// 业务列覆盖记录并落 synced_at=NOW；keep=server 仅落 synced_at=NOW；随后冲突行置 resolved。

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"strings"
)

// parseSnapshot 解析冲突快照 JSON（NULL/非法 → 空对象）。
func parseSnapshot(raw string) map[string]any {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return map[string]any{}
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(raw), &out); err != nil || out == nil {
		return map[string]any{}
	}
	return out
}

// nullStr 输出用：NULL → nil，其余为字符串。
func nullStr(v sql.NullString) any {
	if v.Valid {
		return v.String
	}
	return nil
}

// handleConflicts GET /conflicts：本班 pending 冲突列表（带 record 当前值）。
func (m *Module) handleConflicts(w http.ResponseWriter, r *http.Request) {
	team, _, ok := requireManagerNow(w, r)
	if !ok {
		return
	}
	rows, err := m.DB.QueryContext(r.Context(),
		`SELECT c.id, c.team_id, c.record_id, c.server_snapshot, c.intranet_snapshot, c.status, c.created_at,
		   r.voltage_level, r.line_name, r.section, r.tree_species, r.tree_count, r.dist_small, r.dist_big, r.diameter,
		   r.horizontal, r.vertical, r.vertical_num, r.finder_name, DATE_FORMAT(r.found_at, '%Y-%m-%d') AS found_at,
		   r.handler_name, r.handle_note, DATE_FORMAT(r.handled_at, '%Y-%m-%d') AS handled_at, r.status AS record_status
		 FROM treehazard_conflict c LEFT JOIN treehazard_record r ON r.id = c.record_id
		 WHERE c.team_id = ? AND c.status = 'pending' ORDER BY c.id`, team.ID)
	if err != nil {
		writeFail(w, http.StatusInternalServerError, codeParam, "查询失败", nil)
		return
	}
	defer rows.Close()
	data := make([]map[string]any, 0)
	for rows.Next() {
		var (
			id, teamID               int64
			recordID                 sql.NullInt64
			serverSnap, intranetSnap sql.NullString
			status, createdAt        string
			voltageLevel, lineName   sql.NullString
			section, treeSpecies     sql.NullString
			treeCount, distSmall     sql.NullString
			distBig, diameter        sql.NullString
			horizontal, vertical     sql.NullString
			verticalNum              sql.NullFloat64
			finderName, foundAt      sql.NullString
			handlerName, handleNote  sql.NullString
			handledAt, recordStatus  sql.NullString
		)
		if err := rows.Scan(&id, &teamID, &recordID, &serverSnap, &intranetSnap, &status, &createdAt,
			&voltageLevel, &lineName, &section, &treeSpecies, &treeCount, &distSmall, &distBig, &diameter,
			&horizontal, &vertical, &verticalNum, &finderName, &foundAt,
			&handlerName, &handleNote, &handledAt, &recordStatus); err != nil {
			writeFail(w, http.StatusInternalServerError, codeParam, "查询失败", nil)
			return
		}
		item := map[string]any{
			"id": id, "team_id": teamID,
			"server_snapshot":   parseSnapshot(serverSnap.String),
			"intranet_snapshot": parseSnapshot(intranetSnap.String),
			"status":            status, "created_at": createdAt,
			"voltage_level": nullStr(voltageLevel), "line_name": nullStr(lineName),
			"section": nullStr(section), "tree_species": nullStr(treeSpecies),
			"tree_count": nullStr(treeCount), "dist_small": nullStr(distSmall),
			"dist_big": nullStr(distBig), "diameter": nullStr(diameter),
			"horizontal": nullStr(horizontal), "vertical": nullStr(vertical),
			"vertical_num": numOrNil(verticalNum), "finder_name": nullStr(finderName),
			"found_at": nullStr(foundAt), "handler_name": nullStr(handlerName),
			"handle_note": nullStr(handleNote), "handled_at": nullStr(handledAt),
			"record_status": nullStr(recordStatus),
		}
		if recordID.Valid {
			item["record_id"] = recordID.Int64
		} else {
			item["record_id"] = nil
		}
		data = append(data, item)
	}
	writeOK(w, data, "")
}

// handleConflictResolve POST /conflicts/:id/resolve：body { keep: 'server'|'intranet' }。
func (m *Module) handleConflictResolve(w http.ResponseWriter, r *http.Request) {
	team, ident, ok := requireManagerNow(w, r)
	if !ok {
		return
	}
	var body struct {
		Keep string `json:"keep"`
	}
	_ = decodeJSON(r, &body)
	keep := strings.TrimSpace(body.Keep)
	if keep != "server" && keep != "intranet" {
		writeFail(w, http.StatusBadRequest, codeParam, "keep 应为 server 或 intranet", nil)
		return
	}
	conflictID, ok := pathID(r)
	if !ok {
		writeFail(w, http.StatusBadRequest, codeParam, "id 无效", nil)
		return
	}
	var (
		recordID     sql.NullInt64
		intranetSnap sql.NullString
	)
	err := m.DB.QueryRowContext(r.Context(),
		`SELECT record_id, intranet_snapshot FROM treehazard_conflict WHERE id = ? AND team_id = ? AND status = 'pending'`,
		conflictID, team.ID).Scan(&recordID, &intranetSnap)
	if err != nil {
		writeFail(w, http.StatusNotFound, codeNotFound, "冲突不存在或已解决", nil)
		return
	}

	tx, err := m.DB.BeginTx(r.Context(), nil)
	if err != nil {
		writeFail(w, http.StatusInternalServerError, codeParam, "裁决失败", nil)
		return
	}
	if recordID.Valid {
		if keep == "intranet" {
			// 快照列名与内网行对象同形（ParseIntranetSheet 产物），复用 intranetFields 归一
			fields := intranetFields(parseSnapshot(intranetSnap.String))
			if _, err := tx.ExecContext(r.Context(),
				`UPDATE treehazard_record SET voltage_level = ?, line_name = ?, section = ?, tree_species = ?, tree_count = ?,
				   dist_small = ?, dist_big = ?, diameter = ?, horizontal = ?, vertical = ?, finder_name = ?, found_at = ?,
				   handler_name = ?, handle_note = ?, handled_at = ?, vertical_num = ?, status = ?, synced_at = NOW()
				 WHERE id = ?`,
				fields["voltage_level"], fields["line_name"], fields["section"], fields["tree_species"], fields["tree_count"],
				fields["dist_small"], fields["dist_big"], fields["diameter"], fields["horizontal"], fields["vertical"],
				fields["finder_name"], fields["found_at"], fields["handler_name"], fields["handle_note"], fields["handled_at"],
				fields["vertical_num"], fields["status"], recordID.Int64); err != nil {
				_ = tx.Rollback()
				writeFail(w, http.StatusInternalServerError, codeParam, "裁决失败", nil)
				return
			}
		} else {
			if _, err := tx.ExecContext(r.Context(),
				`UPDATE treehazard_record SET synced_at = NOW() WHERE id = ?`, recordID.Int64); err != nil {
				_ = tx.Rollback()
				writeFail(w, http.StatusInternalServerError, codeParam, "裁决失败", nil)
				return
			}
		}
	}
	if _, err := tx.ExecContext(r.Context(),
		`UPDATE treehazard_conflict SET status = 'resolved', resolution = ?, resolved_by = ?, resolved_at = NOW() WHERE id = ?`,
		keep, ident.ID, conflictID); err != nil {
		_ = tx.Rollback()
		writeFail(w, http.StatusInternalServerError, codeParam, "裁决失败", nil)
		return
	}
	if err := tx.Commit(); err != nil {
		writeFail(w, http.StatusInternalServerError, codeParam, "裁决失败", nil)
		return
	}
	writeOK(w, map[string]any{"id": conflictID}, "")
}
