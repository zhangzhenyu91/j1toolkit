package config

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

// app_log 应用日志表：结构化中文、原文、上下文；默认永久保留，
// log_id 为文件持久层生成的唯一 ID（幂等入库），occurred_at 为事件发生时间。
const (
	AppLogSchemaSQL = `
CREATE TABLE IF NOT EXISTS app_log (
  id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT COMMENT '主键',
  log_id VARCHAR(64) NULL COMMENT '日志唯一 ID（文件持久层生成，幂等入库）',
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
  source VARCHAR(64) NOT NULL DEFAULT '' COMMENT '日志来源（文件/直写等）',
  is_access TINYINT(1) NOT NULL DEFAULT 0 COMMENT '是否访问日志',
  occurred_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP COMMENT '事件发生时间',
  created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP COMMENT '记录时间',
  PRIMARY KEY (id),
  UNIQUE KEY uk_app_log_log_id (log_id),
  KEY idx_app_log_created (created_at),
  KEY idx_app_log_app_created (app_code, created_at),
  KEY idx_app_log_level_created (level, created_at),
  KEY idx_app_log_category_created (category, created_at),
  KEY idx_app_log_trace (trace_id),
  KEY idx_app_log_occurred (occurred_at),
  KEY idx_app_log_access_occurred (is_access, occurred_at, id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COMMENT='应用日志'
`

	// AppLogRetentionDays 默认保留天数：0 表示永久保留。
	AppLogRetentionDays = 0
	// AppLogMaxRetentionDays 显式配置的保留天数上限。
	AppLogMaxRetentionDays = 3650
	// appLogBackfillBatch 老库回填默认批大小。
	appLogBackfillBatch = 5000
	// appLogCleanupBatch 单次 DELETE 的行数上限（避免长事务/锁表）。
	appLogCleanupBatch = 5000
	// appLogBatchChunk 单条 INSERT 最多携带的行数（控制占位符数量）。
	appLogBatchChunk = 500
)

// appLogInsertColumns 与 appLogValues 的参数顺序严格对应；旧调用方依赖前 12 列位置。
const appLogInsertColumns = "app_code, level, message, trace_id, user_id, detail, category, event, message_cn, raw_message, context_json, team_id, log_id, occurred_at, source, is_access"

// AppLogEntry 单条应用日志。
type AppLogEntry struct {
	LogID       string    // 唯一 ID（为空自动生成）
	OccurredAt  time.Time // 事件发生时间（为空取当前时间）
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
	Source      string
	IsAccess    bool
}

var appLogIDSeq atomic.Uint64

// NewAppLogID 生成日志唯一 ID（时间戳 + 16 字节随机数，远小于列上限 64）。
func NewAppLogID() string {
	var buf [16]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return fmt.Sprintf("%d-%d", time.Now().UnixNano(), appLogIDSeq.Add(1))
	}
	return fmt.Sprintf("%d-%s", time.Now().UnixNano(), hex.EncodeToString(buf[:]))
}

// normalizeAppLogEntry 归一化缺省值：级别/类别、log_id、事件时间与访问标记。
func normalizeAppLogEntry(e *AppLogEntry) {
	if e.Level == "" {
		e.Level = "info"
	}
	if e.Category == "" {
		e.Category = "business"
	}
	if e.LogID == "" {
		e.LogID = NewAppLogID()
	}
	if e.OccurredAt.IsZero() {
		e.OccurredAt = time.Now()
	}
	if e.Category == "access" {
		e.IsAccess = true
	}
}

func appLogValues(e AppLogEntry) []any {
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
	var accessValue int64
	if e.IsAccess {
		accessValue = 1
	}
	return []any{
		e.AppCode, e.Level, e.Message, e.TraceID, e.UserID, detail, e.Category, e.Event,
		e.MessageCN, e.RawMessage, contextValue, teamValue, e.LogID, e.OccurredAt, e.Source, accessValue,
	}
}

