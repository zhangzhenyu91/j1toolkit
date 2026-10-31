// Package authwx 迁移小程序登录兼容（server/src/routes/auth.js + pages/login 契约）：
// POST /auth/login（账号密码 + 可选 wx_code 自动绑定微信）、POST /auth/wx-login（code2session
// 换 openid 后按 openid 找账号，仅启用账号签发 JWT）、POST /auth/app-login（账号密码 +
// 应用权限校验，不签发本平台 JWT）。微信 code2session 走运行时配置 WX_APPID / WX_SECRET
// （config.Store 注册表既有键），HTTP 适配可注入（测试注入假实现，绝不连微信）；openid /
// session_key 仅服务内部使用，绝不写入响应。响应沿用数字信封（server/src/utils/resp.js 口径，
// 小程序按 err.code === 40313 等数值判定）。
package authwx

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"golang.org/x/crypto/bcrypt"

	"shade/internal/auth"
	"shade/internal/httpapi"
	"shade/internal/logsafe"
)

// ConfigGet 运行时配置读取（*config.Store 天然满足；测试可注入假实现）。
// 微信 AppID / 密钥取键 WX_APPID / WX_SECRET，配置写入走既有注册表，无需新增。
type ConfigGet interface {
	Get(ctx context.Context, key string) (string, error)
}

// Authenticator 模块构造注入的认证核心（JWT 签发适配）；*auth.Service 天然满足。
// 只要求免密签发 IssueToken（微信登录用）；密码校验由本模块按 Node 同口径直查 sys_user + bcrypt，
// 若核心另具 Authenticate / Logout（如 *auth.Service）则本模块可兼作 httpapi.Authenticator。
type Authenticator interface {
	IssueToken(ctx context.Context, user auth.Identity, client string) (auth.LoginResult, error)
}

// Module 小程序登录兼容模块：实现 httpapi.Module（注册登录三接口）与 httpapi.WXLoginService；
// 可直接作为 Deps.Auth 装配（需核心另具 Authenticate / Logout）。
type Module struct {
	db   *sql.DB
	cfg  ConfigGet
	core Authenticator

	// WX code2session 适配；nil 时用 &HTTPCode2Session{}（生产直连微信）。
	// 测试必须注入假实现，绝不连微信。
	WX Code2Sessioner
	// Events 安全事件（登录/绑定；nil 不额外记录）。
	Events httpapi.EventRecorder

	mu    sync.Mutex
	fails map[string]loginAttempt // 登录失败限流（同 Node loginFailMap）
}

// New 构造模块：db 为业务库，cfg 读运行时配置（WX_APPID / WX_SECRET），core 为 JWT 签发适配
// （auth.NewService 产物即可）。
func New(db *sql.DB, cfg ConfigGet, core Authenticator) *Module {
	return &Module{db: db, cfg: cfg, core: core, fails: map[string]loginAttempt{}}
}

var (
	_ httpapi.Module         = (*Module)(nil)
	_ httpapi.WXLoginService = (*Module)(nil)
	_ httpapi.Authenticator  = (*Module)(nil)
)

// RegisterRoutes 注册登录兼容路由（挂在 /api/v1 之下；三接口均为公开路径，见 httpapi.publicAuthPath；
// admin 路径与 /auth/logout 一律不放行）。模块挂载后核心不再重复注册 /auth/login。
func (m *Module) RegisterRoutes(r chi.Router) {
	r.Post("/auth/login", m.handleLogin)
	r.Post("/auth/wx-login", m.handleWXLogin)
	r.Post("/auth/app-login", m.handleAppLogin)
}

func (m *Module) handleLogin(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Username string `json:"username"`
		Password string `json:"password"`
		WXCode   string `json:"wx_code"`
		Client   string `json:"client"`
	}
	if err := decodeJSON(r, &body); err != nil {
		fail(w, http.StatusBadRequest, codeBadParam, "登录参数不是合法 JSON")
		return
	}
	data, err := m.LoginWithWxCode(r.Context(), clientIP(r), strings.TrimSpace(body.Username), body.Password, body.Client, body.WXCode)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeOK(w, data)
}

func (m *Module) handleWXLogin(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Code string `json:"code"`
	}
	if err := decodeJSON(r, &body); err != nil {
		fail(w, http.StatusBadRequest, codeBadParam, "登录参数不是合法 JSON")
		return
	}
	data, err := m.WXLogin(r.Context(), clientIP(r), body.Code)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeOK(w, data)
}

