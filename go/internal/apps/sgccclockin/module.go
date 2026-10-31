// Package sgccclockin 迁移 server/src/sgccclockin：商旅打卡——账号绑定（图形码/滑块双通道）、
// 同步/过期探测、照片操作任务、每日核查与午间/傍晚提醒、出口代理探测。
// 协议加解密复用 compat/cryptoaudio（jsonm/jsonx 双通道），外部调用可注入，测试不实连。
package sgccclockin

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"shade/internal/auth"
	"shade/internal/httpapi"
	"shade/internal/logging"
)

// AppPermit 校验用户是否拥有指定应用（app_key）的使用权限（可注入；nil 不拦截）。
type AppPermit interface {
	Allow(ctx context.Context, userID int64, appKey string) (bool, error)
}

// Notifier 商旅通知（登录过期 / 提醒 / 核查日报 / 代理异常；可注入 fake）。
type Notifier interface {
	// NotifyTokenExpired 登录过期：本人 + 全部超管。
	NotifyTokenExpired(ctx context.Context, account Account) error
	// NotifyMissingEndClockin 每日核查附加：开始卡已打、结束卡未打 → 本人/班管/超管。
	NotifyMissingEndClockin(ctx context.Context, teamID int64, date string, rows []MissingClockinRow) error
	// NotifyDailySyncResult 核查日报（每班一封，仅超管）。
	NotifyDailySyncResult(ctx context.Context, teamID int64, date string, summary SyncSummary) error
	// NotifyRemind 提醒消息（kind=start/end；本人 + 班组群两条口径由实现合并）。
	NotifyRemind(ctx context.Context, kind string, rows []MissingClockinRow) error
	// NotifyProxyState 代理异常/恢复（仅超管）。
	NotifyProxyState(ctx context.Context, errMsg string, recovered bool) error
	// NotifyManualCheck 提醒前同步失败 → 人工核查通知（超管 + 涉及班管）。
	NotifyManualCheck(ctx context.Context, label string, rows []MissingClockinRow, reason string) error
}

// AppLogger 应用结构化日志写入（*logging.Logger 满足；nil 时以 DB 默认构造，见 appLog）。
type AppLogger interface {
	Info(ctx context.Context, category, event, messageCN, raw string, fields map[string]any) error
	Warn(ctx context.Context, category, event, messageCN, raw string, fields map[string]any) error
	Error(ctx context.Context, category, event, messageCN, raw string, fields map[string]any) error
}

// 编译期契约：生产装配按此注入。
var _ AppLogger = (*logging.Logger)(nil)

// appLogCode 应用日志模块标识（app_log.app_code，与 httpapi 路由口径 sgcc-clockin 一致，
// 属公共日志渠道，无需 sys_app 登记）。
const appLogCode = "sgcc-clockin"

// Client 商旅协议客户端接口（生产用 *Protocol，测试用 fake）。
type Client interface {
	LoginCaptcha(ctx context.Context, mobile string) (string, error)
	LoginSendSmsV2(ctx context.Context, mobile, checkImgCode string) (map[string]any, error)
	LoginSendSmsSlapp(ctx context.Context, mobile, captchaToken, constID string, opts CallOpts) (map[string]any, error)
	LoginBySmsSlapp(ctx context.Context, mobile, checkCode, captchaToken, constID string, opts CallOpts) (string, error)
	LoginByPasswordV3(ctx context.Context, mobile, password, checkImgCode string, opts CallOpts) (string, error)
	LoginByPassword(ctx context.Context, mobile, password, captchaToken, constID string, opts CallOpts) (string, error)
	// DayNew 拉某日打卡全量（登录态探测 + 对账共用）；应答不可解返回 (nil, nil)。
	DayNew(ctx context.Context, token, date string, opts CallOpts) (map[string]any, error)
	// MarkClockin 打卡（seq 1 首次 / 2 结束 / update 更新）。
	MarkClockin(ctx context.Context, token string, payload map[string]any, opts CallOpts) (map[string]any, error)
	// SaveFee 保存费用（完整模板 read-modify-write，slapp 通道）。
	SaveFee(ctx context.Context, token string, payload map[string]any, opts CallOpts) (map[string]any, error)
	// GetFeeInfoNew 取费用模板（提交装配的数据源）。
	GetFeeInfoNew(ctx context.Context, token string, params FeeTplParams, opts CallOpts) (map[string]any, error)
	// SaveFeeInfoNew 完整费用模板保存（toMoshi 装配 + overrides 覆盖 value/data，slapp 通道）。
	SaveFeeInfoNew(ctx context.Context, token, date string, clockTemplate map[string]any, overrides map[string]string, opts CallOpts) (map[string]any, error)
	// UploadImageV2 费用凭证照片上传（fastdfs multipart，slapp 通道；返回 fileInfoId/访问地址）。
	UploadImageV2(ctx context.Context, token string, buf []byte, opts CallOpts) (UploadedImage, error)
	// ProbeProxy 出口代理健康探测（TCP 建连）。
	ProbeProxy() (ok bool, skipped bool, errMsg string)
	// SelfCheck 启动通道自检（jsonm/slapp 双通道拉池）。
	SelfCheck(ctx context.Context) error
}

// Account 商旅绑定账号行（worklog_sgcc_account）。
type Account struct {
	ID            int64
	TeamID        int64
	UserID        int64
	MemberID      int64
	Mobile        string
	Token         string
	DeviceType    string
	SystemVersion string
	TokenStatus   int
	LastCheckAt   *time.Time // last_check_at（代绑行等口径一致）
	CardMemberID  int64      // 联表解析的卡片成员 id（loadEntries 同口径，可空）
}

