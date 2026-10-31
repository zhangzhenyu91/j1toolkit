package office

// ONLYOFFICE 模块装配：/api/v1/office 路由（status / config 签发 / raw 回源 / callback 保存回调）。
// 回调应答为 OnlyOffice 约定格式 {"error":0}（不走统一信封）。

import (
	"context"
	"crypto/sha1"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"

	"shade/internal/auth"
	"shade/internal/httpapi"

	"shade/internal/outbound"
)

// Module ONLYOFFICE 对接模块。
type Module struct {
	// Cfg 运行时配置（OO_PUBLIC_URL / OO_INTERNAL_URL / OO_CALLBACK_BASE / OO_JWT_SECRET / OO_TICKET_SECRET）。
	Cfg ConfigReader
	// DB 生效班组解析（httpapi.ResolveTeam；nil 时超管指定班组不校验存在性，测试/旧装配口径）。
	DB *sql.DB
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
	// SaveFetchTimeout 保存回调产物拉回超时（0 取默认 5 分钟；测试可下调）。
	SaveFetchTimeout time.Duration
	// SaveMaxBytes 保存回调产物字节预算（0 取默认 512MiB；超限显式报错不写回）。
	SaveMaxBytes int64
	// SaveTempDir 保存回调临时文件目录（空取系统临时目录；测试可注入）。
	SaveTempDir string

	// rawSem 回源并发闸（8）：大文档流式转发仍占用上游连接与带宽，避免 OO 批量打开拖垮网盘。
	rawSem     chan struct{}
	rawSemOnce sync.Once
}

// maxConcurrentRawFetches Office raw 回源并发上限。
const maxConcurrentRawFetches = 8

// acquireRaw 获取回源名额（非阻塞）；满额返回 false，由调用方回 503。
func (m *Module) acquireRaw() bool {
	m.rawSemOnce.Do(func() { m.rawSem = make(chan struct{}, maxConcurrentRawFetches) })
	select {
	case m.rawSem <- struct{}{}:
		return true
	default:
		return false
	}
}

func (m *Module) releaseRaw() { <-m.rawSem }

// ErrDocTooLarge 文档回源/产物超出字节预算。
var ErrDocTooLarge = errors.New("文档超出大小限制")

const (
	defaultSaveFetchTimeout = 5 * time.Minute
	defaultSaveMaxBytes     = int64(512 << 20)
)

func (m *Module) saveFetchTimeout() time.Duration {
	if m.SaveFetchTimeout > 0 {
		return m.SaveFetchTimeout
	}
	return defaultSaveFetchTimeout
}

func (m *Module) saveMaxBytes() int64 {
	if m.SaveMaxBytes > 0 {
		return m.SaveMaxBytes
	}
	return defaultSaveMaxBytes
}

// DefaultSaveTempDir 保存回调 staging 默认目录（容器数据卷下，持久化便于启动清理）。
const DefaultSaveTempDir = "data/office-staging"

// saveTempDir 生效临时目录：显式字段 > OO_SAVE_TEMP_DIR 运行时配置 > 默认持久数据目录。
func (m *Module) saveTempDir(ctx context.Context) string {
	if m.SaveTempDir != "" {
		return m.SaveTempDir
	}
	if v := strings.TrimSpace(m.configValue(ctx, "OO_SAVE_TEMP_DIR")); v != "" {
		return v
	}
	return DefaultSaveTempDir
}

// cleanupSaveTemp 清理目录内超期 oo-save-* 临时文件（崩溃残留），返回删除数量。
func cleanupSaveTemp(dir string, olderThan time.Duration) int {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0
	}
	removed := 0
	now := time.Now()
	for _, e := range entries {
		if e.IsDir() || !strings.HasPrefix(e.Name(), "oo-save-") {
			continue
		}
		info, err := e.Info()
		if err != nil || now.Sub(info.ModTime()) < olderThan {
			continue
		}
		if os.Remove(filepath.Join(dir, e.Name())) == nil {
			removed++
		}
	}
	return removed
}

