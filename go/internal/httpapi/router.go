package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"

	"shade/internal/logsafe"
)

// 日志分页缺省与上限。
const (
	defaultPageSize = 20
	maxPageSize     = 100
)

// Deps 可注入依赖；nil 表示未接入，对应数据路由返回 dependency_unavailable（HTTP 503），
// /healthz 不依赖数据库、恒为 200。
type Deps struct {
	Apps      AppCatalog      // 应用目录（nil → /api/v1/app/list 503；app-logs 跳过应用存在性校验）
	Cfg       ConfigAdmin     // 运行时配置（nil → /api/v1/admin/config 503）
	Logs      LogReader       // 应用日志（nil → /api/v1/app-logs 503）
	LogFiles  LogFileSource   // 日志文件导出与 Probe（nil → export/ticket 503）
	LogHealth LogHealthSource // 日志持久层健康（nil → /api/v1/app-logs/health 503）
	Auth      Authenticator   // 登录/JWT 鉴权（nil 仅允许单元测试绕过，生产必须注入）
	Accounts  AccountStore    // 账号核心查询（nil → /user/profile、/admin/teams 503）
	Notices   NoticeReader    // 站内通知（nil → /notice/list 503）
	Events    EventRecorder   // 安全/配置/运行事件（nil 不额外记录）
}

type api struct {
	d       Deps
	health  *logHealthCache // /healthz 公开轮询用的短缓存
	tickets *logTicketStore // 一次性导出票据
}

// logHealthCacheTTL /healthz 日志健康快照短缓存（公开轮询不反复 ReadDir/stderr）。
const logHealthCacheTTL = 2 * time.Second

// logHealthCache 保存已派生告警的健康快照；过期后重新读取持久层。
type logHealthCache struct {
	mu  sync.Mutex
	ttl time.Duration
	now func() time.Time
	at  time.Time
	h   LogHealth
	ok  bool
}

func newLogHealthCache(ttl time.Duration, now func() time.Time) *logHealthCache {
	if ttl <= 0 {
		ttl = logHealthCacheTTL
	}
	if now == nil {
		now = time.Now
	}
	return &logHealthCache{ttl: ttl, now: now}
}

// get 返回缓存快照（含派生 status/alert）；过期时读取 src 并缓存。
func (c *logHealthCache) get(src LogHealthSource) LogHealth {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.now()
	if c.ok && now.Sub(c.at) < c.ttl {
		return c.h
	}
	h := src.LogHealth()
	annotateLogHealth(&h)
	c.h, c.at, c.ok = h, now, true
	return h
}

// NewRouter 构建 chi 路由（返回 http.Handler，便于挂载/测试）。
// wechatanswer 模块路由挂在 /api/v1/wechatanswer；管理入口统一 /api/v1/wechat-answer（路径别名重写）。
// /auth/login 在 authwx 模块挂载（实现 WXLoginService）时由模块统一注册（数字信封 + wx_code +
// 限流），未挂载时由核心回退纯账号密码登录（fixture / 单测路径）。
func NewRouter(d Deps, modules ...Module) http.Handler {
	a := &api{
		d:       d,
		health:  newLogHealthCache(logHealthCacheTTL, time.Now),
		tickets: newLogTicketStore(logExportTicketTTL, time.Now),
	}
	r := chi.NewRouter()
	r.Use(recoverer(d.Events))
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
			r.Post("/auth/login", loginHandler(d.Auth, d.Events))
		}
		r.Post("/auth/logout", logoutHandler(d.Auth, d.Events))
		r.Get("/app/list", a.listApps)
		r.Get("/user/profile", a.userProfile)
		r.Get("/admin/teams", a.adminTeams)
		r.Get("/notice/list", a.noticeList)
		r.Get("/admin/config", a.getConfig)
		r.Put("/admin/config", a.putConfig)
		r.Get("/app-logs", a.appLogs)
		r.Get("/app-logs/health", a.appLogHealth)
		r.Get("/app-logs/detail", a.appLogDetail)
		r.Get("/app-logs/export", a.appLogsExport)
		r.Post("/app-logs/export/ticket", a.appLogsExportTicket)
		r.Get("/app-logs/export/download", a.appLogsExportDownload)
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