// MissingClockinRow 提醒/核查查询结果行。
type MissingClockinRow struct {
	TeamID   int64
	MemberID int64
	Name     string
	UserID   int64
	StartHM  string // 已打开始卡时刻 'HH:MM'（未打为空）
}

// SyncSummary 核查日报汇总。
type SyncSummary struct {
	TeamName string
	OK       int
	Diff     int
	Fail     int
	Fails    []SyncLogRow
}

// Module 是商旅打卡应用模块（出工日志扩展能力，应用权限走 work-log）。
type Module struct {
	DB       *sql.DB
	Permit   AppPermit
	API      Client
	Ops      *PhotoOpManager
	Limiter  *LoginRateLimiter
	Notifier Notifier
	// Log 应用结构化日志（可注入；nil 时以 DB 默认构造 app_code=sgcc-clockin，见 appLog）。
	Log AppLogger
	// Cfg 运行时配置（密钥/代理由 Protocol.Cfg 热生效；此处读排程时刻与并发上限）；nil 用缺省值。
	Cfg ConfigStore
	// Recompense 绑定成功后的补核钩子（member 口径；nil 不补核）。
	Recompense func(ctx context.Context, memberID int64)
	// FetchPhoto 照片原文回源函数（COS 公网地址 GET；nil 用默认 http 客户端 60s 超时）；测试注入 fake/httptest。
	FetchPhoto PhotoFetcher
	// FetchSgccImage 商旅费用照片下载（显式 SGCC_PROXY_URL 时与 API 同出口代理，无显式代理时直连且不继承环境代理）；测试注入 fake。
	FetchSgccImage PhotoFetcher
	// Store 对象存储（费用照片拉下入库 / 删除对账；nil 时拉下方向不可用并记 fail 日志）。
	Store PhotoStore
	// VerifyPhoto 拉下入库照片的水印识别回写钩子（异步触发；nil 跳过）。
	VerifyPhoto PhotoVerifier
	// GeoHTTP 高德定位客户端；nil 使用 8 秒超时默认客户端。
	GeoHTTP *http.Client

	// locks 成员+日期键控互斥：同步单元与打卡/费用/照片关联的 read-modify-write 共用同一键。
	locks keyedMutex
	// syncGate 同步单元全局并发闸门（SGCC_SYNC_CONCURRENCY，默认 4，收敛 1–8）。
	syncGate gate

	photoTransports transportCache // 商旅图片下载代理传输层缓存
}

// New 构造商旅打卡模块。
func New(db *sql.DB) *Module {
	m := &Module{
		DB:      db,
		Limiter: NewLoginRateLimiter(),
	}
	m.Ops = NewPhotoOpManager()
	// 照片任务并发上限按运行时配置热生效（默认 2，收敛 1–4）
	m.Ops.Limit = func() int { return m.photoConcurrency(context.Background()) }
	return m
}

// appLog 写一条应用结构化日志（模块 sgcc-clockin / 类别 business）。
// 未注入 Log 时以 DB 默认构造写入器；写失败仅回退标准日志，绝不影响业务。
func (m *Module) appLog(ctx context.Context, level, event, messageCN, raw string, fields map[string]any) {
	l := m.Log
	if l == nil {
		if m.DB == nil {
			return
		}
		l = logging.New(m.DB, appLogCode)
	}
	var err error
	switch level {
	case "warn":
		err = l.Warn(ctx, "business", event, messageCN, raw, fields)
	case "error":
		err = l.Error(ctx, "business", event, messageCN, raw, fields)
	default:
		err = l.Info(ctx, "business", event, messageCN, raw, fields)
	}
	if err != nil {
		log.Printf("[商旅打卡] 应用日志写入失败（%s）：%v", event, err)
	}
}

// RegisterRoutes 注册 /api/v1/sgcc 下全部路由（挂载在 /api/v1 之下）。
func (m *Module) RegisterRoutes(r chi.Router) {
	r.Route("/sgcc", func(r chi.Router) {
		r.Post("/login/captcha", m.loginCaptcha)
		r.Post("/login/sms", m.loginSms)
		r.Post("/login/bind", m.loginBind)
		r.Get("/account", m.accountGet)
		r.Put("/account/device", m.accountDevice)
		r.Delete("/account", m.accountDelete)
		r.Get("/expired", m.expiredList)
		r.Get("/accounts", m.accountsList)
		r.Put("/accounts/{id}/device", m.accountAdminDevice)
		r.Post("/accounts/bind", m.accountsBind)
		r.Get("/day", m.dayGet)
		r.Post("/clockin", m.clockinPost)
		r.Get("/geo", m.geo)
		r.Get("/fee", m.feeGet)
		r.Post("/fee", m.feePost)
		r.Post("/photos/{id}/resync", m.photoResync)
		r.Get("/op/status", m.opStatus)
		r.Post("/sync/pull", m.syncPull)
		r.Get("/sync/progress", m.syncProgress)
		r.Get("/sync/active", m.syncActive)
		r.Get("/sync/logs", m.syncLogs)
		r.Delete("/sync/logs", m.syncLogsClear)
	})
}

