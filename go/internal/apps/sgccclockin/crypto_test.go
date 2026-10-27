package sgccclockin

// 协议加解密测试：AES-128-ECB / SM2 / RSA 分块 / sign 头 / JWT / 设备 uuid。
// 全部本地密钥生成，不触真实商旅。

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"

	"shade/internal/compat/cryptoaudio"
)

func testKeys(t *testing.T) Keys {
	t.Helper()
	sm2Priv, sm2Pub, err := cryptoaudio.GenerateSM2KeyPairHex()
	if err != nil {
		t.Fatalf("生成 SM2 密钥: %v", err)
	}
	// 协议两对 RSA 密钥各自只管一个方向；测试生成两对
	rsaPubB64, rsaPrivB64 := genRSA(t)
	dcuPubB64, wlaPrivB64 := genRSA(t)
	return Keys{
		JWTSecret:     "test-jwt-secret",
		AdSecret:      "test-ad-secret",
		SM2ServerPub:  sm2Pub,
		SM2ClientPriv: sm2Priv,
		RSAPub:        rsaPubB64,
		RSAPriv:       rsaPrivB64,
		DCUPub:        dcuPubB64,
		WLAPriv:       wlaPrivB64,
	}
}

func genRSA(t *testing.T) (pubB64, privB64 string) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 1024)
	if err != nil {
		t.Fatalf("生成 RSA 密钥: %v", err)
	}
	pubDER, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		t.Fatalf("导出公钥: %v", err)
	}
	privDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatalf("导出私钥: %v", err)
	}
	return base64.StdEncoding.EncodeToString(pubDER), base64.StdEncoding.EncodeToString(privDER)
}

// TestJSONMBodyRoundtrip jsonm 请求体加解密往返。
func TestJSONMBodyRoundtrip(t *testing.T) {
	k := testKeys(t)
	body, err := k.jsonmEncryptBody(map[string]any{"mobile": "13800138000"}, 1234567890)
	if err != nil {
		t.Fatalf("加密: %v", err)
	}
	if !strings.Contains(body, `"data"`) || !strings.Contains(body, `"sign"`) {
		t.Fatalf("请求体缺 data/sign: %s", body)
	}
	if !strings.HasPrefix(mustSign(t, body), "04") {
		t.Fatalf("SM2 密文应带 04 前缀")
	}
	decoded, err := k.jsonmDecryptBody(mustSymmetric(t, k, body))
	if err != nil {
		t.Fatalf("解密: %v", err)
	}
	if decoded["mobile"] != "13800138000" {
		t.Fatalf("解密内容不符: %v", decoded)
	}
	if decoded["_r"] == nil {
		t.Fatalf("缺 _r 随机数")
	}
}

func mustSign(t *testing.T, body string) string {
	t.Helper()
	var j map[string]any
	if err := json.Unmarshal([]byte(body), &j); err != nil {
		t.Fatalf("解析请求体: %v", err)
	}
	s, _ := j["sign"].(string)
	return s
}

// mustSymmetric 把请求体原样交回解密（jsonmDecryptBody 只读 data/sign 字段）。
func mustSymmetric(t *testing.T, k Keys, body string) string {
	t.Helper()
	return body
}

// TestJSONXBodyRoundtrip jsonx（default/slapp）请求体加解密往返。
func TestJSONXBodyRoundtrip(t *testing.T) {
	k := testKeys(t)
	for _, tenant := range []string{"default", "slapp"} {
		ct, err := k.jsonxEncryptBody(tenant, map[string]any{"clockInDate": "2026-10-27"}, 1234567890)
		if err != nil {
			t.Fatalf("%s 加密: %v", tenant, err)
		}
		decoded, err := k.jsonxDecryptBody(tenant, ct)
		if err != nil {
			t.Fatalf("%s 解密: %v", tenant, err)
		}
		if decoded["clockInDate"] != "2026-10-27" {
			t.Fatalf("%s 解密内容不符: %v", tenant, decoded)
		}
	}
	// 跨租户解密应失败（不同密钥对）
	ct, err := k.jsonxEncryptBody("default", map[string]any{"x": 1}, 1)
	if err != nil {
		t.Fatalf("加密: %v", err)
	}
	if _, err := k.jsonxDecryptBody("slapp", ct); err == nil {
		t.Fatalf("slapp 私钥不应解开 default 密文")
	}
}

