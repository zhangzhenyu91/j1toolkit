// 建表语句（幂等）。本模块只存「监听群/规则配置 + 消息管线技术状态」：
// 业务记录本体由 CaptureSink 交 tree/crossing 模块入库，不在此复刻 crane JSON 业务台账。
// EnsureSchema 兼容旧结构：既有表缺列时按 information_schema 判定补列，存量数据保留。
package wechatanswer

import "context"

// schemaDDL 建表语句（CREATE TABLE IF NOT EXISTS，幂等）。
var schemaDDL = []string{
	// wa_group 监听群（CRUD 主体；team_id 归属班组，last_message_at 最近消息时间）
	`CREATE TABLE IF NOT EXISTS wa_group (
    id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
    group_id VARCHAR(128) NOT NULL COMMENT '微信群 ID（*@chatroom）',
    group_name VARCHAR(64) NOT NULL DEFAULT '' COMMENT '群备注',
    enabled TINYINT NOT NULL DEFAULT 1 COMMENT '1 启用 0 停用',
    team_id BIGINT NOT NULL DEFAULT 0 COMMENT '归属班组 sys_team.id',
    last_message_at BIGINT NOT NULL DEFAULT 0 COMMENT '最近入站消息时间（Unix 秒）',
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    UNIQUE KEY uk_group (group_id)
  ) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COMMENT='微信群智能应答-监听群'`,
	// wa_rule 监听规则：每群每类（location/treeWatch）最多一条，密钥独立于全局 WeChat/Dify 配置。
	// 密钥列沿用早期命名（extract_key=tree_extract、correct_key=tree_correct、crossing_key=…
	// crossing_extract、crossing_correct_key=…crossing_correct），新增 ask/chat_digest/location 三键，
	// 老库不改列名以保留存量密钥（对外契约键名见 routes.go keys_configured）。
	`CREATE TABLE IF NOT EXISTS wa_rule (
    id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
    group_id VARCHAR(128) NOT NULL COMMENT '关联 wa_group.group_id',
    trigger_type VARCHAR(16) NOT NULL COMMENT 'location | treeWatch',
    name VARCHAR(64) NOT NULL DEFAULT '' COMMENT '规则名称',
    enabled TINYINT NOT NULL DEFAULT 1 COMMENT '1 启用 0 停用',
    ask_enabled TINYINT NOT NULL DEFAULT 0 COMMENT 'treeWatch 群内提问开关',
    extract_key VARCHAR(200) NOT NULL DEFAULT '' COMMENT 'WF1 树障提取 Dify API Key',
    correct_key VARCHAR(200) NOT NULL DEFAULT '' COMMENT 'WF2 树障更正 Dify API Key',
    crossing_key VARCHAR(200) NOT NULL DEFAULT '' COMMENT 'WF1c 交跨提取 Dify API Key（空=不启用交跨）',
    crossing_correct_key VARCHAR(200) NOT NULL DEFAULT '' COMMENT 'WF2c 交跨更正 Dify API Key',
    ask_name_key VARCHAR(200) NOT NULL DEFAULT '' COMMENT 'WF3 回答文件命名 Dify API Key',
    chat_digest_key VARCHAR(200) NOT NULL DEFAULT '' COMMENT 'WF4 聊天总结 Dify API Key',
    location_key VARCHAR(200) NOT NULL DEFAULT '' COMMENT '位置处理 Dify Workflow Key',
    team_name VARCHAR(32) NOT NULL DEFAULT '' COMMENT '归属班组（legacy：优先 wa_group.team_id）',
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    UNIQUE KEY uk_group_trigger (group_id, trigger_type)
  ) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COMMENT='微信群智能应答-监听规则'`,
	// wa_record 消息管线技术状态（更正/撤回/辅证定位与补推幂等用；非业务台账）
	`CREATE TABLE IF NOT EXISTS wa_record (
    id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
    record_key VARCHAR(64) NOT NULL COMMENT '技术主键（载荷 crane_record_id，tree/crossing 幂等键）',
    group_id VARCHAR(128) NOT NULL COMMENT '群 ID',
    kind VARCHAR(16) NOT NULL COMMENT 'tree | crossing',
    msg_id VARCHAR(64) NOT NULL DEFAULT '' COMMENT '来源消息 svr_id',
    sender_id VARCHAR(64) NOT NULL DEFAULT '',
    sender_name VARCHAR(64) NOT NULL DEFAULT '',
    co_sender_id VARCHAR(64) NOT NULL DEFAULT '' COMMENT '协作供图人 wxid',
    co_sender_name VARCHAR(64) NOT NULL DEFAULT '',
    content TEXT NULL COMMENT '报送原文',
    captured_at BIGINT NOT NULL COMMENT '报送时间（Unix 秒）',
    status VARCHAR(16) NOT NULL DEFAULT 'pending' COMMENT 'pending | active | dropped',
    extracted JSON NULL COMMENT 'Dify 提取结果（更正 current_record）',
    line_name_raw VARCHAR(64) NOT NULL DEFAULT '',
    handled_note VARCHAR(255) NOT NULL DEFAULT '' COMMENT '状态批注（action=status）',
    push_status VARCHAR(16) NOT NULL DEFAULT '' COMMENT '空 | ok | failed',
    push_error VARCHAR(500) NOT NULL DEFAULT '',
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    UNIQUE KEY uk_record_key (record_key),
    KEY idx_group_captured (group_id, captured_at),
    KEY idx_group_msg (group_id, msg_id),
    KEY idx_sender_kind (sender_id, kind, captured_at)
  ) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COMMENT='微信群智能应答-管线记录状态'`,
	// wa_photo 辅证照片（树障记录；文件落盘 ServerDir/data/wechatanswer/attachments/）
	`CREATE TABLE IF NOT EXISTS wa_photo (
    id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
    record_key VARCHAR(64) NOT NULL COMMENT '关联 wa_record.record_key',
    msg_id VARCHAR(64) NOT NULL DEFAULT '' COMMENT '图片消息 svr_id',
    sender_id VARCHAR(64) NOT NULL DEFAULT '' COMMENT '供图人',
    captured_at BIGINT NOT NULL DEFAULT 0,
    file_path VARCHAR(255) NOT NULL DEFAULT '' COMMENT '相对 ServerDir 的文件路径',
    bytes INT NOT NULL DEFAULT 0,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    KEY idx_record (record_key),
    KEY idx_msg (msg_id)
  ) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COMMENT='微信群智能应答-辅证照片'`,
	// wa_image 图片媒体缓存（CDN 下载参数备查；裸图待引用挂接）
	`CREATE TABLE IF NOT EXISTS wa_image (
    svr_id VARCHAR(64) NOT NULL PRIMARY KEY COMMENT '图片消息 svr_id',
    group_id VARCHAR(128) NOT NULL DEFAULT '',
    sender_id VARCHAR(64) NOT NULL DEFAULT '',
    captured_at BIGINT NOT NULL DEFAULT 0,
    file_no VARCHAR(128) NOT NULL DEFAULT '' COMMENT 'CDN file_no',
    aes_key VARCHAR(128) NOT NULL DEFAULT '' COMMENT 'CDN file_aes_key',
    data_len BIGINT NOT NULL DEFAULT 0,
    file_path VARCHAR(255) NOT NULL DEFAULT '' COMMENT '已下载本体相对路径（空=未下载）',
    record_key VARCHAR(64) NOT NULL DEFAULT '' COMMENT '已挂接记录（空=未挂接，参与过期清理）',
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    KEY idx_group_sender (group_id, sender_id, captured_at),
    KEY idx_record (record_key)
  ) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COMMENT='微信群智能应答-图片缓存'`,
	// wa_media 文件/视频/语音下载参数缓存（3 天过期；只存参数不存二进制）
	`CREATE TABLE IF NOT EXISTS wa_media (
    svr_id VARCHAR(64) NOT NULL PRIMARY KEY COMMENT '媒体消息 svr_id/new_msg_id',
    group_id VARCHAR(128) NOT NULL DEFAULT '',
    sender_id VARCHAR(64) NOT NULL DEFAULT '',
    captured_at BIGINT NOT NULL DEFAULT 0,
    kind VARCHAR(16) NOT NULL DEFAULT '' COMMENT 'file | video | voice',
    file_name VARCHAR(255) NOT NULL DEFAULT '',
    app_id VARCHAR(64) NOT NULL DEFAULT '',
    attach_id VARCHAR(128) NOT NULL DEFAULT '',
    data_len BIGINT NOT NULL DEFAULT 0,
    msg_id VARCHAR(64) NOT NULL DEFAULT '',
    new_msg_id VARCHAR(64) NOT NULL DEFAULT '',
    master_buf_id VARCHAR(128) NOT NULL DEFAULT '',
    length BIGINT NOT NULL DEFAULT 0,
    from_user_name VARCHAR(128) NOT NULL DEFAULT '',
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    KEY idx_captured (captured_at)
  ) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COMMENT='微信群智能应答-媒体参数缓存'`,
	// wa_event 消息幂等登记（处理中撤回/更正与重复投递的可靠幂等；30 天清理）
	`CREATE TABLE IF NOT EXISTS wa_event (
    msg_key VARCHAR(64) NOT NULL PRIMARY KEY COMMENT '消息 svr_id（重复投递去重键）',
    group_id VARCHAR(128) NOT NULL DEFAULT '',
    event_kind VARCHAR(32) NOT NULL DEFAULT '' COMMENT 'message | recall',
    created_at BIGINT NOT NULL DEFAULT 0 COMMENT '登记时间（Unix 秒）',
    KEY idx_created (created_at)
  ) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COMMENT='微信群智能应答-消息幂等登记'`,
	// wa_recall 撤回暂存（撤回先于记录落库时持久化，记录出现时补判；1 小时窗口清理）
	`CREATE TABLE IF NOT EXISTS wa_recall (
    id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
    group_id VARCHAR(128) NOT NULL,
    sender_id VARCHAR(64) NOT NULL,
    at BIGINT NOT NULL COMMENT '撤回时间（Unix 秒）',
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    KEY idx_group_sender (group_id, sender_id, at),
    KEY idx_at (at)
  ) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COMMENT='微信群智能应答-撤回暂存'`,
	// wa_push_queue 失败补推技术队列（CaptureSink 失败快照，崩溃安全）
	`CREATE TABLE IF NOT EXISTS wa_push_queue (
    id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
    kind VARCHAR(16) NOT NULL COMMENT 'tree | crossing',
    payload JSON NOT NULL COMMENT 'CaptureSink 载荷快照（upsert/deletes 契约原样）',
    status VARCHAR(16) NOT NULL DEFAULT 'pending' COMMENT 'pending | done | dead',
    attempts INT NOT NULL DEFAULT 0,
    next_retry_at DATETIME NOT NULL COMMENT '下次补推时间',
    last_error VARCHAR(500) NOT NULL DEFAULT '',
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    KEY idx_status_retry (status, next_retry_at)
  ) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COMMENT='微信群智能应答-补推队列'`,
	// wa_chat_msg 未消费聊天归档（文本消息）
	`CREATE TABLE IF NOT EXISTS wa_chat_msg (
    id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
    day CHAR(10) NOT NULL COMMENT '北京时间自然日 yyyy-MM-dd',
    group_id VARCHAR(128) NOT NULL,
    group_name VARCHAR(64) NOT NULL DEFAULT '',
    msg_svr_id VARCHAR(64) NOT NULL DEFAULT '',
    sender_id VARCHAR(64) NOT NULL DEFAULT '',
    sender_name VARCHAR(64) NOT NULL DEFAULT '',
    content TEXT NULL,
    msg_time BIGINT NOT NULL DEFAULT 0 COMMENT '消息时间（Unix 秒）',
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE KEY uk_day_group_msg (day, group_id, msg_svr_id),
    KEY idx_day (day)
  ) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COMMENT='微信群智能应答-聊天归档消息'`,
	// wa_chat_doc 未消费聊天归档（文档类文件登记；二进制上传由 ChatArchiver 的消费方负责）
	`CREATE TABLE IF NOT EXISTS wa_chat_doc (
    id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
    day CHAR(10) NOT NULL COMMENT '北京时间自然日 yyyy-MM-dd',
    group_id VARCHAR(128) NOT NULL,
    group_name VARCHAR(64) NOT NULL DEFAULT '',
    msg_svr_id VARCHAR(64) NOT NULL DEFAULT '' COMMENT '文件消息 svr_id（去重键）',
    sender_id VARCHAR(64) NOT NULL DEFAULT '',
    sender_name VARCHAR(64) NOT NULL DEFAULT '',
    file_name VARCHAR(255) NOT NULL DEFAULT '',
    unsupported TINYINT NOT NULL DEFAULT 0 COMMENT '1=扩展名不在入库白名单',
    msg_time BIGINT NOT NULL DEFAULT 0,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE KEY uk_day_group_msg (day, group_id, msg_svr_id),
    KEY idx_day (day)
  ) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COMMENT='微信群智能应答-聊天归档文档'`,
	// wa_chat_day 聊天日归档进度（WF4 失败 attempts 累计，≥3 丢弃）
	`CREATE TABLE IF NOT EXISTS wa_chat_day (
    day CHAR(10) NOT NULL,
    group_id VARCHAR(128) NOT NULL,
    attempts INT NOT NULL DEFAULT 0,
    last_error VARCHAR(500) NOT NULL DEFAULT '',
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    PRIMARY KEY (day, group_id)
  ) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COMMENT='微信群智能应答-聊天日归档进度'`,
}