// enter 完成 应用权限（work-log）+ 生效班组解析。
func (m *Module) enter(w http.ResponseWriter, r *http.Request) (auth.Identity, *httpapi.Team, bool) {
	ident, ok := httpapi.IdentityFromContext(r.Context())
	if !ok {
		fail(w, http.StatusUnauthorized, codeNoLogin, "未登录或登录已过期")
		return auth.Identity{}, nil, false
	}
	if m.Permit != nil {
		allow, err := m.Permit.Allow(r.Context(), ident.ID, "work-log")
		if err != nil {
			writeErr(w, err)
			return auth.Identity{}, nil, false
		}
		if !allow {
			fail(w, http.StatusForbidden, codeNoApp, "暂无该应用的使用权限，请联系管理员开通")
			return auth.Identity{}, nil, false
		}
	}
	adapter := httpapi.IdentityAdapter{TeamID: ident.TeamID, Role: ident.Role}
	wanted := wantedTeamID(r)
	team, err := httpapi.ResolveTeam(r.Context(), m.DB, adapter, wanted)
	if err != nil && wanted > 0 {
		// Node resolveReqTeam 口径：超管期望班组不可用（不存在/停用）时回退本人班组，再无则默认班组
		team, err = httpapi.ResolveTeam(r.Context(), m.DB, adapter, 0)
	}
	if err != nil {
		return ident, nil, true // 无可用班组 → 空态（各路由自行判空）
	}
	return ident, &team, true
}

// wantedTeamID 请求期望班组（Node resolveReqTeam 口径：query 优先、其次 body；仅超管生效，
// 非法/缺省落 0 由 ResolveTeam 按角色收敛）。读 body 后原样放回，不影响后续 decodeBody。
func wantedTeamID(r *http.Request) int64 {
	if v := strings.TrimSpace(r.URL.Query().Get("team_id")); v != "" {
		n, _ := strconv.ParseInt(v, 10, 64)
		if n > 0 {
			return n
		}
		return 0
	}
	if r.Body == nil || r.Method == http.MethodGet || r.Method == http.MethodDelete {
		return 0
	}
	buf, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		return 0
	}
	r.Body = io.NopCloser(bytes.NewReader(buf))
	var body map[string]json.RawMessage
	if json.Unmarshal(buf, &body) != nil {
		return 0
	}
	n, _ := strconv.ParseInt(rawString(body["team_id"]), 10, 64)
	if n > 0 {
		return n
	}
	return 0
}

// requireDictAdmin 管理门控：超管任意班组，班组管理员仅本班。
func requireDictAdmin(w http.ResponseWriter, ident auth.Identity, team *httpapi.Team) bool {
	if ident.Role == "admin" {
		return true
	}
	if ident.Role == "team_admin" && team != nil && ident.TeamID == team.ID {
		return true
	}
	fail(w, http.StatusForbidden, codeNotAdmin, "仅管理员可执行此操作")
	return false
}

// overLoginRate 限流判定（kind=captcha/sms/bind）。
func (m *Module) overLoginRate(kind string, userID int64, mobile string) bool {
	if m.Limiter == nil {
		return false
	}
	return m.Limiter.Over(kind, userID, mobile)
}

// loginCaptcha POST /login/captcha：取图形验证码（风控窗口期回 40035，前端降级滑块）。
func (m *Module) loginCaptcha(w http.ResponseWriter, r *http.Request) {
	ident, _, ok := m.enter(w, r)
	if !ok {
		return
	}
	body, err := decodeBody(r)
	if err != nil {
		fail(w, http.StatusBadRequest, codeParam, "参数错误")
		return
	}
	mobile := rawString(body["mobile"])
	if !validMobile(mobile) {
		fail(w, http.StatusBadRequest, codeBadMobile, "手机号格式不正确")
		return
	}
	if m.overLoginRate("captcha", ident.ID, mobile) {
		fail(w, http.StatusTooManyRequests, codeRateLimit, "操作过于频繁，请稍后再试")
		return
	}
	if m.API == nil {
		writeErr(w, httpapi.DependencyUnavailable("商旅协议服务未接入"))
		return
	}
	image, err := m.API.LoginCaptcha(r.Context(), mobile)
	if err != nil {
		fail(w, http.StatusBadRequest, codeCaptchaOff, "图形验证码暂不可用，请使用滑块验证")
		return
	}
	if image != "" && !strings.HasPrefix(image, "data:") {
		image = "data:image/png;base64," + image
	}
	writeOK(w, map[string]any{"image": image})
}

// loginSms POST /login/sms：发短信（图形码 v2 / 滑块 v3 双通道）。
func (m *Module) loginSms(w http.ResponseWriter, r *http.Request) {
	ident, _, ok := m.enter(w, r)
	if !ok {
		return
	}
	body, err := decodeBody(r)
	if err != nil {
		fail(w, http.StatusBadRequest, codeParam, "参数错误")
		return
	}
	mobile := rawString(body["mobile"])
	if !validMobile(mobile) {
		fail(w, http.StatusBadRequest, codeBadMobile, "手机号格式不正确")
		return
	}
	if m.overLoginRate("sms", ident.ID, mobile) {
		fail(w, http.StatusTooManyRequests, codeRateLimit, "操作过于频繁，请稍后再试")
		return
	}
	if m.API == nil {
		writeErr(w, httpapi.DependencyUnavailable("商旅协议服务未接入"))
		return
	}
	checkImgCode := rawString(body["checkImgCode"])
	captchaToken := rawString(body["captchaToken"])
	constID := rawString(body["constId"])
	if checkImgCode != "" {
		d, err := m.API.LoginSendSmsV2(r.Context(), mobile, checkImgCode)
		if err != nil {
			writeErr(w, err)
			return
		}
		if num(d["statusCode"]) != 200 {
			// v2 错误文案取顶层 msg（Node 口径）
			msg := "短信发送失败，请重试"
			if s, ok := d["msg"].(string); ok && s != "" {
				msg = s
			}
			fail(w, http.StatusBadRequest, codeSMSFail, msg)
			return
		}
		writeOK(w, nil)
		return
	}
	if captchaToken == "" {
		fail(w, http.StatusBadRequest, codeNeedRisk, "请先完成图形或滑块验证")
		return
	}
	d, err := m.API.LoginSendSmsSlapp(r.Context(), mobile, captchaToken, constID, CallOpts{})
	if err != nil {
		writeErr(w, err)
		return
	}
	// v3 失败时 statusCode 也是 200，成败看 data.code（成功无 data 或 data.code=0）
	if num(d["statusCode"]) != 200 || bizFailed(d) {
		msg := msgOr(d, "短信发送失败，请重试")
		fail(w, http.StatusBadRequest, codeSMSFail, msg)
		return
	}
	writeOK(w, nil)
}

