package httpapi

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"shade/internal/config"
)

// SQLAppCatalog 从 sys_app 读取应用目录，保持现有 sort/id 顺序。
type SQLAppCatalog struct{ DB *sql.DB }

func (c SQLAppCatalog) ListApps(ctx context.Context) ([]App, error) {
	rows, err := c.DB.QueryContext(ctx, `SELECT app_key, name, icon, path, terminal, sort, status FROM sys_app WHERE status = 1 ORDER BY sort, id`)
	if err != nil {
		return nil, DependencyUnavailable("应用目录读取失败")
	}
	defer rows.Close()
	out := make([]App, 0)
	for rows.Next() {
		var a App
		var status int
		if err := rows.Scan(&a.Code, &a.Name, &a.Icon, &a.Path, &a.Terminal, &a.Sort, &status); err != nil {
			return nil, err
		}
		a.AppKey = a.Code
		a.Enabled = status == 1
		out = append(out, a)
	}
	return out, rows.Err()
}

// adminOnlyAppKeys admin 无需授权即可见的管理应用（app/list 口径）。
var adminOnlyAppKeys = map[string]bool{"wechat-answer": true, "app-log": true}

// ListAppsForUser 按登录用户权限过滤（sys_user_app 授权且启用）；admin 追加管理应用
// wechat-answer / app-log（sys_app 中启用的行，已含则不重复）。
func (c SQLAppCatalog) ListAppsForUser(ctx context.Context, userID int64, role string) ([]App, error) {
	rows, err := c.DB.QueryContext(ctx,
		`SELECT a.app_key, a.name, a.icon, a.path, a.terminal, a.sort, a.status FROM sys_user_app ua
		 JOIN sys_app a ON a.id = ua.app_id
		 WHERE ua.user_id = ? AND a.status = 1
		 ORDER BY a.sort, a.id`, userID)
	if err != nil {
		return nil, DependencyUnavailable("应用目录读取失败")
	}
	out := make([]App, 0)
	seen := map[string]bool{}
	for rows.Next() {
		var a App
		var status int
		if err := rows.Scan(&a.Code, &a.Name, &a.Icon, &a.Path, &a.Terminal, &a.Sort, &status); err != nil {
			rows.Close()
			return nil, err
		}
		a.AppKey = a.Code
		a.Enabled = status == 1
		seen[a.Code] = true
		out = append(out, a)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()
	if role != "admin" {
		return out, nil
	}
	// admin 追加管理应用（按 sort,id 排在授权列表之后亦可；这里保持 sys_app 顺序合并）
	extraRows, err := c.DB.QueryContext(ctx,
		`SELECT app_key, name, icon, path, terminal, sort, status FROM sys_app
		 WHERE status = 1 AND app_key IN ('wechat-answer', 'app-log') ORDER BY sort, id`)
	if err != nil {
		return nil, DependencyUnavailable("应用目录读取失败")
	}
	defer extraRows.Close()
	for extraRows.Next() {
		var a App
		var status int
		if err := extraRows.Scan(&a.Code, &a.Name, &a.Icon, &a.Path, &a.Terminal, &a.Sort, &status); err != nil {
			return nil, err
		}
		if seen[a.Code] || !adminOnlyAppKeys[a.Code] {
			continue
		}
		a.AppKey = a.Code
		a.Enabled = status == 1
		seen[a.Code] = true
		out = append(out, a)
	}
	return out, extraRows.Err()
}

// StoreConfigAdmin 把 config.Store 适配为 HTTP 配置管理接口。
type StoreConfigAdmin struct{ Store *config.Store }

func (a StoreConfigAdmin) Get(ctx context.Context, key string) (ConfigItem, error) {
	value, err := a.Store.Get(ctx, key)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ConfigItem{}, NotFound("配置键不存在: " + key)
		}
		return ConfigItem{}, err
	}
	updated, err := a.Store.UpdatedAt(ctx, key)
	if err != nil {
		return ConfigItem{}, err
	}
	return ConfigItem{Key: key, Group: config.GroupForKey(key), Value: value, Sensitive: isSensitiveKey(key), UpdatedAt: updated}, nil
}

func isSensitiveKey(key string) bool {
	for _, spec := range config.DefaultSpecs() {
		if spec.Key == key {
			return spec.Sensitive
		}
	}
	// 自定义应用密钥默认按敏感处理，避免把未知密钥明文返回。
	return true
}

