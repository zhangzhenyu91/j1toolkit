// Package treehazard 迁移 server/src/treehazard：树障隐患记录（treehazard_record）、处理前后/辅证照片
// （COS 适配层可注入）、看板 dashboard、记录列表（运维线路分组）、crane-answer 直推 ingest（含跳过计数
// 与 deletes 撤回）、统计表导出、垂直距离阈值设置、删除墓碑与暂存区。
//
// 路由基路径 /api/v1/treehazard；POST /ingest 凭 X-Ingest-Token 免登录（需主程序把该路径放行到
// 鉴权中间件之外，见 httpapi.publicAuthPath），其余接口需登录 + tree-hazard 应用权限 + 生效班组，
// 暂存区/设置修改/删记录/手动新增与编辑限 超管 或 本班 team_admin。响应信封 {code,message,data} 沿用现有数字业务码。
package treehazard

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
	"shade/internal/config"
	"shade/internal/httpapi"
)

// 编译期契约：*config.Store 满足 ConfigReader（配置一律从运行时配置读取）。
var _ ConfigReader = (*config.Store)(nil)

// 现有业务码（与 server/src/utils/resp.js 及各路由 fail 口径一致）。
const (
	codeOK            = 0
	codeParam         = 40001
	codePhotoFormat   = 40010
	codePhotoSize     = 40011
	codeFile          = 40022
	codeUnauthorized  = 40101
	codeIngestToken   = 40104
	codeForbidden     = 40304
	codeNoTeam        = 40310
	codeNotFound      = 40400
	codeNotConfigured = 50301
)

// Envelope 统一响应信封（成功 code=0；失败为数字业务码）。
type Envelope struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

func writeOK(w http.ResponseWriter, data any, message string) {
	if message == "" {
		message = "ok"
	}
	writeJSON(w, http.StatusOK, Envelope{Code: codeOK, Message: message, Data: data})
}

func writeFail(w http.ResponseWriter, status, code int, message string, data any) {
	writeJSON(w, status, Envelope{Code: code, Message: message, Data: data})
}

func writeJSON(w http.ResponseWriter, status int, env Envelope) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(env)
}

// ConfigReader 运行时配置读取（*config.Store 满足；测试可注入桩）。
type ConfigReader interface {
	Get(ctx context.Context, key string) (string, error)
}

// AppPermit 应用权限检查（对应 Node requireApp('tree-hazard')）；nil 时放行（保留可接入点）。
type AppPermit interface {
	Allow(ctx context.Context, userID int64, appKey string) (bool, error)
}

// AllowAllPermit 放行全部用户（本地开发/测试用）。
type AllowAllPermit struct{}

func (AllowAllPermit) Allow(context.Context, int64, string) (bool, error) { return true, nil }

// Module 树障隐患模块。
type Module struct {
	DB *sql.DB
	// Cfg 运行时配置（ingest 密钥 TREEHAZARD_INGEST_TOKEN、COS_TREEHAZARD_PREFIX 等）；nil 时报未配置。
	Cfg ConfigReader
	// Objects 对象存储适配层（COS）；nil 时照片相关接口返回依赖未接入，ingest 只计 photos_failed。
	Objects ObjectStore
	// Files 随批 xlsx 落盘；nil 时不落盘（xlsx_path 留空）。
	Files BatchFileStore
	// Permit 应用权限检查；nil 放行。
	Permit AppPermit
	// WeKnora WeKnora 知识库 API（可注入，测试用假实现）；nil 时按 WEKNORA_* 配置走内置 HTTP 客户端。
	WeKnora WeKnoraClient
	// WeKnoraState 知识库同步状态存储；nil 时用 data/treehazard/weknora-sync-state.json。
	WeKnoraState WeKnoraStateStore
	// Usb 内网 U 盘同步设备链路（可注入，见 UsbLink）；nil 时同步明确报链路未接入。
	Usb UsbLink
	// SyncNotifier 同步结果站内通知（可注入）；nil 跳过通知不影响同步。
	SyncNotifier SyncNotifier

	syncMu         sync.Mutex
	syncRunning    bool // 内网 U 盘同步轮次重入守卫
	weknoraRunning bool // WeKnora 同步轮次重入守卫
}

// New 构造模块（db 必填）。
func New(db *sql.DB) *Module { return &Module{DB: db, Permit: AllowAllPermit{}} }

