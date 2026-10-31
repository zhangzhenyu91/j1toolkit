// Package worklog 迁移 server/src/worklog：出工日志卡片（worklog_entry / entry_member / photo）CRUD、
// 按日期/月历统计（day-status/stats）、照片上传/删除与 Dify 水印验证（可注入）、车牌/目的地/成员/杆塔字典、
// 派车单号与费用标准/任务单、地理服务（高德可注入）、导出/PDF（复用 compat/exceldoc、compat/imagepdf）。
//
// 路由基路径 /api/v1/worklog；全部接口需登录 + work-log 应用权限 + 生效班组（超管可用 ?team_id= 指定）。
// 跨班日志（cross_team=1）在用车人所在班组可见可互动（entryVisibleSQL），汇总类按归属班组口径。
// 响应信封 {code,message,data} 沿用现有数字业务码。
package worklog

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"

	"shade/internal/auth"
	"shade/internal/httpapi"
)

// ConfigReader 运行时配置读取（*config.Store 满足；测试可注入桩）。
type ConfigReader interface {
	Get(ctx context.Context, key string) (string, error)
}

// AppPermit 应用权限检查（对应 Node requireApp('work-log')）；nil 时放行。
type AppPermit interface {
	Allow(ctx context.Context, userID int64, appKey string) (bool, error)
}

// AllowAllPermit 放行全部用户（本地开发/测试用）。
type AllowAllPermit struct{}

func (AllowAllPermit) Allow(context.Context, int64, string) (bool, error) { return true, nil }

// Module 出工日志模块。
type Module struct {
	DB  *sql.DB
	Cfg ConfigReader
	// Store COS 适配层；nil 时照片/附件上传返回依赖未接入。
	Store ObjectStore
	// Dify Dify 验证客户端；nil 时照片验证置 failed（不阻塞上传）。
	Dify DifyClient
	// Geo 高德地理服务；nil 时 /geo 返回空串。
	Geo GeoService
	// Notifier 人工审核通知；nil 不通知。
	Notifier Notifier
	// Dispatcher 派车单每日同步执行器；nil 时 sync-now 返回依赖未接入。
	Dispatcher DispatchSyncer
	// SgccOps 商旅照片任务锁（删卡/传图前拦截）；nil 不拦截。
	SgccOps ActivePhotoOp
	// SgccRemote 商旅费用照片远端联动（上传/调人名/删除远端先行）；nil 时照片仅本地处理。
	SgccRemote SgccPhotoRemote
	// PhotoOps 商旅照片任务队列（任务化异步执行，端侧凭 opId 轮询）；nil 时同步执行、响应不带 opId。
	PhotoOps PhotoOpStarter
	// Netdisk 保存到网盘适配层；nil 时 save-netdisk 返回依赖未接入。
	Netdisk NetdiskSaver
	Permit  AppPermit
	// DisableAsync 验证同步执行（测试确定性用；生产 false 异步执行）。
	DisableAsync bool
	// SweepInterval pending 照片清扫周期（0 取默认 10 分钟；测试可缩短）。
	SweepInterval time.Duration
	// PDFMaxRangeDays / PDFMaxPhotos / PDFMaxBytes 组合 PDF 资源预算
	// （0 取默认 92 天 / 300 张 / 512MiB；测试可下调）。
	PDFMaxRangeDays int
	PDFMaxPhotos    int
	PDFMaxBytes     int64
	// PDFTimeout 单次组合 PDF 生成总超时（0 取默认 5 分钟；测试可下调）。
	PDFTimeout time.Duration

	// pdfJobs 组合 PDF 后台任务表（内存、TTL 过期即弃，不持久化）；懒初始化。
	pdfJobMu sync.Mutex
	pdfJobs  map[string]*pdfJob
	// pdfJobSem 组合 PDF 全局任务并发预算（懒初始化；同步取件/保存与后台任务共用）。
	pdfJobSem chan struct{}

	// verifyMu 保护 verifySem / verifyInFlight 懒初始化与在飞集合。
	verifyMu       sync.Mutex
	verifySem      chan struct{}
	verifyInFlight map[int64]struct{}
}

// New 构造模块（db 必填）。
func New(db *sql.DB) *Module { return &Module{DB: db, Permit: AllowAllPermit{}} }