// schemaColumnMigrations 既有表补列（information_schema 判定，幂等；保存量数据）。
var schemaColumnMigrations = []struct {
	Table  string
	Column string
	DDL    string
}{
	{"wa_group", "team_id", "ALTER TABLE wa_group ADD COLUMN team_id BIGINT NOT NULL DEFAULT 0 COMMENT '归属班组 sys_team.id' AFTER enabled"},
	{"wa_group", "last_message_at", "ALTER TABLE wa_group ADD COLUMN last_message_at BIGINT NOT NULL DEFAULT 0 COMMENT '最近入站消息时间（Unix 秒）' AFTER team_id"},
	{"wa_rule", "name", "ALTER TABLE wa_rule ADD COLUMN name VARCHAR(64) NOT NULL DEFAULT '' COMMENT '规则名称' AFTER trigger_type"},
	{"wa_rule", "ask_enabled", "ALTER TABLE wa_rule ADD COLUMN ask_enabled TINYINT NOT NULL DEFAULT 0 COMMENT 'treeWatch 群内提问开关' AFTER enabled"},
	{"wa_rule", "ask_name_key", "ALTER TABLE wa_rule ADD COLUMN ask_name_key VARCHAR(200) NOT NULL DEFAULT '' COMMENT 'WF3 回答文件命名 Dify API Key' AFTER crossing_correct_key"},
	{"wa_rule", "chat_digest_key", "ALTER TABLE wa_rule ADD COLUMN chat_digest_key VARCHAR(200) NOT NULL DEFAULT '' COMMENT 'WF4 聊天总结 Dify API Key' AFTER ask_name_key"},
	{"wa_rule", "location_key", "ALTER TABLE wa_rule ADD COLUMN location_key VARCHAR(200) NOT NULL DEFAULT '' COMMENT '位置处理 Dify Workflow Key' AFTER chat_digest_key"},
}

// ensureColumns 按 information_schema 补缺列（MySQL 8 不支持 ADD COLUMN IF NOT EXISTS）。
func (m *Module) ensureColumns(ctx context.Context) error {
	for _, mig := range schemaColumnMigrations {
		var n int
		err := m.DB.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM information_schema.columns
       WHERE table_schema = DATABASE() AND table_name = ? AND column_name = ?`,
			mig.Table, mig.Column).Scan(&n)
		if err != nil {
			return err
		}
		if n > 0 {
			continue
		}
		if _, err := m.DB.ExecContext(ctx, mig.DDL); err != nil {
			return err
		}
	}
	return nil
}
