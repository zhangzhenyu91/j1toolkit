package auth

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

const testSM2Pub = GDKey // 登录页 encryptKey 用同格式公钥（只验流程，不解密）

// TestSSOLoginFlow 端到端 SSO 流程（httptest 假 SSO）：登录页 → preLogin/check →
// login 302 链 → 提取 T；断言密码以 SM2 DER 密文出现（绝不明文）、三元组可验签。
func TestSSOLoginFlow(t *testing.T) {
	const (
		testPwd = "Test@密码123"
		wantT   = "eyJhbGciOiJIUzI1NiJ9.abc-def_123"
	)
	var gotUserTicket, gotEncPwd string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/login":
			fmt.Fprintf(w, `<html><body>
<input type="hidden" name="execution" value="e1sX"/>
<script>aostaritEncryptUtils.init({encryptKey:"%s"})</script>
</body></html>`, testSM2Pub)
		case r.URL.Path == "/preLogin/check":
			q := r.URL.Query()
			rt, nonce, sign := q.Get("requestTime"), q.Get("nonce"), q.Get("sign")
			if rt == "" || nonce == "" || sign != sm3Hex([]byte(rt+","+nonce)) {
				t.Errorf("preLogin 三元组验签失败: rt=%s nonce=%s sign=%s", rt, nonce, sign)
			}
			body := map[string]any{}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("preLogin body 解析失败: %v", err)
			}
			for _, k := range []string{"appId", "authMode", "provinceId", "wangsheng", "username",
				"password", "execution", "_eventId", "requestTime", "nonce", "sign", "redirectUri"} {
				if _, ok := body[k]; !ok {
					t.Errorf("preLogin body 缺字段 %s", k)
				}
			}
			if body["appId"] != SSOAppID || body["authMode"] != "ACCOUNT_PASSWORD_SGCC" {
				t.Errorf("preLogin body 参数错: %v", body)
			}
			if body["execution"] != "e1sX" {
				t.Errorf("execution 未取到页面值: %v", body["execution"])
			}
			enc, _ := body["password"].(string)
			gotEncPwd = enc
			if enc == "" || strings.Contains(enc, testPwd) || !strings.HasPrefix(enc, "30") {
				t.Errorf("password 未按 SM2 DER 密文提交: %q", enc)
			}
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"data":{"userTicket":"UT-12345"}}`)
		case r.Method == http.MethodPost && r.URL.Path == "/login":
			if err := r.ParseForm(); err != nil {
				t.Errorf("login form 解析失败: %v", err)
			}
			gotUserTicket = r.Form.Get("userTicket")
			if gotUserTicket != "UT-12345" {
				t.Errorf("userTicket 未带上: %q", gotUserTicket)
			}
			if r.Form.Get("password") != gotEncPwd {
				t.Errorf("login 与 preLogin 密文不一致")
			}
			w.Header().Set("Location", srvURL(r)+"/authLogin?ticket=ST-1")
			w.WriteHeader(http.StatusFound)
		case r.URL.Path == "/authLogin":
			w.Header().Set("Location", srvURL(r)+"/uvmp-web/index.html#home?T="+wantT)
			w.WriteHeader(http.StatusFound)
		case r.URL.Path == "/uvmp-web/index.html":
			w.WriteHeader(http.StatusOK)
		default:
			t.Errorf("意外请求: %s %s", r.Method, r.URL)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	hc := &http.Client{Timeout: 5 * time.Second}
	cfg := Config{
		Username: "zhang",
		Password: testPwd,
		SSO:      SSOConfig{Base: srv.URL, ProvinceID: "sx", WangSheng: "sx"},
	}
	got, err := SSOLogin(context.Background(), hc, cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got != wantT {
		t.Fatalf("T = %s, want %s", got, wantT)
	}
}

// TestSSOLoginNoTicket preLogin/check 未返回 userTicket 应报认证错误（含原文摘要）。
func TestSSOLoginNoTicket(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/login" && r.Method == http.MethodGet {
			fmt.Fprintf(w, `encryptKey:"%s" name="execution" value="e1s1"`, testSM2Pub)
			return
		}
		fmt.Fprint(w, `{"code":"NO_TICKET"}`)
	}))
	defer srv.Close()
	cfg := Config{
		Username: "zhang",
		Password: "pwd",
		SSO:      SSOConfig{Base: srv.URL, ProvinceID: "sx", WangSheng: "sx"},
	}
	_, err := SSOLogin(context.Background(), &http.Client{Timeout: 5 * time.Second}, cfg, nil)
	if !IsAuthError(err) {
		t.Fatalf("err = %v, want AuthError", err)
	}
	if !strings.Contains(err.Error(), "NO_TICKET") {
		t.Fatalf("错误未带响应原文: %v", err)
	}
}

// TestExtractLoginPage 登录页解析（vehicle_export._extract_login_page）。
func TestExtractLoginPage(t *testing.T) {
	exec, pub := extractLoginPage(`<input name="execution" value="e1s9"/> init({encryptKey:"` + testSM2Pub + `"})`)
	if exec != "e1s9" {
		t.Fatalf("execution = %s", exec)
	}
	if pub != testSM2Pub {
		t.Fatalf("pubkey = %s", pub)
	}
	exec, pub = extractLoginPage(`<html></html>`)
	if exec != "e1s1" || pub != "" {
		t.Fatalf("缺省口径错: %q %q", exec, pub)
	}
}

// TestExtractUserTicket userTicket 提取路径（顶层/嵌套 data）。
func TestExtractUserTicket(t *testing.T) {
	if got := extractUserTicket([]byte(`"UT-1"`)); got != "UT-1" {
		t.Fatalf("顶层字符串: %q", got)
	}
	if got := extractUserTicket([]byte(`{"data":{"userTicket":"UT-2"}}`)); got != "UT-2" {
		t.Fatalf("嵌套 data: %q", got)
	}
	if got := extractUserTicket([]byte(`{"resultValue":{"ticket":"UT-3"}}`)); got != "UT-3" {
		t.Fatalf("resultValue.ticket: %q", got)
	}
	if got := extractUserTicket([]byte(`{"ut":"xx"}`)); got != "" {
		t.Fatalf("非 UT- 前缀不应取: %q", got)
	}
	if got := extractUserTicket([]byte(`not-json`)); got != "" {
		t.Fatalf("非 JSON 不应取: %q", got)
	}
}

// TestGetUserAuth getUserAuth 换证（httptest 假网关 + 固定 K/V + 测试侧 SM4 加密夹具）。
func TestGetUserAuth(t *testing.T) {
	kHex := "00112233445566778899aabbccddeeff"
	vHex := "ffeeddccbbaa99887766554433221100"
	key, _ := hex.DecodeString(kHex)
	iv, _ := hex.DecodeString(vHex)

	const plainJSON = `{"isLogin":true,"token":"tok-1","mtk":"aabbccdd","mtMode":1,` +
		`"loginUser":{"refreshToken":"rt-1","loginName":"zhang","defaultOrgCode":"ORG1"}}`
	cipherHex := hex.EncodeToString(sm4CBCEncryptForTest(t, []byte(JSQuote(plainJSON)), key, iv))

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/getUserAuth" {
			t.Errorf("意外路径: %s", r.URL.Path)
		}
		p := r.URL.Query().Get("p")
		if !strings.HasPrefix(p, "04") || len(p) < 200 {
			t.Errorf("p 参数形态错: %.40s…", p)
		}
		fmt.Fprintf(w, `{"p":%q}`, cipherHex)
	}))
	defer srv.Close()

	var logs []string
	logf := LogFunc(func(format string, args ...any) {
		logs = append(logs, fmt.Sprintf(format, args...))
	})
	creds, err := getUserAuthWithKV(context.Background(), srv.Client(), srv.URL+"/", "T-abc", kHex, vHex, logf)
	if err != nil {
		t.Fatal(err)
	}
	if creds.Token != "tok-1" || creds.Mtk != "aabbccdd" || creds.RefreshToken != "rt-1" {
		t.Fatalf("凭证错: %+v", creds)
	}
	if creds.LoginUser["loginName"] != "zhang" {
		t.Fatalf("loginUser 未透传: %v", creds.LoginUser)
	}
	joined := strings.Join(logs, "\n")
	if !strings.Contains(joined, "getUserAuth 响应字段:") || !strings.Contains(joined, "mtk(len=8)") {
		t.Fatalf("诊断日志缺失: %s", joined)
	}
	if !strings.Contains(joined, "登录成功：zhang（ORG1）") {
		t.Fatalf("登录成功日志缺失: %s", joined)
	}
}

// TestGetUserAuthNotLogin isLogin=false 报认证错误。
func TestGetUserAuthNotLogin(t *testing.T) {
	kHex := "00112233445566778899aabbccddeeff"
	vHex := "ffeeddccbbaa99887766554433221100"
	key, _ := hex.DecodeString(kHex)
	iv, _ := hex.DecodeString(vHex)
	plain := `{"isLogin":false,"token":"","mtk":""}`
	cipherHex := hex.EncodeToString(sm4CBCEncryptForTest(t, []byte(JSQuote(plain)), key, iv))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"p":%q}`, cipherHex)
	}))
	defer srv.Close()

	_, err := getUserAuthWithKV(context.Background(), srv.Client(), srv.URL+"/", "T", kHex, vHex, nil)
	if !IsAuthError(err) || !strings.Contains(err.Error(), "isLogin=false") {
		t.Fatalf("err = %v, want isLogin AuthError", err)
	}
}

