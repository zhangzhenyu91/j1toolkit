package crossing

// 冲突（班管/超管；内网同步产生，人工二选一解决，与 server/src/crossing/index.js 同口径）：
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

// conflictsList GET /conflicts：本班 pending 冲突列表（带 record 当前值）。
func (m *Module) conflictsList(w http.ResponseWriter, r *http.Request) {
	_, team, ok := m.enterAdmin(w, r, 0)
	if !ok {
		return
	}
	if team == nil {
		fail(w, http.StatusBadRequest, codeNoTeam, "无可用班组")
		return
	}
	rows, err := m.DB.QueryContext(r.Context(),
		`SELECT c.id, c.team_id, c.record_id, c.server_snapshot, c.intranet_snapshot, c.status, c.created_at,
		   r.voltage_level, r.line_name, r.section, r.crossing_type, r.dist_small, r.dist_big, r.cross_horizontal,
		   r.cross_vertical, r.cross_angle, r.has_hazard, r.finder_name,
		   DATE_FORMAT(r.inspect_at, '%Y-%m-%d') AS inspect_at, r.remark
		 FROM crossing_conflict c LEFT JOIN crossing_record r ON r.id = c.record_id
		 WHERE c.team_id = ? AND c.status = 'pending' ORDER BY c.id`, team.ID)
	if err != nil {
		writeErr(w, err)
		return
	}
	defer rows.Close()
	data := make([]map[string]any, 0)
	for rows.Next() {
		var (
			id, teamID                int64
			recordID                  sql.NullInt64
			serverSnap, intranetSnap  sql.NullString
			status, createdAt         string
			voltageLevel, lineName    sql.NullString
			section, crossingType     sql.NullString
			distSmall, distBig        sql.NullString
			crossHorizontal           sql.NullString
			crossVertical, crossAngle sql.NullString
			hasHazard, finderName     sql.NullString
			inspectAt, remark         sql.NullString
		)
		if err := rows.Scan(&id, &teamID, &recordID, &serverSnap, &intranetSnap, &status, &createdAt,
			&voltageLevel, &lineName, &section, &crossingType, &distSmall, &distBig, &crossHorizontal,
			&crossVertical, &crossAngle, &hasHazard, &finderName, &inspectAt, &remark); err != nil {
			writeErr(w, err)
			return
		}
		item := map[string]any{
			"id": id, "team_id": teamID,
			"server_snapshot":   parseSnapshot(serverSnap.String),
			"intranet_snapshot": parseSnapshot(intranetSnap.String),
			"status":            status, "created_at": createdAt,
			"voltage_level": nullStr(voltageLevel), "line_name": nullStr(lineName),
			"section": nullStr(section), "crossing_type": nullStr(crossingType),
			"dist_small": nullStr(distSmall), "dist_big": nullStr(distBig),
			"cross_horizontal": nullStr(crossHorizontal), "cross_vertical": nullStr(crossVertical),
			"cross_angle": nullStr(crossAngle), "has_hazard": nullStr(hasHazard),
			"finder_name": nullStr(finderName), "inspect_at": nullStr(inspectAt),
			"remark": nullStr(remark),
		}
		if recordID.Valid {
			item["record_id"] = recordID.Int64
		} else {
			item["record_id"] = nil
		}
		data = append(data, item)
	}
	writeOK(w, data)
}

// conflictResolve POST /conflicts/:id/resolve：body { keep: 'server'|'intranet' }。
func (m *Module) conflictResolve(w http.ResponseWriter, r *http.Request) {
	ident, team, ok := m.enterAdmin(w, r, 0)
	if !ok {
		return
	}
	if team == nil {
		fail(w, http.StatusBadRequest, codeNoTeam, "无可用班组")
		return
	}
	var body struct {
		Keep string `json:"keep"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	keep := strings.TrimSpace(body.Keep)
	if keep != "server" && keep != "intranet" {
		fail(w, http.StatusBadRequest, codeParam, "keep 应为 server 或 intranet")
		return
	}
	conflictID := pathID(r)
	if conflictID <= 0 {
		fail(w, http.StatusBadRequest, codeParam, "id 无效")
		return
	}
	var (
		recordID     sql.NullInt64
		intranetSnap sql.NullString
	)
	if err := m.DB.QueryRowContext(r.Context(),
		"SELECT record_id, intranet_snapshot FROM crossing_conflict WHERE id = ? AND team_id = ? AND status = 'pending'",
		conflictID, team.ID).Scan(&recordID, &intranetSnap); err != nil {
		fail(w, http.StatusNotFound, codeMissing, "冲突不存在或已解决")
		return
	}

	tx, err := m.DB.BeginTx(r.Context(), nil)
	if err != nil {
		writeErr(w, err)
		return
	}
	if recordID.Valid {
		if keep == "intranet" {
			// 快照列名与内网行对象同形（ParseIntranetSheet 产物），复用 intranetFields 归一
			fields := intranetFields(parseSnapshot(intranetSnap.String))
			if _, err := tx.ExecContext(r.Context(),
				`UPDATE crossing_record SET voltage_level = ?, line_name = ?, section = ?, crossing_type = ?,
				   dist_small = ?, dist_big = ?, cross_horizontal = ?, cross_vertical = ?, cross_angle = ?, has_hazard = ?,
				   finder_name = ?, inspect_at = ?, remark = ?, synced_at = NOW()
				 WHERE id = ?`,
				fields["voltage_level"], fields["line_name"], fields["section"], fields["crossing_type"],
				fields["dist_small"], fields["dist_big"], fields["cross_horizontal"], fields["cross_vertical"],
				fields["cross_angle"], fields["has_hazard"], fields["finder_name"], fields["inspect_at"],
				fields["remark"], recordID.Int64); err != nil {
				_ = tx.Rollback()
				writeErr(w, err)
				return
			}
		} else {
			if _, err := tx.ExecContext(r.Context(),
				"UPDATE crossing_record SET synced_at = NOW() WHERE id = ?", recordID.Int64); err != nil {
				_ = tx.Rollback()
				writeErr(w, err)
				return
			}
		}
	}
	if _, err := tx.ExecContext(r.Context(),
		"UPDATE crossing_conflict SET status = 'resolved', resolution = ?, resolved_by = ?, resolved_at = NOW() WHERE id = ?",
		keep, ident.ID, conflictID); err != nil {
		_ = tx.Rollback()
		writeErr(w, err)
		return
	}
	if err := tx.Commit(); err != nil {
		writeErr(w, err)
		return
	}
	writeOK(w, map[string]any{"id": conflictID})
}
