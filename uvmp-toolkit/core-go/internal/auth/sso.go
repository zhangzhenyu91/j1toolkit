package auth

// sso.go — SSO 账号密码登录与 getUserAuth 换证，逐字移植 vehicle_export.py
// （sso_login / Auth.via_getuserauth / encrypt_sso_password / _sign_triple 等）。
// 流程：GET 登录页（种 SESSION，取 execution 与 encryptKey）→ 密码混淆+SM2(ASN.1) 加密
// → POST preLogin/check（JSON，独立三元组）取 userTicket → POST login（表单）跟随 302 链
// 取 T(JWT) → getUserAuth 换 token/mtk/refreshToken/loginUser。

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// userAgent 浏览器 UA（vehicle_export 各 Session 固定头，逐字保留）。
const userAgent = "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/151.0.0.0 Safari/537.36"

var (
	executionRe  = regexp.MustCompile(`name="execution"[^>]*value="([^"]+)"`)
	encryptKeyRe = regexp.MustCompile(`encryptKey["']?\s*[:=]\s*["']([0-9a-fA-F]{128,130})["']`)
	tParamRe     = regexp.MustCompile(`[?&]T=([A-Za-z0-9._-]+)`)
)

// ssoLoginBody preLogin/check 的 JSON 体与 login 的表单字段（键序同 Python body 字典）。
type ssoLoginBody struct {
	AppID       string `json:"appId"`
	AuthMode    string `json:"authMode"`
	ProvinceID  string `json:"provinceId"`
	WangSheng   string `json:"wangsheng"`
	Username    string `json:"username"`
	Password    string `json:"password"`
	Execution   string `json:"execution"`
	EventID     string `json:"_eventId"`
	RequestTime string `json:"requestTime"`
	Nonce       string `json:"nonce"`
	Sign        string `json:"sign"`
	RedirectURI string `json:"redirectUri"`
}