func (m *Module) handleAppLogin(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Username string `json:"username"`
		Password string `json:"password"`
		AppKey   string `json:"app_key"`
	}
	if err := decodeJSON(r, &body); err != nil {
		fail(w, http.StatusBadRequest, codeBadParam, "登录参数不是合法 JSON")
		return
	}
	data, err := m.AppLogin(r.Context(), clientIP(r), strings.TrimSpace(body.Username), body.Password, strings.TrimSpace(body.AppKey))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeOK(w, data)
}

// LoginWithWxCode 账号密码登录，可选携带 wx_code：账号未绑定微信时自动绑定当前微信号
// （openid 已绑其他账号则随密码 + 微信双重校验转移）。绑定失败不阻断登录，仅 bind_message 告知；
// 微信未配置时静默跳过绑定（同 Node 口径）。返回小程序用户形状，wx_bound 为本次是否新完成绑定。
func (m *Module) LoginWithWxCode(ctx context.Context, clientIP, username, password, client, wxCode string) (httpapi.WXLoginData, error) {
	if m.limited(clientIP) {
		return httpapi.WXLoginData{}, apiErr(http.StatusTooManyRequests, codeRateLimit, "尝试过于频繁，请稍后再试")
	}
	if username == "" || password == "" {
		return httpapi.WXLoginData{}, apiErr(http.StatusBadRequest, codeBadParam, "请输入账号和密码")
	}
	user, err := m.verifyPassword(ctx, username, password)
	if err != nil {
		if errors.Is(err, auth.ErrInvalidCredentials) {
			m.recordFail(clientIP)
		}
		m.security(ctx, "warn", "security.login.failed", "登录失败",
			"login failed username="+username+" ip="+clientIP,
			map[string]any{"username": username, "client": client, "ip": clientIP})
		return httpapi.WXLoginData{}, bizErr(err)
	}

	wxBound := false
	bindMessage := ""
	if user.OpenID == "" && wxCode != "" {
		if appID, secret, ok := m.wxConfig(ctx); ok {
			sess, err := m.code2Session(ctx, appID, secret, wxCode)
			if err == nil {
				err = m.bindOpenID(ctx, user.ID, sess.OpenID, sess.UnionID)
			}
			if err != nil {
				// 绑定失败不影响本次登录，仅记录并告知前端（同 Node）；安全事件不落 openid/session_key。
				bindMessage = "微信号绑定失败，可稍后在登录时重试"
				m.security(ctx, "warn", "security.wx.bind_failed", "微信号绑定失败",
					"wx bind failed user="+user.Username+" ip="+clientIP,
					map[string]any{"username": user.Username, "ip": clientIP, "user_id": user.ID, "team_id": user.TeamID, "reason": "bind_failed"})
			} else {
				wxBound = true
				user.OpenID = sess.OpenID // 内存同步绑定结果：user.wx_bound 即时为真
			}
		}
	}

	m.clearFail(clientIP)
	result, err := m.issue(ctx, user, client)
	if err != nil {
		return httpapi.WXLoginData{}, bizErr(err)
	}
	m.security(ctx, "info", "security.login.succeeded", "登录成功",
		"login username="+user.Username+" client="+client+" ip="+clientIP,
		map[string]any{"username": user.Username, "client": client, "ip": clientIP, "user_id": user.ID, "team_id": user.TeamID})
	if wxBound {
		m.security(ctx, "info", "security.wx.bound", "微信号绑定成功",
			"wx bound user="+user.Username+" ip="+clientIP,
			map[string]any{"username": user.Username, "ip": clientIP, "user_id": user.ID, "team_id": user.TeamID})
	}
	return httpapi.WXLoginData{
		Token:       result.Token,
		ExpiresIn:   result.ExpiresIn,
		User:        user.Public(),
		WXBound:     wxBound,
		BindMessage: bindMessage,
	}, nil
}