// StartSaveTempJanitor 进程启动清理一次并周期清理 staging（ctx 结束即停止）。
func (m *Module) StartSaveTempJanitor(ctx context.Context) {
	dir := m.saveTempDir(ctx)
	_ = os.MkdirAll(dir, 0o700)
	cleanupSaveTemp(dir, saveTempStaleAfter)
	go func() {
		ticker := time.NewTicker(saveTempJanitorInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				cleanupSaveTemp(dir, saveTempStaleAfter)
			}
		}
	}()
}

// saveTempJanitorInterval / saveTempStaleAfter staging 周期清理与超期阈值。
const (
	saveTempJanitorInterval = 30 * time.Minute
	saveTempStaleAfter      = time.Hour
)

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
			fail(w, http.StatusForbidden, codeAppDenied, "暂无该应用的使用权限，请联系管理员开通")
			return auth.Identity{}, false
		}
	}
	return ident, true
}

// resolveTeam 按身份收敛生效班组：超管可指定班组（经 httpapi.ResolveTeam 校验存在/启用，
// 缺省回落本人班组/默认班组）；其他角色（含班管）只能本班——显式他班 team_id 直接 403，
// 不静默改写成自家班组。它把「身份边界」放在 /config 签发之前，网盘桥仍保留归属兜底。
func (m *Module) resolveTeam(ctx context.Context, ident auth.Identity, wanted int64) (int64, error) {
	if wanted <= 0 {
		wanted = ident.TeamID
	}
	if ident.Role != "admin" {
		if wanted != ident.TeamID {
			return 0, &RefError{Status: http.StatusForbidden, Code: codeForbidden, Message: "无权访问其他班组的数据"}
		}
		return wanted, nil
	}
	if m.DB == nil {
		return wanted, nil
	}
	team, err := httpapi.ResolveTeam(ctx, m.DB, httpapi.IdentityAdapter{TeamID: ident.TeamID, Role: ident.Role}, wanted)
	if err != nil {
		return 0, &RefError{Status: http.StatusBadRequest, Code: codeNoTeam, Message: "无可用班组"}
	}
	return team.ID, nil
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

// defaultCallbackBase OO_CALLBACK_BASE 未配置时的推导基址（Node config.office.callbackBase 同口径）：
// compose 容器名 + 内外同端口 http://j1toolkit:<PORT>（PORT 取 env，缺省 3000）。空基址会把
// document.url / callbackUrl 签成站内相对路径，OnlyOffice 容器无法回源，编辑器停在加载态。
func defaultCallbackBase() string {
	port := strings.TrimSpace(os.Getenv("PORT"))
	if port == "" {
		port = "3000"
	}
	return "http://j1toolkit:" + port
}

// requireOOEnabled 功能开关与 OO JWT 密钥校验（Node enabled()/jwtSecret 口径：两者缺一拒发配置）。
func (m *Module) requireOOEnabled(w http.ResponseWriter, r *http.Request) bool {
	if m.configValue(r.Context(), "OO_PUBLIC_URL") == "" {
		fail(w, http.StatusServiceUnavailable, codeNotConfigured, "在线编辑未配置（OO_PUBLIC_URL）")
		return false
	}
	if _, err := m.ooSecret(r.Context()); err != nil {
		fail(w, http.StatusServiceUnavailable, codeNotConfigured, "在线编辑未配置（OO_JWT_SECRET）")
		return false
	}
	return true
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
		fail(w, http.StatusBadRequest, codeParam, "参数错误：src")
		return
	}
	// 校验顺序照抄 Node：view-only 源先开关/密钥后权限；网盘源先权限后开关/密钥。
	if src != "ndp" && !m.requireOOEnabled(w, r) {
		return
	}
	// 应用权限按来源分派（口径同 requireApp：netdisk / work-log / safe-day / tree-hazard / crossing）。
	appKey := map[string]string{
		"ndp": "netdisk", "wls": "work-log", "wlr": "work-log",
		"sdr": "safe-day", "thd": "tree-hazard", "crs": "crossing",
	}[src]
	ident, okAuth := m.authed(w, r, appKey)
	if !okAuth {
		return
	}
	if src == "ndp" && !m.requireOOEnabled(w, r) {
		return
	}
	team := int64(ident.TeamID)
	if src != "sdr" {
		resolved, err := m.resolveTeam(r.Context(), ident, int64(body.Team))
		if err != nil {
			refFail(w, err)
			return
		}
		team = resolved
	}

	// ---- 票据载荷与引用（ref 直传优先；否则按扁平参数组装） ----
	ticket := TicketPayload{Src: src}
	ref := strings.TrimSpace(body.Ref)
	switch src {
	case "ndp":
		rel, okPath := normNDPPath(body.Path)
		if !okPath {
			fail(w, http.StatusBadRequest, codeParam, "路径不合法")
			return
		}
		ticket.NDP = &NDP{Space: body.Space, User: ident.Username, Path: rel, Team: team}
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
		name = baseNameOfPath(ticket.NDP.Path) // 网盘文件名取规范化路径末段（office.html 不透传 name）
	}
	ticket.Name = name
	if name == "" {
		fail(w, http.StatusBadRequest, codeParam, "参数错误：name")
		return
	}
	docType, editable := DocTypeOf(name)
	if docType == "" {
		fail(w, http.StatusBadRequest, codeBadFormat, "该格式不支持在线打开")
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
	if base == "" {
		base = defaultCallbackBase()
	}
	public := strings.TrimRight(m.configValue(r.Context(), "OO_PUBLIC_URL"), "/")
	fileTTL := m.ttlSeconds(r.Context(), "OO_FILE_TICKET_TTL", 600)
	cbTTL := m.ttlSeconds(r.Context(), "OO_CALLBACK_TICKET_TTL", 43200)
	token, err := m.SignTicket(r.Context(), ticket, fileTTL)
	if err != nil {
		fail(w, http.StatusInternalServerError, codeUpstream, "票据签发失败")
		return
	}
	fileURL := fmt.Sprintf("%s/api/v1/office/raw/%s/%s", base, token, url.PathEscape(name))
	// 权限与外壳（Node 口径）：编辑态 review/comment 开启、forcesave 触发 status 6 回调即时写回；
	// 只读态强制 view、不配 forcesave。
	perm := map[string]any{"download": true, "print": true, "edit": !viewOnly, "review": !viewOnly, "comment": !viewOnly}
	customization := map[string]any{
		"close":    map[string]any{"visible": true, "text": "关闭文件"}, // 配合页面 onRequestClose 返回来源页
		"unit":     "cm",
		"features": map[string]any{"spellcheck": false},
	}
	if !viewOnly {
		customization["forcesave"] = true // 保存按钮触发 status 6 回调即时写回（仅编辑模式有意义）
	}
	doc := map[string]any{
		"fileType":    fileExt(name),
		"key":         docVer,
		"title":       name,
		"url":         fileURL,
		"permissions": perm,
	}
	editor := map[string]any{
		"lang":          "zh-CN",
		"region":        "zh-CN",
		"customization": customization,
		"user":          map[string]any{"id": fmt.Sprint(ident.ID), "name": firstNonEmpty(ident.Nickname, ident.Username)},
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
	// OnlyOffice JWT：config 全量签名（OO_JWT_SECRET 已由 requireOOEnabled 保证，Node 同口径必签）。
	ooToken, err := m.SignOOJWT(r.Context(), config)
	if err != nil {
		fail(w, http.StatusInternalServerError, codeUpstream, "票据签发失败")
		return
	}
	config["token"] = ooToken
	ok(w, map[string]any{"config": config, "public_url": public, "publicUrl": public}, "")
}

// normNDPPath 网盘路径规范化（Node office normRel 同口径）：反斜杠转正、补前导斜杠、压缩重复
// 斜杠，拒 "."/".." 越界段与超长；文档路径必须落到文件（空间根 "/" 判非法）。合法返回以 / 开头的
// 规范化空间内相对路径，签发票据与定名共用（细粒度越界校验仍在网盘侧 NormRel，此处拦明显越界）。
func normNDPPath(input string) (string, bool) {
	p := strings.TrimSpace(strings.ReplaceAll(strings.TrimSpace(input), "\\", "/"))
	if p == "" {
		return "", false
	}
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	segs := strings.Split(p, "/")
	clean := make([]string, 0, len(segs))
	for _, seg := range segs {
		if seg == "" {
			continue
		}
		if seg == ".." || seg == "." {
			return "", false
		}
		clean = append(clean, seg)
	}
	if len(clean) == 0 {
		return "", false // 空间根不是文档路径
	}
	joined := "/" + strings.Join(clean, "/")
	if len(joined) > 400 {
		return "", false
	}
	return joined, true
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
// 错误一律纯文本（OO 不消费 {code,message,data} 信封，Node res.status().type('text') 同口径）；
// URL 尾段 :name 仅供 OO 按扩展名识别格式，实际取数与定名全按票据载荷。
func (m *Module) handleRaw(w http.ResponseWriter, r *http.Request) {
	p, err := m.VerifyTicket(r.Context(), chi.URLParam(r, "ticket"))
	if err != nil || !knownSrc(p.Src) {
		writeText(w, http.StatusUnauthorized, "回源票据无效或已过期")
		return
	}
	name := p.Name
	if name == "" {
		name = chi.URLParam(r, "name")
	}
	if !m.acquireRaw() {
		writeText(w, http.StatusServiceUnavailable, "回源繁忙，请稍后再试")
		return
	}
	defer m.releaseRaw()

	// 网盘源（ndp）优先流式回源：大文档边读边转发，不整读进内存，响应结束关闭上游 body。
	if p.Src == "ndp" && m.Netdisk != nil && p.NDP != nil {
		if sd, ok := m.Netdisk.(NetdiskStreamDoc); ok {
			rc, fetchedCT, size, err := sd.FetchStream(r.Context(), *p.NDP, name)
			if err != nil {
				rawFetchFail(w, err)
				return
			}
			defer rc.Close()
			writeRawHeaders(w, name, fetchedCT, size)
			// 每块写后 flush：大文档首块立即到达 OO，不必等 32KiB 缓冲填满或整篇读完。
			_, _ = io.Copy(&flushWriter{w: w}, rc)
			return
		}
	}

	data, fetchedCT, err := m.fetchDoc(r.Context(), p, name)
	if err != nil {
		rawFetchFail(w, err)
		return
	}
	writeRawHeaders(w, name, fetchedCT, int64(len(data)))
	_, _ = w.Write(data)
}

// flushWriter 每次 Write 后刷新响应（流式回源的即时性保障；flush 失败不影响数据写入）。
type flushWriter struct{ w http.ResponseWriter }

func (f *flushWriter) Write(p []byte) (int, error) {
	n, err := f.w.Write(p)
	if n > 0 {
		_ = http.NewResponseController(f.w).Flush()
	}
	return n, err
}

// writeRawHeaders 回源响应头：Content-Type 按扩展名给准（上游可能只回 octet-stream，
// OO 依赖它判格式）；已知大小写 Content-Length，便于 OO 展示进度。
func writeRawHeaders(w http.ResponseWriter, name, fetchedCT string, size int64) {
	ct := rawMIME(name)
	if ct == "" {
		ct = fetchedCT
	}
	if ct == "" {
		ct = "application/octet-stream"
	}
	w.Header().Set("Content-Type", ct)
	w.Header().Set("Content-Disposition", "inline; filename*=UTF-8''"+url.PathEscape(name))
	if size > 0 {
		w.Header().Set("Content-Length", strconv.FormatInt(size, 10))
	}
	w.WriteHeader(http.StatusOK)
}

// knownSrc 票据来源白名单（未知来源按无效票据处理，Node 同口径）。
func knownSrc(src string) bool {
	switch src {
	case "ndp", "wls", "sdr", "wlr", "thd", "crs":
		return true
	}
	return false
}

// rawFetchFail 回源失败应答（纯文本；状态/文案由桥错误携带，缺省 500）。
func rawFetchFail(w http.ResponseWriter, err error) {
	var re *RefError
	if errors.As(err, &re) {
		status := re.Status
		if status == 0 {
			status = http.StatusBadRequest
		}
		writeText(w, status, re.Message)
		return
	}
	if errors.Is(err, ErrDocNotFound) {
		writeText(w, http.StatusNotFound, "文档不存在")
		return
	}
	if isTooLargeErr(err) {
		writeText(w, http.StatusRequestEntityTooLarge, "文件超出大小限制")
		return
	}
	writeText(w, http.StatusInternalServerError, "回源失败，请稍后再试")
}

// rawMIME 回源 Content-Type 白名单（Node RAW_MIME 同口径，网盘 PREVIEW_MIME 扩展 pdf）。
func rawMIME(name string) string {
	switch fileExt(name) {
	case "doc":
		return "application/msword"
	case "docx":
		return "application/vnd.openxmlformats-officedocument.wordprocessingml.document"
	case "xls":
		return "application/vnd.ms-excel"
	case "xlsx":
		return "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"
	case "ppt":
		return "application/vnd.ms-powerpoint"
	case "pptx":
		return "application/vnd.openxmlformats-officedocument.presentationml.presentation"
	case "pdf":
		return "application/pdf"
	}
	return ""
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
// 口径照抄 Node：JWT 两个携带位（Bearer 头 / body.token）都认、头位载荷解包 {payload: 回调体}
// （v10 DocsCoServer.js sendServerRequest 口径）；验签失败或载荷缺数值型 status 一律 401
// {error:1,message}；写回失败 200 {error:1,message:"写回网盘失败"}。
func (m *Module) handleCallback(w http.ResponseWriter, r *http.Request) {
	rawBody, _ := io.ReadAll(io.LimitReader(r.Body, 4<<20))
	claims := m.callbackClaims(r, rawBody)
	st, okStatus := claimNumber(claims, "status")
	if !okStatus {
		writeOO(w, http.StatusUnauthorized, 1, "回调签名校验失败")
		return
	}
	p, err := m.VerifyTicket(r.Context(), chi.URLParam(r, "ticket"))
	if err != nil {
		writeOO(w, http.StatusUnauthorized, 1, "回调票据无效或已过期")
		return
	}
	// 只读源无 callbackUrl（防御：即便被调用也不写回）。
	if viewOnlySources(p.Src) || m.Netdisk == nil || p.NDP == nil {
		writeOO(w, http.StatusOK, 0, "")
		return
	}
	// 2=会话结束保存；6=强制保存（customization.forcesave）：拉回产物写回网盘原路径。
	if st == 2 || st == 6 {
		if err := m.writeBackFromURL(r.Context(), *p.NDP, claimString(claims, "url")); err != nil {
			writeCallbackWriteBackErr(w, err)
			return
		}
	}
	// 3/7=文档损坏：仅记录，不写回；1=编辑中 / 4=关闭无改动 / 其余：仅应答确认。
	writeOO(w, http.StatusOK, 0, "")
}

// callbackClaims 校验 OnlyOffice 回调 JWT 并解包（头位 token 载荷再包一层 {payload: 回调体}）；
// Bearer 头与 body.token 两个携带位依次尝试（头位验签失败回落 body 位，Node 同口径）。
func (m *Module) callbackClaims(r *http.Request, rawBody []byte) map[string]any {
	tokens := make([]string, 0, 2)
	if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
		tokens = append(tokens, strings.TrimPrefix(h, "Bearer "))
	}
	var body struct {
		Token string `json:"token"`
	}
	_ = json.Unmarshal(rawBody, &body)
	if body.Token != "" {
		tokens = append(tokens, body.Token)
	}
	for _, tk := range tokens {
		claims, err := m.VerifyOOJWT(r.Context(), tk)
		if err != nil {
			continue
		}
		if inner, ok := claims["payload"].(map[string]any); ok {
			claims = inner
		}
		return claims
	}
	return nil
}

// claimNumber 载荷数值字段（Node typeof status === 'number' 同口径：仅 JSON number）。
func claimNumber(claims map[string]any, key string) (int, bool) {
	switch v := claims[key].(type) {
	case float64:
		return int(v), true
	case int:
		return v, true
	}
	return 0, false
}

func claimString(claims map[string]any, key string) string {
	s, _ := claims[key].(string)
	return s
}

// writeBackFromURL 保存产物拉回并写回网盘原路径（Node 写回口径：地址改写 OO_INTERNAL_URL + path；
// 地址无效 / 非 2xx / 拉回失败 / 超字节预算 / 空产物 / 写回失败都判失败）。
// 网盘实现 NetdiskStreamWriter 时优先流式写回（大文档不整读进内存）。
func (m *Module) writeBackFromURL(ctx context.Context, ndp NDP, rawURL string) error {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || u.Path == "" {
		return errors.New("回调产物地址无效")
	}
	saveURL := rawURL
	if internal := strings.TrimRight(m.configValue(ctx, "OO_INTERNAL_URL"), "/"); internal != "" {
		saveURL = internal + u.Path
		if u.RawQuery != "" {
			saveURL += "?" + u.RawQuery
		}
	}
	ctx, cancel := context.WithTimeout(ctx, m.saveFetchTimeout())
	defer cancel()
	req, err := http.NewRequestWithContext(outbound.WithServiceName(ctx, "office"), http.MethodGet, saveURL, nil)
	if err != nil {
		return err
	}
	resp, err := (&http.Client{}).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("回调产物回源失败（HTTP %d）", resp.StatusCode)
	}
	max := m.saveMaxBytes()
	if resp.ContentLength > max {
		return fmt.Errorf("%w（%d > %d 字节）", ErrDocTooLarge, resp.ContentLength, max)
	}
	// 先落本地临时文件（limit+1）：空体/超限/读取失败都在触达上游前结束，绝不写入空文件或半截文件。
	tmpDir := m.saveTempDir(ctx)
	if err := os.MkdirAll(tmpDir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(tmpDir, "oo-save-*.bin")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	written, copyErr := io.Copy(tmp, io.LimitReader(resp.Body, max+1))
	closeErr := tmp.Close()
	if copyErr != nil {
		return copyErr
	}
	if closeErr != nil {
		return closeErr
	}
	if written == 0 {
		return errors.New("回调产物为空")
	}
	if written > max {
		return fmt.Errorf("%w（> %d 字节）", ErrDocTooLarge, max)
	}
	f, err := os.Open(tmpName)
	if err != nil {
		return err
	}
	defer f.Close()
	if sw, ok := m.Netdisk.(NetdiskStreamWriter); ok {
		return sw.WriteFileStream(ctx, ndp.Space, ndp.User, ndp.Team, ndp.Path, f, written)
	}
	data, err := io.ReadAll(f)
	if err != nil {
		return err
	}
	return m.Netdisk.WriteBack(ctx, ndp, data)
}

// writeCallbackWriteBackErr OO 回调契约：写回失败一律 200 + {"error":1}（避免 OO 无意义重试），
// 仅文案区分超大小；普通 HTTP（/config、/raw）仍按 413 映射。
func writeCallbackWriteBackErr(w http.ResponseWriter, err error) {
	msg := "写回网盘失败"
	if isTooLargeErr(err) {
		msg = "文件超出大小限制"
	}
	writeOO(w, http.StatusOK, 1, msg)
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
