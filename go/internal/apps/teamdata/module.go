// Package teamdata 迁移 server/src/teamdata：班组字典（车牌 / 目的地 / 出工成员）、成员排序、
// 运维线路 team_line、杆塔坐标与 Excel 导入/模板。数据复用 worklog_dict_* 与 worklog_tower 表
// （表结构不动，归出工日志应用建表），team_line 为本模块建表；全部接口限 超管 或 本班 team_admin。
package teamdata

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"shade/internal/auth"
	"shade/internal/config"
	"shade/internal/httpapi"
)

// AppPermit 校验用户是否拥有指定应用（app_key）的使用权限（对接 sys_user_app；nil 表示未接入，不拦截）。
type AppPermit interface {
	Allow(ctx context.Context, userID int64, appKey string) (bool, error)
}

// Module 是班组数据应用模块。
type Module struct {
	DB     *sql.DB
	Cfg    *config.Store // 预留：配置读取（当前班组数据不依赖运行时配置）
	Permit AppPermit     // 应用权限检查（可注入；nil 不拦截）
	// OnTowersChanged 杆塔坐标导入后的缓存失效钩子（对齐 Node 的 towers.invalidate，
	// 供出工日志侧杆塔缓存重载；nil 不通知）。
	OnTowersChanged func(teamID int64)
}

// New 构造班组数据模块；Permit 可在装配后注入。
func New(db *sql.DB, cfg *config.Store) *Module {
	return &Module{DB: db, Cfg: cfg}
}

// RegisterRoutes 注册 /api/v1/teamdata 下全部路由（挂载在 /api/v1 之下）。
func (m *Module) RegisterRoutes(r chi.Router) {
	r.Route("/teamdata", func(r chi.Router) {
		for _, spec := range dictSpecs {
			spec := spec
			r.Get("/"+spec.path, func(w http.ResponseWriter, req *http.Request) { m.dictList(w, req, spec) })
			r.Post("/"+spec.path, func(w http.ResponseWriter, req *http.Request) { m.dictCreate(w, req, spec) })
			r.Put("/"+spec.path+"/{id}", func(w http.ResponseWriter, req *http.Request) { m.dictUpdate(w, req, spec) })
			r.Delete("/"+spec.path+"/{id}", func(w http.ResponseWriter, req *http.Request) { m.dictDelete(w, req, spec) })
		}
		r.Put("/members/{id}/move", m.moveMember)
		r.Get("/lines", m.lineAgg)
		r.Get("/towers", m.towerList)
		r.Get("/towers/template", m.towerTemplate)
		r.Post("/towers/import", m.towerImport)
		r.Get("/oplines", m.oplineList)
		r.Post("/oplines", m.oplineCreate)
		r.Put("/oplines/{id}", m.oplineUpdate)
		r.Delete("/oplines/{id}", m.oplineDelete)
		r.Put("/oplines/{id}/move", m.moveOpline)
		r.Get("/oplines/template", m.oplineTemplate)
		r.Post("/oplines/import", m.oplineImport)
	})
}

// enter 完成 应用权限 + 生效班组 + 管理门控（超管任意班组、班组管理员仅本班）。
// bodyTeamID 为请求体/表单携带的 team_id 候选；bodyWins=true 时表单优先于 query（导入接口口径），
// 否则 query 优先（utils/team.resolveReqTeam 口径）。失败时已写出响应，调用方直接返回。
func (m *Module) enter(w http.ResponseWriter, r *http.Request, bodyTeamID int64, bodyWins bool) (auth.Identity, *httpapi.Team, bool) {
	ident, ok := httpapi.IdentityFromContext(r.Context())
	if !ok {
		fail(w, http.StatusUnauthorized, codeNoLogin, "未登录或登录已过期")
		return auth.Identity{}, nil, false
	}
	if m.Permit != nil {
		allow, err := m.Permit.Allow(r.Context(), ident.ID, "team-data")
		if err != nil {
			writeErr(w, err)
			return auth.Identity{}, nil, false
		}
		if !allow {
			fail(w, http.StatusForbidden, codeNoApp, "暂无该应用的使用权限，请联系管理员开通")
			return auth.Identity{}, nil, false
		}
	}
	team, err := m.resolveTeam(r, ident, bodyTeamID, bodyWins)
	if err != nil {
		writeErr(w, err)
		return auth.Identity{}, nil, false
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
func (m *Module) resolveTeam(r *http.Request, ident auth.Identity, bodyTeamID int64, bodyWins bool) (*httpapi.Team, error) {
	var wanted int64
	q, hasQuery := r.URL.Query()["team_id"]
	switch {
	case bodyWins && bodyTeamID > 0:
		wanted = bodyTeamID // 导入口径：表单 team_id 优先（server/src/teamdata 导入路由）
	case hasQuery:
		wanted = parseWantedTeamID(q[0])
	case bodyWins:
		wanted = bodyTeamID
	default:
		wanted = peekBodyTeamID(r) // resolveReqTeam 口径：query 缺省时取 JSON body
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

// peekBodyTeamID 窥探 JSON 请求体的 team_id（读完回填 body，不消费请求体）。
func peekBodyTeamID(r *http.Request) int64 {
	if r.Body == nil {
		return 0
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, 8<<20))
	r.Body = io.NopCloser(bytes.NewReader(raw))
	if err != nil {
		return 0
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		return 0
	}
	return bodyTeamID(obj)
}

// parseWantedTeamID 按 JS Number(x) + Number.isInteger 判定：整数 > 0 才生效，其余 0。
func parseWantedTeamID(raw string) int64 {
	n, err := strconv.ParseFloat(strings.TrimSpace(raw), 64)
	if err != nil || n <= 0 || n != math.Trunc(n) {
		return 0
	}
	return int64(n)
}
