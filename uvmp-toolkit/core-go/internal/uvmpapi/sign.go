package uvmpapi

// sign.go — X-Content-Token 请求签名与 URL 构造，逐字移植 vehicle_export.py
// （make_x_content_token / UvmpClient._build / js_quote / b64url_nopad）。
// 签名算法已用浏览器真实样本双样本验证（见接口分析.md 第三节）：
//
//	new_data = 发送前的原始 p_data（未编码原文，filter 中用空格）合并 url 自有 query
//	           （解码后；rnd/cl_u_id/_/T/Auth_Token/ticket 不参与签名），按 key 排序
//	           后值经 `str(v) if v else v` 转换：真值字符串化（1.0→"1.0"、true→"True"），
//	           假值保持原类型（null/false/0/0.0/""/[]/{}）
//	content  = token + "." + b64url_nopad(js_quote(JSON(new_data))) + "." + b64url_nopad(temp_url)
//	sign     = base64url_nopad(HMAC-SHA256(key=unhex(mtk), content))
//	X-Content-Token = "MAC " + sign

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"net/url"
	"sort"
	"strings"

	"github.com/zhangzhenyu91/j1toolkit/uvmp-toolkit/core-go/internal/auth"
)

// signFiltered 不参与签名的 query 键（vehicle_export.SIGN_FILTERED）。
var signFiltered = map[string]bool{
	"rnd": true, "cl_u_id": true, "_": true,
	"T": true, "Auth_Token": true, "ticket": true,
}

// MakeXContentToken 计算 X-Content-Token 头（vehicle_export.make_x_content_token）。
// newData 为签名用原始值（GET 为 query 解码字符串，POST 为 body 原值；中文/空格不编码），
// 按 key 排序后逐值转 JSON（Python `{k:(str(v) if v else v)}` + json.dumps 口径，
// 见 signJSONValue）。GET 的字符串值经该口径不变（空串 "" 保持 ""），黄金向量不受影响；
// mtkHex 为 48 位等 hex 串。
func MakeXContentToken(token, mtkHex, tempURL string, newData map[string]any) (string, error) {
	keys := make([]string, 0, len(newData))
	for k := range newData {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var sb strings.Builder
	sb.WriteByte('{')
	for i, k := range keys {
		if i > 0 {
			sb.WriteByte(',')
		}
		sb.WriteString(pyJSONString(k))
		sb.WriteByte(':')
		vb, err := signJSONValue(newData[k])
		if err != nil {
			return "", err
		}
		sb.WriteString(vb)
	}
	sb.WriteByte('}')
	js := sb.String()

	content := token + "." +
		auth.B64URLNoPad([]byte(auth.JSQuote(js))) + "." +
		auth.B64URLNoPad([]byte(tempURL))
	mtk, err := hex.DecodeString(mtkHex)
	if err != nil {
		return "", fmt.Errorf("mtk 非法 hex: %w", err)
	}
	mac := hmac.New(sha256.New, mtk)
	mac.Write([]byte(content))
	return "MAC " + auth.B64URLNoPad(mac.Sum(nil)), nil
}

// signJSONValue 签名单值的 JSON 片段（Python `{k:(str(v) if v else v)}` + json.dumps 口径）：
// 真值按 str(v) 输出 JSON 字符串（1.0→"1.0"、true→"True"）；假值保持原类型
// （null/false/0/0.0/""/[]/{}），不做一律字符串化。
func signJSONValue(v any) (string, error) {
	if auth.PyTruthy(v) {
		return pyJSONString(auth.PyStr(v)), nil
	}
	switch t := v.(type) {
	case nil:
		return "null", nil
	case bool:
		return "false", nil
	case string:
		return `""`, nil
	case float32:
		return pyFalsyFloatJSON(float64(t)), nil
	case float64:
		return pyFalsyFloatJSON(t), nil
	case int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64:
		return "0", nil
	case []any:
		return "[]", nil
	case map[string]any:
		return "{}", nil
	default:
		return "", fmt.Errorf("签名值类型不支持: %T", v)
	}
}

// pyFalsyFloatJSON 假值浮点仅 ±0（Python json.dumps(0.0)="0.0"、(-0.0)="-0.0"）。
func pyFalsyFloatJSON(f float64) string {
	if math.Signbit(f) {
		return "-0.0"
	}
	return "0.0"
}

// pyJSONString Python json.dumps 字符串编码（ensure_ascii=False, separators=(",",":")）：
// 转义 " \ 与控制字符（\b \f \n \r \t 短转义，其余 \uXXXX），非 ASCII 原样保留。
func pyJSONString(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\b':
			b.WriteString(`\b`)
		case '\f':
			b.WriteString(`\f`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		default:
			if r < 0x20 {
				fmt.Fprintf(&b, `\u%04x`, r)
			} else {
				b.WriteRune(r)
			}
		}
	}
	b.WriteByte('"')
	return b.String()
}

// jsonNoHTMLEscape JSON 字符串编码（不做 HTML 转义，Python json.dumps
// ensure_ascii=False 口径，api.paramsJSON 用）。
func jsonNoHTMLEscape(s string) (string, error) {
	var sb strings.Builder
	enc := json.NewEncoder(&sb)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(s); err != nil {
		return "", err
	}
	return strings.TrimRight(sb.String(), "\n"), nil
}

// builtURL 构造结果：最终 URL、签名用 temp_url 与 new_data。
type builtURL struct {
	url     string
	tempURL string
	newData map[string]any
}

// buildURL 构造最终 URL 与签名用 new_data（vehicle_export.UvmpClient._build）。
// basePath 可自带 query（如 getPrintDetails?id=a,b,c——原样拼接不编码，与浏览器一致）；
// pData 为原始值（中文、空格不编码），URL 序列化用 quote_plus（空格->+，与抓包一致）。
func buildURL(gateway, basePath string, pData map[string]string, rnd, ts string) builtURL {
	tempURL := "/" + strings.SplitN(basePath, "?", 2)[0]
	u := gateway + basePath
	if strings.Contains(u, "?") {
		u += "&"
	} else {
		u += "?"
	}
	u += "rnd=" + rnd + "&cl_u_id=null"
	if len(pData) > 0 {
		keys := make([]string, 0, len(pData))
		for k := range pData {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		var sb strings.Builder
		for _, k := range keys {
			sb.WriteString("&")
			sb.WriteString(url.QueryEscape(k))
			sb.WriteString("=")
			sb.WriteString(url.QueryEscape(pData[k]))
		}
		u += sb.String()
	}
	u += "&_=" + ts

	newData := map[string]any{}
	if i := strings.Index(basePath, "?"); i >= 0 {
		for _, item := range strings.Split(basePath[i+1:], "&") {
			k, v, _ := strings.Cut(item, "=")
			if k == "" || signFiltered[k] {
				continue
			}
			if dec, err := url.QueryUnescape(v); err == nil {
				newData[k] = dec
			} else {
				newData[k] = v
			}
		}
	}
	for k, v := range pData {
		newData[k] = v
	}
	return builtURL{url: u, tempURL: tempURL, newData: newData}
}
