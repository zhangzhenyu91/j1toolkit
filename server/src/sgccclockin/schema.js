// 商旅打卡：表结构初始化（仅 SGCC_CLOCKIN_ENABLED=true 时由 db.js 调用）
// 本模块是出工日志的扩展能力：打卡/费用/照片同步均以 worklog_member 为口径双写本地表，
// 应用权限归属出工日志（sys_app 统一走 work-log，不再单设 sgcc-clockin 应用）

const DDL = [
  // 商旅账号绑定：一人一号（user_id 本人绑定；member_id 关联出工成员）
  `CREATE TABLE IF NOT EXISTS worklog_sgcc_account (
    id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
    team_id BIGINT UNSIGNED NULL COMMENT '所属班组，关联 sys_team.id',
    user_id BIGINT UNSIGNED NOT NULL COMMENT '绑定人，关联 sys_user.id',
    member_id BIGINT UNSIGNED NULL COMMENT '关联出工成员 worklog_member.id（按班组+昵称认领）',
    mobile VARCHAR(32) NOT NULL DEFAULT '' COMMENT '商旅注册手机号（脱敏展示用原文）',
    token VARCHAR(128) NOT NULL DEFAULT '' COMMENT '商旅登录 token（短信登录换取，长效）',
    device_type VARCHAR(64) NOT NULL DEFAULT '' COMMENT '打卡设备型号「厂商 型号」（打卡记录展示，本人可改）',
    system_version VARCHAR(64) NOT NULL DEFAULT '' COMMENT '系统版本（同上）',
    token_status TINYINT NOT NULL DEFAULT 1 COMMENT '登录态：1 有效 0 已失效（打卡/上传/核查时探测标记）',
    last_check_at DATETIME NULL COMMENT '最近一次登录态探测时间',
    backfill_done TINYINT NOT NULL DEFAULT 0 COMMENT '（已废弃）原 8 月回填标记，列保留兼容老库，代码已不再读写',
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    UNIQUE KEY uk_team_user (team_id, user_id),
    UNIQUE KEY uk_team_member (team_id, member_id)
  ) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,
  // 打卡流水：每人每日两条（开始/结束），与商旅逐条对账
  `CREATE TABLE IF NOT EXISTS worklog_clockin (
    id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
    team_id BIGINT UNSIGNED NULL COMMENT '所属班组，关联 sys_team.id',
    member_id BIGINT UNSIGNED NOT NULL COMMENT '关联 worklog_member.id',
    clock_date DATE NOT NULL COMMENT '打卡日期',
    seq TINYINT NOT NULL COMMENT '1 开始打卡 2 结束打卡',
    detail_id VARCHAR(64) NOT NULL DEFAULT '' COMMENT '商旅打卡明细 id（updateMark 用）',
    clock_time DATETIME NULL COMMENT '打卡时间（商旅口径）',
    position VARCHAR(255) NOT NULL DEFAULT '' COMMENT '完整地址串（高德逆编码路/街道级）',
    longitude VARCHAR(32) NOT NULL DEFAULT '' COMMENT '经度',
    latitude VARCHAR(32) NOT NULL DEFAULT '' COMMENT '纬度',
    city_code VARCHAR(32) NOT NULL DEFAULT '' COMMENT '城市行政区编码（高德 adcode；带入时随地址串一并带入）',
    city_name VARCHAR(64) NOT NULL DEFAULT '' COMMENT '城市名（同上）',
    work_hours VARCHAR(16) NOT NULL DEFAULT '' COMMENT '工时（商旅服务端按两次打卡计算）',
    source TINYINT NOT NULL DEFAULT 0 COMMENT '来源：0 壹匣打卡 1 商旅同步（核查拉取）',
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    UNIQUE KEY uk_member_date_seq (member_id, clock_date, seq),
    KEY idx_team_date (team_id, clock_date)
  ) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,
  // 费用：每人每日一条（本地优先读取；核查时与商旅对账）
  `CREATE TABLE IF NOT EXISTS worklog_fee (
    id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
    team_id BIGINT UNSIGNED NULL COMMENT '所属班组，关联 sys_team.id',
    member_id BIGINT UNSIGNED NOT NULL COMMENT '关联 worklog_member.id',
    fee_date DATE NOT NULL COMMENT '费用日期',
    food_fee DECIMAL(8,2) NOT NULL DEFAULT 0 COMMENT '伙食补助',
    transit_fee DECIMAL(8,2) NOT NULL DEFAULT 0 COMMENT '交通费',
    cost_center_code VARCHAR(64) NOT NULL DEFAULT '' COMMENT '成本中心编码',
    cost_center_name VARCHAR(128) NOT NULL DEFAULT '' COMMENT '成本中心名称',
    extra JSON NULL COMMENT '其余费用组件快照（公司垫付/员工垫付/费用照片 id 列表等）',
    synced_at DATETIME NULL COMMENT '最近一次与商旅对账时间',
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    UNIQUE KEY uk_member_date (member_id, fee_date),
    KEY idx_team_date (team_id, fee_date)
  ) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,
  // 核查记录：每晚定时核查 + 手动从商旅拉取的流水
  `CREATE TABLE IF NOT EXISTS worklog_sync_log (
    id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
    team_id BIGINT UNSIGNED NULL COMMENT '所属班组，关联 sys_team.id',
    member_id BIGINT UNSIGNED NULL COMMENT '关联 worklog_member.id（整班核查为 NULL）',
    sync_date DATE NOT NULL COMMENT '核查的数据日期',
    scope VARCHAR(16) NOT NULL DEFAULT 'daily' COMMENT 'daily 当日核查（历史值 backfill 为已删除的绑定回填）',
    type VARCHAR(16) NOT NULL COMMENT 'clockin 打卡 / photo 照片 / fee 费用 / auth 登录态',
    result VARCHAR(8) NOT NULL COMMENT 'ok 一致 / diff 有差异已回写 / fail 失败',
    detail VARCHAR(512) NOT NULL DEFAULT '' COMMENT '明细（如 商旅侧新发现照片 1 张）',
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    KEY idx_team_date (team_id, sync_date)
  ) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,
];

