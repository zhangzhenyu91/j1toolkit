package uvmpapi

// client.go — UVMP 业务网关请求客户端，逐字移植 vehicle_export.UvmpClient：
// 每次请求带 Auth_Tk / Refresh_Tk / X-Content-Token 三头，Accept-Encoding: identity
// （网关 deflate 错标间歇出现，已踩坑）；401/403 且 Token-Refresh:true 时经
// auth.Manager singleflight 刷新后重签重试一次；其余异常按 2s/4s/6s 退避重试共 3 次。

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/zhangzhenyu91/j1toolkit/uvmp-toolkit/core-go/internal/auth"
)

// webOrigin 官方前端源（Origin/Referer，vehicle_export.WEB_ORIGIN）。
const webOrigin = "http://uvmp.sgcc.com.cn"

// LogFunc 逐行日志回调（nil 丢弃）。
type LogFunc func(format string, args ...any)

// logf 安全调用日志回调。
func (f LogFunc) logf(format string, args ...any) {
	if f != nil {
		f(format, args...)
	}
}

// userAgent 浏览器 UA（vehicle_export.UvmpClient 固定头，逐字保留）。
const userAgent = "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/151.0.0.0 Safari/537.36"

// Client UVMP 业务网关客户端。零值不可用，用 NewClient 创建。并发安全。
type Client struct {
	gateway    string
	mgr        *auth.Manager
	hc         *http.Client
	retries    int
	logf       LogFunc
	retryDelay func(attempt int) time.Duration
	rnd        func() string // 测试注入固定 rnd
	nowMs      func() string // 测试注入固定时间戳
}

// Option Client 可选项。
type Option func(*Client)

// WithHTTPClient 自定义 HTTP 客户端（默认 60s 超时，对应 Python timeout=60）。
func WithHTTPClient(hc *http.Client) Option {
	return func(c *Client) { c.hc = hc }
}

// WithLogger 注入日志回调。
func WithLogger(f LogFunc) Option {
	return func(c *Client) { c.logf = f }
}

// WithRetries 重试次数（默认 3，同 vehicle_export.UvmpClient(retries=3)）。
func WithRetries(n int) Option {
	return func(c *Client) { c.retries = n }
}

// WithRetryDelay 自定义重试退避（默认 2*(attempt+1) 秒）。
func WithRetryDelay(f func(attempt int) time.Duration) Option {
	return func(c *Client) { c.retryDelay = f }
}

// NewClient 创建业务网关客户端；gateway 取自 mgr.Gateway()。
func NewClient(mgr *auth.Manager, opts ...Option) *Client {
	c := &Client{
		mgr:        mgr,
		gateway:    mgr.Gateway(),
		retries:    3,
		retryDelay: func(attempt int) time.Duration { return 2 * time.Duration(attempt+1) * time.Second },
		rnd:        rnd16,
		nowMs:      tsMs,
	}
	for _, o := range opts {
		o(c)
	}
	if c.hc == nil {
		c.hc = &http.Client{Timeout: 60 * time.Second}
	}
	return c
}

// Get GET 业务接口（vehicle_export.UvmpClient.get）。basePath 可自带 query；
// pData 为签名/发送用原始值（中文、空格不编码，发送时 quote_plus）。
func (c *Client) Get(ctx context.Context, basePath string, pData map[string]string, desc string) (map[string]any, error) {
	b := buildURL(c.gateway, basePath, pData, c.rnd(), c.nowMs())
	return c.do(ctx, http.MethodGet, b, nil, desc)
}

// Post POST 业务接口（vehicle_export.UvmpClient.post）：body 序列化为紧凑 JSON；
// 签名 new_data 即 body 原值（Python `str(v) if v else v` 口径在 MakeXContentToken
// 内转换），与 Python 一致不并入 base_path 自带 query。
func (c *Client) Post(ctx context.Context, basePath string, body map[string]any, desc string) (map[string]any, error) {
	b := buildURL(c.gateway, basePath, nil, c.rnd(), c.nowMs())
	b.newData = body
	return c.do(ctx, http.MethodPost, b, body, desc)
}

