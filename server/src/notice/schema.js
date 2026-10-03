// 通知推送：表结构初始化（本体基础能力，由 db.js 无条件调用）
const DDL = [
  `CREATE TABLE IF NOT EXISTS sys_notice (
    id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
    title VARCHAR(128) NOT NULL COMMENT '通知标题',
    content TEXT NOT NULL COMMENT '通知内容',
    targets JSON NOT NULL COMMENT '推送对象（角色数组，取值子集 ["admin","team_admin","user"]；空数组=仅按人投放）',
    user_ids JSON NULL COMMENT '指定接收人（sys_user.id 数组）；NULL=不按人投放',
    created_by BIGINT UNSIGNED NULL COMMENT '推送人，关联 sys_user.id；NULL=系统',
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    KEY idx_created (created_at)
  ) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,
  `CREATE TABLE IF NOT EXISTS sys_notice_read (
    id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
    notice_id BIGINT UNSIGNED NOT NULL COMMENT '通知，关联 sys_notice.id',
    user_id BIGINT UNSIGNED NOT NULL COMMENT '已读用户，关联 sys_user.id',
    read_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE KEY uk_notice_user (notice_id, user_id),
    KEY idx_user (user_id)
  ) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,
  `CREATE TABLE IF NOT EXISTS sys_notice_del (
    id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
    notice_id BIGINT UNSIGNED NOT NULL COMMENT '通知，关联 sys_notice.id',
    user_id BIGINT UNSIGNED NOT NULL COMMENT '删除用户，关联 sys_user.id',
    del_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE KEY uk_notice_user (notice_id, user_id),
    KEY idx_user (user_id)
  ) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,
];

async function ensureNoticeSchema(pool) {
  for (const sql of DDL) {
    await pool.query(sql);
  }
  // 老库补列：sys_notice.user_ids（按人投放；缺列才补，与 db.js 老库补列同口径）
  const [cols] = await pool.query(
    `SELECT COLUMN_NAME FROM information_schema.COLUMNS
     WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'sys_notice' AND COLUMN_NAME = 'user_ids'`
  );
  if (!cols.length) {
    await pool.query(
      `ALTER TABLE sys_notice ADD COLUMN user_ids JSON NULL COMMENT '指定接收人（sys_user.id 数组）；NULL=不按人投放' AFTER targets`
    );
  }
}

module.exports = { ensureNoticeSchema };
