package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"shade/internal/auth"
	"shade/internal/logging"
	"shade/internal/logsafe"
)

// Authenticator 是 HTTP 层认证适配接口，便于生产 SQL/JWT 与本地 fixture 共用路由。
type Authenticator interface {
	Login(ctx context.Context, username, password, client string) (auth.LoginResult, error)
	Authenticate(ctx context.Context, token string) (auth.Identity, error)
	Logout(ctx context.Context, token string, expiresAt time.Time) error
}

type contextKey string

const identityContextKey contextKey = "shade.identity"

// IdentityFromContext 供业务模块读取已验证用户。
func IdentityFromContext(ctx context.Context) (auth.Identity, bool) {
	value, ok := ctx.Value(identityContextKey).(auth.Identity)
	return value, ok
}

// identityCarrierKey 请求内共享身份载体键：最外层访问日志中间件先放入请求 ctx，
// 内层 AuthMiddleware 验签后回填，外层响应后直接取用，避免重复验签/回查 sys_user。
type identityCarrierKey struct{}

// identityCarrier 单请求共享的身份结果；resolved 区分「已定性（含匿名）」与「未验签」。
type identityCarrier struct {
	mu       sync.Mutex
	identity auth.Identity
	resolved bool
}

// withIdentityCarrier 把身份载体放入 ctx 并返回载体指针。
func withIdentityCarrier(ctx context.Context) (context.Context, *identityCarrier) {
	c := &identityCarrier{}
	return context.WithValue(ctx, identityCarrierKey{}, c), c
}

// identityCarrierFromContext 取请求 ctx 中的身份载体（无载体或 nil 返回 false）。
func identityCarrierFromContext(ctx context.Context) (*identityCarrier, bool) {
	if ctx == nil {
		return nil, false
	}
	c, ok := ctx.Value(identityCarrierKey{}).(*identityCarrier)
	return c, ok && c != nil
}

// store 记录验签结果（401 失败同样记录：已定性匿名，外层访问日志不再查用户）。
func (c *identityCarrier) store(identity auth.Identity) {
	c.mu.Lock()
	c.identity, c.resolved = identity, true
	c.mu.Unlock()
}

// load 取已定性身份。
func (c *identityCarrier) load() (auth.Identity, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.identity, c.resolved
}

// storeCarrierIdentity 有共享载体时回填（无载体即普通请求，忽略）。
func storeCarrierIdentity(ctx context.Context, identity auth.Identity) {
	if c, ok := identityCarrierFromContext(ctx); ok {
		c.store(identity)
	}
}

// AuthMiddleware 保护 API；nil Auth 仅供单元测试使用，生产启动必须显式注入。
// 凭证取 Authorization: Bearer；文件下载路径（fileDownloadPath）在无 Authorization 头时
// 兼容旧版 ?token= query（微软查看器/图片标签回源无法带请求头，同 Node utils/tokenQuery 语义），
// 其余接口不接受 query token。
// 验签结果在 admin gate 之前回填请求内的共享身份载体（401 回填匿名），
// 供最外层访问日志复用，同一请求不重复验签/查 sys_user。
func AuthMiddleware(next http.Handler, authenticator Authenticator) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if authenticator == nil || publicAuthPath(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}
		token := bearerToken(r)
		if token == "" && (r.Method == http.MethodGet || r.Method == http.MethodHead) && fileDownloadPath(r.URL.Path) {
			token = strings.TrimSpace(r.URL.Query().Get("token"))
		}
		identity, err := authenticator.Authenticate(r.Context(), token)
		if err != nil {
			storeCarrierIdentity(r.Context(), auth.Identity{})
			writeErr(w, &APIError{Code: "unauthorized", Message: "未登录或登录已过期", Status: http.StatusUnauthorized, Err: err})
			return
		}
		storeCarrierIdentity(r.Context(), identity)
		if isAdminPath(r.URL.Path) && identity.Role != "admin" {
			writeErr(w, &APIError{Code: "forbidden", Message: "仅超级管理员可执行此操作", Status: http.StatusForbidden})
			return
		}
		ctx := logging.WithActor(r.Context(), identity.ID, identity.TeamID)
		ctx = context.WithValue(ctx, identityContextKey, identity)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func bearerToken(r *http.Request) string {
	value := strings.TrimSpace(r.Header.Get("Authorization"))
	if len(value) >= 7 && strings.EqualFold(value[:7], "Bearer ") {
		return strings.TrimSpace(value[7:])
	}
	return ""
}

