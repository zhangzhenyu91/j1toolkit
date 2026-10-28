// Package auth 是内网派车系统（UVMP）的认证层，逐字移植 uvmp-toolkit/vehicle_export.py
// 与 pylib/gmssl 3.2.2 的协议行为：
//
//   - SSO 账号密码登录（preLogin/check → login 302 链取 T），密码 SM3 混淆后
//     SM2(ASN.1 DER, C1C3C2) 加密，防重放三元组 sign = SM3("requestTime,nonce")；
//   - getUserAuth：SM2(C1C3C2, "04"+hex) 换取 token/mtk/refreshToken/loginUser，
//     响应用 SM4-CBC(key=K, iv=V, PKCS7) 解密；
//   - 并发安全 Manager：每批一次 Login，凭证快照带 Generation，
//     401/403 时 singleflight 刷新（tokenRefresh），多协程只发一次刷新请求。
//
// 全部 HTTP 响应只收未压缩内容（Accept-Encoding: identity），网关 deflate 错标问题已踩坑。
// 密码只经 stdin 配置进入进程，绝不写入命令行参数与日志。
package auth

import (
	"encoding/base64"
	"fmt"
	"strings"
)

// DefaultGateway 业务网关默认地址（vehicle_export.GATEWAY）。
const DefaultGateway = "http://20.1.59.23:18080/"

// DefaultSSOBase SSO 单点登录默认地址（vehicle_export.SSO_BASE）。
const DefaultSSOBase = "http://sso.tyqx.sgcc.com.cn/isc_sso"

// SSOAppID SSO 应用 id（vehicle_export.SSO_APPID，前端硬编码）。
const SSOAppID = "8af894fa6a6b03e3016a6b68580100cd"

// RedirectURI SSO 回调地址（vehicle_export.REDIRECT_URI，逐字保留）。
const RedirectURI = "http://20.1.59.23:18080/authLogin?redirectUrl=" +
	"http://uvmp.sgcc.com.cn/uvmp-web/factoryLayout/index.html" +
	"&tycl_parentMenuId=denglu&tycl_sonMenuId=denglu" +
	"&tycl_sonMenuTitle=%E7%99%BB%E5%BD%95"

// GDKey getUserAuth 的 SM2 公钥（前端硬编码，见接口分析.md）。
const GDKey = "04a1b7b1dfef3b21ef0523fe9496624a" +
	"4b99eb83ceb037e75bbbdcf61dab5" +
	"aced689064385366b46456f4d782699873ed3e4" +
	"3695dc64baa107e6d8a167eb570475"

// AuthError 认证/凭证类错误（对应 vehicle_export.AuthError）：
// SSO 登录失败、getUserAuth 失败、业务接口 401/403 等。请求层不再重试该类错误。
type AuthError struct {
	Msg string
}

func (e *AuthError) Error() string { return e.Msg }

// IsAuthError 判断 err 是否为认证/凭证错误。
func IsAuthError(err error) bool {
	_, ok := err.(*AuthError)
	return ok
}

// LogFunc 逐行日志回调（nil 表示丢弃）。密码等敏感值不得进入日志。
type LogFunc func(format string, args ...any)

func (f LogFunc) logf(format string, args ...any) {
	if f != nil {
		f(format, args...)
	}
}

// SSOConfig SSO 登录参数（config.ini [sso] 段）。
type SSOConfig struct {
	// Base SSO 登录地址，默认 DefaultSSOBase（httptest 假服务可覆盖）。
	Base string
	// ProvinceID / WangSheng 省域与网省参数，缺省 "sx"（与前端一致）。
	ProvinceID string
	WangSheng  string
	// SM2PubKey 密码加密公钥（128/130 位 hex）；空则取登录页 encryptKey。
	SM2PubKey string
}

// Config 认证配置，由 ParseConfig 从 config.load 字典解析（口径同 toolkit/config.py
// 的 gateway()/sso_credentials()/sso_cfg() 与 toolkit/uvmp.py 的 make_client()）。
type Config struct {
	// Gateway 业务网关 base，结尾带 "/"；空取 DefaultGateway。
	Gateway string
	// Mode "sso"（默认，账号密码自动登录）或 "token"（config.ini 手动三件套）。
	Mode string
	// Username / Password SSO 账号密码（sso 模式）。
	Username string
	Password string
	// Token / RefreshToken / Mtk 手动凭证（token 模式）。
	Token        string
	RefreshToken string
	Mtk          string
	// SSO 登录参数。
	SSO SSOConfig
}

// Credentials 一次登录后的凭证快照。Generation 从 1 起，每次登录/刷新 +1，
// 供调用方判断快照是否已被其他协程刷新（配合 Manager.Refresh 的 singleflight）。
type Credentials struct {
	Token        string
	RefreshToken string
	Mtk          string
	// LoginUser getUserAuth 返回的 loginUser 原样透传（官方渲染器凭证注入用）。
	LoginUser map[string]any
	// Generation 凭证代数：登录/刷新成功后 +1。
	Generation uint64
}

