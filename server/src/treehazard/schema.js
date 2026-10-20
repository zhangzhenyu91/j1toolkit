// 树障隐患记录：表结构初始化与应用种子（启动时由 db.js 调用）
const config = require('../config');

// 子应用登记：树障隐患记录（双端）；班组数据（双端，树障等班组级数据看板承载）
const APP_TREE_HAZARD = {
  key: 'tree-hazard',
  name: '树障隐患记录',
  icon: 'forest',
  path: '/pkg-treehazard/pages/index/index',
  sort: 8,
  terminal: 'both', // 适配终端：双端（见 db.js sys_app.terminal 口径）
};
const APP_TEAM_DATA = {
  key: 'team-data',
  name: '班组数据',
  icon: 'chart-bar',
  path: '/pkg-teamdata/pages/index/index',
  sort: 9,
  terminal: 'both',
};

const DDL = [
  // 隐患记录主表：crane-answer 直推（source=crane）/ 暂存区导入（staging）/ 内网回写（intranet）
  // uk_crane 直推幂等键：crane_session_id / crane_record_id 空值一律存 NULL（MySQL 唯一索引不约束 NULL，空串会撞唯一键）
  `CREATE TABLE IF NOT EXISTS treehazard_record (
    id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
    team_id BIGINT UNSIGNED NOT NULL COMMENT '所属班组，关联 sys_team.id',
    voltage_level VARCHAR(16) NOT NULL DEFAULT '' COMMENT '电压等级（数值文本，展示时拼 kV）',
    line_name VARCHAR(64) NOT NULL DEFAULT '' COMMENT '线路名称',
    section VARCHAR(32) NOT NULL DEFAULT '' COMMENT '区段展示文本（如 3#-4#）',
    pole_from INT NULL COMMENT '起始杆号（小号侧）',
    pole_to INT NULL COMMENT '终止杆号（大号侧）',
    position_note VARCHAR(32) NOT NULL DEFAULT '' COMMENT '位置备注',
    tree_species VARCHAR(32) NOT NULL DEFAULT '' COMMENT '树种',
    tree_count VARCHAR(16) NOT NULL DEFAULT '' COMMENT '数量',
    dist_small VARCHAR(16) NOT NULL DEFAULT '' COMMENT '距小号侧距离（m）',
    dist_big VARCHAR(16) NOT NULL DEFAULT '' COMMENT '距大号侧距离（m）',
    diameter VARCHAR(16) NOT NULL DEFAULT '' COMMENT '直径（cm）',
    horizontal VARCHAR(16) NOT NULL DEFAULT '' COMMENT '水平距离（m）',
    vertical VARCHAR(16) NOT NULL DEFAULT '' COMMENT '垂直距离原文',
    vertical_num DECIMAL(6,2) NULL COMMENT '垂直距离解析值（取首个数字，供阈值比对）',
    finder_name VARCHAR(32) NOT NULL DEFAULT '' COMMENT '发现人',
    found_at DATE NULL COMMENT '发现时间',
    handler_name VARCHAR(32) NOT NULL DEFAULT '' COMMENT '处理人',
    handle_note VARCHAR(64) NOT NULL DEFAULT '' COMMENT '处理情况',
    handled_at DATE NULL COMMENT '处理时间',
    status VARCHAR(16) NOT NULL DEFAULT 'unhandled' COMMENT '状态：unhandled 未处理 / handled 已处理',
    source VARCHAR(16) NOT NULL DEFAULT 'crane' COMMENT '来源：crane 直推 / staging 暂存区导入 / intranet 内网回写',
    crane_session_id VARCHAR(64) NULL COMMENT 'crane-answer 排查会话 id（幂等键，空存 NULL）',
    crane_record_id VARCHAR(64) NULL COMMENT 'crane-answer 记录 id（幂等键，空存 NULL）',
    synced_at DATETIME NULL COMMENT '上次与内网同步时刻（NULL=从未同步，差异比对基准）',
    created_by BIGINT UNSIGNED NULL COMMENT '创建人，关联 sys_user.id（直推为 NULL）',
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    KEY idx_team_status (team_id, status),
    KEY idx_team_line (team_id, line_name),
    UNIQUE KEY uk_crane (team_id, crane_session_id, crane_record_id)
  ) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,
  // 处理前/处理后照片（kind=evidence 预留：直推原始证据照，不开放上传）
  `CREATE TABLE IF NOT EXISTS treehazard_photo (
    id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
    record_id BIGINT UNSIGNED NOT NULL COMMENT '关联 treehazard_record.id',
    kind VARCHAR(16) NOT NULL COMMENT '类型：evidence 证据（预留）/ before 处理前 / after 处理后',
    cos_key VARCHAR(255) NOT NULL DEFAULT '' COMMENT 'COS 对象键（COS_TREEHAZARD_PREFIX 前缀）',
    url VARCHAR(512) NOT NULL DEFAULT '' COMMENT '访问地址',
    uploaded_by BIGINT UNSIGNED NULL COMMENT '上传人，关联 sys_user.id',
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    KEY idx_record (record_id)
  ) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,
  // 直推暂存批次（mode=staging：班组管理员确认后再导入正式表；同会话整批替换）
  `CREATE TABLE IF NOT EXISTS treehazard_staging_batch (
    id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
    team_id BIGINT UNSIGNED NOT NULL COMMENT '所属班组，关联 sys_team.id',
    crane_session_id VARCHAR(64) NOT NULL DEFAULT '' COMMENT 'crane-answer 排查会话 id（班组内唯一）',
    group_name VARCHAR(64) NOT NULL DEFAULT '' COMMENT '微信群名称',
    pusher_name VARCHAR(32) NOT NULL DEFAULT '' COMMENT '推送人昵称',
    review_note VARCHAR(255) NULL COMMENT '审核意见（审核不通过时的修改说明，展示给暂存区修改人；仅 mode=staging 有意义）',
    xlsx_path VARCHAR(255) NOT NULL DEFAULT '' COMMENT '随批 xlsx 落盘路径（相对 server 目录，data/treehazard/inbox/ 下）',
    status VARCHAR(16) NOT NULL DEFAULT 'pending' COMMENT '状态：pending 待导入 / imported 已导入 / discarded 已废弃',
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE KEY uk_team_session (team_id, crane_session_id)
  ) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,
  // 暂存批次的记录行（业务列与主表「发现 12 列 + 处理 3 列」同构，另带 crane_record_id / vertical_num）
  `CREATE TABLE IF NOT EXISTS treehazard_staging_record (
    id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
    batch_id BIGINT UNSIGNED NOT NULL COMMENT '关联 treehazard_staging_batch.id',
    crane_record_id VARCHAR(64) NULL COMMENT 'crane-answer 记录 id（导入时随批次写入主表幂等键，空存 NULL）',
    voltage_level VARCHAR(16) NOT NULL DEFAULT '' COMMENT '电压等级',
    line_name VARCHAR(64) NOT NULL DEFAULT '' COMMENT '线路名称',
    section VARCHAR(32) NOT NULL DEFAULT '' COMMENT '区段展示文本',
    tree_species VARCHAR(32) NOT NULL DEFAULT '' COMMENT '树种',
    tree_count VARCHAR(16) NOT NULL DEFAULT '' COMMENT '数量',
    dist_small VARCHAR(16) NOT NULL DEFAULT '' COMMENT '距小号侧距离（m）',
    dist_big VARCHAR(16) NOT NULL DEFAULT '' COMMENT '距大号侧距离（m）',
    diameter VARCHAR(16) NOT NULL DEFAULT '' COMMENT '直径（cm）',
    horizontal VARCHAR(16) NOT NULL DEFAULT '' COMMENT '水平距离（m）',
    vertical VARCHAR(16) NOT NULL DEFAULT '' COMMENT '垂直距离原文',
    vertical_num DECIMAL(6,2) NULL COMMENT '垂直距离解析值',
    finder_name VARCHAR(32) NOT NULL DEFAULT '' COMMENT '发现人',
    found_at DATE NULL COMMENT '发现时间',
    handler_name VARCHAR(32) NOT NULL DEFAULT '' COMMENT '处理人',
    handle_note VARCHAR(64) NOT NULL DEFAULT '' COMMENT '处理情况',
    handled_at DATE NULL COMMENT '处理时间',
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    KEY idx_batch (batch_id)
  ) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,
  // 暂存批次的辅证照片（直推随批上传 COS；转入正式表时改挂 treehazard_photo，废弃/整批替换时连带清理）
  `CREATE TABLE IF NOT EXISTS treehazard_staging_photo (
    id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
    batch_id BIGINT UNSIGNED NOT NULL COMMENT '关联 treehazard_staging_batch.id',
    crane_record_id VARCHAR(64) NOT NULL DEFAULT '' COMMENT 'crane-answer 记录 id（转入时据以归挂正式记录）',
    cos_key VARCHAR(255) NOT NULL DEFAULT '' COMMENT 'COS 对象键（COS_TREEHAZARD_PREFIX 前缀）',
    url VARCHAR(512) NOT NULL DEFAULT '' COMMENT '访问地址',
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    KEY idx_batch (batch_id)
  ) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,
  // 内网同步冲突（双边都改过：服务端快照与内网快照留档，人工二选一解决）
  `CREATE TABLE IF NOT EXISTS treehazard_conflict (
    id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
    team_id BIGINT UNSIGNED NOT NULL COMMENT '所属班组，关联 sys_team.id',
    record_id BIGINT UNSIGNED NULL COMMENT '关联 treehazard_record.id（疑似重复冲突时为服务端命中行）',
    server_snapshot JSON NULL COMMENT '服务端业务列快照',
    intranet_snapshot JSON NULL COMMENT '内网表行业务列快照',
    status VARCHAR(16) NOT NULL DEFAULT 'pending' COMMENT '状态：pending 待解决 / resolved 已解决',
    resolution VARCHAR(16) NULL COMMENT '解决方式：server 保留服务端 / intranet 采用内网',
    resolved_by BIGINT UNSIGNED NULL COMMENT '解决人，关联 sys_user.id',
    resolved_at DATETIME NULL COMMENT '解决时间',
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    KEY idx_team_status (team_id, status)
  ) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,
  // 班组级设置：垂直距离阈值（未处理且解析值 ≤ 阈值 计入看板「超阈值」）
  `CREATE TABLE IF NOT EXISTS treehazard_team_setting (
    team_id BIGINT UNSIGNED NOT NULL UNIQUE COMMENT '所属班组，关联 sys_team.id',
    vertical_threshold DECIMAL(6,2) NULL COMMENT '垂直距离阈值（m），NULL=未配置',
    updated_by BIGINT UNSIGNED NULL COMMENT '最近修改人，关联 sys_user.id',
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP
  ) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,
  // 删除墓碑：记录被删 id（内网同步时识别「服务端已删」行，不再回灌）
  `CREATE TABLE IF NOT EXISTS treehazard_deleted_id (
    id BIGINT UNSIGNED NOT NULL PRIMARY KEY COMMENT '被删记录 id（墓碑）',
    team_id BIGINT UNSIGNED NOT NULL COMMENT '所属班组，关联 sys_team.id',
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
  ) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,
  // 内网 U 盘同步日志（manual 手动 / idle 空闲 / night 夜间）
  `CREATE TABLE IF NOT EXISTS treehazard_sync_log (
    id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
    team_id BIGINT UNSIGNED NOT NULL COMMENT '所属班组，关联 sys_team.id',
    kind VARCHAR(16) NOT NULL DEFAULT 'manual' COMMENT '同步方式：manual 手动 / idle 空闲 / night 夜间',
    result TEXT NULL COMMENT '结果摘要',
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
  ) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,
];

async function ensureTreehazardSchema(pool) {
  for (const sql of DDL) {
    await pool.query(sql);
  }

  // 老库兼容：treehazard_staging_batch 补 review_note 列（MySQL 的 ALTER 不支持 IF NOT EXISTS，先查 information_schema）
  const [reviewNoteCols] = await pool.query(
    `SELECT COLUMN_NAME FROM information_schema.COLUMNS
     WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'treehazard_staging_batch' AND COLUMN_NAME = 'review_note'`
  );
  if (!reviewNoteCols.length) {
    await pool.query(
      `ALTER TABLE treehazard_staging_batch ADD COLUMN review_note VARCHAR(255) NULL
       COMMENT '审核意见（审核不通过时的修改说明，展示给暂存区修改人；仅 mode=staging 有意义）' AFTER pusher_name`
    );
    console.log('[初始化] 已为 treehazard_staging_batch 补充 review_note 列');
  }

  // 写入/更新应用记录（同题库刷题种子模式；terminal 随种子刷新）
  for (const app of [APP_TREE_HAZARD, APP_TEAM_DATA]) {
    await pool.query(
      `INSERT INTO sys_app (app_key, name, icon, path, terminal, sort, status) VALUES (?, ?, ?, ?, ?, ?, 1)
       ON DUPLICATE KEY UPDATE name = VALUES(name), icon = VALUES(icon), path = VALUES(path),
         terminal = VALUES(terminal), sort = VALUES(sort)`,
      [app.key, app.name, app.icon, app.path, app.terminal, app.sort]
    );
  }

  // 管理员默认授予 树障隐患记录 / 班组数据 权限
  const [adminRows] = await pool.query('SELECT id FROM sys_user WHERE username = ?', [config.admin.username]);
  if (adminRows.length) {
    for (const app of [APP_TREE_HAZARD, APP_TEAM_DATA]) {
      await pool.query(
        'INSERT IGNORE INTO sys_user_app (user_id, app_id) SELECT ?, id FROM sys_app WHERE app_key = ?',
        [adminRows[0].id, app.key]
      );
    }
  }
}

module.exports = { ensureTreehazardSchema };