// EnsureSchema 建表（幂等）+ 老库补列/班组回填/唯一键升级/补索引 + 费用标准种子。
func (m *Module) EnsureSchema(ctx context.Context) error {
	for _, ddl := range schemaDDL {
		if _, err := m.DB.ExecContext(ctx, ddl); err != nil {
			return err
		}
	}
	if err := m.migrateTeamIDs(ctx); err != nil {
		return err
	}
	if err := m.ensureLegacyColumns(ctx); err != nil {
		return err
	}
	if err := m.upgradeUniqueKeys(ctx); err != nil {
		return err
	}
	if err := m.ensureSchemaIndexes(ctx); err != nil {
		return err
	}
	if _, err := m.DB.ExecContext(ctx, feeStdSeeds); err != nil {
		return err
	}
	return nil
}

// RegisterRoutes 注册 /api/v1/worklog 路由。
func (m *Module) RegisterRoutes(r chi.Router) {
	r.Route("/worklog", func(r chi.Router) {
		r.Use(m.withContext)

		// 卡片视图
		r.Get("/meta", m.handleMeta)
		r.Get("/logs", m.handleLogsList)
		r.Get("/day-status", m.handleDayStatus)
		r.Get("/stats", m.handleStats)
		r.Get("/report", m.handleReport)

		// 卡片 CRUD（跨班入口仅超管）
		r.Post("/logs", m.handleLogCreate)
		r.Get("/logs/cross/meta", m.handleCrossMeta)
		r.Post("/logs/cross", m.handleCrossCreate)
		r.Put("/logs/cross/{id}", m.handleCrossUpdate)
		r.Put("/logs/{id}", m.handleLogUpdate)
		r.Delete("/logs/{id}", m.handleLogDelete)
		r.Put("/logs/{id}/members/{mid}/check", m.handleMemberCheck)

		// 照片
		r.Post("/logs/{id}/photos", m.handlePhotoUpload)
		r.Post("/photos/{id}/verify", m.handlePhotoVerify)
		r.Put("/photos/{id}/wm", m.handlePhotoUpdateWM)
		r.Put("/photos/{id}/members", m.handlePhotoUpdateMembers)
		r.Delete("/photos/{id}", m.handlePhotoDelete)
		r.Get("/photos/{id}/download", m.handlePhotoDownload)
		r.Get("/photos", m.handlePhotoList)
		r.Post("/photos.pdf/jobs", m.handlePhotosPDFJobCreate)
		r.Get("/photos.pdf/jobs/{id}", m.handlePhotosPDFJobStatus)
		r.Get("/photos.pdf", m.handlePhotosPDF)
		r.Post("/zip", m.handleZip)
		r.Post("/photos-pdf/save-netdisk", m.handlePhotosPDFSaveNetdisk)

		// 备注附件（multipart 上传 / Office 预览 / 下载代理）
		r.Post("/logs/{id}/remark-files", m.handleRemarkUpload)
		r.Get("/logs/{id}/remark-preview", m.handleRemarkPreview)
		r.Get("/logs/{id}/remark-download", m.handleRemarkDownload)

		// 地理 / 杆塔 / 派车单
		r.Get("/geo", m.handleGeo)
		r.Get("/towers", m.handleTowers)
		r.Get("/towers/template", m.handleTowerTemplate)
		r.Post("/towers/import", m.handleTowerImport)
		r.Post("/dispatch/align", m.handleDispatchAlign)
		r.Get("/dispatch/sync-switch", m.handleSyncSwitchGet)
		r.Put("/dispatch/sync-switch", m.handleSyncSwitchPut)
		r.Post("/dispatch/sync-now", m.handleSyncNow)

		// 汇总表（任务单 / 费用汇总 / 派车汇总；班管/超管）：
		// 下载二进制（{name} 路径变体带文件名后缀，供在线预览查看器按扩展名识别格式）、
		// 在线预览地址、保存到网盘
		r.Get("/task-sheet/preview", m.handleTaskSheetPreview)
		r.Get("/fee-sheet/preview", m.handleFeeSheetPreview)
		r.Get("/dispatch-sheet/preview", m.handleDispatchSheetPreview)
		r.Post("/sheet/save-netdisk", m.handleSheetSaveNetdisk)
		r.Get("/task-sheet", m.handleTaskSheet)
		r.Get("/task-sheet/{name}", m.handleTaskSheet)
		r.Get("/fee-sheet", m.handleFeeSheet)
		r.Get("/fee-sheet/{name}", m.handleFeeSheet)
		r.Get("/dispatch-sheet", m.handleDispatchSheet)
		r.Get("/dispatch-sheet/{name}", m.handleDispatchSheet)

		// 字典管理（班管/超管）
		r.Get("/admin/vehicles", m.handleDictListVehicles)
		r.Post("/admin/vehicles", m.handleDictCreateVehicles)
		r.Put("/admin/vehicles/{id}", m.handleDictUpdateVehicles)
		r.Delete("/admin/vehicles/{id}", m.handleDictDeleteVehicles)
		r.Get("/admin/destinations", m.handleDictListDestinations)
		r.Post("/admin/destinations", m.handleDictCreateDestinations)
		r.Put("/admin/destinations/{id}", m.handleDictUpdateDestinations)
		r.Delete("/admin/destinations/{id}", m.handleDictDeleteDestinations)
		r.Get("/admin/members", m.handleDictListMembers)
		r.Post("/admin/members", m.handleDictCreateMembers)
		r.Put("/admin/members/{id}", m.handleDictUpdateMembers)
		r.Delete("/admin/members/{id}", m.handleDictDeleteMembers)
		r.Put("/admin/members/{id}/move", m.handleMemberMove)
	})
}

