// 统一 HTTP 请求访问日志中间件：除 healthz 外记录 /api/v1 请求的方法、路径、状态、耗时与
// 登录用户班组（app_log.category = access）。主程序一行挂载即可：
//
//	handler := httpapi.WithAccessLog(httpapi.WithSPA(httpapi.NewRouter(deps, modules...), webDir), db)
//
// 设计约束：
//   - 按路由映射应用 code：系统公共接口记 system，其余按应用（sys_app 口径键名，
//     见 routeAppCode；office / chatlog 无 sys_app 登记，用自定义 code office / chat-log）；
//   - 级别：401 未授权记 info（未登录/登录过期属正常访问噪声），其余 4xx 记 warn，5xx 记 error；
//   - 中文说明（message_cn）与技术原文（message/raw_message）分离，trace_id 全程贯穿
//     （中间件生成或沿用入站 X-Trace-Id，注入请求 context 并回写响应头，同链路业务日志自动带出）；
//   - query 里的 token/密钥类参数脱敏为 **** 后才进 detail，Authorization 头绝不落库；
//   - 落库在响应写出后进行，失败/异常只吞不抛（panic 仍向上抛给 http.Server），绝不影响业务响应。
//
// 登录用户归属：AuthMiddleware 在内层为下游注入 identity，最外层中间件读不到其 context，
// 故先读 IdentityFromContext（内层挂载时直取），否则按 AuthMiddleware 同口径取
// Bearer / 下载路径 ?token=，用 auth.Service 验签并回查 sys_user 取班组（黑名单不校验，
// 仅作日志归属；查不到即匿名）。未配置 JWT_SECRET 时归属降级为匿名。
package httpapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"shade/internal/auth"
	"shade/internal/config"
	"shade/internal/logging"
)

// accessTraceHeader 链路 ID 请求/响应头（入站合法则沿用，出站恒回写）。
const accessTraceHeader = "X-Trace-Id"

// accessWriteTimeout 单条访问日志落库超时（响应已写出，慢库只拖日志不拖业务）。
const accessWriteTimeout = 3 * time.Second

// accessAppBySegment 路由首段 → 应用 code（sys_app 口径；系统公共记 system）。
// office / chatlog 无 sys_app 登记（chatlog 为纯对接端点），用日志口径自定义 code。
var accessAppBySegment = map[string]string{
	"auth": "system", "app": "system", "user": "system", "admin": "system",
	"notice": "system", "app-logs": "system",
	"worklog":       "work-log",
	"teamdata":      "team-data",
	"treehazard":    "tree-hazard",
	"crossing":      "crossing",
	"netdisk":       "netdisk",
	"quiz":          "quiz",
	"callme":        "call-me",
	"safeday":       "safe-day",
	"kvm":           "kvm",
	"wmadd":         "wm-add",
	"sgcc":          "sgcc-clockin",
	"wechatanswer":  "wechat-answer",
	"wechat-answer": "wechat-answer",
	"office":        "office",
	"chatlog":       "chat-log",
}

// routeAppCode 按路由映射应用 code：系统公共 system，其他全部按应用；未知路由回落 system。
func routeAppCode(path string) string {
	seg := strings.TrimPrefix(path, "/api/v1")
	seg = strings.TrimPrefix(seg, "/")
	if i := strings.IndexByte(seg, '/'); i >= 0 {
		seg = seg[:i]
	}
	if code, ok := accessAppBySegment[seg]; ok {
		return code
	}
	return "system"
}

// accessLevel 访问日志级别：401 未授权 info、其他 4xx warn、5xx error、其余 info。
func accessLevel(status int) string {
	switch {
	case status >= 500:
		return "error"
	case status == http.StatusUnauthorized:
		return "info"
	case status >= 400:
		return "warn"
	default:
		return "info"
	}
}

// accessPhrase 中文状态短语（拼进 message_cn）。
func accessPhrase(status int) string {
	switch {
	case status >= 500:
		return "访问服务端错误"
	case status == http.StatusUnauthorized:
		return "访问未授权"
	case status >= 400:
		return "访问客户端错误"
	default:
		return "访问成功"
	}
}

// accessLogPath 是否记录：/healthz 除外的 /api/v1 请求。
func accessLogPath(path string) bool {
	if path == "/healthz" || path == "/api/v1/healthz" {
		return false
	}
	return path == "/api/v1" || strings.HasPrefix(path, "/api/v1/")
}

// sensitiveParamName 敏感参数名判定（query 脱敏口径）：token/secret/password/credential
// 族子串，或以 key 结尾（key、api_key、map_key 等；keyword 不以 key 结尾不受影响）。
func sensitiveParamName(name string) bool {
	lower := strings.ToLower(name)
	switch lower {
	case "auth", "authorization", "sign", "sig", "session", "sessionid", "sid", "jwt", "pwd":
		return true
	}
	for _, frag := range []string{"token", "secret", "password", "passwd", "signature", "credential"} {
		if strings.Contains(lower, frag) {
			return true
		}
	}
	return strings.HasSuffix(lower, "key")
}

// redactQuery 查询参数脱敏：敏感参数值一律 ****（绝不落库），其余取原值。
func redactQuery(q url.Values) map[string]string {
	if len(q) == 0 {
		return nil
	}
	out := make(map[string]string, len(q))
	for name, values := range q {
		if sensitiveParamName(name) {
			out[name] = "****"
			continue
		}
		out[name] = strings.Join(values, ",")
	}
	return out
}