// WXLogin 微信登录：小程序 wx.login 得 code，code2session 换 openid 后按 openid 找账号；
// 仅已绑定且启用的账号签发 JWT（未绑定 40313 引导先账号密码登录，禁用 40302）。
// 响应只含 token/expires_in/user，openid / session_key 不外泄。
func (m *Module) WXLogin(ctx context.Context, clientIP, code string) (httpapi.WXSessionData, error) {
	if m.limited(clientIP) {
		return httpapi.WXSessionData{}, apiErr(http.StatusTooManyRequests, codeRateLimit, "尝试过于频繁，请稍后再试")
	}
	if code == "" {
		return httpapi.WXSessionData{}, apiErr(http.StatusBadRequest, codeNoWXCode, "缺少微信 code")
	}
	appID, secret, ok := m.wxConfig(ctx)
	if !ok {
		return httpapi.WXSessionData{}, apiErr(http.StatusServiceUnavailable, codeNotConfig, "微信登录未配置（WX_APPID / WX_SECRET）")
	}
	sess, err := m.code2Session(ctx, appID, secret, code)
	if err != nil {
		// 对匿名调用方只回固定文案；原始错误（可能含 URL/appid/secret）仅脱敏写入安全事件。
		detail := logsafe.TextForLog(err.Error())
		m.security(ctx, "warn", "security.login.failed", "微信登录失败",
			"wx login failed ip="+clientIP+": "+detail,
			map[string]any{"channel": "wx", "ip": clientIP, "reason": "wx_code_failed", "error": detail})
		return httpapi.WXSessionData{}, apiErr(http.StatusBadRequest, codeWXFailed, "微信登录失败，请稍后重试")
	}
	user, err := m.userByOpenID(ctx, sess.OpenID)
	if errors.Is(err, sql.ErrNoRows) {
		m.recordFail(clientIP)
		m.security(ctx, "warn", "security.login.failed", "微信登录失败",
			"wx login failed ip="+clientIP, map[string]any{"channel": "wx", "ip": clientIP, "reason": "unbound"})
		return httpapi.WXSessionData{}, apiErr(http.StatusForbidden, codeWXUnbound,
			"该微信号尚未绑定账号，请先使用账号密码登录一次后再使用微信登录")
	}
	if err != nil {
		return httpapi.WXSessionData{}, bizErr(err)
	}
	if user.Status != 1 {
		m.recordFail(clientIP)
		m.security(ctx, "warn", "security.login.failed", "微信登录失败",
			"wx login failed ip="+clientIP, map[string]any{"channel": "wx", "ip": clientIP, "reason": "disabled"})
		return httpapi.WXSessionData{}, apiErr(http.StatusForbidden, codeDisabled, "账号已被禁用，请联系管理员")
	}
	m.clearFail(clientIP)
	result, err := m.issue(ctx, user, "")
	if err != nil {
		return httpapi.WXSessionData{}, bizErr(err)
	}
	m.security(ctx, "info", "security.login.succeeded", "微信登录成功",
		"wx login username="+user.Username+" ip="+clientIP,
		map[string]any{"username": user.Username, "ip": clientIP, "channel": "wx", "user_id": user.ID, "team_id": user.TeamID})
	return httpapi.WXSessionData{Token: result.Token, ExpiresIn: result.ExpiresIn, User: user.Public()}, nil
}

// AppLogin 应用登录校验（供外部应用复用本平台账号体系）：一次完成「账号密码 + 指定应用权限」
// 校验；不签发本平台 JWT，会话由调用方自行管理（同 Node app-login）。
func (m *Module) AppLogin(ctx context.Context, clientIP, username, password, appKey string) (httpapi.AppLoginData, error) {
	if m.limited(clientIP) {
		return httpapi.AppLoginData{}, apiErr(http.StatusTooManyRequests, codeRateLimit, "尝试过于频繁，请稍后再试")
	}
	if username == "" || password == "" || appKey == "" {
		return httpapi.AppLoginData{}, apiErr(http.StatusBadRequest, codeBadParam, "请输入账号和密码")
	}
	user, err := m.verifyPassword(ctx, username, password)
	if err != nil {
		if errors.Is(err, auth.ErrInvalidCredentials) {
			m.recordFail(clientIP)
		}
		m.security(ctx, "warn", "security.login.failed", "应用登录失败",
			"app login failed username="+username+" ip="+clientIP,
			map[string]any{"username": username, "app_key": appKey, "channel": "app", "ip": clientIP, "reason": "bad_credentials"})
		return httpapi.AppLoginData{}, bizErr(err)
	}
	allow, err := m.hasAppPermission(ctx, user.ID, appKey)
	if err != nil {
		return httpapi.AppLoginData{}, bizErr(err)
	}
	if !allow {
		m.security(ctx, "warn", "security.login.failed", "应用登录失败",
			"app login failed username="+username+" ip="+clientIP,
			map[string]any{"username": username, "app_key": appKey, "channel": "app", "ip": clientIP, "reason": "no_app", "user_id": user.ID, "team_id": user.TeamID})
		return httpapi.AppLoginData{}, apiErr(http.StatusForbidden, codeNoApp, "暂无该应用的使用权限")
	}
	m.clearFail(clientIP)
	m.security(ctx, "info", "security.login.succeeded", "应用登录成功",
		"app login username="+user.Username+" app="+appKey+" ip="+clientIP,
		map[string]any{"username": user.Username, "app_key": appKey, "ip": clientIP, "channel": "app", "user_id": user.ID, "team_id": user.TeamID})
	return httpapi.AppLoginData{User: user.Public()}, nil
}

