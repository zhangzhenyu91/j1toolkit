// 统一响应与请求体工具：沿用 server/src/utils/resp.js 的 {code,message,data} 信封与数字业务码，
// 错误载体复用 httpapi.APIError（Code 存数字业务码字符串，输出时还原为 JSON 数值对齐 Node 契约）。
package authwx

import (
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"

	"shade/internal/httpapi"
)

// maxAuthBodyBytes 登录/绑定请求体上限（登录体很小；超限截断后 JSON 解析失败，
// 避免先把超大请求体整读进内存）。
const maxAuthBodyBytes = 1 << 20

// 登录域数字业务码（与 server/src/routes/auth.js 的 fail() 数值码一致）。
const (
	codeBadParam  = 40001 // 参数错误：缺账号密码 / 请求体不是合法 JSON
	codeNoWXCode  = 40002 // 缺少微信 code
	codeWXFailed  = 40012 // 微信登录失败（code2session 报错）
	codeBadCreds  = 40111 // 账号或密码错误
	codeNoApp     = 40301 // 暂无该应用的使用权限
	codeDisabled  = 40302 // 账号已被禁用
	codeWXUnbound = 40313 // 微信号未绑定账号
	codeRateLimit = 42901 // 尝试过于频繁
	codeInternal  = 50000 // 服务器开小差了
	codeNotConfig = 50301 // 未配置 / 未接入（微信登录、认证服务）
)

// apiErr 构造带数字业务码的错误（输出时还原为 JSON 数值）。
func apiErr(status, code int, message string) error {
	return &httpapi.APIError{Code: strconv.Itoa(code), Message: message, Status: status}
}

// fail 按 resp.js 口径写出业务错误信封。
func fail(w http.ResponseWriter, status, code int, message string) {
	writeErr(w, apiErr(status, code, message))
}

// writeOK 写出成功信封 {code:0,message,data}。
func writeOK(w http.ResponseWriter, data any) {
	writeJSON(w, http.StatusOK, map[string]any{"code": 0, "message": "ok", "data": data})
}

// writeErr 写出错误信封 {code,message}；数字业务码还原为 JSON 数值，未知错误兜底 50000。
func writeErr(w http.ResponseWriter, err error) {
	var ae *httpapi.APIError
	if errors.As(err, &ae) {
		code := any(codeInternal)
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
		writeJSON(w, status, map[string]any{"code": code, "message": msg})
		return
	}
	writeJSON(w, http.StatusInternalServerError, map[string]any{"code": codeInternal, "message": "服务器开小差了，请稍后再试"})
}

// writeJSON 写出 JSON 响应（统一 Content-Type 与 no-store）。
func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

// decodeJSON 解码请求体（未知字段忽略，与 Node express.json 同口径——登录体允许随客户端扩展字段）；
// 请求体在解析前限总量（maxAuthBodyBytes），超限按截断 JSON 报错。
func decodeJSON(r *http.Request, target any) error {
	return json.NewDecoder(io.LimitReader(r.Body, maxAuthBodyBytes)).Decode(target)
}

// clientIP 取请求来源 IP。Node 侧 app.set('trust proxy', 'loopback')（server/src/index.js）：
// 环回反代（nginx 同机）之后按 X-Forwarded-For 自右向左取第一个非环回地址，直连不认 XFF——
// 登录限流按 IP 统计依赖此口径，不能只看 RemoteAddr（否则反代后全站共享一个限流桶）。
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	host = strings.TrimSpace(host)
	if !isLoopbackAddr(host) {
		return host
	}
	parts := strings.Split(r.Header.Get("X-Forwarded-For"), ",")
	var hops []string
	for _, p := range parts {
		if ip := strings.TrimSpace(p); ip != "" {
			hops = append(hops, ip)
		}
	}
	for i := len(hops) - 1; i >= 0; i-- {
		if !isLoopbackAddr(hops[i]) {
			return hops[i]
		}
	}
	if len(hops) > 0 {
		return hops[0]
	}
	return host
}

// isLoopbackAddr 判定环回地址（Node proxy-addr loopback：127.0.0.0/8 与 ::1）。
func isLoopbackAddr(host string) bool {
	h := strings.Trim(host, "[]")
	if h == "localhost" {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && ip.IsLoopback()
}