// recoverer panic 兜底为 JSON internal_error，不把堆栈写进响应；同时写 runtime.panic
// 事件（panic 文本经 logsafe 脱敏，不含堆栈/请求正文）。
func recoverer(events EventRecorder) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				if rec := recover(); rec != nil {
					recordEvent(r.Context(), events, "error", "runtime", "runtime.panic",
						"请求处理发生 panic", logsafe.TextForLog(fmt.Sprint(rec)),
						map[string]any{"method": r.Method, "path": logsafe.PathForLog(r.URL.Path)})
					writeErr(w, Internal(fmt.Errorf("panic: %v", rec)))
				}
			}()
			next.ServeHTTP(w, r)
		})
	}
}

// healthz 健康检查：进程存活即 200，不探测数据库。
// 日志持久层的磁盘失败/待补录积压在此一并暴露（DB 故障时仍可观测），完整信息见
// 超管接口 /api/v1/app-logs/health。
func (a *api) healthz(w http.ResponseWriter, _ *http.Request) {
	data := map[string]any{
		"status": "ok",
		"time":   time.Now().UTC().Format(time.RFC3339),
	}
	if a.d.LogHealth != nil {
		var h LogHealth
		if a.health != nil {
			h = a.health.get(a.d.LogHealth)
		} else {
			h = a.d.LogHealth.LogHealth()
			annotateLogHealth(&h)
		}
		logs := map[string]any{
			"status":         h.Status,
			"pending":        h.Pending,
			"write_errors":   h.WriteErrors,
			"disk_fallbacks": h.DiskFallbacks,
			"fsync_errors":   h.FsyncErrors,
			"corrupt_lines":  h.CorruptLines,
			"insert_errors":  h.InsertErrors,
			"db_connected":   h.DBConnected,
		}
		if h.LastImport != "" {
			logs["last_import"] = h.LastImport
		}
		if h.Alert != "" {
			logs["alert"] = h.Alert
		}
		data["logs"] = logs
	}
	writeOK(w, data)
}

// logHealthReasons 从健康计数派生关键告警（磁盘失败/入库失败/积压/数据库未接入）。
func logHealthReasons(h LogHealth) []string {
	reasons := make([]string, 0, 5)
	if h.WriteErrors > 0 {
		reasons = append(reasons, fmt.Sprintf("日志文件写入失败 %d 次", h.WriteErrors))
	}
	if h.DiskFallbacks > 0 {
		reasons = append(reasons, fmt.Sprintf("磁盘写入失败后回退数据库直写 %d 次", h.DiskFallbacks))
	}
	if h.InsertErrors > 0 {
		reasons = append(reasons, fmt.Sprintf("日志入库失败 %d 次", h.InsertErrors))
	}
	if h.Pending >= logPendingAlertLimit {
		reasons = append(reasons, fmt.Sprintf("待补录日志积压 %d 条", h.Pending))
	}
	if !h.DBConnected {
		reasons = append(reasons, "日志数据库未接入，日志仅写入文件")
	}
	return reasons
}

// annotateLogHealth 写入派生状态与告警文本，并把关键告警同步到 stderr（30s 节流）：
// 磁盘/积压问题不因数据库故障或管理端不可达而完全不可见。
func annotateLogHealth(h *LogHealth) {
	reasons := logHealthReasons(*h)
	h.Alert = strings.Join(reasons, "；")
	h.Status = "ok"
	if h.Alert != "" {
		h.Status = "degraded"
		emitLogHealthAlert(h.Alert)
	}
}

// 健康告警 stderr 输出（串行化并按消息节流；测试可替换 writer）。
var (
	logHealthAlertMu     sync.Mutex
	logHealthAlertAt     time.Time
	logHealthAlertLast   string
	logHealthAlertWriter io.Writer = os.Stderr
)

