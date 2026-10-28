// Package crossing 迁移 server/src/crossing：交跨记录（crossing_record）、crane-answer 直推入库
// （direct 幂等 upsert / staging 暂存区整批替换 / deletes 撤回删除）、暂存区确认导入、手动新增与编辑
// （POST /records、PUT /records/:id，见 recordwrite.go）、删除墓碑、看板与按运维线路（team_line）分组的
// 记录列表、统计表导出。
// 表结构与 Node 版一致（幂等键 uk_crane = (team_id, crane_session_id, crane_record_id)，空值存 NULL）；
// 鉴权沿用 httpapi.IdentityFromContext：超管可 ?team_id= 指定班组，team_admin 仅本班，
// 普通用户按 crossing 应用权限（AppPermit 可注入，nil 暂不拦截）；直推 /ingest 凭 X-Ingest-Token 免登录。
package crossing

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"encoding/json"
	"errors"
	"math"
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

// AppKey 本模块对应的 sys_app 应用标识（requireApp('crossing') 同口径）。
const AppKey = "crossing"

// AppPermit 校验用户是否拥有指定应用（app_key）的使用权限（对接 sys_user_app；nil 表示未接入，不拦截）。
type AppPermit interface {
	Allow(ctx context.Context, userID int64, appKey string) (bool, error)
}

// ConfigGet 运行时配置读取（*config.Store 天然满足；测试可注入假实现，不连真实库）。
type ConfigGet interface {
	Get(ctx context.Context, key string) (string, error)
}

// Notifier 站内通知（可注入；nil 表示未接入，仅跳过通知不影响直推应答）。
type Notifier interface {
	// Push 向 admin + team_admin 推送站内通知；失败由实现记录日志，不影响主流程。
	Push(ctx context.Context, targets []string, title, content string) error
}

// Module 是交跨记录应用模块。
type Module struct {
	DB       *sql.DB
	Cfg      ConfigGet // 配置（CROSSING_INGEST_TOKEN 等）；nil 视为未配置
	Permit   AppPermit // 应用权限检查（可注入；nil 不拦截）
	Notifier Notifier  // 站内通知（可注入；nil 不通知）
	// ServerDir 数据目录根（随批 xlsx 落盘于 {ServerDir}/data/crossing/inbox/）；空 = 当前目录。
	ServerDir string
	// Now 时间源（测试可注入）；nil = time.Now。
	Now func() time.Time
	// WeKnora WeKnora 知识库 API（可注入，测试用假实现）；nil 时按 WEKNORA_* 配置走内置 HTTP 客户端。
	WeKnora WeKnoraClient
	// WeKnoraState 知识库同步状态存储；nil 时用 data/crossing/weknora-sync-state.json。
	WeKnoraState WeKnoraStateStore
	// Usb 内网 U 盘同步设备链路（可注入，见 UsbLink）；nil 时同步明确报链路未接入。
	Usb UsbLink

	syncMu         sync.Mutex
	syncRunning    bool // 内网 U 盘同步轮次重入守卫
	weknoraRunning bool // WeKnora 同步轮次重入守卫
}

// New 构造交跨记录模块；Permit / Notifier 可在装配后注入。
func New(db *sql.DB, cfg ConfigGet) *Module {
	return &Module{DB: db, Cfg: cfg}
}

// 编译期确认 *config.Store 满足 ConfigGet。
var _ ConfigGet = (*config.Store)(nil)

func (m *Module) now() time.Time {
	if m.Now != nil {
		return m.Now()
	}
	return time.Now()
}

// RegisterRoutes 注册 /api/v1/crossing 下全部路由（挂载在 /api/v1 之下）。
// POST /ingest 免登录（X-Ingest-Token），其余接口需登录 + crossing 应用权限 + 生效班组。
func (m *Module) RegisterRoutes(r chi.Router) {
	r.Post("/crossing/ingest", m.handleIngest)
	r.Route("/crossing", func(r chi.Router) {
		r.Get("/dashboard", m.dashboard)
		r.Get("/records", m.recordList)
		r.Post("/records", m.recordCreate)
		r.Get("/records/{id}", m.recordDetail)
		r.Put("/records/{id}", m.recordUpdate)
		r.Delete("/records/{id}", m.recordDelete)
		r.Get("/export/preview", m.exportPreview)
		r.Get("/export", m.exportXLSX)
		r.Get("/export/{name}", m.exportXLSX)
		// 暂存区（班管/超管）
		r.Get("/staging/batches", m.stagingBatches)
		r.Get("/staging/batches/{id}", m.stagingBatchDetail)
		r.Put("/staging/records/{id}", m.stagingRecordUpdate)
		r.Post("/staging/batches/{id}/import", m.stagingImport)
		r.Delete("/staging/batches/{id}", m.stagingBatchDelete)
		// 冲突（班管/超管；内网同步产生，人工二选一解决）。
		r.Get("/conflicts", m.conflictsList)
		r.Post("/conflicts/{id}/resolve", m.conflictResolve)
		// 内网 U 盘同步（编排见 sync.go）：手动触发 + 状态查询。
		r.Post("/sync/push-now", m.syncPushNow)
		r.Get("/sync/status", m.syncStatus)
		// WeKnora 知识库同步（编排见 weknora.go）：手动触发。
		r.Post("/weknora-sync/push-now", m.weknoraPushNow)
	})
}