type moduleContextKey string

const (
	teamContextKey     moduleContextKey = "worklog.team"
	identityContextKey moduleContextKey = "worklog.identity"
)

var errNoTeam = errors.New("未分配班组")

// entryVisibleSQL 卡片可见性 SQL 片段（别名 e）：本班卡 + 用车人含本班成员的跨班日志；
// 参数按顺序传两份 team.id。汇总类功能不用此片段（跨班仅参与归属班组）。
const entryVisibleSQL = `(e.team_id = ? OR (e.cross_team = 1 AND e.id IN (
  SELECT em.entry_id FROM worklog_entry_member em JOIN worklog_member m ON m.id = em.member_id
  WHERE m.team_id = ?)))`

func teamFrom(ctx context.Context) (httpapi.Team, bool) {
	t, ok := ctx.Value(teamContextKey).(httpapi.Team)
	return t, ok
}

func identityFrom(ctx context.Context) (auth.Identity, bool) {
	v, ok := ctx.Value(identityContextKey).(auth.Identity)
	return v, ok
}

// enter 请求入口：身份 → 应用权限 → 生效班组；失败已写响应。
// 路由中间件 withContext 已完成同一解析时直接复用，避免每个请求重复查 SysTeam 与权限。
func (m *Module) enter(w http.ResponseWriter, r *http.Request) (auth.Identity, *httpapi.Team, bool) {
	if ident, ok := identityFrom(r.Context()); ok {
		if team, ok := teamFrom(r.Context()); ok {
			return ident, &team, true
		}
	}
	ident, ok := httpapi.IdentityFromContext(r.Context())
	if !ok {
		fail(w, http.StatusUnauthorized, codeUnauthorized, "未登录或登录已过期")
		return ident, nil, false
	}
	if m.Permit != nil {
		allowed, err := m.Permit.Allow(r.Context(), ident.ID, "work-log")
		if err != nil {
			writeErr(w, err)
			return ident, nil, false
		}
		if !allowed {
			fail(w, http.StatusForbidden, codeAppDenied, "暂无该应用的使用权限，请联系管理员开通")
			return ident, nil, false
		}
	}
	team, err := m.resolveTeam(r, ident)
	if err != nil {
		if errors.Is(err, errNoTeam) {
			return ident, nil, true // 无班组由各路由按空态/40310 处理
		}
		writeErr(w, err)
		return ident, nil, false
	}
	return ident, &team, true
}

