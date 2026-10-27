package httpapi

import (
	"context"
	"time"
)

// 本文件定义 HTTP 层依赖的注入接口与数据形状；主代理以 MySQL 实现
// （可对接 go/internal/config 的 sys_runtime_config / app_log 与 sys_app 表），
// HTTP 层只依赖这里的接口，不感知数据库。

// App 应用目录条目（sys_app 摘要）。
type App struct {
	Code        string `json:"code"`                  // Vue 应用标识
	AppKey      string `json:"app_key"`               // 小程序/旧网页应用标识
	Name        string `json:"name"`                  // 应用名称
	Description string `json:"description,omitempty"` // 一句话描述
	Icon        string `json:"icon"`
	Path        string `json:"path"`
	Terminal    string `json:"terminal"`
	Sort        int    `json:"sort"`
	Enabled     bool   `json:"enabled"` // 是否启用
}

// AppCatalog 应用目录。
type AppCatalog interface {
	// ListApps 返回全部应用；错误实现可返回 APIError / ErrDependencyUnavailable。
	ListApps(ctx context.Context) ([]App, error)
}

// UserAppCatalog 按登录用户权限过滤的应用目录（app/list 口径）。
// 实现约定：返回该用户 sys_user_app 授权且启用的应用；role 为 admin 时追加
// 管理应用 wechat-answer / app-log（无需授权即可见）。
type UserAppCatalog interface {
	AppCatalog
	ListAppsForUser(ctx context.Context, userID int64, role string) ([]App, error)
}

// UserProfile 当前登录用户资料（GET /api/v1/user/profile；不返回 openid 本体）。
type UserProfile struct {
	ID        int64     `json:"id"`
	Username  string    `json:"username"`
	Nickname  string    `json:"nickname"`
	Avatar    string    `json:"avatar"`
	Team      string    `json:"team"`
	TeamID    int64     `json:"team_id"`
	Role      string    `json:"role"`
	WXBound   bool      `json:"wx_bound"`
	CreatedAt time.Time `json:"created_at"`
}

// TeamItem 班组条目（GET /api/v1/admin/teams，保留小程序和旧网页使用的完整字段）。
type TeamItem struct {
	ID           int64     `json:"id"`
	Name         string    `json:"name"`
	WXID         string    `json:"wxid"`
	KVMGroupName string    `json:"kvm_group_name"`
	Sort         int       `json:"sort"`
	Status       int       `json:"status"`
	CreatedAt    time.Time `json:"created_at"`
	UserCount    int64     `json:"user_count"`
}

// AccountStore 账号核心查询（profile / 班组列表）。
type AccountStore interface {
	// Profile 读当前用户资料；用户不存在返回 ErrNotFound。
	Profile(ctx context.Context, userID int64) (UserProfile, error)
	// Teams 返回班组列表（id/name，按 sort, id 排序）。
	Teams(ctx context.Context) ([]TeamItem, error)
}

// NoticeItem 单条站内通知（GET /api/v1/notice/list 条目）。
type NoticeItem struct {
	ID        int64    `json:"id"`
	Title     string   `json:"title"`
	Content   string   `json:"content"`
	Targets   []string `json:"targets"`
	CreatedBy string   `json:"createdByName"`
	CreatedAt string   `json:"createdAt"`
	Read      int      `json:"read"`
}

// NoticePage 通知列表结果（items 倒序 + 未读总数）。
type NoticePage struct {
	Items  []NoticeItem `json:"items"`
	Unread int64        `json:"unread"`
}

// NoticeReader 站内通知读取（当前用户可见：角色命中 targets 或本人命中 user_ids，已删除外）。
type NoticeReader interface {
	ListNotices(ctx context.Context, userID int64, role string, limit int) (NoticePage, error)
}

// ConfigItem 单条运行时配置。
// Value 为明文进入 HTTP 层，由本包统一按 Sensitive 脱敏后输出（MaskValue）；
// 依赖实现无需自行脱敏，但也绝不应把明文写到日志。
type ConfigItem struct {
	Key       string    `json:"key"`                  // 配置键
	Group     string    `json:"group,omitempty"`      // 分组（管理端分组展示）
	Value     string    `json:"value"`                // 值（敏感项输出前脱敏）
	Sensitive bool      `json:"sensitive"`            // 是否敏感（输出脱敏、禁止完整回显）
	UpdatedAt time.Time `json:"updated_at,omitempty"` // 最近更新时间
}

// ConfigAdmin 运行时配置读写。
// Get / List 返回明文值，脱敏由 HTTP 层统一负责；Update 写入明文值。
type ConfigAdmin interface {
	// Get 读单键；键不存在返回 ErrNotFound（或同码 APIError）。
	Get(ctx context.Context, key string) (ConfigItem, error)
	// List 返回全部配置项（键序稳定即可，如按 key 排序）。
	List(ctx context.Context) ([]ConfigItem, error)
	// Update 写入单键（明文）；键不存在返回 ErrNotFound。
	Update(ctx context.Context, key, value string) error
}

// LogFilter 日志筛选条件（零值字段不参与筛选）。
type LogFilter struct {
	AppCode  string    // 应用标识（精确匹配）
	Level    string    // 级别 debug/info/warn/error（精确匹配，已小写化）
	Category string    // 事件类别（精确匹配）
	TraceID  string    // 链路 ID
	Keyword  string    // 消息关键字（模糊匹配）
	Since    time.Time // 起始时间（含）
	Until    time.Time // 截止时间（不含）
	Page     int       // 页码，从 1 起
	PageSize int       // 每页条数（1~100）
}

// LogEntry 单条应用日志（字段对齐 app_log 表，见 go/internal/config）。
type LogEntry struct {
	ID          int64     `json:"id"`
	AppCode     string    `json:"app_code"`
	Level       string    `json:"level"`
	Category    string    `json:"category"`
	Event       string    `json:"event"`
	Message     string    `json:"message"`                // 原文
	MessageCN   string    `json:"message_cn,omitempty"`   // 中文说明
	RawMessage  string    `json:"raw_message,omitempty"`  // 技术原文
	ContextJSON string    `json:"context_json,omitempty"` // 结构化上下文
	TraceID     string    `json:"trace_id,omitempty"`
	UserID      int64     `json:"user_id,omitempty"`
	TeamID      int64     `json:"team_id,omitempty"`
	Detail      string    `json:"detail,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
}

// LogPage 日志分页结果。
type LogPage struct {
	Items    []LogEntry `json:"items"`     // 当页条目（无数据时为空数组）
	Total    int64      `json:"total"`     // 总条数
	Page     int        `json:"page"`      // 页码
	PageSize int        `json:"page_size"` // 每页条数
}

// LogReader 应用日志读取。
type LogReader interface {
	// ListLogs 按筛选条件返回单页日志；无数据库/连接失败返回 ErrDependencyUnavailable，
	// 未知应用由实现返回 ErrNotFound（HTTP 层也会在应用目录可用时先行校验）。
	ListLogs(ctx context.Context, f LogFilter) (LogPage, error)
}