// do 请求主循环（vehicle_export.UvmpClient._request 逐字语义）：
// AuthError 立即抛出不重试；其余异常退避重试 retries 次；401/403 且
// Token-Refresh:true 先 singleflight 刷新再重签重试（每请求仅一次）。
func (c *Client) do(ctx context.Context, method string, b builtURL, body map[string]any, desc string) (map[string]any, error) {
	var lastErr error
	refreshed := false
	for attempt := 0; attempt < c.retries; attempt++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		snap := c.mgr.Snapshot()
		tokenHeader, err := MakeXContentToken(snap.Token, snap.Mtk, b.tempURL, b.newData)
		if err != nil {
			lastErr = err
			if werr := c.wait(ctx, attempt); werr != nil {
				return nil, werr
			}
			continue
		}
		var req *http.Request
		if method == http.MethodGet {
			req, err = http.NewRequestWithContext(ctx, http.MethodGet, b.url, nil)
		} else {
			payload, merr := marshalNoHTMLEscape(body)
			if merr != nil {
				return nil, merr
			}
			req, err = http.NewRequestWithContext(ctx, http.MethodPost, b.url, strings.NewReader(payload))
		}
		if err != nil {
			return nil, err
		}
		req.Header.Set("User-Agent", userAgent)
		req.Header.Set("Origin", webOrigin)
		req.Header.Set("Referer", webOrigin+"/")
		req.Header.Set("Accept-Encoding", "identity")
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Auth_Tk", snap.Token)
		req.Header.Set("Refresh_Tk", snap.RefreshToken)
		req.Header.Set("X-Content-Token", tokenHeader)

		resp, herr := c.hc.Do(req)
		if herr != nil {
			lastErr = herr
			c.logf.logf("  [重试 %d/%d] %s 失败: %v，%ds 后重试",
				attempt+1, c.retries, descOr(desc, b.url), herr, 2*(attempt+1))
			if werr := c.wait(ctx, attempt); werr != nil {
				return nil, werr
			}
			continue
		}
		raw, rerr := io.ReadAll(resp.Body)
		resp.Body.Close()
		if rerr != nil {
			lastErr = rerr
			if werr := c.wait(ctx, attempt); werr != nil {
				return nil, werr
			}
			continue
		}
		if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
			// token 过期时服务器会带 Token-Refresh: true 头，可自动换新 token 重试
			if !refreshed && strings.EqualFold(resp.Header.Get("Token-Refresh"), "true") {
				refreshed = true
				if _, rerr := c.mgr.Refresh(ctx, snap.Generation); rerr == nil {
					continue
				}
				if ctx.Err() != nil {
					return nil, ctx.Err()
				}
				// 刷新失败：落入下方凭证校验失败（同 Python raise AuthError）
			}
			return nil, &auth.AuthError{Msg: fmt.Sprintf(
				"凭证校验失败（HTTP %d）。Token-Refresh=%s；响应体：%s；请求URL：%s；计算的X-Content-Token：%s",
				resp.StatusCode, resp.Header.Get("Token-Refresh"), clipStr(raw, 200),
				clipStr([]byte(b.url), 160), clipStr([]byte(tokenHeader), 60))}
		}
		if resp.StatusCode != http.StatusOK {
			lastErr = fmt.Errorf("HTTP %d: %s", resp.StatusCode, clipStr(raw, 200))
			c.logf.logf("  [重试 %d/%d] %s 失败: %v，%ds 后重试",
				attempt+1, c.retries, descOr(desc, b.url), lastErr, 2*(attempt+1))
			if werr := c.wait(ctx, attempt); werr != nil {
				return nil, werr
			}
			continue
		}
		// UseNumber：JSON 数字保留为 json.Number，防大整数单据 ID 过 float64
		// 丢精度（如 1234567890123456789）；消费侧 strAny/dictInt 已兼容。
		data := map[string]any{}
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.UseNumber()
		if jerr := dec.Decode(&data); jerr != nil {
			lastErr = jerr
			if werr := c.wait(ctx, attempt); werr != nil {
				return nil, werr
			}
			continue
		}
		if ok, isBool := data["successful"].(bool); isBool && !ok {
			lastErr = fmt.Errorf("接口返回失败 %s: %s", descOr(desc, b.url), clipStr(raw, 300))
			if werr := c.wait(ctx, attempt); werr != nil {
				return nil, werr
			}
			continue
		}
		return data, nil
	}
	return nil, fmt.Errorf("请求失败（已重试 %d 次）%s: %w", c.retries, descOr(desc, b.url), lastErr)
}

// wait 重试退避（2*(attempt+1) 秒），可被 ctx 取消。
func (c *Client) wait(ctx context.Context, attempt int) error {
	d := c.retryDelay(attempt)
	if d <= 0 {
		return ctx.Err()
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// rnd16 16 位数字随机串（vehicle_export.rnd16）。
func rnd16() string {
	var sb strings.Builder
	for i := 0; i < 16; i++ {
		sb.WriteByte(byte('0' + rand.Intn(10)))
	}
	return sb.String()
}

// tsMs 毫秒时间戳（vehicle_export.ts_ms）。
func tsMs() string {
	return strconv.FormatInt(time.Now().UnixMilli(), 10)
}

// descOr 报错摘要：有 desc 用 desc，否则用 url。
func descOr(desc, u string) string {
	if desc != "" {
		return desc
	}
	return u
}

// clipStr 按 rune 截断（Python s[:n] 按字符口径，防 UTF-8 截裂）。
func clipStr(b []byte, n int) string {
	s := string(b)
	if n <= 0 {
		return ""
	}
	rs := []rune(s)
	if len(rs) > n {
		return string(rs[:n])
	}
	return s
}

// marshalNoHTMLEscape JSON 紧凑序列化，不做 HTML 转义
// （Python json.dumps ensure_ascii=False, separators=(",",":")）。
func marshalNoHTMLEscape(v any) (string, error) {
	var sb strings.Builder
	enc := json.NewEncoder(&sb)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return "", err
	}
	return strings.TrimRight(sb.String(), "\n"), nil
}