// withContext 中间件：身份 → 应用权限 → 生效班组（超管可用 ?team_id= 指定）。
func (m *Module) withContext(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ident, ok := httpapi.IdentityFromContext(r.Context())
		if !ok {
			fail(w, http.StatusUnauthorized, codeUnauthorized, "未登录或登录已过期")
			return
		}
		if m.Permit != nil {
			allowed, err := m.Permit.Allow(r.Context(), ident.ID, "work-log")
			if err != nil {
				writeErr(w, err)
				return
			}
			if !allowed {
				fail(w, http.StatusForbidden, codeAppDenied, "暂无该应用的使用权限，请联系管理员开通")
				return
			}
		}
		team, err := m.resolveTeam(r, ident)
		ctx := context.WithValue(r.Context(), identityContextKey, ident)
		if err != nil {
			if !errors.Is(err, errNoTeam) {
				writeErr(w, err)
				return
			}
			next.ServeHTTP(w, r.WithContext(ctx))
			return
		}
		ctx = context.WithValue(ctx, teamContextKey, team)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// resolveTeam 解析生效班组：超管可用 query/body 的 team_id 指定任意启用班组；其余角色固定本班。
func (m *Module) resolveTeam(r *http.Request, ident auth.Identity) (httpapi.Team, error) {
	wanted := wantedTeamID(r)
	t, err := httpapi.ResolveTeam(r.Context(), m.DB,
		httpapi.IdentityAdapter{TeamID: ident.TeamID, Role: ident.Role}, wanted)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) || strings.Contains(err.Error(), "未分配班组") {
			return httpapi.Team{}, errNoTeam
		}
		return httpapi.Team{}, err
	}
	return t, nil
}

// requireDictAdmin 字典管理门控：超管任意班组；班组管理员仅本班。
func requireDictAdmin(w http.ResponseWriter, ident auth.Identity, team *httpapi.Team) bool {
	if ident.Role == "admin" {
		return true
	}
	if ident.Role == "team_admin" && team != nil && ident.TeamID == team.ID {
		return true
	}
	fail(w, http.StatusForbidden, codeForbidden, "仅管理员可执行此操作")
	return false
}

// wantedTeamID 从 query 优先、JSON body 次之读取 team_id（body 读取后复原）。
func wantedTeamID(r *http.Request) int64 {
	if v := strings.TrimSpace(r.URL.Query().Get("team_id")); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil && n > 0 {
			return n
		}
	}
	ct := r.Header.Get("Content-Type")
	if r.Body == nil || !strings.HasPrefix(ct, "application/json") {
		return 0
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		return 0
	}
	_ = r.Body.Close()
	r.Body = io.NopCloser(strings.NewReader(string(raw)))
	var body map[string]json.RawMessage
	if json.Unmarshal(raw, &body) != nil {
		return 0
	}
	return rawNumberOr0(body["team_id"])
}

// photoURLBase 对象访问前缀：每个请求只解析一次 PublicURL（该调用每次都读运行时配置），
// 批量场景先取前缀再拼接，避免逐张照片/附件重复解析。
type photoURLBase string

// urlBase 解析当前 COS 访问前缀；未接对象存储或未配置时为空串（回退库内历史地址）。
func (m *Module) urlBase() photoURLBase {
	if m.Store == nil {
		return ""
	}
	return photoURLBase(m.Store.PublicURL(""))
}

// join 按前缀拼访问地址；前缀为空或对象键为空时回退库内历史 url（域名变更前的旧数据）。
func (b photoURLBase) join(cosKey, url string) string {
	if cosKey == "" || b == "" {
		return url
	}
	return string(b) + cosKey
}

// photoURL 单点场景访问地址（批量场景先取 urlBase 复用，避免逐条解析）。
func (m *Module) photoURL(cosKey, url string) string {
	return m.urlBase().join(cosKey, url)
}

// configValue 读运行时配置；未配置返回空串。
func (m *Module) configValue(ctx context.Context, key string) string {
	if m.Cfg == nil {
		return ""
	}
	v, err := m.Cfg.Get(ctx, key)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(v)
}

// saveToNetdisk 保存到网盘（适配层未接入时返回依赖未接入错误，不假成功）。
func (m *Module) saveToNetdisk(ctx context.Context, ident auth.Identity, req NetdiskSaveRequest) (string, error) {
	if m.Netdisk == nil {
		return "", &NetdiskSaveError{Status: http.StatusServiceUnavailable, Code: codeNotConfigured,
			Message: "网盘保存服务未接入"}
	}
	return m.Netdisk.SaveToNetdisk(ctx, ident, req)
}

// writeNetdiskErr 网盘保存失败出口：业务错误随信封原样下发，其余按上游失败处理。
func writeNetdiskErr(w http.ResponseWriter, err error) {
	var nerr *NetdiskSaveError
	if errors.As(err, &nerr) {
		fail(w, nerr.Status, nerr.Code, nerr.Message)
		return
	}
	fail(w, http.StatusBadGateway, codeUpstream, err.Error())
}
