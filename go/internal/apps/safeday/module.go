// Package safeday 迁移 server/src/safeday：安全日活动记录（自 SafeDayLogs 独立服务合并）。
// 响应保持 { ok, error, ... } 信封；/callback 凭 SAFEDAY_CALLBACK_TOKEN 校验（fail-closed——
// token 未配置或不符一律 403）；其余接口需登录 + safe-day 应用权限。download 两路由被
// httpapi.publicAuthPath 放行到统一鉴权中间件之外（微软查看器回源仅能带 ?token=），须自行
// 验证登录（Authorization: Bearer 或旧版 ?token= JWT），绝不匿名放行。
// 班组隔离：记录带 team 字段（班组名），产物存 docs/{班组名}/ 子目录；超管可 ?team_id= 指定或 all 全部。
// 生成链路：学习文件合并后传 COS 拿公网 URL，以 string 变量（url/date/class）调 Dify 工作流；
// 工作流只产出六段文字并经 /callback 回传 → 后端套模板渲染 docx 落盘；COS 源文件为中转性质，
// 记录到终态（done/failed/删除）即删。生成表单/转换/工作流/对象存储/网盘均可注入，测试不实连外部服务。
package safeday

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"shade/internal/auth"
	"shade/internal/compat/imagepdf"
	"shade/internal/httpapi"
	"shade/internal/logging"
)

// AppPermit 校验用户是否拥有指定应用（app_key）的使用权限（对接 sys_user_app；nil 表示未接入，不拦截）。
type AppPermit interface {
	Allow(ctx context.Context, userID int64, appKey string) (bool, error)
}

// ConfigReader 运行时配置读取（*config.Store 满足；测试可注入桩）。
type ConfigReader interface {
	Get(ctx context.Context, key string) (string, error)
}

// TokenAuthenticator 令牌鉴权适配层（*auth.Service 满足；测试可注入桩）：
// 供 download 自验证旧版 ?token= JWT 用（回源无法带请求头）。
type TokenAuthenticator interface {
	Authenticate(ctx context.Context, token string) (auth.Identity, error)
}

// AppLogger 应用结构化日志写入（*logging.Logger 满足；nil 时降级标准库 log）。
type AppLogger interface {
	Info(ctx context.Context, category, event, messageCN, raw string, fields map[string]any) error
	Error(ctx context.Context, category, event, messageCN, raw string, fields map[string]any) error
}

// 编译期契约：生产装配按此注入。
var (
	_ AppLogger          = (*logging.Logger)(nil)
	_ TokenAuthenticator = (*auth.Service)(nil)
)

// ObjectStore 对象存储适配层（COS）：真实外部服务必须经此注入，测试不得实际调用。
type ObjectStore interface {
	// Put 上传对象，返回公开访问地址。
	Put(ctx context.Context, key string, data []byte, contentType string) (url string, err error)
	// PublicURL 按对象键生成当前访问地址。
	PublicURL(key string) string
	// Delete 删除对象。
	Delete(ctx context.Context, key string) error
}

// NetdiskSaveRequest 保存到网盘入参。
type NetdiskSaveRequest struct {
	Space string // my / public
	Dir   string // 目标空间内多级相对路径
	Name  string // 文件名
	Body  []byte // 文件内容
}

// NetdiskSaver 网盘保存适配层（netdisk 应用侧实现；可注入；nil 时 save-netdisk 报未接入）。
type NetdiskSaver interface {
	SaveToNetdisk(ctx context.Context, user auth.Identity, req NetdiskSaveRequest) (string, error)
}

// 上传限制与白名单（与 Node multer / ALLOWED_EXT 同口径）。
const (
	maxUploadFiles    = 10
	maxUploadFileSize = 50 * 1024 * 1024
)

var allowedExt = []string{"pdf", "doc", "docx", "ppt", "pptx", "xls", "xlsx"}

// mimeByExt COS 上传 ContentType 按扩展名（与 ALLOWED_EXT 同族，缺省 octet-stream）。
var mimeByExt = map[string]string{
	"pdf":  "application/pdf",
	"doc":  "application/msword",
	"docx": "application/vnd.openxmlformats-officedocument.wordprocessingml.document",
	"ppt":  "application/vnd.ms-powerpoint",
	"pptx": "application/vnd.openxmlformats-officedocument.presentationml.presentation",
	"xls":  "application/vnd.ms-excel",
	"xlsx": "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
}

// dateRe 生成日期格式 YYYY.MM.DD。
var dateRe = regexp.MustCompile(`^\d{4}\.\d{2}\.\d{2}$`)

