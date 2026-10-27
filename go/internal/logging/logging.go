// Package logging 提供统一的应用结构化日志写入，敏感原文由调用方先脱敏。
package logging

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"shade/internal/config"
)

// Logger 应用日志写入器；每个实例绑定一个应用标识。
type Logger struct {
	db      *sql.DB
	appCode string
}

func New(db *sql.DB, appCode string) *Logger {
	return &Logger{db: db, appCode: appCode}
}

// WriteEntry 写入完整日志条目：TraceID 为空时自动取 ctx 中的链路 ID（trace_id 贯穿）。
func WriteEntry(ctx context.Context, db *sql.DB, e config.AppLogEntry) error {
	if db == nil {
		return errors.New("logging: 数据库未配置")
	}
	if e.TraceID == "" {
		e.TraceID = TraceIDFromContext(ctx)
	}
	return config.InsertAppLog(ctx, db, e)
}

// Record 写入一条结构化日志。messageCN 是面向管理员的中文说明，raw 是技术原文；
// trace_id 为空自动取 ctx 中的链路 ID（与 HTTP 访问日志同链路贯穿）。
func (l *Logger) Record(ctx context.Context, level, category, event, messageCN, raw string, fields map[string]any) error {
	if l == nil {
		return errors.New("logging: 数据库未配置")
	}
	if category == "" {
		category = "business"
	}
	if event == "" {
		event = "runtime.event"
	}
	if messageCN == "" {
		messageCN = raw
	}
	var detail string
	if len(fields) > 0 {
		data, err := json.Marshal(fields)
		if err != nil {
			return err
		}
		detail = string(data)
	}
	return WriteEntry(ctx, l.db, config.AppLogEntry{
		AppCode: l.appCode, Level: level, Category: category, Event: event,
		Message: raw, MessageCN: messageCN, RawMessage: raw, Detail: detail,
	})
}

func (l *Logger) Info(ctx context.Context, category, event, messageCN, raw string, fields map[string]any) error {
	return l.Record(ctx, "info", category, event, messageCN, raw, fields)
}

func (l *Logger) Warn(ctx context.Context, category, event, messageCN, raw string, fields map[string]any) error {
	return l.Record(ctx, "warn", category, event, messageCN, raw, fields)
}

func (l *Logger) Error(ctx context.Context, category, event, messageCN, raw string, fields map[string]any) error {
	return l.Record(ctx, "error", category, event, messageCN, raw, fields)
}
