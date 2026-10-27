// 出工日志：统一响应信封、业务码与请求解析小工具。
// 业务码与 server/src/utils/resp.js 及 worklog/index.js 各 fail 口径一致。
package worklog

import (
	"encoding/json"
	"net/http"
	"regexp"
	"strconv"
	"strings"
)

// 业务码（与 Node 侧逐一对应）。
const (
	codeOK            = 0
	codeParam         = 40000 // 日期格式/月份格式/范围参数
	codeInput         = 40001 // 入参组合非法（未出车填目的地等）
	codeVehicle       = 40002
	codeDestination   = 40003
	codeMember        = 40004
	codeHasPhoto      = 40005 // 已有照片不可改未出车
	codeMemberPhoto   = 40006 // 人名已有照片不可移出
	codeNotCardMember = 40007 // 人名不在本卡用车人
	codeMemberUsed    = 40008 // 每人限一张
	codePickMembers   = 40009
	codePhotoFormat   = 40010
	codePhotoSize     = 40011
	codeDictName      = 40012
	codeDateRange     = 40013
	codeVerifyBusy    = 40014 // 识别中/仅失败可重验/非水印不参与
	codeWM            = 40015 // 水印渲染失败
	codeGeo           = 40016 // 经纬度参数无效
	codeNoPhoto       = 40017 // 没有可下载的照片
	codeFileBad       = 40018
	codeFileTooLarge  = 40019
	codeFileCount     = 40020 // 备注附件最多 9 个 / 同日同人重复
	codePreviewOnly   = 40021
	codeFile          = 40022 // 杆塔 Excel 解析口径
	codeDispatchFile  = 40023
	codeBoundMember   = 40024
	codeUnauthorized  = 40101
	codeAppDenied     = 40301
	codeForbidden     = 40304
	codeNoTeam        = 40310
	codeMissing       = 40400
	codeNoPhotos      = 40402
	codeDupName       = 40900
	codeDictInUse     = 40901
	codeVerifyFailed  = 40902 // 汇总前核验拦截（Node 40901 同码另一场景）
	codeOpRunning     = 40909
	codeInternal      = 50000
	codeDownload      = 50001 // ZIP 打包照片全部下载失败
	codeUpstream      = 50201
	codeNotConfigured = 50301
)

// Envelope 统一响应信封（成功 code=0；失败为数字业务码）。
type Envelope struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

// writeOK 成功响应；message 缺省 'ok'。
func writeOK(w http.ResponseWriter, data any, message ...string) {
	msg := "ok"
	if len(message) > 0 && message[0] != "" {
		msg = message[0]
	}
	writeJSON(w, http.StatusOK, Envelope{Code: codeOK, Message: msg, Data: data})
}

// fail 业务失败响应（无业务明细）。
func fail(w http.ResponseWriter, status, code int, message string) {
	writeJSON(w, status, Envelope{Code: code, Message: message})
}

// failData 业务失败响应（带业务明细，如汇总前核验的未通过清单）。
func failData(w http.ResponseWriter, status, code int, message string, data any) {
	writeJSON(w, status, Envelope{Code: code, Message: message, Data: data})
}

// writeErr 内部错误统一出口。
func writeErr(w http.ResponseWriter, err error) {
	writeJSON(w, http.StatusInternalServerError, Envelope{Code: codeInternal, Message: "服务器开小差了，请稍后再试"})
	_ = err
}

func writeJSON(w http.ResponseWriter, status int, env Envelope) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(env)
}

var (
	dateRe  = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)
	monthRe = regexp.MustCompile(`^\d{4}-\d{2}$`)
)

// decodeBody 解析 JSON 请求体为字段映射（未知字段保留，供 hasOwnProperty 语义比对）。
func decodeBody(r *http.Request) (map[string]json.RawMessage, error) {
	var body map[string]json.RawMessage
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		return nil, err
	}
	if body == nil {
		body = map[string]json.RawMessage{}
	}
	return body, nil
}

// rawString 取 JSON 字段的字符串值（数值/布尔同取字面量）。
func rawString(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	return strings.Trim(string(raw), `"`)
}

// rawNumberOr0 取 JSON 字段的数值（缺省 0；字符串数字兼容）。
func rawNumberOr0(raw json.RawMessage) int64 {
	if len(raw) == 0 {
		return 0
	}
	var n float64
	if err := json.Unmarshal(raw, &n); err == nil {
		return int64(n)
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		v, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
		if err == nil {
			return v
		}
	}
	return 0
}

// rawBool 取 JSON 字段的布尔（true/1/"true" 为真）。
func rawBool(raw json.RawMessage) bool {
	if len(raw) == 0 {
		return false
	}
	var b bool
	if err := json.Unmarshal(raw, &b); err == nil {
		return b
	}
	return strings.Trim(string(raw), `"`) == "true" || strings.TrimSpace(string(raw)) == "1"
}

// truncate 按列宽截断（入库前防 ER_DATA_TOO_LONG）。
func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}

// parseNames 解析 photo.members JSON 为名字数组。
func parseNames(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return []string{}
	}
	var arr []string
	if err := json.Unmarshal([]byte(raw), &arr); err != nil {
		return []string{}
	}
	if arr == nil {
		return []string{}
	}
	return arr
}
