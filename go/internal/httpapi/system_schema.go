package httpapi

// 系统表结构（sys_team / sys_user / sys_app / sys_user_app）：空库建表 + 老库补列。
// 对齐 server/src/db.js 的 DDL 与老库兼容补列；全部幂等、只补缺——
// 已有表/列一律不动（不改写既有列定义、不覆盖既有数据）。

import (
	"context"
	"database/sql"
	"fmt"
)

// systemSchemaDDL 空库建表语句（CREATE TABLE IF NOT EXISTS 幂等；列注释同 Node）。
var systemSchemaDDL = []string{
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
}

// legacyColumn 老库补列规格（MySQL ALTER 不支持 IF NOT EXISTS，先查 information_schema 再补）。
type legacyColumn struct {
	Table  string
	Column string
	DDL    string
}

// systemLegacyColumns 老库必要列升级清单（只补缺）：sys_user unionid/wxid/team_id/role/status、
// sys_team wxid/kvm_group_name/status、sys_app terminal/status。已有一律不动。
var systemLegacyColumns = []legacyColumn{
	{"sys_user", "unionid", `ALTER TABLE sys_user ADD COLUMN unionid VARCHAR(64) NULL COMMENT '微信 unionid'`},
	{"sys_user", "wxid", `ALTER TABLE sys_user ADD COLUMN wxid VARCHAR(128) NOT NULL DEFAULT '' COMMENT '微信 wxid（超管维护，消息微信推送用；空=未设置）'`},
	{"sys_user", "team_id", `ALTER TABLE sys_user ADD COLUMN team_id BIGINT UNSIGNED NULL COMMENT '所属班组，关联 sys_team.id；NULL=未分配'`},
	{"sys_user", "role", `ALTER TABLE sys_user ADD COLUMN role VARCHAR(16) NOT NULL DEFAULT 'user' COMMENT '角色：admin 超级管理员 / team_admin 班组管理员 / user 普通用户'`},
	{"sys_user", "status", `ALTER TABLE sys_user ADD COLUMN status TINYINT NOT NULL DEFAULT 1 COMMENT '1 正常 0 禁用'`},
	{"sys_team", "wxid", `ALTER TABLE sys_team ADD COLUMN wxid VARCHAR(128) NOT NULL DEFAULT '' COMMENT '微信群 wxid（超管维护，班组群推送用；空=未设置）'`},
	{"sys_team", "kvm_group_name", `ALTER TABLE sys_team ADD COLUMN kvm_group_name VARCHAR(64) NOT NULL DEFAULT '' COMMENT 'GLKVM 平台设备组名，空=与班组同名'`},
	{"sys_team", "status", `ALTER TABLE sys_team ADD COLUMN status TINYINT NOT NULL DEFAULT 1 COMMENT '1 启用 0 停用'`},
	{"sys_app", "terminal", `ALTER TABLE sys_app ADD COLUMN terminal VARCHAR(16) NOT NULL DEFAULT 'both' COMMENT '适配终端：both 双端 / mobile 移动端 / pc PC端'`},
	{"sys_app", "status", `ALTER TABLE sys_app ADD COLUMN status TINYINT NOT NULL DEFAULT 1 COMMENT '1 上架 0 下架'`},
}

// EnsureSystemSchema 系统表建表 + 老库补列（幂等、只补缺）：
//  1. 四表 CREATE TABLE IF NOT EXISTS（空库一次性建齐）；
//  2. 逐列查 information_schema.COLUMNS，缺失才 ALTER ADD COLUMN（老库升级，已有列不动）。
func EnsureSystemSchema(ctx context.Context, db *sql.DB) error {
	for _, ddl := range systemSchemaDDL {
		if _, err := db.ExecContext(ctx, ddl); err != nil {
			return err
		}
	}
	for _, col := range systemLegacyColumns {
		var name string
		err := db.QueryRowContext(ctx,
			fmt.Sprintf(`SELECT COLUMN_NAME FROM information_schema.COLUMNS
			 WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = '%s' AND COLUMN_NAME = '%s'`,
				col.Table, col.Column)).Scan(&name)
		if err == nil {
			continue // 列已存在，不动
		}
		if err != sql.ErrNoRows {
			return err
		}
		if _, err := db.ExecContext(ctx, col.DDL); err != nil {
			return err
		}
	}
	return nil
}