// EnsureAppLogSchema 建表并幂等地给老库补列/索引（DDL 使用调用方 ctx，不回填数据）。
// 老库回填由 BackfillAppLog 使用独立预算执行，避免与 30s 启动建表上下文互相拖死。
func EnsureAppLogSchema(ctx context.Context, db *sql.DB) error {
	if db == nil {
		return errors.New("config: 建表 app_log: 数据库未配置")
	}
	if _, err := db.ExecContext(ctx, AppLogSchemaSQL); err != nil {
		return fmt.Errorf("config: 建表 app_log: %w", err)
	}
	if err := ensureAppLogColumns(ctx, db); err != nil {
		return err
	}
	return ensureAppLogIndexes(ctx, db)
}

// appLogColumnDDL 老库缺失列（occurred_at 先允许 NULL，回填完成后再收紧）。
var appLogColumnDDL = []struct{ name, clause string }{
	{"log_id", "log_id VARCHAR(64) NULL COMMENT '日志唯一 ID（文件持久层生成，幂等入库）'"},
	{"occurred_at", "occurred_at DATETIME NULL COMMENT '事件发生时间'"},
	{"source", "source VARCHAR(64) NOT NULL DEFAULT '' COMMENT '日志来源（文件/直写等）'"},
	{"is_access", "is_access TINYINT(1) NOT NULL DEFAULT 0 COMMENT '是否访问日志'"},
}

// appLogIndexDDL 老库缺失索引（唯一键保证 log_id 幂等）。
var appLogIndexDDL = []struct{ name, clause string }{
	{"uk_app_log_log_id", "UNIQUE KEY uk_app_log_log_id (log_id)"},
	{"idx_app_log_trace", "INDEX idx_app_log_trace (trace_id)"},
	{"idx_app_log_occurred", "INDEX idx_app_log_occurred (occurred_at)"},
	{"idx_app_log_access_occurred", "INDEX idx_app_log_access_occurred (is_access, occurred_at, id)"},
}

func ensureAppLogColumns(ctx context.Context, db *sql.DB) error {
	existing, err := appLogSchemaNames(ctx, db,
		`SELECT COLUMN_NAME FROM information_schema.COLUMNS WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'app_log'`)
	if err != nil {
		return fmt.Errorf("config: 读取 app_log 列: %w", err)
	}
	for _, col := range appLogColumnDDL {
		if existing[col.name] {
			continue
		}
		if _, err := db.ExecContext(ctx, "ALTER TABLE app_log ADD COLUMN "+col.clause); err != nil {
			return fmt.Errorf("config: app_log 补列 %s: %w", col.name, err)
		}
	}
	return nil
}

func ensureAppLogIndexes(ctx context.Context, db *sql.DB) error {
	existing, err := appLogSchemaNames(ctx, db,
		`SELECT DISTINCT INDEX_NAME FROM information_schema.STATISTICS WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'app_log'`)
	if err != nil {
		return fmt.Errorf("config: 读取 app_log 索引: %w", err)
	}
	for _, idx := range appLogIndexDDL {
		if existing[idx.name] {
			continue
		}
		if _, err := db.ExecContext(ctx, "ALTER TABLE app_log ADD "+idx.clause); err != nil {
			return fmt.Errorf("config: app_log 补索引 %s: %w", idx.name, err)
		}
	}
	return nil
}

func appLogSchemaNames(ctx context.Context, db *sql.DB, query string) (map[string]bool, error) {
	rows, err := db.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		out[name] = true
	}
	return out, rows.Err()
}

// BackfillAppLog 分批回填老库 occurred_at=created_at 与 access 标记，
// 回填完成后把 occurred_at 收紧为 NOT NULL。可重复执行（只处理缺失值）。
func BackfillAppLog(ctx context.Context, db *sql.DB, batchSize int) error {
	if db == nil {
		return errors.New("config: 回填 app_log: 数据库未配置")
	}
	if batchSize <= 0 {
		batchSize = appLogBackfillBatch
	}
	for {
		n, err := appLogBackfillBatchRun(ctx, db,
			`UPDATE app_log SET occurred_at = created_at WHERE occurred_at IS NULL LIMIT ?`, batchSize)
		if err != nil {
			return fmt.Errorf("config: 回填 occurred_at: %w", err)
		}
		if n < int64(batchSize) {
			break
		}
	}
	for {
		n, err := appLogBackfillBatchRun(ctx, db,
			`UPDATE app_log SET is_access = 1 WHERE is_access = 0 AND category = 'access' LIMIT ?`, batchSize)
		if err != nil {
			return fmt.Errorf("config: 回填 is_access: %w", err)
		}
		if n < int64(batchSize) {
			break
		}
	}
	nullable, err := appLogOccurredNullable(ctx, db)
	if err != nil {
		return fmt.Errorf("config: 读取 occurred_at 可空性: %w", err)
	}
	if !nullable {
		return nil
	}
	if _, err := db.ExecContext(ctx,
		`ALTER TABLE app_log MODIFY COLUMN occurred_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP COMMENT '事件发生时间'`); err != nil {
		return fmt.Errorf("config: 收紧 occurred_at NOT NULL: %w", err)
	}
	return nil
}

