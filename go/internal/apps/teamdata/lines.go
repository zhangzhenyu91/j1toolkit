package teamdata

import (
	"database/sql"
	"net/http"
)

// lineAgg GET /lines：本班线路聚合（按 worklog_tower 导入序），
// [{ voltage_level, line_name, tower_count }]；无班组返回空数组。
func (m *Module) lineAgg(w http.ResponseWriter, r *http.Request) {
	_, team, ok := m.enter(w, r, 0, false)
	if !ok {
		return
	}
	if team == nil {
		writeOK(w, []any{})
		return
	}
	rows, err := m.DB.QueryContext(r.Context(),
		`SELECT voltage_level, line_name, COUNT(*) AS tower_count
		 FROM worklog_tower WHERE team_id = ? GROUP BY voltage_level, line_name ORDER BY MIN(sort)`,
		team.ID)
	if err != nil {
		writeErr(w, err)
		return
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var voltage, line string
		var count int64
		if err := rows.Scan(&voltage, &line, &count); err != nil {
			writeErr(w, err)
			return
		}
		out = append(out, map[string]any{"voltage_level": voltage, "line_name": line, "tower_count": count})
	}
	if err := rows.Err(); err != nil {
		writeErr(w, err)
		return
	}
	writeOK(w, out)
}

// towerList GET /towers：本班全部杆塔行（管理视图，含 id 与 sort）；无班组返回空数组。
func (m *Module) towerList(w http.ResponseWriter, r *http.Request) {
	_, team, ok := m.enter(w, r, 0, false)
	if !ok {
		return
	}
	if team == nil {
		writeOK(w, []any{})
		return
	}
	rows, err := m.DB.QueryContext(r.Context(),
		`SELECT id, voltage_level, line_name, tower_no, lng, lat, sort
		 FROM worklog_tower WHERE team_id = ? ORDER BY sort, id`, team.ID)
	if err != nil {
		writeErr(w, err)
		return
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var id, sort int64
		var voltage, line, towerNo, lng, lat string
		if err := rows.Scan(&id, &voltage, &line, &towerNo, &lng, &lat, &sort); err != nil {
			writeErr(w, err)
			return
		}
		out = append(out, map[string]any{
			"id": id, "voltage_level": voltage, "line_name": line,
			"tower_no": towerNo, "lng": lng, "lat": lat, "sort": sort,
		})
	}
	if err := rows.Err(); err != nil {
		writeErr(w, err)
		return
	}
	writeOK(w, out)
}

// oplineList GET /oplines：本班运维线路（按 sort），{ list: [...] }；无班组返回空列表。
func (m *Module) oplineList(w http.ResponseWriter, r *http.Request) {
	_, team, ok := m.enter(w, r, 0, false)
	if !ok {
		return
	}
	if team == nil {
		writeOK(w, map[string]any{"list": []any{}})
		return
	}
	rows, err := m.DB.QueryContext(r.Context(),
		`SELECT id, voltage_level, line_name, sort, created_at, updated_at
		 FROM team_line WHERE team_id = ? ORDER BY sort, id`, team.ID)
	if err != nil {
		writeErr(w, err)
		return
	}
	defer rows.Close()
	list := []map[string]any{}
	for rows.Next() {
		var id, sort int64
		var voltage, line string
		var createdAt, updatedAt any
		if err := rows.Scan(&id, &voltage, &line, &sort, &createdAt, &updatedAt); err != nil {
			writeErr(w, err)
			return
		}
		list = append(list, map[string]any{
			"id": id, "voltage_level": voltage, "line_name": line,
			"sort": sort, "created_at": createdAt, "updated_at": updatedAt,
		})
	}
	if err := rows.Err(); err != nil {
		writeErr(w, err)
		return
	}
	writeOK(w, map[string]any{"list": list})
}

