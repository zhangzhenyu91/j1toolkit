package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
)

// 日志分页缺省与上限。
const (
	defaultPageSize = 20
	maxPageSize     = 100
)

// Deps 可注入依赖；nil 表示未接入，对应数据路由返回 dependency_unavailable（HTTP 503），
// /healthz 不依赖数据库、恒为 200。
type Deps struct {
	Apps     AppCatalog    // 应用目录（nil → /api/v1/app/list 503；app-logs 跳过应用存在性校验）
	Cfg      ConfigAdmin   // 运行时配置（nil → /api/v1/admin/config 503）
	Logs     LogReader     // 应用日志（nil → /api/v1/app-logs 503）
	Auth     Authenticator // 登录/JWT 鉴权（nil 仅允许单元测试绕过，生产必须注入）
	Accounts AccountStore  // 账号核心查询（nil → /user/profile、/admin/teams 503）
	Notices  NoticeReader  // 站内通知（nil → /notice/list 503）
}

type api struct {
	d Deps
}

// NewRouter 构建 chi 路由（返回 http.Handler，便于挂载/测试）。
// wechatanswer 模块路由挂在 /api/v1/wechatanswer；管理入口统一 /api/v1/wechat-answer（路径别名重写）。
// /auth/login 在 authwx 模块挂载（实现 WXLoginService）时由模块统一注册（数字信封 + wx_code +
// 限流），未挂载时由核心回退纯账号密码登录（fixture / 单测路径）。
func NewRouter(d Deps, modules ...Module) http.Handler {
	a := &api{d: d}
	r := chi.NewRouter()
	r.Use(recoverer)
	r.Get("/healthz", a.healthz)
	r.Route("/api/v1", func(r chi.Router) {
		loginOwned := false
		for _, module := range modules {
			if _, ok := module.(WXLoginService); ok {
				loginOwned = true
				break
			}
		}
		if !loginOwned {
			r.Post("/auth/login", loginHandler(d.Auth))
		}
		r.Post("/auth/logout", logoutHandler(d.Auth))
		r.Get("/app/list", a.listApps)
		r.Get("/user/profile", a.userProfile)
		r.Get("/admin/teams", a.adminTeams)
		r.Get("/notice/list", a.noticeList)
		r.Get("/admin/config", a.getConfig)
		r.Put("/admin/config", a.putConfig)
		r.Get("/app-logs", a.appLogs)
		for _, module := range modules {
			module.RegisterRoutes(r)
		}
	})
	return AuthMiddleware(wechatAnswerAlias(r), d.Auth)
}

