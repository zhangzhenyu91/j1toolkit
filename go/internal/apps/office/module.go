package office

// ONLYOFFICE 模块装配：/api/v1/office 路由（status / config 签发 / raw 回源 / callback 保存回调）。
// 回调应答为 OnlyOffice 约定格式 {"error":0}（不走统一信封）。

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"shade/internal/auth"
	"shade/internal/httpapi"
)

// Module ONLYOFFICE 对接模块。
type Module struct {
	// Cfg 运行时配置（OO_PUBLIC_URL / OO_INTERNAL_URL / OO_CALLBACK_BASE / OO_JWT_SECRET / OO_TICKET_SECRET）。
	Cfg ConfigReader
	// Sources view-only 文档来源（key = wls/sdr/wlr/thd/crs）；可注入。
	Sources map[string]DocSource
	// Netdisk 网盘来源（可编辑协同 + 保存写回）；nil 时 ndp 源不可用。
	Netdisk NetdiskDoc
	// Permit 应用权限检查（/config 按 src 分派应用）；nil 放行。
	Permit AppPermit
	// TicketSecret 票据密钥（优先于 Cfg）。
	TicketSecret string
	// OOJWTSecret OnlyOffice JWT 密钥（优先于 Cfg）。
	OOJWTSecret string
}

// New 构造 ONLYOFFICE 模块。
func New(cfg ConfigReader) *Module {
	return &Module{Cfg: cfg, Permit: AllowAllPermit{}, Sources: map[string]DocSource{}}
}

// RegisterRoutes 注册 /api/v1/office 路由。
func (m *Module) RegisterRoutes(r chi.Router) {
	r.Route("/office", func(r chi.Router) {
		r.Get("/status", m.handleStatus)
		r.Post("/config", m.handleConfig)
		// 免登录：票据即鉴权（主程序需放行 /api/v1/office/raw 与 /callback）。
		r.Get("/raw/{ticket}", m.handleRaw)
		r.Get("/raw/{ticket}/{name}", m.handleRaw)
		r.Post("/callback/{ticket}", m.handleCallback)
	})
}

type moduleContextKey string

const identityContextKey moduleContextKey = "office.identity"

// authed 登录态 + 应用权限分派（/config 按 src 决定应用）。
func (m *Module) authed(w http.ResponseWriter, r *http.Request, appKey string) (auth.Identity, bool) {
	ident, okIdent := httpapi.IdentityFromContext(r.Context())
	if !okIdent {
		fail(w, http.StatusUnauthorized, codeUnauthorized, "未登录或登录已过期")
		return auth.Identity{}, false
	}
	if m.Permit != nil && appKey != "" {
		allow, err := m.Permit.Allow(r.Context(), ident.ID, appKey)
		if err != nil {
			fail(w, http.StatusInternalServerError, codeUpstream, "应用权限校验失败")
			return auth.Identity{}, false
		}
		if !allow {
			fail(w, http.StatusForbidden, codeUnauthorized, "暂无该应用的使用权限，请联系管理员开通")
			return auth.Identity{}, false
		}
	}
	return ident, true
}

// configValue 配置读取（空串容忍）。
func (m *Module) configValue(ctx context.Context, key string) string {
	if m.Cfg == nil {
		return ""
	}
	v, err := m.Cfg.Get(ctx, key)
	if err != nil {
		return ""
	}
	return v
}

// handleStatus GET /status：启用状态（OO 公网地址已配置即启用）。
func (m *Module) handleStatus(w http.ResponseWriter, r *http.Request) {
	ok(w, map[string]any{"enabled": m.configValue(r.Context(), "OO_PUBLIC_URL") != ""}, "")
}

