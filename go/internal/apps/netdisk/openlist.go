package netdisk

import (
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// OpenList 客户端接口（可注入；HTTP 实现见 OpenListHTTP，测试用 httptest 假 OpenList）。
// 统一信封 {code,message,data}，成功 code=200（与主平台 code=0 不同）。
type OpenListClient interface {
	EnsureConfigured() error
	Call(ctx context.Context, method, apiPath string, opts CallOpts) (json.RawMessage, error)
	DownloadStream(ctx context.Context, absPath string) (io.ReadCloser, http.Header, error)
	EnsureDir(ctx context.Context, absPath string) error
	PutEmpty(ctx context.Context, absPath string) error
	FsPut(ctx context.Context, absPath string, body io.Reader, size int64) error
	ArchiveDownloadStream(ctx context.Context, absPath, innerPath, archivePass string) (io.ReadCloser, http.Header, error)
}

// CallOpts JSON 调用可选项。
type CallOpts struct {
	Headers map[string]string
	Body    any
	Query   map[string]string
	Timeout time.Duration // 0 = 60s；负值 = 不限
}

// UpstreamError 上游业务/网络错误（路由层 relayFail 归一）。
type UpstreamError struct {
	Exposable    bool // 可读文案是否可透传
	Status       int  // 映射 HTTP 状态
	Code         int  // 映射业务码
	UpstreamCode int  // OpenList 业务码（429/409 等分流用；网络层 0）
	Message      string
}

func (e *UpstreamError) Error() string { return e.Message }

// ErrNotConfigured 服务账户未配置。
var ErrNotConfigured = errors.New("团队网盘未配置：请在环境变量设置 NETDISK_USERNAME / NETDISK_PASSWORD（OpenList 服务账户）")

const (
	tokenTTL          = 47 * time.Hour // OpenList JWT 默认 48h，提前 1h 重登
	loginFailCooldown = time.Minute    // 登录失败冷却：凭证错误期间不再向上游打错误凭证
)

// OpenListHTTP 基于 OpenList API 的默认实现（fs / multipart / 下载接口封装）。
// 连接配置读取顺序：Cfg 运行时配置（NETDISK_API_URL / NETDISK_USERNAME / NETDISK_PASSWORD，
// 库内更新即生效、凭证变化强制重登）非空优先，回退静态字段（测试/旧装配口径）。
type OpenListHTTP struct {
	BaseURL  string // 静态回退：OpenList API 基址（http://127.0.0.1:5244）
	Username string // 静态回退：服务账户
	Password string // 静态回退：服务密码
	Cfg      ConfigReader
	HTTP     *http.Client

	mu      sync.Mutex
	token   string
	tokenAt time.Time
	tokenID string // 登录时生效的凭证指纹；配置热更新后强制重登
	failAt  time.Time
	failErr error
}

// effective 生效连接配置：运行时配置非空优先（热更新），否则回退静态字段。
func (c *OpenListHTTP) effective(ctx context.Context) (baseURL, username, password string) {
	baseURL, username, password = c.BaseURL, c.Username, c.Password
	if c.Cfg == nil {
		return
	}
	if v, err := c.Cfg.Get(ctx, "NETDISK_API_URL"); err == nil && strings.TrimSpace(v) != "" {
		baseURL = strings.TrimSpace(v)
	}
	if v, err := c.Cfg.Get(ctx, "NETDISK_USERNAME"); err == nil && strings.TrimSpace(v) != "" {
		username = strings.TrimSpace(v)
	}
	if v, err := c.Cfg.Get(ctx, "NETDISK_PASSWORD"); err == nil && v != "" {
		password = v
	}
	return
}

// credsID 凭证指纹（BaseURL+账户+密码）：热更新后与缓存 token 不匹配即重登。
func credsID(baseURL, username, password string) string {
	h := sha1.Sum([]byte(baseURL + "\x00" + username + "\x00" + password))
	return hex.EncodeToString(h[:])
}

func (c *OpenListHTTP) httpc() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return &http.Client{Timeout: 60 * time.Second}
}

// EnsureConfigured 未配置服务账户时抛可读错误（路由层映射 50301）。
func (c *OpenListHTTP) EnsureConfigured() error {
	_, username, password := c.effective(context.Background())
	if username == "" || password == "" {
		return ErrNotConfigured
	}
	return nil
}

