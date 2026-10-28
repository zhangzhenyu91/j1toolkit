// 协议加解密内核：移植 server/src/sgccclockin/protocol.js 的 jsonm/jsonx 两条通道，
// SM2/RSA/AES-128-ECB 全部复用 compat/cryptoaudio（勿改口径，与逆向分析仓实测一致）。
// jsonm（H5，gwslapi）：AES-128-ECB + SM2 加密 AES 密钥；用于图形码/发短信与 uniID 池。
// jsonx（App，gwslapi/gwslapizb）：RSA 分块加密（117B/128B，PKCS1）；
// tenant=default 用 RSA_PUB/RSA_PRIV；tenant=slapp 用 dCu 加密 / wLA 解密。
package sgccclockin

import (
	"crypto/hmac"
	"crypto/md5"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"shade/internal/compat/cryptoaudio"
)

// Keys 协议密钥（env 注入；缺失时业务调用返回明确错误）。
type Keys struct {
	JWTSecret     string // SGCC_JWT_SECRET
	AdSecret      string // SGCC_AD_SECRET
	SM2ServerPub  string // SGCC_SM2_SERVER_PUB
	SM2ClientPriv string // SGCC_SM2_CLIENT_PRIV
	RSAPub        string // SGCC_RSA_PUB（default 通道加密）
	RSAPriv       string // SGCC_RSA_PRIV（default 通道解密）
	DCUPub        string // SGCC_DCU_PUB（slapp 通道加密）
	WLAPriv       string // SGCC_WLA_PRIV（slapp 通道解密）
	Version       string // 客户端版本（默认 3.3.7）
	GrayVersion   string // 灰度版本（默认 2.4.7.1）
	VersionCode   string // 版本号（默认 202609232038）
}

func (k Keys) version() string {
	if k.Version != "" {
		return k.Version
	}
	return "3.3.7"
}

func (k Keys) grayVersion() string {
	if k.GrayVersion != "" {
		return k.GrayVersion
	}
	return "2.4.7.1"
}

func (k Keys) versionCode() string {
	if k.VersionCode != "" {
		return k.VersionCode
	}
	return "202609232038"
}

// ErrMissingKey 缺少协议密钥。
type ErrMissingKey struct{ Name string }

func (e *ErrMissingKey) Error() string {
	return fmt.Sprintf("缺少环境变量 %s（商旅打卡协议密钥，见 docker/.env.example）", e.Name)
}

func need(name, v string) (string, error) {
	if v == "" {
		return "", &ErrMissingKey{Name: name}
	}
	return v, nil
}

// b64url base64url 无填充。
func b64url(b []byte) string {
	s := base64.RawURLEncoding.EncodeToString(b)
	return s
}

// ToPEM 把裸 base64 密钥包成 PEM（label = PUBLIC KEY / PRIVATE KEY）。
func ToPEM(b64, label string) string {
	body := strings.Join(strings.Fields(b64), "")
	var lines []string
	for i := 0; i < len(body); i += 64 {
		end := i + 64
		if end > len(body) {
			end = len(body)
		}
		lines = append(lines, body[i:end])
	}
	return fmt.Sprintf("-----BEGIN %s-----\n%s\n-----END %s-----", label, strings.Join(lines, "\n"), label)
}

// MakeJWT H5/App 自签 JWT（HS256）：tenant 型用于 /api/ads；sub=slapp 型用于 jsonx 请求 uniID。
func (k Keys) MakeJWT(kind string) (string, error) {
	secret, err := need("SGCC_JWT_SECRET", k.JWTSecret)
	if err != nil {
		return "", err
	}
	now := time.Now().Unix()
	header := b64url([]byte(`{"alg":"HS256","typ":"JWT"}`))
	var payload string
	if kind == "tenant" {
		// 与 Node JSON.stringify 插入顺序逐字一致；不能让 map 按字母重排改变 uniID 原文。
		payload = b64url([]byte(fmt.Sprintf(`{"tenant":"newh5","jti":%q,"exp":%d,"iat":%d}`, newUUID(), now+120, now)))
	} else {
		payload = b64url([]byte(fmt.Sprintf(`{"sub":"slapp","jti":%q,"exp":%d}`, newUUID(), now+120)))
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(header + "." + payload))
	sig := b64url(mac.Sum(nil))
	return header + "." + payload + "." + sig, nil
}

