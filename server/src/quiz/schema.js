// 题库刷题：表结构初始化与应用种子（仅 QUIZ_ENABLED=true 时由 db.js 调用）
const config = require('../config');

const APP_QUIZ = {
  key: 'quiz',
  name: '题库刷题',
  icon: 'book',
  path: '/pkg-quiz/pages/index/index',
  sort: 7,
  terminal: 'both', // 适配终端：双端（见 db.js sys_app.terminal 口径）
};

const DDL = [
  `CREATE TABLE IF NOT EXISTS quiz_bank (
    id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
    team_id BIGINT UNSIGNED NULL COMMENT '所属班组，关联 sys_team.id；scope=all 时为 NULL',
    name VARCHAR(100) NOT NULL COMMENT '题库名称（班组内唯一）',
    description VARCHAR(255) NOT NULL DEFAULT '' COMMENT '题库简介',
    scope VARCHAR(10) NOT NULL DEFAULT 'team' COMMENT '题库池：team 班组池 / all 全部池',
    status TINYINT NOT NULL DEFAULT 1 COMMENT '1 启用 0 停用',
    created_by BIGINT UNSIGNED NULL COMMENT '创建人，关联 sys_user.id',
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NULL ON UPDATE CURRENT_TIMESTAMP,
    UNIQUE KEY uk_team_name (team_id, name)
  ) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,
  `CREATE TABLE IF NOT EXISTS quiz_question (
    id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
    bank_id BIGINT UNSIGNED NOT NULL COMMENT '所属题库，关联 quiz_bank.id',
    type VARCHAR(10) NOT NULL COMMENT '题型：single 单选 / multiple 多选 / judge 判断',
    content TEXT NOT NULL COMMENT '题干',
    options JSON NOT NULL COMMENT '选项字符串数组；判断题固定 ["正确","错误"]',
    answer VARCHAR(10) NOT NULL COMMENT '答案字母（如 A、AC；判断题 A=正确 B=错误）',
    analysis TEXT NULL COMMENT '答案解析（导入/手填或 AI 生成）',
    analysis_status VARCHAR(10) NOT NULL DEFAULT 'none' COMMENT '解析状态：none 无 / pending 生成中 / done 已生成 / failed 生成失败',
    sort INT NOT NULL DEFAULT 0 COMMENT '行序（导入顺序）',
    status TINYINT NOT NULL DEFAULT 1 COMMENT '1 启用 0 停用',
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    KEY idx_bank (bank_id, status, sort, id)
  ) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,
  `CREATE TABLE IF NOT EXISTS quiz_record (
    id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
    user_id BIGINT UNSIGNED NOT NULL COMMENT '答题人，关联 sys_user.id',
    bank_id BIGINT UNSIGNED NOT NULL COMMENT '所属题库，关联 quiz_bank.id',
    question_id BIGINT UNSIGNED NOT NULL COMMENT '题目，关联 quiz_question.id',
    is_right TINYINT NOT NULL COMMENT '1 答对 0 答错',
    user_answer VARCHAR(10) NOT NULL COMMENT '用户作答（字母，已归一转大写排序去重）',
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    KEY idx_user_bank (user_id, bank_id),
    KEY idx_user_q (user_id, question_id)
  ) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,
  `CREATE TABLE IF NOT EXISTS quiz_wrong (
    id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
    user_id BIGINT UNSIGNED NOT NULL COMMENT '所属用户，关联 sys_user.id',
    bank_id BIGINT UNSIGNED NOT NULL COMMENT '所属题库，关联 quiz_bank.id',
    question_id BIGINT UNSIGNED NOT NULL COMMENT '题目，关联 quiz_question.id',
    wrong_count INT NOT NULL DEFAULT 1 COMMENT '累计答错次数',
    right_streak INT NOT NULL DEFAULT 0 COMMENT '连对次数（达 3 移出错题本）',
    last_wrong_at DATETIME NULL COMMENT '最近答错时间',
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE KEY uk_user_q (user_id, question_id)
  ) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,
  `CREATE TABLE IF NOT EXISTS quiz_user_bank (
    id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
    user_id BIGINT UNSIGNED NOT NULL COMMENT '订阅用户，关联 sys_user.id',
    bank_id BIGINT UNSIGNED NOT NULL COMMENT '题库，关联 quiz_bank.id',
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE KEY uk_user_bank (user_id, bank_id)
  ) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COMMENT='个人题库订阅'`,
  `CREATE TABLE IF NOT EXISTS quiz_favorite (
    id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
    user_id BIGINT UNSIGNED NOT NULL COMMENT '所属用户，关联 sys_user.id',
    bank_id BIGINT UNSIGNED NOT NULL COMMENT '所属题库，关联 quiz_bank.id',
    question_id BIGINT UNSIGNED NOT NULL COMMENT '题目，关联 quiz_question.id',
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE KEY uk_user_q (user_id, question_id)
  ) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COMMENT='题目收藏'`,
];

