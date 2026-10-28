package sgccclockin

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"os"
	"strings"
	"testing"
	"time"
)

// TestLiveSGCCProxyTransport 仅在显式设置 SGCC_LIVE_PROXY_URL 时运行，用于生产代理差异排障。
// 不读取数据库、不发送账号或加密业务数据，只验证 Go 协议传输能否取得网关 HTTP 响应；常规测试始终跳过。
func TestLiveSGCCProxyTransport(t *testing.T) {
	proxyURL := os.Getenv("SGCC_LIVE_PROXY_URL")
	if proxyURL == "" {
		t.Skip("未设置 SGCC_LIVE_PROXY_URL，跳过实网传输探测")
	}
	p := NewProtocol(Keys{})
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	status, _, err := p.post(ctx, proxyURL, p.hostH5(), "ads", map[string]string{
		"Content-Type": "application/jsonm",
	}, "{}")
	if err != nil {
		t.Fatalf("Go 协议传输失败: %v", err)
	}
	if status != 200 {
		t.Fatalf("网关 HTTP 状态异常: %d", status)
	}
}

// liveEnv 仅在用户显式指定旧版 env 文件时读取，字段只用于本次协议验证，不输出密钥原文。
func liveEnv(t *testing.T) map[string]string {
	t.Helper()
	path := os.Getenv("SGCC_LIVE_ENV")
	if path == "" {
		t.Skip("未设置 SGCC_LIVE_ENV，跳过真实协议握手")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取验证配置失败: %v", err)
	}
	values := map[string]string{}
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.SplitN(line, "=", 2)
		if len(parts) == 2 {
			values[parts[0]] = parts[1]
		}
	}
	return values
}

// TestLiveSGCCProtocolHandshake 用旧版密钥做两次真实 ads 初始化，覆盖加密请求与解密 uniID 列表。
// 不读取数据库、不发送短信、不尝试账号密码；常规测试始终跳过。
func TestLiveSGCCProtocolHandshake(t *testing.T) {
	v := liveEnv(t)
	p := NewProtocol(Keys{
		JWTSecret:     v["SGCC_JWT_SECRET"],
		AdSecret:      v["SGCC_AD_SECRET"],
		SM2ServerPub:  v["SGCC_SM2_SERVER_PUB"],
		SM2ClientPriv: v["SGCC_SM2_CLIENT_PRIV"],
		RSAPub:        v["SGCC_RSA_PUB"],
		RSAPriv:       v["SGCC_RSA_PRIV"],
		DCUPub:        v["SGCC_DCU_PUB"],
		WLAPriv:       v["SGCC_WLA_PRIV"],
		Version:       v["SGCC_VERSION"],
		VersionCode:   v["SGCC_VERSION_CODE"],
		GrayVersion:   v["SGCC_GRAY_VERSION"],
	})
	p.ProxyURL = v["SGCC_PROXY_URL"]
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if err := p.InitSession(ctx); err != nil {
		t.Fatalf("jsonm ads 初始化失败: %v", err)
	}
	if err := p.InitSessionX(ctx); err != nil {
		t.Fatalf("slapp ads 初始化失败: %v", err)
	}
	t.Logf("jsonm 与 slapp 协议握手成功（uniID 池 %d / %d）", len(p.poolM), len(p.poolX))
}

// TestLiveSGCCNodeReplay 将旧 Node 已成功的 ads 请求原样交给 Go 传输，隔离生成逻辑与 HTTP 行为。
func TestLiveSGCCNodeReplay(t *testing.T) {
	v := liveEnv(t)
	path := os.Getenv("SGCC_REPLAY_JSON")
	if path == "" {
		t.Skip("未设置 SGCC_REPLAY_JSON，跳过 Node 请求回放")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取 Node 回放请求失败: %v", err)
	}
	var captured struct {
		Host    string         `json:"host"`
		Headers map[string]any `json:"headers"`
		Body    string         `json:"body"`
	}
	if err := json.Unmarshal(raw, &captured); err != nil {
		t.Fatalf("解析 Node 回放请求失败: %v", err)
	}
	headers := map[string]string{}
	for k, val := range captured.Headers {
		headers[k] = str(val)
	}
	p := NewProtocol(Keys{SM2ClientPriv: v["SGCC_SM2_CLIENT_PRIV"]})
	p.ProxyURL = v["SGCC_PROXY_URL"]
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	status, response, err := p.post(ctx, p.ProxyURL, captured.Host, "ads", headers, captured.Body)
	if err != nil {
		t.Fatalf("Go 回放 Node 请求传输失败: %v", err)
	}
	decoded, err := p.Keys.jsonmDecryptBody(response)
	if err != nil {
		t.Fatalf("Go 回放响应解密失败（HTTP %d）: %v", status, err)
	}
	if status != 200 || len(extractList(decoded)) == 0 {
		t.Fatalf("Go 回放 Node 请求未获 uniID 列表: %s", adsFailureSummary(status, decoded))
	}
	t.Logf("Go 原样回放 Node 请求成功（uniID 池 %d）", len(extractList(decoded)))
}

// TestCaptureGoAds 捕获 Go 生成的 ads 请求供 Node 对照回放，不触真实网关。
func TestCaptureGoAds(t *testing.T) {
	v := liveEnv(t)
	path := os.Getenv("SGCC_CAPTURE_GO")
	if path == "" {
		t.Skip("未设置 SGCC_CAPTURE_GO，跳过 Go 请求捕获")
	}
	var captured struct {
		Host    string              `json:"host"`
		Headers map[string][]string `json:"headers"`
		Body    string              `json:"body"`
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		captured.Headers = r.Header.Clone()
		captured.Body = string(raw)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()
	p := NewProtocol(Keys{
		JWTSecret: v["SGCC_JWT_SECRET"], AdSecret: v["SGCC_AD_SECRET"],
		SM2ServerPub: v["SGCC_SM2_SERVER_PUB"], SM2ClientPriv: v["SGCC_SM2_CLIENT_PRIV"],
	})
	p.Scheme = "http"
	p.HostH5 = strings.TrimPrefix(srv.URL, "http://")
	_ = p.InitSession(context.Background())
	captured.Host = defaultHostH5
	raw, err := json.Marshal(captured)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatalf("写入 Go 回放请求失败: %v", err)
	}
	if captured.Body == "" {
		t.Fatal("未捕获到 Go ads 请求体")
	}
}

type liveRoundTripFunc func(*http.Request) (*http.Response, error)

func (f liveRoundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// TestCaptureGoWire 抓取 Go 实际请求行与 header，避免被服务端的规范化读取误导。
func TestCaptureGoWire(t *testing.T) {
	v := liveEnv(t)
	path := os.Getenv("SGCC_WIRE_OUT")
	if path == "" {
		t.Skip("未设置 SGCC_WIRE_OUT，跳过 Go 原始请求捕获")
	}
	p := NewProtocol(Keys{
		JWTSecret: v["SGCC_JWT_SECRET"], AdSecret: v["SGCC_AD_SECRET"],
		SM2ServerPub: v["SGCC_SM2_SERVER_PUB"], SM2ClientPriv: v["SGCC_SM2_CLIENT_PRIV"],
	})
	p.HTTP = &http.Client{Transport: liveRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		raw, err := httputil.DumpRequestOut(r, true)
		if err != nil {
			return nil, err
		}
		if err := os.WriteFile(path, raw, 0o600); err != nil {
			return nil, err
		}
		return &http.Response{StatusCode: 200, Status: "200 OK", Header: make(http.Header), Body: io.NopCloser(strings.NewReader("{}"))}, nil
	})}
	_ = p.InitSession(context.Background())
}
