// Package sgccclockin 迁移 server/src/sgccclockin：商旅打卡——账号绑定（图形码/滑块双通道）、
// 同步/过期探测、照片操作任务、每日核查与午间/傍晚提醒、出口代理探测。
// 协议加解密复用 compat/cryptoaudio（jsonm/jsonx 双通道），外部调用可注入，测试不实连。
package sgccclockin

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"shade/internal/auth"
	"shade/internal/httpapi"
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
	CardMemberID  int64 // 联表解析的卡片成员 id（loadEntries 同口径，可空）
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
	// Cfg 运行时配置（密钥/代理由 Protocol.Cfg 热生效；此处读排程时刻与防风控间隔）；nil 用缺省值。
	Cfg ConfigStore
	// Recompense 绑定成功后的补核钩子（member 口径；nil 不补核）。
	Recompense func(ctx context.Context, memberID int64)
	// FetchPhoto 照片原文回源函数（COS 公网地址 GET；nil 用默认 http 客户端 60s 超时）；测试注入 fake/httptest。
	FetchPhoto PhotoFetcher
	// GeoHTTP 高德定位客户端；nil 使用 8 秒超时默认客户端。
	GeoHTTP *http.Client
	// IntervalMs 日期间防风控间隔缺省值（Cfg 的 SGCC_SYNC_INTERVAL_MS 优先；默认 500ms；测试置 0）。
	IntervalMs int
}

