package authwx

import (
	"context"
	"database/sql"
	"errors"

	"shade/internal/httpapi"
)

// userRow 登录查询结果（sys_user 相关列 + 班组名）；openid 仅用于绑定状态与绑定动作，绝不外泄。
type userRow struct {
	ID           int64
	Username     string
	Nickname     string
	Avatar       string
	TeamName     string
	TeamID       int64
	Role         string
	Status       int64
	PasswordHash string
	OpenID       string
}

// Public 映射小程序用户形状（httpapi.WXUser；openid 只折算为 wx_bound）。
func (u *userRow) Public() httpapi.WXUser {
	user := httpapi.WXUser{ID: u.ID, Username: u.Username, Nickname: u.Nickname, Avatar: u.Avatar, Team: u.TeamName, Role: u.Role, WXBound: u.OpenID != ""}
	if u.TeamID != 0 {
		teamID := u.TeamID
		user.TeamID = &teamID
	}
	return user
}

// userSelect 登录类查询统一口径（同 Node USER_SELECT：联出班组名）。
const userSelect = `SELECT u.id, u.username, u.nickname, u.avatar, u.team_id, t.name, u.role, u.status, u.password_hash, u.openid
FROM sys_user u LEFT JOIN sys_team t ON t.id = u.team_id`

// scanUser 扫描 userSelect 单行（昵称/头像/班组名/密码/绑定均可能为 NULL）。
func scanUser(row *sql.Row) (*userRow, error) {
	var (
		u                      userRow
		nickname, avatar, team sql.NullString
		teamID, status         sql.NullInt64
		hash, openid           sql.NullString
	)
	err := row.Scan(&u.ID, &u.Username, &nickname, &avatar, &teamID, &team, &u.Role, &status, &hash, &openid)
	if err != nil {
		return nil, err
	}
	u.Nickname, u.Avatar, u.TeamName = nickname.String, avatar.String, team.String
	u.TeamID, u.Status = teamID.Int64, status.Int64
	u.PasswordHash, u.OpenID = hash.String, openid.String
	return &u, nil
}

// userByUsername 按用户名读登录全行（仅启用账号，同 Node WHERE username=? AND status=1）。
func (m *Module) userByUsername(ctx context.Context, username string) (*userRow, error) {
	if m.db == nil {
		return nil, errNoDB
	}
	return scanUser(m.db.QueryRowContext(ctx, userSelect+` WHERE u.username = ? AND u.status = 1`, username))
}

// userByOpenID 按 openid 读登录全行（不限状态：禁用账号单独报 40302，同 Node 口径）。
func (m *Module) userByOpenID(ctx context.Context, openid string) (*userRow, error) {
	if m.db == nil {
		return nil, errNoDB
	}
	return scanUser(m.db.QueryRowContext(ctx, userSelect+` WHERE u.openid = ?`, openid))
}

// bindOpenID 将 openid 绑定到账号；openid 已绑其他账号则转移（密码 + 微信双重校验通过，无越权）。
// 两条 UPDATE 包在同一事务：避免「先清他号、后绑本号」中途失败导致该 openid 无人持有（同 Node）。
func (m *Module) bindOpenID(ctx context.Context, userID int64, openid, unionid string) error {
	if m.db == nil {
		return errNoDB
	}
	tx, err := m.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx,
		`UPDATE sys_user SET openid = NULL, unionid = NULL WHERE openid = ? AND id <> ?`, openid, userID); err != nil {
		return err
	}
	var uid any
	if unionid != "" {
		uid = unionid
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE sys_user SET openid = ?, unionid = COALESCE(unionid, ?) WHERE id = ?`, openid, uid, userID); err != nil {
		return err
	}
	return tx.Commit()
}

// hasAppPermission 应用权限校验（同 Node hasAppPermission SQL 口径；admin 不免校验）。
func (m *Module) hasAppPermission(ctx context.Context, userID int64, appKey string) (bool, error) {
	if m.db == nil {
		return false, errNoDB
	}
	var count int
	err := m.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM sys_user_app ua JOIN sys_app a ON a.id = ua.app_id
WHERE ua.user_id = ? AND a.app_key = ? AND a.status = 1`, userID, appKey).Scan(&count)
	if err != nil {
		return false, err
	}
	return count > 0, nil
}

// errNoDB 数据库未接入（模块构造未提供 *sql.DB）。
var errNoDB = errors.New("数据库未接入")