// TestGetUserAuthTruthyIsLogin isLogin 按 Python 真值判定：兼容 bool/1/字符串等常见
// 形态（非空字符串恒为真，含 "false"，与 Python 逐字一致）；假值报认证错误。
func TestGetUserAuthTruthyIsLogin(t *testing.T) {
	kHex := "00112233445566778899aabbccddeeff"
	vHex := "ffeeddccbbaa99887766554433221100"
	key, _ := hex.DecodeString(kHex)
	iv, _ := hex.DecodeString(vHex)
	cases := []struct {
		isLoginJSON string // isLogin 字段的 JSON 原文
		wantLogin   bool
	}{
		{`true`, true},
		{`1`, true},
		{`"true"`, true},
		{`"1"`, true},
		{`"false"`, true}, // Python：非空字符串恒为真
		{`false`, false},
		{`0`, false},
		{`""`, false},
		{`null`, false},
	}
	for _, tc := range cases {
		plain := fmt.Sprintf(`{"isLogin":%s,"token":"tok-1","mtk":"aabbccdd",`+
			`"loginUser":{"refreshToken":"rt-1","loginName":"zhang","defaultOrgCode":"ORG1"}}`, tc.isLoginJSON)
		cipherHex := hex.EncodeToString(sm4CBCEncryptForTest(t, []byte(JSQuote(plain)), key, iv))
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprintf(w, `{"p":%q}`, cipherHex)
		}))
		creds, err := getUserAuthWithKV(context.Background(), srv.Client(), srv.URL+"/", "T", kHex, vHex, nil)
		srv.Close()
		if tc.wantLogin {
			if err != nil {
				t.Fatalf("isLogin=%s err = %v, want 成功", tc.isLoginJSON, err)
			}
			if creds.Token != "tok-1" {
				t.Fatalf("isLogin=%s token = %q", tc.isLoginJSON, creds.Token)
			}
		} else {
			if !IsAuthError(err) || !strings.Contains(err.Error(), "isLogin=false") {
				t.Fatalf("isLogin=%s err = %v, want isLogin AuthError", tc.isLoginJSON, err)
			}
		}
	}
}

// TestGetUserAuthMissingP 响应缺 p 字段报认证错误（含原文摘要）。
func TestGetUserAuthMissingP(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"oops":1}`)
	}))
	defer srv.Close()
	_, err := getUserAuthWithKV(context.Background(), srv.Client(), srv.URL+"/", "T",
		"00112233445566778899aabbccddeeff", "ffeeddccbbaa99887766554433221100", nil)
	if !IsAuthError(err) {
		t.Fatalf("err = %v, want AuthError", err)
	}
}

// TestEncryptSSOPassword 密码混淆结构：SM3(密码)64hex + 8 位随机串 + 密码，再 SM2/ASN.1。
func TestEncryptSSOPassword(t *testing.T) {
	enc, err := EncryptSSOPassword("Test@密码123", testSM2Pub)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(enc, "30") {
		t.Fatalf("非 DER hex: %.8s", enc)
	}
	if strings.Contains(enc, url.QueryEscape("Test@密码123")) {
		t.Fatal("密文中出现明文")
	}
}

// srvURL 从请求推断假服务根地址（httptest 反代场景下用 Host）。
func srvURL(r *http.Request) string {
	return "http://" + r.Host
}