// SSOLogin SSO 账号密码登录，返回 authLogin 颁发的 T(JWT)（vehicle_export.sso_login）。
// cfg 需含 Username/Password 与 SSO 参数；hc 建议带 Cookie 会话（Manager 默认提供）。
// 密码只在请求体中以 SM2 密文出现，绝不写日志。
func SSOLogin(ctx context.Context, hc *http.Client, cfg Config, logf LogFunc) (string, error) {
	service := RedirectURI
	// 1. 打开登录页（种 SESSION cookie，提取 execution 和密码加密公钥 encryptKey）
	u := strings.TrimSuffix(cfg.SSO.Base, "/") + "/login"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u+"?service="+url.QueryEscape(service), nil)
	if err != nil {
		return "", err
	}
	setCommonHeaders(req)
	resp, err := hc.Do(req)
	if err != nil {
		return "", fmt.Errorf("SSO 登录页请求失败: %w", err)
	}
	pageBytes, err := readBody(resp)
	if err != nil {
		return "", err
	}
	if resp.StatusCode != http.StatusOK {
		return "", errAuthf("SSO 登录页异常（HTTP %d）", resp.StatusCode)
	}
	execution, pagePubkey := extractLoginPage(string(pageBytes))
	pubkey := strings.TrimSpace(cfg.SSO.SM2PubKey)
	if pubkey == "" {
		pubkey = pagePubkey
	}
	if pubkey == "" {
		return "", errAuthf("登录页中未找到密码加密公钥(encryptKey)，请在 config.ini [sso] sm2_pubkey= 手动配置")
	}
	if normalizePubkey(pubkey) == "" {
		return "", errAuthf("SM2 公钥格式不正确（应为 128 或 04 开头的 130 位十六进制）")
	}
	// 2. 密码混淆+SM2(ASN.1) 加密；构造签名体
	encPwd, err := EncryptSSOPassword(cfg.Password, pubkey)
	if err != nil {
		return "", err
	}
	body := ssoLoginBody{
		AppID:       SSOAppID,
		AuthMode:    "ACCOUNT_PASSWORD_SGCC",
		ProvinceID:  cfg.SSO.ProvinceID,
		WangSheng:   cfg.SSO.WangSheng,
		Username:    cfg.Username,
		Password:    encPwd,
		Execution:   execution,
		EventID:     "submit",
		RedirectURI: service,
	}
	rt, nonce, sign := signTriple()
	body.RequestTime, body.Nonce, body.Sign = rt, nonce, sign

	// 3. preLogin/check（URL query 用独立的一组三元组）
	rt2, nonce2, sign2 := signTriple()
	payload, err := marshalNoHTMLEscape(body)
	if err != nil {
		return "", err
	}
	checkURL := strings.TrimSuffix(cfg.SSO.Base, "/") + "/preLogin/check?requestTime=" +
		url.QueryEscape(rt2) + "&nonce=" + url.QueryEscape(nonce2) + "&sign=" + url.QueryEscape(sign2)
	req2, err := http.NewRequestWithContext(ctx, http.MethodPost, checkURL, strings.NewReader(payload))
	if err != nil {
		return "", err
	}
	setCommonHeaders(req2)
	req2.Header.Set("Content-Type", "application/json;charset=UTF-8")
	pr, err := hc.Do(req2)
	if err != nil {
		return "", fmt.Errorf("preLogin/check 请求失败: %w", err)
	}
	prBytes, err := readBody(pr)
	if err != nil {
		return "", err
	}
	userTicket := extractUserTicket(prBytes)
	if userTicket == "" {
		return "", errAuthf("preLogin/check 未返回 userTicket（HTTP %d），响应原文：%s"+
			"——请把该响应发给开发者调整提取逻辑", pr.StatusCode, clip(prBytes, 300))
	}

	// 4. 正式登录（表单），跟随 302 链：login→authLogin?ticket→authLogin→index#home?T=
	form := url.Values{}
	form.Set("appId", body.AppID)
	form.Set("authMode", body.AuthMode)
	form.Set("provinceId", body.ProvinceID)
	form.Set("wangsheng", body.WangSheng)
	form.Set("username", body.Username)
	form.Set("password", body.Password)
	form.Set("execution", body.Execution)
	form.Set("_eventId", body.EventID)
	form.Set("requestTime", body.RequestTime)
	form.Set("nonce", body.Nonce)
	form.Set("sign", body.Sign)
	form.Set("redirectUri", body.RedirectURI)
	form.Set("userTicket", userTicket)

	var hopURLs []string
	hcFollow := *hc
	hcFollow.CheckRedirect = func(r *http.Request, via []*http.Request) error {
		if loc := r.Response.Header.Get("Location"); loc != "" {
			hopURLs = append(hopURLs, loc)
		}
		hopURLs = append(hopURLs, r.URL.String())
		if len(via) >= 30 {
			return fmt.Errorf("SSO 登录重定向次数过多")
		}
		return nil
	}
	loginURL := strings.TrimSuffix(cfg.SSO.Base, "/") + "/login?service=" + url.QueryEscape(service)
	req3, err := http.NewRequestWithContext(ctx, http.MethodPost, loginURL, strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	setCommonHeaders(req3)
	req3.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	lr, err := hcFollow.Do(req3)
	if err != nil {
		return "", fmt.Errorf("SSO 登录请求失败: %w", err)
	}
	lrBytes, err := readBody(lr)
	if err != nil {
		return "", err
	}
	hopURLs = append(hopURLs, lr.Request.URL.String())
	for _, u := range hopURLs {
		if m := tParamRe.FindStringSubmatch(u); m != nil {
			return m[1], nil
		}
	}
	return "", errAuthf("SSO 登录未获得 T：请检查账号密码是否正确（或响应: %s）", clip(lrBytes, 200))
}

// EncryptSSOPassword 与 aostaritEncrypt.js 一致：SM3(密码)(64hex) + 8 位随机串 + 密码，
// 再 SM2/ASN.1 加密（vehicle_export.encrypt_sso_password）。
func EncryptSSOPassword(password, pubHex string) (string, error) {
	wrapped := sm3Hex([]byte(password)) + randomString(8) + password
	return Sm2EncryptASN1([]byte(wrapped), pubHex)
}

// GetUserAuth 用 authLogin 颁发的 T，经 getUserAuth 换取 token/mtk/refreshToken
// （vehicle_export.Auth.via_getuserauth）。K/V 为前端随机 16 字节 hex。
func GetUserAuth(ctx context.Context, hc *http.Client, gateway, tJWT string, logf LogFunc) (Credentials, error) {
	k := randomHex16()
	v := randomHex16()
	return getUserAuthWithKV(ctx, hc, gateway, tJWT, k, v, logf)
}

