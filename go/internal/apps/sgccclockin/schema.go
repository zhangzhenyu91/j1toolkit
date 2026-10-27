package sgccclockin

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// 商旅打卡建表（与 server/src/sgccclockin/schema.js 的 DDL 一致）。
// 本模块是出工日志的扩展能力：打卡/费用/照片同步均以 worklog_member 为口径双写本地表，
// 应用权限归属出工日志（sys_app 统一走 work-log）。
var ddl = []string{
	// 商旅账号绑定：一人一号（token 跟人走：user_id / member_id 全局唯一）
	`CREATE TABLE IF NOT EXISTS worklog_sgcc_account (
    id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
    team_id BIGINT UNSIGNED NULL COMMENT '绑定人当前所在班组，关联 sys_team.id',
    user_id BIGINT UNSIGNED NULL COMMENT '绑定人，关联 sys_user.id（NULL=管理员代绑）',
    member_id BIGINT UNSIGNED NULL COMMENT '关联出工成员 worklog_member.id',
    mobile VARCHAR(32) NOT NULL DEFAULT '' COMMENT '商旅注册手机号',
    token VARCHAR(128) NOT NULL DEFAULT '' COMMENT '商旅登录 token（短信登录换取，长效）',
    device_type VARCHAR(64) NOT NULL DEFAULT '' COMMENT '打卡设备型号「厂商 型号」',
    system_version VARCHAR(64) NOT NULL DEFAULT '' COMMENT '系统版本',
    token_status TINYINT NOT NULL DEFAULT 1 COMMENT '登录态：1 有效 0 已失效',
    last_check_at DATETIME NULL COMMENT '最近一次登录态探测时间',
    backfill_done TINYINT NOT NULL DEFAULT 0 COMMENT '（已废弃）列保留兼容老库',
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    UNIQUE KEY uk_user (user_id),
    UNIQUE KEY uk_member (member_id)
  ) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,
	// 打卡流水：每人每日两条（开始/结束），与商旅逐条对账
	`CREATE TABLE IF NOT EXISTS worklog_clockin (
    id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
    team_id BIGINT UNSIGNED NULL COMMENT '所属班组，关联 sys_team.id',
    member_id BIGINT UNSIGNED NOT NULL COMMENT '关联 worklog_member.id',
    clock_date DATE NOT NULL COMMENT '打卡日期',
    seq TINYINT NOT NULL COMMENT '1 开始打卡 2 结束打卡',
    detail_id VARCHAR(64) NOT NULL DEFAULT '' COMMENT '商旅打卡明细 id',
    clock_time DATETIME NULL COMMENT '打卡时间（商旅口径）',
    position VARCHAR(255) NOT NULL DEFAULT '' COMMENT '完整地址串',
    longitude VARCHAR(32) NOT NULL DEFAULT '' COMMENT '经度',
    latitude VARCHAR(32) NOT NULL DEFAULT '' COMMENT '纬度',
    city_code VARCHAR(32) NOT NULL DEFAULT '' COMMENT '城市行政区编码（高德 adcode）',
    city_name VARCHAR(64) NOT NULL DEFAULT '' COMMENT '城市名',
    work_hours VARCHAR(16) NOT NULL DEFAULT '' COMMENT '工时',
    source TINYINT NOT NULL DEFAULT 0 COMMENT '来源：0 壹匣打卡 1 商旅同步（核查拉取）',
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    UNIQUE KEY uk_member_date_seq (member_id, clock_date, seq),
    KEY idx_team_date (team_id, clock_date)
  ) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,
	// 费用：每人每日一条（本地优先读取；核查时与商旅对账）
	`CREATE TABLE IF NOT EXISTS worklog_fee (
    id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
    team_id BIGINT UNSIGNED NULL COMMENT '所属班组，关联 sys_team.id',
    member_id BIGINT UNSIGNED NOT NULL COMMENT '关联 worklog_member.id',
    fee_date DATE NOT NULL COMMENT '费用日期',
    food_fee DECIMAL(8,2) NOT NULL DEFAULT 0 COMMENT '伙食补助',
    transit_fee DECIMAL(8,2) NOT NULL DEFAULT 0 COMMENT '交通费',
    cost_center_code VARCHAR(64) NOT NULL DEFAULT '' COMMENT '成本中心编码',
    cost_center_name VARCHAR(128) NOT NULL DEFAULT '' COMMENT '成本中心名称',
    extra JSON NULL COMMENT '其余费用组件快照',
    synced_at DATETIME NULL COMMENT '最近一次与商旅对账时间',
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    UNIQUE KEY uk_member_date (member_id, fee_date),
    KEY idx_team_date (team_id, fee_date)
  ) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,
	// 核查记录：每晚定时核查 + 手动从商旅拉取的流水
	`CREATE TABLE IF NOT EXISTS worklog_sync_log (
    id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
    team_id BIGINT UNSIGNED NULL COMMENT '所属班组，关联 sys_team.id',
    member_id BIGINT UNSIGNED NULL COMMENT '关联 worklog_member.id（整班核查为 NULL）',
    sync_date DATE NOT NULL COMMENT '核查的数据日期',
    scope VARCHAR(16) NOT NULL DEFAULT 'daily' COMMENT 'daily 当日核查 / remind 提醒前轻量同步',
    type VARCHAR(16) NOT NULL COMMENT 'clockin 打卡 / photo 照片 / fee 费用 / auth 登录态',
    result VARCHAR(8) NOT NULL COMMENT 'ok 一致 / diff 有差异已回写 / fail 失败',
    detail VARCHAR(512) NOT NULL DEFAULT '' COMMENT '明细',
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    KEY idx_team_date (team_id, sync_date)
  ) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,
}