// EnsureSchema 建表（幂等），含老库兼容补列（review_note / staging_record_id）。
func (m *Module) EnsureSchema(ctx context.Context) error {
	for _, ddl := range schemaDDL {
		if _, err := m.DB.ExecContext(ctx, ddl); err != nil {
			return err
		}
	}
	for _, compat := range schemaCompatColumns {
		var found string
		err := m.DB.QueryRowContext(ctx,
			`SELECT COLUMN_NAME FROM information_schema.COLUMNS
			 WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = ? AND COLUMN_NAME = ?`,
			compat.table, compat.column).Scan(&found)
		if err != nil {
			if !errors.Is(err, sql.ErrNoRows) {
				return err
			}
			if _, err := m.DB.ExecContext(ctx, compat.alterSQL); err != nil {
				return err
			}
		}
	}
	return nil
}

// RegisterRoutes 注册 /api/v1/treehazard 路由。
func (m *Module) RegisterRoutes(r chi.Router) {
	r.Route("/treehazard", func(r chi.Router) {
		// crane-answer 直推：不走 JWT，凭 X-Ingest-Token（主程序需放行该路径免登录）。
		r.Post("/ingest", m.handleIngest)

		r.Group(func(r chi.Router) {
			r.Use(m.withContext)
			r.Get("/dashboard", m.handleDashboard)
			r.Get("/records", m.handleRecords)
			r.Get("/records/{id}", m.handleRecordGet)
			r.Post("/records", m.handleRecordCreate)
			r.Put("/records/{id}", m.handleRecordUpdate)
			r.Post("/records/{id}/handle", m.handleRecordHandle)
			r.Post("/records/handle-batch", m.handleRecordHandleBatch)
			r.Post("/records/{id}/photos", m.handlePhotoUpload)
			r.Delete("/records/{id}/photos/{photoId}", m.handlePhotoDelete)
			r.Delete("/records/{id}", m.handleRecordDelete)
			r.Get("/settings", m.handleSettingsGet)
			r.Put("/settings", m.handleSettingsPut)
			r.Get("/export/preview", m.handleExportPreview)
			r.Get("/export", m.handleExport)
			r.Get("/export/{name}", m.handleExport)

			// 暂存区（班管/超管）。
			r.Get("/staging/batches", m.handleStagingBatches)
			r.Get("/staging/batches/{id}", m.handleStagingBatchGet)
			r.Put("/staging/records/{id}", m.handleStagingRecordUpdate)
			r.Post("/staging/batches/{id}/import", m.handleStagingImport)
			r.Delete("/staging/batches/{id}", m.handleStagingDelete)

			// 冲突（班管/超管；内网同步产生，人工二选一解决）。
			r.Get("/conflicts", m.handleConflicts)
			r.Post("/conflicts/{id}/resolve", m.handleConflictResolve)

			// 内网 U 盘同步（编排见 sync.go）：手动触发 + 状态查询。
			r.Post("/sync/push-now", m.handleSyncPushNow)
			r.Get("/sync/status", m.handleSyncStatus)

			// WeKnora 知识库同步（编排见 weknora.go）：手动触发。
			r.Post("/weknora-sync/push-now", m.handleWeKnoraPushNow)
		})
	})
}

type moduleContextKey string

const (
	teamContextKey     moduleContextKey = "treehazard.team"
	identityContextKey moduleContextKey = "treehazard.identity"
)

var errNoTeam = errors.New("未分配班组")

func teamFrom(ctx context.Context) (httpapi.Team, bool) {
	t, ok := ctx.Value(teamContextKey).(httpapi.Team)
	return t, ok
}

func identityFrom(ctx context.Context) (auth.Identity, bool) {
	v, ok := ctx.Value(identityContextKey).(auth.Identity)
	return v, ok
}

// withContext 中间件：身份 → 应用权限 → 生效班组（超管可用 ?team_id= 指定）。
func (m *Module) withContext(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ident, ok := httpapi.IdentityFromContext(r.Context())
		if !ok {
			writeFail(w, http.StatusUnauthorized, codeUnauthorized, "未登录或登录已过期", nil)
			return
		}
		if m.Permit != nil {
			allowed, err := m.Permit.Allow(r.Context(), ident.ID, "tree-hazard")
			if err != nil {
				writeFail(w, http.StatusInternalServerError, codeParam, "应用权限校验失败", nil)
				return
			}
			if !allowed {
				writeFail(w, http.StatusForbidden, 40301, "暂无该应用的使用权限，请联系管理员开通", nil)
				return
			}
		}
		team, err := m.resolveTeam(r, ident)
		if err != nil {
			if errors.Is(err, errNoTeam) {
				// 无班组由各路由按空态/40310 处理，此处置零值班组继续。
				next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), identityContextKey, ident)))
				return
			}
			writeFail(w, http.StatusInternalServerError, codeParam, "班组解析失败", nil)
			return
		}
		ctx := context.WithValue(r.Context(), teamContextKey, team)
		ctx = context.WithValue(ctx, identityContextKey, ident)
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

