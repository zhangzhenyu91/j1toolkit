// GLKVM Cloud 平台 API 客户端（对齐 server/src/kvm/glkvm.js）：以「当前员工账号」代登录平台
// （用户名 = 壹匣登录账号，密码 = 服务端统一配置的 GLKVM_PASSWORD），代理拉取该员工可见的设备列表。
// GLKVM_URL / GLKVM_PASSWORD 支持运行时配置动态取值（Cfg 注入，空值回退固定缺省），配置变更即清会话缓存；
// 会话按员工缓存在内存中，过期（24h，提前 1h 过期）或失效（AUTH_REQUIRED/AUTH_EXPIRED）时自动重登；
// 信封约定：glkvm 接口 HTTP 恒 200，业务成败看 body.ok / body.code。
// 客户端可注入（测试用 httptest，不实连平台）。
package kvm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ConfigReader 运行时配置读取（*config.Store 天然满足；测试可注入桩）。
// GLKVM_URL / GLKVM_PASSWORD 每次请求动态读取，库内配置中心改值即生效。
type ConfigReader interface {
	Get(ctx context.Context, key string) (string, error)
}

// Platform 平台适配层（GLKVMClient 为真实实现；测试可注入桩）。
type Platform interface {
	// BaseURL 平台基址（jump.html 深链拼接用）。
	BaseURL() string
	// ListDevices 设备列表（q/status/groupId 过滤；可见范围随员工平台权限）。
	ListDevices(ctx context.Context, query url.Values, username string) ([]Device, error)
	// SessionToken 员工平台会话 token（登录并缓存）。
	SessionToken(ctx context.Context, username string) (string, error)
	// ProxySession 经平台 /web/ 链路换设备子域名代理会话（rttysid）。
	ProxySession(ctx context.Context, deviceID, username string) (ProxySession, error)
}

// Device 平台设备（保留原始字段透传，响应形状与平台 DTO 一致 + 维护锁标注）。
type Device struct {
	ID              int64          `json:"id"`
	Ddns            string         `json:"ddns"`
	DeviceGroupName string         `json:"deviceGroupName"`
	Raw             map[string]any `json:"-"` // 平台原始字段（响应透传用）
}

// proxySession 设备 fileshare 转发会话。
type ProxySession struct {
	Origin string // 形如 https://<ddns>.kvm.j1net.com
	Cookie string // rtty-http-sid=...
}

// GLKVMClient 平台客户端（HTTP 与运行时配置可注入）。
// URL/Password 为固定缺省值；Cfg 接入后按请求动态读 GLKVM_URL / GLKVM_PASSWORD（空值回退固定值），
// 生效配置变更时自动清空员工会话缓存（旧登录态不再可信）。
type GLKVMClient struct {
	URL      string       // GLKVM_URL 固定缺省（Cfg 未接入或取值为空时用）
	Password string       // GLKVM_PASSWORD 固定缺省（同上）
	Cfg      ConfigReader // 运行时配置（可注入；nil 用固定值）
	HTTP     *http.Client

	mu         sync.Mutex
	sessions   map[string]cachedSession // username → 会话
	logging    map[string]chan struct{} // 登录单例（按员工）
	activeURL  string                   // 最近一次生效的平台基址（BaseURL 动态回显）
	activePass string                   // 最近一次生效的密码（指纹变更即清会话缓存）
}

type cachedSession struct {
	token     string
	expiresAt time.Time
}

// NewGLKVMClient 构造平台客户端。
func NewGLKVMClient(baseURL, password string) *GLKVMClient {
	return &GLKVMClient{
		URL:      strings.TrimRight(strings.TrimSpace(baseURL), "/"),
		Password: password,
		HTTP:     &http.Client{Timeout: 15 * time.Second},
		sessions: map[string]cachedSession{},
		logging:  map[string]chan struct{}{},
	}
}

