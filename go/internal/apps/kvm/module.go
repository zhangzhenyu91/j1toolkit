// Package kvm 迁移 server/src/kvm：KVM 远程管理子模块——设备列表实时代理自 GLKVM Cloud 平台
// （员工账号代登），终端/远程控制经 /jump 签发带 sid 的跳转地址；文件分享转发点
// （status/files/push/download/mount/delete）经平台 /web/ 链路直达设备 fileshare 服务；
// 网盘与设备互推（pull-from-netdisk / save-to-netdisk）经 NetdiskBridge 注入。
//
// 路由基路径 /api/v1/kvm；权限按路由细分：设备列表与文件转发 = kvm 或 file-transfer 任一，
// 带态跳转 = 仅 kvm。班组设备口径：生效设备 = 平台可见设备中 deviceGroupName 命中本班组名；
// 本班组 0 台 → 回退默认班组（首个启用班组）设备，响应带 fallback 标记。维护锁命中 423。
package kvm

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"shade/internal/auth"
	"shade/internal/httpapi"
)

// AppPermit 校验用户是否拥有指定应用（app_key）的使用权限（对接 sys_user_app；nil 表示未接入，不拦截）。
type AppPermit interface {
	Allow(ctx context.Context, userID int64, appKey string) (bool, error)
}

// 推送上传限制：单文件上限 2GB、单次最多 20 个（与 Node multer 同口径）。
const (
	maxPushFileSize = 2 * 1024 * 1024 * 1024
	maxPushFiles    = 20
)

// maxKVMResponseBytes 设备 fileshare 常规（非流式）转发响应字节上限：
// 超出显式报错，绝不把截断内容当完整结果（大文件下载仍走流式通道）。
var maxKVMResponseBytes int64 = 16 << 20

// readBodyLimited 读取有界响应体：limit+1 检出超限并显式报错。
func readBodyLimited(r io.Reader, max int64) ([]byte, error) {
	buf, err := io.ReadAll(io.LimitReader(r, max+1))
	if err != nil {
		return nil, err
	}
	if int64(len(buf)) > max {
		return nil, fmt.Errorf("设备响应超出大小限制（%d 字节）", max)
	}
	return buf, nil
}

// Module 是 KVM 远程管理应用模块。
type Module struct {
	DB       *sql.DB
	Permit   AppPermit     // 应用权限检查（可注入；nil 不拦截）
	Platform Platform      // GLKVM 平台客户端（可注入）
	Locker   Locker        // 设备维护锁（可注入；nil 按未锁放行）
	Netdisk  NetdiskBridge // 团队网盘桥接（可注入；nil 时互推接口报未接入）
	HTTP     *http.Client  // 设备 fileshare 转发用（可注入；常规 60s 超时、大文件转发不限超时，注入时保留其 Transport）
}

// New 构造 KVM 模块；Permit/Platform/Locker/Netdisk 可在装配后注入。
func New(db *sql.DB, platform Platform) *Module {
	return &Module{DB: db, Platform: platform}
}

// RegisterRoutes 注册 /api/v1/kvm 下全部路由（挂载在 /api/v1 之下）。
func (m *Module) RegisterRoutes(r chi.Router) {
	r.Route("/kvm", func(r chi.Router) {
		r.Get("/devices", m.requireAnyApp(m.deviceList))
		r.Post("/jump", m.requireApp(m.jump))
		r.Get("/devices/{id}/status", m.requireAnyApp(m.fileStatus))
		r.Get("/devices/{id}/files", m.requireAnyApp(m.fileList))
		r.Post("/devices/{id}/push", m.requireAnyApp(m.filePush))
		r.Get("/devices/{id}/download", m.requireAnyApp(m.fileDownload))
		r.Post("/devices/{id}/mount", m.requireAnyApp(m.fileMount))
		r.Post("/devices/{id}/delete", m.requireAnyApp(m.fileDelete))
		r.Post("/devices/{id}/pull-from-netdisk", m.requireAnyApp(m.pullFromNetdisk))
		r.Post("/devices/{id}/save-to-netdisk", m.requireAnyApp(m.saveToNetdisk))
	})
}

