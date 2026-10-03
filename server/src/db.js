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
  `CREATE TABLE IF NOT EXISTS sys_team (
    id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
    name VARCHAR(64) NOT NULL UNIQUE COMMENT '班组名称',
    wxid VARCHAR(128) NOT NULL DEFAULT '' COMMENT '微信群 wxid（超管维护，班组群推送用；空=未设置）',
    kvm_group_name VARCHAR(64) NOT NULL DEFAULT '' COMMENT 'GLKVM 平台设备组名，空=与班组同名',
    sort INT NOT NULL DEFAULT 0 COMMENT '排序，小的在前（首个启用班组即默认班组）',
    status TINYINT NOT NULL DEFAULT 1 COMMENT '1 启用 0 停用',
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
  ) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,
  `CREATE TABLE IF NOT EXISTS sys_user (
    id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
    username VARCHAR(64) NOT NULL UNIQUE COMMENT '登录账号',
    password_hash VARCHAR(255) NULL COMMENT '密码 bcrypt 哈希，微信创建的用户可为空',
    nickname VARCHAR(64) NOT NULL DEFAULT '' COMMENT '昵称/姓名',
    avatar VARCHAR(512) NOT NULL DEFAULT '' COMMENT '头像地址',
    openid VARCHAR(64) NULL UNIQUE COMMENT '微信 openid',
    unionid VARCHAR(64) NULL COMMENT '微信 unionid',
    wxid VARCHAR(128) NOT NULL DEFAULT '' COMMENT '微信 wxid（超管维护，消息微信推送用；空=未设置）',
    team VARCHAR(64) NOT NULL DEFAULT '' COMMENT '所属班组（已废弃，由 team_id 取代，仅迁移期保留）',
    team_id BIGINT UNSIGNED NULL COMMENT '所属班组，关联 sys_team.id；NULL=未分配',
    role VARCHAR(16) NOT NULL DEFAULT 'user' COMMENT '角色：admin 超级管理员 / team_admin 班组管理员 / user 普通用户',
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
    terminal VARCHAR(16) NOT NULL DEFAULT 'both' COMMENT '适配终端：both 双端 / mobile 移动端 / pc PC端',
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
// terminal 取值：both 双端 / mobile 仅小程序 / pc 仅网页端（宫格按端过滤展示）
const APP_CALL_ME = {
  key: 'call-me',
  name: 'Call Me',
  icon: 'robot',
  path: '/pkg-callme/pages/sessions/sessions',
  sort: 1,
  terminal: 'both',
};

// 「安全日活动记录」：双端应用（小程序 pkg-safeday + 网页端 safeday.html）
const APP_SAFE_DAY = {
  key: 'safe-day',
  name: '安全日活动记录',
  icon: 'file-safety',
  path: '/pkg-safeday/pages/index/index',
  sort: 3,
  terminal: 'both',
};

// 「远程连接计算机」（原 KVM 远程管理）：GLKVM Cloud 平台对接，PC 端应用（网页端 kvm.html）
const APP_KVM = {
  key: 'kvm',
  name: '远程连接计算机',
  icon: 'terminal',
  path: '',
  sort: 4,
  terminal: 'pc',
};

// 「文件传输」：移动端应用（小程序 pkg-filetransfer），向 KVM 设备虚拟 U 盘推送/取回文件
const APP_FILE_TRANSFER = {
  key: 'file-transfer',
  name: '文件传输',
  icon: 'swap',
  path: '/pkg-filetransfer/pages/index/index',
  sort: 5,
  terminal: 'mobile',
};

// 「水印添加」：移动端应用（小程序 pkg-wmadd），选片/拍摄 → 4:3 裁剪 → 编辑水印 → 服务端渲染仅回图 → 存相册
const APP_WM_ADD = {
  key: 'wm-add',
  name: '水印添加',
  icon: 'image',
  path: '/pkg-wmadd/pages/index/index',
  sort: 6,
  terminal: 'mobile',
};

// 「内网客户端」：仅网页端内部应用（client.html 直贴 CNB 制品库链接取安装包；安装包不再托管本站）
const APP_CLIENT = {
  key: 'client',
  name: '内网客户端',
  icon: 'download',
  path: '',
  sort: 7,
  terminal: 'pc',
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

  // 老库兼容：sys_app 补 terminal 列（适配终端：both 双端 / mobile 移动端 / pc PC端）
  const [termCols] = await pool.query(
    `SELECT COLUMN_NAME FROM information_schema.COLUMNS
     WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'sys_app' AND COLUMN_NAME = 'terminal'`
  );
  if (!termCols.length) {
    await pool.query(
      `ALTER TABLE sys_app ADD COLUMN terminal VARCHAR(16) NOT NULL DEFAULT 'both'
       COMMENT '适配终端：both 双端 / mobile 移动端 / pc PC端' AFTER path`
    );
    console.log('[初始化] 已为 sys_app 补充 terminal 列');
  }

  // ===== 班组（sys_team）与 sys_user.team_id =====
  // 老库兼容：sys_user 补 team_id 列（原 team 文本列废弃，仅迁移期保留）
  const [teamIdCols] = await pool.query(
    `SELECT COLUMN_NAME FROM information_schema.COLUMNS
     WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'sys_user' AND COLUMN_NAME = 'team_id'`
  );
  if (!teamIdCols.length) {
    await pool.query(
      `ALTER TABLE sys_user ADD COLUMN team_id BIGINT UNSIGNED NULL
       COMMENT '所属班组，关联 sys_team.id；NULL=未分配' AFTER team`
    );
    console.log('[初始化] 已为 sys_user 补充 team_id 列');
  }

  // 老库兼容：sys_user / sys_team 补 wxid 列（消息微信推送：用户=个人 wxid，班组=微信群 wxid，超管维护）
  const [userWxidCols] = await pool.query(
    `SELECT COLUMN_NAME FROM information_schema.COLUMNS
     WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'sys_user' AND COLUMN_NAME = 'wxid'`
  );
  if (!userWxidCols.length) {
    await pool.query(
      `ALTER TABLE sys_user ADD COLUMN wxid VARCHAR(128) NOT NULL DEFAULT ''
       COMMENT '微信 wxid（超管维护，消息微信推送用；空=未设置）' AFTER unionid`
    );
    console.log('[初始化] 已为 sys_user 补充 wxid 列');
  }
  const [teamWxidCols] = await pool.query(
    `SELECT COLUMN_NAME FROM information_schema.COLUMNS
     WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'sys_team' AND COLUMN_NAME = 'wxid'`
  );
  if (!teamWxidCols.length) {
    await pool.query(
      `ALTER TABLE sys_team ADD COLUMN wxid VARCHAR(128) NOT NULL DEFAULT ''
       COMMENT '微信群 wxid（超管维护，班组群推送用；空=未设置）' AFTER name`
    );
    console.log('[初始化] 已为 sys_team 补充 wxid 列');
  }

  // 班组种子（仅空表时写入）：检修一班 = 默认班组（首个启用班组，sort 最小）
  const [teamCnt] = await pool.query('SELECT COUNT(*) AS cnt FROM sys_team');
  if (!teamCnt[0].cnt) {
    await pool.query('INSERT INTO sys_team (name, sort) VALUES (?, 1), (?, 2)', ['检修一班', '运维二班']);
    console.log('[初始化] 已写入班组种子：检修一班 / 运维二班');
  }

  // 老库 sys_user.team 文本值去重并入 sys_team（不丢老数据里的班名）
  await pool.query(
    `INSERT IGNORE INTO sys_team (name, sort)
     SELECT DISTINCT u.team, 100 FROM sys_user u
     WHERE u.team <> '' AND u.team NOT IN (SELECT name FROM sys_team)`
  );

  // role 列注释归一（含 team_admin；注释不同才 ALTER，避免每次启动元数据变更）
  const [roleComment] = await pool.query(
    `SELECT COLUMN_COMMENT FROM information_schema.COLUMNS
     WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'sys_user' AND COLUMN_NAME = 'role'`
  );
  if (roleComment.length && !roleComment[0].COLUMN_COMMENT.includes('team_admin')) {
    await pool.query(
      `ALTER TABLE sys_user MODIFY COLUMN role VARCHAR(16) NOT NULL DEFAULT 'user'
       COMMENT '角色：admin 超级管理员 / team_admin 班组管理员 / user 普通用户'`
    );
  }

  // 写入/更新应用记录（terminal 随种子刷新）
  for (const app of [APP_CALL_ME, APP_SAFE_DAY, APP_KVM, APP_FILE_TRANSFER, APP_WM_ADD, APP_CLIENT]) {
    await pool.query(
      `INSERT INTO sys_app (app_key, name, icon, path, terminal, sort, status) VALUES (?, ?, ?, ?, ?, ?, 1)
       ON DUPLICATE KEY UPDATE name = VALUES(name), icon = VALUES(icon), path = VALUES(path),
         terminal = VALUES(terminal), sort = VALUES(sort)`,
      [app.key, app.name, app.icon, app.path, app.terminal, app.sort]
    );
  }

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

  // 按旧 team 文本回填 sys_user.team_id（含管理员账号；空 team 保持 NULL = 未分配班组）
  await pool.query(
    `UPDATE sys_user u JOIN sys_team t ON t.name = u.team
     SET u.team_id = t.id WHERE u.team_id IS NULL AND u.team <> ''`
  );

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

  // 管理员默认授予 文件传输 权限
  await pool.query(
    'INSERT IGNORE INTO sys_user_app (user_id, app_id) SELECT ?, id FROM sys_app WHERE app_key = ?',
    [adminId, APP_FILE_TRANSFER.key]
  );

  // 管理员默认授予 水印添加 权限
  await pool.query(
    'INSERT IGNORE INTO sys_user_app (user_id, app_id) SELECT ?, id FROM sys_app WHERE app_key = ?',
    [adminId, APP_WM_ADD.key]
  );

  // 管理员默认授予 内网客户端 权限
  await pool.query(
    'INSERT IGNORE INTO sys_user_app (user_id, app_id) SELECT ?, id FROM sys_app WHERE app_key = ?',
    [adminId, APP_CLIENT.key]
  );

  // 出工日志：建表并写入应用/成员种子
  await require('./worklog/schema').ensureWorklogSchema(pool);
  console.log('[初始化] 出工日志表结构与应用/成员种子就绪');

  // 安全日活动记录：班组迁移（records.json 回填班组、docs 旧文件迁入班组子目录）
  await require('./safeday/migrate').migrateSafedayTeams();

  // 题库刷题：建表并写入应用种子
  await require('./quiz/schema').ensureQuizSchema(pool);
  console.log('[初始化] 题库刷题表结构与应用种子就绪');

  // 商旅打卡（出工日志扩展）：建表并清理旧 sgcc-clockin 应用
  await require('./sgccclockin/schema').ensureSgccSchema(pool);
  console.log('[初始化] 商旅打卡表结构就绪，旧 sgcc-clockin 应用已清理');

  // 通知推送：本体基础能力，无条件建表
  await require('./notice/schema').ensureNoticeSchema(pool);
}

module.exports = { pool, ensureSchema };
