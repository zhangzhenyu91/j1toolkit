package config

import (
	"strings"

	"github.com/zhangzhenyu91/j1toolkit/uvmp-toolkit/core-go/internal/auth"
)

// MaskedView 供 API 返回的脱敏视图：密码不回传，只告知是否已配置
// （Python config.masked_view 同口径）。
func MaskedView(cfg Dict) Dict {
	view := Dict{}
	for k, v := range cfg {
		if !strings.HasPrefix(k, "_") {
			view[k] = v
		}
	}
	view = deepCopy(view).(Dict)
	sso := MapField(view, "sso")
	if sso == nil {
		sso = Dict{}
		view["sso"] = sso
	}
	pw := StrField(sso, "password")
	iniPw := StrField(MapField(MapField(cfg, "_ini"), "auth"), "password")
	sso["password"] = ""
	sso["has_password"] = pw != "" || iniPw != ""
	switch {
	case pw != "":
		sso["password_source"] = "config.json"
	case iniPw != "":
		sso["password_source"] = "config.ini"
	default:
		sso["password_source"] = ""
	}
	return view
}

// SSOCredentials 有效 SSO 凭据：config.json 优先，config.ini [auth] 兜底
// （Python config.sso_credentials 同口径）。
func SSOCredentials(cfg Dict) (username, password string) {
	sso := MapField(cfg, "sso")
	iniAuth := MapField(MapField(cfg, "_ini"), "auth")
	username = strings.TrimSpace(StrField(sso, "username"))
	if username == "" {
		username = strings.TrimSpace(StrField(iniAuth, "username"))
	}
	password = StrField(sso, "password")
	if password == "" {
		password = StrField(iniAuth, "password")
	}
	return username, password
}

// VECfg 构造 vehicle_export 业务函数期望的 cfg 字典（与 Python config.ve_cfg 的解析口径一致）。
func VECfg(cfg Dict) Dict {
	exp := MapField(MapField(cfg, "_ini"), "export")
	out := Dict{}
	for k, v := range exp {
		out[k] = v
	}
	out["page_size"] = IntField(exp, "page_size", 50)
	out["run_state"] = StrFieldDefault(exp, "run_state", "2")
	out["vehicle_state"] = StrFieldDefault(exp, "vehicle_state", "04")
	out["keep_tag"] = StrFieldDefault(exp, "keep_tag", "00,01")
	out["map_tiles_wait"] = StrFieldDefault(exp, "map_tiles_wait", "12000")
	out["map_idle_quiet"] = StrFieldDefault(exp, "map_idle_quiet", "5000")
	out["map_min_wait"] = StrFieldDefault(exp, "map_min_wait", "30000")
	out["pcd_render_wait"] = StrFieldDefault(exp, "pcd_render_wait", "6000")
	out["track_render"] = StrFieldDefault(exp, "track_render", "official")
	return out
}

// Gateway 业务网关 base（结尾带 "/"）；config.ini [gateway] base 缺省时取内置默认
// （与 Python config.gateway 回退 vehicle_export.GATEWAY 同口径）。
func Gateway(cfg Dict) string {
	g := StrFieldTrim(MapField(MapField(cfg, "_ini"), "gateway"), "base")
	if g == "" {
		g = auth.DefaultGateway
	}
	if !strings.HasSuffix(g, "/") {
		g += "/"
	}
	return g
}

// RenderCfg render 配置：config.ini [render] 为底，config.json render.chrome（用户手选）覆盖。
func RenderCfg(cfg Dict) Dict {
	rc := Dict{}
	for k, v := range MapField(MapField(cfg, "_ini"), "render") {
		rc[k] = v
	}
	chosen := StrFieldTrim(MapField(cfg, "render"), "chrome")
	if chosen != "" {
		rc["chrome"] = chosen
	}
	return rc
}

// SsoCfg config.ini [sso] 段原样返回（SSO 登录参数）。
func SsoCfg(cfg Dict) Dict {
	out := Dict{}
	for k, v := range MapField(MapField(cfg, "_ini"), "sso") {
		out[k] = v
	}
	return out
}

// StrFieldDefault 取字符串值；仅键缺失时用默认值（Python dict.get(k, default) 口径）。
func StrFieldDefault(m Dict, key, def string) string {
	if m == nil {
		return def
	}
	if v, ok := m[key]; ok {
		if s, ok := v.(string); ok {
			return s
		}
		return ""
	}
	return def
}

// deepCopy 深拷贝 dict/list 叶子（脱敏视图用，避免改到原配置）。
func deepCopy(v any) any {
	switch t := v.(type) {
	case map[string]any:
		out := Dict{}
		for k, vv := range t {
			out[k] = deepCopy(vv)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, vv := range t {
			out[i] = deepCopy(vv)
		}
		return out
	default:
		return v
	}
}