async function ensureSgccSchema(pool) {
  for (const sql of DDL) {
    await pool.query(sql);
  }

  // worklog_photo 补充商旅同步列（老库兼容：先查 information_schema 再 ALTER）
  const PHOTO_SGCC_COLUMNS = [
    ['is_watermark', `is_watermark TINYINT NOT NULL DEFAULT 1 COMMENT '1 水印照片（参与验证） 0 非水印照片（仅存档同步，免验证）' AFTER verify_status`],
    ['source', `source TINYINT NOT NULL DEFAULT 0 COMMENT '来源：0 壹匣上传 1 商旅同步（核查发现）' AFTER is_watermark`],
    ['sgcc_img_id', `sgcc_img_id VARCHAR(512) NOT NULL DEFAULT '' COMMENT '商旅费用照片关联（JSON：{worklog_member_id: 商旅图片id}）' AFTER source`],
    ['sgcc_synced', `sgcc_synced TINYINT NOT NULL DEFAULT 0 COMMENT '商旅费用照片同步：0 未同步 1 已同步 2 同步失败' AFTER sgcc_img_id`],
    ['md5', `md5 VARCHAR(32) NOT NULL DEFAULT '' COMMENT '图片内容 MD5（商旅拉取按内容合并相同照片：一图多人标注）' AFTER sgcc_synced`],
  ];
  for (const [col, ddl] of PHOTO_SGCC_COLUMNS) {
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

  // 一次性修正：早期商旅拉下（source=1）的照片误落 sgcc_synced=0（卡片/灯箱误显「未同步」），
  // 拉下的照片本就在商旅侧，统一置 1 已同步；修正过的行不再满足条件，天然幂等
  const [fixed] = await pool.query('UPDATE worklog_photo SET sgcc_synced = 1 WHERE source = 1 AND sgcc_synced = 0');
  if (fixed.affectedRows) {
    console.log(`[初始化] 已修正 ${fixed.affectedRows} 张商旅拉下照片的同步标记（sgcc_synced 0→1）`);
  }

  // worklog_clockin 补充城市信息列（老库兼容：先查 information_schema 再 ALTER；打卡落库时写入，带入时随地址串一并带入）
  const CLOCKIN_CITY_COLUMNS = [
    ['city_code', `city_code VARCHAR(32) NOT NULL DEFAULT '' COMMENT '城市行政区编码（高德 adcode）' AFTER latitude`],
    ['city_name', `city_name VARCHAR(64) NOT NULL DEFAULT '' COMMENT '城市名' AFTER city_code`],
  ];
  for (const [col, ddl] of CLOCKIN_CITY_COLUMNS) {
    const [cols] = await pool.query(
      `SELECT COLUMN_NAME FROM information_schema.COLUMNS
       WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'worklog_clockin' AND COLUMN_NAME = ?`,
      [col]
    );
    if (!cols.length) {
      await pool.query(`ALTER TABLE worklog_clockin ADD COLUMN ${ddl}`);
      console.log(`[初始化] 已为 worklog_clockin 补充 ${col} 列`);
    }
  }

  // 一次性清理：商旅打卡归属出工日志（权限统一走 work-log），删除旧 sgcc-clockin 应用及授权
  // 先删子表 sys_user_app 再删 sys_app；删过后查不到行，天然幂等
  const [appRows] = await pool.query('SELECT id FROM sys_app WHERE app_key = ?', ['sgcc-clockin']);
  if (appRows.length) {
    await pool.query('DELETE FROM sys_user_app WHERE app_id = ?', [appRows[0].id]);
    await pool.query('DELETE FROM sys_app WHERE id = ?', [appRows[0].id]);
    console.log('[初始化] 已清理 sgcc-clockin 应用及授权（商旅打卡归属出工日志，权限走 work-log）');
  }
}

module.exports = { ensureSgccSchema };
