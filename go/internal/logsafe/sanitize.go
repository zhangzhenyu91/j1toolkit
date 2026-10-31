// Package logsafe 提供写日志前的脱敏工具：URL、查询参数等。
package logsafe

import (
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"
)

// Mask 日志中的密文占位。
const Mask = "****"

// sensitiveExact 敏感查询参数名（小写精确匹配）。
var sensitiveExact = map[string]struct{}{
	"auth":          {},
	"authorization": {},
	"code":          {},
	"credential":    {},
	"credentials":   {},
	"cookie":        {},
	"jwt":           {},
	"key":           {},
	"js_code":       {}, // 微信登录一次性 js_code（视同 code）
	"api_key":       {},
	"apikey":        {},
	"pass":          {},
	"password":      {},
	"passwd":        {},
	"pwd":           {},
	"secret":        {},
	"session":       {},
	"session_id":    {},
	"sessionid":     {},
	"session_key":   {},
	"secret_key":    {},
	"private_key":   {},
	"client_secret": {},
	"sig":           {},
	"sign":          {},
	"signature":     {},
	"ticket":        {},
	"token":         {},
	"access_token":  {},
	"refresh_token": {},
	"id_token":      {},
	"accesstoken":   {},
	"refreshtoken":  {},
	"idtoken":       {},
}

// sensitiveSuffix 常见云厂商/网关前缀（x-amz-signature、x-goog-credential 等）。
var sensitiveSuffix = []string{
	"signature", "token", "secret", "password", "passwd", "ticket", "credential",
	"apikey", "api_key", "api-key", "authorization", "cookie",
	"session_key", "secret_key", "private_key", "client_secret",
}

// isSensitiveKey 判断查询参数名是否敏感（大小写不敏感；不误伤 fileInfoId 等业务键）。
func isSensitiveKey(key string) bool {
	k := strings.ToLower(strings.TrimSpace(key))
	k = strings.TrimSuffix(strings.TrimSuffix(k, "[]"), "[]")
	if k == "" {
		return false
	}
	if _, ok := sensitiveExact[k]; ok {
		return true
	}
	for _, suffix := range sensitiveSuffix {
		if len(k) > len(suffix) && strings.HasSuffix(k, suffix) {
			return true
		}
	}
	return false
}

// URLForLog 生成可写日志的 URL：保留 scheme、host、完整 path、查询参数名与顺序，
// 仅掩码 userinfo 与敏感参数值；解析失败返回错误且不产生输出。
func URLForLog(raw string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", errors.New("logsafe: URL 为空")
	}
	u, err := url.Parse(trimmed)
	if err != nil {
		return "", fmt.Errorf("logsafe: 解析 URL: %w", err)
	}
	if u.User != nil {
		if _, hasPassword := u.User.Password(); hasPassword {
			u.User = url.UserPassword(Mask, Mask)
		} else {
			u.User = url.User(Mask)
		}
	}
	u.Path = PathForLog(u.Path)
	u.RawPath = ""
	u.RawQuery = maskRawQuery(u.RawQuery)
	// fragment 常携带 OAuth 隐式 token；整体掩码，避免原样泄露。
	if u.Fragment != "" {
		u.Fragment = Mask
		u.RawFragment = ""
	}
	out := u.String()
	// net/url 会把 userinfo 中的 * 转义为 %2A；日志可读性要求保留字面 ****。
	if u.User != nil {
		if _, hasPassword := u.User.Password(); hasPassword {
			out = strings.Replace(out, url.UserPassword(Mask, Mask).String()+"@", Mask+":"+Mask+"@", 1)
		} else {
			out = strings.Replace(out, url.User(Mask).String()+"@", Mask+"@", 1)
		}
	}
	if u.Fragment == Mask {
		if idx := strings.LastIndex(out, "#"); idx >= 0 {
			out = out[:idx+1] + Mask
		}
	}
	return out, nil
}

