// 统一 HTTP 请求访问日志中间件：除 healthz 外记录 /api/v1 请求的方法、路径、状态、耗时与
// 登录用户班组（app_log.category = access）。主程序一行挂载即可：
//
//	handler := httpapi.WithAccessLog(httpapi.WithSPA(httpapi.NewRouter(deps, modules...), webDir), db)
//
// 生产接线注入共享文件持久层与同一 auth.Service（访问日志与各应用日志共用同一 DurableSink）：
//
//	handler := httpapi.WithAccessLogOptions(next, httpapi.AccessLogOptions{Sink: queue, Auth: authSvc, DB: db})
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
// 登录用户归属：最外层挂载时把共享身份载体放入请求 ctx，内层 AuthMiddleware 验签后在
// admin gate 前回填（401 回填匿名），响应后直接复用——同一请求只验签一次，403 不重复查
// sys_user、401 不再查用户。内层挂载时直接读 IdentityFromContext；两者都没有时按
// AuthMiddleware 同口径取 Bearer / 下载路径 ?token=，用 auth.Service 验签并回查 sys_user
// 取班组（生产注入的 Auth 与 AuthMiddleware 同一 auth.Service、含 Redis 黑名单；仅按 DB
// 兜底构造时黑名单为 nil、仅作日志归属；查不到即匿名）。未配置 JWT_SECRET 时归属降级为匿名。
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
	"shade/internal/logsafe"
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

// sensitiveParamName 敏感参数名判定（与 logsafe 查询参数同口径）。
func sensitiveParamName(name string) bool { return logsafe.SensitiveKey(name) }

// redactQuery 查询参数脱敏：敏感参数值一律 ****（绝不落库），其余取原值。
func redactQuery(q url.Values) map[string]string { return logsafe.QueryForLog(q) }

// mutationMethod 是否写操作方法（2xx 时额外记 audit；4xx/5xx 由 access 的 warn/error 覆盖）。
func mutationMethod(method string) bool {
	switch method {
	case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
		return true
	}
	return false
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

// AccessLogOptions 访问日志中间件可注入项：Sink 优先于 DB（生产注入共享 DurableSink），
// Auth 优先于按 DB 构造（便于测试与复用同一 auth.Service）。Sink 与 DB 都为空原样透传。
type AccessLogOptions struct {
	DB     *sql.DB
	Sink   logging.Sink
	Auth   Authenticator
	Events EventRecorder // 中间件自身 panic 的 runtime 事件（nil 不记）
}

// WithAccessLog 构造统一 HTTP 请求访问日志中间件（旧签名，db 为空原样透传，不落日志）。
func WithAccessLog(next http.Handler, db *sql.DB) http.Handler {
	return WithAccessLogOptions(next, AccessLogOptions{DB: db})
}

// WithAccessLogOptions 构造统一 HTTP 请求访问日志中间件（可注入 Sink/Auth）。
func WithAccessLogOptions(next http.Handler, opts AccessLogOptions) http.Handler {
	if next == nil {
		return nil
	}
	sink := opts.Sink
	if sink == nil {
		if opts.DB == nil {
			return next
		}
		// 未注入持久层时同步直写（兜底；生产统一注入共享 DurableSink）。
		sink = logging.DirectSink{DB: opts.DB}
	}
	authn := opts.Auth
	if authn == nil && opts.DB != nil {
		// 验签取登录用户（与 AuthMiddleware 同一 auth.Service 口径；生产注入的 Auth 含 Redis 黑名单，
		// 此处按 DB 兜底构造时黑名单为 nil，仅作日志归属）。
		authn = auth.NewService(auth.SQLUserStore{DB: opts.DB}, nil, os.Getenv("JWT_SECRET"), 0, 0)
	}
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
		// 身份载体：内层 AuthMiddleware 验签后回填，响应后直接复用（403 不重查、401 不再查）。
		ctx, _ = withIdentityCarrier(ctx)
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
		// 归属与落库在响应后进行：请求 ctx 可能已取消，换不可取消 ctx 并限时（保留载体与链路 ID）。
		logCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), accessWriteTimeout)
		defer cancel()
		if pan != nil {
			recordEvent(logCtx, opts.Events, "error", "runtime", "runtime.panic",
				"请求处理发生 panic", logsafe.TextForLog(fmt.Sprint(pan)),
				map[string]any{"method": r.Method, "path": logsafe.PathForLog(r.URL.Path)})
		}
		ident := accessIdentity(logCtx, r, authn)
		dur := time.Since(start)
		writeAccessLog(logCtx, sink, accessEntry(r, status, dur, ident, traceID))
		if mutationMethod(r.Method) && status >= 200 && status < 300 {
			writeAccessLog(logCtx, sink, mutationEntry(r, status, dur, ident, traceID))
		}
		if pan != nil {
			panic(pan) // 记录后照常向上抛，不吞 panic
		}
	})
}

// accessIdentity 登录用户归属：内层挂载直取 context；最外层挂载读共享载体（含已定性匿名）；
// 都没有时按 AuthMiddleware 同口径取 token 验签回查（失败匿名）。
func accessIdentity(ctx context.Context, r *http.Request, authn Authenticator) auth.Identity {
	if ident, ok := IdentityFromContext(ctx); ok {
		return ident
	}
	if carrier, ok := identityCarrierFromContext(ctx); ok {
		if ident, resolved := carrier.load(); resolved {
			return ident
		}
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
	path := logsafe.PathForLog(r.URL.Path)
	detail := map[string]any{
		"method":      r.Method,
		"path":        path,
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
	raw := fmt.Sprintf("%s %s %d %.1fms uid=%d team=%d", r.Method, path, status, durMs, ident.ID, ident.TeamID)
	messageCN := fmt.Sprintf("%s %s %s（%d，耗时 %.0fms）", r.Method, path, accessPhrase(status), status, math.Round(durMs))
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

// mutationEntry 2xx 写操作审计条目：在 access 条目基础上改为 audit/http.mutation。
func mutationEntry(r *http.Request, status int, dur time.Duration, ident auth.Identity, traceID string) config.AppLogEntry {
	e := accessEntry(r, status, dur, ident, traceID)
	e.Category = "audit"
	e.Event = "http.mutation"
	e.Level = "info"
	path := logsafe.PathForLog(r.URL.Path)
	e.MessageCN = fmt.Sprintf("写操作成功（%s %s，状态 %d）", r.Method, path, status)
	return e
}

// recordEvent 写 security/config/runtime 事件：nil 静默，失败/panic 一律吞掉；
// 脱离请求取消并限时写入，绝不拖慢业务响应。
func recordEvent(ctx context.Context, events EventRecorder, level, category, event, messageCN, raw string, fields map[string]any) {
	if events == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	defer func() { _ = recover() }()
	ectx, cancel := context.WithTimeout(context.WithoutCancel(ctx), accessWriteTimeout)
	defer cancel()
	_ = events.Record(ectx, level, category, event, messageCN, raw, fields)
}

// writeAccessLog 落库：失败与异常一律吞掉，绝不影响业务响应。
func writeAccessLog(ctx context.Context, sink logging.Sink, e config.AppLogEntry) {
	defer func() { _ = recover() }()
	if sink == nil {
		return
	}
	_ = sink.Write(ctx, e)
}
