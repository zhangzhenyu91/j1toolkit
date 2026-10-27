// 统一响应与请求体工具：沿用 server/src/utils/resp.js 的 {code,message,data} 信封与数字业务码，
// 错误载体复用 httpapi.APIError（Code 存数字业务码字符串，输出时还原为 JSON 数值对齐 Node 契约）。
package adminapi

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

// 业务码（与 server/src/routes/admin.js、server/src/notice/index.js 的 fail() 数字码逐一对应；
// 40027 / 40406 为 Go 侧新增（用户删除与应用管理，Node 无对应接口），保持同一命名空间）。
const (
	codeBadUsername  = 40010 // 账号需为 2-64 位字母、数字或下划线
	codeBadPassword  = 40011 // 密码至少 6 位
	codeSelfProtect  = 40012 // 不能禁用/降级/删除自己的账号
	codeBadRole      = 40013 // 非法角色
	codeNoChange     = 40014 // 没有需要修改的内容
	codeTeamInvalid  = 40015 // 班组不存在或已停用
	codeNameEmpty    = 40017 // 请输入班组名称
	codeWXIDTooLong  = 40018 // wxid 不能超过 128 字
	codeAppIDsBad    = 40025 // app_ids 需为数组
	codeTeamAdminNil = 40026 // 班组管理员必须有所属班组
	codeLastAdmin    = 40027 // 不能删除最后一个超级管理员
	codeNoticeInput  = 40030 // 标题/内容不能为空 / 标题不能超过 128 字
	codeNoticeTarget = 40031 // 推送对象不合法
	codeNoLogin      = 40101 // 未登录或登录已过期
	codeNotAdmin     = 40304 // 仅管理员可执行此操作
	codeUserMissing  = 40401 // 用户不存在
	codeNoticeMiss   = 40403 // 通知不存在
	codeTeamMissing  = 40405 // 班组不存在
	codeAppMissing   = 40406 // 应用不存在
	codeUserDup      = 40901 // 账号已存在
	codeTeamDup      = 40902 // 班组已存在
	codeTeamInUse    = 40903 // 班组有数据引用，拒绝删除
	codeInternal     = 50000 // 服务器开小差了
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

// decodeBody 解析 JSON 请求体到 map（保留字段存在性，对齐 Node 的 hasOwnProperty 判定）；
// 空体视为空对象，非 JSON 体返回错误由调用方转 400 参数错。
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

// rawText 取字段原文（不 trim）：账号/密码按 Node 原样校验，空格不吞。
func rawText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	v := strings.TrimSpace(string(raw))
	if v == "null" {
		return ""
	}
	return strings.Trim(v, `"`)
}

// rawString 按 JS String(...) 口径取文本：null/缺省 → ”，其余去引号后 trim。
func rawString(raw json.RawMessage) string {
	return strings.TrimSpace(rawText(raw))
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

// rawFlag01 按 JS Number(x) ? 1 : 0 口径取 0/1 标记（"0"/false/0 → 0，true → 1）。
func rawFlag01(raw json.RawMessage) int {
	var b bool
	if err := json.Unmarshal(raw, &b); err == nil {
		if b {
			return 1
		}
		return 0
	}
	text := rawString(raw)
	n, err := strconv.ParseFloat(text, 64)
	if err != nil || n == 0 {
		return 0
	}
	return 1
}

// isDupEntry 判定 MySQL 唯一键冲突（ER_DUP_ENTRY=1062），对齐 Node 的 err.code === 'ER_DUP_ENTRY'。
func isDupEntry(err error) bool {
	var me *mysql.MySQLError
	return errors.As(err, &me) && me.Number == 1062
}

// DependencyUnavailableMsg 依赖不可用（数据库读写失败）错误，HTTP 落 dependency_unavailable（503）。
func DependencyUnavailableMsg(msg string) error { return httpapi.DependencyUnavailable(msg) }