// handleConfig POST /config：签发编辑器配置。
// body 兼容两种口径：office.html 扁平 query 透传（src=worklog-sheet&sheet=..&from=..&to=.. 等，
// 值全为字符串，宽松解析）与结构化口径（src=wls + ref JSON）；缺省 src=ndp（网盘文件）。
// name 在扁平口径下由桥签发期定名（body.Name 可缺省，网盘取路径末段）；
// wlr 的 key 为附件 cos_key（文档版本 key 由后端按内容指纹计算，见下）。
func (m *Module) handleConfig(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Src   string     `json:"src"`
		Space string     `json:"space"`
		Path  string     `json:"path"`
		Name  string     `json:"name"`
		Key   string     `json:"key"` // ndp=文档版本 key；wlr=附件 cos_key（扁平透传共用键名）
		Ref   string     `json:"ref"` // 引用直传（缺省按扁平参数组装）
		Team  flexInt64  `json:"team_id"`
		Mode  string     `json:"mode"` // view 强制只读
		Sheet flexString `json:"sheet"`
		From  flexString `json:"from"`
		To    flexString `json:"to"`
		ID    flexString `json:"id"`
		Line  flexString `json:"line"`
		Split flexBool   `json:"split"`
		Force flexBool   `json:"force"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		fail(w, http.StatusBadRequest, codeParam, "请求参数错误")
		return
	}
	src := srcAlias[strings.ToLower(strings.TrimSpace(body.Src))]
	if src == "" {
		fail(w, http.StatusBadRequest, codeUnknownSrc, "未知文档来源")
		return
	}
	// 应用权限按来源分派（口径同 requireApp：netdisk / work-log / safeday / tree-hazard / crossing）。
	appKey := map[string]string{
		"ndp": "netdisk", "wls": "work-log", "wlr": "work-log",
		"sdr": "safeday", "thd": "tree-hazard", "crs": "crossing",
	}[src]
	ident, okAuth := m.authed(w, r, appKey)
	if !okAuth {
		return
	}
	team := int64(body.Team)
	if team <= 0 {
		team = ident.TeamID
	}

	// ---- 票据载荷与引用（ref 直传优先；否则按扁平参数组装） ----
	ticket := TicketPayload{Src: src}
	ref := strings.TrimSpace(body.Ref)
	switch src {
	case "ndp":
		if !validNDPPath(body.Path) {
			fail(w, http.StatusBadRequest, codeParam, "路径不合法")
			return
		}
		ticket.NDP = &NDP{Space: body.Space, User: ident.Username, Path: body.Path, Team: team}
	case "wls":
		if ref == "" {
			sheet := string(body.Sheet)
			if sheet != "task" && sheet != "fee" && sheet != "dispatch" {
				fail(w, http.StatusBadRequest, codeParam, "参数错误：sheet（task/fee/dispatch）")
				return
			}
			if ok := validSheetRange(w, string(body.From), string(body.To)); !ok {
				return
			}
			ref = EncodeRef(WlsRef{Sheet: sheet, From: string(body.From), To: string(body.To),
				Team: team, Force: bool(body.Force)})
		}
	case "sdr":
		if ref == "" {
			if strings.TrimSpace(string(body.ID)) == "" {
				fail(w, http.StatusBadRequest, codeParam, "参数错误：id")
				return
			}
			ref = EncodeRef(SdrRef{ID: string(body.ID)})
		}
	case "wlr":
		if ref == "" {
			if strings.TrimSpace(string(body.ID)) == "" || strings.TrimSpace(body.Key) == "" {
				fail(w, http.StatusBadRequest, codeParam, "参数错误：id / key")
				return
			}
			ref = EncodeRef(WlrRef{ID: string(body.ID), Key: body.Key, Team: team})
		}
	case "thd", "crs":
		if ref == "" {
			ref = EncodeRef(LineRef{Team: team, Line: strings.TrimSpace(string(body.Line)), Split: bool(body.Split)})
		}
	}
	ticket.Ref = ref

	// ---- 定名 + 签发校验（实现 Namer 的桥：范围/归属/汇总前核验都在此拒绝） ----
	name := strings.TrimSpace(body.Name)
	if src != "ndp" {
		if namer, ok := m.Sources[src].(Namer); ok {
			resolved, err := namer.Resolve(r.Context(), ident, ref)
			if err != nil {
				refFail(w, err)
				return
			}
			if resolved != "" {
				name = resolved
			}
		}
	} else if name == "" {
		name = baseNameOfPath(body.Path) // 网盘文件名取路径末段（office.html 不透传 name）
	}
	ticket.Name = name
	if name == "" {
		fail(w, http.StatusBadRequest, codeParam, "参数错误：name")
		return
	}
	docType, editable := DocTypeOf(name)
	if docType == "" {
		fail(w, http.StatusBadRequest, codeUnsupported, "不支持的文档格式")
		return
	}
	viewOnly := viewOnlySources(src) || !editable || strings.EqualFold(strings.TrimSpace(body.Mode), "view")

	// ---- 文档版本 key：内容变即滚动（OnlyOffice 按 key 归并会话与缓存） ----
	docVer := strings.TrimSpace(body.Key)
	if src == "wlr" {
		docVer = "" // wlr 的 key 是 cos_key，文档 key 另算
	}
	if docVer == "" && src == "ndp" && ticket.NDP != nil {
		if k, ok := m.Netdisk.(NetdiskKeyer); ok {
			key, err := k.DocKeyOf(r.Context(), *ticket.NDP)
			if err != nil {
				refFail(w, err)
				return
			}
			docVer = key
		}
	}
	if docVer == "" && src == "wlr" {
		var wr WlrRef
		if json.Unmarshal([]byte(ref), &wr) == nil && wr.Key != "" {
			sum := sha1.Sum([]byte(wr.Key)) // 附件不可变（删旧传新即换 cos_key），sha1(cos_key) 即内容指纹
			docVer = hex.EncodeToString(sum[:])
		}
	}
	if docVer == "" {
		docVer = "k-" + NewTicketID()
	}

	base := strings.TrimRight(m.configValue(r.Context(), "OO_CALLBACK_BASE"), "/")
	public := strings.TrimRight(m.configValue(r.Context(), "OO_PUBLIC_URL"), "/")
	if public == "" {
		fail(w, http.StatusServiceUnavailable, codeNotConfigured, "OnlyOffice 未配置（OO_PUBLIC_URL）")
		return
	}
	fileTTL := m.ttlSeconds(r.Context(), "OO_FILE_TICKET_TTL", 600)
	cbTTL := m.ttlSeconds(r.Context(), "OO_CALLBACK_TICKET_TTL", 43200)
	token, err := m.SignTicket(r.Context(), ticket, fileTTL)
	if err != nil {
		fail(w, http.StatusInternalServerError, codeUpstream, "票据签发失败")
		return
	}
	fileURL := fmt.Sprintf("%s/api/v1/office/raw/%s/%s", base, token, url.PathEscape(name))
	doc := map[string]any{
		"fileType":    fileExt(name),
		"key":         docVer,
		"title":       name,
		"url":         fileURL,
		"permissions": map[string]any{"edit": !viewOnly, "download": true, "print": true},
	}
	editor := map[string]any{
		"lang":   "zh-CN",
		"region": "zh-CN",
		"customization": map[string]any{
			"forcesave": false, // 只读源无 forcesave；网盘源以回调保存为准
			"chat":      false,
			"comments":  false,
			"close":     map[string]any{"visible": true, "text": "关闭文件"}, // 配合页面 onRequestClose 返回来源页
			"unit":      "cm",
			"features":  map[string]any{"spellcheck": false},
		},
		"user": map[string]any{"id": fmt.Sprint(ident.ID), "name": firstNonEmpty(ident.Nickname, ident.Username)},
	}
	if !viewOnly {
		editor["mode"] = "edit"
		callbackTicket, err := m.SignTicket(r.Context(),
			TicketPayload{Src: src, NDP: ticket.NDP, Ref: ticket.Ref, Name: name}, cbTTL)
		if err != nil {
			fail(w, http.StatusInternalServerError, codeUpstream, "票据签发失败")
			return
		}
		editor["callbackUrl"] = fmt.Sprintf("%s/api/v1/office/callback/%s", base, callbackTicket)
	} else {
		editor["mode"] = "view"
	}
	config := map[string]any{
		"documentType": docType,
		"document":     doc,
		"editorConfig": editor,
		"type":         "desktop",
	}
	// OnlyOffice JWT：config 全量签名（OO_JWT_SECRET 未配置则不带 token）。
	if ooToken, err := m.SignOOJWT(r.Context(), config); err == nil {
		config["token"] = ooToken
	}
	ok(w, map[string]any{"config": config, "public_url": public, "publicUrl": public}, "")
}

// validNDPPath 网盘路径轻量越界校验（细粒度校验在网盘侧 NormRel，此处拦明显越界）。
func validNDPPath(p string) bool {
	p = strings.TrimSpace(strings.ReplaceAll(p, "\\", "/"))
	if p == "" || !strings.HasPrefix(p, "/") {
		return false
	}
	for _, seg := range strings.Split(p, "/") {
		if seg == ".." {
			return false
		}
	}
	return true
}

// baseNameOfPath 路径末段（文件名）。
func baseNameOfPath(p string) string {
	segs := strings.Split(strings.ReplaceAll(p, "\\", "/"), "/")
	for i := len(segs) - 1; i >= 0; i-- {
		if segs[i] != "" {
			return segs[i]
		}
	}
	return ""
}

// validSheetRange 日期范围校验（口径照抄 worklog sheetRange：YYYY-MM-DD、from≤to、≤31 天）；
// 不合法时已应答。
func validSheetRange(w http.ResponseWriter, from, to string) bool {
	if !isDate(from) || !isDate(to) || from > to {
		fail(w, http.StatusBadRequest, codeParam, "日期格式应为 YYYY-MM-DD，且 from 不晚于 to")
		return false
	}
	f, _ := time.Parse("2006-01-02", from)
	t, _ := time.Parse("2006-01-02", to)
	if t.Sub(f).Hours()/24+1 > 31 {
		fail(w, http.StatusBadRequest, codeParam, "日期范围不能超过 31 天")
		return false
	}
	return true
}

// isDate YYYY-MM-DD 且可解析。
func isDate(s string) bool {
	if len(s) != 10 {
		return false
	}
	_, err := time.Parse("2006-01-02", s)
	return err == nil
}

// handleRaw GET /raw/:ticket/:name?：免登录回源（票据即鉴权）。
func (m *Module) handleRaw(w http.ResponseWriter, r *http.Request) {
	p, err := m.VerifyTicket(r.Context(), chi.URLParam(r, "ticket"))
	if err != nil {
		fail(w, http.StatusUnauthorized, codeBadTicket, "文件票据无效或已过期")
		return
	}
	name := chi.URLParam(r, "name")
	if name == "" {
		name = p.Name
	}
	data, contentType, err := m.fetchDoc(r.Context(), p, name)
	if err != nil {
		var re *RefError
		if errors.As(err, &re) {
			refFail(w, err) // 桥业务错误（范围/归属等）直出
			return
		}
		if errors.Is(err, ErrDocNotFound) {
			fail(w, http.StatusNotFound, codeNotFound, "文档不存在")
			return
		}
		fail(w, http.StatusBadGateway, codeUpstream, "文档回源失败")
		return
	}
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Disposition", `inline; filename="`+strings.ReplaceAll(name, `"`, "")+`"`)
	_, _ = w.Write(data)
}