func (c *OpenListHTTP) login(ctx context.Context) (string, error) {
	baseURL, username, password := c.effective(ctx)
	if username == "" || password == "" {
		return "", ErrNotConfigured
	}
	id := credsID(baseURL, username, password)
	c.mu.Lock()
	if c.failErr != nil && time.Since(c.failAt) < loginFailCooldown {
		err := c.failErr
		c.mu.Unlock()
		return "", err
	}
	c.mu.Unlock()

	payload, _ := json.Marshal(map[string]string{"username": username, "password": password})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		strings.TrimRight(baseURL, "/")+"/api/auth/login", bytes.NewReader(payload))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := c.httpc().Do(req)
	if err != nil {
		e := &UpstreamError{Exposable: false, Status: 502, Code: codeUpstream, UpstreamCode: 0, Message: "网盘服务异常，请稍后再试"}
		c.mu.Lock()
		c.failAt, c.failErr = time.Now(), e
		c.mu.Unlock()
		return "", e
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	var body struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
		Data    struct {
			Token string `json:"token"`
		} `json:"data"`
	}
	_ = json.Unmarshal(raw, &body)
	if body.Code != 200 || body.Data.Token == "" {
		e := &UpstreamError{Exposable: true, Status: 502, Code: codeUpstream, UpstreamCode: body.Code,
			Message: "OpenList 登录失败：" + firstNonEmpty(body.Message, fmt.Sprintf("HTTP %d", res.StatusCode))}
		c.mu.Lock()
		c.failAt, c.failErr = time.Now(), e
		c.mu.Unlock()
		return "", e
	}
	c.mu.Lock()
	c.token, c.tokenAt, c.tokenID, c.failErr = body.Data.Token, time.Now(), id, nil
	c.mu.Unlock()
	return body.Data.Token, nil
}

func (c *OpenListHTTP) tokenIfFresh(ctx context.Context, force bool) (string, error) {
	baseURL, username, password := c.effective(ctx)
	id := credsID(baseURL, username, password)
	c.mu.Lock()
	tok, at, used := c.token, c.tokenAt, c.tokenID
	c.mu.Unlock()
	if force || tok == "" || used != id || time.Since(at) > tokenTTL {
		return c.login(ctx)
	}
	return tok, nil
}

// Call JSON 调用：解 OpenList 信封；401 强制重登重试一次；上游业务码挂 UpstreamCode。
func (c *OpenListHTTP) Call(ctx context.Context, method, apiPath string, opts CallOpts) (json.RawMessage, error) {
	return c.call(ctx, method, apiPath, opts, false)
}

