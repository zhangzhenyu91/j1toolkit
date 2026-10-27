// 出工成员字典与账号同步（迁移 server/src/worklog/member-sync.js）：
// 班组内账号的昵称默认启用为本班出工成员（只增不删；超管账号同规则——班组长常以超管账号兼任，本人即真实成员）。
// 事件触发（员工管理变更时调 SyncMemberForUser）：新建 / 改昵称 / 调班组 / 停启用；
// 调班组 = 他班同步成员行直接迁移到新班（member_id 不变，历史卡片/打卡/照片人名引用全保持，原班不留停用残留；
// 仅新班已有同名成员撞 uk_team_member 时回退「旧行停用让位 + 认领/新建」），商旅账号绑定随人迁移（token 跟人走）；
// 启动时 SyncAllUserMembers 全量对齐一次（只补缺失，不改既有行的启停与姓名，避免覆盖管理端的停用决定）。
package worklog

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// syncUser 同步用账号快照（sys_user 关键列）。
type syncUser struct {
	ID       int64
	Nickname string
	TeamID   int64
	Status   int
}

// syncSgccAccountForUser 商旅账号跟随调班：绑定行迁到本人当前班组与当前成员行（token 跟人走，调班无需重绑）。
// 正常迁移 member_id 不变（仅 team_id 跟进）；重名回退路径传 oldMemberID，member_id 一并改指新行。
// ER_DUP_ENTRY（新成员行已被管理员代绑占 uk_member）：先删本人指向旧成员/他班的行，占坑代绑行归并本人。
func (m *Module) syncSgccAccountForUser(ctx context.Context, user syncUser, memberID int64, oldMemberID int64) error {
	if memberID == 0 {
		return nil
	}
	if oldMemberID == 0 {
		oldMemberID = memberID
	}
	_, err := m.DB.ExecContext(ctx,
		`UPDATE worklog_sgcc_account SET team_id = ?, member_id = ? WHERE user_id = ? OR member_id = ?`,
		user.TeamID, memberID, user.ID, oldMemberID)
	if err == nil {
		return nil
	}
	if !isDupEntry(err) {
		return err
	}
	if _, err := m.DB.ExecContext(ctx,
		`DELETE FROM worklog_sgcc_account WHERE (user_id = ? OR member_id = ?) AND member_id <> ?`,
		user.ID, oldMemberID, memberID); err != nil {
		return err
	}
	_, err = m.DB.ExecContext(ctx,
		`UPDATE worklog_sgcc_account SET user_id = ?, team_id = ? WHERE member_id = ? AND user_id IS NULL`,
		user.ID, user.TeamID, memberID)
	return err
}

