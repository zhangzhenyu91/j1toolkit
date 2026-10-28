package crossing

// 手动新增与编辑（班管/超管，权限沿用 DELETE /records/:id 的 enterAdmin 管理口径）：
// POST /records 手动录入一条记录（source='manual'，created_by 记创建人，crane 幂等双 NULL——
// uk_crane 不约束 NULL，与直推/暂存/内网行并存不撞唯一键）；PUT /records/:id 原地编辑业务列
// （保记录 ID、source(kind)、crane 幂等键、synced_at 同步点与 created_by 不动；updated_at 随更新前移，
// 下一轮内网同步按「服务端变更」下发收敛），WHERE id = ? AND team_id = ? 防越班。
// 业务列清洗沿用直推口径（文本按列宽截断、inspect_at 日期归一），区段原文入库（带字母杆号不剔除），
// pole_from/pole_to 数值锚点沿用 parseSectionPoles 从区段文本解析（带字母杆号解析不出即置空）。

import (
	"net/http"
)

// manualFieldOrder 手动录入/编辑可写业务列（列宽复用 stagingEditable，日期列单独归一）。
var manualFieldOrder = []string{
	"voltage_level", "line_name", "section", "crossing_type", "dist_small", "dist_big",
	"cross_horizontal", "cross_vertical", "cross_angle", "has_hazard", "finder_name", "remark",
}

// recordCreate POST /records：手动新增（body 为业务列白名单，line_name 必填；inspect_at 缺省/无法识别存 NULL）。
func (m *Module) recordCreate(w http.ResponseWriter, r *http.Request) {
	ident, team, ok := m.enterAdmin(w, r, 0)
	if !ok {
		return
	}
	if team == nil {
		fail(w, http.StatusForbidden, codeNoTeamDeep, "未分配班组，请联系管理员分配")
		return
	}
	body, err := decodeBody(r)
	if err != nil {
		fail(w, http.StatusBadRequest, codeParam, "请求体不是合法 JSON")
		return
	}
	fields := map[string]string{}
	for _, col := range manualFieldOrder {
		fields[col] = ""
		if raw, present := body[col]; present {
			fields[col] = truncate(rawString(raw), stagingEditable[col])
		}
	}
	if fields["line_name"] == "" {
		fail(w, http.StatusBadRequest, codeParam, "参数错误：线路名称不能为空")
		return
	}
	var inspectAt any
	if raw, present := body["inspect_at"]; present {
		inspectAt = nullIfEmpty(normDateStr(rawString(raw)))
	}
	poleFrom, poleTo := parseSectionPoles(fields["section"])
	res, err := m.DB.ExecContext(r.Context(),
		`INSERT INTO crossing_record (team_id, voltage_level, line_name, section, pole_from, pole_to, crossing_type,
       dist_small, dist_big, cross_horizontal, cross_vertical, cross_angle, has_hazard,
       finder_name, inspect_at, remark, source, created_by)
     VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'manual', ?)`,
		team.ID, fields["voltage_level"], fields["line_name"], fields["section"], poleFrom, poleTo, fields["crossing_type"],
		fields["dist_small"], fields["dist_big"], fields["cross_horizontal"], fields["cross_vertical"], fields["cross_angle"],
		fields["has_hazard"], fields["finder_name"], inspectAt, fields["remark"], ident.ID)
	if err != nil {
		writeErr(w, err)
		return
	}
	id, _ := res.LastInsertId()
	writeOK(w, map[string]any{"id": id}, "已新增")
}

// recordUpdate PUT /records/:id：原地编辑业务列（body 出现哪个键改哪个键；line_name 出现时不得为空）。
// 区段变更时 pole_from/pole_to 数值锚点随区段文本重解析（解析不出置空，区段原文不改写）。
func (m *Module) recordUpdate(w http.ResponseWriter, r *http.Request) {
	_, team, ok := m.enterAdmin(w, r, 0)
	if !ok {
		return
	}
	if team == nil {
		fail(w, http.StatusForbidden, codeNoTeamDeep, "未分配班组，请联系管理员分配")
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
		"SELECT id FROM crossing_record WHERE id = ? AND team_id = ?", id, team.ID).Scan(&exist); err != nil {
		fail(w, http.StatusNotFound, codeMissing, "记录不存在")
		return
	}
	sets := []string{}
	args := []any{}
	section, sectionPresent := "", false
	for _, col := range manualFieldOrder {
		raw, present := body[col]
		if !present {
			continue
		}
		val := truncate(rawString(raw), stagingEditable[col])
		if col == "line_name" && val == "" {
			fail(w, http.StatusBadRequest, codeParam, "参数错误：线路名称不能为空")
			return
		}
		sets = append(sets, col+" = ?")
		args = append(args, val)
		if col == "section" {
			section, sectionPresent = val, true
		}
	}
	if raw, present := body["inspect_at"]; present {
		sets = append(sets, "inspect_at = ?")
		args = append(args, nullIfEmpty(normDateStr(rawString(raw))))
	}
	if sectionPresent {
		poleFrom, poleTo := parseSectionPoles(section)
		sets = append(sets, "pole_from = ?", "pole_to = ?")
		args = append(args, poleFrom, poleTo)
	}
	if len(sets) == 0 {
		fail(w, http.StatusBadRequest, codeParam, "没有可更新的字段")
		return
	}
	args = append(args, id, team.ID)
	if _, err := m.DB.ExecContext(r.Context(),
		"UPDATE crossing_record SET "+joinComma(sets)+" WHERE id = ? AND team_id = ?", args...); err != nil {
		writeErr(w, err)
		return
	}
	writeOK(w, map[string]any{"id": id}, "已保存")
}
