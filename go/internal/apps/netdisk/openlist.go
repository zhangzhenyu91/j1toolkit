package netdisk

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"shade/internal/integrations"
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

// olConn 一次操作解析出的连接配置（每操作只读一次运行时配置，避免重复读库）。
type olConn struct {
	baseURL  string
	username string
	password string
	id       string // 凭证指纹（热更新后与缓存 token 不匹配即重登）
}

// OpenListHTTP 基于 OpenList API 的默认实现（fs / multipart / 下载接口封装）。
// 连接配置读取顺序：Cfg 运行时配置（NETDISK_API_URL / NETDISK_USERNAME / NETDISK_PASSWORD，
// 库内更新即生效、凭证变化强制重登）非空优先，回退静态字段（测试/旧装配口径）。
type OpenListHTTP struct {
	BaseURL  string // 静态回退：OpenList API 基址（http://127.0.0.1:5244）
	Username string // 静态回退：服务账户
	Password string // 静态回退：服务密码
	Cfg      ConfigReader
	HTTP     *http.Client

	loginMu sync.Mutex // 登录单飞：并发首登只向上游发一次 login

	mu      sync.Mutex
	token   string
	tokenAt time.Time
	tokenID string // 登录时生效的凭证指纹；配置热更新后强制重登
	failAt  time.Time
	failErr error
}

// conn 生效连接配置（每操作解析一次）：运行时配置非空优先（热更新），否则回退静态字段。
func (c *OpenListHTTP) conn(ctx context.Context) olConn {
	baseURL, username, password := c.BaseURL, c.Username, c.Password
	if c.Cfg != nil {
		if v, err := c.Cfg.Get(ctx, "NETDISK_API_URL"); err == nil && strings.TrimSpace(v) != "" {
			baseURL = strings.TrimSpace(v)
		}
		if v, err := c.Cfg.Get(ctx, "NETDISK_USERNAME"); err == nil && strings.TrimSpace(v) != "" {
			username = strings.TrimSpace(v)
		}
		if v, err := c.Cfg.Get(ctx, "NETDISK_PASSWORD"); err == nil && v != "" {
			password = v
		}
	}
	return olConn{baseURL: baseURL, username: username, password: password, id: credsID(baseURL, username, password)}
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
	cr := c.conn(context.Background())
	if cr.username == "" || cr.password == "" {
		return ErrNotConfigured
	}
	return nil
}

// login 登录并缓存 token：loginMu 内双检（排队期间他人已完成登录即复用），
// 401 强制重登也接受「本次排队开始后」刷新出的新 token，避免并发重登风暴。
func (c *OpenListHTTP) login(ctx context.Context, cr olConn, force bool, since time.Time) (string, error) {
	if cr.username == "" || cr.password == "" {
		return "", ErrNotConfigured
	}
	c.loginMu.Lock()
	defer c.loginMu.Unlock()

	c.mu.Lock()
	if c.token != "" && c.tokenID == cr.id && time.Since(c.tokenAt) <= tokenTTL &&
		(!force || c.tokenAt.After(since)) {
		tok := c.token
		c.mu.Unlock()
		return tok, nil
	}
	if c.failErr != nil && time.Since(c.failAt) < loginFailCooldown {
		err := c.failErr
		c.mu.Unlock()
		return "", err
	}
	c.mu.Unlock()

	payload, _ := json.Marshal(map[string]string{"username": cr.username, "password": cr.password})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		strings.TrimRight(cr.baseURL, "/")+"/api/auth/login", bytes.NewReader(payload))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := c.httpc().Do(req)
	if err != nil {
		e := &UpstreamError{Exposable: false, Status: 502, Code: codeUpstream, UpstreamCode: 0, Message: "网盘服务异常，请稍后再试"}
		if ctx.Err() != nil || isTimeoutError(err) {
			// caller 取消/超时或客户端自身 timeout 都不是凭证问题：不得写入全局登录冷却，
			// 否则后续正常请求会被误伤（冷却只针对明确的凭证/登录失败）。
			return "", e
		}
		c.mu.Lock()
		c.failAt, c.failErr = time.Now(), e
		c.mu.Unlock()
		return "", e
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if ctx.Err() != nil {
		return "", &UpstreamError{Exposable: false, Status: 502, Code: codeUpstream, UpstreamCode: 0, Message: "网盘服务异常，请稍后再试"}
	}
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
	c.token, c.tokenAt, c.tokenID, c.failErr = body.Data.Token, time.Now(), cr.id, nil
	c.mu.Unlock()
	return body.Data.Token, nil
}