func appLogBackfillBatchRun(ctx context.Context, db *sql.DB, query string, batchSize int) (int64, error) {
	res, err := db.ExecContext(ctx, query, batchSize)
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, err
	}
	if n < 0 {
		return 0, errors.New("影响行数非法")
	}
	return n, nil
}

func appLogOccurredNullable(ctx context.Context, db *sql.DB) (bool, error) {
	var nullable string
	err := db.QueryRowContext(ctx,
		`SELECT IS_NULLABLE FROM information_schema.COLUMNS WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'app_log' AND COLUMN_NAME = 'occurred_at'`).Scan(&nullable)
	if err != nil {
		return false, err
	}
	return strings.EqualFold(nullable, "YES"), nil
}

// ParseAppLogRetention 解析保留天数：空或 0 表示永久；正数 1-3650；其他值报错。
// 解析失败时调用方必须停用清理，绝不回退到 7 天。
func ParseAppLogRetention(raw string) (int, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("config: 保留天数 %q 不是整数", raw)
	}
	if n == 0 {
		return 0, nil
	}
	if n < 1 || n > AppLogMaxRetentionDays {
		return 0, fmt.Errorf("config: 保留天数 %d 越界（0 永久或 1-%d）", n, AppLogMaxRetentionDays)
	}
	return n, nil
}

// InsertAppLog 写入一条日志（level 空取 info，category 空取 business，log_id/事件时间自动补缺）。
func InsertAppLog(ctx context.Context, db *sql.DB, e AppLogEntry) error {
	if db == nil {
		return errors.New("config: 写入 app_log: 数据库未配置")
	}
	normalizeAppLogEntry(&e)
	_, err := db.ExecContext(ctx,
		`INSERT INTO app_log (`+appLogInsertColumns+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		appLogValues(e)...)
	if err != nil {
		return fmt.Errorf("config: 写入 app_log: %w", err)
	}
	return nil
}