// maskRawQuery 逐段处理原始查询串：保留参数名、分隔符（&/;）与原编码，
// 仅替换敏感值为 ****；不使用 url.Values.Encode()（会重排参数并改变非敏感值编码）。
func maskRawQuery(raw string) string {
	if raw == "" {
		return ""
	}
	var b strings.Builder
	b.Grow(len(raw))
	start := 0
	for i := 0; i < len(raw); i++ {
		if raw[i] == '&' || raw[i] == ';' {
			b.WriteString(maskQuerySegment(raw[start:i]))
			b.WriteByte(raw[i])
			start = i + 1
		}
	}
	b.WriteString(maskQuerySegment(raw[start:]))
	return b.String()
}

func maskQuerySegment(seg string) string {
	eq := strings.IndexByte(seg, '=')
	if eq < 0 {
		return seg
	}
	name := seg[:eq]
	decoded, err := url.QueryUnescape(name)
	if err != nil {
		decoded = name
	}
	if isSensitiveKey(decoded) {
		return name + "=" + Mask
	}
	return seg
}

// SensitiveKey 查询参数/自由文本键名是否敏感（与 URLForLog 同口径；
// fileInfoId、page、key_name、object_key 等业务/路径键不误伤）。
func SensitiveKey(key string) bool { return isSensitiveKey(key) }

// QueryForLog 查询参数脱敏：敏感键值一律 ****，其余取原值（多值逗号拼接）。
func QueryForLog(q url.Values) map[string]string {
	if len(q) == 0 {
		return nil
	}
	out := make(map[string]string, len(q))
	for name, values := range q {
		if isSensitiveKey(name) {
			out[name] = Mask
			continue
		}
		out[name] = strings.Join(values, ",")
	}
	return out
}

var (
	// reAssign 自由文本 name=value（URL query 与裸键值对通用）。
	reAssign = regexp.MustCompile(`([A-Za-z0-9_\-.]+=)([^&\s"']*)`)
	// reBearer Authorization: Bearer 令牌。
	reBearer = regexp.MustCompile(`(?i)(bearer\s+)[^\s"']+`)
	// reJSONSecret JSON 字符串字段中的密钥值（code/ticket 等短票据同样掩码）。
	reJSONSecret = regexp.MustCompile(`(?i)("(?:password|passwd|pwd|secret|secret_key|session_key|private_key|client_secret|token|access_token|refresh_token|api_key|apikey|authorization|cookie|signature|code|js_code|ticket|openid|unionid)"\s*:\s*")[^"]*(")`)
	// reURL 文本中的完整 http(s) URL（userinfo/query/fragment/路径票据由 URLForLog 处理）。
	reURL = regexp.MustCompile(`(?i)https?://[^\s"'<>]+`)
	// rePathTicket 文本中的已知票据路径（无 scheme 时也掩码）。
	rePathTicket = regexp.MustCompile(`(?i)(?:/api/v1/office/raw/|/api/v1/office/callback/|/api/v1/netdisk/preview/|/api/v1/netdisk/public/share/|/api/v1/share/)[^\s"'<>]*`)
)

// TextForLog 自由文本脱敏：JSON 密钥字段、name=value 敏感参数、Bearer 令牌一律 ****。
func TextForLog(s string) string {
	if s == "" {
		return ""
	}
	// JSON 序列化会转义 &；还原后再按 URL 解析，避免 query 参数串成一段。
	s = strings.ReplaceAll(s, `\u0026`, "&")
	s = reURL.ReplaceAllStringFunc(s, func(m string) string {
		if safe, err := URLForLog(m); err == nil {
			return safe
		}
		return Mask
	})
	s = rePathTicket.ReplaceAllStringFunc(s, PathForLog)
	s = reJSONSecret.ReplaceAllString(s, "${1}"+Mask+"${2}")
	s = reAssign.ReplaceAllStringFunc(s, func(m string) string {
		parts := reAssign.FindStringSubmatch(m)
		if len(parts) == 3 && isSensitiveKey(strings.TrimSuffix(parts[1], "=")) {
			return parts[1] + Mask
		}
		return m
	})
	s = reBearer.ReplaceAllString(s, "${1}"+Mask)
	return s
}

