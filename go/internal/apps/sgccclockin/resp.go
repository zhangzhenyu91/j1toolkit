// 统一响应与请求体工具：沿用 server/src/utils/resp.js 的 {code,message,data} 信封与数字业务码。
package sgccclockin

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

// 常用业务码（与 server/src/sgccclockin/index.js 的 fail() 数字码一致）。
const (
	codeParam      = 40000 // 参数错误
	codeBadMobile  = 40030 // 手机号格式不正确
	codeSMSFail    = 40031 // 短信发送失败
	codeNeedCred   = 40032 // 请填写短信验证码或密码
	codeNeedRisk   = 40034 // 请先完成图形或滑块验证
	codeCaptchaOff = 40035 // 图形验证码暂不可用
	codePlatform   = 40037 // 平台业务失败
	codeFeeTpl     = 40038 // 获取商旅费用模板失败
	codeFeeSave    = 40039 // 商旅费用保存失败
	codeNoTeam     = 40010 // 无可用班组
	codeNoLogin    = 40101 // 未登录或登录已过期
	codeNoApp      = 40301 // 暂无该应用的使用权限
	codeNotAdmin   = 40012 // 仅管理员可执行此操作（对齐 requireDictAdmin）
	codeNoAssign   = 40310 // 未分配班组
	codeMissing    = 40400 // 对象不存在
	codeBoundOther = 40910 // 该成员已绑定其他登录账号
	codeRateLimit  = 42901 // 操作过于频繁
	codeInternal   = 50000 // 服务器开小差了
)

// fail 写出业务错误信封。
func fail(w http.ResponseWriter, status, code int, message string) {
	writeErr(w, &httpapi.APIError{Code: strconv.Itoa(code), Message: message, Status: status})
}

// writeErr 写出错误信封 {code,message}；数字业务码还原为 JSON 数值。
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

// writeJSON 输出 JSON。
func writeJSON(w http.ResponseWriter, status int, body map[string]any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

// decodeBody 解析 JSON 请求体到 map（保留字段存在性）。
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

// rawString 取文本：null/缺省 → ”，其余 trim。
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

// validMobile 手机号：1 开头 11 位。
func validMobile(s string) bool {
	if len(s) != 11 || s[0] != '1' {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// isDupEntry 判定 MySQL 唯一键冲突。
func isDupEntry(err error) bool {
	var me *mysql.MySQLError
	return errors.As(err, &me) && me.Number == 1062
}
