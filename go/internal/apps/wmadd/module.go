// Package wmadd 迁移 server/src/wmadd：水印添加（移动端独立子应用，app_key wm-add）——
// 复制出工日志「拍摄/选择照片并添加水印」能力，与出工日志解耦：仅渲染水印并 base64 回图，
// 不传 COS、不入库、不触发 Dify 验证。地理/杆塔/渲染均对齐出工日志模块语义；本应用无业务表。
//
// 路由基路径 /api/v1/wmadd；全部接口需登录 + wm-add 应用权限；杆塔按生效班组隔离
// （超管可用 ?team_id= 指定，其余角色固定本班）。响应信封 {code,message,data} 沿用数字业务码。
package wmadd

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"shade/internal/auth"
	"shade/internal/httpapi"
)

// AppPermit 校验用户是否拥有指定应用（app_key）的使用权限（对接 sys_user_app；nil 表示未接入，不拦截）。
type AppPermit interface {
	Allow(ctx context.Context, userID int64, appKey string) (bool, error)
}

// Module 是水印添加应用模块。
type Module struct {
	DB       *sql.DB
	Permit   AppPermit     // 应用权限检查（可注入；nil 不拦截）
	Geo      GeoClient     // 地理服务（可注入；nil 表示未接入，/geo 回空串）
	Renderer PhotoRenderer // 水印渲染（可注入；nil 时 /render 报错）
}

// New 构造水印添加模块；Permit/Geo/Renderer 可在装配后注入。
func New(db *sql.DB) *Module {
	return &Module{DB: db}
}

// RegisterRoutes 注册 /api/v1/wmadd 下全部路由（挂载在 /api/v1 之下）。
func (m *Module) RegisterRoutes(r chi.Router) {
	r.Route("/wmadd", func(r chi.Router) {
		r.Get("/geo", m.geo)
		r.Get("/towers", m.towers)
		r.Post("/render", m.render)
	})
}

// enter 完成 应用权限 + 身份校验；失败时已写出响应，调用方直接返回。
func (m *Module) enter(w http.ResponseWriter, r *http.Request) (auth.Identity, bool) {
	ident, ok := httpapi.IdentityFromContext(r.Context())
	if !ok {
		fail(w, http.StatusUnauthorized, codeNoLogin, "未登录或登录已过期")
		return auth.Identity{}, false
	}
	if m.Permit != nil {
		allow, err := m.Permit.Allow(r.Context(), ident.ID, "wm-add")
		if err != nil {
			writeErr(w, err)
			return auth.Identity{}, false
		}
		if !allow {
			fail(w, http.StatusForbidden, codeNoApp, "暂无该应用的使用权限，请联系管理员开通")
			return auth.Identity{}, false
		}
	}
	return ident, true
}

// resolveTeam 解析请求生效班组（超管可指定 ?team_id=，其余固定本班；无可用班组返回 nil）。
func (m *Module) resolveTeam(r *http.Request, ident auth.Identity) *httpapi.Team {
	if m.DB == nil {
		return nil
	}
	wanted := parseWantedTeamID(r.URL.Query().Get("team_id"))
	adapter := httpapi.IdentityAdapter{TeamID: ident.TeamID, Role: ident.Role}
	team, err := httpapi.ResolveTeam(r.Context(), m.DB, adapter, wanted)
	if err != nil {
		return nil
	}
	return &team
}

// parseWantedTeamID 按 JS Number(x) + Number.isInteger 判定：整数 > 0 才生效，其余 0。
func parseWantedTeamID(raw string) int64 {
	n, err := strconv.ParseFloat(strings.TrimSpace(raw), 64)
	if err != nil || n <= 0 || n != math.Trunc(n) {
		return 0
	}
	return int64(n)
}

// GET /geo?lng=&lat=：按经纬度取当前「地点 + 天气」（高德），与出工日志同口径；
// 未配置 AMAP_MAP_KEY 或调用失败时返回空串，前端留空手填。
func (m *Module) geo(w http.ResponseWriter, r *http.Request) {
	ident, ok := m.enter(w, r)
	_ = ident
	if !ok {
		return
	}
	lng, okLng := parseCoord(r.URL.Query().Get("lng"), 180)
	lat, okLat := parseCoord(r.URL.Query().Get("lat"), 90)
	if !okLng || !okLat {
		fail(w, http.StatusBadRequest, codeCoord, "经纬度参数无效")
		return
	}
	result := LocationWeather{}
	if m.Geo != nil {
		got, err := m.Geo.FetchLocationWeather(r.Context(), lng, lat)
		if err == nil {
			result = got
		}
	}
	writeOK(w, result)
}