// EnsureSchema 建表（幂等）+ 老库列/键升级：仅加列、改可空与绑定唯一键迁移，
// 不改写或删除打卡/费用/照片/核查流水等业务历史数据（worklog_photo 的商旅列归 worklog 应用建表时已带全列，
// 老库缺列在此补齐；口径对照 server/src/sgccclockin/schema.js 的 ensureSgccSchema）。
func (m *Module) EnsureSchema(ctx context.Context) error {
	if m.DB == nil {
		return fmt.Errorf("sgccclockin: 数据库未接入")
	}
	for _, stmt := range ddl {
		if _, err := m.DB.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("sgccclockin: 建表: %w", err)
		}
	}
	if err := m.ensurePhotoColumns(ctx); err != nil {
		return err
	}
	if err := m.ensureClockinColumns(ctx); err != nil {
		return err
	}
	if err := m.ensureAccountNullable(ctx); err != nil {
		return err
	}
	if err := m.ensureAccountUniqueKeys(ctx); err != nil {
		return err
	}
	return nil
}

// ensureColumn 老库补列（information_schema 先查后加；缺列才 ALTER，幂等）。
func (m *Module) ensureColumn(ctx context.Context, table, column, ddlStmt string) error {
	var got string
	err := m.DB.QueryRowContext(ctx,
		`SELECT COLUMN_NAME FROM information_schema.COLUMNS
		 WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = ? AND COLUMN_NAME = ?`,
		table, column).Scan(&got)
	if err == nil {
		return nil // 已有列
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("sgccclockin: 查列 %s.%s: %w", table, column, err)
	}
	if _, err := m.DB.ExecContext(ctx, "ALTER TABLE "+table+" ADD COLUMN "+ddlStmt); err != nil {
		return fmt.Errorf("sgccclockin: 补列 %s.%s: %w", table, column, err)
	}
	return nil
}

// ensurePhotoColumns worklog_photo 补齐商旅同步列（老库兼容：is_watermark/source/sgcc_img_id/sgcc_synced/md5）。
func (m *Module) ensurePhotoColumns(ctx context.Context) error {
	cols := []struct{ name, ddl string }{
		{"is_watermark", `is_watermark TINYINT NOT NULL DEFAULT 1 COMMENT '1 水印照片（参与验证） 0 非水印照片（仅存档同步，免验证）' AFTER verify_status`},
		{"source", `source TINYINT NOT NULL DEFAULT 0 COMMENT '来源：0 壹匣上传 1 商旅同步（核查发现）' AFTER is_watermark`},
		{"sgcc_img_id", `sgcc_img_id VARCHAR(512) NOT NULL DEFAULT '' COMMENT '商旅费用照片关联（JSON：{worklog_member_id: 商旅图片id}）' AFTER source`},
		{"sgcc_synced", `sgcc_synced TINYINT NOT NULL DEFAULT 0 COMMENT '商旅费用照片同步：0 未同步 1 已同步 2 同步失败' AFTER sgcc_img_id`},
		{"md5", `md5 VARCHAR(32) NOT NULL DEFAULT '' COMMENT '图片内容 MD5（商旅拉取按内容合并相同照片：一图多人标注）' AFTER sgcc_synced`},
	}
	for _, c := range cols {
		if err := m.ensureColumn(ctx, "worklog_photo", c.name, c.ddl); err != nil {
			return err
		}
	}
	return nil
}

// ensureClockinColumns worklog_clockin 补齐城市信息列（打卡落库时写入）。
func (m *Module) ensureClockinColumns(ctx context.Context) error {
	cols := []struct{ name, ddl string }{
		{"city_code", `city_code VARCHAR(32) NOT NULL DEFAULT '' COMMENT '城市行政区编码（高德 adcode）' AFTER latitude`},
		{"city_name", `city_name VARCHAR(64) NOT NULL DEFAULT '' COMMENT '城市名' AFTER city_code`},
	}
	for _, c := range cols {
		if err := m.ensureColumn(ctx, "worklog_clockin", c.name, c.ddl); err != nil {
			return err
		}
	}
	return nil
}