async function ensureQuizSchema(pool) {
  for (const sql of DDL) {
    await pool.query(sql);
  }

  // 老库兼容：quiz_bank 补 scope 列（双题库池；MySQL 的 ALTER 不支持 IF NOT EXISTS，先查 information_schema）
  const [scopeCols] = await pool.query(
    `SELECT COLUMN_NAME FROM information_schema.COLUMNS
     WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'quiz_bank' AND COLUMN_NAME = 'scope'`
  );
  if (!scopeCols.length) {
    await pool.query(
      `ALTER TABLE quiz_bank ADD COLUMN scope VARCHAR(10) NOT NULL DEFAULT 'team'
       COMMENT '题库池：team 班组池 / all 全部池' AFTER description`
    );
    console.log('[初始化] 已为 quiz_bank 补充 scope 列');
  }

  // 老库兼容：quiz_bank.team_id 放宽为可空（all 池题库不属任何班组；存量行保持原值与 scope='team' 不动）
  const [teamIdCols] = await pool.query(
    `SELECT IS_NULLABLE FROM information_schema.COLUMNS
     WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'quiz_bank' AND COLUMN_NAME = 'team_id'`
  );
  if (teamIdCols.length && teamIdCols[0].IS_NULLABLE === 'NO') {
    await pool.query(
      `ALTER TABLE quiz_bank MODIFY COLUMN team_id BIGINT UNSIGNED NULL
       COMMENT '所属班组，关联 sys_team.id；scope=all 时为 NULL'`
    );
    console.log('[初始化] 已将 quiz_bank.team_id 放宽为可空');
  }

  // 启动自愈：进程重启会把「生成中」的回写弄丢，复位为 none 防止永远卡 pending（可经 analyze-retry 重新入队）
  await pool.query("UPDATE quiz_question SET analysis_status = 'none' WHERE analysis_status = 'pending'");

  // 写入/更新应用记录（同出工日志种子模式；terminal 随种子刷新）
  await pool.query(
    `INSERT INTO sys_app (app_key, name, icon, path, terminal, sort, status) VALUES (?, ?, ?, ?, ?, ?, 1)
     ON DUPLICATE KEY UPDATE name = VALUES(name), icon = VALUES(icon), path = VALUES(path),
       terminal = VALUES(terminal), sort = VALUES(sort)`,
    [APP_QUIZ.key, APP_QUIZ.name, APP_QUIZ.icon, APP_QUIZ.path, APP_QUIZ.terminal, APP_QUIZ.sort]
  );

  // 管理员默认授予题库刷题权限
  const [adminRows] = await pool.query('SELECT id FROM sys_user WHERE username = ?', [config.admin.username]);
  if (adminRows.length) {
    await pool.query(
      'INSERT IGNORE INTO sys_user_app (user_id, app_id) SELECT ?, id FROM sys_app WHERE app_key = ?',
      [adminRows[0].id, APP_QUIZ.key]
    );
  }
}

module.exports = { ensureQuizSchema, APP_QUIZ };