// getUserAuthWithKV 同 GetUserAuth，但 K/V 由调用方给定（测试注入固定值用）。
func getUserAuthWithKV(ctx context.Context, hc *http.Client, gateway, tJWT, k, v string, logf LogFunc) (Credentials, error) {
	tokenData := "T=" + tJWT + "&K=" + k + "&V=" + v
	p, err := Sm2EncryptHex([]byte(JSQuote(tokenData)), GDKey)
	if err != nil {
		return Credentials{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		gateway+"getUserAuth?p="+url.QueryEscape(p), nil)
	if err != nil {
		return Credentials{}, err
	}
	setCommonHeaders(req)
	resp, err := hc.Do(req)
	if err != nil {
		return Credentials{}, fmt.Errorf("getUserAuth 请求失败: %w", err)
	}
	raw, err := readBody(resp)
	if err != nil {
		return Credentials{}, err
	}
	if resp.StatusCode != http.StatusOK {
		return Credentials{}, errAuthf("getUserAuth 异常（HTTP %d）: %s", resp.StatusCode, clip(raw, 200))
	}
	var outer struct {
		P string `json:"p"`
	}
	if err := json.Unmarshal(raw, &outer); err != nil || outer.P == "" {
		return Credentials{}, errAuthf("getUserAuth 返回异常: %s", clip(raw, 200))
	}
	key, err := hex.DecodeString(k)
	if err != nil {
		return Credentials{}, errAuthf("getUserAuth K 非法: %v", err)
	}
	iv, err := hex.DecodeString(v)
	if err != nil {
		return Credentials{}, errAuthf("getUserAuth V 非法: %v", err)
	}
	plain, err := Sm4CBCDecryptHex(outer.P, key, iv)
	if err != nil {
		return Credentials{}, errAuthf("getUserAuth 解密失败: %v", err)
	}
	data := map[string]any{}
	if err := json.Unmarshal([]byte(unquoteLenient(string(plain))), &data); err != nil {
		return Credentials{}, errAuthf("getUserAuth 明文解析失败: %v", err)
	}
	// 诊断输出：打印响应顶层字段（值脱敏，只显示长度），便于核对 token/mtk 字段名
	var brief []string
	for _, kk := range sortedKeys(data) {
		vv := data[kk]
		if _, isMap := vv.(map[string]any); isMap {
			continue
		}
		if _, isList := vv.([]any); isList {
			continue
		}
		brief = append(brief, fmt.Sprintf("%s(len=%d)", kk, len(fmt.Sprintf("%v", vv))))
	}
	logf.logf("getUserAuth 响应字段: %s", strings.Join(brief, ", "))
	if s, _ := data["mtk"].(string); s == "" {
		logf.logf("!! 警告：getUserAuth 响应中 mtk 字段为空或不存在，签名必然失败，请把上面字段列表发给开发者")
	}
	// isLogin 按 Python 真值判定（data.get("isLogin") 的 not 口径）：兼容 bool/1/字符串
	// 等常见形态，非空字符串恒为真（含 "false"，与 Python 逐字一致）。
	if !PyTruthy(data["isLogin"]) {
		return Credentials{}, errAuthf("getUserAuth: isLogin=false，T 已失效，请重新登录获取")
	}
	user, _ := data["loginUser"].(map[string]any)
	if user == nil {
		user = map[string]any{}
	}
	logf.logf("登录成功：%v（%v）", pickStr(user, "loginName", "?"), pickStr(user, "defaultOrgCode", "?"))
	token, _ := data["token"].(string)
	if token == "" {
		return Credentials{}, errAuthf("getUserAuth: 响应缺少 token 字段")
	}
	mtk, _ := data["mtk"].(string)
	refresh, _ := user["refreshToken"].(string)
	return Credentials{
		Token:        token,
		RefreshToken: refresh,
		Mtk:          mtk,
		LoginUser:    user,
	}, nil
}

// extractLoginPage 从登录页 HTML 提取 execution 和 aostaritEncryptUtils.init 的 encryptKey
// （vehicle_export._extract_login_page）。
func extractLoginPage(html string) (execution, pubkey string) {
	execution = "e1s1"
	if m := executionRe.FindStringSubmatch(html); m != nil {
		execution = m[1]
	}
	if m := encryptKeyRe.FindStringSubmatch(html); m != nil {
		pubkey = m[1]
	}
	return execution, pubkey
}

// extractUserTicket 从 preLogin/check 响应提取 userTicket（vehicle_export 同名逻辑逐字）。
func extractUserTicket(body []byte) string {
	var pj any
	if err := json.Unmarshal(body, &pj); err != nil {
		return ""
	}
	candidates := []any{pj}
	if m, ok := pj.(map[string]any); ok {
		for _, k := range []string{"data", "result", "resultValue", "dataMap"} {
			if sub, ok := m[k].(map[string]any); ok {
				candidates = append(candidates, sub)
			}
		}
	}
	for _, c := range candidates {
		if s, ok := c.(string); ok && strings.HasPrefix(s, "UT-") {
			return s
		}
		if m, ok := c.(map[string]any); ok {
			for _, k := range []string{"userTicket", "ticket", "ut", "user_ticket"} {
				if s, ok := m[k].(string); ok && strings.HasPrefix(s, "UT-") {
					return s
				}
			}
		}
	}
	return ""
}

// signTriple preLogin/login 的防重放三元组：sign = SM3("requestTime,nonce")
// （vehicle_export._sign_triple，已由 4 组抓包样本验证）。
func signTriple() (requestTime, nonce, sign string) {
	requestTime = strconv.FormatInt(time.Now().UnixMilli(), 10)
	nonce = getUID()
	sign = sm3Hex([]byte(requestTime + "," + nonce))
	return requestTime, nonce, sign
}

// getUID 与前端 getUID 一致：'xxxxxxxxxxxx4xxxyxxxxxxxxxxxxxxx' 形式的 32 位 hex
// （vehicle_export._getuid）。
func getUID() string {
	const pattern = "xxxxxxxxxxxx4xxxyxxxxxxxxxxxxxxx"
	out := make([]byte, 0, len(pattern))
	for _, c := range pattern {
		switch c {
		case 'x':
			out = append(out, hexDigit(randomNibble()))
		case 'y':
			out = append(out, hexDigit(randomNibble()&0x3|0x8))
		default:
			out = append(out, byte(c))
		}
	}
	return string(out)
}

// randomString 与前端 getRandomString 相同字符集（去掉易混淆字符，
// vehicle_export._random_string，字符集上均匀取值）。
func randomString(n int) string {
	const chars = "ABCDEFGHJKMNPQRSTWXYZabcdefhijkmnprstwxyz2345678"
	out := make([]byte, n)
	for i := range out {
		out[i] = chars[randomBelow(uint32(len(chars)))]
	}
	return string(out)
}

// setCommonHeaders 内网网关/SSO 统一请求头：只收未压缩内容
// （网关 deflate 错标间歇出现，已踩坑，Accept-Encoding: identity 根除）。
func setCommonHeaders(req *http.Request) {
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept-Encoding", "identity")
}

// readBody 读完并关闭响应体。
func readBody(resp *http.Response) ([]byte, error) {
	defer resp.Body.Close()
	return io.ReadAll(resp.Body)
}

// clip 截断字符串用于报错摘要（Python s[:n] 口径，按字节）。
func clip(b []byte, n int) string {
	s := string(b)
	if len(s) > n {
		return s[:n]
	}
	return s
}

// unquoteLenient 等价 Python urllib.parse.unquote：只做 %XX 解码，'+' 不转空格，
// 非法序列原样保留。
func unquoteLenient(s string) string {
	if !strings.Contains(s, "%") {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); {
		if s[i] == '%' && i+2 < len(s) {
			hi, ok1 := unhexVal(s[i+1])
			lo, ok2 := unhexVal(s[i+2])
			if ok1 && ok2 {
				b.WriteByte(hi<<4 | lo)
				i += 3
				continue
			}
		}
		b.WriteByte(s[i])
		i++
	}
	return b.String()
}

func unhexVal(c byte) (byte, bool) {
	switch {
	case c >= '0' && c <= '9':
		return c - '0', true
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10, true
	case c >= 'A' && c <= 'F':
		return c - 'A' + 10, true
	}
	return 0, false
}

// marshalNoHTMLEscape JSON 紧凑序列化，不做 HTML 转义（Python json.dumps
// ensure_ascii=False, separators=(",",":") 口径；结构体字段序即键序）。
func marshalNoHTMLEscape(v any) (string, error) {
	var sb strings.Builder
	enc := json.NewEncoder(&sb)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return "", err
	}
	return strings.TrimRight(sb.String(), "\n"), nil
}

// sortedKeys 字典键升序（签名与诊断输出用）。
func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	for i := 1; i < len(keys); i++ {
		for j := i; j > 0 && keys[j] < keys[j-1]; j-- {
			keys[j], keys[j-1] = keys[j-1], keys[j]
		}
	}
	return keys
}

// pickStr 取字典字符串字段，缺失给默认值。
func pickStr(m map[string]any, key, def string) string {
	if s, ok := m[key].(string); ok && s != "" {
		return s
	}
	return def
}

func hexDigit(n byte) byte {
	if n < 10 {
		return '0' + n
	}
	return 'a' + (n - 10)
}