// oplineCreate POST /oplines {voltage_level, line_name}：新增线路（sort 排尾；班内 line_name 唯一）。
func (m *Module) oplineCreate(w http.ResponseWriter, r *http.Request) {
	_, team, ok := m.enter(w, r, 0, false)
	if !ok {
		return
	}
	if team == nil {
		fail(w, http.StatusBadRequest, codeNoTeam, "无可用班组")
		return
	}
	body, err := decodeBody(r)
	if err != nil {
		fail(w, http.StatusBadRequest, codeParam, "请求体不是合法 JSON")
		return
	}
	voltage := truncate(rawString(body["voltage_level"]), 16)
	lineName := truncate(rawString(body["line_name"]), 64)
	if voltage == "" {
		fail(w, http.StatusBadRequest, codeInput, "请输入电压等级")
		return
	}
	if lineName == "" {
		fail(w, http.StatusBadRequest, codeInput, "请输入线路名称")
		return
	}
	var maxSort int64
	if err := m.DB.QueryRowContext(r.Context(),
		"SELECT COALESCE(MAX(sort), 0) FROM team_line WHERE team_id = ?", team.ID).Scan(&maxSort); err != nil {
		writeErr(w, err)
		return
	}
	res, err := m.DB.ExecContext(r.Context(),
		"INSERT INTO team_line (team_id, voltage_level, line_name, sort) VALUES (?, ?, ?, ?)",
		team.ID, voltage, lineName, maxSort+1)
	if err != nil {
		if isDupEntry(err) {
			fail(w, http.StatusConflict, codeDup, "「"+lineName+"」已存在")
			return
		}
		writeErr(w, err)
		return
	}
	id, _ := res.LastInsertId()
	writeOK(w, map[string]any{"id": id})
}

// oplineUpdate PUT /oplines/{id} {voltage_level?, line_name?}：改电压等级/线路名（仅更新出现的字段）。
func (m *Module) oplineUpdate(w http.ResponseWriter, r *http.Request) {
	_, team, ok := m.enter(w, r, 0, false)
	if !ok {
		return
	}
	if team == nil {
		fail(w, http.StatusBadRequest, codeNoTeam, "无可用班组")
		return
	}
	id := pathID(r)
	var exist int64
	err := m.DB.QueryRowContext(r.Context(),
		"SELECT id FROM team_line WHERE id = ? AND team_id = ?", id, team.ID).Scan(&exist)
	if err != nil {
		if err == sql.ErrNoRows {
			fail(w, http.StatusNotFound, codeMissing, "线路不存在")
			return
		}
		writeErr(w, err)
		return
	}
	body, err := decodeBody(r)
	if err != nil {
		fail(w, http.StatusBadRequest, codeParam, "请求体不是合法 JSON")
		return
	}
	if raw, present := body["voltage_level"]; present {
		voltage := truncate(rawString(raw), 16)
		if voltage == "" {
			fail(w, http.StatusBadRequest, codeInput, "请输入电压等级")
			return
		}
		if _, err := m.DB.ExecContext(r.Context(),
			"UPDATE team_line SET voltage_level = ? WHERE id = ?", voltage, id); err != nil {
			writeErr(w, err)
			return
		}
	}
	if raw, present := body["line_name"]; present {
		lineName := truncate(rawString(raw), 64)
		if lineName == "" {
			fail(w, http.StatusBadRequest, codeInput, "请输入线路名称")
			return
		}
		if _, err := m.DB.ExecContext(r.Context(),
			"UPDATE team_line SET line_name = ? WHERE id = ?", lineName, id); err != nil {
			if isDupEntry(err) {
				fail(w, http.StatusConflict, codeDup, "「"+lineName+"」已存在")
				return
			}
			writeErr(w, err)
			return
		}
	}
	writeOK(w, nil)
}

// oplineDelete DELETE /oplines/{id}：删除线路（无外键引用；树障记录按 line_name 文本关联，
// 删线后其记录落 unmatched，不拦截）。
func (m *Module) oplineDelete(w http.ResponseWriter, r *http.Request) {
	_, team, ok := m.enter(w, r, 0, false)
	if !ok {
		return
	}
	if team == nil {
		fail(w, http.StatusBadRequest, codeNoTeam, "无可用班组")
		return
	}
	id := pathID(r)
	res, err := m.DB.ExecContext(r.Context(),
		"DELETE FROM team_line WHERE id = ? AND team_id = ?", id, team.ID)
	if err != nil {
		writeErr(w, err)
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		fail(w, http.StatusNotFound, codeMissing, "线路不存在")
		return
	}
	writeOK(w, nil)
}

// moveOpline PUT /oplines/{id}/move：线路排序（与 members move 共用 moveRow）。
func (m *Module) moveOpline(w http.ResponseWriter, r *http.Request) {
	m.moveRow(w, r, "team_line", "线路")
}