// JSQuote 等价于 JS encodeURIComponent（safe = "-_.!~*'()"），签名与 getUserAuth 共用。
func JSQuote(s string) string {
	const upper = "0123456789ABCDEF"
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z', c >= '0' && c <= '9',
			c == '-', c == '_', c == '.', c == '!', c == '~', c == '*', c == '\'', c == '(',
			c == ')':
			b.WriteByte(c)
		default:
			b.WriteByte('%')
			b.WriteByte(upper[c>>4])
			b.WriteByte(upper[c&0x0F])
		}
	}
	return b.String()
}

// B64URLNoPad base64url 编码并去除 "=" 填充（前端 Base64url safe_map 无填充）。
func B64URLNoPad(data []byte) string {
	return base64.RawURLEncoding.EncodeToString(data)
}

// ParseConfig 从 config.load 字典（toolkit/config.py load() 的输出，含 _ini 原始 ini 段）
// 解析认证配置。解析口径：
//
//   - gateway 取 _ini.gateway.base，缺省 DefaultGateway，补结尾 "/";
//   - mode 取 _ini.auth.mode，缺省 "sso"；"token" 走手动三件套，其余走 SSO;
//   - token 模式要求 _ini.auth 的 token/refresh_token/mtk 填齐；
//   - sso 模式凭据 config.json sso.* 优先、_ini.auth 兜底（config.sso_credentials）：
//     用户名两侧先各自 TrimSpace 再按序取用（json 全空白时回落 ini），密码不裁剪。
func ParseConfig(load map[string]any) (Config, error) {
	ini := mapSection(load, "_ini")
	iniAuth := mapSection(ini, "auth")
	iniSso := mapSection(ini, "sso")

	cfg := Config{
		Gateway: strings.TrimSpace(strField(mapSection(ini, "gateway"), "base")),
		Mode:    strings.TrimSpace(strField(iniAuth, "mode")),
		SSO: SSOConfig{
			Base:       strFieldDefault(iniSso, "base", DefaultSSOBase),
			ProvinceID: strFieldDefault(iniSso, "province_id", "sx"),
			WangSheng:  strFieldDefault(iniSso, "wangsheng", "sx"),
			SM2PubKey:  strings.TrimSpace(strField(iniSso, "sm2_pubkey")),
		},
	}
	if cfg.SSO.Base == "" {
		cfg.SSO.Base = DefaultSSOBase
	}
	if cfg.Gateway == "" {
		cfg.Gateway = DefaultGateway
	}
	if !strings.HasSuffix(cfg.Gateway, "/") {
		cfg.Gateway += "/"
	}
	if cfg.Mode == "" {
		cfg.Mode = "sso"
	}

	if cfg.Mode == "token" {
		cfg.Token = strings.TrimSpace(strField(iniAuth, "token"))
		cfg.RefreshToken = strings.TrimSpace(strField(iniAuth, "refresh_token"))
		cfg.Mtk = strings.TrimSpace(strField(iniAuth, "mtk"))
		if cfg.Token == "" || cfg.RefreshToken == "" || cfg.Mtk == "" {
			return Config{}, &AuthError{Msg: "config.ini [auth] mode=token，但 token/refresh_token/mtk 未填齐；" +
				"GUI 环境不能交互粘贴凭证，请改用 sso 模式或先在 config.ini 填好"}
		}
		return cfg, nil
	}

	jsonSso := mapSection(load, "sso")
	cfg.Username = orStr(strings.TrimSpace(strField(jsonSso, "username")), strings.TrimSpace(strField(iniAuth, "username")))
	cfg.Password = orStr(strField(jsonSso, "password"), strField(iniAuth, "password"))
	if cfg.Username == "" || cfg.Password == "" {
		return Config{}, &AuthError{Msg: "未配置 SSO 账号密码：请在工具箱设置页填写，或在 config.ini [auth] 填写"}
	}
	return cfg, nil
}

// mapSection 取嵌套 dict；缺失或类型不符返回空 map。
func mapSection(m map[string]any, key string) map[string]any {
	if m == nil {
		return map[string]any{}
	}
	if sub, ok := m[key].(map[string]any); ok {
		return sub
	}
	return map[string]any{}
}

// strField 取 dict 中的字符串值；非字符串（含缺失）返回 ""。
func strField(m map[string]any, key string) string {
	v, ok := m[key]
	if !ok {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

// strFieldDefault 取 dict 中的字符串值；仅当键缺失时用默认值（Python cfg.get(k, default) 口径）。
func strFieldDefault(m map[string]any, key, def string) string {
	if v, ok := m[key]; ok {
		if s, ok := v.(string); ok {
			return s
		}
		return ""
	}
	return def
}

// orStr 取第一个非空串（config.sso_credentials 的 or 链口径）。
func orStr(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// errAuthf 构造认证错误。
func errAuthf(format string, args ...any) error {
	return &AuthError{Msg: fmt.Sprintf(format, args...)}
}