// statusRecorder 包装响应写入器捕获状态码；Unwrap 供 http.ResponseController 透传
// Flush/Hijack 等扩展接口。
type statusRecorder struct {
	http.ResponseWriter
	status int
	wrote  bool
}

func (w *statusRecorder) WriteHeader(status int) {
	if !w.wrote {
		w.status, w.wrote = status, true
	}
	w.ResponseWriter.WriteHeader(status)
}

func (w *statusRecorder) Write(p []byte) (int, error) {
	if !w.wrote {
		w.status, w.wrote = http.StatusOK, true
	}
	return w.ResponseWriter.Write(p)
}

func (w *statusRecorder) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// statusCode 实际响应状态（未显式写出按 200）。
func (w *statusRecorder) statusCode() int {
	if !w.wrote {
		return http.StatusOK
	}
	return w.status
}

// WithAccessLog 构造统一 HTTP 请求访问日志中间件（db 为空原样透传，不落日志）。
func WithAccessLog(next http.Handler, db *sql.DB) http.Handler {
	if next == nil {
		return nil
	}
	if db == nil {
		return next
	}
	// 验签取登录用户（与 AuthMiddleware 同一 auth.Service 口径；黑名单不接入，仅作日志归属）。
	authn := auth.NewService(auth.SQLUserStore{DB: db}, nil, os.Getenv("JWT_SECRET"), 0, 0)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !accessLogPath(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}
		start := time.Now()
		// trace_id 贯穿：沿用合法入站 X-Trace-Id，否则新生成；注入请求 context 供业务日志带出。
		ctx := logging.WithTraceID(r.Context(), r.Header.Get(accessTraceHeader))
		traceID := logging.TraceIDFromContext(ctx)
		if traceID == "" {
			traceID = logging.NewTraceID()
			ctx = logging.WithTraceID(ctx, traceID)
		}
		w.Header().Set(accessTraceHeader, traceID)
		rec := &statusRecorder{ResponseWriter: w}
		var pan any
		func() {
			defer func() { pan = recover() }()
			next.ServeHTTP(rec, r.WithContext(ctx))
		}()
		status := rec.statusCode()
		if pan != nil && !rec.wrote {
			status = http.StatusInternalServerError
		}
		// 归属与落库在响应后进行：请求 ctx 可能已取消，换不可取消 ctx 并限时。
		logCtx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), accessWriteTimeout)
		defer cancel()
		ident := accessIdentity(logCtx, r, authn)
		writeAccessLog(logCtx, db, accessEntry(r, status, time.Since(start), ident, traceID))
		if pan != nil {
			panic(pan) // 记录后照常向上抛，不吞 panic
		}
	})
}

// accessIdentity 登录用户归属：内层挂载直取 context；否则按 AuthMiddleware 同口径取
// token 验签回查（失败匿名）。
func accessIdentity(ctx context.Context, r *http.Request, authn Authenticator) auth.Identity {
	if ident, ok := IdentityFromContext(r.Context()); ok {
		return ident
	}
	if authn == nil {
		return auth.Identity{}
	}
	token := bearerToken(r)
	if token == "" && (r.Method == http.MethodGet || r.Method == http.MethodHead) && fileDownloadPath(r.URL.Path) {
		token = strings.TrimSpace(r.URL.Query().Get("token"))
	}
	if token == "" {
		return auth.Identity{}
	}
	ident, err := authn.Authenticate(ctx, token)
	if err != nil {
		return auth.Identity{}
	}
	return ident
}

// accessEntry 组装访问日志条目：中文说明与技术原文分离，query 脱敏后进 detail。
func accessEntry(r *http.Request, status int, dur time.Duration, ident auth.Identity, traceID string) config.AppLogEntry {
	durMs := float64(dur) / float64(time.Millisecond)
	detail := map[string]any{
		"method":      r.Method,
		"path":        r.URL.Path,
		"status":      status,
		"duration_ms": math.Round(durMs*10) / 10,
	}
	if q := redactQuery(r.URL.Query()); len(q) > 0 {
		detail["query"] = q
	}
	if ident.ID != 0 {
		detail["user_id"] = ident.ID
		detail["username"] = ident.Username
		detail["team_id"] = ident.TeamID
	}
	var detailJSON string
	if data, err := json.Marshal(detail); err == nil {
		detailJSON = string(data)
	}
	raw := fmt.Sprintf("%s %s %d %.1fms uid=%d team=%d", r.Method, r.URL.Path, status, durMs, ident.ID, ident.TeamID)
	messageCN := fmt.Sprintf("%s %s %s（%d，耗时 %.0fms）", r.Method, r.URL.Path, accessPhrase(status), status, math.Round(durMs))
	return config.AppLogEntry{
		AppCode:    routeAppCode(r.URL.Path),
		Level:      accessLevel(status),
		Category:   "access",
		Event:      "http.request",
		Message:    raw,
		MessageCN:  messageCN,
		RawMessage: raw,
		TraceID:    traceID,
		UserID:     ident.ID,
		TeamID:     ident.TeamID,
		Detail:     detailJSON,
	}
}

// writeAccessLog 落库：失败与异常一律吞掉，绝不影响业务响应。
func writeAccessLog(ctx context.Context, db *sql.DB, e config.AppLogEntry) {
	defer func() { _ = recover() }()
	_ = logging.WriteEntry(ctx, db, e)
}