// publicAuthPath 统一鉴权中间件放行路径（接口自鉴权，绝不匿名服务数据）：
// 健康检查、登录三接口、各对接直推（凭 X-Ingest-Token）、分享页与票据回源（票据即鉴权）。
// netdisk /preview/{ticket} 票据取流放行，但 /preview/sign 登录态签发不放行；
// admin 路径与 /auth/logout 一律不放行（唯一例外见下方日志导出 download）。
// 日志导出唯一例外：GET /api/v1/app-logs/export/download?ticket= 凭超管签发的 60 秒、一次性、
// 仅 export 用途 capability ticket 放行（持票者无需 JWT）；签发接口与其余 app-logs 子路径仍超管。
func publicAuthPath(path string) bool {
	if path == "/healthz" || path == "/api/v1/auth/login" || path == "/api/v1/auth/wx-login" || path == "/api/v1/auth/app-login" || path == "/api/v1/safeday/callback" || path == "/api/v1/sgcc/callback" || path == "/api/v1/treehazard/ingest" || path == "/api/v1/crossing/ingest" || path == "/api/v1/callme/ingest-chat" || path == "/api/v1/chatlog/ingest" {
		return true
	}
	// 日志导出一次性票据即鉴权（短时、一次性、仅导出用途）；签发接口仍需超管登录。
	if path == "/api/v1/app-logs/export/download" {
		return true
	}
	for _, prefix := range []string{"/api/v1/share/", "/api/v1/office/raw/", "/api/v1/office/callback/", "/api/v1/netdisk/public/share/"} {
		if strings.HasPrefix(path, prefix) {
			return true
		}
	}
	if strings.HasPrefix(path, "/api/v1/netdisk/preview/") {
		// 预览票据取流免登录（票据即鉴权）；签发接口必须登录
		return path != "/api/v1/netdisk/preview/sign"
	}
	if strings.HasPrefix(path, "/api/v1/safeday/records/") {
		suffix := strings.TrimPrefix(path, "/api/v1/safeday/records/")
		parts := strings.Split(suffix, "/")
		return len(parts) >= 2 && parts[0] != "" && parts[1] == "download" && (len(parts) == 2 || len(parts) == 3 && parts[2] != "")
	}
	return false
}

// fileDownloadPath 文件下载/预览回源路径（GET/HEAD 且无 Authorization 头时允许 ?token= 兜底）。
// 仅限二进制下载与在线预览回源接口，列表/管理接口一律不接受 query token。
func fileDownloadPath(path string) bool {
	switch {
	case path == "/api/v1/worklog/photos.pdf",
		strings.HasPrefix(path, "/api/v1/worklog/task-sheet"),
		strings.HasPrefix(path, "/api/v1/worklog/fee-sheet"),
		strings.HasPrefix(path, "/api/v1/worklog/dispatch-sheet"),
		strings.HasPrefix(path, "/api/v1/treehazard/export"),
		strings.HasPrefix(path, "/api/v1/crossing/export"),
		strings.HasPrefix(path, "/api/v1/office/raw/"),
		strings.HasPrefix(path, "/api/v1/netdisk/preview/"),
		path == "/api/v1/netdisk/download" || path == "/api/v1/netdisk/download/dir",
		path == "/api/v1/netdisk/archive/download" || path == "/api/v1/netdisk/thumb",
		path == "/api/v1/teamdata/towers/template" || path == "/api/v1/teamdata/oplines/template":
		return true
	}
	if strings.HasPrefix(path, "/api/v1/worklog/photos/") && strings.HasSuffix(path, "/download") {
		return true
	}
	if strings.HasPrefix(path, "/api/v1/worklog/logs/") && (strings.HasSuffix(path, "/remark-download") || strings.HasSuffix(path, "/remark-preview")) {
		return true
	}
	if strings.HasPrefix(path, "/api/v1/safeday/records/") {
		suffix := strings.TrimPrefix(path, "/api/v1/safeday/records/")
		parts := strings.Split(suffix, "/")
		return len(parts) >= 2 && (parts[1] == "download" || parts[1] == "preview")
	}
	if strings.HasPrefix(path, "/api/v1/kvm/devices/") && strings.HasSuffix(path, "/download") {
		return true
	}
	return false
}

// isAdminPath 管理路径超管门控：/api/v1/admin/*、应用日志、微信智能应答管理
// （统一入口 /api/v1/wechat-answer/ 与模块直挂 /api/v1/wechatanswer/ 同口径）。
// 应用日志唯一例外：GET /api/v1/app-logs/export/download 由 publicAuthPath 按一次性 ticket
// 先行放行；其签发接口与其余子路径仍在此处要求超管。
func isAdminPath(path string) bool {
	return strings.HasPrefix(path, "/api/v1/admin/") || path == "/api/v1/app-logs" ||
		strings.HasPrefix(path, "/api/v1/app-logs/") ||
		strings.HasPrefix(path, "/api/v1/wechat-answer/") || path == "/api/v1/wechat-answer" ||
		strings.HasPrefix(path, "/api/v1/wechatanswer/") || path == "/api/v1/wechatanswer"
}