// newUUID 简化 UUID v4（随机 16 字节置版本/变体位）。
func newUUID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// ---------- jsonm 通道（AES + SM2）----------

// jsonmEncryptBody 组装 jsonm 请求体 {data: AES(...), sign: SM2(key)}。
func (k Keys) jsonmEncryptBody(plainObj map[string]any, r int64) (string, error) {
	key, err := cryptoaudio.GenAESKey()
	if err != nil {
		return "", err
	}
	obj := map[string]any{}
	for k2, v := range plainObj {
		obj[k2] = v
	}
	obj["_r"] = r // Node 口径：{_r} 恒后置覆盖（JSON.stringify({...plainObj, _r: r})）
	var plain []byte
	if clientID, hasID := plainObj["adClientId"]; hasID && len(obj) == 3 {
		// ads 明文字段顺序与 Node 完全一致，避免 map 把 _r 提前。
		plain = []byte(fmt.Sprintf(`{"adClientId":%q,"adSecret":%q,"_r":%d}`, clientID, plainObj["adSecret"], r))
	} else {
		plain, _ = json.Marshal(obj)
	}
	ct, err := cryptoaudio.AESEncryptECBB64(string(plain), key)
	if err != nil {
		return "", err
	}
	pub, err := need("SGCC_SM2_SERVER_PUB", k.SM2ServerPub)
	if err != nil {
		return "", err
	}
	sign, err := cryptoaudio.SM2EncryptHex(key, pub)
	if err != nil {
		return "", err
	}
	body := map[string]any{"data": ct, "sign": sign}
	out, _ := json.Marshal(body)
	return string(out), nil
}

// jsonmDecryptBody 解 jsonm 响应体 {data, sign}。
func (k Keys) jsonmDecryptBody(raw string) (map[string]any, error) {
	var j struct {
		Data string `json:"data"`
		Sign string `json:"sign"`
	}
	if err := json.Unmarshal([]byte(raw), &j); err != nil {
		return nil, err
	}
	priv, err := need("SGCC_SM2_CLIENT_PRIV", k.SM2ClientPriv)
	if err != nil {
		return nil, err
	}
	keyHex := strings.TrimPrefix(j.Sign, "04")
	key, err := cryptoaudio.SM2DecryptHex(keyHex, priv)
	if err != nil {
		return nil, err
	}
	plain, err := cryptoaudio.AESDecryptECBB64(j.Data, key)
	if err != nil {
		return nil, err
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(plain), &out); err != nil {
		return nil, err
	}
	return out, nil
}

// MakeSignHeaderM jsonm sign 头：非空键逗号连接 + ';' + MD5(值串联 + r)。
func MakeSignHeaderM(headers map[string]string, r int64) string {
	order := []string{"token", "uniID", "tenant", "funcCode", "systemCode"}
	keys := ""
	vals := ""
	for _, k := range order {
		v := headers[k]
		if v != "" {
			if keys != "" {
				keys += ","
			}
			keys += k
			vals += v
		}
	}
	if keys != "" {
		keys += ";"
	}
	sum := md5.Sum([]byte(vals + itoa(r)))
	return keys + hex.EncodeToString(sum[:])
}

// MakeSignHeaderX jsonx sign 头：keyList「token,uniID,tenant;」+ md5(值串联 + r)。
func MakeSignHeaderX(headers map[string]string, r int64) string {
	keyList := ""
	vals := ""
	for _, pair := range [][2]string{{"token", ","}, {"uniID", ","}, {"tenant", ";"}} {
		k, sep := pair[0], pair[1]
		v := headers[k]
		if v != "" && v != "null" {
			keyList += k + sep
			vals += v
		}
	}
	sum := md5.Sum([]byte(vals + itoa(r)))
	return keyList + hex.EncodeToString(sum[:])
}