// enter 完成 登录 + 应用权限 + 生效班组解析。bodyTeamID 为请求体携带的 team_id 候选
// （query 优先，对齐 utils/team.resolveReqTeam 口径）。team 为 nil 表示无可用班组（Node req.team = null）。
// 失败时已写出响应，调用方直接返回。
func (m *Module) enter(w http.ResponseWriter, r *http.Request, bodyTeamID int64) (auth.Identity, *httpapi.Team, bool) {
	ident, ok := httpapi.IdentityFromContext(r.Context())
	if !ok {
		fail(w, http.StatusUnauthorized, codeNoLogin, "未登录或登录已过期")
		return auth.Identity{}, nil, false
	}
	if m.Permit != nil {
		allow, err := m.Permit.Allow(r.Context(), ident.ID, AppKey)
		if err != nil {
			writeErr(w, err)
			return auth.Identity{}, nil, false
		}
		if !allow {
			fail(w, http.StatusForbidden, codeNoApp, "暂无该应用的使用权限，请联系管理员开通")
			return auth.Identity{}, nil, false
		}
	}
	team, err := m.resolveTeam(r, ident, bodyTeamID)
	if err != nil {
		writeErr(w, err)
		return auth.Identity{}, nil, false
	}
	return ident, team, true
}

// enterAdmin 在 enter 之上叠加管理门控：超管任意班组（team 由 ?team_id= 解析），班组管理员仅本班。
func (m *Module) enterAdmin(w http.ResponseWriter, r *http.Request, bodyTeamID int64) (auth.Identity, *httpapi.Team, bool) {
	ident, team, ok := m.enter(w, r, bodyTeamID)
	if !ok {
		return ident, team, false
	}
	if ident.Role == "admin" {
		return ident, team, true
	}
	if ident.Role == "team_admin" && team != nil && ident.TeamID == team.ID {
		return ident, team, true
	}
	fail(w, http.StatusForbidden, codeNotAdmin, "仅管理员可执行此操作")
	return auth.Identity{}, nil, false
}

// resolveTeam 解析请求生效班组（超管可指定，其余固定本班）。
// 无可用班组时返回 (nil, nil)，由各路由按空态处理，对齐 Node 的 req.team = null。
func (m *Module) resolveTeam(r *http.Request, ident auth.Identity, bodyTeamID int64) (*httpapi.Team, error) {
	wanted := bodyTeamID
	if q, ok := r.URL.Query()["team_id"]; ok {
		wanted = parseWantedTeamID(q[0])
	}
	adapter := httpapi.IdentityAdapter{TeamID: ident.TeamID, Role: ident.Role}
	team, err := httpapi.ResolveTeam(r.Context(), m.DB, adapter, wanted)
	if err == nil {
		return &team, nil
	}
	if errors.Is(err, sql.ErrNoRows) {
		// Node 口径：超管指定班组不存在/停用时回退本班，再无则默认班组
		if ident.Role == "admin" && wanted > 0 {
			t2, err2 := httpapi.ResolveTeam(r.Context(), m.DB, adapter, 0)
			if err2 == nil {
				return &t2, nil
			}
			if errors.Is(err2, sql.ErrNoRows) {
				return nil, nil
			}
			return nil, err2
		}
		return nil, nil
	}
	if err.Error() == "未分配班组" { // httpapi.ResolveTeam 对未分配班组的固定错误文案
		return nil, nil
	}
	return nil, err
}

// bodyTeamID 从 JSON 请求体取 team_id 候选（缺省/非法 → 0）。
func bodyTeamID(body map[string]json.RawMessage) int64 {
	raw, ok := body["team_id"]
	if !ok {
		return 0
	}
	return parseWantedTeamID(rawString(raw))
}

// parseWantedTeamID 按 JS Number(x) + Number.isInteger 判定：整数 > 0 才生效，其余 0。
func parseWantedTeamID(raw string) int64 {
	n, err := strconv.ParseFloat(strings.TrimSpace(raw), 64)
	if err != nil || n <= 0 || n != math.Trunc(n) {
		return 0
	}
	return int64(n)
}

// cfgValue 读运行时配置（缺省回落 def）；Cfg 未注入时直接取 def。
func (m *Module) cfgValue(ctx context.Context, key, def string) (string, error) {
	if m.Cfg == nil {
		return def, nil
	}
	v, err := m.Cfg.Get(ctx, key)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return def, nil
		}
		return def, err
	}
	return v, nil
}

// ingestToken 取直推对接密钥（空串 = 未配置）。
func (m *Module) ingestToken(ctx context.Context) (string, error) {
	return m.cfgValue(ctx, "CROSSING_INGEST_TOKEN", "")
}

// tokenEqual 恒定时间比较（先比长度，防时序侧信道，对齐 Node crypto.timingSafeEqual 口径）。
func tokenEqual(provided, expected string) bool {
	a := []byte(provided)
	b := []byte(expected)
	if len(a) != len(b) {
		return false
	}
	return subtle.ConstantTimeCompare(a, b) == 1
}

// EnsureSchema 建立本模块依赖的表（CREATE TABLE IF NOT EXISTS 幂等）。
func (m *Module) EnsureSchema(ctx context.Context) error {
	for _, ddl := range schemaDDL {
		if _, err := m.DB.ExecContext(ctx, ddl); err != nil {
			return err
		}
	}
	return nil
}
