// 老库 worklog 幂等迁移：补列（team_id/备注/派车单号/照片识别/商旅扩展）、
// 默认班组回填、字典唯一键由单列升级为（team_id, 名称）联合唯一。
// 全部探测走 information_schema，缺什么补什么；查询失败直接终止启动，不带着未知旧结构继续。
package worklog

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// worklogTeamTables 需补 team_id 并按默认班组回填的四张业务表。
var worklogTeamTables = []string{"worklog_entry", "worklog_vehicle", "worklog_destination", "worklog_member"}

// worklogCompatColumns 老库缺列清单（表名/列名为固定白名单，直接内联便于幂等探测）。
var worklogCompatColumns = []struct {
	Table, Column, DDL string
}{
	{"worklog_entry", "dispatch_order_no", "ALTER TABLE worklog_entry ADD COLUMN dispatch_order_no VARCHAR(64) NULL COMMENT '派车单号（每日同步带入，改派车可改；「派车汇总」导出字段）' AFTER destination_id"},
	{"worklog_entry", "remark", "ALTER TABLE worklog_entry ADD COLUMN remark TEXT NULL COMMENT '备注（可空）' AFTER patrol_content"},
	{"worklog_entry", "remark_files", "ALTER TABLE worklog_entry ADD COLUMN remark_files JSON NULL COMMENT '备注附件 [{name,url,cos_key,type,size}]（type=image/video/doc）' AFTER remark"},
	{"worklog_entry", "cross_team", "ALTER TABLE worklog_entry ADD COLUMN cross_team TINYINT NOT NULL DEFAULT 0 COMMENT '跨班日志：1 用车人含非归属班组成员（仅超管可建/删/改派车）' AFTER destination_id"},
	{"worklog_member", "user_id", "ALTER TABLE worklog_member ADD COLUMN user_id BIGINT UNSIGNED NULL COMMENT '账号同步成员关联 sys_user.id；NULL=手动添加' AFTER team_id"},
	{"worklog_destination", "out_of_city", "ALTER TABLE worklog_destination ADD COLUMN out_of_city TINYINT NOT NULL DEFAULT 0 COMMENT '市外标记：1 出差至市外（费用按市外标准验证）' AFTER status"},
	{"worklog_destination", "stationed", "ALTER TABLE worklog_destination ADD COLUMN stationed TINYINT NOT NULL DEFAULT 0 COMMENT '驻地标记：1 驻地（伙食40/交通0）' AFTER out_of_city"},
	{"worklog_photo", "shot_time", "ALTER TABLE worklog_photo ADD COLUMN shot_time VARCHAR(32) NOT NULL DEFAULT '' COMMENT '水印拍摄时间（time）' AFTER work_content"},
	{"worklog_photo", "weather", "ALTER TABLE worklog_photo ADD COLUMN weather VARCHAR(64) NOT NULL DEFAULT '' COMMENT '天气（weather）' AFTER shot_time"},
	{"worklog_photo", "location", "ALTER TABLE worklog_photo ADD COLUMN location VARCHAR(255) NOT NULL DEFAULT '' COMMENT '地点（location）' AFTER weather"},
	{"worklog_photo", "date_ok", "ALTER TABLE worklog_photo ADD COLUMN date_ok TINYINT NULL COMMENT '日期核验：1 相符 0 不符' AFTER lat"},
	{"worklog_photo", "dest_ok", "ALTER TABLE worklog_photo ADD COLUMN dest_ok TINYINT NULL COMMENT '地点核验：1 相符 0 不符' AFTER date_ok"},
	{"worklog_photo", "is_watermark", "ALTER TABLE worklog_photo ADD COLUMN is_watermark TINYINT NOT NULL DEFAULT 1 COMMENT '1 水印照片 0 非水印（免验证、不占每人一张）' AFTER dest_ok"},
	{"worklog_photo", "md5", "ALTER TABLE worklog_photo ADD COLUMN md5 VARCHAR(32) NOT NULL DEFAULT '' COMMENT '图片内容 MD5（商旅按内容合并相同照片）' AFTER is_watermark"},
	{"worklog_photo", "source", "ALTER TABLE worklog_photo ADD COLUMN source VARCHAR(16) NOT NULL DEFAULT 'app' COMMENT '来源：app 壹匣上传 / sgcc 商旅拉回' AFTER md5"},
	{"worklog_photo", "sgcc_img_id", "ALTER TABLE worklog_photo ADD COLUMN sgcc_img_id VARCHAR(512) NOT NULL DEFAULT '' COMMENT '商旅费用照片关联（JSON：{worklog_member_id: 商旅图片id}）' AFTER source"},
	{"worklog_photo", "sgcc_synced", "ALTER TABLE worklog_photo ADD COLUMN sgcc_synced TINYINT NOT NULL DEFAULT 0 COMMENT '商旅费用照片同步：0 未同步 1 已同步 2 同步失败' AFTER sgcc_img_id"},
}

// worklogUniqueKeys 字典唯一键升级：单列 UNIQUE → (team_id, 名称) 班组内联合唯一。
var worklogUniqueKeys = []struct {
	Table, Field, Name string
}{
	{"worklog_vehicle", "plate_no", "uk_team_plate"},
	{"worklog_destination", "name", "uk_team_dest"},
	{"worklog_member", "name", "uk_team_member"},
}

// ensureLegacyColumns 补齐全量老库列（缺列才 ALTER）。
func (m *Module) ensureLegacyColumns(ctx context.Context) error {
	for _, col := range worklogCompatColumns {
		exists, err := worklogColumnExists(ctx, m.DB, col.Table, col.Column)
		if err != nil {
			return fmt.Errorf("worklog: 探测 %s.%s 失败: %w", col.Table, col.Column, err)
		}
		if exists {
			continue
		}
		if _, err := m.DB.ExecContext(ctx, col.DDL); err != nil {
			return fmt.Errorf("worklog: 补列 %s.%s 失败: %w", col.Table, col.Column, err)
		}
	}
	return nil
}

