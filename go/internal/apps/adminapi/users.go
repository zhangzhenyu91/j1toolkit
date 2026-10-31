// 员工（账号）管理：列表 / 新建 / 修改（昵称、wxid、班组、状态、角色、重置密码）/ 删除。
// 迁移 server/src/routes/admin.js 的 /users 口径（含自我保护与班组管理员约束）；
// 删除为 Go 侧补齐（Node 无此接口），带「不能删自己 / 不能删最后一个超管」约束。
package adminapi

import (
	"context"
	"database/sql"
	"net/http"
	"regexp"
	"strings"

	"golang.org/x/crypto/bcrypt"
)

// usernamePattern 账号规则（Node: /^[a-zA-Z0-9_]{2,64}$/）。
var usernamePattern = regexp.MustCompile(`^[a-zA-Z0-9_]{2,64}$`)

// userRow 员工列表条目（字段同 Node GET /admin/users）。
type userRow struct {
	ID        int64   `json:"id"`
	Username  string  `json:"username"`
	Nickname  string  `json:"nickname"`
	WXID      string  `json:"wxid"`
	TeamID    *int64  `json:"team_id"`
	Team      *string `json:"team"`
	Role      string  `json:"role"`
	Status    int     `json:"status"`
	CreatedAt string  `json:"created_at"`
}

// validTeam 校验班组 id 合法且启用（Node validTeam）。
func (m *Module) validTeam(ctx context.Context, teamID int64) (bool, error) {
	var status int
	err := m.DB.QueryRowContext(ctx, `SELECT status FROM sys_team WHERE id = ?`, teamID).Scan(&status)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, DependencyUnavailableMsg("班组校验失败")
	}
	return status == 1, nil
}

// listUsers GET /api/v1/admin/users?keyword=&team_id= → data:{list:[...]}
// keyword 模糊搜索账号/昵称；team_id=0 筛未分组；LIMIT 500 同 Node。
func (m *Module) listUsers(w http.ResponseWriter, r *http.Request) {
	if _, ok := m.ident(w, r); !ok {
		return
	}
	keyword := strings.TrimSpace(r.URL.Query().Get("keyword"))
	wheres := make([]string, 0, 2)
	params := make([]any, 0, 3)
	if keyword != "" {
		wheres = append(wheres, "(u.username LIKE ? OR u.nickname LIKE ?)")
		like := "%" + keyword + "%"
		params = append(params, like, like)
	}
	if raw, exists := r.URL.Query()["team_id"]; exists && strings.TrimSpace(raw[0]) != "" {
		if strings.TrimSpace(raw[0]) == "0" {
			wheres = append(wheres, "u.team_id IS NULL")
		} else {
			wheres = append(wheres, "u.team_id = ?")
			params = append(params, rawNumberOr0([]byte(raw[0])))
		}
	}
	where := ""
	if len(wheres) > 0 {
		where = "WHERE " + strings.Join(wheres, " AND ")
	}
	rows, err := m.DB.QueryContext(r.Context(),
		`SELECT u.id, u.username, u.nickname, u.wxid, u.team_id, t.name, u.role, u.status,
		        DATE_FORMAT(u.created_at, '%Y-%m-%d %H:%i:%s')
		 FROM sys_user u LEFT JOIN sys_team t ON t.id = u.team_id `+where+` ORDER BY u.id LIMIT 500`,
		params...)
	if err != nil {
		writeErr(w, DependencyUnavailableMsg("员工列表读取失败"))
		return
	}
	defer rows.Close()
	list := make([]userRow, 0)
	for rows.Next() {
		var u userRow
		var teamID sql.NullInt64
		var teamName sql.NullString
		if err := rows.Scan(&u.ID, &u.Username, &u.Nickname, &u.WXID, &teamID, &teamName, &u.Role, &u.Status, &u.CreatedAt); err != nil {
			writeErr(w, err)
			return
		}
		if teamID.Valid {
			u.TeamID = &teamID.Int64
		}
		if teamName.Valid {
			u.Team = &teamName.String
		}
		list = append(list, u)
	}
	if err := rows.Err(); err != nil {
		writeErr(w, err)
		return
	}
	writeOK(w, map[string]any{"list": list})
}

