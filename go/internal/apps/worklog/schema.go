// 出工日志表结构（与 server/src/worklog/schema.js 同口径）：
// 一条日志卡片 = 一次派车，未出车即 vehicle_id 为 NULL；
// worklog_sgcc_account / clockin / fee 为商旅打卡扩展表（verify 规则 a/f/g 读取），随本模块一并建齐。
package worklog

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// schemaDDL 建表语句（幂等）。
var schemaDDL = []string{
	`CREATE TABLE IF NOT EXISTS worklog_entry (
    id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
    team_id BIGINT UNSIGNED NULL COMMENT '所属班组，关联 sys_team.id',
    log_date DATE NOT NULL COMMENT '日志日期',
    patrol_content TEXT NULL COMMENT '巡视内容（可空）',
    remark TEXT NULL COMMENT '备注（可空）',
    remark_files JSON NULL COMMENT '备注附件 [{name,url,cos_key,type,size}]（type=image/video/doc）',
    vehicle_id BIGINT UNSIGNED NULL COMMENT '车牌，关联 worklog_vehicle.id；NULL=未出车',
    destination_id BIGINT UNSIGNED NULL COMMENT '目的地，关联 worklog_destination.id',
    dispatch_order_no VARCHAR(64) NULL COMMENT '派车单号（每日同步带入，改派车可改；「派车汇总」导出字段）',
    cross_team TINYINT NOT NULL DEFAULT 0 COMMENT '跨班日志：1 用车人含非归属班组成员（仅超管可建/删/改派车）',
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
    verify_status VARCHAR(16) NOT NULL DEFAULT 'pending' COMMENT 'pending/passed/mismatch/failed/skipped',
    work_content VARCHAR(512) NOT NULL DEFAULT '' COMMENT 'Dify 返回施工内容（title）',
    shot_time VARCHAR(32) NOT NULL DEFAULT '' COMMENT '水印拍摄时间（time）',
    weather VARCHAR(64) NOT NULL DEFAULT '' COMMENT '天气（weather）',
    location VARCHAR(255) NOT NULL DEFAULT '' COMMENT '地点（location）',
    lng VARCHAR(32) NOT NULL DEFAULT '' COMMENT '经度',
    lat VARCHAR(32) NOT NULL DEFAULT '' COMMENT '纬度',
    date_ok TINYINT NULL COMMENT '日期核验：1 相符 0 不符（NULL=历史数据未存）',
    dest_ok TINYINT NULL COMMENT '地点核验：1 相符 0 不符（NULL=历史数据未存）',
    is_watermark TINYINT NOT NULL DEFAULT 1 COMMENT '1 水印照片 0 非水印（免验证、不占每人一张）',
    md5 VARCHAR(32) NOT NULL DEFAULT '' COMMENT '图片内容 MD5（商旅按内容合并相同照片）',
    source VARCHAR(16) NOT NULL DEFAULT 'app' COMMENT '来源：app 壹匣上传 / sgcc 商旅拉回',
    sgcc_img_id VARCHAR(512) NOT NULL DEFAULT '' COMMENT '商旅费用照片关联（JSON：{worklog_member_id: 商旅图片id}）',
    sgcc_synced TINYINT NOT NULL DEFAULT 0 COMMENT '商旅费用照片同步：0 未同步 1 已同步 2 同步失败',
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    KEY idx_entry (entry_id),
    KEY idx_verify_created (verify_status, created_at)
  ) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,
	`CREATE TABLE IF NOT EXISTS worklog_remark_file (
    id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
    entry_id BIGINT UNSIGNED NOT NULL COMMENT '关联 worklog_entry.id',
    cos_key VARCHAR(255) NOT NULL COMMENT '服务端上传登记的对象键',
    name VARCHAR(128) NOT NULL DEFAULT '' COMMENT '原始文件名',
    type VARCHAR(16) NOT NULL DEFAULT '' COMMENT 'image/video/doc',
    size BIGINT NOT NULL DEFAULT 0 COMMENT '字节数',
    uploaded_by BIGINT UNSIGNED NULL COMMENT '上传人，关联 sys_user.id',
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE KEY uk_cos_key (cos_key),
    KEY idx_entry (entry_id)
  ) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COMMENT='出工日志-备注附件上传登记（归属与容量占位）'`,
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
    out_of_city TINYINT NOT NULL DEFAULT 0 COMMENT '市外标记：1 出差至市外（费用按市外标准验证）',
    stationed TINYINT NOT NULL DEFAULT 0 COMMENT '驻地标记：1 驻地（伙食40/交通0）；与市外互斥',
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
	`CREATE TABLE IF NOT EXISTS worklog_dispatch_sync_team (
    team_id BIGINT UNSIGNED NOT NULL PRIMARY KEY COMMENT '开启每日派车单同步的班组；在表即开启',
    created_by BIGINT UNSIGNED NULL COMMENT '操作人，关联 sys_user.id',
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
  ) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,
	`CREATE TABLE IF NOT EXISTS worklog_fee_std (
    id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
    scope TINYINT NOT NULL COMMENT '适用口径：0 市内 1 市外 2 驻地',
    food_fee DECIMAL(8,2) NOT NULL COMMENT '伙食补助标准',
    transit_fee DECIMAL(8,2) NOT NULL COMMENT '交通费标准',
    effective_from DATE NOT NULL COMMENT '生效起始日（含当日）',
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE KEY uk_scope_eff (scope, effective_from)
  ) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,
	// 商旅打卡扩展（verify 规则 a/f/g 读取；sgccclockin 模块复用同表）。
	`CREATE TABLE IF NOT EXISTS worklog_sgcc_account (
    id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
    team_id BIGINT UNSIGNED NULL COMMENT '绑定人当前所在班组',
    user_id BIGINT UNSIGNED NULL COMMENT '绑定人，关联 sys_user.id',
    member_id BIGINT UNSIGNED NULL COMMENT '关联出工成员 worklog_member.id',
    mobile VARCHAR(32) NOT NULL DEFAULT '' COMMENT '商旅注册手机号',
    token VARCHAR(128) NOT NULL DEFAULT '' COMMENT '商旅登录 token',
    device_type VARCHAR(64) NOT NULL DEFAULT '' COMMENT '打卡设备型号',
    system_version VARCHAR(64) NOT NULL DEFAULT '' COMMENT '系统版本',
    token_status TINYINT NOT NULL DEFAULT 1 COMMENT '登录态：1 有效 0 已失效',
    last_check_at DATETIME NULL COMMENT '最近一次登录态探测时间',
    backfill_done TINYINT NOT NULL DEFAULT 0 COMMENT '（已废弃）列保留兼容老库',
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    UNIQUE KEY uk_user (user_id),
    UNIQUE KEY uk_member (member_id)
  ) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,
	`CREATE TABLE IF NOT EXISTS worklog_clockin (
    id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
    team_id BIGINT UNSIGNED NULL COMMENT '所属班组',
    member_id BIGINT UNSIGNED NOT NULL COMMENT '关联 worklog_member.id',
    clock_date DATE NOT NULL COMMENT '打卡日期',
    seq TINYINT NOT NULL COMMENT '1 开始打卡 2 结束打卡',
    detail_id VARCHAR(64) NOT NULL DEFAULT '' COMMENT '商旅打卡明细 id',
    clock_time DATETIME NULL COMMENT '打卡时间',
    position VARCHAR(255) NOT NULL DEFAULT '' COMMENT '完整地址串',
    longitude VARCHAR(32) NOT NULL DEFAULT '' COMMENT '经度',
    latitude VARCHAR(32) NOT NULL DEFAULT '' COMMENT '纬度',
    city_code VARCHAR(32) NOT NULL DEFAULT '' COMMENT '城市行政区编码',
    city_name VARCHAR(64) NOT NULL DEFAULT '' COMMENT '城市名',
    work_hours VARCHAR(16) NOT NULL DEFAULT '' COMMENT '工时',
    source TINYINT NOT NULL DEFAULT 0 COMMENT '来源：0 壹匣打卡 1 商旅同步',
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    UNIQUE KEY uk_member_date_seq (member_id, clock_date, seq),
    KEY idx_team_date (team_id, clock_date)
  ) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,
	`CREATE TABLE IF NOT EXISTS worklog_fee (
    id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
    team_id BIGINT UNSIGNED NULL COMMENT '所属班组',
    member_id BIGINT UNSIGNED NOT NULL COMMENT '关联 worklog_member.id',
    fee_date DATE NOT NULL COMMENT '费用日期',
    food_fee DECIMAL(8,2) NOT NULL DEFAULT 0 COMMENT '伙食补助',
    transit_fee DECIMAL(8,2) NOT NULL DEFAULT 0 COMMENT '交通费',
    cost_center_code VARCHAR(64) NOT NULL DEFAULT '' COMMENT '成本中心编码',
    cost_center_name VARCHAR(128) NOT NULL DEFAULT '' COMMENT '成本中心名称',
    extra JSON NULL COMMENT '其余费用组件快照',
    synced_at DATETIME NULL COMMENT '最近一次与商旅对账时间',
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    UNIQUE KEY uk_member_date (member_id, fee_date),
    KEY idx_team_date (team_id, fee_date)
  ) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,
	`CREATE TABLE IF NOT EXISTS worklog_sync_log (
    id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
    team_id BIGINT UNSIGNED NULL COMMENT '所属班组',
    member_id BIGINT UNSIGNED NULL COMMENT '关联 worklog_member.id（整班核查为 NULL）',
    sync_date DATE NOT NULL COMMENT '核查的数据日期',
    scope VARCHAR(16) NOT NULL DEFAULT 'daily' COMMENT 'daily 当日核查 / remind 提醒前轻量同步',
    type VARCHAR(16) NOT NULL COMMENT 'clockin 打卡 / photo 照片 / fee 费用 / auth 登录态',
    result VARCHAR(8) NOT NULL COMMENT 'ok 一致 / diff 有差异已回写 / fail 失败',
    detail VARCHAR(512) NOT NULL DEFAULT '' COMMENT '明细',
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    KEY idx_team_date (team_id, sync_date)
  ) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,
}

// feeStdSeeds 费用验证标准种子（仅空表时写入；新规入行不翻旧账，调整直接改表）。
var feeStdSeeds = `INSERT IGNORE INTO worklog_fee_std (scope, food_fee, transit_fee, effective_from) VALUES
  (0, 60, 0, '2000-01-01'), (1, 60, 0, '2000-01-01'), (1, 100, 0, '2026-08-14'), (2, 40, 0, '2000-01-01')`

// schemaCompatIndexes 老库幂等补索引（MySQL ADD INDEX 无 IF NOT EXISTS，先查 information_schema）。
var schemaCompatIndexes = []struct {
	table, index, alterSQL string
}{
	{
		table: "worklog_entry", index: "idx_team",
		alterSQL: `ALTER TABLE worklog_entry ADD KEY idx_team (team_id)`,
	},
	{
		table: "worklog_photo", index: "idx_verify_created",
		alterSQL: `ALTER TABLE worklog_photo ADD KEY idx_verify_created (verify_status, created_at)`,
	},
}

// ensureSchemaIndexes 缺索引才 ALTER（幂等；已有库启动时补齐班组索引与 pending 清扫索引）。
func (m *Module) ensureSchemaIndexes(ctx context.Context) error {
	for _, idx := range schemaCompatIndexes {
		var got string
		query := fmt.Sprintf(`SELECT INDEX_NAME FROM information_schema.STATISTICS
			WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = '%s' AND INDEX_NAME = '%s'`, idx.table, idx.index)
		err := m.DB.QueryRowContext(ctx, query).Scan(&got)
		if err == nil {
			continue
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if _, err := m.DB.ExecContext(ctx, idx.alterSQL); err != nil {
			return err
		}
	}
	return nil
}