// fetchDoc 按票据来源取文档。
func (m *Module) fetchDoc(ctx context.Context, p TicketPayload, name string) ([]byte, string, error) {
	if p.Src == "ndp" {
		if m.Netdisk == nil || p.NDP == nil {
			return nil, "", ErrDocNotFound
		}
		return m.Netdisk.Fetch(ctx, *p.NDP, name)
	}
	src := m.Sources[p.Src]
	if src == nil {
		return nil, "", ErrDocNotFound
	}
	data, fetchedName, err := src.Fetch(ctx, p.Ref)
	if err != nil {
		return nil, "", err
	}
	_ = fetchedName
	return data, "", nil
}

// handleCallback POST /callback/:ticket：保存回调（OnlyOffice JWT + 票据双校验；应答 {"error":0}）。
func (m *Module) handleCallback(w http.ResponseWriter, r *http.Request) {
	p, err := m.VerifyTicket(r.Context(), chi.URLParam(r, "ticket"))
	if err != nil {
		writePlain(w, http.StatusOK, `{"error":1}`)
		return
	}
	rawBody, _ := io.ReadAll(io.LimitReader(r.Body, 4<<20))
	claims, err := m.VerifyOOJWT(r.Context(), callbackJWT(r, rawBody))
	if err != nil {
		writePlain(w, http.StatusOK, `{"error":1}`)
		return
	}
	// 回调体取值：优先 JWT 载荷（v10 头位 token 还会包一层 {payload: 回调体}，须解包；
	// 仅 body.token 携带时载荷同样在 JWT 内），回落 rawBody JSON（OO 未启用 JWT 口径）。
	var cb struct {
		Status int    `json:"status"`
		URL    string `json:"url"` // status=2 保存产物地址
	}
	if inner, ok := claims["payload"].(map[string]any); ok {
		claims = inner
	}
	if st, ok := claims["status"].(float64); ok {
		cb.Status = int(st)
		cb.URL, _ = claims["url"].(string)
	} else {
		_ = json.Unmarshal(rawBody, &cb)
	}
	// 只读源无 callbackUrl（防御：即便被调用也不写回）。
	if viewOnlySources(p.Src) || m.Netdisk == nil || p.NDP == nil {
		writePlain(w, http.StatusOK, `{"error":0}`)
		return
	}
	// status=2 会话结束保存 / status=6 强制保存（forcesave）：拉回产物写回网盘原路径。
	if (cb.Status == 2 || cb.Status == 6) && cb.URL != "" {
		saveURL := cb.URL
		// 保存产物地址改写：OO 容器返回自身可达地址，本服务经 OO_INTERNAL_URL 拉回（Node 口径）。
		if internal := strings.TrimRight(m.configValue(r.Context(), "OO_INTERNAL_URL"), "/"); internal != "" {
			if u, perr := url.Parse(cb.URL); perr == nil && u.Path != "" {
				saveURL = internal + u.Path
				if u.RawQuery != "" {
					saveURL += "?" + u.RawQuery
				}
			}
		}
		resp, err := http.Get(saveURL)
		if err != nil {
			writePlain(w, http.StatusOK, `{"error":1}`)
			return
		}
		defer resp.Body.Close()
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 512<<20))
		if len(data) == 0 {
			writePlain(w, http.StatusOK, `{"error":1}`)
			return
		}
		if err := m.Netdisk.WriteBack(r.Context(), *p.NDP, data); err != nil {
			writePlain(w, http.StatusOK, `{"error":1}`)
			return
		}
	}
	writePlain(w, http.StatusOK, `{"error":0}`)
}

// callbackJWT 取 OnlyOffice 回调 JWT：Authorization Bearer / X-Callback-JWT / body.token 兼容。
func callbackJWT(r *http.Request, rawBody []byte) string {
	if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
		return strings.TrimPrefix(h, "Bearer ")
	}
	if t := r.Header.Get("X-Callback-JWT"); t != "" {
		return t
	}
	var body struct {
		Token string `json:"token"`
	}
	_ = json.Unmarshal(rawBody, &body)
	return body.Token
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
