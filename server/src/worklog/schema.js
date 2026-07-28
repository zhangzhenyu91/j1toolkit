// 出工日志：表结构初始化与应用/成员种子（仅 WORKLOG_ENABLED=true 时由 db.js 调用）
// 表结构对应《开发指南》3.4；一条日志卡片 = 一次派车，未出车即 vehicle_id 为 NULL
const config = require('../config');

const APP_WORK_LOG = {
  key: 'work-log',
  name: '出工日志',
  icon: 'calendar',
  path: '/pkg-worklog/pages/index/index',
  sort: 2,
};

// 首批出工成员种子（sort 即点亮按钮顺序）
const MEMBER_SEED = ['赵登', '郑海楠', '任舒诺', '薛忠亮', '曹万鑫', '张振宇', '高麒涵'];

const DDL = [
  `CREATE TABLE IF NOT EXISTS worklog_entry (
    id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
    log_date DATE NOT NULL COMMENT '日志日期',
    patrol_content TEXT NULL COMMENT '巡视内容（可空）',
    vehicle_id BIGINT UNSIGNED NULL COMMENT '车牌，关联 worklog_vehicle.id；NULL=未出车',
    destination_id BIGINT UNSIGNED NULL COMMENT '目的地，关联 worklog_destination.id',
    created_by BIGINT UNSIGNED NOT NULL COMMENT '创建人，关联 sys_user.id',
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    KEY idx_log_date (log_date)
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
    verify_status VARCHAR(16) NOT NULL DEFAULT 'pending' COMMENT 'pending/passed/date_mismatch/dest_mismatch/failed',
    work_content VARCHAR(512) NOT NULL DEFAULT '' COMMENT 'Dify 返回施工内容（title）',
    lng VARCHAR(32) NOT NULL DEFAULT '' COMMENT '经度',
    lat VARCHAR(32) NOT NULL DEFAULT '' COMMENT '纬度',
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    KEY idx_entry (entry_id)
  ) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,
  `CREATE TABLE IF NOT EXISTS worklog_vehicle (
    id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
    plate_no VARCHAR(32) NOT NULL UNIQUE COMMENT '车牌号',
    sort INT NOT NULL DEFAULT 0 COMMENT '下拉排序',
    status TINYINT NOT NULL DEFAULT 1 COMMENT '1 启用 0 停用',
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
  ) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,
  `CREATE TABLE IF NOT EXISTS worklog_destination (
    id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
    name VARCHAR(64) NOT NULL UNIQUE COMMENT '目的地名称',
    sort INT NOT NULL DEFAULT 0 COMMENT '下拉排序',
    status TINYINT NOT NULL DEFAULT 1 COMMENT '1 启用 0 停用',
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
  ) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,
  `CREATE TABLE IF NOT EXISTS worklog_member (
    id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
    name VARCHAR(64) NOT NULL UNIQUE COMMENT '成员姓名',
    sort INT NOT NULL DEFAULT 0 COMMENT '点亮按钮排列顺序',
    status TINYINT NOT NULL DEFAULT 1 COMMENT '1 启用 0 停用',
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
  ) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,
];

async function ensureWorklogSchema(pool) {
  for (const sql of DDL) {
    await pool.query(sql);
  }

  // 写入/更新应用记录（同 Call Me 种子模式）
  await pool.query(
    `INSERT INTO sys_app (app_key, name, icon, path, sort, status) VALUES (?, ?, ?, ?, ?, 1)
     ON DUPLICATE KEY UPDATE name = VALUES(name), icon = VALUES(icon), path = VALUES(path), sort = VALUES(sort)`,
    [APP_WORK_LOG.key, APP_WORK_LOG.name, APP_WORK_LOG.icon, APP_WORK_LOG.path, APP_WORK_LOG.sort]
  );

  // 首批成员种子（仅在空表时写入，管理端后续自行维护）
  const [memberRows] = await pool.query('SELECT COUNT(*) AS cnt FROM worklog_member');
  if (!memberRows[0].cnt) {
    for (let i = 0; i < MEMBER_SEED.length; i += 1) {
      await pool.query('INSERT INTO worklog_member (name, sort) VALUES (?, ?)', [MEMBER_SEED[i], i + 1]);
    }
    console.log(`[初始化] 已写入出工日志成员种子 ${MEMBER_SEED.length} 人`);
  }

  // 管理员默认授予出工日志权限
  const [adminRows] = await pool.query('SELECT id FROM sys_user WHERE username = ?', [config.admin.username]);
  if (adminRows.length) {
    await pool.query(
      'INSERT IGNORE INTO sys_user_app (user_id, app_id) SELECT ?, id FROM sys_app WHERE app_key = ?',
      [adminRows[0].id, APP_WORK_LOG.key]
    );
  }
}

module.exports = { ensureWorklogSchema, APP_WORK_LOG };