// 编译期确认 GLKVMClient 满足 Platform（可装配）。
var _ Platform = (*GLKVMClient)(nil)

// BaseURL 平台基址（Cfg 接入后回显最近一次动态解析结果）。
func (c *GLKVMClient) BaseURL() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.activeURL != "" {
		return c.activeURL
	}
	return c.URL
}

// resolveConfig 解析当前生效配置：Cfg（GLKVM_URL / GLKVM_PASSWORD）优先、空值/读取失败回退固定值；
// 与上次生效值不一致即清空员工会话缓存（配置变更后旧登录态作废，对齐「配置变更清缓存」口径）。
func (c *GLKVMClient) resolveConfig(ctx context.Context) (string, string) {
	url := strings.TrimRight(strings.TrimSpace(c.URL), "/")
	pass := c.Password
	if c.Cfg != nil {
		if v, err := c.Cfg.Get(ctx, "GLKVM_URL"); err == nil && strings.TrimSpace(v) != "" {
			url = strings.TrimRight(strings.TrimSpace(v), "/")
		}
		if v, err := c.Cfg.Get(ctx, "GLKVM_PASSWORD"); err == nil && v != "" {
			pass = v
		}
	}
	c.mu.Lock()
	if c.activeURL != url || c.activePass != pass {
		c.sessions = map[string]cachedSession{}
	}
	c.activeURL, c.activePass = url, pass
	c.mu.Unlock()
	return url, pass
}

// InvalidateSessions 清空员工平台会话缓存（配置中心 OnChange 监听处可显式调用；
// resolveConfig 亦按生效配置指纹自动清，双保险）。
func (c *GLKVMClient) InvalidateSessions() {
	c.mu.Lock()
	c.sessions = map[string]cachedSession{}
	c.mu.Unlock()
}

// ensureConfigured 未配置时抛可读业务错误。
func ensureConfigured(url, password string) error {
	if url == "" || password == "" {
		return bizErr("GLKVM Cloud 未配置（GLKVM_URL / GLKVM_PASSWORD）", http.StatusBadGateway, codePlatform)
	}
	return nil
}

// 会话有效期口径：平台默认 24h，提前 1h 主动过期重登。
const sessionTTLSec = 23 * 3600

// login 以员工账号登录平台并缓存会话（员工平台账号请勿开启 2FA）。
func (c *GLKVMClient) login(ctx context.Context, username string) (string, error) {
	baseURL, password := c.resolveConfig(ctx)
	if err := ensureConfigured(baseURL, password); err != nil {
		return "", err
	}
	payload, _ := json.Marshal(map[string]string{"username": username, "password": password})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/api/login", bytes.NewReader(payload))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return "", bizErr("GLKVM 平台连接失败："+err.Error(), http.StatusBadGateway, codePlatform)
	}
	defer resp.Body.Close()
	var body struct {
		OK   bool `json:"ok"`
		Data *struct {
			Token string `json:"token"`
		} `json:"data"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&body)
	if !body.OK || body.Data == nil || body.Data.Token == "" {
		return "", bizErr("KVM 平台登录失败：请确认你的账号已接入平台、未开启 2FA，且密码与统一密码一致", http.StatusBadGateway, codePlatform)
	}
	c.mu.Lock()
	c.sessions[username] = cachedSession{token: body.Data.Token, expiresAt: time.Now().Add(sessionTTLSec * time.Second)}
	c.mu.Unlock()
	return body.Data.Token, nil
}

// SessionToken 取员工平台会话 token（带登录单例：同一员工并发请求共享一次登录；
// 入口先解析动态配置，配置变更即清缓存后走重新登录）。
func (c *GLKVMClient) SessionToken(ctx context.Context, username string) (string, error) {
	c.resolveConfig(ctx)
	c.mu.Lock()
	if s, ok := c.sessions[username]; ok && time.Now().Before(s.expiresAt) {
		c.mu.Unlock()
		return s.token, nil
	}
	if ch, running := c.logging[username]; running {
		c.mu.Unlock()
		select {
		case <-ch:
		case <-ctx.Done():
			return "", ctx.Err()
		}
		c.mu.Lock()
		s, ok := c.sessions[username]
		c.mu.Unlock()
		if ok && time.Now().Before(s.expiresAt) {
			return s.token, nil
		}
		return c.login(ctx, username)
	}
	ch := make(chan struct{})
	c.logging[username] = ch
	c.mu.Unlock()
	token, err := c.login(ctx, username)
	c.mu.Lock()
	delete(c.logging, username)
	c.mu.Unlock()
	close(ch)
	return token, err
}

// clearSession 失效缓存（AUTH_REQUIRED/AUTH_EXPIRED 时调用）。
func (c *GLKVMClient) clearSession(username string) {
	c.mu.Lock()
	delete(c.sessions, username)
	c.mu.Unlock()
}

// envelope glkvm 接口统一响应体。
type envelope struct {
	OK      bool            `json:"ok"`
	Code    string          `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data"`
}

