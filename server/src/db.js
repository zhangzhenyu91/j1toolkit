// MySQL 连接池与表结构初始化：首次启动自动建表并写入初始数据（应用记录、管理员）
const mysql = require('mysql2/promise');
const bcrypt = require('bcryptjs');
const config = require('./config');

const pool = mysql.createPool({
  host: config.mysql.host,
  port: config.mysql.port,
  user: config.mysql.user,
  password: config.mysql.password,
  database: config.mysql.database,
  waitForConnections: true,
  connectionLimit: 10,
  charset: 'utf8mb4',
});

// 表结构（对应《开发指南》第三章）
const DDL = [
  `CREATE TABLE IF NOT EXISTS sys_user (
    id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
    username VARCHAR(64) NOT NULL UNIQUE COMMENT '登录账号',
    password_hash VARCHAR(255) NULL COMMENT '密码 bcrypt 哈希，微信创建的用户可为空',
    nickname VARCHAR(64) NOT NULL DEFAULT '' COMMENT '昵称/姓名',
    avatar VARCHAR(512) NOT NULL DEFAULT '' COMMENT '头像地址',
    openid VARCHAR(64) NULL UNIQUE COMMENT '微信 openid',
    unionid VARCHAR(64) NULL COMMENT '微信 unionid',
    team VARCHAR(64) NOT NULL DEFAULT '' COMMENT '所属班组',
    role VARCHAR(16) NOT NULL DEFAULT 'user' COMMENT '角色：admin 管理员 / user 普通用户',
    status TINYINT NOT NULL DEFAULT 1 COMMENT '1 正常 0 禁用',
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP
  ) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,
  `CREATE TABLE IF NOT EXISTS sys_app (
    id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
    app_key VARCHAR(64) NOT NULL UNIQUE COMMENT '应用唯一标识',
    name VARCHAR(64) NOT NULL COMMENT '应用名称',
    icon VARCHAR(64) NOT NULL DEFAULT 'app' COMMENT 'TDesign 图标名',
    path VARCHAR(255) NOT NULL DEFAULT '' COMMENT '小程序页面路径',
    sort INT NOT NULL DEFAULT 0 COMMENT '宫格排序，小的在前',
    status TINYINT NOT NULL DEFAULT 1 COMMENT '1 上架 0 下架',
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
  ) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,
  `CREATE TABLE IF NOT EXISTS sys_user_app (
    id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
    user_id BIGINT UNSIGNED NOT NULL,
    app_id BIGINT UNSIGNED NOT NULL,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP COMMENT '授权时间',
    UNIQUE KEY uk_user_app (user_id, app_id)
  ) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,
];

// 首个接入的应用：「Call Me」AI 知识库
const APP_CALL_ME = {
  key: 'call-me',
  name: 'Call Me',
  icon: 'robot',
  path: '/pkg-callme/pages/sessions/sessions',
  sort: 1,
};

// 「安全日活动记录」：网页端应用，小程序无页面且宫格入口已隐藏（仅隐藏入口，不影响权限授予与网页端使用）
const APP_SAFE_DAY = {
  key: 'safe-day',
  name: '安全日活动记录',
  icon: 'file-safety',
  path: '',
  sort: 3,
};

// 「KVM 远程管理」：GLKVM Cloud 平台对接；小程序分包页仅展示设备状态（无终端/远程控制入口），操作端在网页端 kvm.html
const APP_KVM = {
  key: 'kvm',
  name: 'KVM 远程管理',
  icon: 'terminal',
  path: '/pkg-kvm/pages/index/index',
  sort: 4,
};

function sleep(ms) {
  return new Promise((resolve) => setTimeout(resolve, ms));
}

// Docker 启动时 MySQL 可能尚未就绪，重试等待
async function waitForDatabase(retries = 10, intervalMs = 3000) {
  let lastErr;
  for (let i = 1; i <= retries; i += 1) {
    try {
      await pool.query('SELECT 1');
      return;
    } catch (err) {
      lastErr = err;
      console.log(`[初始化] 等待数据库就绪（${i}/${retries}）：${err.message}`);
      await sleep(intervalMs);
    }
  }
  throw lastErr;
}

async function ensureSchema() {
  await waitForDatabase();
  for (const sql of DDL) {
    await pool.query(sql);
  }

  // 老库兼容：sys_user 补 role 列（MySQL 的 ALTER 不支持 IF NOT EXISTS，先查 information_schema）
  const [roleCols] = await pool.query(
    `SELECT COLUMN_NAME FROM information_schema.COLUMNS
     WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'sys_user' AND COLUMN_NAME = 'role'`
  );
  if (!roleCols.length) {
    await pool.query(
      `ALTER TABLE sys_user ADD COLUMN role VARCHAR(16) NOT NULL DEFAULT 'user'
       COMMENT '角色：admin 管理员 / user 普通用户' AFTER team`
    );
    console.log('[初始化] 已为 sys_user 补充 role 列');
  }

  // 写入/更新 Call Me 应用记录
  await pool.query(
    `INSERT INTO sys_app (app_key, name, icon, path, sort, status) VALUES (?, ?, ?, ?, ?, 1)
     ON DUPLICATE KEY UPDATE name = VALUES(name), icon = VALUES(icon), path = VALUES(path), sort = VALUES(sort)`,
    [APP_CALL_ME.key, APP_CALL_ME.name, APP_CALL_ME.icon, APP_CALL_ME.path, APP_CALL_ME.sort]
  );

  // 写入/更新 安全日活动记录 应用记录
  await pool.query(
    `INSERT INTO sys_app (app_key, name, icon, path, sort, status) VALUES (?, ?, ?, ?, ?, 1)
     ON DUPLICATE KEY UPDATE name = VALUES(name), icon = VALUES(icon), path = VALUES(path), sort = VALUES(sort)`,
    [APP_SAFE_DAY.key, APP_SAFE_DAY.name, APP_SAFE_DAY.icon, APP_SAFE_DAY.path, APP_SAFE_DAY.sort]
  );

  // 写入/更新 KVM 远程管理 应用记录
  await pool.query(
    `INSERT INTO sys_app (app_key, name, icon, path, sort, status) VALUES (?, ?, ?, ?, ?, 1)
     ON DUPLICATE KEY UPDATE name = VALUES(name), icon = VALUES(icon), path = VALUES(path), sort = VALUES(sort)`,
    [APP_KVM.key, APP_KVM.name, APP_KVM.icon, APP_KVM.path, APP_KVM.sort]
  );

  // 初始管理员（仅当账号不存在时创建，密码 bcrypt 存储）
  const [rows] = await pool.query('SELECT id FROM sys_user WHERE username = ?', [config.admin.username]);
  let adminId = rows[0] && rows[0].id;
  if (!adminId) {
    const hash = await bcrypt.hash(config.admin.password, 10);
    const [r] = await pool.query(
      'INSERT INTO sys_user (username, password_hash, nickname, team, role) VALUES (?, ?, ?, ?, ?)',
      [config.admin.username, hash, config.admin.nickname, '检修一班', 'admin']
    );
    adminId = r.insertId;
    console.log(`[初始化] 已创建管理员账号 ${config.admin.username}（密码取自环境变量 ADMIN_PASSWORD）`);
  }

  // 保证环境变量指定的管理员始终具备 admin 角色（防止误改）
  await pool.query('UPDATE sys_user SET role = ? WHERE username = ?', ['admin', config.admin.username]);

  // 管理员默认授予 Call Me 权限
  await pool.query(
    'INSERT IGNORE INTO sys_user_app (user_id, app_id) SELECT ?, id FROM sys_app WHERE app_key = ?',
    [adminId, APP_CALL_ME.key]
  );

  // 管理员默认授予 安全日活动记录 权限
  await pool.query(
    'INSERT IGNORE INTO sys_user_app (user_id, app_id) SELECT ?, id FROM sys_app WHERE app_key = ?',
    [adminId, APP_SAFE_DAY.key]
  );

  // 管理员默认授予 KVM 远程管理 权限
  await pool.query(
    'INSERT IGNORE INTO sys_user_app (user_id, app_id) SELECT ?, id FROM sys_app WHERE app_key = ?',
    [adminId, APP_KVM.key]
  );

  // 出工日志：WORKLOG_ENABLED=true 时建表并写入应用/成员种子
  if (config.worklog.enabled) {
    await require('./worklog/schema').ensureWorklogSchema(pool);
    console.log('[初始化] 出工日志已开启（WORKLOG_ENABLED=true），表结构与应用/成员种子就绪');
  }
}

module.exports = { pool, ensureSchema };