// TestSignHeaders sign 头口径：keyList + md5(值串联 + r)。
func TestSignHeaders(t *testing.T) {
	headers := map[string]string{"token": "tk", "uniID": "uid", "tenant": "newh5"}
	got := MakeSignHeaderM(headers, 42)
	if !strings.HasPrefix(got, "token,uniID,tenant;") {
		t.Fatalf("jsonm sign 头键序不符: %s", got)
	}
	if len(got) <= len("token,uniID,tenant;") {
		t.Fatalf("jsonm sign 头缺 md5: %s", got)
	}
	gotX := MakeSignHeaderX(map[string]string{"token": "tk", "uniID": "uid", "tenant": "slapp"}, 42)
	if !strings.HasPrefix(gotX, "token,uniID,tenant;") {
		t.Fatalf("jsonx sign 头键序不符: %s", gotX)
	}
	// 空 token 不参与
	gotNoTok := MakeSignHeaderM(map[string]string{"uniID": "uid", "tenant": "newh5"}, 42)
	if strings.HasPrefix(gotNoTok, "token") {
		t.Fatalf("空 token 不应入键表: %s", gotNoTok)
	}
}

// TestMakeJWT 自签 JWT 结构（HS256 三段）。
func TestMakeJWT(t *testing.T) {
	k := testKeys(t)
	token, err := k.MakeJWT("tenant")
	if err != nil {
		t.Fatalf("签发 JWT: %v", err)
	}
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		t.Fatalf("JWT 应为三段: %s", token)
	}
	header, _ := base64.RawURLEncoding.DecodeString(parts[0])
	if !strings.Contains(string(header), "HS256") {
		t.Fatalf("JWT 头不符: %s", header)
	}
	payload, _ := base64.RawURLEncoding.DecodeString(parts[1])
	if !strings.Contains(string(payload), "newh5") {
		t.Fatalf("tenant 型 payload 缺 tenant: %s", payload)
	}
	token2, _ := k.MakeJWT("slapp")
	payload2, _ := base64.RawURLEncoding.DecodeString(strings.Split(token2, ".")[1])
	if !strings.Contains(string(payload2), "slapp") {
		t.Fatalf("slapp 型 payload 缺 sub: %s", payload2)
	}
}

// TestDeviceUuid 同手机号确定性、不同手机号互异、UUID 形态。
func TestDeviceUuid(t *testing.T) {
	k := testKeys(t)
	u1 := k.DeviceUuid("13800138000")
	u2 := k.DeviceUuid("13800138000")
	u3 := k.DeviceUuid("13900139000")
	if u1 != u2 {
		t.Fatalf("同手机号 uuid 应确定性一致")
	}
	if u1 == u3 {
		t.Fatalf("不同手机号 uuid 应互异")
	}
	if len(u1) != 36 || strings.Count(u1, "-") != 4 {
		t.Fatalf("uuid 形态不符: %s", u1)
	}
}

// TestJSONXHeaders 请求头指纹（版本/租户/平台口径）。
func TestJSONXHeaders(t *testing.T) {
	k := testKeys(t)
	h := k.JSONXHeaders("slapp", "Pixel 7", "Android 13", "13800138000", "application/jsonx", "tk", "uid")
	if h["secretKeyType"] != "2" {
		t.Fatalf("slapp 缺 secretKeyType: %v", h)
	}
	if h["platform"] != "1" || h["tenant"] != "slapp" {
		t.Fatalf("slapp 平台/租户口径不符: %v", h)
	}
	if h["token"] != "tk" || h["uniID"] != "uid" {
		t.Fatalf("token/uniID 缺失: %v", h)
	}
	if h["version"] != "3.3.7" {
		t.Fatalf("默认版本不符: %v", h)
	}
	h2 := k.JSONXHeaders("default", "Pixel 7", "Android 13", "", "application/jsonx", "", "")
	if h2["platform"] != "android" || h2["secretKeyType"] != "" {
		t.Fatalf("default 口径不符: %v", h2)
	}
}