func (a StoreConfigAdmin) List(ctx context.Context) ([]ConfigItem, error) {
	values, err := a.Store.List(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]ConfigItem, 0, len(values))
	for _, v := range values {
		out = append(out, ConfigItem{Key: v.Key, Group: config.GroupForKey(v.Key), Value: v.Value, Sensitive: isSensitiveKey(v.Key), UpdatedAt: v.UpdatedAt})
	}
	return out, nil
}

func (a StoreConfigAdmin) Update(ctx context.Context, key, value string) error {
	if err := a.Store.Update(ctx, key, value); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return NotFound("配置键不存在: " + key)
		}
		return err
	}
	return nil
}

// SQLLogReader 从 app_log 查询结构化日志，支持应用、级别、类别、关键词和时间范围。
type SQLLogReader struct{ DB *sql.DB }

func (r SQLLogReader) ListLogs(ctx context.Context, f LogFilter) (LogPage, error) {
	where := []string{"1 = 1"}
	args := make([]any, 0)
	if f.AppCode != "" {
		where = append(where, "app_code = ?")
		args = append(args, f.AppCode)
	}
	if f.Level != "" {
		where = append(where, "level = ?")
		args = append(args, f.Level)
	}
	if f.Category != "" {
		where = append(where, "category = ?")
		args = append(args, f.Category)
	}
	if f.TraceID != "" {
		where = append(where, "trace_id = ?")
		args = append(args, f.TraceID)
	}
	if f.Keyword != "" {
		where = append(where, "(message LIKE ? OR message_cn LIKE ? OR raw_message LIKE ? OR event LIKE ?)")
		keyword := "%" + f.Keyword + "%"
		args = append(args, keyword, keyword, keyword, keyword)
	}
	if !f.Since.IsZero() {
		where = append(where, "created_at >= ?")
		args = append(args, f.Since)
	}
	if !f.Until.IsZero() {
		where = append(where, "created_at < ?")
		args = append(args, f.Until)
	}
	condition := strings.Join(where, " AND ")
	var total int64
	if err := r.DB.QueryRowContext(ctx, "SELECT COUNT(*) FROM app_log WHERE "+condition, args...).Scan(&total); err != nil {
		return LogPage{}, DependencyUnavailable("应用日志读取失败")
	}
	if f.Page < 1 {
		f.Page = 1
	}
	if f.PageSize < 1 || f.PageSize > 100 {
		f.PageSize = 20
	}
	args = append(args, f.PageSize, (f.Page-1)*f.PageSize)
	rows, err := r.DB.QueryContext(ctx, `SELECT id, app_code, level, category, event, message, message_cn, raw_message, context_json, trace_id, user_id, team_id, created_at
		FROM app_log WHERE `+condition+` ORDER BY created_at DESC, id DESC LIMIT ? OFFSET ?`, args...)
	if err != nil {
		return LogPage{}, DependencyUnavailable("应用日志读取失败")
	}
	defer rows.Close()
	items := make([]LogEntry, 0)
	for rows.Next() {
		var e LogEntry
		var category, event, message, trace string
		var messageCN, raw, contextJSON sql.NullString
		var userID, teamID sql.NullInt64
		if err := rows.Scan(&e.ID, &e.AppCode, &e.Level, &category, &event, &message, &messageCN, &raw, &contextJSON, &trace, &userID, &teamID, &e.CreatedAt); err != nil {
			return LogPage{}, err
		}
		e.Category, e.Event, e.Message, e.TraceID = category, event, message, trace
		e.MessageCN, e.RawMessage, e.ContextJSON = messageCN.String, raw.String, contextJSON.String
		e.UserID, e.TeamID = userID.Int64, teamID.Int64
		items = append(items, e)
	}
	return LogPage{Items: items, Total: total, Page: f.Page, PageSize: f.PageSize}, rows.Err()
}