// migrateTeamIDs 补 team_id 列并按默认班组（首个启用班组）回填既有数据。
func (m *Module) migrateTeamIDs(ctx context.Context) error {
	for _, table := range worklogTeamTables {
		exists, err := worklogColumnExists(ctx, m.DB, table, "team_id")
		if err != nil {
			return fmt.Errorf("worklog: 探测 %s.team_id 失败: %w", table, err)
		}
		if exists {
			continue
		}
		ddl := fmt.Sprintf("ALTER TABLE %s ADD COLUMN team_id BIGINT UNSIGNED NULL COMMENT '所属班组，关联 sys_team.id' AFTER id", table)
		if _, err := m.DB.ExecContext(ctx, ddl); err != nil {
			return fmt.Errorf("worklog: 补列 %s.team_id 失败: %w", table, err)
		}
	}
	var defaultTeamID int64
	err := m.DB.QueryRowContext(ctx, `SELECT id FROM sys_team WHERE status = 1 ORDER BY sort, id LIMIT 1`).Scan(&defaultTeamID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil // 尚未建班组：保留 NULL，等账号自举后再回填
	}
	if err != nil {
		return fmt.Errorf("worklog: 读取默认班组失败: %w", err)
	}
	for _, table := range worklogTeamTables {
		if _, err := m.DB.ExecContext(ctx,
			fmt.Sprintf("UPDATE %s SET team_id = ? WHERE team_id IS NULL", table), defaultTeamID); err != nil {
			return fmt.Errorf("worklog: 回填 %s.team_id 失败: %w", table, err)
		}
	}
	return nil
}

// upgradeUniqueKeys 删除旧单列唯一索引并建立（班组 + 名称）联合唯一。
func (m *Module) upgradeUniqueKeys(ctx context.Context) error {
	for _, uk := range worklogUniqueKeys {
		query := fmt.Sprintf(`SELECT INDEX_NAME, COLUMN_NAME FROM information_schema.STATISTICS
			WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = '%s' AND NON_UNIQUE = 0
			  AND INDEX_NAME <> 'PRIMARY' ORDER BY INDEX_NAME, SEQ_IN_INDEX`, uk.Table)
		rows, err := m.DB.QueryContext(ctx, query)
		if err != nil {
			return fmt.Errorf("worklog: 读取 %s 唯一索引失败: %w", uk.Table, err)
		}
		indexes := map[string][]string{}
		order := make([]string, 0, 2)
		for rows.Next() {
			var name, column string
			if err := rows.Scan(&name, &column); err != nil {
				rows.Close()
				return fmt.Errorf("worklog: 解析 %s 唯一索引失败: %w", uk.Table, err)
			}
			if _, ok := indexes[name]; !ok {
				order = append(order, name)
			}
			indexes[name] = append(indexes[name], column)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return fmt.Errorf("worklog: 读取 %s 唯一索引失败: %w", uk.Table, err)
		}
		hasTeamUnique := false
		for _, columns := range indexes {
			hasTeam, hasField := false, false
			for _, column := range columns {
				switch column {
				case "team_id":
					hasTeam = true
				case uk.Field:
					hasField = true
				}
			}
			if hasTeam && hasField {
				hasTeamUnique = true
				break
			}
		}
		if hasTeamUnique {
			continue
		}
		// 先建新联合唯一、再删旧全局唯一：ADD 失败时旧约束仍在，不出现无约束窗口。
		// 唯一例外：旧单列唯一索引名恰好与新联合键同名，必须先删同名索引才能 ADD。
		oldIndexes := make([]string, 0, len(order))
		for _, name := range order {
			if columns := indexes[name]; len(columns) == 1 && columns[0] == uk.Field {
				if name == uk.Name {
					if _, err := m.DB.ExecContext(ctx, fmt.Sprintf("ALTER TABLE %s DROP INDEX `%s`", uk.Table, name)); err != nil {
						return fmt.Errorf("worklog: 删除 %s 旧唯一索引 %s 失败: %w", uk.Table, name, err)
					}
					continue
				}
				oldIndexes = append(oldIndexes, name)
			}
		}
		if _, err := m.DB.ExecContext(ctx,
			fmt.Sprintf("ALTER TABLE %s ADD UNIQUE KEY %s (team_id, %s)", uk.Table, uk.Name, uk.Field)); err != nil {
			return fmt.Errorf("worklog: 建立 %s.%s 失败：班组内 %s 可能存在重复数据，"+
				"请先执行 SELECT team_id, %s, COUNT(*) FROM %s GROUP BY team_id, %s HAVING COUNT(*) > 1 合并后重启: %w",
				uk.Table, uk.Name, uk.Field, uk.Field, uk.Table, uk.Field, err)
		}
		for _, name := range oldIndexes {
			if _, err := m.DB.ExecContext(ctx, fmt.Sprintf("ALTER TABLE %s DROP INDEX `%s`", uk.Table, name)); err != nil {
				return fmt.Errorf("worklog: 删除 %s 旧唯一索引 %s 失败: %w", uk.Table, name, err)
			}
		}
	}
	return nil
}

// worklogColumnExists 列是否存在（表名/列名来自固定白名单，直接内联）。
func worklogColumnExists(ctx context.Context, db *sql.DB, table, column string) (bool, error) {
	query := fmt.Sprintf(`SELECT COLUMN_NAME FROM information_schema.COLUMNS
		WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = '%s' AND COLUMN_NAME = '%s'`, table, column)
	var name string
	err := db.QueryRowContext(ctx, query).Scan(&name)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}
