// 字典管理（车牌 / 目的地 / 成员）+ 杆塔坐标 + 地理取值 + 派车单同步开关。
// 三类字典同构维护，按生效班组隔离；超管可管任意班组（?team_id=），班组管理员仅本班。
package worklog

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"shade/internal/auth"
	"shade/internal/compat/exceldoc"
	"shade/internal/httpapi"
)

// dictSpec 字典规格（路径 / 表 / 名称列 / 标签 / 引用计数）。
type dictSpec struct {
	path      string
	table     string
	field     string
	label     string
	nameLen   int
	countRefs func(m *Module, r *http.Request, id int64) int
	extraCols []string // 列表附加列（目的地 out_of_city/stationed、成员 user_id）
}

var dictSpecs = map[string]dictSpec{
	"vehicles": {
		path: "vehicles", table: "worklog_vehicle", field: "plate_no", label: "车牌", nameLen: 32,
		countRefs: func(m *Module, r *http.Request, id int64) int {
			var cnt int
			_ = m.DB.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM worklog_entry WHERE vehicle_id = ?`, id).Scan(&cnt)
			return cnt
		},
	},
	"destinations": {
		path: "destinations", table: "worklog_destination", field: "name", label: "目的地", nameLen: 64,
		countRefs: func(m *Module, r *http.Request, id int64) int {
			var cnt int
			_ = m.DB.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM worklog_entry WHERE destination_id = ?`, id).Scan(&cnt)
			return cnt
		},
		extraCols: []string{"out_of_city", "stationed"},
	},
	"members": {
		path: "members", table: "worklog_member", field: "name", label: "成员", nameLen: 64,
		countRefs: func(m *Module, r *http.Request, id int64) int {
			var cnt int
			_ = m.DB.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM worklog_entry_member WHERE member_id = ?`, id).Scan(&cnt)
			return cnt
		},
		extraCols: []string{"user_id"},
	},
}

// enterDict 字典管理门控（权限 + 班组）。
func (m *Module) enterDict(w http.ResponseWriter, r *http.Request) (auth.Identity, *httpapi.Team, bool) {
	ident, team, ok := m.enter(w, r)
	if !ok {
		return ident, nil, false
	}
	if !requireDictAdmin(w, ident, team) {
		return ident, nil, false
	}
	return ident, team, true
}

// handleDictList GET /admin/{path}：字典列表（按生效班组）。
func (m *Module) dictList(w http.ResponseWriter, r *http.Request, spec dictSpec) {
	_, team, ok := m.enterDict(w, r)
	if !ok {
		return
	}
	if team == nil {
		writeOK(w, map[string]any{"list": []any{}})
		return
	}
	extra := ""
	for _, c := range spec.extraCols {
		extra += ", " + c
	}
	list := queryDictRows(r.Context(), m.DB,
		`SELECT id, `+spec.field+` AS name, sort, status`+extra+` FROM `+spec.table+` WHERE team_id = ? ORDER BY sort, id`,
		team.ID)
	writeOK(w, map[string]any{"list": list})
}

// dictCreate POST /admin/{path}：新增字典（班组内名称唯一）。
func (m *Module) dictCreate(w http.ResponseWriter, r *http.Request, spec dictSpec) {
	_, team, ok := m.enterDict(w, r)
	if !ok {
		return
	}
	if team == nil {
		fail(w, http.StatusBadRequest, codeNoUsableTeam, "无可用班组")
		return
	}
	body, err := decodeBody(r)
	if err != nil {
		fail(w, http.StatusBadRequest, codeInput, "参数错误")
		return
	}
	name := truncate(strings.TrimSpace(rawString(body["name"])), spec.nameLen)
	if name == "" {
		fail(w, http.StatusBadRequest, codeDictName, "请输入"+spec.label+"名称")
		return
	}
	var maxSort int
	_ = m.DB.QueryRowContext(r.Context(),
		`SELECT COALESCE(MAX(sort), 0) FROM `+spec.table+` WHERE team_id = ?`, team.ID).Scan(&maxSort)
	res, err := m.DB.ExecContext(r.Context(),
		`INSERT INTO `+spec.table+` (team_id, `+spec.field+`, sort) VALUES (?, ?, ?)`,
		team.ID, name, maxSort+1)
	if err != nil {
		if isDupEntry(err) {
			fail(w, http.StatusConflict, codeDupName, "「"+name+"」已存在")
			return
		}
		writeErr(w, err)
		return
	}
	id, _ := res.LastInsertId()
	writeOK(w, map[string]any{"id": id})
}

// dictUpdate PUT /admin/{path}/{id}：改名 / 排序 / 启停 / 市外驻地标记。
func (m *Module) dictUpdate(w http.ResponseWriter, r *http.Request, spec dictSpec) {
	_, team, ok := m.enterDict(w, r)
	if !ok {
		return
	}
	if team == nil {
		fail(w, http.StatusBadRequest, codeNoUsableTeam, "无可用班组")
		return
	}
	id, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	var exist int
	if err := m.DB.QueryRowContext(r.Context(),
		`SELECT id FROM `+spec.table+` WHERE id = ? AND team_id = ?`, id, team.ID).Scan(&exist); err != nil {
		fail(w, http.StatusNotFound, codeMissing, spec.label+"不存在")
		return
	}
	body, err := decodeBody(r)
	if err != nil {
		fail(w, http.StatusBadRequest, codeInput, "参数错误")
		return
	}
	if raw, exists := body["name"]; exists {
		name := truncate(strings.TrimSpace(rawString(raw)), spec.nameLen)
		if name == "" {
			fail(w, http.StatusBadRequest, codeDictName, "请输入"+spec.label+"名称")
			return
		}
		if _, err := m.DB.ExecContext(r.Context(),
			`UPDATE `+spec.table+` SET `+spec.field+` = ? WHERE id = ?`, name, id); err != nil {
			if isDupEntry(err) {
				fail(w, http.StatusConflict, codeDupName, "「"+name+"」已存在")
				return
			}
			writeErr(w, err)
			return
		}
	}
	if raw, exists := body["sort"]; exists {
		if _, err := m.DB.ExecContext(r.Context(),
			`UPDATE `+spec.table+` SET sort = ? WHERE id = ?`, int(rawNumberOr0(raw)), id); err != nil {
			writeErr(w, err)
			return
		}
	}
	if raw, exists := body["status"]; exists {
		if _, err := m.DB.ExecContext(r.Context(),
			`UPDATE `+spec.table+` SET status = ? WHERE id = ?`, boolToInt(rawBool(raw)), id); err != nil {
			writeErr(w, err)
			return
		}
	}
	// 市外/驻地标记（仅目的地字典，互斥）：标一个即清另一个；同置 1 拒绝
	if spec.table == "worklog_destination" {
		_, hasOOC := body["out_of_city"]
		_, hasSta := body["stationed"]
		if hasOOC || hasSta {
			ooc, sta := -1, -1
			if hasOOC {
				ooc = boolToInt(rawBool(body["out_of_city"]))
			}
			if hasSta {
				sta = boolToInt(rawBool(body["stationed"]))
			}
			if ooc == 1 && sta == 1 {
				fail(w, http.StatusBadRequest, codeDictName, "市外与驻地只能标记其一")
				return
			}
			sets := make([]string, 0)
			params := make([]any, 0)
			if ooc >= 0 {
				sets = append(sets, "out_of_city = ?")
				params = append(params, ooc)
			}
			if sta >= 0 {
				sets = append(sets, "stationed = ?")
				params = append(params, sta)
			}
			if ooc == 1 {
				sets = append(sets, "stationed = 0")
			}
			if sta == 1 {
				sets = append(sets, "out_of_city = 0")
			}
			params = append(params, id)
			if _, err := m.DB.ExecContext(r.Context(),
				`UPDATE `+spec.table+` SET `+strings.Join(sets, ", ")+` WHERE id = ?`, params...); err != nil {
				writeErr(w, err)
				return
			}
		}
	}
	writeOK(w, nil)
}

// dictDelete DELETE /admin/{path}/{id}：删除（被引用时拒绝，提示改停用）。
func (m *Module) dictDelete(w http.ResponseWriter, r *http.Request, spec dictSpec) {
	_, team, ok := m.enterDict(w, r)
	if !ok {
		return
	}
	if team == nil {
		fail(w, http.StatusBadRequest, codeNoUsableTeam, "无可用班组")
		return
	}
	id, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if refs := spec.countRefs(m, r, id); refs > 0 {
		fail(w, http.StatusConflict, codeDictInUse, "该"+spec.label+"已被 "+strconv.Itoa(refs)+" 条日志引用，请改为停用")
		return
	}
	res, err := m.DB.ExecContext(r.Context(),
		`DELETE FROM `+spec.table+` WHERE id = ? AND team_id = ?`, id, team.ID)
	if err != nil {
		writeErr(w, err)
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		fail(w, http.StatusNotFound, codeMissing, spec.label+"不存在")
		return
	}
	writeOK(w, nil)
}

// 各字典路由 handler 适配。
func (m *Module) handleDictListVehicles(w http.ResponseWriter, r *http.Request) {
	m.dictList(w, r, dictSpecs["vehicles"])
}
func (m *Module) handleDictCreateVehicles(w http.ResponseWriter, r *http.Request) {
	m.dictCreate(w, r, dictSpecs["vehicles"])
}
func (m *Module) handleDictUpdateVehicles(w http.ResponseWriter, r *http.Request) {
	m.dictUpdate(w, r, dictSpecs["vehicles"])
}
func (m *Module) handleDictDeleteVehicles(w http.ResponseWriter, r *http.Request) {
	m.dictDelete(w, r, dictSpecs["vehicles"])
}
func (m *Module) handleDictListDestinations(w http.ResponseWriter, r *http.Request) {
	m.dictList(w, r, dictSpecs["destinations"])
}
func (m *Module) handleDictCreateDestinations(w http.ResponseWriter, r *http.Request) {
	m.dictCreate(w, r, dictSpecs["destinations"])
}
func (m *Module) handleDictUpdateDestinations(w http.ResponseWriter, r *http.Request) {
	m.dictUpdate(w, r, dictSpecs["destinations"])
}
func (m *Module) handleDictDeleteDestinations(w http.ResponseWriter, r *http.Request) {
	m.dictDelete(w, r, dictSpecs["destinations"])
}
func (m *Module) handleDictListMembers(w http.ResponseWriter, r *http.Request) {
	m.dictList(w, r, dictSpecs["members"])
}
func (m *Module) handleDictCreateMembers(w http.ResponseWriter, r *http.Request) {
	m.dictCreate(w, r, dictSpecs["members"])
}
func (m *Module) handleDictUpdateMembers(w http.ResponseWriter, r *http.Request) {
	m.dictUpdate(w, r, dictSpecs["members"])
}
func (m *Module) handleDictDeleteMembers(w http.ResponseWriter, r *http.Request) {
	m.dictDelete(w, r, dictSpecs["members"])
}

// handleMemberMove PUT /admin/members/{id}/move {dir:'up'|'down'}：成员排序（与相邻换位，重写连续 sort）。
func (m *Module) handleMemberMove(w http.ResponseWriter, r *http.Request) {
	_, team, ok := m.enterDict(w, r)
	if !ok {
		return
	}
	if team == nil {
		fail(w, http.StatusBadRequest, codeNoUsableTeam, "无可用班组")
		return
	}
	id, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	body, err := decodeBody(r)
	if err != nil {
		fail(w, http.StatusBadRequest, codeInput, "参数错误")
		return
	}
	dir := rawString(body["dir"])
	rows, err := m.DB.QueryContext(r.Context(),
		`SELECT id FROM worklog_member WHERE team_id = ? ORDER BY sort, id`, team.ID)
	if err != nil {
		writeErr(w, err)
		return
	}
	order := make([]int64, 0)
	idx := -1
	for rows.Next() {
		var mid int64
		if rows.Scan(&mid) == nil {
			if mid == id {
				idx = len(order)
			}
			order = append(order, mid)
		}
	}
	rows.Close()
	if idx < 0 {
		fail(w, http.StatusNotFound, codeMissing, "成员不存在")
		return
	}
	swap := idx + 1
	if dir == "up" {
		swap = idx - 1
	}
	if swap < 0 || swap >= len(order) {
		writeOK(w, nil) // 已在顶端/底部，次序不变
		return
	}
	order[idx], order[swap] = order[swap], order[idx]
	tx, err := m.DB.BeginTx(r.Context(), nil)
	if err != nil {
		writeErr(w, err)
		return
	}
	for i, mid := range order {
		if _, err := tx.ExecContext(r.Context(),
			`UPDATE worklog_member SET sort = ? WHERE id = ?`, i+1, mid); err != nil {
			tx.Rollback()
			writeErr(w, err)
			return
		}
	}
	if err := tx.Commit(); err != nil {
		writeErr(w, err)
		return
	}
	writeOK(w, nil)
}

// handleTowers GET /towers：当前班组杆塔坐标全量（行 = [电压等级, 线路名称, 杆塔号, 经度, 纬度]）。
// 响应形状与 server/src/worklog/towers.js 契约一致为 { rows: [...] }（经纬度按数字回传）；
// 无班组回 { rows: [] }（与有班组同形状，前端三级级联按空数据提示）。
func (m *Module) handleTowers(w http.ResponseWriter, r *http.Request) {
	_, team, ok := m.enter(w, r)
	if !ok {
		return
	}
	out := make([]any, 0)
	if team != nil {
		rows, err := m.DB.QueryContext(r.Context(),
			`SELECT voltage_level, line_name, tower_no, lng, lat FROM worklog_tower WHERE team_id = ? ORDER BY sort, id`,
			team.ID)
		if err != nil {
			writeErr(w, err)
			return
		}
		defer rows.Close()
		for rows.Next() {
			var voltage, line, tower, lng, lat string
			if rows.Scan(&voltage, &line, &tower, &lng, &lat) == nil {
				out = append(out, []any{voltage, line, tower, toFloat(lng), toFloat(lat)})
			}
		}
	}
	writeOK(w, map[string]any{"rows": out})
}

// handleTowerTemplate GET /towers/template：杆塔坐标导入模板 xlsx。
func (m *Module) handleTowerTemplate(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := m.enterDict(w, r); !ok {
		return
	}
	buf, err := exceldoc.WriteWorkbook(exceldoc.WorkbookSpec{
		Sheets: []exceldoc.SheetSpec{{
			Name:    "杆塔坐标",
			Columns: []exceldoc.Column{{Width: 12}, {Width: 20}, {Width: 12}, {Width: 14}, {Width: 14}},
			Rows: [][]string{
				{"电压等级", "线路名称", "杆塔号", "经度", "纬度"},
				{"110kV", "示例线路", "N1", "111.123456", "37.123456"},
			},
		}},
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet")
	w.Header().Set("Content-Disposition", `attachment; filename="tower-template.xlsx"`)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(buf)
}

// handleTowerImport POST /towers/import：导入杆塔坐标 xlsx（全量替换本班组坐标）。
func (m *Module) handleTowerImport(w http.ResponseWriter, r *http.Request) {
	_, team, ok := m.enterDict(w, r)
	if !ok {
		return
	}
	if team == nil {
		fail(w, http.StatusBadRequest, codeFile, "无可用班组")
		return
	}
	data, name, ok := readUploadFile(w, r, 10*1024*1024)
	if !ok {
		return
	}
	if !strings.HasSuffix(strings.ToLower(name), ".xlsx") {
		fail(w, http.StatusBadRequest, codeFile, "仅支持 .xlsx 文件，请使用模板填写")
		return
	}
	wb, err := exceldoc.ReadWorkbook(data)
	if err != nil || len(wb.Sheets) == 0 {
		fail(w, http.StatusBadRequest, codeFile, "Excel 解析失败，请使用模板文件填写")
		return
	}
	sheet := wb.Sheets[0]
	idx := map[string]int{}
	for i, h := range sheet.Headers {
		idx[strings.TrimSpace(h)] = i
	}
	get := func(row []string, col string) string {
		i, ok := idx[col]
		if !ok || i >= len(row) {
			return ""
		}
		return strings.TrimSpace(row[i])
	}
	type towerRow struct{ voltage, line, tower, lng, lat string }
	rowsIn := make([]towerRow, 0)
	skipped := 0
	for _, row := range sheet.Rows {
		item := towerRow{
			voltage: get(row, "电压等级"),
			line:    get(row, "线路名称"),
			tower:   get(row, "杆塔号"),
			lng:     get(row, "经度"),
			lat:     get(row, "纬度"),
		}
		if item.voltage == "" && item.line == "" && item.tower == "" {
			continue
		}
		if item.voltage == "" || item.line == "" || item.tower == "" || item.lng == "" || item.lat == "" ||
			toFloat(item.lng) == 0 && !isNumeric(item.lng) || !isNumeric(item.lat) {
			skipped++
			continue
		}
		item.voltage = truncate(item.voltage, 32)
		item.line = truncate(item.line, 64)
		item.tower = truncate(item.tower, 64)
		item.lng = truncate(item.lng, 32)
		item.lat = truncate(item.lat, 32)
		rowsIn = append(rowsIn, item)
	}
	if len(rowsIn) == 0 {
		fail(w, http.StatusBadRequest, codeFile, "未识别到有效坐标行，请按模板列填写")
		return
	}
	tx, err := m.DB.BeginTx(r.Context(), nil)
	if err != nil {
		writeErr(w, err)
		return
	}
	if _, err := tx.ExecContext(r.Context(),
		`DELETE FROM worklog_tower WHERE team_id = ?`, team.ID); err != nil {
		tx.Rollback()
		writeErr(w, err)
		return
	}
	for i, row := range rowsIn {
		if _, err := tx.ExecContext(r.Context(),
			`INSERT INTO worklog_tower (team_id, voltage_level, line_name, tower_no, lng, lat, sort) VALUES (?, ?, ?, ?, ?, ?, ?)`,
			team.ID, row.voltage, row.line, row.tower, row.lng, row.lat, i+1); err != nil {
			tx.Rollback()
			writeErr(w, err)
			return
		}
	}
	if err := tx.Commit(); err != nil {
		writeErr(w, err)
		return
	}
	writeOK(w, map[string]any{"imported": len(rowsIn), "skipped": skipped},
		"已导入 "+strconv.Itoa(len(rowsIn))+" 条坐标")
}

// handleGeo GET /geo?lng=&lat=：取 {location, weather}（「选照片并添加水印」预填用）。
func (m *Module) handleGeo(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := m.enter(w, r); !ok {
		return
	}
	lng := strings.TrimSpace(r.URL.Query().Get("lng"))
	lat := strings.TrimSpace(r.URL.Query().Get("lat"))
	if lng == "" || lat == "" || !isNumeric(lng) || !isNumeric(lat) {
		fail(w, http.StatusBadRequest, codeGeo, "经纬度参数无效")
		return
	}
	lw := LocationWeather{}
	if m.Geo != nil {
		lw = m.Geo.FetchLocationWeather(r.Context(), lng, lat)
	}
	writeOK(w, map[string]any{"location": lw.Location, "weather": lw.Weather})
}

// handleSyncSwitchGet GET /dispatch/sync-switch：派车单每日同步开关。
func (m *Module) handleSyncSwitchGet(w http.ResponseWriter, r *http.Request) {
	_, team, ok := m.enterDict(w, r)
	if !ok {
		return
	}
	if team == nil {
		fail(w, http.StatusBadRequest, codeNoUsableTeam, "无可用班组")
		return
	}
	var cnt int
	_ = m.DB.QueryRowContext(r.Context(),
		`SELECT COUNT(*) FROM worklog_dispatch_sync_team WHERE team_id = ?`, team.ID).Scan(&cnt)
	writeOK(w, map[string]any{"enabled": cnt > 0})
}

// handleSyncSwitchPut PUT /dispatch/sync-switch {enabled}：开关每日派车单自动同步。
func (m *Module) handleSyncSwitchPut(w http.ResponseWriter, r *http.Request) {
	ident, team, ok := m.enterDict(w, r)
	if !ok {
		return
	}
	if team == nil {
		fail(w, http.StatusBadRequest, codeNoUsableTeam, "无可用班组")
		return
	}
	body, err := decodeBody(r)
	if err != nil {
		fail(w, http.StatusBadRequest, codeInput, "参数错误")
		return
	}
	enabled := rawBool(body["enabled"])
	if enabled {
		_, err = m.DB.ExecContext(r.Context(),
			`INSERT IGNORE INTO worklog_dispatch_sync_team (team_id, created_by) VALUES (?, ?)`,
			team.ID, ident.ID)
	} else {
		_, err = m.DB.ExecContext(r.Context(),
			`DELETE FROM worklog_dispatch_sync_team WHERE team_id = ?`, team.ID)
	}
	if err != nil {
		writeErr(w, err)
		return
	}
	msg := "已关闭每日派车单自动同步"
	if enabled {
		msg = "已开启每日派车单自动同步"
	}
	writeOK(w, map[string]any{"enabled": enabled}, msg)
}

// handleSyncNow POST /dispatch/sync-now：立即同步一次派车单（结果通知照常发送；
// 当日无文件按旧版口径由执行器发「今日无文件」通知并正常返回，不报 500）。
func (m *Module) handleSyncNow(w http.ResponseWriter, r *http.Request) {
	_, team, ok := m.enterDict(w, r)
	if !ok {
		return
	}
	if team == nil {
		fail(w, http.StatusBadRequest, codeNoUsableTeam, "无可用班组")
		return
	}
	if m.Dispatcher == nil {
		fail(w, http.StatusServiceUnavailable, codeNotConfigured, "派车单同步服务未接入")
		return
	}
	n, err := m.Dispatcher.SyncNow(r.Context(), team.ID)
	if err != nil {
		fail(w, http.StatusInternalServerError, codeSyncFail, err.Error())
		return
	}
	msg := fmt.Sprintf("同步完成：新建 %d 张出车卡片", n)
	writeOK(w, map[string]any{"ok": true, "message": msg, "synced": n}, msg)
}

// isDupEntry MySQL ER_DUP_ENTRY 判定（1062）。
func isDupEntry(err error) bool {
	return err != nil && strings.Contains(err.Error(), "Duplicate entry")
}

// isNumeric 数字串判定。
func isNumeric(s string) bool {
	if strings.TrimSpace(s) == "" {
		return false
	}
	_, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	return err == nil
}

// toFloat 字符串转 float64（非数字返回 0，杆塔坐标按数字回传）。
func toFloat(s string) float64 {
	v, _ := strconv.ParseFloat(strings.TrimSpace(s), 64)
	return v
}