// New 构造商旅打卡模块。
func New(db *sql.DB) *Module {
	return &Module{
		DB:      db,
		Ops:     NewPhotoOpManager(),
		Limiter: NewLoginRateLimiter(),
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
	team, err := httpapi.ResolveTeam(r.Context(), m.DB, adapter, 0)
	if err != nil {
		return ident, nil, true // 无可用班组 → 空态（各路由自行判空）
	}
	return ident, &team, true
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
			msg := msgOr(d, "短信发送失败，请重试")
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
	mobile := rawString(body["mobile"])
	checkCode := rawString(body["checkCode"])
	password := rawString(body["password"])
	checkImgCode := rawString(body["checkImgCode"])
	captchaToken := rawString(body["captchaToken"])
	constID := rawString(body["constId"])
	deviceType := truncateStr(rawString(body["deviceType"]), 64)
	systemVersion := truncateStr(rawString(body["systemVersion"]), 64)
	if !validMobile(mobile) {
		fail(w, http.StatusBadRequest, codeBadMobile, "手机号格式不正确")
		return
	}
	if checkCode == "" && password == "" {
		fail(w, http.StatusBadRequest, codeNeedCred, "请填写短信验证码或密码")
		return
	}
	if password != "" && checkImgCode == "" && captchaToken == "" {
		fail(w, http.StatusBadRequest, codeNeedRisk, "密码登录请先完成图形或滑块验证")
		return
	}
	if m.overLoginRate("bind", ident.ID, mobile) {
		fail(w, http.StatusTooManyRequests, codeRateLimit, "操作过于频繁，请稍后再试")
		return
	}
	if m.API == nil {
		writeErr(w, httpapi.DependencyUnavailable("商旅协议服务未接入"))
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
	var token string
	var callErr error
	switch {
	case password != "":
		if checkImgCode != "" {
			token, callErr = m.API.LoginByPasswordV3(r.Context(), mobile, password, checkImgCode, CallOpts{})
		} else {
			token, callErr = m.API.LoginByPassword(r.Context(), mobile, password, captchaToken, constID, CallOpts{})
		}
	default:
		token, callErr = m.API.LoginBySmsSlapp(r.Context(), mobile, checkCode, captchaToken, constID, CallOpts{})
	}
	if callErr != nil {
		var bizErr *LoginBizError
		if errors.As(callErr, &bizErr) {
			fail(w, http.StatusBadRequest, codePlatform, bizErr.Error())
			return
		}
		writeErr(w, callErr)
		return
	}

	devType := deviceType
	if devType == "" {
		devType = "Xiaomi 2509FPN0BC"
	}
	sysVer := systemVersion
	if sysVer == "" {
		sysVer = "Android 16"
	}
	if code, msg := m.upsertAccount(r.Context(), ident, team.ID, memberID, mobile, token, devType, sysVer); code != 0 {
		fail(w, http.StatusInternalServerError, code, msg)
		return
	}
	// 重新登录成功：补核此前因登录态过期而核查失败的日期（异步不阻塞响应）
	if memberID.Valid && m.Recompense != nil {
		go m.Recompense(context.Background(), memberID.Int64)
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
func (m *Module) myAccount(ctx context.Context, userID int64) *Account {
	var a Account
	var memberID sql.NullInt64
	err := m.DB.QueryRowContext(ctx,
		`SELECT id, team_id, user_id, member_id, mobile, token, device_type, system_version, token_status
		 FROM worklog_sgcc_account WHERE user_id = ?`, userID,
	).Scan(&a.ID, &a.TeamID, &a.UserID, &memberID, &a.Mobile, &a.Token, &a.DeviceType, &a.SystemVersion, &a.TokenStatus)
	if err != nil {
		return nil
	}
	if memberID.Valid {
		a.MemberID = memberID.Int64
	}
	return &a
}

// accountGet GET /account：本人绑定状态 + 今日打卡/费用摘要。
func (m *Module) accountGet(w http.ResponseWriter, r *http.Request) {
	ident, team, ok := m.enter(w, r)
	if !ok {
		return
	}
	acc := m.myAccount(r.Context(), ident.ID)
	if acc == nil {
		writeOK(w, map[string]any{"bound": false})
		return
	}
	today := TodayCN()
	out := map[string]any{
		"bound":          true,
		"mobile_masked":  maskMobile(acc.Mobile),
		"device_type":    acc.DeviceType,
		"system_version": acc.SystemVersion,
		"token_status":   acc.TokenStatus,
		"member_id":      acc.MemberID,
		"clockin":        m.clockinSummary(r.Context(), acc.MemberID, today),
		"fee":            m.feeSummary(r.Context(), acc.MemberID, today),
	}
	_ = team
	writeOK(w, out)
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
	ident, _, ok := m.enter(w, r)
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
	res, err := m.DB.ExecContext(r.Context(),
		`UPDATE worklog_sgcc_account SET device_type = ?, system_version = ? WHERE user_id = ?`,
		devType, sysVer, ident.ID)
	if err != nil {
		writeErr(w, err)
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		fail(w, http.StatusNotFound, codeMissing, "未绑定商旅账号")
		return
	}
	writeOK(w, nil)
}

// accountDelete DELETE /account：解绑（本人）。
func (m *Module) accountDelete(w http.ResponseWriter, r *http.Request) {
	ident, _, ok := m.enter(w, r)
	if !ok {
		return
	}
	res, err := m.DB.ExecContext(r.Context(),
		`DELETE FROM worklog_sgcc_account WHERE user_id = ?`, ident.ID)
	if err != nil {
		writeErr(w, err)
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		fail(w, http.StatusNotFound, codeMissing, "未绑定商旅账号")
		return
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
		`SELECT a.id, COALESCE(m.name, u.nickname, a.mobile), a.mobile, a.device_type, a.system_version,
		        a.token_status, a.last_check_at
		 FROM worklog_sgcc_account a
		 LEFT JOIN worklog_member m ON m.id = a.member_id
		 LEFT JOIN sys_user u ON u.id = a.user_id
		 WHERE a.team_id = ? ORDER BY a.id`, team.ID)
	if err != nil {
		writeErr(w, err)
		return
	}
	defer rows.Close()
	type item struct {
		ID            int64      `json:"id"`
		Name          string     `json:"name"`
		MobileMasked  string     `json:"mobile_masked"`
		DeviceType    string     `json:"device_type"`
		SystemVersion string     `json:"system_version"`
		TokenStatus   int        `json:"token_status"`
		LastCheckAt   *time.Time `json:"last_check_at"`
	}
	list := make([]item, 0)
	for rows.Next() {
		var it item
		var mobile string
		var lastCheck sql.NullTime
		if err := rows.Scan(&it.ID, &it.Name, &mobile, &it.DeviceType, &it.SystemVersion,
			&it.TokenStatus, &lastCheck); err == nil {
			it.MobileMasked = maskMobile(mobile)
			if lastCheck.Valid {
				t := lastCheck.Time
				it.LastCheckAt = &t
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

// accountsBind POST /accounts/bind：管理员代绑（写 user_id=NULL 的代绑行）。
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
	memberID := rawNum(body["member_id"])
	mobile := rawString(body["mobile"])
	token := rawString(body["token"])
	devType := truncateStr(rawString(body["deviceType"]), 64)
	sysVer := truncateStr(rawString(body["systemVersion"]), 64)
	if memberID <= 0 || !validMobile(mobile) || token == "" {
		fail(w, http.StatusBadRequest, codeParam, "参数错误")
		return
	}
	if devType == "" {
		devType = "Xiaomi 2509FPN0BC"
	}
	if sysVer == "" {
		sysVer = "Android 16"
	}
	// 归属校验：该成员已被其他账号自绑时拒绝
	var otherUser sql.NullInt64
	if err := m.DB.QueryRowContext(r.Context(),
		`SELECT user_id FROM worklog_sgcc_account WHERE member_id = ?`, memberID).Scan(&otherUser); err == nil {
		if otherUser.Valid {
			fail(w, http.StatusConflict, codeBoundOther, "该成员已绑定其他登录账号，请联系对方解绑或请管理员处理")
			return
		}
	}
	_, err = m.DB.ExecContext(r.Context(),
		`INSERT INTO worklog_sgcc_account (team_id, user_id, member_id, mobile, token, device_type, system_version, token_status, last_check_at)
		 VALUES (?, NULL, ?, ?, ?, ?, ?, 1, NOW())
		 ON DUPLICATE KEY UPDATE team_id = VALUES(team_id), mobile = VALUES(mobile), token = VALUES(token),
		   device_type = VALUES(device_type), system_version = VALUES(system_version), token_status = 1, last_check_at = NOW()`,
		team.ID, memberID, mobile, token, devType, sysVer)
	if err != nil {
		writeErr(w, err)
		return
	}
	if m.Recompense != nil {
		go m.Recompense(context.Background(), memberID)
	}
	writeOK(w, map[string]any{"bound": true})
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
