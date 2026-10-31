// 班组管理：全量列表 / 新增 / 修改（改名、微信群 wxid、排序、停启用）/ 删除。
// 迁移 server/src/routes/admin.js 的 /teams 口径；GET /api/v1/admin/teams 窄列表（id/name）
// 已由 httpapi 提供，本模块只注册全量列表 GET /api/v1/admin/teams/full 与增改删。
package adminapi

import (
	"database/sql"
	"fmt"
	"net/http"
	"strings"
)

// teamRow 班组全量条目（字段同 Node GET /admin/teams，加 created_at 格式化）。
type teamRow struct {
	ID           int64  `json:"id"`
	Name         string `json:"name"`
	WXID         string `json:"wxid"`
	KvmGroupName string `json:"kvm_group_name"`
	Sort         int    `json:"sort"`
	Status       int    `json:"status"`
	CreatedAt    string `json:"created_at"`
	UserCount    int64  `json:"user_count"`
}

// listTeams GET /api/v1/admin/teams/full → data:{list:[...]}（含停用班组；按 sort, id 排序）。
func (m *Module) listTeams(w http.ResponseWriter, r *http.Request) {
	if _, ok := m.ident(w, r); !ok {
		return
	}
	rows, err := m.DB.QueryContext(r.Context(),
		`SELECT t.id, t.name, t.wxid, t.kvm_group_name, t.sort, t.status,
		        DATE_FORMAT(t.created_at, '%Y-%m-%d %H:%i:%s'),
		        (SELECT COUNT(*) FROM sys_user u WHERE u.team_id = t.id)
		 FROM sys_team t ORDER BY t.sort, t.id`)
	if err != nil {
		writeErr(w, DependencyUnavailableMsg("班组列表读取失败"))
		return
	}
	defer rows.Close()
	list := make([]teamRow, 0)
	for rows.Next() {
		var t teamRow
		if err := rows.Scan(&t.ID, &t.Name, &t.WXID, &t.KvmGroupName, &t.Sort, &t.Status, &t.CreatedAt, &t.UserCount); err != nil {
			writeErr(w, err)
			return
		}
		list = append(list, t)
	}
	if err := rows.Err(); err != nil {
		writeErr(w, err)
		return
	}
	writeOK(w, map[string]any{"list": list})
}

// createTeam POST /api/v1/admin/teams → data:{id}
// name 必填（≤64 字）、wxid 可空（≤128 字）；sort 取当前最大 +1；名称唯一（ER_DUP_ENTRY → 40902）。
func (m *Module) createTeam(w http.ResponseWriter, r *http.Request) {
	if _, ok := m.ident(w, r); !ok {
		return
	}
	body, err := decodeBody(r)
	if err != nil {
		fail(w, http.StatusBadRequest, codeNameEmpty, "请求体不是合法 JSON")
		return
	}
	name := truncate64(rawString(body["name"]))
	wxid := rawString(body["wxid"])
	if name == "" {
		fail(w, http.StatusBadRequest, codeNameEmpty, "请输入班组名称")
		return
	}
	if len([]rune(wxid)) > 128 {
		fail(w, http.StatusBadRequest, codeWXIDTooLong, "wxid 不能超过 128 字")
		return
	}
	var maxSort int
	if err := m.DB.QueryRowContext(r.Context(),
		`SELECT COALESCE(MAX(sort), 0) FROM sys_team`).Scan(&maxSort); err != nil {
		writeErr(w, DependencyUnavailableMsg("班组排序读取失败"))
		return
	}
	res, err := m.DB.ExecContext(r.Context(),
		`INSERT INTO sys_team (name, wxid, sort) VALUES (?, ?, ?)`, name, wxid, maxSort+1)
	if err != nil {
		if isDupEntry(err) {
			fail(w, http.StatusConflict, codeTeamDup, fmt.Sprintf("班组「%s」已存在", name))
			return
		}
		writeErr(w, err)
		return
	}
	id, _ := res.LastInsertId()
	m.logOp(r.Context(), "admin.team.create", "新增班组", "admin team created",
		map[string]any{"team_id": id, "name": name})
	writeOK(w, map[string]any{"id": id}, "已创建")
}