// SyncMemberForUser 事件驱动同步：单个账号变更后对齐其出工成员记录（幂等）。
// 停用账号 / 未分配班组 / 昵称为空：同步成员全部停用（不删，保留历史引用）；
// 已有成员对齐姓名并启用；他班同步成员行直接迁移（member_id 不变）；
// 本班已有同名成员则认领（user_id IS NULL 行）；都没有才新建。仅捕获错误向上返回，不同步失败不影响管理端响应。
func (m *Module) SyncMemberForUser(ctx context.Context, userID int64) error {
	var user syncUser
	var teamID sql.NullInt64
	err := m.DB.QueryRowContext(ctx,
		`SELECT id, username, nickname, team_id, status FROM sys_user WHERE id = ?`, userID).
		Scan(&user.ID, new(string), &user.Nickname, &teamID, &user.Status)
	if errors.Is(err, sql.ErrNoRows) {
		return nil // 账号已删除，无同步对象
	}
	if err != nil {
		return err
	}
	if teamID.Valid {
		user.TeamID = teamID.Int64
	}

	// 停用账号 / 未分配班组 / 昵称为空：同步成员全部停用（不删，保留历史引用）
	if user.Status != 1 || user.TeamID == 0 || user.Nickname == "" {
		_, err := m.DB.ExecContext(ctx,
			`UPDATE worklog_member SET status = 0 WHERE user_id = ?`, user.ID)
		return err
	}
	// 调出本班：其他班组的同步成员先停用（被直接迁移的行随后由迁移语句换班并重新启用；
	// 历史调班残留的跨班重复行保持停用收敛）
	if _, err := m.DB.ExecContext(ctx,
		`UPDATE worklog_member SET status = 0 WHERE user_id = ? AND team_id <> ?`,
		user.ID, user.TeamID); err != nil {
		return err
	}

	// 本班组同步成员：已存在则对齐姓名并启用
	var memberID int64
	err = m.DB.QueryRowContext(ctx,
		`SELECT id FROM worklog_member WHERE user_id = ? AND team_id = ?`, user.ID, user.TeamID).
		Scan(&memberID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if err == nil {
		_, err = m.DB.ExecContext(ctx,
			`UPDATE worklog_member SET name = ?, status = 1 WHERE id = ?`, user.Nickname, memberID)
		if isDupEntry(err) {
			// 目标姓名与本班既有成员重名：保留既有行，同步行停用让位（不强行合并引用）
			_, err = m.DB.ExecContext(ctx, `UPDATE worklog_member SET status = 0 WHERE id = ?`, memberID)
			return err
		}
		if err != nil {
			return err
		}
		return m.syncSgccAccountForUser(ctx, user, memberID, 0)
	}

	// 调入本班：他班同步成员行直接迁移（member_id 不变，历史引用全保持；sort 排到本班末尾，同新建行口径）
	var oldMemberID int64
	err = m.DB.QueryRowContext(ctx,
		`SELECT id FROM worklog_member WHERE user_id = ? AND team_id <> ? ORDER BY id DESC`,
		user.ID, user.TeamID).Scan(&oldMemberID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if err == nil {
		migErr := m.migrateMemberRow(ctx, user, oldMemberID)
		if migErr == nil {
			return nil
		}
		if !isDupEntry(migErr) {
			return migErr
		}
		// 新班已有同名成员：迁移撞 uk_team_member，回退「旧行停用让位 + 下方认领/新建」（member_id 变更，账号改指新行）
		if _, err := m.DB.ExecContext(ctx,
			`UPDATE worklog_member SET status = 0 WHERE id = ?`, oldMemberID); err != nil {
			return err
		}
	} else {
		oldMemberID = 0
	}

	// 本班已有同名成员（历史种子 / 手动添加）：认领为账号同步成员
	var sameID int64
	err = m.DB.QueryRowContext(ctx,
		`SELECT id FROM worklog_member WHERE team_id = ? AND name = ? AND user_id IS NULL`,
		user.TeamID, user.Nickname).Scan(&sameID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if err == nil {
		if _, err := m.DB.ExecContext(ctx,
			`UPDATE worklog_member SET user_id = ?, status = 1 WHERE id = ?`, user.ID, sameID); err != nil {
			return err
		}
		return m.syncSgccAccountForUser(ctx, user, sameID, oldMemberID)
	}

	// 新建成员行（sort 排到本班末尾）
	maxSort, err := m.maxMemberSort(ctx, user.TeamID)
	if err != nil {
		return err
	}
	res, err := m.DB.ExecContext(ctx,
		`INSERT INTO worklog_member (team_id, user_id, name, sort, status) VALUES (?, ?, ?, ?, 1)`,
		user.TeamID, user.ID, user.Nickname, maxSort+1)
	if err != nil {
		return err
	}
	newID, _ := res.LastInsertId()
	return m.syncSgccAccountForUser(ctx, user, newID, oldMemberID)
}

// migrateMemberRow 他班同步成员行迁移入本班（撞 uk_team_member 返回 dup 错误，由调用方回退处理）。
func (m *Module) migrateMemberRow(ctx context.Context, user syncUser, memberID int64) error {
	maxSort, err := m.maxMemberSort(ctx, user.TeamID)
	if err != nil {
		return err
	}
	if _, err := m.DB.ExecContext(ctx,
		`UPDATE worklog_member SET team_id = ?, name = ?, sort = ?, status = 1 WHERE id = ?`,
		user.TeamID, user.Nickname, maxSort+1, memberID); err != nil {
		return err
	}
	return m.syncSgccAccountForUser(ctx, user, memberID, 0)
}

// maxMemberSort 本班成员最大 sort（无成员为 0）。
func (m *Module) maxMemberSort(ctx context.Context, teamID int64) (int, error) {
	var maxSort sql.NullInt64
	if err := m.DB.QueryRowContext(ctx,
		`SELECT COALESCE(MAX(sort), 0) AS maxSort FROM worklog_member WHERE team_id = ?`, teamID).
		Scan(&maxSort); err != nil {
		return 0, err
	}
	return int(maxSort.Int64), nil
}

// SyncAllUserMembers 启动全量对齐：仅补缺（插入 / 认领），不改既有成员行的启停与姓名
// （避免覆盖管理端的停用/启用决定）；单账号失败记录后继续，返回首个错误供启动日志告警（不阻断启动）。
func (m *Module) SyncAllUserMembers(ctx context.Context) error {
	rows, err := m.DB.QueryContext(ctx,
		`SELECT id, username, nickname, team_id FROM sys_user WHERE status = 1 AND team_id IS NOT NULL AND nickname <> ''`)
	if err != nil {
		return err
	}
	defer rows.Close()
	var users []syncUser
	for rows.Next() {
		var u syncUser
		if err := rows.Scan(&u.ID, new(string), &u.Nickname, &u.TeamID); err != nil {
			return err
		}
		users = append(users, u)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	var firstErr error
	for _, u := range users {
		if err := m.backfillMemberForUser(ctx, u); err != nil {
			// 同 Node：单账号失败仅记录，继续后续账号
			if firstErr == nil {
				firstErr = fmt.Errorf("用户 %d：%w", u.ID, err)
			}
		}
	}
	return firstErr
}

// backfillMemberForUser 启动补缺单账号：已有本班同步成员行则不动；否则认领同名行或新建。
func (m *Module) backfillMemberForUser(ctx context.Context, u syncUser) error {
	var memberID int64
	err := m.DB.QueryRowContext(ctx,
		`SELECT id FROM worklog_member WHERE user_id = ? AND team_id = ?`, u.ID, u.TeamID).Scan(&memberID)
	if err == nil {
		return nil // 已有同步成员行，不动（启停与姓名保持管理端决定）
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	var sameID int64
	err = m.DB.QueryRowContext(ctx,
		`SELECT id FROM worklog_member WHERE team_id = ? AND name = ? AND user_id IS NULL`,
		u.TeamID, u.Nickname).Scan(&sameID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if err == nil {
		_, err = m.DB.ExecContext(ctx,
			`UPDATE worklog_member SET user_id = ? WHERE id = ?`, u.ID, sameID)
		return err
	}
	maxSort, err := m.maxMemberSort(ctx, u.TeamID)
	if err != nil {
		return err
	}
	_, err = m.DB.ExecContext(ctx,
		`INSERT INTO worklog_member (team_id, user_id, name, sort, status) VALUES (?, ?, ?, ?, 1)`,
		u.TeamID, u.ID, u.Nickname, maxSort+1)
	return err
}
