package httpapi

import (
	"context"
	"database/sql"
)

// SQLAppPermit 按 sys_user_app + sys_app 判断用户应用权限，供各业务模块复用。
type SQLAppPermit struct{ DB *sql.DB }

func (p SQLAppPermit) Allow(ctx context.Context, userID int64, appKey string) (bool, error) {
	var count int
	err := p.DB.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM sys_user_app ua JOIN sys_app a ON a.id = ua.app_id
		 WHERE ua.user_id = ? AND a.app_key = ? AND a.status = 1`, userID, appKey).Scan(&count)
	if err != nil {
		return false, DependencyUnavailable("应用权限读取失败")
	}
	return count > 0, nil
}
