// 统一响应与请求体工具：沿用 server/src/utils/resp.js 的 {code,message,data} 信封与数字业务码，
// 错误载体复用 httpapi.APIError（Code 存数字业务码字符串，输出时还原为 JSON 数值对齐 Node 契约）。
package wmadd

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	"shade/internal/httpapi"
)

// 常用业务码（与 server/src/wmadd/index.js 的 fail() 数字码一致）。
const (
	codeParam    = 40001 // 参数错误 / 请求体不是合法 JSON
	codeCoord    = 40022 // 经纬度参数无效
	codeFormat   = 40023 // 照片格式应为 jpeg/png
	codeSize     = 40024 // 照片大小应在 15MB 以内
	codeNoLogin  = 40101 // 未登录或登录已过期
	codeNoApp    = 40301 // 暂无该应用的使用权限
	codeInternal = 50000 // 服务器开小差了
)

// fail 按 server/src/utils/resp.js 口径写出业务错误信封。
func fail(w http.ResponseWriter, status, code int, message string) {
	writeErr(w, &httpapi.APIError{Code: strconv.Itoa(code), Message: message, Status: status})
}

// writeErr 写出错误信封 {code,message}；数字业务码还原为 JSON 数值，未知错误兜底 50000。
func writeErr(w http.ResponseWriter, err error) {
	var ae *httpapi.APIError
	if errors.As(err, &ae) {
		code := any(ae.Code)
		if n, e := strconv.Atoi(ae.Code); e == nil {
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

// writeOK 写出成功信封 {code:0,message,data}。
func writeOK(w http.ResponseWriter, data any, message ...string) {
	msg := "ok"
	if len(message) > 0 && message[0] != "" {
		msg = message[0]
	}
	writeJSON(w, http.StatusOK, map[string]any{"code": 0, "message": msg, "data": data})
}

// writeJSON 输出 JSON（与 Node 一致显式 no-store，保证数据即时更新）。
func writeJSON(w http.ResponseWriter, status int, body map[string]any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

// bodyMaxBytes /render JSON 请求体上限（对齐旧 Node server/src/index.js 对 /api/v1/wmadd 的
// express.json({limit:'22mb'})：15MB 原图 base64 展开约 20MB + JSON 包裹余量）。
const bodyMaxBytes = 22 << 20

// errBodyTooLarge 请求体超出 bodyMaxBytes：须按 40024 照片大小口径报错，
// 不得 io.LimitReader 静默截断后误报 40001「不是合法 JSON」。
var errBodyTooLarge = errors.New("请求体超出大小限制")

// decodeBody 解析 JSON 请求体到 map（保留字段存在性，对齐 Node 的 hasOwnProperty 判定）；
// 空体视为空对象，超限返回 errBodyTooLarge（多读 1 字节检测超限，不截断），非 JSON 体返回错误由调用方转 40001。
func decodeBody(r *http.Request) (map[string]json.RawMessage, error) {
	raw, err := io.ReadAll(io.LimitReader(r.Body, bodyMaxBytes+1))
	if err != nil {
		return nil, err
	}
	if len(raw) > bodyMaxBytes {
		return nil, errBodyTooLarge
	}
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" {
		return map[string]json.RawMessage{}, nil
	}
	var out map[string]json.RawMessage
	if err := json.Unmarshal([]byte(trimmed), &out); err != nil {
		return nil, err
	}
	if out == nil {
		out = map[string]json.RawMessage{}
	}
	return out, nil
}

// rawString 按 JS String(...) 口径取文本：null/缺省 → ”，其余去引号后 trim。
func rawString(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return strings.TrimSpace(s)
	}
	v := strings.TrimSpace(string(raw))
	if v == "null" {
		return ""
	}
	return strings.Trim(v, `"`)
}

// rawFloat 按 JS Number(x) 口径取浮点数；非法返回 (0,false)。
func rawFloat(raw json.RawMessage) (float64, bool) {
	text := rawString(raw)
	n, err := strconv.ParseFloat(text, 64)
	if err != nil {
		return 0, false
	}
	return n, true
}