// callGlkvm 调 glkvm 接口；会话失效（AUTH_REQUIRED/AUTH_EXPIRED）时清缓存重登一次并重试。
func (c *GLKVMClient) callGlkvm(ctx context.Context, path string, params url.Values, username string) (json.RawMessage, error) {
	baseURL, _ := c.resolveConfig(ctx)
	token, err := c.SessionToken(ctx, username)
	if err != nil {
		return nil, err
	}
	for retry := 0; retry < 2; retry++ {
		target := baseURL + path
		if len(params) > 0 {
			target += "?" + params.Encode()
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err := c.HTTP.Do(req)
		if err != nil {
			return nil, bizErr("GLKVM 平台连接失败："+err.Error(), http.StatusBadGateway, codePlatform)
		}
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
		resp.Body.Close()
		var env envelope
		if err := json.Unmarshal(body, &env); err != nil {
			return nil, bizErr("GLKVM 平台返回异常", http.StatusBadGateway, codePlatform)
		}
		if env.OK {
			return env.Data, nil
		}
		if env.Code == "AUTH_REQUIRED" || env.Code == "AUTH_EXPIRED" {
			c.clearSession(username)
			token, err = c.SessionToken(ctx, username)
			if err != nil {
				return nil, err
			}
			continue
		}
		msg := env.Message
		if msg == "" {
			msg = env.Code
		}
		if msg == "" {
			msg = "未知错误"
		}
		return nil, bizErr("GLKVM 平台返回错误："+msg, http.StatusBadGateway, codePlatform)
	}
	return nil, bizErr("GLKVM 平台登录态刷新失败", http.StatusBadGateway, codePlatform)
}

// ListDevices 设备列表（不传 pageSize 平台即返回全部；status 仅接受 online/offline/disabled）。
func (c *GLKVMClient) ListDevices(ctx context.Context, query url.Values, username string) ([]Device, error) {
	params := url.Values{}
	if q := query.Get("q"); q != "" {
		runes := []rune(q)
		if len(runes) > 64 {
			q = string(runes[:64])
		}
		params.Set("q", q)
	}
	switch query.Get("status") {
	case "online", "offline", "disabled":
		params.Set("status", query.Get("status"))
	}
	if gid := query.Get("groupId"); regexp.MustCompile(`^\d+$`).MatchString(gid) {
		params.Set("groupId", gid)
	}
	data, err := c.callGlkvm(ctx, "/api/devices", params, username)
	if err != nil {
		return nil, err
	}
	var payload struct {
		Items []json.RawMessage `json:"items"`
	}
	_ = json.Unmarshal(data, &payload)
	out := make([]Device, 0, len(payload.Items))
	for _, raw := range payload.Items {
		var d Device
		_ = json.Unmarshal(raw, &d)
		_ = json.Unmarshal(raw, &d.Raw)
		if d.ID == 0 {
			// 平台 id 可能为字符串，宽松解析
			var m map[string]any
			if json.Unmarshal(raw, &m) == nil {
				switch v := m["id"].(type) {
				case string:
					d.ID, _ = strconv.ParseInt(v, 10, 64)
				case float64:
					d.ID = int64(v)
				}
				if s, ok := m["ddns"].(string); ok {
					d.Ddns = s
				}
				if s, ok := m["deviceGroupName"].(string); ok {
					d.DeviceGroupName = s
				}
			}
		}
		out = append(out, d)
	}
	return out, nil
}

// deviceByID 按平台设备 ID 取设备（/web/ 链路与设备子域名都以 ddns 寻址）。
func (c *GLKVMClient) deviceByID(ctx context.Context, deviceID, username string) (Device, error) {
	items, err := c.ListDevices(ctx, url.Values{}, username)
	if err != nil {
		return Device{}, err
	}
	id, _ := strconv.ParseInt(strings.TrimSpace(deviceID), 10, 64)
	for _, d := range items {
		if d.ID == id || (id == 0 && strings.TrimSpace(deviceID) == strconv.FormatInt(d.ID, 10)) {
			if d.Ddns == "" {
				return Device{}, bizErr("该设备缺少 ddns 标识", http.StatusBadRequest, codeParam)
			}
			return d, nil
		}
	}
	return Device{}, bizErr("设备不存在，或当前平台账号对其不可见", http.StatusNotFound, codePlatform)
}

var proxyLocationRe = regexp.MustCompile(`^(https?://[^/]+)/.*[?&]rttysid=([^&]+)`)

// ProxySession 经平台 /web/ 链路换设备子域名代理会话（rttysid）。
// /web/ 属平台 legacy authorized 组，只认 sid Cookie（不认 Bearer）。
func (c *GLKVMClient) ProxySession(ctx context.Context, deviceID, username string) (ProxySession, error) {
	dev, err := c.deviceByID(ctx, deviceID, username)
	if err != nil {
		return ProxySession{}, err
	}
	sid, err := c.SessionToken(ctx, username)
	if err != nil {
		return ProxySession{}, err
	}
	baseURL, _ := c.resolveConfig(ctx)
	for retry := 0; retry < 2; retry++ {
		target := fmt.Sprintf("%s/web/%s/https/%s", baseURL, url.PathEscape(dev.Ddns), url.PathEscape("127.0.0.1:443/"))
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
		if err != nil {
			return ProxySession{}, err
		}
		req.Header.Set("Cookie", "sid="+sid)
		// /web/ 链路只取 302 Location 换 rttysid，不跟随跳转（Node maxRedirects: 0 同口径）
		base := c.HTTP
		if base == nil {
			base = &http.Client{Timeout: 15 * time.Second}
		}
		noRedirect := &http.Client{
			Transport:     base.Transport,
			Timeout:       base.Timeout,
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		}
		resp, err := noRedirect.Do(req)
		if err != nil {
			return ProxySession{}, bizErr("GLKVM 平台连接失败："+err.Error(), http.StatusBadGateway, codePlatform)
		}
		resp.Body.Close()
		if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
			c.clearSession(username)
			sid, err = c.SessionToken(ctx, username)
			if err != nil {
				return ProxySession{}, err
			}
			continue
		}
		loc := resp.Header.Get("Location")
		if resp.StatusCode == http.StatusFound {
			if m := proxyLocationRe.FindStringSubmatch(loc); m != nil {
				return ProxySession{Origin: m[1], Cookie: "rtty-http-sid=" + m[2]}, nil
			}
		}
		return ProxySession{}, bizErr(fmt.Sprintf("平台未签发代理会话（HTTP %d），设备可能离线", resp.StatusCode), http.StatusBadGateway, codePlatform)
	}
	return ProxySession{}, bizErr("GLKVM 平台登录态刷新失败", http.StatusBadGateway, codePlatform)
}