// parseCoord 按 JS Number + isFinite + 绝对值上限判定经纬度。
func parseCoord(raw string, absMax float64) (float64, bool) {
	n, err := strconv.ParseFloat(strings.TrimSpace(raw), 64)
	if err != nil || math.IsNaN(n) || math.IsInf(n, 0) || math.Abs(n) > absMax {
		return 0, false
	}
	return n, true
}

// GET /towers：当前用户班组杆塔坐标全量（行 = [电压等级, 线路名称, 杆塔号, 经度, 纬度]），
// 数据读写见 worklog_tower 表；未分配班组返回 { rows: [] }（与有班组同形状）。
func (m *Module) towers(w http.ResponseWriter, r *http.Request) {
	ident, ok := m.enter(w, r)
	if !ok {
		return
	}
	rows := []any{}
	team := m.resolveTeam(r, ident)
	if team != nil && m.DB != nil {
		list, err := loadTowers(r.Context(), m.DB, team.ID)
		if err != nil {
			writeErr(w, err)
			return
		}
		rows = list
	}
	writeOK(w, map[string]any{"rows": rows})
}

// loadTowers 读取班组杆塔坐标（worklog_tower，按 sort,id 排序；经纬度转数字返回）。
func loadTowers(ctx context.Context, db *sql.DB, teamID int64) ([]any, error) {
	res, err := db.QueryContext(ctx,
		`SELECT voltage_level, line_name, tower_no, lng, lat FROM worklog_tower WHERE team_id = ? ORDER BY sort, id`, teamID)
	if err != nil {
		return nil, err
	}
	defer res.Close()
	out := []any{}
	for res.Next() {
		var voltage, lineName, towerNo, lngText, latText string
		if err := res.Scan(&voltage, &lineName, &towerNo, &lngText, &latText); err != nil {
			return nil, err
		}
		lng, _ := strconv.ParseFloat(strings.TrimSpace(lngText), 64)
		lat, _ := strconv.ParseFloat(strings.TrimSpace(latText), 64)
		out = append(out, []any{voltage, lineName, towerNo, lng, lat})
	}
	return out, res.Err()
}

// dataURLPhoto dataURL 解析产物（jpeg/png）。
type dataURLPhoto struct {
	buf []byte
}

var dataURLPrefixes = []struct {
	mime string
}{{"image/jpeg"}, {"image/jpg"}, {"image/png"}}

// parseDataUrlPhoto 解析 jpeg/png base64 dataURL；非法返回 nil（同 Node utils/photo.js）。
func parseDataUrlPhoto(image string) *dataURLPhoto {
	const base64Marker = ";base64,"
	trimmed := strings.TrimSpace(image)
	if !strings.HasPrefix(trimmed, "data:image/") {
		return nil
	}
	idx := strings.Index(trimmed, base64Marker)
	if idx < 0 {
		return nil
	}
	mime := strings.TrimPrefix(trimmed[:idx], "data:")
	mime = strings.TrimSuffix(mime, ";base64")
	okMime := false
	for _, p := range dataURLPrefixes {
		if strings.EqualFold(mime, p.mime) {
			okMime = true
			break
		}
	}
	if !okMime {
		return nil
	}
	buf, err := base64.StdEncoding.DecodeString(trimmed[idx+len(base64Marker):])
	if err != nil {
		return nil
	}
	return &dataURLPhoto{buf: buf}
}

// POST /render：原图 base64 + wm 字段（含 EXIF orientation）→ 渲染水印 → base64 JPEG 回图。
// 只回图：不传 COS、不入库、不触发 Dify 验证（wm 字段清洗用出工日志同款 sanitizeWm）。
func (m *Module) render(w http.ResponseWriter, r *http.Request) {
	if _, ok := m.enter(w, r); !ok {
		return
	}
	if m.Renderer == nil {
		writeErr(w, errors.New("水印渲染未接入"))
		return
	}
	body, err := decodeBody(r)
	if err != nil {
		fail(w, http.StatusBadRequest, codeParam, "参数错误：请求体不是合法 JSON")
		return
	}
	photo := parseDataUrlPhoto(rawString(body["image"]))
	if photo == nil {
		fail(w, http.StatusBadRequest, codeFormat, "照片格式应为 jpeg/png（base64 dataURL）")
		return
	}
	if len(photo.buf) == 0 || len(photo.buf) > 15*1024*1024 {
		fail(w, http.StatusBadRequest, codeSize, "照片大小应在 15MB 以内")
		return
	}
	wm := map[string]string{}
	orientation := ""
	if raw, has := body["wm"]; has && string(raw) != "null" {
		var obj map[string]json.RawMessage
		if err := json.Unmarshal(raw, &obj); err == nil {
			for k, v := range obj {
				wm[k] = rawString(v)
			}
			orientation = wm["orientation"]
		}
	}
	out, err := m.Renderer.Render(photo.buf, SanitizeWm(wm), orientation)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeOK(w, map[string]any{"image": "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(out)})
}