// num any → float64（JS Number 同口径）。
func num(v any) float64 {
	switch t := v.(type) {
	case float64:
		return t
	case int:
		return float64(t)
	case int64:
		return float64(t)
	case string:
		f, _ := strconv.ParseFloat(strings.TrimSpace(t), 64)
		return f
	}
	return 0
}

// bizFailed 滑块 v3 业务失败（data.code 非 0）。
func bizFailed(d map[string]any) bool {
	data, ok := d["data"].(map[string]any)
	if !ok || data == nil {
		return false
	}
	if code, exists := data["code"]; exists && code != nil {
		return num(code) != 0
	}
	return false
}

// msgOr 取平台错误文案（data.msg → msg → 兜底）。
func msgOr(d map[string]any, fallback string) string {
	if data, ok := d["data"].(map[string]any); ok {
		if s, ok := data["msg"].(string); ok && s != "" {
			return s
		}
	}
	if s, ok := d["msg"].(string); ok && s != "" {
		return s
	}
	return fallback
}

// bindCreds 绑定 / 代绑共用的登录凭据（短信、密码 × 图形码 / 滑块）。
type bindCreds struct {
	Mobile        string
	CheckCode     string
	Password      string
	CheckImgCode  string
	CaptchaToken  string
	ConstID       string
	DeviceType    string
	SystemVersion string
}

// parseBindCreds 解析登录凭据（键名与小程序 / 网页端一致）。
func parseBindCreds(body map[string]json.RawMessage) bindCreds {
	return bindCreds{
		Mobile:        rawString(body["mobile"]),
		CheckCode:     rawString(body["checkCode"]),
		Password:      rawString(body["password"]),
		CheckImgCode:  rawString(body["checkImgCode"]),
		CaptchaToken:  rawString(body["captchaToken"]),
		ConstID:       rawString(body["constId"]),
		DeviceType:    truncateStr(rawString(body["deviceType"]), 64),
		SystemVersion: truncateStr(rawString(body["systemVersion"]), 64),
	}
}

// checkBindCreds 校验登录凭据与限流（loginBind / accountsBind 共用同一规则；失败已写响应）。
func (m *Module) checkBindCreds(w http.ResponseWriter, ident auth.Identity, c bindCreds) bool {
	if !validMobile(c.Mobile) {
		fail(w, http.StatusBadRequest, codeBadMobile, "手机号格式不正确")
		return false
	}
	if c.CheckCode == "" && c.Password == "" {
		fail(w, http.StatusBadRequest, codeNeedCred, "请填写短信验证码或密码")
		return false
	}
	if c.Password != "" && c.CheckImgCode == "" && c.CaptchaToken == "" {
		fail(w, http.StatusBadRequest, codeNeedRisk, "密码登录请先完成图形或滑块验证")
		return false
	}
	if m.overLoginRate("bind", ident.ID, c.Mobile) {
		fail(w, http.StatusTooManyRequests, codeRateLimit, "操作过于频繁，请稍后再试")
		return false
	}
	if m.API == nil {
		writeErr(w, httpapi.DependencyUnavailable("商旅协议服务未接入"))
		return false
	}
	return true
}

// bindToken 用凭据换商旅 token（短信 / 密码双通道；失败已写响应）。
func (m *Module) bindToken(w http.ResponseWriter, r *http.Request, c bindCreds) (string, bool) {
	var token string
	var callErr error
	switch {
	case c.Password != "":
		if c.CheckImgCode != "" {
			token, callErr = m.API.LoginByPasswordV3(r.Context(), c.Mobile, c.Password, c.CheckImgCode, CallOpts{})
		} else {
			token, callErr = m.API.LoginByPassword(r.Context(), c.Mobile, c.Password, c.CaptchaToken, c.ConstID, CallOpts{})
		}
	default:
		token, callErr = m.API.LoginBySmsSlapp(r.Context(), c.Mobile, c.CheckCode, c.CaptchaToken, c.ConstID, CallOpts{})
	}
	if callErr != nil {
		var bizErr *LoginBizError
		if errors.As(callErr, &bizErr) {
			fail(w, http.StatusBadRequest, codePlatform, bizErr.Error())
			return "", false
		}
		writeErr(w, callErr)
		return "", false
	}
	return token, true
}

// bindDevice 设备口径（缺省值与 Node 口径一致）。
func bindDevice(c bindCreds) (devType, sysVer string) {
	devType, sysVer = c.DeviceType, c.SystemVersion
	if devType == "" {
		devType = "Xiaomi 2509FPN0BC"
	}
	if sysVer == "" {
		sysVer = "Android 16"
	}
	return devType, sysVer
}