// FixtureDeps 构造本地开发专用依赖，不访问数据库。仅当 SHADE_LOCAL_FIXTURE=1 时使用。
func FixtureDeps() Deps {
	f := &fixture{
		apps: []App{
			{Code: "work-log", Name: "出工日志", Description: "本地演示应用", Enabled: true},
			{Code: "wechat-answer", Name: "微信智能应答", Description: "本地演示应用", Enabled: true},
			{Code: "app-log", Name: "应用日志", Description: "本地演示应用", Enabled: true},
		},
		cfg: map[string]ConfigItem{
			"DIFY_API_URL":         {Key: "DIFY_API_URL", Group: "通用集成", Value: "https://dify.example.invalid", Sensitive: false, UpdatedAt: time.Now()},
			"DIFY_WORKLOG_API_KEY": {Key: "DIFY_WORKLOG_API_KEY", Group: "通用集成", Value: "local-fixture-secret", Sensitive: true, UpdatedAt: time.Now()},
		},
		logs: []LogEntry{
			{ID: 3, AppCode: "wechat-answer", Level: "info", Category: "message", Event: "wechat.message.received", MessageCN: "收到监听群文本消息", Message: "[message] incoming group text", TraceID: "local-3", CreatedAt: time.Now().Add(-time.Minute)},
			{ID: 2, AppCode: "work-log", Level: "error", Category: "external", Event: "dify.workflow.timeout", MessageCN: "照片识别工作流超时", Message: "timeout of 90000ms exceeded", TraceID: "local-2", CreatedAt: time.Now().Add(-2 * time.Minute)},
			{ID: 1, AppCode: "app-log", Level: "info", Category: "scheduler", Event: "log.retention.completed", MessageCN: "七天前日志清理完成", Message: "[app-log] retention cleanup completed", TraceID: "local-1", CreatedAt: time.Now().Add(-3 * time.Minute)},
		},
	}
	return Deps{Apps: f, Cfg: f, Logs: f, Accounts: f, Notices: f}
}

type fixture struct {
	apps []App
	cfg  map[string]ConfigItem
	logs []LogEntry
}

func (f *fixture) ListApps(context.Context) ([]App, error) { return f.apps, nil }

func (f *fixture) Get(_ context.Context, key string) (ConfigItem, error) {
	it, ok := f.cfg[key]
	if !ok {
		return ConfigItem{}, NotFound("配置键不存在: " + key)
	}
	return it, nil
}

func (f *fixture) List(context.Context) ([]ConfigItem, error) {
	out := make([]ConfigItem, 0, len(f.cfg))
	for _, it := range f.cfg {
		out = append(out, it)
	}
	return out, nil
}

func (f *fixture) Update(_ context.Context, key, value string) error {
	it, ok := f.cfg[key]
	if !ok {
		return NotFound("配置键不存在: " + key)
	}
	it.Value = value
	it.UpdatedAt = time.Now()
	f.cfg[key] = it
	return nil
}

func (f *fixture) ListLogs(_ context.Context, filter LogFilter) (LogPage, error) {
	items := make([]LogEntry, 0)
	for _, e := range f.logs {
		if filter.AppCode != "" && e.AppCode != filter.AppCode {
			continue
		}
		if filter.Level != "" && e.Level != filter.Level {
			continue
		}
		if filter.Category != "" && e.Category != filter.Category {
			continue
		}
		if filter.TraceID != "" && e.TraceID != filter.TraceID {
			continue
		}
		if filter.Keyword != "" && !strings.Contains(strings.ToLower(e.Message+e.MessageCN+e.RawMessage+e.Event), strings.ToLower(filter.Keyword)) {
			continue
		}
		if !filter.Since.IsZero() && e.CreatedAt.Before(filter.Since) {
			continue
		}
		if !filter.Until.IsZero() && !e.CreatedAt.Before(filter.Until) {
			continue
		}
		items = append(items, e)
	}
	if filter.Page < 1 {
		filter.Page = 1
	}
	if filter.PageSize < 1 || filter.PageSize > 100 {
		filter.PageSize = 20
	}
	start := (filter.Page - 1) * filter.PageSize
	if start > len(items) {
		start = len(items)
	}
	end := start + filter.PageSize
	if end > len(items) {
		end = len(items)
	}
	return LogPage{Items: items[start:end], Total: int64(len(items)), Page: filter.Page, PageSize: filter.PageSize}, nil
}

// Profile 本地演示用户资料（fixture 模式固定账号）。
func (f *fixture) Profile(context.Context, int64) (UserProfile, error) {
	return UserProfile{ID: 1, Username: "demo", Nickname: "本地演示", Team: "检修一班", TeamID: 1, Role: "admin", CreatedAt: time.Now()}, nil
}

// Teams 本地演示班组列表。
func (f *fixture) Teams(context.Context) ([]TeamItem, error) {
	return []TeamItem{{ID: 1, Name: "检修一班"}, {ID: 2, Name: "运维二班"}}, nil
}

// ListNotices 本地演示通知（空列表 + 0 未读）。
func (f *fixture) ListNotices(context.Context, int64, string, int) (NoticePage, error) {
	return NoticePage{Items: []NoticeItem{}}, nil
}

// 防止适配器误把数据库错误当成无记录；供编译期检查接口一致性。
var _ AppCatalog = SQLAppCatalog{}
var _ ConfigAdmin = StoreConfigAdmin{}
var _ LogReader = SQLLogReader{}
var _ = fmt.Sprintf
