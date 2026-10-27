package httpapi

// MaskValue 敏感值脱敏（对外展示口径，与 go/internal/config.MaskSecret 同规则）：
// 仅保留首尾各 2 字符、中间以 **** 占位；不足 5 字符全掩为 ****；空串原样返回。
// HTTP 层输出敏感配置前统一过此函数，保证响应绝不回显完整敏感值。
func MaskValue(plain string) string {
	if plain == "" {
		return ""
	}
	r := []rune(plain)
	if len(r) <= 4 {
		return "****"
	}
	return string(r[:2]) + "****" + string(r[len(r)-2:])
}

// maskItems 返回脱敏后的副本（不修改入参）：敏感项 Value 过 MaskValue，其余原样。
func maskItems(items []ConfigItem) []ConfigItem {
	out := make([]ConfigItem, 0, len(items))
	for _, it := range items {
		if it.Sensitive {
			it.Value = MaskValue(it.Value)
		}
		out = append(out, it)
	}
	return out
}