// requireManager 超管任意班组；班组管理员仅本班。
func requireManager(ident auth.Identity, team httpapi.Team) bool {
	return httpapi.RequireTeamAdmin(httpapi.IdentityAdapter{TeamID: ident.TeamID, Role: ident.Role}, team)
}

// requireManagerNow 当前请求的管理门控（暂存区/设置修改/删记录/手动新增与编辑）。
func requireManagerNow(w http.ResponseWriter, r *http.Request) (httpapi.Team, auth.Identity, bool) {
	team, ok := teamFrom(r.Context())
	ident, _ := identityFrom(r.Context())
	if !ok || team.ID == 0 {
		writeFail(w, http.StatusBadRequest, 40010, "无可用班组", nil)
		return httpapi.Team{}, ident, false
	}
	if !requireManager(ident, team) {
		writeFail(w, http.StatusForbidden, codeForbidden, "仅管理员可执行此操作", nil)
		return httpapi.Team{}, ident, false
	}
	return team, ident, true
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
	var probe struct {
		TeamID any `json:"team_id"`
	}
	if json.Unmarshal(raw, &probe) == nil && probe.TeamID != nil {
		switch v := probe.TeamID.(type) {
		case float64:
			if v > 0 {
				return int64(v)
			}
		case string:
			if n, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64); err == nil && n > 0 {
				return n
			}
		}
	}
	return 0
}

// decodeJSON 解析请求体（未知字段忽略）。
func decodeJSON(r *http.Request, target any) error {
	return json.NewDecoder(r.Body).Decode(target)
}

// pathID 读取路径参数 id。
func pathID(r *http.Request) (int64, bool) {
	n, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil || n <= 0 {
		return 0, false
	}
	return n, true
}

// configValue 读运行时配置；未配置/未接入返回空串。
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

// 对接密钥校验见 ingest.go 的 verifyIngestToken（HTTP 与进程内入口共用）。

// weekStartText 北京时间本周一 00:00 的库内墙钟文本（库内 created_at 随容器为 UTC 墙钟，北京周界减 8h 比较）。
func weekStartText(now time.Time) string {
	cn := now.UTC().Add(8 * time.Hour) // UTC 字段即北京墙钟
	dow := (int(cn.Weekday()) + 6) % 7 // 周一 = 0
	monday := time.Date(cn.Year(), cn.Month(), cn.Day()-dow, 0, 0, 0, 0, time.UTC).Add(-8 * time.Hour)
	return monday.Format("2006-01-02 15:04:05")
}