// handlerFunc 带身份的路由处理函数。
type handlerFunc func(w http.ResponseWriter, r *http.Request, ident auth.Identity)

// requireApp 登录 + 单应用权限（safe-day 同口径）。
func (m *Module) requireApp(next handlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ident, ok := m.enter(w, r)
		if !ok {
			return
		}
		if !m.allowApp(r.Context(), ident.ID, "kvm") {
			fail(w, http.StatusForbidden, codeNoApp, "暂无该应用的使用权限，请联系管理员开通")
			return
		}
		next(w, r, ident)
	}
}

// requireAnyApp 登录 + 任一应用权限（kvm 或 file-transfer 任一命中即放行）。
func (m *Module) requireAnyApp(next handlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ident, ok := m.enter(w, r)
		if !ok {
			return
		}
		if !m.allowAnyApp(r.Context(), ident.ID, "kvm", "file-transfer") {
			fail(w, http.StatusForbidden, codeNoApp, "暂无该应用的使用权限，请联系管理员开通")
			return
		}
		next(w, r, ident)
	}
}

// enter 身份校验；失败时已写出响应。
func (m *Module) enter(w http.ResponseWriter, r *http.Request) (auth.Identity, bool) {
	ident, ok := httpapi.IdentityFromContext(r.Context())
	if !ok {
		fail(w, http.StatusUnauthorized, codeNoLogin, "未登录或登录已过期")
		return auth.Identity{}, false
	}
	return ident, true
}

// allowApp 单应用权限（Permit nil 放行）。
func (m *Module) allowApp(ctx context.Context, userID int64, appKey string) bool {
	if m.Permit == nil {
		return true
	}
	allow, err := m.Permit.Allow(ctx, userID, appKey)
	return err == nil && allow
}

// allowAnyApp 任一应用权限命中。
func (m *Module) allowAnyApp(ctx context.Context, userID int64, appKeys ...string) bool {
	for _, key := range appKeys {
		if m.allowApp(ctx, userID, key) {
			return true
		}
	}
	return len(appKeys) == 0
}

// resolveTeam 解析请求生效班组（超管可指定 ?team_id=，其余固定本班；无可用班组返回 nil）。
func (m *Module) resolveTeam(ctx context.Context, r *http.Request, ident auth.Identity) *httpapi.Team {
	if m.DB == nil {
		return nil
	}
	wanted := parseWantedTeamID(r.URL.Query().Get("team_id"))
	adapter := httpapi.IdentityAdapter{TeamID: ident.TeamID, Role: ident.Role}
	team, err := httpapi.ResolveTeam(ctx, m.DB, adapter, wanted)
	if err != nil {
		return nil
	}
	return &team
}

// parseWantedTeamID 按 JS Number(x) + Number.isInteger 判定：整数 > 0 才生效，其余 0。
func parseWantedTeamID(raw string) int64 {
	n, err := strconv.ParseFloat(strings.TrimSpace(raw), 64)
	if err != nil || n <= 0 || n != float64(int64(n)) {
		return 0
	}
	return int64(n)
}

// teamDeviceCtx 班组设备口径：生效设备 = 平台可见设备中 deviceGroupName 命中本班组名；
// 本班组组内 0 台 → 回退默认班组（首个启用班组）设备，响应带 fallback 标记供前端提示。
type teamDeviceCtx struct {
	team         *httpapi.Team
	items        []Device
	fallback     bool
	fallbackTeam string
}

func (m *Module) teamDevices(ctx context.Context, r *http.Request, ident auth.Identity) (teamDeviceCtx, error) {
	out := teamDeviceCtx{}
	out.team = m.resolveTeam(ctx, r, ident)
	if out.team == nil {
		return out, nil
	}
	if m.Platform == nil {
		return out, bizErr("GLKVM Cloud 未配置（GLKVM_URL / GLKVM_PASSWORD）", http.StatusBadGateway, codePlatform)
	}
	all, err := m.Platform.ListDevices(ctx, r.URL.Query(), ident.Username)
	if err != nil {
		return out, err
	}
	for _, d := range all {
		if d.DeviceGroupName == out.team.Name {
			out.items = append(out.items, d)
		}
	}
	if len(out.items) == 0 {
		def := m.defaultTeam(ctx)
		if def != nil && def.ID != out.team.ID {
			for _, d := range all {
				if d.DeviceGroupName == def.Name {
					out.items = append(out.items, d)
					out.fallback = true
					out.fallbackTeam = def.Name
				}
			}
		}
	}
	return out, nil
}