// wechatAnswerAlias 把管理路径 /api/v1/wechat-answer/* 重写到 wechatanswer 模块路由（统一管理入口）。
// 重写发生在鉴权之后，isAdminPath 仍按原始路径 /api/v1/wechat-answer/ 判定超管门控。
func wechatAnswerAlias(next http.Handler) http.Handler {
	const prefix = "/api/v1/wechat-answer"
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == prefix || strings.HasPrefix(r.URL.Path, prefix+"/") {
			r2 := r.Clone(r.Context())
			r2.URL.Path = "/api/v1/wechatanswer" + strings.TrimPrefix(r.URL.Path, prefix)
			next.ServeHTTP(w, r2)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// recoverer panic 兜底为 JSON internal_error，不把堆栈写进响应。
func recoverer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				writeErr(w, Internal(fmt.Errorf("panic: %v", rec)))
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// healthz 健康检查：进程存活即 200，不探测数据库。
func (a *api) healthz(w http.ResponseWriter, _ *http.Request) {
	writeOK(w, map[string]any{
		"status": "ok",
		"time":   time.Now().UTC().Format(time.RFC3339),
	})
}

// listApps 应用列表：GET /api/v1/app/list → data:{apps:[...]}
// 按登录用户权限过滤（sys_user_app）；admin 追加管理应用 wechat-answer / app-log。
func (a *api) listApps(w http.ResponseWriter, r *http.Request) {
	if a.d.Apps == nil {
		writeErr(w, DependencyUnavailable("应用目录未接入（数据库未配置）"))
		return
	}
	var apps []App
	var err error
	if uc, ok := a.d.Apps.(UserAppCatalog); ok {
		var userID int64
		role := ""
		if ident, found := IdentityFromContext(r.Context()); found {
			userID, role = ident.ID, ident.Role
		}
		apps, err = uc.ListAppsForUser(r.Context(), userID, role)
	} else {
		apps, err = a.d.Apps.ListApps(r.Context())
	}
	if err != nil {
		writeErr(w, err)
		return
	}
	if apps == nil {
		apps = []App{}
	}
	for i := range apps {
		if apps[i].AppKey == "" {
			apps[i].AppKey = apps[i].Code
		}
	}
	writeOK(w, map[string]any{"apps": apps, "list": apps})
}

// getConfig 配置读取：GET /api/v1/admin/config?key=&group= → data:{items:[...]}
// 单键查询键不存在返回 not_found；敏感值输出前脱敏。
func (a *api) getConfig(w http.ResponseWriter, r *http.Request) {
	if a.d.Cfg == nil {
		writeErr(w, DependencyUnavailable("配置存储未接入（数据库未配置）"))
		return
	}
	q := r.URL.Query()
	key := strings.TrimSpace(q.Get("key"))
	group := strings.TrimSpace(q.Get("group"))

	var items []ConfigItem
	var err error
	if key != "" {
		var it ConfigItem
		it, err = a.d.Cfg.Get(r.Context(), key)
		if err == nil {
			items = []ConfigItem{it}
		}
	} else {
		items, err = a.d.Cfg.List(r.Context())
		if err == nil && group != "" {
			filtered := make([]ConfigItem, 0, len(items))
			for _, it := range items {
				if it.Group == group {
					filtered = append(filtered, it)
				}
			}
			items = filtered
		}
	}
	if err != nil {
		writeErr(w, err)
		return
	}
	writeOK(w, map[string]any{"items": maskItems(items)})
}

// configUpdateItem PUT /api/v1/admin/config 的单条更新。
type configUpdateItem struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

// configUpdateReq PUT /api/v1/admin/config 请求体 {items:[{key,value}]}。
type configUpdateReq struct {
	Items []configUpdateItem `json:"items"`
}

// putConfig 配置更新：PUT /api/v1/admin/config {items:[{key,value}]} → data:{items:[...]}
// 逐项校验：键不能为空、键必须已存在（未知键 not_found），任一失败整体不更新；
// 响应条目为更新后形状，敏感值脱敏输出。value 为空串表示清空该配置。
func (a *api) putConfig(w http.ResponseWriter, r *http.Request) {
	if a.d.Cfg == nil {
		writeErr(w, DependencyUnavailable("配置存储未接入（数据库未配置）"))
		return
	}
	var req configUpdateReq
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		writeErr(w, InvalidParam("请求体不是合法的配置更新 JSON: "+err.Error()))
		return
	}
	if len(req.Items) == 0 {
		writeErr(w, InvalidParam("items 不能为空"))
		return
	}
	updated := make([]ConfigItem, 0, len(req.Items))
	for _, u := range req.Items {
		key := strings.TrimSpace(u.Key)
		if key == "" {
			writeErr(w, InvalidParam("配置键不能为空"))
			return
		}
		cur, err := a.d.Cfg.Get(r.Context(), key)
		if err != nil {
			writeErr(w, err) // 未知键 → not_found
			return
		}
		if err := a.d.Cfg.Update(r.Context(), key, u.Value); err != nil {
			writeErr(w, err)
			return
		}
		cur.Value = u.Value
		updated = append(updated, cur)
	}
	writeOK(w, map[string]any{"items": maskItems(updated)})
}

// appLogs 应用日志：GET /api/v1/app-logs?app_code=&level=&trace_id=&keyword=&since=&until=&page=&page_size=
// → data:{items,total,page,page_size}。应用目录可用时校验 app_code 存在性（未知应用 app_not_found）。
func (a *api) appLogs(w http.ResponseWriter, r *http.Request) {
	f, err := parseLogFilter(r.URL.Query())
	if err != nil {
		writeErr(w, err)
		return
	}
	if a.d.Logs == nil {
		writeErr(w, DependencyUnavailable("日志读取未接入（数据库未配置）"))
		return
	}
	if f.AppCode != "" && a.d.Apps != nil {
		apps, err := a.d.Apps.ListApps(r.Context())
		if err != nil {
			writeErr(w, err)
			return
		}
		found := f.AppCode == "system" || f.AppCode == "office" || f.AppCode == "chat-log" || f.AppCode == "sgcc-clockin"
		for _, app := range apps {
			if app.Code == f.AppCode {
				found = true
				break
			}
		}
		if !found {
			writeErr(w, AppNotFound("应用不存在: "+f.AppCode))
			return
		}
	}
	page, err := a.d.Logs.ListLogs(r.Context(), f)
	if err != nil {
		writeErr(w, err)
		return
	}
	if page.Items == nil {
		page.Items = []LogEntry{}
	}
	page.Page, page.PageSize = f.Page, f.PageSize
	writeOK(w, page)
}

// parseLogFilter 解析并校验日志查询参数；非法参数返回 invalid_param。
func parseLogFilter(q url.Values) (LogFilter, error) {
	var f LogFilter
	f.AppCode = strings.TrimSpace(q.Get("app_code"))
	f.Level = strings.ToLower(strings.TrimSpace(q.Get("level")))
	f.Category = strings.TrimSpace(q.Get("category"))
	f.TraceID = strings.TrimSpace(q.Get("trace_id"))
	f.Keyword = strings.TrimSpace(q.Get("keyword"))

	switch f.Level {
	case "", "debug", "info", "warn", "error":
	default:
		return f, InvalidParam("level 只支持 debug/info/warn/error")
	}

	if v := strings.TrimSpace(q.Get("since")); v != "" {
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			return f, InvalidParam("since 需为 RFC3339 时间")
		}
		f.Since = t
	}
	if v := strings.TrimSpace(q.Get("until")); v != "" {
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			return f, InvalidParam("until 需为 RFC3339 时间")
		}
		f.Until = t
	}
	if !f.Since.IsZero() && !f.Until.IsZero() && f.Until.Before(f.Since) {
		return f, InvalidParam("until 不能早于 since")
	}

	f.Page, f.PageSize = 1, defaultPageSize
	if v := strings.TrimSpace(q.Get("page")); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			return f, InvalidParam("page 需为不小于 1 的整数")
		}
		f.Page = n
	}
	if v := strings.TrimSpace(q.Get("page_size")); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > maxPageSize {
			return f, InvalidParam("page_size 需为 1~100 的整数")
		}
		f.PageSize = n
	}
	return f, nil
}
