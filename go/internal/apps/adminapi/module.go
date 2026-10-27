// Package adminapi 迁移 server/src/routes/admin.js 与 server/src/notice 的管理侧接口：
// 班组 CRUD、用户 CRUD（启用/角色/班组/wxid/密码重置/删除）、应用启停与排序、按人应用授权，
// 以及站内通知已读/删除/超管推送（Node 路由挂在 /api/v1/notice，随本模块一并补齐）。
//
// 路由以 httpapi.Module 挂载在 /api/v1 之下；GET /api/v1/admin/teams（窄列表 id/name）与
// GET /api/v1/notice/list 已由 httpapi 提供，本模块不重复注册以免路由冲突——
// 班组全量管理列表（含成员数/状态/排序）走 GET /api/v1/admin/teams/full。
// /api/v1/admin/* 由 httpapi.AuthMiddleware 的 isAdminPath 门控超管；通知已读/按人删除
// 同 Node 口径仅需登录，推送/全局删除在处理器内自检超管（Node requireAdmin）。
package adminapi

import (
	"context"
	"database/sql"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"shade/internal/auth"
	"shade/internal/httpapi"
)

// 可推送角色全集（与 sys_user.role 口径一致）。
var roles = []string{"admin", "team_admin", "user"}

// Logger 应用日志写入（对接 go/internal/logging.Logger；nil 不记录，仅跳过审计日志）。
type Logger interface {
	Info(ctx context.Context, category, event, messageCN, raw string, fields map[string]any) error
	Warn(ctx context.Context, category, event, messageCN, raw string, fields map[string]any) error
	Error(ctx context.Context, category, event, messageCN, raw string, fields map[string]any) error
}

// Module 是管理员账号/权限/通知管理模块。
type Module struct {
	DB *sql.DB
	// COS 通知配图对象存储；nil 时上传返回未配置。
	COS NoticeImageStore
	// Log 可选：管理操作应用日志（记录建删改与推送）；nil 不记录。
	Log Logger
	// OnUserChanged 账号变更后的出工成员字典同步钩子（Node worklog/member-sync.syncMemberForUser
	// 口径：新建/改昵称/调班组/停启用后调用）；nil 跳过。
	OnUserChanged func(ctx context.Context, userID int64)
	// OnTeamRenamed 班组改名级联钩子（Node safeday/migrate.renameTeamFolder + netdisk 缓存失效）；nil 跳过。
	OnTeamRenamed func(oldName, newName string)
	// HasTeamSafedayRecords 班组删除前的安全日活动记录检查（Node safeday/store.list 按班组名）；
	// 返回 true 表示有记录、拒绝删除；nil 跳过该检查。
	HasTeamSafedayRecords func(teamName string) bool
}

// New 构造管理员模块；钩子与日志可在装配后注入。
func New(db *sql.DB) *Module {
	return &Module{DB: db}
}

// RegisterRoutes 注册 /api/v1 下的管理与通知路由（不与 httpapi 已有路由重叠）。
func (m *Module) RegisterRoutes(r chi.Router) {
	// 班组：全量列表 + 增改删（GET /admin/teams 窄列表归 httpapi）
	r.Get("/admin/teams/full", m.listTeams)
	r.Post("/admin/teams", m.createTeam)
	r.Put("/admin/teams/{id}", m.updateTeam)
	r.Delete("/admin/teams/{id}", m.deleteTeam)
	// 用户与按人应用授权
	r.Get("/admin/users", m.listUsers)
	r.Post("/admin/users", m.createUser)
	r.Put("/admin/users/{id}", m.updateUser)
	r.Delete("/admin/users/{id}", m.deleteUser)
	r.Get("/admin/users/{id}/apps", m.userApps)
	r.Put("/admin/users/{id}/apps", m.setUserApps)
	// 应用目录：全量列表 + 启停/排序
	r.Get("/admin/apps", m.listApps)
	r.Put("/admin/apps/{id}", m.updateApp)
	// 站内通知：已读 / 按人删除 / 超管推送（GET /notice/list 归 httpapi）
	r.Post("/notice/{id}/read", m.noticeRead)
	r.Post("/notice/read-all", m.noticeReadAll)
	r.Post("/notice/del-read", m.noticeDelRead)
	r.Post("/notice/push", m.noticePush)
	r.Post("/notice/upload-image", m.noticeImageUpload)
	r.Delete("/notice/{id}", m.noticeDelete)
}

// ident 取当前登录身份；未登录写 40101 并返回 false。
func (m *Module) ident(w http.ResponseWriter, r *http.Request) (auth.Identity, bool) {
	ident, ok := httpapi.IdentityFromContext(r.Context())
	if !ok {
		fail(w, http.StatusUnauthorized, codeNoLogin, "未登录或登录已过期")
		return auth.Identity{}, false
	}
	return ident, true
}

// requireAdmin 超管门控（Node requireAdmin 同口径，供通知推送/全局删除自检）。
func (m *Module) requireAdmin(w http.ResponseWriter, ident auth.Identity) bool {
	if ident.Role != "admin" {
		fail(w, http.StatusForbidden, codeNotAdmin, "仅超级管理员可执行此操作")
		return false
	}
	return true
}

// pathID 解析路径参数 {id}；非法按 0 处理（后续存在性检查落 404）。
func pathID(r *http.Request) int64 {
	n, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	return n
}

// logOp 管理操作审计日志（best-effort，失败不影响业务响应）。
func (m *Module) logOp(ctx context.Context, event, messageCN, raw string, fields map[string]any) {
	if m.Log == nil {
		return
	}
	_ = m.Log.Info(ctx, "admin", event, messageCN, raw, fields)
}
