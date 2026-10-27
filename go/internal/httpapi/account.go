package httpapi

// 账号核心 API：GET /api/v1/user/profile 与 GET /api/v1/admin/teams 的 SQL 实现与处理器。
// 迁移 server/src/routes/user.js 与 routes/admin.js 的班组列表口径；
// 返回完整班组字段（超管可见），管理增改删由 adminapi 模块提供。

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
)

// SQLAccountStore 账号核心查询的 MySQL 实现。
type SQLAccountStore struct{ DB *sql.DB }

// Profile 读当前用户资料（sys_user + sys_team 联表；openid 只回绑定状态，不回本体）。
func (s SQLAccountStore) Profile(ctx context.Context, userID int64) (UserProfile, error) {
	var u UserProfile
	var avatar, teamName sql.NullString
	var teamID sql.NullInt64
	var openid sql.NullString
	err := s.DB.QueryRowContext(ctx,
		`SELECT u.id, u.username, u.nickname, u.avatar, u.team_id, t.name, u.role, u.openid, u.created_at
		 FROM sys_user u LEFT JOIN sys_team t ON t.id = u.team_id WHERE u.id = ?`, userID).
		Scan(&u.ID, &u.Username, &u.Nickname, &avatar, &teamID, &teamName, &u.Role, &openid, &u.CreatedAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return UserProfile{}, NotFound("用户不存在")
		}
		return UserProfile{}, DependencyUnavailable("用户资料读取失败")
	}
	u.Avatar = avatar.String
	u.Team = teamName.String
	u.TeamID = teamID.Int64
	u.WXBound = openid.Valid && openid.String != ""
	return u, nil
}

// Teams 返回班组列表（完整管理字段，按 sort, id 排序）。
func (s SQLAccountStore) Teams(ctx context.Context) ([]TeamItem, error) {
	rows, err := s.DB.QueryContext(ctx,
		`SELECT t.id, t.name, t.wxid, t.kvm_group_name, t.sort, t.status, t.created_at, COUNT(u.id)
		 FROM sys_team t LEFT JOIN sys_user u ON u.team_id = t.id GROUP BY t.id ORDER BY t.sort, t.id`)
	if err != nil {
		return nil, DependencyUnavailable("班组列表读取失败")
	}
	defer rows.Close()
	out := make([]TeamItem, 0)
	for rows.Next() {
		var t TeamItem
		if err := rows.Scan(&t.ID, &t.Name, &t.WXID, &t.KVMGroupName, &t.Sort, &t.Status, &t.CreatedAt, &t.UserCount); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// userProfile GET /api/v1/user/profile → data:{id,username,nickname,avatar,team,team_id,role,wx_bound,created_at}
func (a *api) userProfile(w http.ResponseWriter, r *http.Request) {
	if a.d.Accounts == nil {
		writeErr(w, DependencyUnavailable("账号数据未接入（数据库未配置）"))
		return
	}
	ident, ok := IdentityFromContext(r.Context())
	if !ok {
		writeErr(w, &APIError{Code: "unauthorized", Message: "未登录或登录已过期", Status: http.StatusUnauthorized})
		return
	}
	profile, err := a.d.Accounts.Profile(r.Context(), ident.ID)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeOK(w, profile)
}

// adminTeams GET /api/v1/admin/teams → data:{list:[{id,name}]}（超管；路由层 isAdminPath 已门控）
func (a *api) adminTeams(w http.ResponseWriter, r *http.Request) {
	if a.d.Accounts == nil {
		writeErr(w, DependencyUnavailable("账号数据未接入（数据库未配置）"))
		return
	}
	teams, err := a.d.Accounts.Teams(r.Context())
	if err != nil {
		writeErr(w, err)
		return
	}
	if teams == nil {
		teams = []TeamItem{}
	}
	writeOK(w, map[string]any{"list": teams})
}

// 编译期接口一致性检查。
var _ AccountStore = SQLAccountStore{}