// createUser POST /api/v1/admin/users → data:{id}
// 校验同 Node：账号格式、密码长度、角色合法、班组管理员必须有班组、班组启用、wxid 长度、账号唯一。
func (m *Module) createUser(w http.ResponseWriter, r *http.Request) {
	if _, ok := m.ident(w, r); !ok {
		return
	}
	body, err := decodeBody(r)
	if err != nil {
		fail(w, http.StatusBadRequest, codeBadUsername, "请求体不是合法 JSON")
		return
	}
	username := rawText(body["username"])
	password := rawText(body["password"])
	nickname := rawString(body["nickname"])
	wxid := rawString(body["wxid"])
	role := rawString(body["role"])
	if role == "" {
		role = "user"
	}
	var teamID *int64
	if raw, exists := body["team_id"]; exists && string(raw) != "null" {
		if n := rawNumberOr0(raw); n != 0 {
			teamID = &n
		}
	}
	if !usernamePattern.MatchString(username) {
		fail(w, http.StatusBadRequest, codeBadUsername, "账号需为 2-64 位字母、数字或下划线")
		return
	}
	if len(password) < 6 {
		fail(w, http.StatusBadRequest, codeBadPassword, "密码至少 6 位")
		return
	}
	if !isRole(role) {
		fail(w, http.StatusBadRequest, codeBadRole, "非法角色")
		return
	}
	if role == "team_admin" && teamID == nil {
		fail(w, http.StatusBadRequest, codeTeamAdminNil, "班组管理员必须有所属班组")
		return
	}
	if teamID != nil {
		valid, err := m.validTeam(r.Context(), *teamID)
		if err != nil {
			writeErr(w, err)
			return
		}
		if !valid {
			fail(w, http.StatusBadRequest, codeTeamInvalid, "班组不存在或已停用")
			return
		}
	}
	if len([]rune(wxid)) > 128 {
		fail(w, http.StatusBadRequest, codeWXIDTooLong, "wxid 不能超过 128 字")
		return
	}
	var dupID int64
	if err := m.DB.QueryRowContext(r.Context(), `SELECT id FROM sys_user WHERE username = ?`, username).Scan(&dupID); err == nil {
		fail(w, http.StatusConflict, codeUserDup, "账号已存在")
		return
	} else if err != sql.ErrNoRows {
		writeErr(w, DependencyUnavailableMsg("账号查重失败"))
		return
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		writeErr(w, err)
		return
	}
	res, err := m.DB.ExecContext(r.Context(),
		`INSERT INTO sys_user (username, password_hash, nickname, wxid, team_id, role) VALUES (?, ?, ?, ?, ?, ?)`,
		username, string(hash), nickname, wxid, teamID, role)
	if err != nil {
		writeErr(w, err)
		return
	}
	id, _ := res.LastInsertId()
	if m.OnUserChanged != nil {
		m.OnUserChanged(r.Context(), id)
	}
	m.logOp(r.Context(), "admin.user.create", "创建员工账号", "admin user created",
		map[string]any{"user_id": id, "username": username, "role": role})
	writeOK(w, map[string]any{"id": id}, "已创建")
}

