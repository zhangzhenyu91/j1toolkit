package auth

// pyvalue.go — Python 值语义（真值判定与 str()）的 Go 移植，签名值转换与 isLogin
// 判定共用。语义以 CPython 3 为准：
//
//	PyTruthy == bool(v)：None/False/0/0.0/""/空容器为假，NaN 为真；
//	PyStr    == str(v)：字符串原样；数值取 Python 短表示（1.0→"1.0"、1e16→"1e+16"）；
//	容器按 repr 口径（"[1.0, 'a']"、"{'a': 1}"）。
//
// Python dict 按插入序输出，Go map 无插入序可依：嵌套 map 的 repr 一律按 key 排序
// （受控差异，顶层 new_data 本就按 sorted() 排序）。

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
)

// PyTruthy 等价 Python bool(v)。isLogin 等响应标志按此判定，兼容 bool/1/字符串等
// 常见形态——注意 Python 中非空字符串恒为真（含 "false"），此处与 Python 逐字一致。
func PyTruthy(v any) bool {
	switch t := v.(type) {
	case nil:
		return false
	case bool:
		return t
	case string:
		return t != ""
	case int:
		return t != 0
	case int8:
		return t != 0
	case int16:
		return t != 0
	case int32:
		return t != 0
	case int64:
		return t != 0
	case uint:
		return t != 0
	case uint8:
		return t != 0
	case uint16:
		return t != 0
	case uint32:
		return t != 0
	case uint64:
		return t != 0
	case float32:
		return t != 0 // NaN != 0 为真，同 Python bool(nan) == True
	case float64:
		return t != 0
	case json.Number:
		if t == "" {
			return false
		}
		f, err := t.Float64()
		return err != nil || f != 0
	case []any:
		return len(t) > 0
	case map[string]any:
		return len(t) > 0
	default:
		return true
	}
}

// PyStr 等价 Python str(v)：字符串原样返回；None→"None"、True/False→"True"/"False"、
// 整数→十进制、浮点→Python 短表示、容器→repr 口径。
func PyStr(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case nil:
		return "None"
	case bool:
		if t {
			return "True"
		}
		return "False"
	case int:
		return strconv.Itoa(t)
	case int32:
		return strconv.FormatInt(int64(t), 10)
	case int64:
		return strconv.FormatInt(t, 10)
	case float32:
		return pyFloatStr(float64(t))
	case float64:
		return pyFloatStr(t)
	case json.Number:
		if i, err := t.Int64(); err == nil {
			return strconv.FormatInt(i, 10)
		}
		if f, err := t.Float64(); err == nil {
			return pyFloatStr(f)
		}
		return t.String()
	case []any:
		parts := make([]string, len(t))
		for i, e := range t {
			parts[i] = pyRepr(e)
		}
		return "[" + strings.Join(parts, ", ") + "]"
	case map[string]any:
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		parts := make([]string, 0, len(keys))
		for _, k := range keys {
			parts = append(parts, pyReprStr(k)+": "+pyRepr(t[k]))
		}
		return "{" + strings.Join(parts, ", ") + "}"
	default:
		return fmt.Sprintf("%v", v)
	}
}

// pyRepr 等价 Python repr(v)（容器元素用）：字符串加引号转义，其余同 PyStr。
func pyRepr(v any) string {
	if s, ok := v.(string); ok {
		return pyReprStr(s)
	}
	return PyStr(v)
}

// pyReprStr 等价 Python repr(str)：默认单引号，含 ' 不含 " 时改用双引号；转义引号、
// 反斜杠与控制字符（\n \r \t 短转义，其余 \xHH），非 ASCII 原样保留。
func pyReprStr(s string) string {
	quote := byte('\'')
	if strings.ContainsRune(s, '\'') && !strings.ContainsRune(s, '"') {
		quote = '"'
	}
	var b strings.Builder
	b.WriteByte(quote)
	for _, r := range s {
		switch {
		case r == '\\':
			b.WriteString(`\\`)
		case r == '\n':
			b.WriteString(`\n`)
		case r == '\r':
			b.WriteString(`\r`)
		case r == '\t':
			b.WriteString(`\t`)
		case r < 0x20 || r == 0x7f:
			fmt.Fprintf(&b, `\x%02x`, r)
		case r == rune(quote):
			b.WriteByte('\\')
			b.WriteByte(quote)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte(quote)
	return b.String()
}

// pyFloatStr 等价 Python str(float)：最短往返十进制；小数点位置在 (-4,16] 之外走
// 科学计数（指数补足 2 位，如 1e+16 / 1e-05），其余定点、整值补 ".0"
// （同 CPython Py_DTSF_ADD_DOT_0）。
func pyFloatStr(f float64) string {
	switch {
	case math.IsNaN(f):
		return "nan"
	case math.IsInf(f, 1):
		return "inf"
	case math.IsInf(f, -1):
		return "-inf"
	}
	s := strconv.FormatFloat(f, 'e', -1, 64) // 如 "1.5e+07"、"-0e+00"
	mant, expStr, ok := strings.Cut(s, "e")
	if !ok {
		return s
	}
	exp, err := strconv.Atoi(expStr)
	if err != nil {
		return s
	}
	neg := strings.HasPrefix(mant, "-")
	digits := strings.NewReplacer(".", "", "-", "").Replace(mant)
	decpt := exp + 1
	var b strings.Builder
	if neg {
		b.WriteByte('-')
	}
	switch {
	case decpt <= -4 || decpt > 16: // 科学计数：d[.ddd]e±dd
		b.WriteString(digits[:1])
		if len(digits) > 1 {
			b.WriteByte('.')
			b.WriteString(digits[1:])
		}
		sign := "+"
		if exp < 0 {
			sign = "-"
		}
		fmt.Fprintf(&b, "e%s%02d", sign, absInt(exp))
	case decpt <= 0:
		b.WriteString("0.")
		b.WriteString(strings.Repeat("0", -decpt))
		b.WriteString(digits)
	case decpt >= len(digits):
		b.WriteString(digits)
		b.WriteString(strings.Repeat("0", decpt-len(digits)))
		b.WriteString(".0")
	default:
		b.WriteString(digits[:decpt])
		b.WriteByte('.')
		b.WriteString(digits[decpt:])
	}
	return b.String()
}

// absInt int 绝对值。
func absInt(n int) int {
	if n < 0 {
		return -n
	}
	return n
}
