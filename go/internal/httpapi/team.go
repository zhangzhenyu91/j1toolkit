package httpapi

import (
	"context"
	"database/sql"
	"fmt"
)

// Team 是当前请求生效的班组。
type Team struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

func DefaultTeam(ctx context.Context, db *sql.DB) (Team, error) {
	var t Team
	err := db.QueryRowContext(ctx, `SELECT id, name FROM sys_team WHERE status = 1 ORDER BY sort, id LIMIT 1`).Scan(&t.ID, &t.Name)
	return t, err
}

func TeamByID(ctx context.Context, db *sql.DB, id int64) (Team, error) {
	var t Team
	err := db.QueryRowContext(ctx, `SELECT id, name FROM sys_team WHERE id = ? AND status = 1`, id).Scan(&t.ID, &t.Name)
	return t, err
}

// ResolveTeam 按现有超管可指定班组、其他角色固定本班的规则解析生效班组。
func ResolveTeam(ctx context.Context, db *sql.DB, identity interface {
	GetTeamID() int64
	GetRole() string
}, wantedTeamID int64) (Team, error) {
	if identity.GetRole() == "admin" {
		if wantedTeamID > 0 {
			return TeamByID(ctx, db, wantedTeamID)
		}
		if identity.GetTeamID() > 0 {
			return TeamByID(ctx, db, identity.GetTeamID())
		}
		return DefaultTeam(ctx, db)
	}
	if identity.GetTeamID() == 0 {
		return Team{}, fmt.Errorf("未分配班组")
	}
	return TeamByID(ctx, db, identity.GetTeamID())
}

// RequireTeamAdmin 判断当前用户是否能管理生效班组。
func RequireTeamAdmin(identity interface {
	GetTeamID() int64
	GetRole() string
}, team Team) bool {
	return identity.GetRole() == "admin" || (identity.GetRole() == "team_admin" && identity.GetTeamID() == team.ID)
}

// IdentityAdapter 让 auth.Identity 直接满足权限辅助接口，业务模块无需复制角色判断。
type IdentityAdapter struct {
	TeamID int64
	Role   string
}

func (a IdentityAdapter) GetTeamID() int64 { return a.TeamID }
func (a IdentityAdapter) GetRole() string  { return a.Role }