// ---- 登录兼容（数字信封，与 server/src/routes/auth.js + 小程序 err.code 判定对齐） ----

// 鉴权域数字业务码（Node routes/auth.js 的 fail() 数值码；小程序按 err.code === 40313 等判定）。
const (
	authCodeBadParam  = 40001 // 参数错误：缺账号密码 / 请求体不是合法 JSON
	authCodeNoWXCode  = 40002 // 缺少微信 code
	authCodeWXFailed  = 40012 // 微信登录失败（code2session 报错）
	authCodeBadCreds  = 40111 // 账号或密码错误
	authCodeNoApp     = 40301 // 暂无该应用的使用权限
	authCodeDisabled  = 40302 // 账号已被禁用
	authCodeWXUnbound = 40313 // 微信号未绑定账号
	authCodeRateLimit = 42901 // 尝试过于频繁
	authCodeInternal  = 50000 // 服务器开小差了
	authCodeNotConfig = 50301 // 未配置 / 未接入（微信登录、认证服务）
)

// WXUser 小程序用户形状（与 Node routes/auth.js publicUser 一致）；
// openid / session_key 绝不进入本结构，绑定状态只折算为 wx_bound。
type WXUser struct {
	ID       int64  `json:"id"`
	Username string `json:"username"`
	Nickname string `json:"nickname"`
	Avatar   string `json:"avatar"`
	Team     string `json:"team"`    // 班组名（未分配为空串）
	TeamID   *int64 `json:"team_id"` // 未分配为 null
	Role     string `json:"role"`
	WXBound  bool   `json:"wx_bound"` // 是否已绑定微信
}

// WXLoginData POST /auth/login 成功载荷：{token,expires_in,user,wx_bound[,bind_message]}。
// wx_bound 为本次登录是否新完成微信绑定（user.wx_bound 为账号是否已绑定）；
// bind_message 仅绑定尝试失败时下发，成功登录不被绑定失败阻断。
type WXLoginData struct {
	Token       string `json:"token"`
	ExpiresIn   int64  `json:"expires_in"`
	User        WXUser `json:"user"`
	WXBound     bool   `json:"wx_bound"`
	BindMessage string `json:"bind_message,omitempty"`
}

// WXSessionData POST /auth/wx-login 成功载荷：{token,expires_in,user}（无 wx_bound 字段）。
type WXSessionData struct {
	Token     string `json:"token"`
	ExpiresIn int64  `json:"expires_in"`
	User      WXUser `json:"user"`
}

// AppLoginData POST /auth/app-login 成功载荷：{user}（不签发本平台 JWT，会话由调用方管理）。
type AppLoginData struct {
	User WXUser `json:"user"`
}

// WXLoginService 小程序登录兼容扩展（authwx 模块实现）：
// /auth/login 的 wx_code 自动绑定、/auth/wx-login 微信登录、/auth/app-login 应用登录。
// 模块挂载后 /auth/login 由模块统一注册；未挂载时核心回退纯账号密码登录。
type WXLoginService interface {
	LoginWithWxCode(ctx context.Context, clientIP, username, password, client, wxCode string) (WXLoginData, error)
	WXLogin(ctx context.Context, clientIP, code string) (WXSessionData, error)
	AppLogin(ctx context.Context, clientIP, username, password, appKey string) (AppLoginData, error)
}

// writeAuthOK 写出登录域成功信封（{code:0,message,data}）。
func writeAuthOK(w http.ResponseWriter, message string, data any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]any{"code": CodeOK, "message": message, "data": data})
}

// writeAuthFail 写出登录域错误信封（{code,message}，code 为 JSON 数值，同 Node fail()）。
func writeAuthFail(w http.ResponseWriter, status, code int, message string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"code": code, "message": message})
}

// writeAuthErr 错误映射为登录域数字信封：APIError 的数字业务码还原为 JSON 数值，
// 凭证错误 401/40111，其余兜底 500/50000。
func writeAuthErr(w http.ResponseWriter, err error) {
	var ae *APIError
	if errors.As(err, &ae) {
		code := authCodeInternal
		if n, convErr := strconv.Atoi(ae.Code); convErr == nil {
			code = n
		}
		status := ae.Status
		if status == 0 {
			status = http.StatusInternalServerError
		}
		msg := ae.Message
		if msg == "" {
			msg = "服务器开小差了，请稍后再试"
		}
		writeAuthFail(w, status, code, msg)
		return
	}
	if errors.Is(err, auth.ErrInvalidCredentials) {
		writeAuthFail(w, http.StatusUnauthorized, authCodeBadCreds, "账号或密码错误")
		return
	}
	if errors.Is(err, context.Canceled) {
		writeAuthFail(w, http.StatusRequestTimeout, authCodeBadCreds, "账号或密码错误")
		return
	}
	writeAuthFail(w, http.StatusInternalServerError, authCodeInternal, "服务器开小差了，请稍后再试")
}

