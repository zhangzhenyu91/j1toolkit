// 统一响应信封：沿用 server/src/utils/resp.js 的 {code,message,data} 口径（数字业务码）。
package wechatanswer

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"

	"github.com/go-sql-driver/mysql"
	"shade/internal/httpapi"
)

// 常用业务码。
const (
	codeParam    = 40001 // 参数错误 / 请求体不是合法 JSON
	codeNoLogin  = 40101 // 未登录或登录已过期
	codeNoApp    = 40301 // 暂无该应用的使用权限
	codeNotAdmin = 40304 // 仅管理员可执行此操作
	codeMissing  = 40400 // 对象不存在
	codeDup      = 40900 // 唯一键冲突
	codeInternal = 50000 // 服务器开小差了
)

func fail(w http.ResponseWriter, status, code int, message string) {
	writeErr(w, apiErr(status, code, message))
}

func apiErr(status, code int, message string) error {
	return &httpapi.APIError{Code: strconv.Itoa(code), Message: message, Status: status}
}

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
	var me *mysql.MySQLError
	if errors.As(err, &me) && me.Number == 1062 {
		writeJSON(w, http.StatusConflict, map[string]any{"code": codeDup, "message": "已存在同名/同键记录"})
		return
	}
	writeJSON(w, http.StatusInternalServerError, map[string]any{"code": codeInternal, "message": "服务器开小差了，请稍后再试"})
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func ok(w http.ResponseWriter, data any) {
	writeJSON(w, http.StatusOK, map[string]any{"code": 0, "message": "ok", "data": data})
}

// readBody 读取 JSON 请求体到 map（body 非法 → false 并已写出错误响应）。
func readBody(w http.ResponseWriter, r *http.Request) (map[string]json.RawMessage, bool) {
	raw, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		fail(w, http.StatusBadRequest, codeParam, "参数错误：请求体读取失败")
		return nil, false
	}
	if len(raw) == 0 {
		return map[string]json.RawMessage{}, true
	}
	var body map[string]json.RawMessage
	if err := json.Unmarshal(raw, &body); err != nil {
		fail(w, http.StatusBadRequest, codeParam, "参数错误：请求体不是合法 JSON")
		return nil, false
	}
	return body, true
}

func rawString(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	var n float64
	if err := json.Unmarshal(raw, &n); err == nil {
		return strconv.FormatFloat(n, 'f', -1, 64)
	}
	var b bool
	if err := json.Unmarshal(raw, &b); err == nil {
		if b {
			return "true"
		}
		return "false"
	}
	return ""
}

func rawBool(raw json.RawMessage, def bool) bool {
	if len(raw) == 0 {
		return def
	}
	var b bool
	if err := json.Unmarshal(raw, &b); err == nil {
		return b
	}
	return def
}