// loginBind POST /login/bind：短信码或密码换 token 完成绑定（人级唯一归并 + 接管校验）。
func (m *Module) loginBind(w http.ResponseWriter, r *http.Request) {
	ident, team, ok := m.enter(w, r)
	if !ok {
		return
	}
	if team == nil {
		fail(w, http.StatusForbidden, codeNoAssign, "未分配班组，请联系管理员分配")
		return
	}
	body, err := decodeBody(r)
	if err != nil {
		fail(w, http.StatusBadRequest, codeParam, "参数错误")
		return
	}
	creds := parseBindCreds(body)
	if !m.checkBindCreds(w, ident, creds) {
		return
	}

	// 认领出工成员：班组内按昵称匹配
	var memberID sql.NullInt64
	_ = m.DB.QueryRowContext(r.Context(),
		`SELECT id FROM worklog_member WHERE team_id = ? AND name = ?`,
		team.ID, ident.Nickname).Scan(&memberID)

	// 接管前归属校验：该成员绑定行已被其他账号自绑时拒绝
	if memberID.Valid {
		var otherUser sql.NullInt64
		err := m.DB.QueryRowContext(r.Context(),
			`SELECT user_id FROM worklog_sgcc_account WHERE member_id = ?`, memberID.Int64).Scan(&otherUser)
		if err == nil && otherUser.Valid && otherUser.Int64 != ident.ID {
			fail(w, http.StatusConflict, codeBoundOther, "该成员已绑定其他登录账号，请联系对方解绑或请管理员处理")
			return
		}
	}

	// 换 token（短信 / 密码双通道）
	token, ok := m.bindToken(w, r, creds)
	if !ok {
		return
	}
	devType, sysVer := bindDevice(creds)
	if code, msg := m.upsertAccount(r.Context(), ident, team.ID, memberID, creds.Mobile, token, devType, sysVer); code != 0 {
		fail(w, http.StatusInternalServerError, code, msg)
		return
	}
	// 重新登录成功：补核此前因登录态过期而核查失败的日期（异步不阻塞响应）
	if memberID.Valid && m.Recompense != nil {
		go m.Recompense(logging.WithNewTraceID(context.WithoutCancel(r.Context())), memberID.Int64)
	}
	writeOK(w, map[string]any{"bound": true})
}

// upsertAccount 落库归并（人级唯一 uk_user/uk_member，token 跟人走）：
// 本人已有绑定行（含调班前旧班行）→ 更新为当前班组/成员口径；无本人行但该成员有代绑行 → 接管回本人；
// 两者并存且不同行（代绑残留）→ 删代绑行后更新本人行；设备口径缺省值覆盖。
func (m *Module) upsertAccount(ctx context.Context, ident auth.Identity, teamID int64, memberID sql.NullInt64,
	mobile, token, devType, sysVer string) (code int, message string) {
	var byUserID sql.NullInt64
	byUserFound := m.DB.QueryRowContext(ctx,
		`SELECT id FROM worklog_sgcc_account WHERE user_id = ?`, ident.ID).Scan(&byUserID) == nil
	var byMemberID, byMemberUser sql.NullInt64
	byMemberFound := false
	if memberID.Valid {
		if err := m.DB.QueryRowContext(ctx,
			`SELECT id, user_id FROM worklog_sgcc_account WHERE member_id = ?`, memberID.Int64,
		).Scan(&byMemberID, &byMemberUser); err == nil {
			byMemberFound = true
		}
	}
	if byUserFound && byMemberFound && byUserID.Int64 != byMemberID.Int64 {
		_, _ = m.DB.ExecContext(ctx, `DELETE FROM worklog_sgcc_account WHERE id = ?`, byMemberID.Int64)
	}
	switch {
	case byUserFound:
		_, err := m.DB.ExecContext(ctx,
			`UPDATE worklog_sgcc_account SET team_id = ?, member_id = ?, mobile = ?, token = ?,
			   device_type = ?, system_version = ?, token_status = 1, last_check_at = NOW() WHERE id = ?`,
			teamID, memberID, mobile, token, devType, sysVer, byUserID.Int64)
		if err != nil {
			return codeInternal, "服务器开小差了，请稍后再试"
		}
	case byMemberFound:
		_, err := m.DB.ExecContext(ctx,
			`UPDATE worklog_sgcc_account SET user_id = ?, team_id = ?, mobile = ?, token = ?,
			   device_type = ?, system_version = ?, token_status = 1, last_check_at = NOW() WHERE id = ?`,
			ident.ID, teamID, mobile, token, devType, sysVer, byMemberID.Int64)
		if err != nil {
			return codeInternal, "服务器开小差了，请稍后再试"
		}
	default:
		_, err := m.DB.ExecContext(ctx,
			`INSERT INTO worklog_sgcc_account (team_id, user_id, member_id, mobile, token, device_type, system_version, token_status, last_check_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?, 1, NOW())`,
			teamID, ident.ID, memberID, mobile, token, devType, sysVer)
		if err != nil {
			if isDupEntry(err) {
				return codeInternal, "该成员已绑定其他登录账号，请联系对方解绑或请管理员处理"
			}
			return codeInternal, "服务器开小差了，请稍后再试"
		}
	}
	return 0, ""
}