// identityToWXUser 无 SQL 场景（fixture / 回退登录）下把 auth.Identity 映射为小程序用户形状。
func identityToWXUser(id auth.Identity) WXUser {
	user := WXUser{ID: id.ID, Username: id.Username, Nickname: id.Nickname, Role: id.Role, WXBound: false}
	if id.TeamID != 0 {
		teamID := id.TeamID
		user.TeamID = &teamID
	}
	return user
}

// clientIP 取请求来源 IP（与 Node req.ip 同口径：不信任 X-Forwarded-For）。
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func loginHandler(a Authenticator, events EventRecorder) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Username string `json:"username"`
			Password string `json:"password"`
			WXCode   string `json:"wx_code"`
			Client   string `json:"client"`
		}
		if err := decodeJSON(r, &body); err != nil {
			writeAuthFail(w, http.StatusBadRequest, authCodeBadParam, "登录参数不是合法 JSON")
			return
		}
		// authwx 模块注入时整体委托（限流 + wx_code 绑定 + 小程序用户形状）。
		if wx, ok := a.(WXLoginService); ok {
			data, err := wx.LoginWithWxCode(r.Context(), clientIP(r), strings.TrimSpace(body.Username), body.Password, body.Client, body.WXCode)
			if err != nil {
				recordAuthFailure(r, events, strings.TrimSpace(body.Username), body.Client, err)
				writeAuthErr(w, err)
				return
			}
			recordAuthSuccess(r, events, data.User.ID, teamIDOf(data.User.TeamID), data.User.Username, body.Client)
			writeAuthOK(w, "ok", data)
			return
		}
		if a == nil {
			writeAuthFail(w, http.StatusServiceUnavailable, authCodeNotConfig, "认证服务未接入")
			return
		}
		username := strings.TrimSpace(body.Username)
		if username == "" || body.Password == "" {
			writeAuthFail(w, http.StatusBadRequest, authCodeBadParam, "请输入账号和密码")
			return
		}
		result, err := a.Login(r.Context(), username, body.Password, body.Client)
		if err != nil {
			recordAuthFailure(r, events, username, body.Client, err)
			writeAuthErr(w, err)
			return
		}
		recordAuthSuccess(r, events, result.User.ID, result.User.TeamID, result.User.Username, body.Client)
		writeAuthOK(w, "ok", WXLoginData{Token: result.Token, ExpiresIn: result.ExpiresIn, User: identityToWXUser(result.User)})
	}
}

func logoutHandler(a Authenticator, events EventRecorder) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if a == nil {
			writeAuthFail(w, http.StatusServiceUnavailable, authCodeNotConfig, "认证服务未接入")
			return
		}
		if err := a.Logout(r.Context(), bearerToken(r), time.Now().Add(24*time.Hour)); err != nil {
			recordEvent(r.Context(), events, "warn", "security", "security.logout.failed",
				"退出登录失败", logsafeText(err.Error()), nil)
			writeAuthErr(w, err)
			return
		}
		userID, teamID := logging.ActorFromContext(r.Context())
		recordEvent(r.Context(), events, "info", "security", "security.logout",
			"退出登录", "logout", map[string]any{"user_id": userID, "team_id": teamID})
		writeAuthOK(w, "已退出登录", nil)
	}
}

// recordAuthSuccess 登录成功安全事件（不记录密码/令牌）。
func recordAuthSuccess(r *http.Request, events EventRecorder, userID, teamID int64, username, client string) {
	ctx := logging.WithActor(r.Context(), userID, teamID)
	recordEvent(ctx, events, "info", "security", "security.login.succeeded",
		"登录成功", "login username="+username+" client="+client+" ip="+clientIP(r),
		map[string]any{"username": username, "client": client, "ip": clientIP(r), "user_id": userID, "team_id": teamID})
}

// recordAuthFailure 登录失败安全事件（只记用户名/来源，不记密码与令牌）。
func recordAuthFailure(r *http.Request, events EventRecorder, username, client string, err error) {
	recordEvent(r.Context(), events, "warn", "security", "security.login.failed",
		"登录失败", "login failed username="+username+" client="+client+" reason="+logsafeText(err.Error()),
		map[string]any{"username": username, "client": client, "ip": clientIP(r), "reason": logsafeText(err.Error())})
}

// teamIDOf 小程序用户形状的 team_id 指针转 int64。
func teamIDOf(id *int64) int64 {
	if id == nil {
		return 0
	}
	return *id
}

// logsafeText 登录事件自由文本脱敏。
func logsafeText(s string) string { return logsafe.TextForLog(s) }

func decodeJSON(r *http.Request, target any) error {
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	return dec.Decode(target)
}