func emitLogHealthAlert(msg string) {
	if msg == "" {
		return
	}
	logHealthAlertMu.Lock()
	defer logHealthAlertMu.Unlock()
	now := time.Now()
	if msg == logHealthAlertLast && now.Sub(logHealthAlertAt) < 30*time.Second {
		return
	}
	logHealthAlertLast, logHealthAlertAt = msg, now
	if logHealthAlertWriter == nil {
		return
	}
	fmt.Fprintln(logHealthAlertWriter, "[logging] "+msg)
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
	// 先全量校验键存在（不写库），再调用一次事务型批量写入：
	// 任一键失败整体不生效，避免逐键 Update 中途失败留下部分更新。
	updated := make([]ConfigItem, 0, len(req.Items))
	batch := make([]ConfigUpdate, 0, len(req.Items))
	index := make(map[string]int, len(req.Items))
	for _, u := range req.Items {
		key := strings.TrimSpace(u.Key)
		if key == "" {
			writeErr(w, InvalidParam("配置键不能为空"))
			return
		}
		if i, ok := index[key]; ok {
			// 同一请求重复键：保留最后一次值（同旧逐键更新语义）
			updated[i].Value = u.Value
			batch[i].Value = u.Value
			continue
		}
		cur, err := a.d.Cfg.Get(r.Context(), key)
		if err != nil {
			writeErr(w, err) // 未知键 → not_found
			return
		}
		cur.Value = u.Value
		index[key] = len(updated)
		updated = append(updated, cur)
		batch = append(batch, ConfigUpdate{Key: key, Value: u.Value})
	}
	if err := a.d.Cfg.UpdateBatch(r.Context(), batch); err != nil {
		writeErr(w, err)
		return
	}
	keys := make([]string, 0, len(updated))
	for _, it := range updated {
		keys = append(keys, it.Key)
	}
	recordEvent(r.Context(), a.d.Events, "info", "config", "config.updated",
		"运行时配置已更新", "config updated: "+strings.Join(keys, ","), map[string]any{"keys": keys})
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

// appLogDetail 单条日志完整详情：GET /api/v1/app-logs/detail?id=<id> → data:LogEntry。
// 列表只给 detail 摘要，drawer 打开时再取全文。
func (a *api) appLogDetail(w http.ResponseWriter, r *http.Request) {
	raw := strings.TrimSpace(r.URL.Query().Get("id"))
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id < 1 {
		writeErr(w, InvalidParam("id 需为正整数"))
		return
	}
	if a.d.Logs == nil {
		writeErr(w, DependencyUnavailable("日志读取未接入（数据库未配置）"))
		return
	}
	entry, err := a.d.Logs.GetLog(r.Context(), id)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeOK(w, entry)
}

// appLogHealth 日志持久层健康：GET /api/v1/app-logs/health → data:LogHealth。
// 数据来自文件持久层，不依赖数据库；数据库故障时仍可看到磁盘/积压状态。
func (a *api) appLogHealth(w http.ResponseWriter, _ *http.Request) {
	if a.d.LogHealth == nil {
		writeErr(w, DependencyUnavailable("日志健康统计未接入"))
		return
	}
	h := a.d.LogHealth.LogHealth()
	annotateLogHealth(&h)
	writeOK(w, h)
}

// logExportMarker 导出流完成/失败哨兵：成功与失败都写一行 JSON，客户端/审计可据此区分
// “完整导出”与“中途截断”，不能把 200 截断流误认为完整文件。
type logExportMarker struct {
	Kind             string `json:"__export__"` // complete / incomplete / failed
	Count            int    `json:"count"`
	FileCopyComplete bool   `json:"file_copy_complete"`
	DiskFallbacks    int64  `json:"disk_fallbacks,omitempty"`
	FsyncErrors      int64  `json:"fsync_errors,omitempty"`
	CorruptLines     int64  `json:"corrupt_lines,omitempty"`
	HalfLines        int64  `json:"half_lines,omitempty"`
	DBOnly           int64  `json:"db_only,omitempty"`
	DBUnavailable    bool   `json:"db_unavailable,omitempty"`
	IntegrityCorrupt bool   `json:"integrity_corrupt,omitempty"`
	Scope            string `json:"scope,omitempty"`
	Error            string `json:"error,omitempty"`
}

// writeNDJSONHeaders 导出响应头（裸下载通道，不套 JSON 信封）。
// 扫描前文件副本状态未知，响应头只写 unknown；最终状态在流结束时通过 trailer
// 与 NDJSON 哨兵给出，禁止在未扫描时预先声称 complete。
func writeNDJSONHeaders(w http.ResponseWriter, filename string, diskFallbacks int64) {
	if strings.TrimSpace(filename) == "" {
		filename = "app-logs-" + time.Now().Format("20060102-150405") + ".ndjson"
	}
	w.Header().Set("Content-Type", "application/x-ndjson; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("X-Accel-Buffering", "no")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, filename))
	w.Header().Add("Trailer", "X-Log-Export-Status")
	w.Header().Add("Trailer", "X-Log-Export-Count")
	w.Header().Add("Trailer", "X-Log-Export-File-Copy")
	w.Header().Set("X-Log-Export-File-Copy", "unknown")
	if diskFallbacks > 0 {
		w.Header().Set("X-Log-Export-Disk-Fallbacks", strconv.FormatInt(diskFallbacks, 10))
	}
	w.WriteHeader(http.StatusOK)
}

// setLogExportTrailer 写流结束 trailer（HTTP/1.1 chunked；同时仍有 NDJSON 哨兵兜底）。
func setLogExportTrailer(w http.ResponseWriter, status string, count int, fileCopy string) {
	w.Header().Set("X-Log-Export-Status", status)
	w.Header().Set("X-Log-Export-Count", strconv.Itoa(count))
	w.Header().Set("X-Log-Export-File-Copy", fileCopy)
}

// appLogsExportTicket 签发一次性短时导出票据：POST /api/v1/app-logs/export/ticket?<筛选>。
// 票据只供 /export/download 使用，浏览器原生下载可流式落盘，不把文件读入 JS 内存。
func (a *api) appLogsExportTicket(w http.ResponseWriter, r *http.Request) {
	if a.d.LogFiles == nil {
		writeErr(w, DependencyUnavailable("日志文件导出未接入"))
		return
	}
	f, err := parseLogFilter(r.URL.Query())
	if err != nil {
		writeErr(w, err)
		return
	}
	// 轻量 Probe：目录/所有段可读、gzip 可初始化才发票据；失败不发票据并返回真实原因。
	if err := a.d.LogFiles.Probe(r.Context()); err != nil {
		writeErr(w, err)
		return
	}
	if a.tickets == nil {
		a.tickets = newLogTicketStore(logExportTicketTTL, time.Now)
	}
	issued, err := a.tickets.issue(f)
	if err != nil {
		writeErr(w, Internal(err))
		return
	}
	data := map[string]any{
		"ticket":     issued.Token,
		"url":        issued.URL,
		"filename":   issued.Filename,
		"expires_in": issued.ExpiresIn,
	}
	if a.d.LogHealth != nil {
		h := a.d.LogHealth.LogHealth()
		if h.DiskFallbacks > 0 || h.WriteErrors > 0 || h.FsyncErrors > 0 || h.CorruptLines > 0 {
			data["file_copy_incomplete"] = true
			data["disk_fallbacks"] = h.DiskFallbacks
			data["write_errors"] = h.WriteErrors
			data["fsync_errors"] = h.FsyncErrors
			data["corrupt_lines"] = h.CorruptLines
		}
	}
	writeOK(w, data)
}

// appLogsExportDownload 凭票据下载导出流：GET /api/v1/app-logs/export/download?ticket=。
// 票据即鉴权（短时、一次性、仅导出用途），不走登录 JWT；无效/过期/已用一律 403。
func (a *api) appLogsExportDownload(w http.ResponseWriter, r *http.Request) {
	// 依赖未接入必须先于 redeem：否则一次异常会白白消耗一次性 ticket。
	if a.d.LogFiles == nil {
		writeErr(w, DependencyUnavailable("日志文件导出未接入"))
		return
	}
	token := strings.TrimSpace(r.URL.Query().Get("ticket"))
	filter, filename, ok := a.tickets.redeem(token)
	if !ok {
		writeErr(w, &APIError{Code: "forbidden", Message: "导出票据无效或已过期", Status: http.StatusForbidden})
		return
	}
	a.streamLogsExport(w, r, filter, filename, true)
}

// appLogsExport 日志导出（管理端兼容直连）：GET /api/v1/app-logs/export?<同列表筛选>。
// 浏览器优先使用一次性 ticket 下载；此入口保留给带 Authorization 的管理端脚本。
func (a *api) appLogsExport(w http.ResponseWriter, r *http.Request) {
	f, err := parseLogFilter(r.URL.Query())
	if err != nil {
		writeErr(w, err)
		return
	}
	if a.d.LogFiles == nil {
		writeErr(w, DependencyUnavailable("日志文件导出未接入"))
		return
	}
	a.streamLogsExport(w, r, f, "", false)
}

// streamLogsExport 共享的 NDJSON 流式导出：逐条过滤写出；开始输出后失败写 failed 哨兵
// + trailer，成功写 complete 哨兵 + trailer，绝不静默截断。
func (a *api) streamLogsExport(w http.ResponseWriter, r *http.Request, f LogFilter, filename string, forceSentinel bool) {
	var h LogHealth
	fileCopyKnown := false
	if a.d.LogHealth != nil {
		h = a.d.LogHealth.LogHealth()
		fileCopyKnown = true
	}
	fileCopyComplete := fileCopyKnown && h.DiskFallbacks == 0 && h.WriteErrors == 0 &&
		h.FsyncErrors == 0 && h.CorruptLines == 0

	started := false
	written := 0
	start := func() {
		if started {
			return
		}
		writeNDJSONHeaders(w, filename, h.DiskFallbacks)
		started = true
	}
	writeValue := func(v any) error {
		start()
		return json.NewEncoder(w).Encode(v)
	}
	writeEntry := func(e LogEntry) error {
		if err := writeValue(e); err != nil {
			return err
		}
		written++
		if written%200 == 0 {
			_ = http.NewResponseController(w).Flush()
		}
		return nil
	}

	if err := a.d.LogFiles.StreamLogs(r.Context(), f, writeEntry); err != nil {
		var inc *LogExportIncomplete
		if errors.As(err, &inc) {
			// 结构化不完整（坏行/半行/DB-only/历史故障）：继续输出已读条目，最后写 incomplete。
			marker := logExportMarker{
				Kind: "incomplete", Count: written, FileCopyComplete: false,
				DiskFallbacks: inc.Persisted.DiskFallbacks, FsyncErrors: inc.Persisted.FsyncErrors,
				CorruptLines: inc.CorruptLines + inc.Persisted.CorruptLines,
				HalfLines:    inc.HalfLines, DBOnly: inc.DBOnly,
				DBUnavailable: inc.DBUnavailable, IntegrityCorrupt: inc.IntegrityCorrupt,
				Scope: "file-copy", Error: logsafe.TextForLog(inc.Error()),
			}
			if mErr := writeValue(marker); mErr != nil {
				return
			}
			setLogExportTrailer(w, "incomplete", written, "incomplete")
			return
		}
		if !started && !forceSentinel {
			// 管理端直连导出尚未开始输出：仍返回可识别的 JSON 错误。
			writeErr(w, err)
			return
		}
		// 原生 ticket 下载即使首段失败也必须写 NDJSON failed 哨兵，避免浏览器把 JSON
		// 错误存成 .ndjson；已开始输出后同样以哨兵 + trailer 标记中断。
		marker := logExportMarker{
			Kind: "failed", Count: written, FileCopyComplete: false,
			DiskFallbacks: h.DiskFallbacks, FsyncErrors: h.FsyncErrors, CorruptLines: h.CorruptLines,
			Scope: "file-copy", Error: logsafe.TextForLog(err.Error()),
		}
		if mErr := writeValue(marker); mErr != nil {
			return
		}
		setLogExportTrailer(w, "failed", written, "incomplete")
		recordEvent(r.Context(), a.d.Events, "warn", "runtime", "applog.export.failed",
			"日志导出中断", logsafe.TextForLog(err.Error()), map[string]any{"written": written})
		return
	}
	kind := "complete"
	if !fileCopyComplete {
		kind = "incomplete"
	}
	marker := logExportMarker{
		Kind: kind, Count: written, FileCopyComplete: fileCopyComplete,
		DiskFallbacks: h.DiskFallbacks, FsyncErrors: h.FsyncErrors, CorruptLines: h.CorruptLines,
		Scope: "file-copy",
	}
	if err := writeValue(marker); err != nil {
		return
	}
	fileCopy := "incomplete"
	if fileCopyComplete {
		fileCopy = "complete"
	}
	setLogExportTrailer(w, kind, written, fileCopy)
}

// parseLogFilter 解析并校验日志查询参数；非法参数返回 invalid_param。
func parseLogFilter(q url.Values) (LogFilter, error) {
	var f LogFilter
	f.AppCode = strings.TrimSpace(q.Get("app_code"))
	f.Level = strings.ToLower(strings.TrimSpace(q.Get("level")))
	f.Category = strings.TrimSpace(q.Get("category"))
	f.TraceID = strings.TrimSpace(q.Get("trace_id"))
	f.Keyword = strings.TrimSpace(q.Get("keyword"))

	switch strings.ToLower(strings.TrimSpace(q.Get("view"))) {
	case "", "activity":
		f.View = "activity"
	case "access":
		f.View = "access"
	case "all":
		f.View = "all"
	default:
		return f, InvalidParam("view 只支持 activity/access/all")
	}

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

	beforeTime := strings.TrimSpace(q.Get("before_time"))
	beforeID := strings.TrimSpace(q.Get("before_id"))
	if (beforeTime == "") != (beforeID == "") {
		return f, InvalidParam("before_time 与 before_id 需同时提供")
	}
	if beforeTime != "" {
		t, err := time.Parse(time.RFC3339, beforeTime)
		if err != nil {
			return f, InvalidParam("before_time 需为 RFC3339 时间")
		}
		id, err := strconv.ParseInt(beforeID, 10, 64)
		if err != nil || id < 1 {
			return f, InvalidParam("before_id 需为正整数")
		}
		f.BeforeTime, f.BeforeID = t, id
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
