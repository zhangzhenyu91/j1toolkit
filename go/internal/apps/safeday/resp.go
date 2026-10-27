// 响应信封：安全日自 SafeDayLogs 独立服务合并，响应保持 { ok, error, ... } 形状，
// 非主平台 {code,message,data} 信封（前端 parseOk 按此解析）。
package safeday

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
)

// writeOK 写出 { ok: true, ...extra }（extra 为附加字段，如 record/records/members）。
func writeOK(w http.ResponseWriter, extra map[string]any) {
	body := map[string]any{"ok": true}
	for k, v := range extra {
		body[k] = v
	}
	writeJSON(w, http.StatusOK, body)
}

// writeFail 写出 { ok: false, error }（可带额外字段如 warning/errors）。
func writeFail(w http.ResponseWriter, status int, errorText string, extra map[string]any) {
	body := map[string]any{"ok": false, "error": errorText}
	for k, v := range extra {
		body[k] = v
	}
	writeJSON(w, status, body)
}

// writeJSON 输出 JSON（与 Node 一致显式 no-store）。
func writeJSON(w http.ResponseWriter, status int, body map[string]any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

// decodeBody 解析 JSON 请求体到 map（保留字段存在性）；空体视为空对象。
func decodeBody(r *http.Request) (map[string]json.RawMessage, error) {
	raw, err := io.ReadAll(io.LimitReader(r.Body, 16<<20))
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

// cutForm 生成表单字段清洗：字符串、去首尾空格、限长（防异常超长入库存档）。
func cutForm(v string, n int) string {
	v = strings.TrimSpace(v)
	runes := []rune(v)
	if len(runes) > n {
		return string(runes[:n])
	}
	return v
}
