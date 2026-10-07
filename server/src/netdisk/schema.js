// 团队网盘：表结构初始化与应用种子（启动时由 db.js 调用）
const config = require('../config');

const APP_NETDISK = {
  key: 'netdisk',
  name: '团队网盘',
  icon: 'folder-open',
  path: '/pkg-netdisk/pages/index/index',
  sort: 8,
  terminal: 'both', // 适配终端：双端（见 db.js sys_app.terminal 口径）
};

const DDL = [
  // 分享链接：OpenList 侧不建分享，提取码/有效期/停用全部由本表承担（班管/超管可管全量）
  `CREATE TABLE IF NOT EXISTS nd_share (
    id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
    share_id VARCHAR(32) NOT NULL COMMENT '分享标识（外链 /share.html#/s/<share_id>）',
    user_id BIGINT UNSIGNED NOT NULL COMMENT '创建人，关联 sys_user.id',
    creator VARCHAR(64) NOT NULL DEFAULT '' COMMENT '创建人昵称（冗余，免联表）',
    base_path VARCHAR(500) NOT NULL COMMENT '空间根（OpenList 绝对路径，越界校验基准）',
    paths JSON NOT NULL COMMENT '分享项（相对 base_path 的路径数组）',
    password VARCHAR(16) NOT NULL COMMENT '提取码',
    expire_at DATETIME NULL COMMENT '过期时间，NULL=永久',
    status TINYINT NOT NULL DEFAULT 1 COMMENT '1 生效 0 停用',
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE KEY uk_share_id (share_id),
    KEY idx_user (user_id, status),
    KEY idx_status (status, expire_at)
  ) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COMMENT='团队网盘分享链接'`,
];

async function ensureNetdiskSchema(pool) {
  for (const sql of DDL) {
    await pool.query(sql);
  }

  // 写入/更新应用记录（同题库种子模式；terminal 随种子刷新）
  await pool.query(
    `INSERT INTO sys_app (app_key, name, icon, path, terminal, sort, status) VALUES (?, ?, ?, ?, ?, ?, 1)
     ON DUPLICATE KEY UPDATE name = VALUES(name), icon = VALUES(icon), path = VALUES(path),
       terminal = VALUES(terminal), sort = VALUES(sort)`,
    [APP_NETDISK.key, APP_NETDISK.name, APP_NETDISK.icon, APP_NETDISK.path, APP_NETDISK.terminal, APP_NETDISK.sort]
  );

  // 管理员默认授予团队网盘权限
  const [adminRows] = await pool.query('SELECT id FROM sys_user WHERE username = ?', [config.admin.username]);
  if (adminRows.length) {
    await pool.query(
      'INSERT IGNORE INTO sys_user_app (user_id, app_id) SELECT ?, id FROM sys_app WHERE app_key = ?',
      [adminRows[0].id, APP_NETDISK.key]
    );
  }
}

module.exports = { ensureNetdiskSchema };
