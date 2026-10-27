package config

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// app_log 应用日志表：结构化中文、原文、上下文与 7 天保留清理。
const (
	AppLogSchemaSQL = `
CREATE TABLE IF NOT EXISTS app_log (
  id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT COMMENT '主键',
  app_code VARCHAR(64) NOT NULL DEFAULT '' COMMENT '应用标识',
  level VARCHAR(16) NOT NULL DEFAULT 'info' COMMENT '级别 debug/info/warn/error',
  category VARCHAR(32) NOT NULL DEFAULT 'business' COMMENT '事件类别',
  event VARCHAR(128) NOT NULL DEFAULT '' COMMENT '结构化事件标识',
  message TEXT NOT NULL COMMENT '技术原文',
  message_cn TEXT NULL COMMENT '中文说明',
  raw_message LONGTEXT NULL COMMENT '原始日志',
  context_json JSON NULL COMMENT '结构化上下文',
  trace_id VARCHAR(64) NOT NULL DEFAULT '' COMMENT '链路 ID',
  user_id BIGINT UNSIGNED NOT NULL DEFAULT 0 COMMENT '用户 ID（0 匿名）',
  team_id BIGINT UNSIGNED NULL COMMENT '班组 ID',
  detail LONGTEXT NULL COMMENT '细节（结构化 JSON 等）',
  created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP COMMENT '记录时间',
  PRIMARY KEY (id),
  KEY idx_app_log_created (created_at),
  KEY idx_app_log_app_created (app_code, created_at),
  KEY idx_app_log_level_created (level, created_at),
  KEY idx_app_log_category_created (category, created_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COMMENT='应用日志'
`

	AppLogRetentionDays = 7
	appLogMaxRetainDays = 365
)

// AppLogEntry 单条应用日志。
type AppLogEntry struct {
	AppCode     string
	Level       string
	Category    string
	Event       string
	Message     string // 技术原文
	MessageCN   string // 中文说明
	RawMessage  string // 原始日志（可与 Message 相同）
	ContextJSON string
	TraceID     string
	UserID      int64
	TeamID      int64
	Detail      string
}

// EnsureAppLogSchema 建表（幂等）。
func EnsureAppLogSchema(ctx context.Context, db *sql.DB) error {
	if _, err := db.ExecContext(ctx, AppLogSchemaSQL); err != nil {
		return fmt.Errorf("config: 建表 app_log: %w", err)
	}
	return nil
}

// InsertAppLog 写入一条日志（level 空取 info，category 空取 business）。
func InsertAppLog(ctx context.Context, db *sql.DB, e AppLogEntry) error {
	level := e.Level
	if level == "" {
		level = "info"
	}
	category := e.Category
	if category == "" {
		category = "business"
	}
	var detail any
	if e.Detail != "" {
		detail = e.Detail
	}
	var contextValue any
	if e.ContextJSON != "" {
		contextValue = e.ContextJSON
	}
	var teamValue any
	if e.TeamID != 0 {
		teamValue = e.TeamID
	}
	_, err := db.ExecContext(ctx,
		`INSERT INTO app_log (app_code, level, message, trace_id, user_id, detail, category, event, message_cn, raw_message, context_json, team_id) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		e.AppCode, level, e.Message, e.TraceID, e.UserID, detail, category, e.Event, e.MessageCN, e.RawMessage, contextValue, teamValue)
	if err != nil {
		return fmt.Errorf("config: 写入 app_log: %w", err)
	}
	return nil
}

// CleanupAppLog 按保留天数删除过期日志，返回删除行数。
func CleanupAppLog(ctx context.Context, db *sql.DB, retainDays int) (int64, error) {
	if retainDays == 0 {
		retainDays = AppLogRetentionDays
	}
	if retainDays < 0 || retainDays > appLogMaxRetainDays {
		return 0, fmt.Errorf("config: 保留天数 %d 越界（0 或 1-%d）", retainDays, appLogMaxRetainDays)
	}
	cutoff := time.Now().AddDate(0, 0, -retainDays)
	res, err := db.ExecContext(ctx,
		`DELETE FROM app_log WHERE created_at < ?`, cutoff)
	if err != nil {
		return 0, fmt.Errorf("config: 清理 app_log: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("config: 清理 app_log 影响行数: %w", err)
	}
	if n < 0 {
		return 0, errors.New("config: 清理 app_log 影响行数非法")
	}
	return n, nil
}