// Module 是安全日应用模块。
type Module struct {
	DB        *sql.DB
	Permit    AppPermit
	Cfg       ConfigReader
	Auth      TokenAuthenticator // 令牌鉴权（download 自验证 ?token=/Bearer JWT；nil 时 download 只认上下文身份）
	Log       AppLogger          // 应用日志（可注入；nil 降级标准库 log）
	Store     *RecordStore
	Dify      WorkflowRunner
	Converter PDFConverter
	COS       ObjectStore
	Netdisk   NetdiskSaver
	Renderer  *DocxRenderer
}

// New 构造安全日模块（数据目录从 env/配置给定；外部客户端装配后注入）。
// Renderer.TemplatePath 按部署布局解析（容器 assets/safeday/… 优先、仓根 server/assets/… 回退）。
func New(db *sql.DB, store *RecordStore) *Module {
	return &Module{DB: db, Store: store, Renderer: &DocxRenderer{TemplatePath: ResolveTemplatePath()}}
}

// RegisterRoutes 注册 /api/v1/safeday 下全部路由（挂载在 /api/v1 之下）。
func (m *Module) RegisterRoutes(r chi.Router) {
	r.Route("/safeday", func(r chi.Router) {
		r.Post("/callback", m.callback)
		r.Get("/form-meta", m.withApp(m.formMeta))
		r.Post("/generate", m.withApp(m.generate))
		r.Get("/records", m.withApp(m.records))
		r.Get("/records/{id}/download", m.withDownload(m.download))
		r.Get("/records/{id}/download/{name}", m.withDownload(m.download))
		r.Post("/records/{id}/save-netdisk", m.withApp(m.saveNetdisk))
		r.Get("/records/{id}/preview", m.withApp(m.preview))
		r.Delete("/records/{id}", m.withApp(m.delete))
	})
}

// handlerFunc 带身份的路由处理函数。
type handlerFunc func(w http.ResponseWriter, r *http.Request, ident auth.Identity)

// withApp 登录 + safe-day 应用权限。
func (m *Module) withApp(next handlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ident, ok := httpapi.IdentityFromContext(r.Context())
		if !ok {
			writeFail(w, http.StatusUnauthorized, "未登录或登录已过期", nil)
			return
		}
		if m.Permit != nil {
			allow, err := m.Permit.Allow(r.Context(), ident.ID, "safe-day")
			if err != nil {
				writeFail(w, http.StatusInternalServerError, "服务器开小差了，请稍后再试", nil)
				return
			}
			if !allow {
				writeFail(w, http.StatusForbidden, "暂无该应用的使用权限，请联系管理员开通", nil)
				return
			}
		}
		next(w, r, ident)
	}
}

// withDownload 下载鉴权：download 两路由被 httpapi.publicAuthPath 放行到统一鉴权中间件之外
// （微软查看器回源仅能带 ?token=），此处自行验证登录后放行，绝不匿名；
// 凭证取中间件上下文身份或 Authorization: Bearer / 旧版 ?token= query JWT。其余记录接口仍走
// 统一登录门控（withApp）。
func (m *Module) withDownload(next handlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ident, ok := m.authenticate(r)
		if !ok {
			writeFail(w, http.StatusUnauthorized, "未登录或登录已过期", nil)
			return
		}
		if m.Permit != nil {
			allow, err := m.Permit.Allow(r.Context(), ident.ID, "safe-day")
			if err != nil {
				writeFail(w, http.StatusInternalServerError, "服务器开小差了，请稍后再试", nil)
				return
			}
			if !allow {
				writeFail(w, http.StatusForbidden, "暂无该应用的使用权限，请联系管理员开通", nil)
				return
			}
		}
		next(w, r, ident)
	}
}

// authenticate 解析登录身份：优先统一鉴权中间件已验证的上下文身份；
// 否则取 Authorization: Bearer 或旧版 ?token= query 中的 JWT 交给 TokenAuthenticator 自验证
// （对齐 Node utils/tokenQuery.js 的 query→Authorization 映射语义）。
func (m *Module) authenticate(r *http.Request) (auth.Identity, bool) {
	if ident, ok := httpapi.IdentityFromContext(r.Context()); ok {
		return ident, true
	}
	token := requestToken(r)
	if token == "" || m.Auth == nil {
		return auth.Identity{}, false
	}
	ident, err := m.Auth.Authenticate(r.Context(), token)
	if err != nil {
		return auth.Identity{}, false
	}
	return ident, true
}

// requestToken 取请求携带的 JWT：Authorization: Bearer 优先，回退旧版 ?token= query。
func requestToken(r *http.Request) string {
	value := strings.TrimSpace(r.Header.Get("Authorization"))
	if len(value) >= 7 && strings.EqualFold(value[:7], "Bearer ") {
		if token := strings.TrimSpace(value[7:]); token != "" {
			return token
		}
	}
	return strings.TrimSpace(r.URL.Query().Get("token"))
}