// ---------- jsonx 通道（RSA 分块）----------

// jsonxEncryptBody 组装 jsonx 请求体（RSA 分块密文）。
func (k Keys) jsonxEncryptBody(tenant string, plainObj map[string]any, r int64) (string, error) {
	obj := map[string]any{}
	for k2, v := range plainObj {
		obj[k2] = v
	}
	obj["_r"] = r // Node 口径：{_r} 恒后置覆盖
	plain, _ := json.Marshal(obj)
	name := "SGCC_RSA_PUB"
	pubIn := k.RSAPub
	if tenant == "slapp" {
		name = "SGCC_DCU_PUB"
		pubIn = k.DCUPub
	}
	raw, err := need(name, pubIn)
	if err != nil {
		return "", err
	}
	return cryptoaudio.RSAEncryptLongPEM([]byte(ToPEM(raw, "PUBLIC KEY")), plain)
}

// jsonxDecryptBody 解 jsonx 响应体（RSA 分块解密）。
func (k Keys) jsonxDecryptBody(tenant string, raw string) (map[string]any, error) {
	name := "SGCC_RSA_PRIV"
	privIn := k.RSAPriv
	if tenant == "slapp" {
		name = "SGCC_WLA_PRIV"
		privIn = k.WLAPriv
	}
	rawKey, err := need(name, privIn)
	if err != nil {
		return nil, err
	}
	plain, err := cryptoaudio.RSADecryptLongPEM([]byte(ToPEM(rawKey, "PRIVATE KEY")), raw)
	if err != nil {
		return nil, err
	}
	var out map[string]any
	if err := json.Unmarshal(plain, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// DeviceUuid 设备 uuid（请求头 uuid）：按手机号确定性派生，同一账号各链路一致。
func (k Keys) DeviceUuid(mobile string) string {
	if mobile == "" {
		mobile = "default"
	}
	sum := md5.Sum([]byte("sgcc-device:" + mobile + ":" + k.JWTSecret))
	h := hex.EncodeToString(sum[:])
	return strings.ToUpper(fmt.Sprintf("%s-%s-%s-%s-%s",
		h[0:8], h[8:12], h[12:16], h[16:20], h[20:32]))
}

// JSONXHeaders jsonx 请求头公共内核（业务调用与 multipart 上传同源）。
func (k Keys) JSONXHeaders(tenant, deviceType, systemVersion, mobile, contentType, token, uniID string) map[string]string {
	isSlapp := tenant == "slapp"
	platform := "android"
	if isSlapp {
		platform = "1"
	}
	headers := map[string]string{
		"Content-Type":  contentType,
		"Accept":        "*/*",
		"User-Agent":    fmt.Sprintf("guo wang shang lu yun/%s (%s; %s; Scale/2.00)", k.version(), deviceType, systemVersion),
		"source":        "1",
		"tenant":        tenant,
		"platform":      platform,
		"systemVersion": systemVersion,
		"deviceType":    deviceType,
		"appType":       "0",
		"grayversion":   k.grayVersion(),
		"version-code":  k.versionCode(),
		"uuid":          k.DeviceUuid(mobile),
		"encFlag":       "2",
		"version":       k.version(),
	}
	if uniID != "" {
		headers["uniID"] = uniID
	}
	if isSlapp {
		headers["secretKeyType"] = "2"
	}
	if token != "" {
		headers["token"] = token
	}
	return headers
}

// randR 协议随机数 _r：1000000000 ~ 9700000000。
func randR() int64 {
	var b [8]byte
	_, _ = rand.Read(b[:])
	var n uint64
	for _, c := range b {
		n = n<<8 | uint64(c)
	}
	return 1000000000 + int64(n%8700000000)
}

func itoa(v int64) string {
	if v == 0 {
		return "0"
	}
	neg := v < 0
	if neg {
		v = -v
	}
	var buf [20]byte
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
