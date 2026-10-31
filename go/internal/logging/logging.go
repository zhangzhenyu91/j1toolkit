// Package logging 提供统一的应用结构化日志写入，敏感原文由调用方先脱敏。
package logging

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"shade/internal/config"
	"shade/internal/logsafe"
)

// ErrNoDatabase 未配置数据库（文件回退直写与旧队列共用）。
var ErrNoDatabase = errors.New("logging: 数据库未配置")

// Sink 应用日志写入目标：生产为文件优先的 *DurableSink，测试/兜底为 DirectSink。
type Sink interface {
	Write(ctx context.Context, e config.AppLogEntry) error
}

// DirectSink 同步直写数据库（测试、兜底与未接入文件持久层的调用方）。
type DirectSink struct{ DB *sql.DB }

// Write 写入一条日志（TraceID 为空时从 ctx 补链路 ID）。
func (s DirectSink) Write(ctx context.Context, e config.AppLogEntry) error {
	return WriteEntry(ctx, s.DB, e)
}

// Logger 应用日志写入器；每个实例绑定一个应用标识与一个写入目标。
type Logger struct {
	sink    Sink
	appCode string
}

// New 构造同步直写数据库的日志写入器（兼容旧调用方；生产装配改用 NewWithSink 接 DurableSink）。
func New(db *sql.DB, appCode string) *Logger {
	return NewWithSink(DirectSink{DB: db}, appCode)
}

// NewWithSink 用共享写入目标构造日志写入器（生产各应用与 integrations recorder 共用同一 DurableSink）。
func NewWithSink(sink Sink, appCode string) *Logger {
	return &Logger{sink: sink, appCode: appCode}
}

// WriteEntry 写入完整日志条目：TraceID 为空时自动取 ctx 中的链路 ID（trace_id 贯穿）。
func WriteEntry(ctx context.Context, db *sql.DB, e config.AppLogEntry) error {
	if db == nil {
		return ErrNoDatabase
	}
	if e.TraceID == "" {
		e.TraceID = TraceIDFromContext(ctx)
	}
	fillActor(&e, ctx)
	sanitizeEntry(&e)
	return config.InsertAppLog(ctx, db, e)
}

// Record 写入一条结构化日志。messageCN 是面向管理员的中文说明，raw 是技术原文；
// trace_id 为空自动取 ctx 中的链路 ID（与 HTTP 访问日志同链路贯穿）。
// fields 无法 JSON 序列化时保留整条日志，并在 detail 中记录 _marshal_error。
func (l *Logger) Record(ctx context.Context, level, category, event, messageCN, raw string, fields map[string]any) error {
	if l == nil || l.sink == nil {
		return ErrNoDatabase
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
	messageCN = logsafe.TextForLog(messageCN)
	raw = logsafe.TextForLog(raw)
	fields = logsafe.FieldsForLog(fields)
	var detail string
	if len(fields) > 0 {
		data, err := json.Marshal(fields)
		if err != nil {
			if fallback, ferr := json.Marshal(map[string]string{"_marshal_error": err.Error()}); ferr == nil {
				detail = string(fallback)
			} else {
				detail = `{"_marshal_error":"fields 无法序列化"}`
			}
		} else {
			detail = string(data)
		}
	}
	e := config.AppLogEntry{
		AppCode: l.appCode, Level: level, Category: category, Event: event,
		Message: raw, MessageCN: messageCN, RawMessage: raw, Detail: detail,
		LogID: config.NewAppLogID(), OccurredAt: time.Now(),
	}
	if e.TraceID == "" {
		e.TraceID = TraceIDFromContext(ctx)
	}
	fillActor(&e, ctx)
	sanitizeEntry(&e)
	if e.Category == "access" {
		e.IsAccess = true
	}
	err := l.sink.Write(ctx, e)
	// info 满队属有界队列的有意降级（warn/error 已由队列转 stderr）：不向业务模块报错，
	// 避免调用方把「已按设计丢弃」再放大成 stderr 噪声。DurableSink 不会丢日志，此分支仅为兼容旧 Queue。
	if errors.Is(err, ErrQueueFull) {
		return nil
	}
	return err
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