func (c *OpenListHTTP) call(ctx context.Context, method, apiPath string, opts CallOpts, retried bool) (json.RawMessage, error) {
	tok, err := c.tokenIfFresh(ctx, false)
	if err != nil {
		return nil, err
	}
	var body io.Reader
	if opts.Body != nil {
		switch v := opts.Body.(type) {
		case string:
			body = strings.NewReader(v)
		case []byte:
			body = bytes.NewReader(v)
		default:
			buf, _ := json.Marshal(opts.Body)
			body = bytes.NewReader(buf)
		}
	}
	baseURL, _, _ := c.effective(ctx)
	u := strings.TrimRight(baseURL, "/") + apiPath
	if len(opts.Query) > 0 {
		q := url.Values{}
		for k, v := range opts.Query {
			q.Set(k, v)
		}
		u += "?" + q.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, method, u, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", tok)
	if opts.Body != nil {
		if _, isStr := opts.Body.(string); !isStr {
			if _, isBytes := opts.Body.([]byte); !isStr && !isBytes {
				req.Header.Set("Content-Type", "application/json")
			}
		}
	}
	for k, v := range opts.Headers {
		req.Header.Set(k, v)
	}
	client := c.httpc()
	timeout := 60 * time.Second
	if opts.Timeout > 0 {
		timeout = opts.Timeout
	}
	var res *http.Response
	if opts.Timeout < 0 {
		noTimeout := *client
		noTimeout.Timeout = 0
		res, err = noTimeout.Do(req)
	} else {
		ctx2, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()
		res, err = client.Do(req.WithContext(ctx2))
	}
	if err != nil {
		return nil, &UpstreamError{Exposable: false, Status: 502, Code: codeUpstream, UpstreamCode: 0, Message: "网盘服务异常，请稍后再试"}
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(res.Body, 64<<20))
	if res.StatusCode == http.StatusUnauthorized && !retried {
		if _, err := c.tokenIfFresh(ctx, true); err != nil {
			return nil, err
		}
		return c.call(ctx, method, apiPath, opts, true)
	}
	var env struct {
		Code    int             `json:"code"`
		Message string          `json:"message"`
		Data    json.RawMessage `json:"data"`
	}
	_ = json.Unmarshal(raw, &env)
	if res.StatusCode == http.StatusUnauthorized || env.Code == 401 {
		return nil, &UpstreamError{Exposable: true, Status: 502, Code: codeUpstream, UpstreamCode: 401,
			Message: "OpenList 登录态失效，重登后仍未通过"}
	}
	if env.Code != 200 {
		upCode := firstNonZero(env.Code, res.StatusCode)
		return nil, &UpstreamError{Exposable: true, Status: 502, Code: codeUpstream, UpstreamCode: upCode,
			Message: firstNonEmpty(env.Message, fmt.Sprintf("OpenList 返回异常（%d）", upCode))}
	}
	return env.Data, nil
}

// DownloadStream 流式下载：/api/fs/get 取 sign，再 GET /d<path>?sign=（驱动直链 302 自动跟随）。
func (c *OpenListHTTP) DownloadStream(ctx context.Context, absPath string) (io.ReadCloser, http.Header, error) {
	data, err := c.Call(ctx, http.MethodPost, "/api/fs/get", CallOpts{Body: map[string]any{"path": absPath, "password": ""}})
	if err != nil {
		return nil, nil, err
	}
	var info struct {
		Sign string `json:"sign"`
	}
	_ = json.Unmarshal(data, &info)
	tok, err := c.tokenIfFresh(ctx, false)
	if err != nil {
		return nil, nil, err
	}
	baseURL, _, _ := c.effective(ctx)
	u := strings.TrimRight(baseURL, "/") + "/d" + encodePathSegs(absPath)
	if info.Sign != "" {
		u += "?sign=" + url.QueryEscape(info.Sign)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, nil, err
	}
	req.Header.Set("Authorization", tok)
	noTimeout := *c.httpc()
	noTimeout.Timeout = 0 // 大文件经两跳转发耗时不可预估
	res, err := noTimeout.Do(req)
	if err != nil {
		return nil, nil, &UpstreamError{Exposable: false, Status: 502, Code: codeUpstream, Message: "网盘服务异常，请稍后再试"}
	}
	if res.StatusCode >= 300 {
		_ = res.Body.Close()
		return nil, nil, &UpstreamError{Exposable: true, Status: 502, Code: codeUpstream,
			Message: fmt.Sprintf("OpenList 返回异常（%d）", res.StatusCode)}
	}
	return res.Body, res.Header, nil
}

// ArchiveDownloadStream 包内文件下载：先 meta 取 sign，再 GET /ad<压缩包路径>?inner=&sign=。
func (c *OpenListHTTP) ArchiveDownloadStream(ctx context.Context, absPath, innerPath, archivePass string) (io.ReadCloser, http.Header, error) {
	data, err := c.Call(ctx, http.MethodPost, "/api/fs/archive/meta", CallOpts{Body: map[string]any{
		"path": absPath, "password": "", "refresh": false, "archive_pass": archivePass,
	}})
	if err != nil {
		return nil, nil, err
	}
	var meta struct {
		Sign string `json:"sign"`
	}
	_ = json.Unmarshal(data, &meta)
	tok, err := c.tokenIfFresh(ctx, false)
	if err != nil {
		return nil, nil, err
	}
	baseURL, _, _ := c.effective(ctx)
	u := strings.TrimRight(baseURL, "/") + "/ad" + encodePathSegs(absPath) +
		"?inner=" + url.QueryEscape(innerPath)
	if meta.Sign != "" {
		u += "&sign=" + url.QueryEscape(meta.Sign)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, nil, err
	}
	req.Header.Set("Authorization", tok)
	noTimeout := *c.httpc()
	noTimeout.Timeout = 0
	res, err := noTimeout.Do(req)
	if err != nil {
		return nil, nil, &UpstreamError{Exposable: false, Status: 502, Code: codeUpstream, Message: "网盘服务异常，请稍后再试"}
	}
	if res.StatusCode >= 300 {
		_ = res.Body.Close()
		return nil, nil, &UpstreamError{Exposable: true, Status: 502, Code: codeUpstream,
			Message: fmt.Sprintf("OpenList 返回异常（%d）", res.StatusCode)}
	}
	return res.Body, res.Header, nil
}

// EnsureDir 逐级建目录（已存在/失败均忽略，用于个人空间与公共区根目录自愈）。
func (c *OpenListHTTP) EnsureDir(ctx context.Context, absPath string) error {
	segs := strings.Split(absPath, "/")
	cur := ""
	for _, seg := range segs {
		if seg == "" {
			continue
		}
		cur += "/" + seg
		_, _ = c.Call(ctx, http.MethodPost, "/api/fs/mkdir", CallOpts{Body: map[string]any{"path": cur}})
	}
	return nil
}

// PutEmpty 空文件：multipart 不接受 0 字节，走 /api/fs/put 直传空体。
func (c *OpenListHTTP) PutEmpty(ctx context.Context, absPath string) error {
	_, err := c.Call(ctx, http.MethodPut, "/api/fs/put", CallOpts{
		Headers: map[string]string{"File-Path": encodeURIPath(absPath), "Content-Length": "0"},
		Body:    "",
		Timeout: -1,
	})
	return err
}

// FsPut 直传（服务端侧已有文件内容，无需分片）。
func (c *OpenListHTTP) FsPut(ctx context.Context, absPath string, body io.Reader, size int64) error {
	tok, err := c.tokenIfFresh(ctx, false)
	if err != nil {
		return err
	}
	baseURL, _, _ := c.effective(ctx)
	req, err := http.NewRequestWithContext(ctx, http.MethodPut,
		strings.TrimRight(baseURL, "/")+"/api/fs/put", body)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", tok)
	req.Header.Set("File-Path", encodeURIPath(absPath))
	req.Header.Set("Content-Type", "application/octet-stream")
	if size > 0 {
		req.ContentLength = size
	}
	noTimeout := *c.httpc()
	noTimeout.Timeout = 0
	res, err := noTimeout.Do(req)
	if err != nil {
		return &UpstreamError{Exposable: false, Status: 502, Code: codeUpstream, Message: "网盘服务异常，请稍后再试"}
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	var env struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	}
	_ = json.Unmarshal(raw, &env)
	if res.StatusCode == http.StatusUnauthorized || env.Code == 401 {
		if _, err := c.tokenIfFresh(ctx, true); err != nil {
			return err
		}
		return &UpstreamError{Exposable: true, Status: 502, Code: codeUpstream, UpstreamCode: 401,
			Message: "OpenList 登录态失效，请重试"}
	}
	if env.Code != 200 {
		return &UpstreamError{Exposable: true, Status: 502, Code: codeUpstream, UpstreamCode: env.Code,
			Message: firstNonEmpty(env.Message, "OpenList 返回异常")}
	}
	return nil
}

// encodePathSegs 路径按段 URL 编码（保留 / 分隔）。
func encodePathSegs(p string) string {
	segs := strings.Split(p, "/")
	for i, seg := range segs {
		segs[i] = url.PathEscape(seg)
	}
	return strings.Join(segs, "/")
}

// encodeURIPath File-Path 头：整路径做 URI 编码（Node encodeURI 口径，保留 /）。
func encodeURIPath(p string) string {
	var b strings.Builder
	for _, r := range p {
		if r == '/' || r == '-' || r == '_' || r == '.' || r == '~' ||
			(r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		} else {
			for _, by := range []byte(string(r)) {
				fmt.Fprintf(&b, "%%%02X", by)
			}
		}
	}
	return b.String()
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

func firstNonZero(vals ...int) int {
	for _, v := range vals {
		if v != 0 {
			return v
		}
	}
	return 0
}

// AtoiLenient 宽松整数解析（分片序号等）。
func AtoiLenient(s string) (int, bool) {
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil {
		return 0, false
	}
	return n, true
}