// updateTeam PUT /api/v1/admin/teams/{id} → data:null
// 改名/微信群 wxid/排序/停启用（status 显式数值归一 0/1）；改名级联钩子 OnTeamRenamed（Node
// safeday docs 子目录更名 + netdisk 班组名缓存失效；失败不阻断保存）。
func (m *Module) updateTeam(w http.ResponseWriter, r *http.Request) {
	if _, ok := m.ident(w, r); !ok {
		return
	}
	targetID := pathID(r)
	var oldName string
	err := m.DB.QueryRowContext(r.Context(), `SELECT name FROM sys_team WHERE id = ?`, targetID).Scan(&oldName)
	if err == sql.ErrNoRows {
		fail(w, http.StatusNotFound, codeTeamMissing, "班组不存在")
		return
	}
	if err != nil {
		writeErr(w, DependencyUnavailableMsg("班组读取失败"))
		return
	}
	body, err := decodeBody(r)
	if err != nil {
		fail(w, http.StatusBadRequest, codeNameEmpty, "请求体不是合法 JSON")
		return
	}
	_, hasName := body["name"]
	_, hasSort := body["sort"]
	_, hasStatus := body["status"]
	_, hasWxid := body["wxid"]

	name := truncate64(rawString(body["name"]))
	wxid := rawString(body["wxid"])
	fields := make([]string, 0, 4)
	params := make([]any, 0, 5)
	if hasName {
		if name == "" {
			fail(w, http.StatusBadRequest, codeNameEmpty, "请输入班组名称")
			return
		}
		fields = append(fields, "name = ?")
		params = append(params, name)
	}
	if hasSort {
		fields = append(fields, "sort = ?")
		params = append(params, rawNumberOr0(body["sort"]))
	}
	// status 显式数值归一为 0/1（字符串 "0" 不能被真值判定误当启用）
	if hasStatus {
		fields = append(fields, "status = ?")
		params = append(params, rawFlag01(body["status"]))
	}
	if hasWxid {
		if len([]rune(wxid)) > 128 {
			fail(w, http.StatusBadRequest, codeWXIDTooLong, "wxid 不能超过 128 字")
			return
		}
		fields = append(fields, "wxid = ?")
		params = append(params, wxid)
	}
	if len(fields) == 0 {
		fail(w, http.StatusBadRequest, codeNoChange, "没有需要修改的内容")
		return
	}
	params = append(params, targetID)
	if _, err := m.DB.ExecContext(r.Context(),
		`UPDATE sys_team SET `+strings.Join(fields, ", ")+` WHERE id = ?`, params...); err != nil {
		if isDupEntry(err) {
			// 与 Node 同文案；未改名时用原名（Node 取 params[0]，未传 name 时会错位，此处按实际名兜正）
			msgName := name
			if !hasName {
				msgName = oldName
			}
			fail(w, http.StatusConflict, codeTeamDup, fmt.Sprintf("班组「%s」已存在", msgName))
			return
		}
		writeErr(w, err)
		return
	}
	// 改名级联（安全日记录/产物目录、网盘班组名缓存）；失败仅跳过，不阻断保存（Node 同口径）
	newName := oldName
	if hasName {
		newName = name
	}
	if newName != oldName && m.OnTeamRenamed != nil {
		m.OnTeamRenamed(oldName, newName)
	}
	m.logOp(r.Context(), "admin.team.update", "修改班组", "admin team updated",
		map[string]any{"team_id": targetID, "name": newName})
	writeOK(w, nil, "已保存")
}

