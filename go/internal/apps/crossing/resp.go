// 统一响应与请求体工具：沿用 server/src/utils/resp.js 的 {code,message,data} 信封与数字业务码，
// 错误载体复用 httpapi.APIError（Code 存数字业务码字符串，输出时还原为 JSON 数值对齐 Node 契约）。
package crossing

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-sql-driver/mysql"
	"shade/internal/httpapi"
)

// 常用业务码（与 server/src/crossing/index.js 的 fail() 数字码一致）。
const (
	codeParam      = 40001 // 参数错误 / 请求体不是合法 JSON
	codeNoTeam     = 40010 // 无可用班组（导出等）
	codeNoTeamDeep = 40310 // 未分配班组，请联系管理员分配（记录详情/删除等）
	codeFile       = 40022 // 暂存 xlsx 落盘类错误
	codeNoLogin    = 40101 // 未登录或登录已过期
	codeIngestTok  = 40104 // 对接密钥错误
	codeNoApp      = 40301 // 暂无该应用的使用权限
	codeNotAdmin   = 40304 // 仅管理员可执行此操作
	codeMissing    = 40400 // 对象不存在
	codeDup        = 40900 // 幂等键/唯一键冲突
	codeNoToken    = 50301 // 未配置对接密钥
	codeInternal   = 50000 // 服务器开小差了
)

// fail 按 server/src/utils/resp.js 口径写出业务错误信封。
func fail(w http.ResponseWriter, status, code int, message string) {
	writeErr(w, apiErr(status, code, message))
}

// apiErr 构造带业务码的错误（供不能直接写响应的辅助函数返回，最终由 writeErr 输出）。
func apiErr(status, code int, message string) error {
	return &httpapi.APIError{Code: strconv.Itoa(code), Message: message, Status: status}
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

// writeOK 写出成功信封 {code:0,message,data}（message 可选，默认 "ok"）。
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

// decodeBody 解析 JSON 请求体到 map（保留字段存在性，对齐 Node 的 hasOwnProperty 判定）；
// 空体视为空对象，非 JSON 体返回错误由调用方转 40001。
func decodeBody(r *http.Request) (map[string]json.RawMessage, error) {
	raw, err := io.ReadAll(io.LimitReader(r.Body, 8<<20))
	if err != nil {
		return nil, err
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

// rawString 按 JS String(...) 口径取文本：null/缺省 → ”，数字/布尔取原文，其余 trim。
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

// rawNumberOr0 按 JS Number(x) || 0 口径取整数：非数字/0 → 0。
func rawNumberOr0(raw json.RawMessage) int64 {
	text := rawString(raw)
	n, err := strconv.ParseFloat(text, 64)
	if err != nil || n == 0 {
		return 0
	}
	return int64(n)
}

// isDupEntry 判定 MySQL 唯一键冲突（ER_DUP_ENTRY=1062），对齐 Node 的 err.code === 'ER_DUP_ENTRY'。
func isDupEntry(err error) bool {
	var me *mysql.MySQLError
	if errors.As(err, &me) {
		return me.Number == 1062
	}
	// 假驱动/非驱动错误按文案兜底（测试夹具与 MySQL 文案一致）
	return strings.Contains(err.Error(), "Duplicate entry")
}

// truncate 按列宽截断（JS slice 口径：按字符）。
func truncate(s string, max int) string {
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	return string(runes[:max])
}
