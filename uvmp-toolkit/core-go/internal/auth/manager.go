package auth

// manager.go — 并发安全的凭证管理器：每批一次登录、凭证快照带 Generation、
// 401/403 时 singleflight 刷新（多协程并发只发一次 tokenRefresh）。
// 密码只保存在内存配置中，绝不写日志。

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"sync"
	"time"
)

// Manager 管理 UVMP 凭证生命周期。并发安全：Snapshot 任意时刻可读；
// Login 每批调用一次；Refresh 带 singleflight，同一时刻只有一个刷新在途。
type Manager struct {
	cfg  Config
	hc   *http.Client
	logf LogFunc

	mu         sync.Mutex
	cur        Credentials
	refreshing bool
	waitCh     chan struct{}
	refreshErr error
}

// Option Manager 可选项。
type Option func(*Manager)

// WithHTTPClient 自定义 HTTP 客户端（默认带 Cookie 会话、30s 超时）。
func WithHTTPClient(hc *http.Client) Option {
	return func(m *Manager) { m.hc = hc }
}

// WithLogger 注入日志回调（nil 丢弃）。
func WithLogger(f LogFunc) Option {
	return func(m *Manager) { m.logf = f }
}

// NewManager 创建凭证管理器。cfg 由 ParseConfig 解析。
func NewManager(cfg Config, opts ...Option) *Manager {
	m := &Manager{cfg: cfg}
	for _, o := range opts {
		o(m)
	}
	if m.hc == nil {
		jar, _ := cookiejar.New(nil)
		m.hc = &http.Client{Jar: jar, Timeout: 30 * time.Second}
	}
	return m
}

// Gateway 业务网关 base（结尾带 "/"）。
func (m *Manager) Gateway() string { return m.cfg.Gateway }

// Login 登录一次并落定凭证：sso 模式走 SSO 账号密码登录 + getUserAuth，
// token 模式直接采用 config.ini 手动三件套（对应 toolkit/uvmp.py make_client）。
// Generation 自增；密码不出现在任何日志。
func (m *Manager) Login(ctx context.Context) (Credentials, error) {
	if m.cfg.Mode == "token" {
		m.mu.Lock()
		defer m.mu.Unlock()
		m.cur = Credentials{
			Token:        m.cfg.Token,
			RefreshToken: m.cfg.RefreshToken,
			Mtk:          m.cfg.Mtk,
			LoginUser:    map[string]any{},
			Generation:   m.cur.Generation + 1,
		}
		return m.cur, nil
	}
	m.logf.logf("SSO 登录中（%s）...", m.cfg.Username)
	tJWT, err := SSOLogin(ctx, m.hc, m.cfg, m.logf)
	if err != nil {
		return Credentials{}, err
	}
	creds, err := GetUserAuth(ctx, m.hc, m.cfg.Gateway, tJWT, m.logf)
	if err != nil {
		return Credentials{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	creds.Generation = m.cur.Generation + 1
	m.cur = creds
	return m.cur, nil
}

// Snapshot 返回当前凭证快照（LoginUser 只读共享）。未登录时 Generation=0。
func (m *Manager) Snapshot() Credentials {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.cur
}

// Refresh 在 401/403（Token-Refresh:true）后刷新 token，对应前端 tokenRefresh 逻辑
// （vehicle_export.UvmpClient._refresh）。singleflight 语义：
//
//   - 当前 Generation != staleGen：已有协程刷新过，直接返回新快照，不再发请求；
//   - 否则本协程执行 tokenRefresh，其余协程等待同一结果；
//   - 刷新成功 Token 更新且 Generation+1；失败返回错误（凭证保持不变）。
func (m *Manager) Refresh(ctx context.Context, staleGen uint64) (Credentials, error) {
	m.mu.Lock()
	if m.cur.Generation != staleGen {
		cur := m.cur
		m.mu.Unlock()
		return cur, nil
	}
	if m.refreshing {
		ch := m.waitCh
		m.mu.Unlock()
		select {
		case <-ch:
			m.mu.Lock()
			cur, err := m.cur, m.refreshErr
			m.mu.Unlock()
			return cur, err
		case <-ctx.Done():
			return Credentials{}, ctx.Err()
		}
	}
	m.refreshing = true
	ch := make(chan struct{})
	m.waitCh = ch
	cur := m.cur
	m.mu.Unlock()

	newToken, err := m.tokenRefresh(ctx, cur)

	m.mu.Lock()
	if err == nil {
		m.cur.Token = newToken
		m.cur.Generation++
	}
	m.refreshErr = err
	m.refreshing = false
	close(ch)
	out := m.cur
	m.mu.Unlock()
	return out, err
}

// tokenRefresh 用 Refresh_Tk 换新 token（vehicle_export.UvmpClient._refresh 逐字语义）。
func (m *Manager) tokenRefresh(ctx context.Context, cur Credentials) (string, error) {
	q := url.Values{}
	q.Set("T", cur.Token)
	q.Set("R", cur.RefreshToken)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, m.cfg.Gateway+"tokenRefresh?"+q.Encode(), nil)
	if err != nil {
		return "", err
	}
	setCommonHeaders(req)
	resp, err := m.hc.Do(req)
	if err != nil {
		m.logf.logf("  [token 刷新异常: %v]", err)
		return "", err
	}
	raw, err := readBody(resp)
	if err != nil {
		m.logf.logf("  [token 刷新异常: %v]", err)
		return "", err
	}
	if resp.StatusCode != http.StatusOK {
		m.logf.logf("  [token 刷新异常: HTTP %d]", resp.StatusCode)
		return "", fmt.Errorf("tokenRefresh HTTP %d", resp.StatusCode)
	}
	data := map[string]any{}
	if err := json.Unmarshal(raw, &data); err != nil {
		m.logf.logf("  [token 刷新异常: %v]", err)
		return "", err
	}
	if b, _ := data["tokenRefreshExpired"].(bool); b {
		m.logf.logf("  [token 刷新失败：refresh 凭证也已过期]")
		return "", fmt.Errorf("token 刷新失败：refresh 凭证也已过期")
	}
	newToken, _ := data["accessToken"].(string)
	if newToken == "" {
		m.logf.logf("  [token 刷新失败：%s]", clip(raw, 150))
		return "", fmt.Errorf("token 刷新失败：%s", clip(raw, 150))
	}
	m.logf.logf("  [token 已自动刷新]")
	return newToken, nil
}

// randomNibble crypto/rand 取 4bit 随机数（前端 getUID/getRandomString 用）。
func randomNibble() byte {
	var b [1]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(fmt.Sprintf("auth: 系统随机源不可用: %v", err))
	}
	return b[0] & 0x0F
}

// randomBelow crypto/rand 取 [0, n) 均匀随机数（拒绝采样，Python secrets.randbelow 口径）。
func randomBelow(n uint32) uint32 {
	if n == 0 {
		return 0
	}
	limit := ^uint32(0) - (^uint32(0) % n)
	for {
		var b [4]byte
		if _, err := rand.Read(b[:]); err != nil {
			panic(fmt.Sprintf("auth: 系统随机源不可用: %v", err))
		}
		v := uint32(b[0])<<24 | uint32(b[1])<<16 | uint32(b[2])<<8 | uint32(b[3])
		if v < limit {
			return v % n
		}
	}
}

// randomHex16 生成 16 字节随机数的 32 位 hex（getUserAuth 的 K/V，secrets.token_hex(16) 等价）。
func randomHex16() string {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		panic(fmt.Sprintf("auth: 系统随机源不可用: %v", err))
	}
	return hex.EncodeToString(buf)
}
