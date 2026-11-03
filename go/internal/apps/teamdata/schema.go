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

// teamLineLinkDDL 线路关联建表：同塔双回等场景把两条（或多条）运维线路并入统一线路名，
// alias_name 为原线路名、line_name 为关联后的统一线路名；供树障/交跨/微信应答统一识别。
const teamLineLinkDDL = `CREATE TABLE IF NOT EXISTS team_line_link (
  id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
  team_id BIGINT UNSIGNED NOT NULL COMMENT '所属班组，关联 sys_team.id',
  line_name VARCHAR(64) NOT NULL DEFAULT '' COMMENT '关联后的统一线路名称',
  alias_name VARCHAR(64) NOT NULL DEFAULT '' COMMENT '被并入的原线路名称',
  created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  UNIQUE KEY uk_team_line_link (team_id, alias_name),
  KEY idx_team_line (team_id, line_name)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`

// EnsureSchema 建表（幂等；team_line / team_line_link，worklog_* 表归出工日志应用建表）。
func (m *Module) EnsureSchema(ctx context.Context) error {
	if m.DB == nil {
		return fmt.Errorf("teamdata: 数据库未接入")
	}
	for _, ddl := range []string{teamLineDDL, teamLineLinkDDL} {
		if _, err := m.DB.ExecContext(ctx, ddl); err != nil {
			return fmt.Errorf("teamdata: 建表 team_line: %w", err)
		}
	}
	return nil
}

// 确保 *sql.DB 满足内部使用面（编译期检查）。
var _ = sql.ErrNoRows