// updateUser PUT /api/v1/admin/users/{id} → data:null
// 部分更新（字段存在性判定同 Node hasOwnProperty）：昵称/wxid/班组/状态/角色/密码；
// 自我保护：不能禁用或降级（改为非超管）自己的账号；班组管理员必须有所属班组。
func (m *Module) updateUser(w http.ResponseWriter, r *http.Request) {
	ident, ok := m.ident(w, r)
	if !ok {
		return
	}
	targetID := pathID(r)
	body, err := decodeBody(r)
	if err != nil {
		fail(w, http.StatusBadRequest, codeBadUsername, "请求体不是合法 JSON")
		return
	}
	_, hasNickname := body["nickname"]
	_, hasWxid := body["wxid"]
	_, hasTeam := body["team_id"]
	_, hasStatus := body["status"]
	_, hasRole := body["role"]
	_, hasPassword := body["password"]

	nickname := rawString(body["nickname"])
	wxid := rawString(body["wxid"])
	role := rawString(body["role"])
	password := rawText(body["password"])
	var teamID *int64
	if hasTeam && string(body["team_id"]) != "null" && rawString(body["team_id"]) != "" {
		if n := rawNumberOr0(body["team_id"]); n != 0 {
			teamID = &n
		}
	}
	// status 显式数值归一为 0/1：字符串 "0" 不能被真值判定误当启用（Node statusNum 口径）
	var statusNum int
	if hasStatus {
		statusNum = rawFlag01(body["status"])
	}

	// 自我保护：不能禁用或降级自己的账号
	if targetID == ident.ID && ((hasStatus && statusNum == 0) || (hasRole && role != "admin")) {
		fail(w, http.StatusBadRequest, codeSelfProtect, "不能禁用或降级自己的账号")
		return
	}

	fields := make([]string, 0, 6)
	params := make([]any, 0, 7)
	if hasNickname {
		fields = append(fields, "nickname = ?")
		params = append(params, nickname)
	}
	if hasWxid {
		if len([]rune(wxid)) > 128 {
			fail(w, http.StatusBadRequest, codeWXIDTooLong, "wxid 不能超过 128 字")
			return
		}
		fields = append(fields, "wxid = ?")
		params = append(params, wxid)
	}
	if hasTeam {
		if teamID != nil {
			valid, err := m.validTeam(r.Context(), *teamID)
			if err != nil {
				writeErr(w, err)
				return
			}
			if !valid {
				fail(w, http.StatusBadRequest, codeTeamInvalid, "班组不存在或已停用")
				return
			}
		}
		fields = append(fields, "team_id = ?")
		params = append(params, teamID)
	}
	if hasStatus {
		fields = append(fields, "status = ?")
		params = append(params, statusNum)
	}
	if hasRole {
		if !isRole(role) {
			fail(w, http.StatusBadRequest, codeBadRole, "非法角色")
			return
		}
		fields = append(fields, "role = ?")
		params = append(params, role)
	}
	// 班组管理员必须有班组（以更新后的 team_id 为准）
	if hasRole && role == "team_admin" || (!hasRole && hasTeam) {
		var curRole string
		var curTeam sql.NullInt64
		err := m.DB.QueryRowContext(r.Context(), `SELECT role, team_id FROM sys_user WHERE id = ?`, targetID).
			Scan(&curRole, &curTeam)
		if err == sql.ErrNoRows {
			fail(w, http.StatusNotFound, codeUserMissing, "用户不存在")
			return
		}
		if err != nil {
			writeErr(w, DependencyUnavailableMsg("用户读取失败"))
			return
		}
		finalRole := curRole
		if hasRole {
			finalRole = role
		}
		finalTeam := curTeam.Int64
		if hasTeam && teamID != nil {
			finalTeam = *teamID
		}
		if hasTeam && teamID == nil {
			finalTeam = 0
		}
		if finalRole == "team_admin" && finalTeam == 0 {
			fail(w, http.StatusBadRequest, codeTeamAdminNil, "班组管理员必须有所属班组")
			return
		}
	}
	if hasPassword {
		if len(password) < 6 {
			fail(w, http.StatusBadRequest, codeBadPassword, "密码至少 6 位")
			return
		}
		hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
		if err != nil {
			writeErr(w, err)
			return
		}
		fields = append(fields, "password_hash = ?")
		params = append(params, string(hash))
	}
	if len(fields) == 0 {
		fail(w, http.StatusBadRequest, codeNoChange, "没有需要修改的内容")
		return
	}
	params = append(params, targetID)
	res, err := m.DB.ExecContext(r.Context(),
		`UPDATE sys_user SET `+strings.Join(fields, ", ")+` WHERE id = ?`, params...)
	if err != nil {
		writeErr(w, err)
		return
	}
	affected, _ := res.RowsAffected()
	if affected == 0 {
		// MySQL 同值 UPDATE 的 affected=0 不代表用户不存在；只有查无此人才报 404。
		var exists int64
		err := m.DB.QueryRowContext(r.Context(), `SELECT id FROM sys_user WHERE id = ?`, targetID).Scan(&exists)
		if err == sql.ErrNoRows {
			fail(w, http.StatusNotFound, codeUserMissing, "用户不存在")
			return
		}
		if err != nil {
			writeErr(w, DependencyUnavailableMsg("用户读取失败"))
			return
		}
	}
	if hasNickname || hasTeam || hasStatus {
		if m.OnUserChanged != nil {
			m.OnUserChanged(r.Context(), targetID)
		}
	}
	m.logOp(r.Context(), "admin.user.update", "修改员工账号", "admin user updated",
		map[string]any{"user_id": targetID})
	writeOK(w, nil, "已保存")
}