// schemaDDL 建表语句（与 server/src/treehazard/schema.js 一致）。
var schemaDDL = []string{
	`CREATE TABLE IF NOT EXISTS treehazard_record (
    id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
    team_id BIGINT UNSIGNED NOT NULL COMMENT '所属班组，关联 sys_team.id',
    voltage_level VARCHAR(16) NOT NULL DEFAULT '' COMMENT '电压等级（数值文本，展示时拼 kV）',
    line_name VARCHAR(64) NOT NULL DEFAULT '' COMMENT '线路名称',
    section VARCHAR(32) NOT NULL DEFAULT '' COMMENT '区段展示文本（如 3#-4#）',
    pole_from INT NULL COMMENT '起始杆号（小号侧）',
    pole_to INT NULL COMMENT '终止杆号（大号侧）',
    position_note VARCHAR(32) NOT NULL DEFAULT '' COMMENT '位置备注',
    tree_species VARCHAR(32) NOT NULL DEFAULT '' COMMENT '树种',
    tree_count VARCHAR(16) NOT NULL DEFAULT '' COMMENT '数量',
    dist_small VARCHAR(16) NOT NULL DEFAULT '' COMMENT '距小号侧距离（m）',
    dist_big VARCHAR(16) NOT NULL DEFAULT '' COMMENT '距大号侧距离（m）',
    diameter VARCHAR(16) NOT NULL DEFAULT '' COMMENT '直径（cm）',
    horizontal VARCHAR(16) NOT NULL DEFAULT '' COMMENT '水平距离（m）',
    vertical VARCHAR(16) NOT NULL DEFAULT '' COMMENT '垂直距离原文',
    vertical_num DECIMAL(6,2) NULL COMMENT '垂直距离解析值（取首个数字，供阈值比对）',
    finder_name VARCHAR(32) NOT NULL DEFAULT '' COMMENT '发现人',
    found_at DATE NULL COMMENT '发现时间',
    handler_name VARCHAR(32) NOT NULL DEFAULT '' COMMENT '处理人',
    handle_note VARCHAR(64) NOT NULL DEFAULT '' COMMENT '处理情况',
    handled_at DATE NULL COMMENT '处理时间',
    status VARCHAR(16) NOT NULL DEFAULT 'unhandled' COMMENT '状态：unhandled 未处理 / handled 已处理',
    source VARCHAR(16) NOT NULL DEFAULT 'crane' COMMENT '来源：crane 直推 / staging 暂存区导入 / intranet 内网回写 / manual 手动录入',
    crane_session_id VARCHAR(64) NULL COMMENT 'crane-answer 排查会话 id（幂等键，空存 NULL）',
    crane_record_id VARCHAR(64) NULL COMMENT 'crane-answer 记录 id（幂等键，空存 NULL）',
    synced_at DATETIME NULL COMMENT '上次与内网同步时刻（NULL=从未同步，差异比对基准）',
    created_by BIGINT UNSIGNED NULL COMMENT '创建人，关联 sys_user.id（直推为 NULL）',
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    KEY idx_team_status (team_id, status),
    KEY idx_team_line (team_id, line_name),
    UNIQUE KEY uk_crane (team_id, crane_session_id, crane_record_id)
  ) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,
	`CREATE TABLE IF NOT EXISTS treehazard_photo (
    id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
    record_id BIGINT UNSIGNED NOT NULL COMMENT '关联 treehazard_record.id',
    kind VARCHAR(16) NOT NULL COMMENT '类型：evidence 证据（预留）/ before 处理前 / after 处理后',
    cos_key VARCHAR(255) NOT NULL DEFAULT '' COMMENT 'COS 对象键（COS_TREEHAZARD_PREFIX 前缀）',
    url VARCHAR(512) NOT NULL DEFAULT '' COMMENT '访问地址',
    uploaded_by BIGINT UNSIGNED NULL COMMENT '上传人，关联 sys_user.id',
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    KEY idx_record (record_id)
  ) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,
	`CREATE TABLE IF NOT EXISTS treehazard_staging_batch (
    id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
    team_id BIGINT UNSIGNED NOT NULL COMMENT '所属班组，关联 sys_team.id',
    crane_session_id VARCHAR(64) NOT NULL DEFAULT '' COMMENT 'crane-answer 排查会话 id（班组内唯一）',
    group_name VARCHAR(64) NOT NULL DEFAULT '' COMMENT '微信群名称',
    pusher_name VARCHAR(32) NOT NULL DEFAULT '' COMMENT '推送人昵称',
    review_note VARCHAR(255) NULL COMMENT '审核意见（审核不通过时的修改说明，展示给暂存区修改人）',
    xlsx_path VARCHAR(255) NOT NULL DEFAULT '' COMMENT '随批 xlsx 落盘路径',
    status VARCHAR(16) NOT NULL DEFAULT 'pending' COMMENT '状态：pending 待导入 / imported 已导入 / discarded 已废弃',
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE KEY uk_team_session (team_id, crane_session_id)
  ) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,
	`CREATE TABLE IF NOT EXISTS treehazard_staging_record (
    id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
    batch_id BIGINT UNSIGNED NOT NULL COMMENT '关联 treehazard_staging_batch.id',
    crane_record_id VARCHAR(64) NULL COMMENT 'crane-answer 记录 id（导入时随批次写入主表幂等键，空存 NULL）',
    voltage_level VARCHAR(16) NOT NULL DEFAULT '' COMMENT '电压等级',
    line_name VARCHAR(64) NOT NULL DEFAULT '' COMMENT '线路名称',
    section VARCHAR(32) NOT NULL DEFAULT '' COMMENT '区段展示文本',
    tree_species VARCHAR(32) NOT NULL DEFAULT '' COMMENT '树种',
    tree_count VARCHAR(16) NOT NULL DEFAULT '' COMMENT '数量',
    dist_small VARCHAR(16) NOT NULL DEFAULT '' COMMENT '距小号侧距离（m）',
    dist_big VARCHAR(16) NOT NULL DEFAULT '' COMMENT '距大号侧距离（m）',
    diameter VARCHAR(16) NOT NULL DEFAULT '' COMMENT '直径（cm）',
    horizontal VARCHAR(16) NOT NULL DEFAULT '' COMMENT '水平距离（m）',
    vertical VARCHAR(16) NOT NULL DEFAULT '' COMMENT '垂直距离原文',
    vertical_num DECIMAL(6,2) NULL COMMENT '垂直距离解析值',
    finder_name VARCHAR(32) NOT NULL DEFAULT '' COMMENT '发现人',
    found_at DATE NULL COMMENT '发现时间',
    handler_name VARCHAR(32) NOT NULL DEFAULT '' COMMENT '处理人',
    handle_note VARCHAR(64) NOT NULL DEFAULT '' COMMENT '处理情况',
    handled_at DATE NULL COMMENT '处理时间',
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    KEY idx_batch (batch_id)
  ) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,
	`CREATE TABLE IF NOT EXISTS treehazard_staging_photo (
    id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
    batch_id BIGINT UNSIGNED NOT NULL COMMENT '关联 treehazard_staging_batch.id',
    staging_record_id BIGINT UNSIGNED NULL COMMENT '关联 treehazard_staging_record.id（NULL=旧数据，转入时回退按 crane_record_id 归挂）',
    crane_record_id VARCHAR(64) NOT NULL DEFAULT '' COMMENT 'crane-answer 记录 id（冗余留存，旧数据转入归挂用）',
    cos_key VARCHAR(255) NOT NULL DEFAULT '' COMMENT 'COS 对象键（COS_TREEHAZARD_PREFIX 前缀）',
    url VARCHAR(512) NOT NULL DEFAULT '' COMMENT '访问地址',
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    KEY idx_batch (batch_id),
    KEY idx_staging_record (staging_record_id)
  ) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,
	`CREATE TABLE IF NOT EXISTS treehazard_conflict (
    id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
    team_id BIGINT UNSIGNED NOT NULL COMMENT '所属班组，关联 sys_team.id',
    record_id BIGINT UNSIGNED NULL COMMENT '关联 treehazard_record.id',
    server_snapshot JSON NULL COMMENT '服务端业务列快照',
    intranet_snapshot JSON NULL COMMENT '内网表行业务列快照',
    status VARCHAR(16) NOT NULL DEFAULT 'pending' COMMENT '状态：pending 待解决 / resolved 已解决',
    resolution VARCHAR(16) NULL COMMENT '解决方式：server 保留服务端 / intranet 采用内网',
    resolved_by BIGINT UNSIGNED NULL COMMENT '解决人，关联 sys_user.id',
    resolved_at DATETIME NULL COMMENT '解决时间',
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    KEY idx_team_status (team_id, status)
  ) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,
	`CREATE TABLE IF NOT EXISTS treehazard_team_setting (
    team_id BIGINT UNSIGNED NOT NULL UNIQUE COMMENT '所属班组，关联 sys_team.id',
    vertical_threshold DECIMAL(6,2) NULL COMMENT '垂直距离阈值（m），NULL=未配置',
    updated_by BIGINT UNSIGNED NULL COMMENT '最近修改人，关联 sys_user.id',
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP
  ) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,
	`CREATE TABLE IF NOT EXISTS treehazard_deleted_id (
    id BIGINT UNSIGNED NOT NULL PRIMARY KEY COMMENT '被删记录 id（墓碑）',
    team_id BIGINT UNSIGNED NOT NULL COMMENT '所属班组，关联 sys_team.id',
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
  ) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,
	`CREATE TABLE IF NOT EXISTS treehazard_sync_log (
    id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
    team_id BIGINT UNSIGNED NOT NULL COMMENT '所属班组，关联 sys_team.id',
    kind VARCHAR(16) NOT NULL DEFAULT 'manual' COMMENT '同步方式：manual 手动 / idle 空闲 / night 夜间',
    result TEXT NULL COMMENT '结果摘要',
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
  ) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,
}

// schemaCompatColumns 老库兼容补列（MySQL ALTER 不支持 IF NOT EXISTS，先查 information_schema）。
var schemaCompatColumns = []struct {
	table, column, alterSQL string
}{
	{
		table:  "treehazard_staging_batch",
		column: "review_note",
		alterSQL: `ALTER TABLE treehazard_staging_batch ADD COLUMN review_note VARCHAR(255) NULL
       COMMENT '审核意见（审核不通过时的修改说明，展示给暂存区修改人）' AFTER pusher_name`,
	},
	{
		table:  "treehazard_staging_photo",
		column: "staging_record_id",
		alterSQL: `ALTER TABLE treehazard_staging_photo
       ADD COLUMN staging_record_id BIGINT UNSIGNED NULL
         COMMENT '关联 treehazard_staging_record.id（NULL=旧数据，转入时回退按 crane_record_id 归挂）' AFTER batch_id,
       ADD KEY idx_staging_record (staging_record_id)`,
	},
}