// defaultTeam 首个启用班组（Node teamUtil.getDefaultTeam）。
func (m *Module) defaultTeam(ctx context.Context) *httpapi.Team {
	if m.DB == nil {
		return nil
	}
	var t httpapi.Team
	err := m.DB.QueryRowContext(ctx,
		`SELECT id, name FROM sys_team WHERE status = 1 ORDER BY sort, id LIMIT 1`).Scan(&t.ID, &t.Name)
	if err != nil {
		return nil
	}
	return &t
}

// assertTeamDevice 校验目标设备在当前生效班组可见集内（含回退集），防跨班组按 id / ddns 直访。
func (m *Module) assertTeamDevice(ctx context.Context, r *http.Request, ident auth.Identity, id, ddns string) (Device, error) {
	devCtx, err := m.teamDevices(ctx, r, ident)
	if err != nil {
		return Device{}, err
	}
	for _, d := range devCtx.items {
		if id != "" && strconv.FormatInt(d.ID, 10) == strings.TrimSpace(id) {
			return d, nil
		}
		if ddns != "" && d.Ddns == ddns {
			return d, nil
		}
	}
	return Device{}, bizErr("设备不存在，或不属于当前班组", http.StatusNotFound, codePlatform)
}

// lockReason 维护锁原因文案（未锁/异常返回空串，fail-open）。
func (m *Module) lockReason(ctx context.Context, ddns string) string {
	if m.Locker == nil {
		return ""
	}
	return m.Locker.GetReason(ctx, ddns)
}

// assertOperable 设备操作前置：班组可见性 + 维护锁（锁定中返回 423）。
func (m *Module) assertOperable(w http.ResponseWriter, r *http.Request, ident auth.Identity, id, ddns string) (Device, bool) {
	dev, err := m.assertTeamDevice(r.Context(), r, ident, id, ddns)
	if err != nil {
		writeErr(w, err)
		return Device{}, false
	}
	if reason := m.lockReason(r.Context(), dev.Ddns); reason != "" {
		fail(w, http.StatusLocked, codeLocked, "设备维护中："+reason)
		return Device{}, false
	}
	return dev, true
}

// deviceJSON 设备响应透传：平台原始字段 + maintaining 维护锁标注。
func (m *Module) deviceJSON(ctx context.Context, d Device) map[string]any {
	out := map[string]any{}
	for k, v := range d.Raw {
		out[k] = v
	}
	out["id"] = d.ID
	if d.Ddns != "" {
		out["ddns"] = d.Ddns
	}
	if d.DeviceGroupName != "" {
		out["deviceGroupName"] = d.DeviceGroupName
	}
	out["maintaining"] = m.lockReason(ctx, d.Ddns) != ""
	return out
}

// GET /devices：设备列表（透传 q/status 过滤；员工账号代登平台，再按班组过滤 + 回退）。
func (m *Module) deviceList(w http.ResponseWriter, r *http.Request, ident auth.Identity) {
	devCtx, err := m.teamDevices(r.Context(), r, ident)
	if err != nil {
		writeErr(w, err)
		return
	}
	items := make([]map[string]any, 0, len(devCtx.items))
	for _, d := range devCtx.items {
		items = append(items, m.deviceJSON(r.Context(), d))
	}
	teamName := ""
	if devCtx.team != nil {
		teamName = devCtx.team.Name
	}
	writeOK(w, map[string]any{
		"items":         items,
		"team":          teamName,
		"fallback":      devCtx.fallback,
		"fallback_team": devCtx.fallbackTeam,
	})
}

// 跳转目标白名单（与平台前端路由同构）：终端为 SPA hash 路由，远程控制为平台 /web/ 代理路由。
var jumpTargets = map[string]func(ddns string) string{
	"term": func(ddns string) string { return "/#/rtty/" + ddns },
	"desk": func(ddns string) string {
		return "/web/" + ddns + "/https/" + url.PathEscape("127.0.0.1:443/")
	},
}

