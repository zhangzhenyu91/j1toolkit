// 班组数据：建表（team-data 应用种子在 treehazard/schema.js 一并写入，此处仅表结构）
// team_line 运维线路：线路相关功能的统一线路源（树障隐患分组/导出于 2026-10 起换源此表，与杆塔坐标表解耦）；
// 班组内 line_name 唯一（uk_team_line），sort 为班内排序（管理端上移/下移）
const DDL = [
  `CREATE TABLE IF NOT EXISTS team_line (
    id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
    team_id BIGINT UNSIGNED NOT NULL COMMENT '所属班组，关联 sys_team.id',
    voltage_level VARCHAR(16) NOT NULL DEFAULT '' COMMENT '电压等级（数值文本，展示时拼 kV）',
    line_name VARCHAR(64) NOT NULL DEFAULT '' COMMENT '线路名称',
    sort INT NOT NULL DEFAULT 0 COMMENT '班组内排序，小的在前',
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    UNIQUE KEY uk_team_line (team_id, line_name),
    KEY idx_team (team_id)
  ) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,
];

async function ensureTeamdataSchema(pool) {
  for (const sql of DDL) {
    await pool.query(sql);
  }
}

module.exports = { ensureTeamdataSchema };
