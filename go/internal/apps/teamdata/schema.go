package teamdata

import (
	"context"
	"database/sql"
	"fmt"
)

// teamLineDDL 运维线路建表（与 server/src/teamdata/schema.js 一致）：
// 线路相关功能的统一线路源（树障隐患分组/导出共用）；班组内 line_name 唯一（uk_team_line），
// sort 为班内排序（管理端上移/下移）。
const teamLineDDL = `CREATE TABLE IF NOT EXISTS team_line (
  id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
  team_id BIGINT UNSIGNED NOT NULL COMMENT '所属班组，关联 sys_team.id',
  voltage_level VARCHAR(16) NOT NULL DEFAULT '' COMMENT '电压等级（数值文本，展示时拼 kV）',
  line_name VARCHAR(64) NOT NULL DEFAULT '' COMMENT '线路名称',
  sort INT NOT NULL DEFAULT 0 COMMENT '班组内排序，小的在前',
  created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  UNIQUE KEY uk_team_line (team_id, line_name),
  KEY idx_team (team_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`

// EnsureSchema 建表（幂等；仅 team_line，worklog_* 表归出工日志应用建表）。
func (m *Module) EnsureSchema(ctx context.Context) error {
	if m.DB == nil {
		return fmt.Errorf("teamdata: 数据库未接入")
	}
	if _, err := m.DB.ExecContext(ctx, teamLineDDL); err != nil {
		return fmt.Errorf("teamdata: 建表 team_line: %w", err)
	}
	return nil
}

// 确保 *sql.DB 满足内部使用面（编译期检查）。
var _ = sql.ErrNoRows