// InsertAppLogBatch 在单个事务内批量写入（重复 log_id 由唯一键幂等忽略）。
// 使用 ON DUPLICATE KEY UPDATE log_id=log_id，禁止 INSERT IGNORE（后者会吞掉其他错误）。
func InsertAppLogBatch(ctx context.Context, db *sql.DB, entries []AppLogEntry) error {
	if db == nil {
		return errors.New("config: 批量写入 app_log: 数据库未配置")
	}
	if len(entries) == 0 {
		return nil
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("config: 批量写入 app_log 开启事务: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	for start := 0; start < len(entries); start += appLogBatchChunk {
		end := start + appLogBatchChunk
		if end > len(entries) {
			end = len(entries)
		}
		chunk := make([]AppLogEntry, end-start)
		copy(chunk, entries[start:end])
		placeholders := make([]string, len(chunk))
		args := make([]any, 0, len(chunk)*16)
		for i := range chunk {
			normalizeAppLogEntry(&chunk[i])
			placeholders[i] = "(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)"
			args = append(args, appLogValues(chunk[i])...)
		}
		query := "INSERT INTO app_log (" + appLogInsertColumns + ") VALUES " +
			strings.Join(placeholders, ", ") + " ON DUPLICATE KEY UPDATE log_id=log_id"
		if _, err := tx.ExecContext(ctx, query, args...); err != nil {
			return fmt.Errorf("config: 批量写入 app_log: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("config: 批量写入 app_log 提交: %w", err)
	}
	return nil
}

// CleanupAppLog 按 occurred_at 删除超过保留天数的日志，返回删除行数。
// retainDays=0 表示永久保留（不执行 DELETE）；正数 1-3650；其他值报错且不删除。
func CleanupAppLog(ctx context.Context, db *sql.DB, retainDays int) (int64, error) {
	if db == nil {
		return 0, errors.New("config: 清理 app_log: 数据库未配置")
	}
	if retainDays == 0 {
		return 0, nil
	}
	if retainDays < 0 || retainDays > AppLogMaxRetentionDays {
		return 0, fmt.Errorf("config: 保留天数 %d 越界（0 永久或 1-%d）", retainDays, AppLogMaxRetentionDays)
	}
	cutoff := time.Now().AddDate(0, 0, -retainDays)
	var total int64
	for {
		if err := ctx.Err(); err != nil {
			return total, err
		}
		res, err := db.ExecContext(ctx,
			`DELETE FROM app_log WHERE occurred_at < ? LIMIT ?`, cutoff, appLogCleanupBatch)
		if err != nil {
			return total, fmt.Errorf("config: 清理 app_log: %w", err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return total, fmt.Errorf("config: 清理 app_log 影响行数: %w", err)
		}
		if n < 0 {
			return total, errors.New("config: 清理 app_log 影响行数非法")
		}
		total += n
		if n < appLogCleanupBatch {
			return total, nil
		}
	}
}

// AppLogIDsBefore 返回待清理区间（occurred_at < cutoff）内的 log_id（含空值，由调用方 fail-closed）。
// 保留清理必须按 log_id 与文件逐条对账，不能仅凭日期存在判定有文件副本。
func AppLogIDsBefore(ctx context.Context, db *sql.DB, cutoff time.Time) ([]string, error) {
	if db == nil {
		return nil, errors.New("config: 读取 app_log log_id: 数据库未配置")
	}
	rows, err := db.QueryContext(ctx,
		`SELECT log_id FROM app_log WHERE occurred_at < ?`, cutoff)
	if err != nil {
		return nil, fmt.Errorf("config: 读取 app_log log_id: %w", err)
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id sql.NullString
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("config: 扫描 app_log log_id: %w", err)
		}
		ids = append(ids, id.String)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("config: 读取 app_log log_id: %w", err)
	}
	return ids, nil
}

// AppLogDaysBefore 返回待清理区间（occurred_at < cutoff）内存在日志的日期集合（按日期升序、本地日）。
// 供保留清理先校验文件归档覆盖，避免删除没有文件副本的历史行。
func AppLogDaysBefore(ctx context.Context, db *sql.DB, cutoff time.Time) ([]time.Time, error) {
	if db == nil {
		return nil, errors.New("config: 读取 app_log 日期: 数据库未配置")
	}
	rows, err := db.QueryContext(ctx,
		`SELECT DISTINCT DATE(occurred_at) AS d FROM app_log WHERE occurred_at < ? ORDER BY d`, cutoff)
	if err != nil {
		return nil, fmt.Errorf("config: 读取 app_log 日期: %w", err)
	}
	defer rows.Close()
	var days []time.Time
	for rows.Next() {
		var raw any
		if err := rows.Scan(&raw); err != nil {
			return nil, fmt.Errorf("config: 扫描 app_log 日期: %w", err)
		}
		day, err := parseAppLogDay(raw)
		if err != nil {
			return nil, err
		}
		days = append(days, day)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("config: 读取 app_log 日期: %w", err)
	}
	return days, nil
}

func parseAppLogDay(raw any) (time.Time, error) {
	switch v := raw.(type) {
	case time.Time:
		return time.Date(v.Year(), v.Month(), v.Day(), 0, 0, 0, 0, time.Local), nil
	case []byte:
		return parseAppLogDayText(string(v))
	case string:
		return parseAppLogDayText(v)
	default:
		return time.Time{}, fmt.Errorf("config: app_log 日期类型非法: %T", raw)
	}
}

func parseAppLogDayText(text string) (time.Time, error) {
	text = strings.TrimSpace(text)
	for _, layout := range []string{"2006-01-02", "2006-01-02 15:04:05"} {
		if day, err := time.ParseInLocation(layout, text, time.Local); err == nil {
			return time.Date(day.Year(), day.Month(), day.Day(), 0, 0, 0, 0, time.Local), nil
		}
	}
	return time.Time{}, fmt.Errorf("config: app_log 日期 %q 无法解析", text)
}