// sensitiveFieldExact 结构化字段的敏感名（精确匹配，避免误伤 path/key_name/object_key）。
var sensitiveFieldExact = map[string]bool{
	"password": true, "passwd": true, "pwd": true,
	"secret": true, "secret_key": true, "secretkey": true,
	"token": true, "access_token": true, "refresh_token": true,
	"api_key": true, "apikey": true, "key": true,
	"signature": true, "sig": true,
	"authorization": true, "auth": true,
	"credential": true, "credentials": true,
	"session": true, "sessionid": true, "sid": true, "jwt": true,
	"cookie": true, "ticket": true,
	"session_key": true, "private_key": true, "client_secret": true,
	"js_code": true,
}

// FieldsForLog 结构化字段脱敏：敏感字段名整值 ****，字符串值再过 TextForLog；
// map/slice 递归处理（JSON fields 嵌套）。path / key_name / object_key / cos_key 等
// 路径类键保持原值。
func FieldsForLog(fields map[string]any) map[string]any {
	if fields == nil {
		return nil
	}
	out := make(map[string]any, len(fields))
	for k, v := range fields {
		if sensitiveFieldExact[strings.ToLower(k)] {
			out[k] = Mask
			continue
		}
		out[k] = sanitizeValue(v)
	}
	return out
}

// sanitizeValue 递归脱敏字段值。
func sanitizeValue(v any) any {
	switch x := v.(type) {
	case string:
		return TextForLog(x)
	case map[string]any:
		return FieldsForLog(x)
	case []any:
		out := make([]any, len(x))
		for i, item := range x {
			out[i] = sanitizeValue(item)
		}
		return out
	case map[string]string:
		out := make(map[string]any, len(x))
		for k, v := range x {
			if sensitiveFieldExact[strings.ToLower(k)] {
				out[k] = Mask
				continue
			}
			out[k] = TextForLog(v)
		}
		return out
	case []map[string]any:
		out := make([]any, len(x))
		for i, item := range x {
			out[i] = sanitizeValue(item)
		}
		return out
	case []map[string]string:
		out := make([]any, len(x))
		for i, item := range x {
			out[i] = sanitizeValue(item)
		}
		return out
	case []string:
		out := make([]string, len(x))
		for i, item := range x {
			out[i] = TextForLog(item)
		}
		return out
	default:
		return v
	}
}

// PathForLog 路径型票据脱敏：Office raw/callback、netdisk preview、公开分享只掩码票据段，
// 文件名/后续动作段保留；普通 API 路径原样返回。
func PathForLog(path string) string {
	if path == "" {
		return ""
	}
	switch {
	case strings.HasPrefix(path, "/api/v1/office/raw/"):
		return maskFirstSegment("/api/v1/office/raw/", path)
	case strings.HasPrefix(path, "/api/v1/office/callback/"):
		return "/api/v1/office/callback/" + Mask
	case strings.HasPrefix(path, "/api/v1/netdisk/preview/"):
		rest := strings.TrimPrefix(path, "/api/v1/netdisk/preview/")
		if rest == "sign" {
			return path
		}
		return maskFirstSegment("/api/v1/netdisk/preview/", path)
	case strings.HasPrefix(path, "/api/v1/netdisk/public/share/"):
		return maskFirstSegment("/api/v1/netdisk/public/share/", path)
	case strings.HasPrefix(path, "/api/v1/share/"):
		return maskFirstSegment("/api/v1/share/", path)
	default:
		return path
	}
}

// maskFirstSegment 保留前缀，只把首段（票据/分享 id）替换为 ****，后续段原样保留。
func maskFirstSegment(prefix, path string) string {
	rest := strings.TrimPrefix(path, prefix)
	if rest == "" {
		return path
	}
	if i := strings.IndexByte(rest, '/'); i >= 0 {
		return prefix + Mask + rest[i:]
	}
	return prefix + Mask
}