// appLog 写应用日志：注入结构化日志走日志库，否则降级标准库 log（对齐 Node console 输出位）。
func (m *Module) appLog(ctx context.Context, level, event, messageCN string, fields map[string]any) {
	if m.Log != nil {
		if level == "error" {
			_ = m.Log.Error(ctx, "business", event, messageCN, messageCN, fields)
		} else {
			_ = m.Log.Info(ctx, "business", event, messageCN, messageCN, fields)
		}
		return
	}
	log.Printf("[安全日] %s", messageCN)
}

// configValue 读运行时配置（缺省空串）。
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

// resolveTeam 解析请求生效班组（对齐 Node utils/team.js resolveTeam：超管逐级回落
// wanted → 本班 → 默认班组，其余角色固定本班）。无可用班组返回 (nil, nil)（Node 侧 null 口径）；
// 数据库错误原样返回，由调用方按旧版错误契约回 500。
func (m *Module) resolveTeam(ctx context.Context, r *http.Request, ident auth.Identity, wantedTeamID int64) (*httpapi.Team, error) {
	if m.DB == nil {
		return nil, nil
	}
	wanted := wantedTeamID
	if wanted == 0 {
		wanted = parseWantedTeamID(r.URL.Query().Get("team_id"))
	}
	if ident.Role == "admin" {
		for _, id := range []int64{wanted, ident.TeamID} {
			if id <= 0 {
				continue
			}
			team, err := httpapi.TeamByID(ctx, m.DB, id)
			if err == nil {
				return &team, nil
			}
			if !errors.Is(err, sql.ErrNoRows) {
				return nil, err
			}
		}
		team, err := httpapi.DefaultTeam(ctx, m.DB)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		return &team, nil
	}
	if ident.TeamID == 0 {
		return nil, nil
	}
	team, err := httpapi.TeamByID(ctx, m.DB, ident.TeamID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &team, nil
}

// parseWantedTeamID 按 JS Number(x) + Number.isInteger 判定：整数 > 0 才生效，其余 0。
func parseWantedTeamID(raw string) int64 {
	n, err := strconv.ParseFloat(strings.TrimSpace(raw), 64)
	if err != nil || n <= 0 || n != float64(int64(n)) {
		return 0
	}
	return int64(n)
}

// canAccess 记录归属校验：超管任意班组；其余角色仅本班（按班组名比对）；
// 班组解析失败（数据库错误）按无权处理（旧版此处为未捕获异常，无既定响应契约）。
func (m *Module) canAccess(ctx context.Context, r *http.Request, ident auth.Identity, record Record) bool {
	if ident.Role == "admin" {
		return true
	}
	team, err := m.resolveTeam(ctx, r, ident, 0)
	if err != nil || team == nil {
		return false
	}
	return record.Team == team.Name
}

// deleteSourceFromCos COS 源文件清理：生成时上传的合并学习文件是中转性质，记录到终态即删。
// 清字段时保留既有 error（store.update 在 patch 不带 error 时会清掉它）。
func (m *Module) deleteSourceFromCos(ctx context.Context, record Record) {
	if record.SourceCosKey == "" || m.COS == nil {
		return
	}
	key := record.SourceCosKey
	if err := m.COS.Delete(ctx, key); err != nil {
		m.appLog(ctx, "error", "safeday.cos.cleanup_failed",
			fmt.Sprintf("COS 源文件删除失败 %s：%v", key, err), map[string]any{"key": key})
		return
	}
	if cur, ok := m.Store.Get(record.ID); ok {
		empty := ""
		m.Store.Update(record.ID, RecordPatch{SourceCosKey: &empty, SetError: true, Error: &cur.Error})
	}
}

// ===== POST /callback：Dify 工作流结束回调（fail-closed token 校验，须挂在登录门控之外） =====

// callbackTextKeys 新链路回传的六段正文字段。
var callbackTextKeys = []string{
	"accident_process",
	"cause_analysis",
	"self_check",
	"rectification_list",
	"prevention_measures",
	"supervision_inspection",
}

func (m *Module) callback(w http.ResponseWriter, r *http.Request) {
	token := m.configValue(r.Context(), "SAFEDAY_CALLBACK_TOKEN")
	if token == "" {
		writeFail(w, http.StatusForbidden, "回调 token 未配置，回调不可用", nil)
		return
	}
	if r.URL.Query().Get("token") != token {
		writeFail(w, http.StatusForbidden, "回调 token 校验失败", nil)
		return
	}
	body, err := decodeBody(r)
	if err != nil {
		writeFail(w, http.StatusInternalServerError, "回调处理失败：请求体不是合法 JSON", nil)
		return
	}
	date := rawString(body["date"])
	className := rawString(body["class"])

	// ===== 新链路：回传文字内容 → 后端渲染落盘 =====
	sections := map[string]string{}
	hasTexts := false
	for _, key := range callbackTextKeys {
		if raw, ok := body[key]; ok {
			var s string
			if jsonUnmarshalString(raw, &s) {
				hasTexts = true
				sections[key] = s
			}
		}
	}
	if hasTexts {
		var target Record
		found := false
		for _, rec := range m.Store.List("") {
			if rec.Status != "processing" {
				continue
			}
			if date != "" && rec.Date != date {
				continue
			}
			if className != "" && rec.Team != className {
				continue
			}
			target, found = rec, true
			break
		}
		if !found {
			writeFail(w, http.StatusOK, "未找到匹配的处理中记录（可能已被删除或重复回调）", nil)
			return
		}
		out, err := m.Renderer.Render(RenderRecordData(target, sections))
		if err != nil {
			m.markRenderFail(w, r, target, err)
			return
		}
		outDir := filepath.Join(m.Store.DocsDir(), target.Team)
		if err := os.MkdirAll(outDir, 0o755); err != nil {
			m.markRenderFail(w, r, target, err)
			return
		}
		if err := os.WriteFile(filepath.Join(outDir, filepath.Base(target.FileName)), out, 0o644); err != nil {
			m.markRenderFail(w, r, target, err)
			return
		}
		done := "done"
		m.Store.Update(target.ID, RecordPatch{Status: &done})
		m.deleteSourceFromCos(r.Context(), target)
		writeOK(w, map[string]any{"done": 1, "failed": 0})
		return
	}

	// ===== 旧链路兼容：仅按产物文件存在性终判 =====
	doneCount, failedCount := 0, 0
	for _, record := range m.Store.List("") {
		if record.Status != "processing" {
			continue
		}
		if className != "" && record.Team != className {
			continue
		}
		if date != "" && record.Date != date {
			continue
		}
		if m.judgeOnce(record) == "done" {
			doneCount++
		} else {
			failedCount++
		}
		m.deleteSourceFromCos(r.Context(), record)
	}
	writeOK(w, map[string]any{"done": doneCount, "failed": failedCount})
}

// jsonUnmarshalString 判定 JSON 字符串值（六段正文按 typeof body[k] === 'string' 判定）。
func jsonUnmarshalString(raw json.RawMessage, out *string) bool {
	if len(raw) == 0 {
		return false
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return false
	}
	*out = s
	return true
}

// markRenderFail 回调渲染落盘失败：置 failed + 记应用日志 + 清 COS 源文件，回写错误响应。
func (m *Module) markRenderFail(w http.ResponseWriter, r *http.Request, target Record, cause error) {
	msg := fmt.Sprintf("文档渲染失败：%v", cause)
	failed := "failed"
	m.Store.Update(target.ID, RecordPatch{Status: &failed, SetError: true, Error: &msg})
	m.appLog(r.Context(), "error", "safeday.callback.render_failed",
		fmt.Sprintf("回调文档渲染失败（记录 %s）：%v", target.ID, cause),
		map[string]any{"record_id": target.ID})
	m.deleteSourceFromCos(r.Context(), target)
	writeFail(w, http.StatusInternalServerError, msg, nil)
}

// judgeOnce 一次性判定：文件存在即置 done，不存在即置 failed（回调即终判，仅检查一次）。
func (m *Module) judgeOnce(record Record) string {
	path := m.Store.RecordFilePath(record)
	if info, err := os.Stat(path); err == nil && info.Mode().IsRegular() {
		done := "done"
		m.Store.Update(record.ID, RecordPatch{Status: &done})
		return "done"
	}
	failed := "failed"
	msg := "回调后未检测到生成文件：" + record.FileName
	m.Store.Update(record.ID, RecordPatch{Status: &failed, SetError: true, Error: &msg})
	return "failed"
}

// ===== GET /form-meta：生成表单数据源（班组成员名单 + 按班组记忆的默认值） =====

func (m *Module) formMeta(w http.ResponseWriter, r *http.Request, ident auth.Identity) {
	team, terr := m.resolveTeam(r.Context(), r, ident, 0)
	if terr != nil {
		writeFail(w, http.StatusInternalServerError, "表单数据加载失败："+terr.Error(), nil)
		return
	}
	defaults := FormDefaults{
		Superior: m.configValue(r.Context(), "SAFEDAY_DEFAULT_SUPERIOR"),
	}
	if team == nil {
		writeOK(w, map[string]any{
			"members":  []string{},
			"defaults": map[string]any{"superior": defaults.Superior, "recorder": ""},
		})
		return
	}
	members := []string{}
	if m.DB != nil {
		rows, err := m.DB.QueryContext(r.Context(),
			`SELECT name FROM worklog_member WHERE team_id = ? AND status = 1 ORDER BY sort, id`, team.ID)
		if err != nil {
			writeFail(w, http.StatusInternalServerError, "表单数据加载失败："+err.Error(), nil)
			return
		}
		defer rows.Close()
		for rows.Next() {
			var name string
			if rows.Scan(&name) == nil {
				members = append(members, name)
			}
		}
	}
	saved := m.Store.GetFormDefaults(team.Name)
	superior := saved.Superior
	if superior == "" {
		superior = m.configValue(r.Context(), "SAFEDAY_DEFAULT_SUPERIOR")
	}
	writeOK(w, map[string]any{
		"members": members,
		"defaults": map[string]any{
			"superior": superior,
			"recorder": saved.Recorder,
		},
	})
}

// uploadedFile 上传文件（内存缓冲）。
type uploadedFile struct {
	name string
	buf  []byte
}

// parseUploads 解析 multipart 上传（字段 files，≤10 个、单文件 ≤50MB）。
func parseUploads(r *http.Request) ([]uploadedFile, error) {
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		return nil, err
	}
	files := r.MultipartForm.File["files"]
	if len(files) > maxUploadFiles {
		return nil, errTooManyFiles
	}
	out := make([]uploadedFile, 0, len(files))
	for _, fh := range files {
		f, err := fh.Open()
		if err != nil {
			return nil, err
		}
		data, err := io.ReadAll(io.LimitReader(f, maxUploadFileSize+1))
		f.Close()
		if err != nil {
			return nil, err
		}
		if len(data) > maxUploadFileSize {
			return nil, errFileTooLarge
		}
		out = append(out, uploadedFile{name: fixLatin1Name(fh.Filename), buf: data})
	}
	return out, nil
}