// deleteUser DELETE /api/v1/admin/users/{id} → data:null
// Go 侧补齐（Node 无删除接口）：不能删除自己；不能删除最后一个超级管理员；
// 删除后清空其应用授权与通知已读/按人删除记录（出工成员行保留，历史引用不受影响）。
func (m *Module) deleteUser(w http.ResponseWriter, r *http.Request) {
	ident, ok := m.ident(w, r)
	if !ok {
		return
	}
	targetID := pathID(r)
	var targetRole string
	err := m.DB.QueryRowContext(r.Context(), `SELECT role FROM sys_user WHERE id = ?`, targetID).Scan(&targetRole)
	if err == sql.ErrNoRows {
		fail(w, http.StatusNotFound, codeUserMissing, "用户不存在")
		return
	}
	if err != nil {
		writeErr(w, DependencyUnavailableMsg("用户读取失败"))
		return
	}
	if targetID == ident.ID {
		fail(w, http.StatusBadRequest, codeSelfProtect, "不能删除自己的账号")
		return
	}
	if targetRole == "admin" {
		var rest int64
		if err := m.DB.QueryRowContext(r.Context(),
			`SELECT COUNT(*) FROM sys_user WHERE role = 'admin' AND id <> ?`, targetID).Scan(&rest); err != nil {
			writeErr(w, DependencyUnavailableMsg("超管计数失败"))
			return
		}
		if rest == 0 {
			fail(w, http.StatusBadRequest, codeLastAdmin, "不能删除最后一个超级管理员")
			return
		}
	}
	res, err := m.DB.ExecContext(r.Context(), `DELETE FROM sys_user WHERE id = ?`, targetID)
	if err != nil {
		writeErr(w, err)
		return
	}
	affected, _ := res.RowsAffected()
	if affected == 0 {
		fail(w, http.StatusNotFound, codeUserMissing, "用户不存在")
		return
	}
	for _, q := range []string{
		`DELETE FROM sys_user_app WHERE user_id = ?`,
		`DELETE FROM sys_notice_read WHERE user_id = ?`,
		`DELETE FROM sys_notice_del WHERE user_id = ?`,
	} {
		if _, err := m.DB.ExecContext(r.Context(), q, targetID); err != nil {
			writeErr(w, err)
			return
		}
	}
	m.logOp(r.Context(), "admin.user.delete", "删除员工账号", "admin user deleted",
		map[string]any{"user_id": targetID})
	writeOK(w, nil, "已删除")
}

// isRole 角色是否合法（Node ROLES）。
func isRole(role string) bool {
	for _, r := range roles {
		if r == role {
			return true
		}
	}
	return false
}