// ddnsRe ddns 仅允许字母数字与 . _ -（禁止路径字符，防拼接注入）。
var ddnsRe = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)

// POST /jump：签发带态跳转地址（以当前员工账号取平台会话 sid，拼平台 jump.html 地址）。
func (m *Module) jump(w http.ResponseWriter, r *http.Request, ident auth.Identity) {
	body, err := decodeBody(r)
	if err != nil {
		fail(w, http.StatusBadRequest, codeParam, "参数错误")
		return
	}
	to := rawString(body["to"])
	build := jumpTargets[to]
	ddns := rawString(body["ddns"])
	if build == nil {
		fail(w, http.StatusBadRequest, codeParam, "参数错误：to")
		return
	}
	if !ddnsRe.MatchString(ddns) {
		fail(w, http.StatusBadRequest, codeParam, "参数错误：ddns")
		return
	}
	if _, ok := m.assertOperable(w, r, ident, "", ddns); !ok {
		return
	}
	sid, err := m.Platform.SessionToken(r.Context(), ident.Username)
	if err != nil {
		writeErr(w, err)
		return
	}
	safeSid := url.QueryEscape(sid)
	safeTo := url.QueryEscape(build(ddns))
	writeOK(w, map[string]any{
		"url": fmt.Sprintf("%s/jump.html#sid=%s&to=%s", m.Platform.BaseURL(), safeSid, safeTo),
	})
}

// fileshare 设备 fileshare 转发会话 + HTTP 客户端（常规/大文件两档）。
type fileshare struct {
	ps  ProxySession
	std *http.Client // 常规 JSON 转发（60s 超时，同 Node axios timeout: 60000）
	big *http.Client // 大文件转发（不限超时，同 Node timeout: 0）
}

// stdClient 常规转发客户端（status/list/mount/delete）。
func (m *Module) stdClient() *http.Client {
	if m.HTTP != nil {
		return m.HTTP
	}
	return &http.Client{Timeout: 60 * time.Second}
}

// bigClient 大文件转发客户端（push/download/两跳保存）：不限超时；注入 HTTP 时保留其 Transport 去掉超时。
func (m *Module) bigClient() *http.Client {
	if m.HTTP != nil {
		return &http.Client{Transport: m.HTTP.Transport}
	}
	return &http.Client{}
}

// openFileshare 取设备转发会话（失败写出响应并返回 false）。
func (m *Module) openFileshare(w http.ResponseWriter, r *http.Request, ident auth.Identity, id string) (fileshare, bool) {
	if m.Platform == nil {
		writeErr(w, bizErr("GLKVM Cloud 未配置（GLKVM_URL / GLKVM_PASSWORD）", http.StatusBadGateway, codePlatform))
		return fileshare{}, false
	}
	ps, err := m.Platform.ProxySession(r.Context(), id, ident.Username)
	if err != nil {
		writeErr(w, err)
		return fileshare{}, false
	}
	return fileshare{ps: ps, std: m.stdClient(), big: m.bigClient()}, true
}

// relayFail 转发调用统一出口：错误文案对用户可读（上游 body.error 优先）。
func relayFail(w http.ResponseWriter, status int, body []byte, err error) {
	msg := ""
	if len(body) > 0 {
		var obj struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(body, &obj) == nil && obj.Error != "" {
			msg = obj.Error
		}
	}
	if msg == "" {
		if err != nil {
			msg = err.Error()
		} else {
			msg = "设备访问失败"
		}
	}
	code := status
	if code == http.StatusUnauthorized || code == 0 {
		code = http.StatusBadGateway
	}
	fail(w, code, codePlatform, msg)
}

// doFileshare 调设备 /api/fileshare/*（GET/POST JSON/multipart；timeoutZero=true 走大文件不限超时客户端）。
func (f fileshare) do(ctx context.Context, method, path string, contentType string, body io.Reader, timeoutZero bool) (int, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, method, f.ps.Origin+path, body)
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Cookie", f.ps.Cookie)
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	client := f.std
	if timeoutZero {
		client = f.big
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	data, err := readBodyLimited(resp.Body, maxKVMResponseBytes)
	return resp.StatusCode, data, err
}