func truncateStr(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

// myAccount 本人绑定行（无则 nil）。
func (m *Module) myAccount(ctx context.Context, ident auth.Identity, team *httpapi.Team) *Account {
	scan := func(query string, args ...any) *Account {
		var a Account
		var memberID, userID sql.NullInt64
		var lastCheck sql.NullTime
		err := m.DB.QueryRowContext(ctx, query, args...).Scan(&a.ID, &a.TeamID, &userID, &memberID,
			&a.Mobile, &a.Token, &a.DeviceType, &a.SystemVersion, &a.TokenStatus, &lastCheck)
		if err != nil {
			return nil
		}
		if memberID.Valid {
			a.MemberID = memberID.Int64
		}
		if userID.Valid {
			a.UserID = userID.Int64
		}
		if lastCheck.Valid {
			t := lastCheck.Time
			a.LastCheckAt = &t
		}
		return &a
	}
	// user_id 直连优先；落空按班组人级兜底（Node myAccount 同口径）：本人成员行或
	// 同名未认领（管理员代绑 user_id=NULL）行——代绑行在「我的 → 绑定商旅」可见
	if a := scan(
		`SELECT id, team_id, user_id, member_id, mobile, token, device_type, system_version, token_status, last_check_at
		 FROM worklog_sgcc_account WHERE user_id = ?`, ident.ID); a != nil {
		return a
	}
	if team == nil {
		return nil
	}
	return scan(
		`SELECT a.id, a.team_id, a.user_id, a.member_id, a.mobile, a.token, a.device_type, a.system_version,
		        a.token_status, a.last_check_at
		 FROM worklog_sgcc_account a
		 JOIN worklog_member m ON m.id = a.member_id
		 WHERE m.team_id = ? AND (m.user_id = ? OR (m.user_id IS NULL AND m.name = ?))
		 ORDER BY (m.user_id = ?) DESC, a.id LIMIT 1`,
		team.ID, ident.ID, ident.Nickname, ident.ID)
}

// accountGet GET /account：本人绑定状态 + 今日打卡/费用摘要。
func (m *Module) accountGet(w http.ResponseWriter, r *http.Request) {
	ident, team, ok := m.enter(w, r)
	if !ok {
		return
	}
	if team == nil {
		fail(w, http.StatusForbidden, codeNoAssign, "未分配班组，请联系管理员分配")
		return
	}
	acc := m.myAccount(r.Context(), ident, team)
	if acc == nil {
		writeOK(w, map[string]any{"bound": false})
		return
	}
	today := TodayCN()
	masked := maskMobile(acc.Mobile)
	out := map[string]any{
		"bound": true,
		// 网页端（snake）与小程序（camel）双端键名并存（Node /account camel 口径）
		"mobile_masked":  masked,
		"mobile":         masked,
		"device_type":    acc.DeviceType,
		"deviceType":     acc.DeviceType,
		"system_version": acc.SystemVersion,
		"systemVersion":  acc.SystemVersion,
		"token_status":   acc.TokenStatus,
		"tokenStatus":    acc.TokenStatus,
		"lastCheckAt":    acc.LastCheckAt,
		"member_id":      acc.MemberID,
		"clockin":        m.clockinSummary(r.Context(), acc.MemberID, today),
		"clockins":       m.clockinRows(r.Context(), acc.MemberID, today),
		"fee":            m.feeRow(r.Context(), acc.MemberID, today),
	}
	writeOK(w, out)
}

// clockinRows 当日打卡行（Node /account clockins 口径：[{seq, clock_time, position, work_hours}]）。
func (m *Module) clockinRows(ctx context.Context, memberID int64, date string) []map[string]any {
	out := []map[string]any{}
	if memberID == 0 {
		return out
	}
	rows, err := m.DB.QueryContext(ctx,
		`SELECT seq, DATE_FORMAT(clock_time, '%Y-%m-%d %H:%i:%s'), position, work_hours
		 FROM worklog_clockin WHERE member_id = ? AND clock_date = ? ORDER BY seq`, memberID, date)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var seq int
		var clockTime, position, workHours sql.NullString
		if err := rows.Scan(&seq, &clockTime, &position, &workHours); err == nil {
			out = append(out, map[string]any{
				"seq": seq, "clock_time": clockTime.String,
				"position": position.String, "work_hours": workHours.String,
			})
		}
	}
	return out
}

// feeRow 当日费用行（Node /account fee 口径：{food_fee, transit_fee, cost_center_code, cost_center_name} 或 null）。
func (m *Module) feeRow(ctx context.Context, memberID int64, date string) any {
	if memberID == 0 {
		return nil
	}
	var food, transit float64
	var ccCode, ccName sql.NullString
	err := m.DB.QueryRowContext(ctx,
		`SELECT food_fee, transit_fee, cost_center_code, cost_center_name FROM worklog_fee
		 WHERE member_id = ? AND fee_date = ?`, memberID, date).Scan(&food, &transit, &ccCode, &ccName)
	if err != nil {
		return nil
	}
	return map[string]any{
		"food_fee": food, "transit_fee": transit,
		"cost_center_code": ccCode.String, "cost_center_name": ccName.String,
	}
}

// maskMobile 手机号脱敏：前三后四。
func maskMobile(mobile string) string {
	if len(mobile) < 7 {
		return mobile
	}
	return mobile[:3] + "****" + mobile[len(mobile)-4:]
}

// accountDevice PUT /account/device：本人改设备口径。
func (m *Module) accountDevice(w http.ResponseWriter, r *http.Request) {
	ident, team, ok := m.enter(w, r)
	if !ok {
		return
	}
	body, err := decodeBody(r)
	if err != nil {
		fail(w, http.StatusBadRequest, codeParam, "参数错误")
		return
	}
	devType := truncateStr(rawString(body["deviceType"]), 64)
	sysVer := truncateStr(rawString(body["systemVersion"]), 64)
	if devType == "" {
		fail(w, http.StatusBadRequest, 40033, "设备型号不能为空")
		return
	}
	acc := m.myAccount(r.Context(), ident, team)
	if acc == nil {
		fail(w, http.StatusBadRequest, 40020, "请先绑定商旅账号")
		return
	}
	if _, err := m.DB.ExecContext(r.Context(),
		`UPDATE worklog_sgcc_account SET device_type = ?, system_version = ? WHERE id = ?`,
		devType, sysVer, acc.ID); err != nil {
		writeErr(w, err)
		return
	}
	writeOK(w, nil)
}