// ensureAccountNullable worklog_sgcc_account.user_id 放宽为允许 NULL（管理员代绑行 user_id=NULL；
// MySQL 唯一键中 NULL 互不相同，uk_user 不冲突，uk_member 仍保证一成员一号）。
func (m *Module) ensureAccountNullable(ctx context.Context) error {
	var nullable string
	err := m.DB.QueryRowContext(ctx,
		`SELECT IS_NULLABLE FROM information_schema.COLUMNS
		 WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'worklog_sgcc_account' AND COLUMN_NAME = 'user_id'`,
	).Scan(&nullable)
	if err != nil || !strings.EqualFold(nullable, "NO") {
		return nil
	}
	_, err = m.DB.ExecContext(ctx,
		`ALTER TABLE worklog_sgcc_account MODIFY user_id BIGINT UNSIGNED NULL COMMENT '绑定人，关联 sys_user.id（NULL=管理员代绑，本人自助重绑时接管回本列）'`)
	if err != nil {
		return fmt.Errorf("sgccclockin: user_id 放宽可空: %w", err)
	}
	return nil
}

// ensureAccountUniqueKeys 绑定唯一键由 (team_id, user_id)/(team_id, member_id) 升级为全局 uk_user/uk_member
// （token 跟人走）。历史调班可能已产生同人跨班双行：先去重（保留与本人当前班组一致的行，都不符保留最新 id 行），
// 再换键；去重只动 worklog_sgcc_account 绑定配置行，不触碰打卡/费用/照片等业务历史数据。
// 旧键不存在时整段跳过，天然幂等。
func (m *Module) ensureAccountUniqueKeys(ctx context.Context) error {
	var idx string
	err := m.DB.QueryRowContext(ctx,
		`SELECT INDEX_NAME FROM information_schema.STATISTICS
		 WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'worklog_sgcc_account' AND INDEX_NAME = 'uk_team_user'`,
	).Scan(&idx)
	if err != nil || idx != "uk_team_user" {
		return nil
	}
	type accRow struct {
		id         int64
		userID     sql.NullInt64
		memberID   sql.NullInt64
		teamID     sql.NullInt64
		userTeamID sql.NullInt64
	}
	rows, err := m.DB.QueryContext(ctx,
		`SELECT a.id, a.user_id, a.member_id, a.team_id, u.team_id
		 FROM worklog_sgcc_account a LEFT JOIN sys_user u ON u.id = a.user_id`)
	if err != nil {
		return fmt.Errorf("sgccclockin: 绑定行去重查询: %w", err)
	}
	groups := map[string][]accRow{}
	for rows.Next() {
		var r accRow
		if rows.Scan(&r.id, &r.userID, &r.memberID, &r.teamID, &r.userTeamID) == nil {
			if r.userID.Valid {
				k := "u" + strconv.FormatInt(r.userID.Int64, 10)
				groups[k] = append(groups[k], r)
			}
			if r.memberID.Valid {
				k := "m" + strconv.FormatInt(r.memberID.Int64, 10)
				groups[k] = append(groups[k], r)
			}
		}
	}
	rows.Close()
	dup := map[int64]bool{}
	for _, list := range groups {
		alive := make([]accRow, 0, len(list))
		for _, r := range list {
			if !dup[r.id] {
				alive = append(alive, r)
			}
		}
		if len(alive) < 2 {
			continue
		}
		keeper := alive[len(alive)-1]
		for _, r := range alive { // 优先保留与本人当前班组一致的行
			if r.userID.Valid && r.userTeamID.Valid && r.teamID.Valid && r.teamID.Int64 == r.userTeamID.Int64 {
				keeper = r
				break
			}
			if r.id > keeper.id {
				keeper = r
			}
		}
		for _, r := range alive {
			if r.id != keeper.id {
				dup[r.id] = true
			}
		}
	}
	if len(dup) > 0 {
		ids := make([]any, 0, len(dup))
		for id := range dup {
			ids = append(ids, id)
		}
		if _, err := m.DB.ExecContext(ctx,
			`DELETE FROM worklog_sgcc_account WHERE id IN (`+placeholders(len(ids))+`)`, ids...); err != nil {
			return fmt.Errorf("sgccclockin: 绑定行去重: %w", err)
		}
	}
	if _, err := m.DB.ExecContext(ctx,
		`ALTER TABLE worklog_sgcc_account
		 DROP INDEX uk_team_user, DROP INDEX uk_team_member,
		 ADD UNIQUE KEY uk_user (user_id), ADD UNIQUE KEY uk_member (member_id)`); err != nil {
		return fmt.Errorf("sgccclockin: 绑定唯一键升级: %w", err)
	}
	return nil
}
