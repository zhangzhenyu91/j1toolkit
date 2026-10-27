package crossing

// schemaDDL 本模块表结构（对齐 server/src/crossing/schema.js）：
// 直推幂等键 uk_crane = (team_id, crane_session_id, crane_record_id)，空值一律存 NULL
// （MySQL 唯一索引不约束 NULL，空串会撞唯一键）；删除墓碑 crossing_deleted_id 供内网同步识别「服务端已删」行。
var schemaDDL = []string{
	`CREATE TABLE IF NOT EXISTS crossing_record (
    id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
    team_id BIGINT UNSIGNED NOT NULL COMMENT '所属班组，关联 sys_team.id',
    voltage_level VARCHAR(16) NOT NULL DEFAULT '' COMMENT '电压等级（数值文本，展示时拼 kV）',
    line_name VARCHAR(64) NOT NULL DEFAULT '' COMMENT '线路名称',
    section VARCHAR(32) NOT NULL DEFAULT '' COMMENT '跨越区段展示文本（如 15#-16#）',
    pole_from INT NULL COMMENT '起始杆号（小号侧）',
    pole_to INT NULL COMMENT '终止杆号（大号侧）',
    crossing_type VARCHAR(128) NOT NULL DEFAULT '' COMMENT '跨越类型（跨越物原文：厂区/10kV线路/通讯线等）',
    dist_small VARCHAR(16) NOT NULL DEFAULT '' COMMENT '交叉点距小号侧距离（m）',
    dist_big VARCHAR(16) NOT NULL DEFAULT '' COMMENT '交叉点距大号侧距离（m）',
    cross_horizontal VARCHAR(16) NOT NULL DEFAULT '' COMMENT '交叉物距边线水平距离（m，一般为空）',
    cross_vertical VARCHAR(16) NOT NULL DEFAULT '' COMMENT '交叉垂直距离（m）',
    cross_angle VARCHAR(16) NOT NULL DEFAULT '' COMMENT '交叉角（°）',
    has_hazard VARCHAR(16) NOT NULL DEFAULT '' COMMENT '是否存在隐患（原文，一般为空）',
    finder_name VARCHAR(32) NOT NULL DEFAULT '' COMMENT '发现人',
    inspect_at DATE NULL COMMENT '巡视时间',
    remark VARCHAR(255) NOT NULL DEFAULT '' COMMENT '备注',
    source VARCHAR(16) NOT NULL DEFAULT 'crane' COMMENT '来源：crane 直推 / staging 暂存区导入 / intranet 内网回写',
    crane_session_id VARCHAR(64) NULL COMMENT 'crane-answer 排查会话 id（幂等键，空存 NULL）',
    crane_record_id VARCHAR(64) NULL COMMENT 'crane-answer 记录 id（幂等键，空存 NULL）',
    synced_at DATETIME NULL COMMENT '上次与内网同步时刻（NULL=从未同步，差异比对基准）',
    created_by BIGINT UNSIGNED NULL COMMENT '创建人，关联 sys_user.id（直推为 NULL）',
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    KEY idx_team_line (team_id, line_name),
    UNIQUE KEY uk_crane (team_id, crane_session_id, crane_record_id)
  ) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,
	`CREATE TABLE IF NOT EXISTS crossing_staging_batch (
    id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
    team_id BIGINT UNSIGNED NOT NULL COMMENT '所属班组，关联 sys_team.id',
    crane_session_id VARCHAR(64) NOT NULL DEFAULT '' COMMENT 'crane-answer 排查会话 id（班组内唯一）',
    group_name VARCHAR(64) NOT NULL DEFAULT '' COMMENT '微信群名称',
    pusher_name VARCHAR(32) NOT NULL DEFAULT '' COMMENT '推送人昵称',
    review_note VARCHAR(255) NULL COMMENT '审核意见（审核不通过时的修改说明，展示给暂存区修改人）',
    xlsx_path VARCHAR(255) NOT NULL DEFAULT '' COMMENT '随批 xlsx 落盘路径（相对 server 目录，data/crossing/inbox/ 下）',
    status VARCHAR(16) NOT NULL DEFAULT 'pending' COMMENT '状态：pending 待导入 / imported 已导入 / discarded 已废弃',
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE KEY uk_team_session (team_id, crane_session_id)
  ) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,
	`CREATE TABLE IF NOT EXISTS crossing_staging_record (
    id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
    batch_id BIGINT UNSIGNED NOT NULL COMMENT '关联 crossing_staging_batch.id',
    crane_record_id VARCHAR(64) NULL COMMENT 'crane-answer 记录 id（导入时随批次写入主表幂等键，空存 NULL）',
    voltage_level VARCHAR(16) NOT NULL DEFAULT '' COMMENT '电压等级',
    line_name VARCHAR(64) NOT NULL DEFAULT '' COMMENT '线路名称',
    section VARCHAR(32) NOT NULL DEFAULT '' COMMENT '跨越区段展示文本',
    crossing_type VARCHAR(128) NOT NULL DEFAULT '' COMMENT '跨越类型（跨越物原文）',
    dist_small VARCHAR(16) NOT NULL DEFAULT '' COMMENT '交叉点距小号侧距离（m）',
    dist_big VARCHAR(16) NOT NULL DEFAULT '' COMMENT '交叉点距大号侧距离（m）',
    cross_horizontal VARCHAR(16) NOT NULL DEFAULT '' COMMENT '交叉物距边线水平距离（m）',
    cross_vertical VARCHAR(16) NOT NULL DEFAULT '' COMMENT '交叉垂直距离（m）',
    cross_angle VARCHAR(16) NOT NULL DEFAULT '' COMMENT '交叉角（°）',
    has_hazard VARCHAR(16) NOT NULL DEFAULT '' COMMENT '是否存在隐患（原文）',
    finder_name VARCHAR(32) NOT NULL DEFAULT '' COMMENT '发现人',
    inspect_at DATE NULL COMMENT '巡视时间',
    remark VARCHAR(255) NOT NULL DEFAULT '' COMMENT '备注',
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    KEY idx_batch (batch_id)
  ) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,
	`CREATE TABLE IF NOT EXISTS crossing_conflict (
    id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
    team_id BIGINT UNSIGNED NOT NULL COMMENT '所属班组，关联 sys_team.id',
    record_id BIGINT UNSIGNED NULL COMMENT '关联 crossing_record.id（疑似重复冲突时为服务端命中行）',
    server_snapshot JSON NULL COMMENT '服务端业务列快照',
    intranet_snapshot JSON NULL COMMENT '内网表行业务列快照',
    status VARCHAR(16) NOT NULL DEFAULT 'pending' COMMENT '状态：pending 待解决 / resolved 已解决',
    resolution VARCHAR(16) NULL COMMENT '解决方式：server 保留服务端 / intranet 采用内网',
    resolved_by BIGINT UNSIGNED NULL COMMENT '解决人，关联 sys_user.id',
    resolved_at DATETIME NULL COMMENT '解决时间',
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    KEY idx_team_status (team_id, status)
  ) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,
	`CREATE TABLE IF NOT EXISTS crossing_deleted_id (
    id BIGINT UNSIGNED NOT NULL PRIMARY KEY COMMENT '被删记录 id（墓碑）',
    team_id BIGINT UNSIGNED NOT NULL COMMENT '所属班组，关联 sys_team.id',
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
  ) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,
	`CREATE TABLE IF NOT EXISTS crossing_sync_log (
    id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
    team_id BIGINT UNSIGNED NOT NULL COMMENT '所属班组，关联 sys_team.id',
    kind VARCHAR(16) NOT NULL DEFAULT 'manual' COMMENT '同步方式：manual 手动 / night 夜间',
    result TEXT NULL COMMENT '结果摘要',
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
  ) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,
}