// accountDelete DELETE /account：解绑（本人；仅删本地绑定与已同步的打卡/费用数据，商旅 App 侧不受影响）。
func (m *Module) accountDelete(w http.ResponseWriter, r *http.Request) {
	ident, team, ok := m.enter(w, r)
	if !ok {
		return
	}
	acc := m.myAccount(r.Context(), ident, team)
	if acc == nil {
		writeOK(w, nil) // Node 口径：本就未绑定按成功返回
		return
	}
	if _, err := m.DB.ExecContext(r.Context(),
		`DELETE FROM worklog_sgcc_account WHERE id = ?`, acc.ID); err != nil {
		writeErr(w, err)
		return
	}
	// 级联清理该成员已同步的打卡/费用数据（Node /account DELETE 同口径）
	if acc.MemberID != 0 {
		_, _ = m.DB.ExecContext(r.Context(),
			`DELETE FROM worklog_clockin WHERE member_id = ?`, acc.MemberID)
		_, _ = m.DB.ExecContext(r.Context(),
			`DELETE FROM worklog_fee WHERE member_id = ?`, acc.MemberID)
	}
	writeOK(w, nil)
}

// expiredList GET /expired：本班登录已过期的绑定成员名单（弹窗提醒数据源）。
func (m *Module) expiredList(w http.ResponseWriter, r *http.Request) {
	_, team, ok := m.enter(w, r)
	if !ok {
		return
	}
	if team == nil {
		writeOK(w, map[string]any{"list": []any{}})
		return
	}
	rows, err := m.DB.QueryContext(r.Context(),
		`SELECT m.name FROM worklog_sgcc_account a
		 JOIN worklog_member m ON m.id = a.member_id
		 WHERE a.team_id = ? AND a.token_status = 0 ORDER BY m.sort, m.id`, team.ID)
	if err != nil {
		writeErr(w, err)
		return
	}
	defer rows.Close()
	list := make([]string, 0)
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err == nil {
			list = append(list, name)
		}
	}
	writeOK(w, map[string]any{"list": list})
}

// accountsList GET /accounts：本班组全部绑定账号（管理端）。
func (m *Module) accountsList(w http.ResponseWriter, r *http.Request) {
	ident, team, ok := m.enter(w, r)
	if !ok {
		return
	}
	if !requireDictAdmin(w, ident, team) {
		return
	}
	if team == nil {
		writeOK(w, map[string]any{"list": []any{}})
		return
	}
	rows, err := m.DB.QueryContext(r.Context(),
		`SELECT a.id, COALESCE(m.name, u.nickname, a.mobile), m.name, a.mobile, a.device_type, a.system_version,
		        a.token_status, a.last_check_at
		 FROM worklog_sgcc_account a
		 LEFT JOIN worklog_member m ON m.id = a.member_id
		 LEFT JOIN sys_user u ON u.id = a.user_id
		 WHERE a.team_id = ? ORDER BY m.sort IS NULL, m.sort, a.id`, team.ID)
	if err != nil {
		writeErr(w, err)
		return
	}
	defer rows.Close()
	list := make([]map[string]any, 0)
	for rows.Next() {
		var id int64
		var name, memberName sql.NullString
		var mobile, devType, sysVer string
		var tokenStatus int
		var lastCheck sql.NullTime
		if err := rows.Scan(&id, &name, &memberName, &mobile, &devType, &sysVer,
			&tokenStatus, &lastCheck); err == nil {
			it := map[string]any{
				"id": id,
				// 网页端（snake/name）与小程序（camel/memberName）双端键名并存（Node /accounts camel 口径）；
				// memberName 为空 = 绑定账号昵称未匹配到出工成员（Node 同口径）
				"name":           name.String,
				"memberName":     memberName.String,
				"mobile_masked":  maskMobile(mobile),
				"mobile":         maskMobile(mobile),
				"device_type":    devType,
				"deviceType":     devType,
				"system_version": sysVer,
				"systemVersion":  sysVer,
				"token_status":   tokenStatus,
				"tokenStatus":    tokenStatus,
				"last_check_at":  nil,
				"lastCheckAt":    "",
			}
			if lastCheck.Valid {
				t := lastCheck.Time
				it["last_check_at"] = &t
				it["lastCheckAt"] = t.Format("2006-01-02 15:04")
			}
			list = append(list, it)
		}
	}
	writeOK(w, map[string]any{"list": list})
}

// accountAdminDevice PUT /accounts/{id}/device：管理端改设备口径。
func (m *Module) accountAdminDevice(w http.ResponseWriter, r *http.Request) {
	ident, team, ok := m.enter(w, r)
	if !ok {
		return
	}
	if !requireDictAdmin(w, ident, team) {
		return
	}
	if team == nil {
		fail(w, http.StatusBadRequest, codeNoTeam, "无可用班组")
		return
	}
	id, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	body, err := decodeBody(r)
	if err != nil {
		fail(w, http.StatusBadRequest, codeParam, "参数错误")
		return
	}
	devType := truncateStr(rawString(body["deviceType"]), 64)
	sysVer := truncateStr(rawString(body["systemVersion"]), 64)
	res, err := m.DB.ExecContext(r.Context(),
		`UPDATE worklog_sgcc_account SET device_type = ?, system_version = ? WHERE id = ? AND team_id = ?`,
		devType, sysVer, id, team.ID)
	if err != nil {
		writeErr(w, err)
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		fail(w, http.StatusNotFound, codeMissing, "绑定记录不存在")
		return
	}
	writeOK(w, nil)
}

