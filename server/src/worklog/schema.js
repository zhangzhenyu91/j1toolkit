// 出工日志：表结构初始化与应用/成员种子（仅 WORKLOG_ENABLED=true 时由 db.js 调用）
// 表结构对应《开发指南》3.5；一条日志卡片 = 一次派车，未出车即 vehicle_id 为 NULL
const fs = require('fs');
const path = require('path');
const config = require('../config');

const APP_WORK_LOG = {
  key: 'work-log',
  name: '出工日志',
  icon: 'calendar',
  path: '/pkg-worklog/pages/index/index',
  sort: 2,
  terminal: 'both', // 适配终端：双端（见 db.js sys_app.terminal 口径）
};

// 首批出工成员种子（sort 即点亮按钮顺序）
const MEMBER_SEED = ['赵登', '郑海楠', '任舒诺', '薛忠亮', '曹万鑫', '张振宇', '高麒涵'];

const DDL = [
  `CREATE TABLE IF NOT EXISTS worklog_entry (
    id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
    team_id BIGINT UNSIGNED NULL COMMENT '所属班组，关联 sys_team.id',
    log_date DATE NOT NULL COMMENT '日志日期',
    patrol_content TEXT NULL COMMENT '巡视内容（可空）',
    remark TEXT NULL COMMENT '备注（可空）',
    remark_files JSON NULL COMMENT '备注附件 [{name,url,cos_key,type,size}]（type=image/video/doc）',
    vehicle_id BIGINT UNSIGNED NULL COMMENT '车牌，关联 worklog_vehicle.id；NULL=未出车',
    destination_id BIGINT UNSIGNED NULL COMMENT '目的地，关联 worklog_destination.id',
    created_by BIGINT UNSIGNED NOT NULL COMMENT '创建人，关联 sys_user.id',
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    KEY idx_log_date (log_date),
    KEY idx_team (team_id)
  ) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,
  `CREATE TABLE IF NOT EXISTS worklog_entry_member (
    id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
    entry_id BIGINT UNSIGNED NOT NULL COMMENT '关联 worklog_entry.id',
    member_id BIGINT UNSIGNED NOT NULL COMMENT '关联 worklog_member.id',
    checked TINYINT NOT NULL DEFAULT 0 COMMENT '打卡：0 未打卡 1 已打卡',
    sort INT NOT NULL DEFAULT 0 COMMENT '展示顺序',
    UNIQUE KEY uk_entry_member (entry_id, member_id),
    KEY idx_entry (entry_id)
  ) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,
  `CREATE TABLE IF NOT EXISTS worklog_photo (
    id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
    entry_id BIGINT UNSIGNED NOT NULL COMMENT '关联 worklog_entry.id',
    cos_key VARCHAR(255) NOT NULL COMMENT 'COS 对象键',
    url VARCHAR(512) NOT NULL COMMENT '照片访问地址',
    members JSON NOT NULL COMMENT '所属人名数组',
    verify_status VARCHAR(16) NOT NULL DEFAULT 'pending' COMMENT 'pending/passed/mismatch/failed（旧值 date_mismatch/dest_mismatch 仅历史数据）',
    work_content VARCHAR(512) NOT NULL DEFAULT '' COMMENT 'Dify 返回施工内容（title）',
    shot_time VARCHAR(32) NOT NULL DEFAULT '' COMMENT '水印拍摄时间（time）',
    weather VARCHAR(64) NOT NULL DEFAULT '' COMMENT '天气（weather）',
    location VARCHAR(255) NOT NULL DEFAULT '' COMMENT '地点（location）',
    lng VARCHAR(32) NOT NULL DEFAULT '' COMMENT '经度',
    lat VARCHAR(32) NOT NULL DEFAULT '' COMMENT '纬度',
    date_ok TINYINT NULL COMMENT '日期核验：1 相符 0 不符（NULL=历史数据未存）',
    dest_ok TINYINT NULL COMMENT '地点核验：1 相符 0 不符（NULL=历史数据未存）',
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    KEY idx_entry (entry_id)
  ) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,
  `CREATE TABLE IF NOT EXISTS worklog_vehicle (
    id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
    team_id BIGINT UNSIGNED NULL COMMENT '所属班组，关联 sys_team.id',
    plate_no VARCHAR(32) NOT NULL COMMENT '车牌号（班组内唯一）',
    sort INT NOT NULL DEFAULT 0 COMMENT '下拉排序',
    status TINYINT NOT NULL DEFAULT 1 COMMENT '1 启用 0 停用',
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE KEY uk_team_plate (team_id, plate_no)
  ) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,
  `CREATE TABLE IF NOT EXISTS worklog_destination (
    id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
    team_id BIGINT UNSIGNED NULL COMMENT '所属班组，关联 sys_team.id',
    name VARCHAR(64) NOT NULL COMMENT '目的地名称（班组内唯一）',
    sort INT NOT NULL DEFAULT 0 COMMENT '下拉排序',
    status TINYINT NOT NULL DEFAULT 1 COMMENT '1 启用 0 停用',
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE KEY uk_team_dest (team_id, name)
  ) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,
  `CREATE TABLE IF NOT EXISTS worklog_member (
    id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
    team_id BIGINT UNSIGNED NULL COMMENT '所属班组，关联 sys_team.id',
    user_id BIGINT UNSIGNED NULL COMMENT '账号同步成员关联 sys_user.id；NULL=手动添加',
    name VARCHAR(64) NOT NULL COMMENT '成员姓名（班组内唯一）',
    sort INT NOT NULL DEFAULT 0 COMMENT '点亮按钮排列顺序',
    status TINYINT NOT NULL DEFAULT 1 COMMENT '1 启用 0 停用',
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE KEY uk_team_member (team_id, name)
  ) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,
  `CREATE TABLE IF NOT EXISTS worklog_tower (
    id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
    team_id BIGINT UNSIGNED NOT NULL COMMENT '所属班组，关联 sys_team.id',
    voltage_level VARCHAR(32) NOT NULL DEFAULT '' COMMENT '电压等级',
    line_name VARCHAR(64) NOT NULL DEFAULT '' COMMENT '线路名称',
    tower_no VARCHAR(64) NOT NULL DEFAULT '' COMMENT '杆塔号',
    lng VARCHAR(32) NOT NULL DEFAULT '' COMMENT '经度',
    lat VARCHAR(32) NOT NULL DEFAULT '' COMMENT '纬度',
    sort INT NOT NULL DEFAULT 0 COMMENT '行序（导入顺序）',
    KEY idx_team (team_id)
  ) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,
];

async function ensureWorklogSchema(pool) {
  for (const sql of DDL) {
    await pool.query(sql);
  }

  // ===== 班组隔离迁移 =====
  // 老库兼容：四张业务表补 team_id 列，既有数据回填默认班组（首个启用班组 = 检修一班）
  const TEAM_TABLES = ['worklog_entry', 'worklog_vehicle', 'worklog_destination', 'worklog_member'];
  for (const table of TEAM_TABLES) {
    const [cols] = await pool.query(
      `SELECT COLUMN_NAME FROM information_schema.COLUMNS
       WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = ? AND COLUMN_NAME = 'team_id'`,
      [table]
    );
    if (!cols.length) {
      await pool.query(
        `ALTER TABLE ${table} ADD COLUMN team_id BIGINT UNSIGNED NULL COMMENT '所属班组，关联 sys_team.id' AFTER id`
      );
      console.log(`[初始化] 已为 ${table} 补充 team_id 列`);
    }
  }
  const [teamRows] = await pool.query('SELECT id FROM sys_team WHERE status = 1 ORDER BY sort, id LIMIT 1');
  const defaultTeamId = teamRows.length ? teamRows[0].id : null;
  if (defaultTeamId) {
    for (const table of TEAM_TABLES) {
      await pool.query(`UPDATE ${table} SET team_id = ? WHERE team_id IS NULL`, [defaultTeamId]);
    }
  }

  // 老库兼容：worklog_entry 补 team_id 索引
  const [entryIdx] = await pool.query(
    `SELECT INDEX_NAME FROM information_schema.STATISTICS
     WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'worklog_entry' AND INDEX_NAME = 'idx_team'`
  );
  if (!entryIdx.length) {
    await pool.query('ALTER TABLE worklog_entry ADD KEY idx_team (team_id)');
  }

  // 老库兼容：worklog_member 补 user_id 列（账号同步成员标记；NULL=手动添加）
  const [uidCols] = await pool.query(
    `SELECT COLUMN_NAME FROM information_schema.COLUMNS
     WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'worklog_member' AND COLUMN_NAME = 'user_id'`
  );
  if (!uidCols.length) {
    await pool.query(
      `ALTER TABLE worklog_member ADD COLUMN user_id BIGINT UNSIGNED NULL
       COMMENT '账号同步成员关联 sys_user.id；NULL=手动添加' AFTER team_id`
    );
    console.log('[初始化] 已为 worklog_member 补充 user_id 列');
  }

  // 字典唯一约束迁移：单列 UNIQUE → (team_id, 名称) 班组内联合唯一（须先完成 team_id 回填）
  for (const [table, field, ukName] of [
    ['worklog_vehicle', 'plate_no', 'uk_team_plate'],
    ['worklog_destination', 'name', 'uk_team_dest'],
    ['worklog_member', 'name', 'uk_team_member'],
  ]) {
    const [stats] = await pool.query(
      `SELECT INDEX_NAME, COLUMN_NAME FROM information_schema.STATISTICS
       WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = ? AND NON_UNIQUE = 0 AND INDEX_NAME <> 'PRIMARY'
       ORDER BY INDEX_NAME, SEQ_IN_INDEX`,
      [table]
    );
    const byIndex = {};
    stats.forEach((s) => {
      (byIndex[s.INDEX_NAME] = byIndex[s.INDEX_NAME] || []).push(s.COLUMN_NAME);
    });
    if (Object.values(byIndex).some((cols) => cols.includes('team_id') && cols.includes(field))) continue;
    for (const [idxName, cols] of Object.entries(byIndex)) {
      if (cols.length === 1 && cols[0] === field) {
        await pool.query(`ALTER TABLE ${table} DROP INDEX \`${idxName}\``);
        console.log(`[初始化] 已删除 ${table} 旧唯一索引 ${idxName}`);
      }
    }
    await pool.query(`ALTER TABLE ${table} ADD UNIQUE KEY ${ukName} (team_id, ${field})`);
    console.log(`[初始化] 已为 ${table} 建立（班组 + ${field}）联合唯一索引`);
  }

  // 杆塔坐标种子：空表时把随仓分发的检修一班坐标 JSON 迁入默认班组
  const [towerCnt] = await pool.query('SELECT COUNT(*) AS cnt FROM worklog_tower');
  if (!towerCnt[0].cnt && defaultTeamId) {
    try {
      const file = path.join(__dirname, '../../assets/worklog/tower-coords.json');
      const parsed = JSON.parse(fs.readFileSync(file, 'utf8'));
      const rows = Array.isArray(parsed) ? parsed : parsed.rows || []; // 文件为 { updatedAt, rows } 结构
      for (let i = 0; i < rows.length; i += 500) {
        const chunk = rows
          .slice(i, i + 500)
          .map((r, j) => [defaultTeamId, String(r[0] || ''), String(r[1] || ''), String(r[2] || ''),
            String(r[3] || ''), String(r[4] || ''), i + j + 1]);
        await pool.query(
          'INSERT INTO worklog_tower (team_id, voltage_level, line_name, tower_no, lng, lat, sort) VALUES ?',
          [chunk]
        );
      }
      console.log(`[初始化] 已迁入杆塔坐标 ${rows.length} 条（默认班组）`);
    } catch (err) {
      console.error('[初始化] 杆塔坐标种子迁移失败（跳过，可后续用 Excel 导入）：', err.message);
    }
  }

  // 老库兼容：worklog_photo 补充 Dify 新增输出列（MySQL 的 ALTER 不支持 IF NOT EXISTS，先查 information_schema）
  const PHOTO_NEW_COLUMNS = [
    ['shot_time', `shot_time VARCHAR(32) NOT NULL DEFAULT '' COMMENT '水印拍摄时间（time）' AFTER work_content`],
    ['weather', `weather VARCHAR(64) NOT NULL DEFAULT '' COMMENT '天气（weather）' AFTER shot_time`],
    ['location', `location VARCHAR(255) NOT NULL DEFAULT '' COMMENT '地点（location）' AFTER weather`],
    ['date_ok', `date_ok TINYINT NULL COMMENT '日期核验：1 相符 0 不符' AFTER lat`],
    ['dest_ok', `dest_ok TINYINT NULL COMMENT '地点核验：1 相符 0 不符' AFTER date_ok`],
  ];
  for (const [col, ddl] of PHOTO_NEW_COLUMNS) {
    const [cols] = await pool.query(
      `SELECT COLUMN_NAME FROM information_schema.COLUMNS
       WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'worklog_photo' AND COLUMN_NAME = ?`,
      [col]
    );
    if (!cols.length) {
      await pool.query(`ALTER TABLE worklog_photo ADD COLUMN ${ddl}`);
      console.log(`[初始化] 已为 worklog_photo 补充 ${col} 列`);
    }
  }

  // 老库兼容：worklog_entry 补充备注列（备注文字 + 附件 JSON，同上方查 information_schema 模式）
  const ENTRY_NEW_COLUMNS = [
    ['remark', `remark TEXT NULL COMMENT '备注（可空）' AFTER patrol_content`],
    ['remark_files', `remark_files JSON NULL COMMENT '备注附件 [{name,url,cos_key,type,size}]（type=image/video/doc）' AFTER remark`],
  ];
  for (const [col, ddl] of ENTRY_NEW_COLUMNS) {
    const [cols] = await pool.query(
      `SELECT COLUMN_NAME FROM information_schema.COLUMNS
       WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'worklog_entry' AND COLUMN_NAME = ?`,
      [col]
    );
    if (!cols.length) {
      await pool.query(`ALTER TABLE worklog_entry ADD COLUMN ${ddl}`);
      console.log(`[初始化] 已为 worklog_entry 补充 ${col} 列`);
    }
  }

  // 写入/更新应用记录（同 Call Me 种子模式；terminal 随种子刷新）
  await pool.query(
    `INSERT INTO sys_app (app_key, name, icon, path, terminal, sort, status) VALUES (?, ?, ?, ?, ?, ?, 1)
     ON DUPLICATE KEY UPDATE name = VALUES(name), icon = VALUES(icon), path = VALUES(path),
       terminal = VALUES(terminal), sort = VALUES(sort)`,
    [APP_WORK_LOG.key, APP_WORK_LOG.name, APP_WORK_LOG.icon, APP_WORK_LOG.path,
      APP_WORK_LOG.terminal, APP_WORK_LOG.sort]
  );

  // 首批成员种子（仅在空表时写入，归属默认班组，管理端后续自行维护）
  const [memberRows] = await pool.query('SELECT COUNT(*) AS cnt FROM worklog_member');
  if (!memberRows[0].cnt && defaultTeamId) {
    for (let i = 0; i < MEMBER_SEED.length; i += 1) {
      await pool.query('INSERT INTO worklog_member (team_id, name, sort) VALUES (?, ?, ?)', [
        defaultTeamId, MEMBER_SEED[i], i + 1,
      ]);
    }
    console.log(`[初始化] 已写入出工日志成员种子 ${MEMBER_SEED.length} 人（默认班组）`);
  }

  // 既有成员按（班组 + 昵称）认领关联账号；再全量对齐：班组内所有账号昵称默认启用为出工成员
  await pool.query(
    `UPDATE worklog_member m JOIN sys_user u ON u.team_id = m.team_id AND u.nickname = m.name AND u.nickname <> ''
     SET m.user_id = u.id WHERE m.user_id IS NULL`
  );
  await require('./member-sync').syncAllUserMembers();

  // 管理员默认授予出工日志权限
  const [adminRows] = await pool.query('SELECT id FROM sys_user WHERE username = ?', [config.admin.username]);
  if (adminRows.length) {
    await pool.query(
      'INSERT IGNORE INTO sys_user_app (user_id, app_id) SELECT ?, id FROM sys_app WHERE app_key = ?',
      [adminRows[0].id, APP_WORK_LOG.key]
    );
  }
}

module.exports = { ensureWorklogSchema };