// tokenFor 取 token：未强制重登且缓存仍新鲜（47h TTL、凭证指纹一致）直接复用。
func (c *OpenListHTTP) tokenFor(ctx context.Context, cr olConn, force bool) (string, error) {
	since := time.Now()
	if !force {
		c.mu.Lock()
		tok, at, used := c.token, c.tokenAt, c.tokenID
		c.mu.Unlock()
		if tok != "" && used == cr.id && time.Since(at) <= tokenTTL {
			return tok, nil
		}
	}
	return c.login(ctx, cr, force, since)
}

// Call JSON 调用：解 OpenList 信封；401 强制重登重试一次；上游业务码挂 UpstreamCode。
// 连接配置在入口解析一次，重试复用同一份（不重复读运行时配置）。
func (c *OpenListHTTP) Call(ctx context.Context, method, apiPath string, opts CallOpts) (json.RawMessage, error) {
	return c.call(ctx, c.conn(ctx), method, apiPath, opts, false)
}

func (c *OpenListHTTP) call(ctx context.Context, cr olConn, method, apiPath string, opts CallOpts, retried bool) (json.RawMessage, error) {
	attempt := 1
	if retried {
		attempt = 2
	}
	ctx = integrations.WithAttempt(ctx, attempt)
	ctx = integrations.WithServiceName(ctx, "openlist")
	tok, err := c.tokenFor(ctx, cr, false)
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
	u := strings.TrimRight(cr.baseURL, "/") + apiPath
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
		if _, err := c.tokenFor(ctx, cr, true); err != nil {
			return nil, err
		}
		return c.call(ctx, cr, method, apiPath, opts, true)
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
	ctx = integrations.WithServiceName(ctx, "openlist")
	cr := c.conn(ctx)
	data, err := c.call(ctx, cr, http.MethodPost, "/api/fs/get", CallOpts{Body: map[string]any{"path": absPath, "password": ""}}, false)
	if err != nil {
		return nil, nil, err
	}
	var info struct {
		Sign string `json:"sign"`
	}
	_ = json.Unmarshal(data, &info)
	tok, err := c.tokenFor(ctx, cr, false)
	if err != nil {
		return nil, nil, err
	}
	u := strings.TrimRight(cr.baseURL, "/") + "/d" + encodePathSegs(absPath)
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
	ctx = integrations.WithServiceName(ctx, "openlist")
	cr := c.conn(ctx)
	data, err := c.call(ctx, cr, http.MethodPost, "/api/fs/archive/meta", CallOpts{Body: map[string]any{
		"path": absPath, "password": "", "refresh": false, "archive_pass": archivePass,
	}}, false)
	if err != nil {
		return nil, nil, err
	}
	var meta struct {
		Sign string `json:"sign"`
	}
	_ = json.Unmarshal(data, &meta)
	tok, err := c.tokenFor(ctx, cr, false)
	if err != nil {
		return nil, nil, err
	}
	u := strings.TrimRight(cr.baseURL, "/") + "/ad" + encodePathSegs(absPath) +
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
// 一次操作只解析一次连接配置，各级 mkdir 复用同一份。
func (c *OpenListHTTP) EnsureDir(ctx context.Context, absPath string) error {
	ctx = integrations.WithServiceName(ctx, "openlist")
	cr := c.conn(ctx)
	segs := strings.Split(absPath, "/")
	cur := ""
	for _, seg := range segs {
		if seg == "" {
			continue
		}
		cur += "/" + seg
		_, _ = c.call(ctx, cr, http.MethodPost, "/api/fs/mkdir", CallOpts{Body: map[string]any{"path": cur}}, false)
	}
	return nil
}

// PutEmpty 空文件：multipart 不接受 0 字节，直接复用 FsPut 原子写层（staging + rename），
// 不得绕过替换保护直写原路径。
func (c *OpenListHTTP) PutEmpty(ctx context.Context, absPath string) error {
	return c.FsPut(ctx, absPath, http.NoBody, 0)
}

// FsPut 原子直传：同目录唯一 staging 完整上传 → 大小校验 → rename 替换原路径。
// 任何一步失败都不触碰原文件（上传失败/校验失败只清 staging）；rename 不覆盖时先把
// 原文件备份为同目录隐藏名，替换失败恢复备份。所有调用方（Office 保存、跨模块、上传）
// 共用这一写层，无需各自处理截断风险。
func (c *OpenListHTTP) FsPut(ctx context.Context, absPath string, body io.Reader, size int64) error {
	absPath = strings.TrimRight(absPath, "/")
	dir, base := splitAbsPath(absPath)
	if dir == "" || base == "" {
		return &UpstreamError{Exposable: true, Status: 502, Code: codeUpstream, Message: "网盘路径不合法"}
	}
	if body == nil {
		body = strings.NewReader("")
	}
	nonce := randomShareID()
	staging := dir + "/." + base + ".shade-tmp-" + nonce

	readBody, contentLen, unknown := preparePutBody(body, size)
	written, err := c.putRaw(ctx, staging, readBody, contentLen)
	if err != nil {
		c.removeQuiet(ctx, staging)
		return err
	}
	if unknown {
		size = written
	} else if written != size {
		c.removeQuiet(ctx, staging)
		return &UpstreamError{Exposable: true, Status: 502, Code: codeUpstream,
			Message: "网盘写入不完整，已保留原文件"}
	}
	metaSize, err := c.statSize(ctx, staging)
	if err != nil || metaSize != size {
		c.removeQuiet(ctx, staging)
		if err != nil {
			return err
		}
		return &UpstreamError{Exposable: true, Status: 502, Code: codeUpstream,
			Message: "网盘写入校验失败，已保留原文件"}
	}

	if err := c.rename(ctx, staging, base); err == nil {
		return nil
	}
	// rename 可能因目标已存在且不覆盖而失败：备份原文件后重试，失败恢复。
	if _, statErr := c.statSize(ctx, absPath); statErr != nil {
		c.removeQuiet(ctx, staging)
		return &UpstreamError{Exposable: true, Status: 502, Code: codeUpstream,
			Message: "网盘文件替换失败，已保留原文件"}
	}
	backup := dir + "/." + base + ".shade-bak-" + nonce
	if err := c.rename(ctx, absPath, backupBase(backup)); err != nil {
		c.removeQuiet(ctx, staging)
		return &UpstreamError{Exposable: true, Status: 502, Code: codeUpstream,
			Message: "网盘文件替换失败，已保留原文件"}
	}
	if err := c.rename(ctx, staging, base); err != nil {
		// 恢复原文件（备份 rename 回原名）；恢复失败必须如实报告并保留备份路径，禁止谎称已恢复。
		restoreErr := c.rename(ctx, backup, base)
		c.removeQuiet(ctx, staging)
		if restoreErr != nil {
			return &UpstreamError{Exposable: true, Status: 502, Code: codeUpstream,
				Message: fmt.Sprintf("网盘文件替换失败，原文件备份恢复失败（备份: %s）", backup)}
		}
		return &UpstreamError{Exposable: true, Status: 502, Code: codeUpstream,
			Message: "网盘文件替换失败，已恢复原文件"}
	}
	c.removeQuiet(ctx, backup)
	return nil
}

// putRaw 上传到指定路径；返回实际读取字节数。contentLen < 0 表示未知（chunked）。
func (c *OpenListHTTP) putRaw(ctx context.Context, absPath string, body io.Reader, contentLen int64) (int64, error) {
	cr := c.conn(ctx)
	tok, err := c.tokenFor(ctx, cr, false)
	if err != nil {
		return 0, err
	}
	counter := &countingReader{r: body}
	reqBody := io.Reader(counter)
	if contentLen == 0 {
		reqBody = http.NoBody
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPut,
		strings.TrimRight(cr.baseURL, "/")+"/api/fs/put", reqBody)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Authorization", tok)
	req.Header.Set("File-Path", encodeURIPath(absPath))
	req.Header.Set("Content-Type", "application/octet-stream")
	if contentLen >= 0 {
		req.ContentLength = contentLen
	}
	noTimeout := *c.httpc()
	noTimeout.Timeout = 0
	res, err := noTimeout.Do(req)
	if err != nil {
		return counter.n, &UpstreamError{Exposable: false, Status: 502, Code: codeUpstream, Message: "网盘服务异常，请稍后再试"}
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	var env struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	}
	_ = json.Unmarshal(raw, &env)
	if res.StatusCode == http.StatusUnauthorized || env.Code == 401 {
		if _, err := c.tokenFor(ctx, cr, true); err != nil {
			return counter.n, err
		}
		return counter.n, &UpstreamError{Exposable: true, Status: 502, Code: codeUpstream, UpstreamCode: 401,
			Message: "OpenList 登录态失效，请重试"}
	}
	if env.Code != 200 {
		return counter.n, &UpstreamError{Exposable: true, Status: 502, Code: codeUpstream, UpstreamCode: env.Code,
			Message: firstNonEmpty(env.Message, "OpenList 返回异常")}
	}
	return counter.n, nil
}

// countingReader 统计上传实际字节数（未知长度时与 fs/get 元数据对账）。
type countingReader struct {
	r io.Reader
	n int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	return n, err
}

// preparePutBody 统一 size 语义：>0 已知长度；0/负数时探测是否空文件，
// 空文件按 0 字节发送（multipart 不支持 0 字节，直传可以），非空则按未知长度流式发送。
func preparePutBody(body io.Reader, size int64) (io.Reader, int64, bool) {
	if size > 0 {
		return io.LimitReader(body, size), size, false
	}
	br := bufio.NewReader(body)
	if _, err := br.Peek(1); err != nil {
		return http.NoBody, 0, false
	}
	return br, -1, true
}

// statSize 读取文件大小；不存在/上游错误返回 error。
func (c *OpenListHTTP) statSize(ctx context.Context, absPath string) (int64, error) {
	data, err := c.call(ctx, c.conn(ctx), http.MethodPost, "/api/fs/get",
		CallOpts{Body: map[string]any{"path": absPath, "password": ""}}, false)
	if err != nil {
		return 0, err
	}
	var meta struct {
		Size  int64 `json:"size"`
		IsDir bool  `json:"is_dir"`
	}
	if err := json.Unmarshal(data, &meta); err != nil {
		return 0, err
	}
	if meta.IsDir {
		return 0, &UpstreamError{Exposable: true, Status: 502, Code: codeUpstream, Message: "网盘路径不是文件"}
	}
	return meta.Size, nil
}

// rename 把同目录 source 改名为 name（OpenList /api/fs/rename 只接收目标文件名）。
func (c *OpenListHTTP) rename(ctx context.Context, source, name string) error {
	_, err := c.call(ctx, c.conn(ctx), http.MethodPost, "/api/fs/rename",
		CallOpts{Body: map[string]any{"path": source, "name": name}}, false)
	return err
}

// removeQuiet 尽力删除 staging/备份（失败仅忽略，原文件已安全）；OpenList remove 以目录+文件名定位。
func (c *OpenListHTTP) removeQuiet(ctx context.Context, absPath string) {
	dir, base := splitAbsPath(absPath)
	_, _ = c.call(ctx, c.conn(ctx), http.MethodPost, "/api/fs/remove",
		CallOpts{Body: map[string]any{"dir": dir, "names": []string{base}}, Timeout: 10 * time.Second}, false)
}

// splitAbsPath 拆出目录与文件名（均不含末尾斜杠）。
func splitAbsPath(absPath string) (dir, base string) {
	if i := strings.LastIndexByte(absPath, '/'); i >= 0 {
		return absPath[:i], absPath[i+1:]
	}
	return "", absPath
}

// backupBase 备份文件名（同目录隐藏名）；rename 接口只需目标文件名。
func backupBase(absPath string) string {
	_, base := splitAbsPath(absPath)
	return base
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

// isTimeoutError 网络/客户端超时判定（context deadline 或 net.Error.Timeout）。
func isTimeoutError(err error) bool {
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var ne net.Error
	return errors.As(err, &ne) && ne.Timeout()
}

// firstNonEmpty 返回首个非空字符串。
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