// accountsBind POST /accounts/bind：管理员代绑（服务端代登录换 token 后写 user_id=NULL 的代绑行）。
// 入参 memberId（兼容 member_id）与登录凭据 mobile/checkCode/password/captchaToken/constId/checkImgCode；
// 前端不再提供 token（与 loginBind 复用同一登录校验与换 token 逻辑，认领成员规则不同：按显式 memberId）。
// 成员已自绑时只换 token、行仍归本人（设备口径保持原值），与 Node 代绑兜底口径一致。
func (m *Module) accountsBind(w http.ResponseWriter, r *http.Request) {
	ident, team, ok := m.enter(w, r)
	if !ok {
		return
	}
	if !requireDictAdmin(w, ident, team) {
		return
	}
	if team == nil {
		fail(w, http.StatusBadRequest, codeNoTeam, "无可用班组")
		return
	}
	body, err := decodeBody(r)
	if err != nil {
		fail(w, http.StatusBadRequest, codeParam, "参数错误")
		return
	}
	memberID := rawNum(body["memberId"])
	if memberID <= 0 {
		memberID = rawNum(body["member_id"])
	}
	if memberID <= 0 {
		fail(w, http.StatusBadRequest, codeParam, "参数错误：请指定代绑成员")
		return
	}
	creds := parseBindCreds(body)
	if !m.checkBindCreds(w, ident, creds) {
		return
	}
	// 成员归属校验：代绑只能落在生效班组内（他班 memberId 一律 403，避免借代绑写他班数据）。
	var memberTeam sql.NullInt64
	err = m.DB.QueryRowContext(r.Context(), `SELECT team_id FROM worklog_member WHERE id = ?`, memberID).Scan(&memberTeam)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && (!memberTeam.Valid || memberTeam.Int64 != team.ID)) {
		fail(w, http.StatusForbidden, codeForbidden, "成员不属于本班组")
		return
	}
	if err != nil {
		writeErr(w, err)
		return
	}
	// 归属校验：该成员已被其他账号自绑时不再拒绝——代绑只换 token，行仍归本人（Node 同口径，
	// 成员过期又联系不上时由管理员兜底换 token；设备口径保持原值，见下方 ON DUPLICATE）。
	// 先取既有归属行用于审计结果口径（user_id 非空 = 已自绑，本次为换 token）。
	var boundOwner sql.NullInt64
	selfBound := m.DB.QueryRowContext(r.Context(),
		`SELECT user_id FROM worklog_sgcc_account WHERE member_id = ?`, memberID).Scan(&boundOwner) == nil && boundOwner.Valid
	// 成员归属校验通过后再代登录换 token，避免为他班成员消耗验证码
	token, ok := m.bindToken(w, r, creds)
	if !ok {
		m.auditAdminBind(r.Context(), ident, memberID, "login_failed")
		return
	}
	devType, sysVer := bindDevice(creds)
	_, err = m.DB.ExecContext(r.Context(),
		`INSERT INTO worklog_sgcc_account (team_id, user_id, member_id, mobile, token, device_type, system_version, token_status, last_check_at)
		 VALUES (?, NULL, ?, ?, ?, ?, ?, 1, NOW())
		 ON DUPLICATE KEY UPDATE team_id = VALUES(team_id), member_id = VALUES(member_id), mobile = VALUES(mobile),
		   token = VALUES(token), token_status = 1, last_check_at = NOW()`,
		team.ID, memberID, creds.Mobile, token, devType, sysVer)
	if err != nil {
		m.auditAdminBind(r.Context(), ident, memberID, "failed")
		writeErr(w, err)
		return
	}
	result := "bound"
	if selfBound {
		result = "token_refreshed" // 已自绑成员：仅换 token，绑定行仍归本人
	}
	m.auditAdminBind(r.Context(), ident, memberID, result)
	if m.Recompense != nil {
		go m.Recompense(logging.WithNewTraceID(context.WithoutCancel(r.Context())), memberID)
	}
	writeOK(w, map[string]any{"bound": true})
}

// auditAdminBind 管理员代绑审计（category=audit，写操作审计）：只记操作人 / 目标成员 / 结果，
// 不落 token、密码、验证码；失败结果记 warn，成功记 info。日志失败不影响业务。
func (m *Module) auditAdminBind(ctx context.Context, ident auth.Identity, memberID int64, result string) {
	l := m.Log
	if l == nil {
		if m.DB == nil {
			return
		}
		l = logging.New(m.DB, appLogCode)
	}
	fields := map[string]any{
		"operator_id":   ident.ID,
		"operator_name": ident.Username,
		"member_id":     memberID,
		"result":        result,
	}
	if result == "login_failed" || result == "failed" {
		_ = l.Warn(ctx, "audit", "sgcc.bind.admin", "管理员代绑商旅账号", "", fields)
		return
	}
	_ = l.Info(ctx, "audit", "sgcc.bind.admin", "管理员代绑商旅账号", "", fields)
}

// rawNum 取数字（JS Number 同口径）。
func rawNum(raw json.RawMessage) int64 {
	return int64(num(rawString(raw)))
}

// rawStringMap 取字符串 map（overrides 透传口径：{组件id: value字符串}）；空/坏 JSON 回空表。
func rawStringMap(raw json.RawMessage) map[string]string {
	out := map[string]string{}
	if len(raw) == 0 {
		return out
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil || m == nil {
		return out
	}
	for k, v := range m {
		out[k] = rawString(v)
	}
	return out
}