var (
	errTooManyFiles = errors.New("文件数量超出限制（最多 10 个）")
	errFileTooLarge = errors.New("文件大小超出限制（单文件最大 50MB）")
)

// fixLatin1Name 按 multer latin1 解析文件名的口径转回 UTF-8（中文名兼容）。
// Go 的 multipart 已按 UTF-8 解析，此处保留同名入口做字节兜底：非打印 latin1 场景直传。
func fixLatin1Name(name string) string {
	return name
}

// getFileExt 取小写扩展名（不含点）。
func getFileExt(name string) string {
	ext := strings.TrimPrefix(filepath.Ext(name), ".")
	return strings.ToLower(ext)
}

// ===== POST /generate：上传文件 + 生成表单 + 触发 Dify 工作流 =====

func (m *Module) generate(w http.ResponseWriter, r *http.Request, ident auth.Identity) {
	files, err := parseUploads(r)
	if err != nil {
		switch {
		case errors.Is(err, errTooManyFiles):
			writeFail(w, http.StatusBadRequest, errTooManyFiles.Error(), nil)
		case errors.Is(err, errFileTooLarge):
			writeFail(w, http.StatusBadRequest, errFileTooLarge.Error(), nil)
		default:
			writeFail(w, http.StatusBadRequest, "文件上传失败："+err.Error(), nil)
		}
		return
	}
	name := strings.TrimSpace(r.FormValue("name"))
	date := strings.TrimSpace(r.FormValue("date"))
	if len(files) < 1 {
		writeFail(w, http.StatusBadRequest, "请至少上传一个文件", nil)
		return
	}
	if !dateRe.MatchString(date) {
		writeFail(w, http.StatusBadRequest, "日期格式不正确，应为 YYYY.MM.DD", nil)
		return
	}
	if name == "" {
		writeFail(w, http.StatusBadRequest, "请填写学习文件名称", nil)
		return
	}
	for _, f := range files {
		ext := getFileExt(f.name)
		if !containsString(allowedExt, ext) {
			writeFail(w, http.StatusBadRequest,
				fmt.Sprintf("不支持的文件格式：%s（仅支持 %s）", f.name, strings.Join(allowedExt, "/")), nil)
			return
		}
	}

	// 生效班组：超管可用表单 team_id 指定（无效时逐级回落本班/默认班组）；其余角色固定本班（未分配拒绝生成）
	team, terr := m.resolveTeam(r.Context(), r, ident, parseWantedTeamID(r.FormValue("team_id")))
	if terr != nil {
		writeFail(w, http.StatusInternalServerError, "生成请求处理失败："+terr.Error(), nil)
		return
	}
	if team == nil {
		writeFail(w, http.StatusBadRequest, "未分配班组，请联系管理员分配后再生成", nil)
		return
	}

	// 合并或取单文件 buffer：多文件时统一合并为一个 PDF 再上送（Dify 侧仅单文件地址入参）；
	// 非 PDF 先经 LibreOffice 转 PDF，PDF 原样参与合并，顺序与上传一致
	var fileBuffer []byte
	var fileName string
	if len(files) >= 2 {
		pdfBuffers := make([][]byte, 0, len(files))
		for _, f := range files {
			ext := getFileExt(f.name)
			if ext == "pdf" {
				pdfBuffers = append(pdfBuffers, f.buf)
				continue
			}
			if m.Converter == nil {
				writeFail(w, http.StatusBadRequest, "服务端未安装 LibreOffice（soffice），无法转换非 PDF 文件，请联系管理员", nil)
				return
			}
			pdf, err := m.Converter.ToPDF(r.Context(), f.buf, ext)
			if err != nil {
				writeFail(w, http.StatusBadRequest, fmt.Sprintf("「%s」%v", f.name, err), nil)
				return
			}
			pdfBuffers = append(pdfBuffers, pdf)
		}
		merged, err := mergePDFs(pdfBuffers)
		if err != nil {
			writeFail(w, http.StatusBadRequest, fmt.Sprintf("PDF 合并失败：%v", err), nil)
			return
		}
		fileBuffer = merged
		fileName = "merged.pdf"
	} else {
		fileBuffer = files[0].buf
		fileName = files[0].name
	}

	// 先建记录（同一班组同一 date 只保留最新一条）
	full, replaced, err := m.Store.Create(Record{
		Name:        name,
		Date:        date,
		FileName:    date + ".docx",
		Team:        team.Name,
		Status:      "processing",
		SourceCount: len(files),
		Sources:     fileNames(files),
		Form: RecordForm{
			Host:         cutForm(r.FormValue("host"), 64),
			Superior:     cutForm(r.FormValue("superior"), 64),
			Recorder:     cutForm(r.FormValue("recorder"), 64),
			Attendees:    cutForm(r.FormValue("attendees"), 512),
			Absentees:    cutForm(r.FormValue("absentees"), 512),
			AbsentReason: cutForm(r.FormValue("absentReason"), 512),
		},
	})
	if err != nil {
		writeFail(w, http.StatusInternalServerError, "生成请求处理失败："+err.Error(), nil)
		return
	}

	// 被覆盖旧记录的产物在新生成失败时已成孤儿：失败时连带删除
	cleanupReplaced := func() {
		for _, old := range replaced {
			if old.FileName == "" {
				continue
			}
			fp := m.Store.RecordFilePath(old)
			if err := os.Remove(fp); err != nil && !os.IsNotExist(err) {
				m.appLog(r.Context(), "error", "safeday.cleanup_replaced_failed",
					fmt.Sprintf("清理被覆盖记录旧产物失败 %s：%v", fp, err), map[string]any{"file": fp})
			}
		}
	}

	// 按班组记忆表单默认值（空值不覆盖）
	m.Store.SaveFormDefaults(team.Name, FormDefaults{
		Superior: strings.TrimSpace(r.FormValue("superior")),
		Recorder: strings.TrimSpace(r.FormValue("recorder")),
	})

	// 学习文件上传 COS 拿公网 URL，作为 string 变量调 Dify 工作流；触发后立即返回
	if m.COS == nil || m.Dify == nil {
		msg := "生成链路未接入（对象存储或 Dify 工作流缺失）"
		failed := "failed"
		m.Store.Update(full.ID, RecordPatch{Status: &failed, SetError: true, Error: &msg})
		cleanupReplaced()
		writeFail(w, http.StatusInternalServerError, msg, nil)
		return
	}
	srcExt := getFileExt(fileName)
	if srcExt == "" {
		srcExt = "pdf"
	}
	cosKey := fmt.Sprintf("safeday/%s/%s-%d.%s", team.Name, date, time.Now().UnixMilli(), srcExt)
	if err := m.mkdirTeamDocs(team.Name); err != nil {
		msg := err.Error()
		failed := "failed"
		m.Store.Update(full.ID, RecordPatch{Status: &failed, SetError: true, Error: &msg})
		cleanupReplaced()
		writeFail(w, http.StatusInternalServerError, msg, nil)
		return
	}
	if _, err := m.COS.Put(r.Context(), cosKey, fileBuffer, mimeByExt[srcExt]); err != nil {
		msg := err.Error()
		failed := "failed"
		m.Store.Update(full.ID, RecordPatch{Status: &failed, SetError: true, Error: &msg})
		cleanupReplaced()
		writeFail(w, http.StatusInternalServerError, msg, nil)
		return
	}
	m.Store.Update(full.ID, RecordPatch{SourceCosKey: &cosKey})

	recordID := full.ID
	onFailed := func(runErr error) {
		msg := runErr.Error()
		failed := "failed"
		m.Store.Update(recordID, RecordPatch{Status: &failed, SetError: true, Error: &msg})
		m.appLog(context.Background(), "error", "safeday.workflow.failed",
			fmt.Sprintf("Dify 工作流失败（记录 %s）：%s", recordID, msg),
			map[string]any{"record_id": recordID, "date": date, "team": team.Name})
		cleanupReplaced()
		if cur, ok := m.Store.Get(recordID); ok {
			m.deleteSourceFromCos(context.Background(), cur)
		}
	}
	if err := m.Dify.Run(r.Context(), WorkflowInput{
		URL:      m.COS.PublicURL(cosKey),
		Date:     date,
		Class:    team.Name,
		OnFailed: onFailed,
	}); err != nil {
		msg := err.Error()
		failed := "failed"
		m.Store.Update(full.ID, RecordPatch{Status: &failed, SetError: true, Error: &msg})
		m.appLog(r.Context(), "error", "safeday.generate.failed",
			fmt.Sprintf("Dify 工作流触发失败（记录 %s）：%s", full.ID, msg),
			map[string]any{"record_id": full.ID, "date": date, "team": team.Name})
		cleanupReplaced()
		if cur, ok := m.Store.Get(full.ID); ok {
			m.deleteSourceFromCos(r.Context(), cur)
		}
		writeFail(w, http.StatusInternalServerError, msg, nil)
		return
	}

	// 返回建记录时的对象（对齐 Node：不含随后写入的 sourceCosKey，出参无 error/sourceCosKey 键）
	writeOK(w, map[string]any{"record": full})
}