// deleteTeam DELETE /api/v1/admin/teams/{id} → data:null
// 有任何数据引用则拒绝（40903 改为停用）：班组下员工、出工日志、题库、商旅打卡、安全日记录；
// 派车单同步开关表（worklog_dispatch_sync_team）只是配置行，随班组级联清除。
func (m *Module) deleteTeam(w http.ResponseWriter, r *http.Request) {
	if _, ok := m.ident(w, r); !ok {
		return
	}
	targetID := pathID(r)
	var teamName string
	err := m.DB.QueryRowContext(r.Context(), `SELECT name FROM sys_team WHERE id = ?`, targetID).Scan(&teamName)
	if err == sql.ErrNoRows {
		fail(w, http.StatusNotFound, codeTeamMissing, "班组不存在")
		return
	}
	if err != nil {
		writeErr(w, DependencyUnavailableMsg("班组读取失败"))
		return
	}

	// 所有引用检查必须先于任何写入：任一命中直接 409，不能留下已删开关/部分清理。
	var cnt int64
	if err := m.DB.QueryRowContext(r.Context(),
		`SELECT COUNT(*) FROM sys_user WHERE team_id = ?`, targetID).Scan(&cnt); err != nil {
		writeErr(w, DependencyUnavailableMsg("班组引用检查失败"))
		return
	}
	if cnt > 0 {
		fail(w, http.StatusConflict, codeTeamInUse, fmt.Sprintf("该班组下仍有 %d 名员工，请改为停用", cnt))
		return
	}
	for _, table := range []string{
		"worklog_entry", "worklog_vehicle", "worklog_destination", "worklog_member", "worklog_tower",
	} {
		ok, err := m.teamRefEmpty(r, table, targetID)
		if err != nil {
			writeErr(w, err)
			return
		}
		if !ok {
			fail(w, http.StatusConflict, codeTeamInUse, "该班组已有出工日志数据，请改为停用")
			return
		}
	}
	if ok, err := m.teamRefEmpty(r, "quiz_bank", targetID); err != nil {
		writeErr(w, err)
		return
	} else if !ok {
		fail(w, http.StatusConflict, codeTeamInUse, "该班组已有题库数据，请改为停用")
		return
	}
	// 树障/交跨业务的正式记录、暂存批次、冲突、删除墓碑均按班组引用；
	// 运维线路与微信智能应答监听群同样不能留孤儿。
	for _, ref := range []struct{ table, message string }{
		{"treehazard_record", "该班组已有树障隐患数据，请改为停用"},
		{"treehazard_staging_batch", "该班组已有树障暂存区数据，请改为停用"},
		{"treehazard_conflict", "该班组已有树障冲突数据，请改为停用"},
		{"treehazard_deleted_id", "该班组已有树障删除墓碑数据，请改为停用"},
		{"crossing_record", "该班组已有交跨记录数据，请改为停用"},
		{"crossing_staging_batch", "该班组已有交跨暂存区数据，请改为停用"},
		{"crossing_conflict", "该班组已有交跨冲突数据，请改为停用"},
		{"crossing_deleted_id", "该班组已有交跨删除墓碑数据，请改为停用"},
		{"team_line", "该班组已有运维线路数据，请改为停用"},
		{"wa_group", "该班组已有微信智能应答监听群，请改为停用"},
	} {
		ok, err := m.teamRefEmpty(r, ref.table, targetID)
		if err != nil {
			writeErr(w, err)
			return
		}
		if !ok {
			fail(w, http.StatusConflict, codeTeamInUse, ref.message)
			return
		}
	}
	for _, table := range []string{
		"worklog_sgcc_account", "worklog_clockin", "worklog_fee", "worklog_sync_log",
	} {
		ok, err := m.teamRefEmpty(r, table, targetID)
		if err != nil {
			writeErr(w, err)
			return
		}
		if !ok {
			fail(w, http.StatusConflict, codeTeamInUse, "该班组已有商旅打卡数据，请改为停用")
			return
		}
	}
	if m.HasTeamSafedayRecords != nil && m.HasTeamSafedayRecords(teamName) {
		fail(w, http.StatusConflict, codeTeamInUse, "该班组已有安全日活动记录，请改为停用")
		return
	}

	// 班组级配置行与同步日志显式随组清理（与班组删除同一事务，不留无口径孤儿）。
	tx, err := m.DB.BeginTx(r.Context(), nil)
	if err != nil {
		writeErr(w, err)
		return
	}
	cleanups := []struct {
		query string
		args  []any
	}{
		{`DELETE FROM worklog_dispatch_sync_team WHERE team_id = ?`, []any{targetID}},
		{`DELETE FROM treehazard_team_setting WHERE team_id = ?`, []any{targetID}},
		{`DELETE FROM treehazard_sync_log WHERE team_id = ?`, []any{targetID}},
		{`DELETE FROM crossing_sync_log WHERE team_id = ?`, []any{targetID}},
	}
	for _, cleanup := range cleanups {
		if _, err := tx.ExecContext(r.Context(), cleanup.query, cleanup.args...); err != nil {
			_ = tx.Rollback()
			writeErr(w, err)
			return
		}
	}
	res, err := tx.ExecContext(r.Context(), `DELETE FROM sys_team WHERE id = ?`, targetID)
	if err != nil {
		_ = tx.Rollback()
		writeErr(w, err)
		return
	}
	affected, _ := res.RowsAffected()
	if affected == 0 {
		_ = tx.Rollback()
		fail(w, http.StatusNotFound, codeTeamMissing, "班组不存在")
		return
	}
	if err := tx.Commit(); err != nil {
		writeErr(w, err)
		return
	}
	m.logOp(r.Context(), "admin.team.delete", "删除班组", "admin team deleted",
		map[string]any{"team_id": targetID, "name": teamName})
	writeOK(w, nil, "已删除")
}

// teamRefEmpty 检查业务表内该班组是否零引用（表名来自本模块固定白名单，非用户输入）。
func (m *Module) teamRefEmpty(r *http.Request, table string, teamID int64) (bool, error) {
	var cnt int64
	if err := m.DB.QueryRowContext(r.Context(),
		`SELECT COUNT(*) FROM `+table+` WHERE team_id = ?`, teamID).Scan(&cnt); err != nil {
		return false, DependencyUnavailableMsg("班组引用检查失败")
	}
	return cnt == 0, nil
}

// truncate64 班组名截断 64 字（Node String(name).trim().slice(0, 64) 口径）。
func truncate64(s string) string {
	r := []rune(s)
	if len(r) > 64 {
		return string(r[:64])
	}
	return s
}