// streamGet 发起 GET 并把响应体以流形式交回调用方（大文件下载/两跳保存用，不整读内存）。
func (f fileshare) streamGet(ctx context.Context, path string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, f.ps.Origin+path, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Cookie", f.ps.Cookie)
	return f.big.Do(req)
}

// GET /devices/{id}/status：挂载状态查询（shared=true 表示已共享给被控机）。
func (m *Module) fileStatus(w http.ResponseWriter, r *http.Request, ident auth.Identity) {
	if _, ok := m.assertOperable(w, r, ident, chi.URLParam(r, "id"), ""); !ok {
		return
	}
	fs, ok := m.openFileshare(w, r, ident, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	status, data, err := fs.do(r.Context(), http.MethodGet, "/api/fileshare/status", "", nil, false)
	if err != nil || status != http.StatusOK {
		relayFail(w, status, data, err)
		return
	}
	writeDeviceJSON(w, data)
}

// GET /devices/{id}/files：盘内文件列表（共享中则先断开）。
func (m *Module) fileList(w http.ResponseWriter, r *http.Request, ident auth.Identity) {
	if _, ok := m.assertOperable(w, r, ident, chi.URLParam(r, "id"), ""); !ok {
		return
	}
	fs, ok := m.openFileshare(w, r, ident, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	status, data, err := fs.do(r.Context(), http.MethodGet, "/api/fileshare/list", "", nil, false)
	if err != nil || status != http.StatusOK {
		relayFail(w, status, data, err)
		return
	}
	writeDeviceJSON(w, data)
}

// writeDeviceJSON 设备 fileshare 响应包统一信封回传（对齐 Node ok(res, r.data)：data 为设备 JSON
// 本体，前端按 {code:0,...} 解包取 data；设备返回非 JSON 体时兜底为字符串 data）。
func writeDeviceJSON(w http.ResponseWriter, data []byte) {
	var v any
	if err := json.Unmarshal(data, &v); err != nil {
		v = string(data)
	}
	writeOK(w, v)
}

// POST /devices/{id}/push：推送文件到盘（断开 → 写入 → 保持非共享；挂载由 /mount 单独触发）。
func (m *Module) filePush(w http.ResponseWriter, r *http.Request, ident auth.Identity) {
	r.Body = http.MaxBytesReader(w, r.Body, maxPushFileSize*maxPushFiles+1<<20)
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		fail(w, http.StatusBadRequest, codeParam, "文件上传失败：请求体过大或格式错误")
		return
	}
	files := r.MultipartForm.File["files"]
	if len(files) == 0 {
		fail(w, http.StatusBadRequest, codeParam, "请至少上传一个文件（字段名 files）")
		return
	}
	if len(files) > maxPushFiles {
		fail(w, http.StatusBadRequest, codeParam, "文件数量超出限制（最多 20 个）")
		return
	}
	// 单文件上限 2GB（与 Node multer LIMIT_FILE_SIZE 同口径、同文案）
	for _, fh := range files {
		if fh.Size > maxPushFileSize {
			fail(w, http.StatusBadRequest, codeParam, "文件大小超出限制（单文件最大 2GB）")
			return
		}
	}
	if _, ok := m.assertOperable(w, r, ident, chi.URLParam(r, "id"), ""); !ok {
		return
	}
	fs, ok := m.openFileshare(w, r, ident, chi.URLParam(r, "id"))
	if !ok {
		return
	}

	// 组装 multipart 转发体
	pr, pw := io.Pipe()
	mw := newMultipartWriter(pw)
	go func() {
		defer pw.Close()
		defer mw.close()
		for i, fh := range files {
			f, err := fh.Open()
			if err != nil {
				mw.abort(err)
				return
			}
			name := sanitizePushName(r, fh.Filename, i, len(files))
			if err := mw.writeFile("files", name, f); err != nil {
				f.Close()
				mw.abort(err)
				return
			}
			f.Close()
		}
	}()
	status, data, err := fs.do(r.Context(), http.MethodPost, "/api/fileshare/push", mw.contentType(), pr, true)
	if err != nil || status != http.StatusOK {
		relayFail(w, status, data, err)
		return
	}
	writeDeviceJSON(w, data)
}

// sanitizePushName 文件名口径：小程序 wx.uploadFile 发的是临时路径名，真实文件名在表单字段
// filename（单文件场景）；其余走上传文件名。
func sanitizePushName(r *http.Request, fallback string, index, total int) string {
	if total == 1 && r.MultipartForm != nil {
		if vals := r.MultipartForm.Value["filename"]; len(vals) > 0 && strings.TrimSpace(vals[0]) != "" {
			runes := []rune(strings.TrimSpace(vals[0]))
			if len(runes) > 255 {
				return string(runes[:255])
			}
			return string(runes)
		}
	}
	runes := []rune(fallback)
	if len(runes) > 255 {
		return string(runes[:255])
	}
	return string(runes)
}

// GET /devices/{id}/download?name=：下载盘内文件（流式回传）。
func (m *Module) fileDownload(w http.ResponseWriter, r *http.Request, ident auth.Identity) {
	name := strings.TrimSpace(r.URL.Query().Get("name"))
	if name == "" {
		fail(w, http.StatusBadRequest, codeParam, "参数错误：name")
		return
	}
	if _, ok := m.assertOperable(w, r, ident, chi.URLParam(r, "id"), ""); !ok {
		return
	}
	fs, ok := m.openFileshare(w, r, ident, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet,
		fs.ps.Origin+"/api/fileshare/download/"+url.PathEscape(name), nil)
	if err != nil {
		relayFail(w, 0, nil, err)
		return
	}
	req.Header.Set("Cookie", fs.ps.Cookie)
	resp, err := fs.big.Do(req) // 大文件流式回传，不限超时（同 Node timeout: 0）
	if err != nil {
		relayFail(w, 0, nil, err)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		data, _ := readBodyLimited(resp.Body, 1<<20)
		relayFail(w, resp.StatusCode, data, nil)
		return
	}
	if cd := resp.Header.Get("Content-Disposition"); cd != "" {
		w.Header().Set("Content-Disposition", cd)
	} else {
		w.Header().Set("Content-Disposition", "attachment; filename*=UTF-8''"+url.PathEscape(name))
	}
	if cl := resp.Header.Get("Content-Length"); cl != "" {
		w.Header().Set("Content-Length", cl)
	}
	w.WriteHeader(http.StatusOK)
	_, _ = io.Copy(w, resp.Body)
}

// POST /devices/{id}/mount：挂载（共享）到被控机（推送全部完成后再调一次）。
func (m *Module) fileMount(w http.ResponseWriter, r *http.Request, ident auth.Identity) {
	if _, ok := m.assertOperable(w, r, ident, chi.URLParam(r, "id"), ""); !ok {
		return
	}
	fs, ok := m.openFileshare(w, r, ident, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	status, data, err := fs.do(r.Context(), http.MethodPost, "/api/fileshare/mount", "", nil, false)
	if err != nil || status != http.StatusOK {
		relayFail(w, status, data, err)
		return
	}
	writeDeviceJSON(w, data)
}

// POST /devices/{id}/delete：删除盘内文件（JSON {"names": [...]}，共享中自动断开）。
func (m *Module) fileDelete(w http.ResponseWriter, r *http.Request, ident auth.Identity) {
	body, err := decodeBody(r)
	if err != nil {
		fail(w, http.StatusBadRequest, codeParam, "参数错误")
		return
	}
	raw, has := body["names"]
	var names []string
	if has {
		_ = json.Unmarshal(raw, &names)
	}
	if len(names) == 0 {
		fail(w, http.StatusBadRequest, codeParam, "参数错误：names（文件名数组）")
		return
	}
	clean := make([]string, 0, len(names))
	for _, n := range names {
		runes := []rune(n)
		if len(runes) > 255 {
			n = string(runes[:255])
		}
		clean = append(clean, n)
	}
	if _, ok := m.assertOperable(w, r, ident, chi.URLParam(r, "id"), ""); !ok {
		return
	}
	fs, ok := m.openFileshare(w, r, ident, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	payload, _ := json.Marshal(map[string]any{"names": clean})
	status, data, err := fs.do(r.Context(), http.MethodPost, "/api/fileshare/delete", "application/json", strings.NewReader(string(payload)), false)
	if err != nil || status != http.StatusOK {
		relayFail(w, status, data, err)
		return
	}
	writeDeviceJSON(w, data)
}

// POST /devices/{id}/pull-from-netdisk：网盘 → 设备（须另持 netdisk 应用权限）。
// body 两种形态兼容：旧 {space, path} 单文件；新 {space, paths: [...]} 批量（1-20 个）。
// 逐文件 拉取→push，单文件失败记录继续不中断整批；全成 200、部分失败 200+汇总文案、全部失败 502。
func (m *Module) pullFromNetdisk(w http.ResponseWriter, r *http.Request, ident auth.Identity) {
	if _, ok := m.assertOperable(w, r, ident, chi.URLParam(r, "id"), ""); !ok {
		return
	}
	if m.Netdisk == nil {
		writeErr(w, errNetdiskNotConfigured)
		return
	}
	if err := m.Netdisk.AssertAccess(r.Context(), ident.ID); err != nil {
		writeErr(w, err)
		return
	}
	body, err := decodeBody(r)
	if err != nil {
		fail(w, http.StatusBadRequest, codeParam, "参数错误")
		return
	}
	space := rawString(body["space"])
	spaceNorm := "my"
	if space == "public" {
		spaceNorm = "public"
	} else if space != "" && space != "my" {
		fail(w, http.StatusBadRequest, codeParam, "参数错误：space（或未分配班组无公共区）")
		return
	}
	var paths []string
	if raw, has := body["paths"]; has && string(raw) != "null" {
		_ = json.Unmarshal(raw, &paths)
		if len(paths) == 0 || len(paths) > 20 {
			fail(w, http.StatusBadRequest, codeParam, "参数错误：paths（1-20 个）")
			return
		}
	} else {
		paths = []string{rawString(body["path"])}
	}
	fs, ok := m.openFileshare(w, r, ident, chi.URLParam(r, "id"))
	if !ok {
		return
	}

	type pullItem struct {
		Name  string `json:"name"`
		OK    bool   `json:"ok"`
		Error string `json:"error,omitempty"`
	}
	items := make([]pullItem, 0, len(paths))
	okCnt := 0
	for _, p := range paths {
		rel, valid := normRel(p)
		name := lastSegment(p)
		if !valid || rel == "/" {
			items = append(items, pullItem{Name: name, OK: false, Error: "路径不合法"})
			continue
		}
		// 生产桥（bridge.KvmBridge）支持流式两跳：网盘 body 直接进 multipart 推设备，
		// 不整读 512MiB；旧桥/测试桩回退 Download 整读兼容。
		if sb, ok := m.Netdisk.(NetdiskStreamBridge); ok {
			rc, size, err := sb.DownloadStream(r.Context(), ident, spaceNorm, rel)
			if err != nil || size == 0 {
				if rc != nil {
					_ = rc.Close()
				}
				reason := "网盘侧文件拉取为空"
				if err != nil {
					reason = err.Error()
				}
				items = append(items, pullItem{Name: name, OK: false, Error: truncate(reason, 120)})
				continue
			}
			pushErr := pushOneFile(r.Context(), fs, name, rc)
			if closeErr := rc.Close(); pushErr == nil && closeErr != nil {
				pushErr = closeErr
			}
			if pushErr != nil {
				items = append(items, pullItem{Name: name, OK: false, Error: truncate(pushErr.Error(), 120)})
				continue
			}
			items = append(items, pullItem{Name: name, OK: true})
			okCnt++
			continue
		}
		data, err := m.Netdisk.Download(r.Context(), ident, spaceNorm, rel)
		if err != nil || len(data) == 0 {
			reason := "网盘侧文件拉取为空"
			if err != nil {
				reason = err.Error()
			}
			items = append(items, pullItem{Name: name, OK: false, Error: truncate(reason, 120)})
			continue
		}
		if err := pushOneFile(r.Context(), fs, name, strings.NewReader(string(data))); err != nil {
			items = append(items, pullItem{Name: name, OK: false, Error: truncate(err.Error(), 120)})
			continue
		}
		items = append(items, pullItem{Name: name, OK: true})
		okCnt++
	}
	bad := 0
	var firstBad string
	for _, it := range items {
		if !it.OK {
			bad++
			if firstBad == "" {
				firstBad = it.Name + "（" + truncate(it.Error, 60) + "）"
			}
		}
	}
	switch {
	case bad == 0:
		writeOK(w, map[string]any{"items": items}, fmt.Sprintf("已从网盘推送 %d 个文件到设备", okCnt))
	case okCnt == 0:
		fail(w, http.StatusBadGateway, codePlatform, fmt.Sprintf("推送全部失败（共 %d 个）：%s", bad, firstBad))
	default:
		writeOK(w, map[string]any{"items": items}, fmt.Sprintf("成功 %d 个，失败 %d 个：%s", okCnt, bad, firstBad))
	}
}

// pushOneFile 单文件推设备（multipart files 字段，body 从 r 流式拷贝，不整读内存）。
func pushOneFile(ctx context.Context, fs fileshare, name string, r io.Reader) error {
	pr, pw := io.Pipe()
	mw := newMultipartWriter(pw)
	go func() {
		defer pw.Close()
		defer mw.close()
		if err := mw.writeFile("files", name, r); err != nil {
			mw.abort(err)
		}
	}()
	status, respBody, err := fs.do(ctx, http.MethodPost, "/api/fileshare/push", mw.contentType(), pr, true)
	if err != nil {
		return err
	}
	if status != http.StatusOK {
		var obj struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(respBody, &obj)
		if obj.Error != "" {
			return errors.New(obj.Error)
		}
		return fmt.Errorf("推送失败（HTTP %d）", status)
	}
	return nil
}

// POST /devices/{id}/save-to-netdisk：设备 → 网盘（须另持 netdisk 应用权限）。
func (m *Module) saveToNetdisk(w http.ResponseWriter, r *http.Request, ident auth.Identity) {
	if _, ok := m.assertOperable(w, r, ident, chi.URLParam(r, "id"), ""); !ok {
		return
	}
	if m.Netdisk == nil {
		writeErr(w, errNetdiskNotConfigured)
		return
	}
	if err := m.Netdisk.AssertAccess(r.Context(), ident.ID); err != nil {
		writeErr(w, err)
		return
	}
	body, err := decodeBody(r)
	if err != nil {
		fail(w, http.StatusBadRequest, codeParam, "参数错误")
		return
	}
	name := rawString(body["name"])
	if name == "" {
		fail(w, http.StatusBadRequest, codeParam, "参数错误：name")
		return
	}
	space := "my"
	if rawString(body["space"]) == "public" {
		space = "public"
	}
	dir := "文件传输"
	if raw, has := body["dir"]; has && string(raw) != "null" {
		dir = rawString(raw)
	}
	fs, ok := m.openFileshare(w, r, ident, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	// 大文件流式两跳：设备下载响应体直传网盘保存，不整读内存（同 Node responseType: 'stream'）
	resp, err := fs.streamGet(r.Context(), "/api/fileshare/download/"+url.PathEscape(name))
	if err != nil {
		relayFail(w, 0, nil, err)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		data, _ := readBodyLimited(resp.Body, 1<<20)
		relayFail(w, resp.StatusCode, data, nil)
		return
	}
	path, err := m.Netdisk.Save(r.Context(), ident, space, dir, name, resp.Body, resp.ContentLength)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeOK(w, map[string]any{"path": path}, "已保存到网盘")
}

// lastSegment 取路径末段（文件名）；空 → 未命名。
func lastSegment(p string) string {
	trimmed := strings.Trim(strings.ReplaceAll(p, "\\", "/"), "/")
	if trimmed == "" {
		return "未命名"
	}
	segs := strings.Split(trimmed, "/")
	name := segs[len(segs)-1]
	if name == "" {
		return "未命名"
	}
	return name
}

// truncate 按 rune 截断字符串。
func truncate(s string, n int) string {
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return string(runes[:n])
}