// mkdirTeamDocs 确保班组产物目录存在。
func (m *Module) mkdirTeamDocs(teamName string) error {
	return os.MkdirAll(filepath.Join(m.Store.DocsDir(), teamName), 0o755)
}

// fileNames 上传文件名列表。
func fileNames(files []uploadedFile) []string {
	out := make([]string, 0, len(files))
	for _, f := range files {
		out = append(out, f.name)
	}
	return out
}

// containsString 列表包含。
func containsString(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// mergePDFs PDF 合并（compat/imagepdf.MergePDFs，纯 Go，对齐 safeday/merge.js）。
func mergePDFs(buffers [][]byte) ([]byte, error) {
	return imagepdf.MergePDFs(buffers)
}

// ===== GET /records：记录列表（纯读取；完成判定只在 Dify 回调时进行一次） =====

func (m *Module) records(w http.ResponseWriter, r *http.Request, ident auth.Identity) {
	if ident.Role == "admin" && r.URL.Query().Get("team_id") == "all" {
		writeOK(w, map[string]any{"records": m.Store.List("")})
		return
	}
	team, terr := m.resolveTeam(r.Context(), r, ident, 0)
	if terr != nil {
		writeFail(w, http.StatusInternalServerError, "读取记录失败："+terr.Error(), nil)
		return
	}
	if team == nil {
		writeOK(w, map[string]any{"records": []Record{}})
		return
	}
	writeOK(w, map[string]any{"records": m.Store.List(team.Name)})
}

// getAccessibleRecordFile 取记录 + 归属校验 + 产物路径解析；不存在/无权限/产物未落盘返回 false。
func (m *Module) getAccessibleRecordFile(ctx context.Context, r *http.Request, ident auth.Identity, id string) (Record, string, bool) {
	record, ok := m.Store.Get(id)
	if !ok {
		return Record{}, "", false
	}
	if !m.canAccess(ctx, r, ident, record) {
		return Record{}, "", false
	}
	path := m.Store.RecordFilePath(record)
	if info, err := os.Stat(path); err != nil || info.IsDir() {
		return Record{}, "", false
	}
	return record, path, true
}

// ===== GET /records/{id}/download：下载产物 =====

func (m *Module) download(w http.ResponseWriter, r *http.Request, ident auth.Identity) {
	record, ok := m.Store.Get(chi.URLParam(r, "id"))
	if !ok || record.Status != "done" {
		writeFail(w, http.StatusNotFound, "记录不存在或文件尚未生成", nil)
		return
	}
	if !m.canAccess(r.Context(), r, ident, record) {
		writeFail(w, http.StatusForbidden, "无权访问其他班组的记录", nil)
		return
	}
	path := m.Store.RecordFilePath(record)
	if info, err := os.Stat(path); err != nil || info.IsDir() {
		writeFail(w, http.StatusNotFound, "文件不存在，可能已被清理", nil)
		return
	}
	w.Header().Set("Content-Type", "application/vnd.openxmlformats-officedocument.wordprocessingml.document")
	w.Header().Set("Content-Disposition",
		fmt.Sprintf("attachment; filename*=UTF-8''%s", urlPathEscape(record.FileName)))
	http.ServeFile(w, r, path)
}

// urlPathEscape 文件名转义（RFC 5987）。
func urlPathEscape(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') ||
			c == '-' || c == '.' || c == '_' || c == '~' {
			b.WriteByte(c)
		} else {
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}
	return b.String()
}

// ===== POST /records/{id}/save-netdisk：保存到网盘 =====

func (m *Module) saveNetdisk(w http.ResponseWriter, r *http.Request, ident auth.Identity) {
	record, ok := m.Store.Get(chi.URLParam(r, "id"))
	if !ok || record.Status != "done" {
		writeFail(w, http.StatusNotFound, "记录不存在或文件尚未生成", nil)
		return
	}
	if !m.canAccess(r.Context(), r, ident, record) {
		writeFail(w, http.StatusForbidden, "无权访问其他班组的记录", nil)
		return
	}
	path := m.Store.RecordFilePath(record)
	if info, err := os.Stat(path); err != nil || info.IsDir() {
		writeFail(w, http.StatusNotFound, "文件不存在，可能已被清理", nil)
		return
	}
	if m.Netdisk == nil {
		writeFail(w, http.StatusInternalServerError, "保存失败", nil)
		return
	}
	body, err := os.ReadFile(path)
	if err != nil {
		writeFail(w, http.StatusInternalServerError, "保存失败", nil)
		return
	}
	space := "my"
	dir := "安全日记录"
	if bodyJSON, err := decodeBody(r); err == nil {
		if rawString(bodyJSON["space"]) == "public" {
			space = "public"
		}
		if raw, has := bodyJSON["dir"]; has && string(raw) != "null" {
			dir = rawString(raw)
		}
	}
	saved, err := m.Netdisk.SaveToNetdisk(r.Context(), ident, NetdiskSaveRequest{
		Space: space, Dir: dir, Name: record.FileName, Body: body,
	})
	if err != nil {
		writeFail(w, http.StatusInternalServerError, err.Error(), nil)
		return
	}
	writeOK(w, map[string]any{"path": saved})
}

// ===== GET /records/{id}/preview：在线预览 =====

func (m *Module) preview(w http.ResponseWriter, r *http.Request, ident auth.Identity) {
	record, ok := m.Store.Get(chi.URLParam(r, "id"))
	if !ok || record.Status != "done" {
		writeFail(w, http.StatusNotFound, "记录不存在或文件尚未生成", nil)
		return
	}
	if !m.canAccess(r.Context(), r, ident, record) {
		writeFail(w, http.StatusForbidden, "无权访问其他班组的记录", nil)
		return
	}
	// OO 分支保持本模块 {ok,error} 信封口径（前端 parseOk 按此解析）
	if oo := m.configValue(r.Context(), "OO_PUBLIC_URL"); oo != "" {
		writeOK(w, map[string]any{
			"url": "/office.html?src=safeday&id=" + urlPathEscape(record.ID) +
				"&name=" + urlPathEscape(record.FileName) +
				"&back=/safeday.html",
		})
		return
	}
	url := m.buildPreviewURL(r, record)
	if url == "" {
		writeFail(w, http.StatusOK, "未配置文件预览服务", nil)
		return
	}
	writeOK(w, map[string]any{"url": url})
}

// buildPreviewURL 微软 Office 查看器地址（仅 Office 格式；凭地址内 ?token= 回源拉取下载接口）。
// 回源地址附当前请求 JWT（对齐 Node utils/preview.js）：download 路径由 httpapi.publicAuthPath
// 放行到统一鉴权中间件之外，下载接口按 ?token= 自验证登录（见 withDownload），非匿名。
func (m *Module) buildPreviewURL(r *http.Request, record Record) string {
	viewer := m.configValue(r.Context(), "PREVIEW_VIEWER_URL")
	if viewer == "" {
		viewer = "https://view.officeapps.live.com/op/view.aspx"
	}
	ext := strings.ToLower(filepath.Ext(record.FileName))
	officeExts := map[string]bool{".doc": true, ".docx": true, ".xls": true, ".xlsx": true, ".ppt": true, ".pptx": true}
	if !officeExts[ext] {
		return ""
	}
	proto := r.Header.Get("X-Forwarded-Proto")
	if proto == "" {
		proto = "http"
	}
	srcPath := fmt.Sprintf("/api/v1/safeday/records/%s/download/%s", urlPathEscape(record.ID), urlPathEscape(record.FileName))
	srcURL := proto + "://" + r.Host + srcPath
	if token := requestToken(r); token != "" {
		srcURL += "?token=" + url.QueryEscape(token)
	}
	return viewer + "?src=" + urlPathEscape(srcURL)
}

// ===== DELETE /records/{id}/：删除记录（连带删除已生成的 docx 文件） =====

func (m *Module) delete(w http.ResponseWriter, r *http.Request, ident auth.Identity) {
	id := chi.URLParam(r, "id")
	target, ok := m.Store.Get(id)
	if ok && !m.canAccess(r.Context(), r, ident, target) {
		writeFail(w, http.StatusForbidden, "无权访问其他班组的记录", nil)
		return
	}
	record, removed := m.Store.Remove(id)
	if !removed {
		writeFail(w, http.StatusNotFound, "记录不存在", nil)
		return
	}
	if record.FileName != "" {
		path := m.Store.RecordFilePath(record)
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			writeOK(w, map[string]any{"warning": fmt.Sprintf("记录已删除，但文件删除失败：%v", err)})
			return
		}
	}
	m.deleteSourceFromCos(r.Context(), record)
	writeOK(w, nil)
}