// Login 纯账号密码登录（无 wx_code 绑定），满足 httpapi.Authenticator，供 Deps.Auth 装配复用。
func (m *Module) Login(ctx context.Context, username, password, client string) (auth.LoginResult, error) {
	user, err := m.verifyPassword(ctx, strings.TrimSpace(username), password)
	if err != nil {
		return auth.LoginResult{}, err
	}
	return m.issue(ctx, user, client)
}

// Authenticate 委托认证核心校验 JWT；核心仅提供签发适配时报未登录（无从校验令牌）。
func (m *Module) Authenticate(ctx context.Context, token string) (auth.Identity, error) {
	if c, ok := m.core.(interface {
		Authenticate(context.Context, string) (auth.Identity, error)
	}); ok {
		return c.Authenticate(ctx, token)
	}
	return auth.Identity{}, auth.ErrUnauthorized
}

// Logout 委托认证核心拉黑令牌；核心仅提供签发适配时按无黑名单降级（同 auth.Service nil Blacklist）。
func (m *Module) Logout(ctx context.Context, token string, expiresAt time.Time) error {
	if c, ok := m.core.(interface {
		Logout(context.Context, string, time.Time) error
	}); ok {
		return c.Logout(ctx, token, expiresAt)
	}
	return nil
}

// verifyPassword 按用户名校验密码并装载登录全行（同 Node：不存在/无密码/禁用/不符统一 40111）。
func (m *Module) verifyPassword(ctx context.Context, username, password string) (*userRow, error) {
	user, err := m.userByUsername(ctx, username)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, auth.ErrInvalidCredentials
	}
	if err != nil {
		return nil, err
	}
	if user.PasswordHash == "" || bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(password)) != nil {
		return nil, auth.ErrInvalidCredentials
	}
	return user, nil
}

// issue 免密签发 JWT（微信登录/绑定后复用；client=="web" 短时效由核心控制）。
func (m *Module) issue(ctx context.Context, user *userRow, client string) (auth.LoginResult, error) {
	if m.core == nil {
		return auth.LoginResult{}, errNoCore
	}
	return m.core.IssueToken(ctx, auth.Identity{
		ID:       user.ID,
		Username: user.Username,
		Nickname: user.Nickname,
		Role:     user.Role,
		TeamID:   user.TeamID,
	}, client)
}

// wxConfig 读运行时微信配置（WX_APPID / WX_SECRET）；任一缺失或为空即视为未配置。
func (m *Module) wxConfig(ctx context.Context) (appID, secret string, ok bool) {
	if m.cfg == nil {
		return "", "", false
	}
	appID, errAppID := m.cfg.Get(ctx, "WX_APPID")
	secret, errSecret := m.cfg.Get(ctx, "WX_SECRET")
	if errAppID != nil || errSecret != nil || appID == "" || secret == "" {
		return "", "", false
	}
	return appID, secret, true
}

// code2Session 走注入适配（nil 用生产 HTTP 适配）换取微信会话。
func (m *Module) code2Session(ctx context.Context, appID, secret, code string) (WXSession, error) {
	if m.WX != nil {
		return m.WX.Code2Session(ctx, appID, secret, code)
	}
	return (&HTTPCode2Session{}).Code2Session(ctx, appID, secret, code)
}

// errNoCore 认证核心未接入（JWT 签发适配未注入）。
var errNoCore = errors.New("认证核心未接入")

// bizErr 把内部错误映射为登录域数字业务码错误（40111 凭证错误 / 50301 未接入 / 50000 其余）。
func bizErr(err error) error {
	switch {
	case errors.Is(err, auth.ErrInvalidCredentials):
		return apiErr(http.StatusUnauthorized, codeBadCreds, "账号或密码错误")
	case errors.Is(err, errNoDB), errors.Is(err, errNoCore):
		return apiErr(http.StatusServiceUnavailable, codeNotConfig, err.Error())
	default:
		return apiErr(http.StatusInternalServerError, codeInternal, "服务器开小差了，请稍后再试")
	}
}
