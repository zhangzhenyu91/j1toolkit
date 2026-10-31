// 协议传输层：jsonm/jsonx 调用（uniID 池 + sign 头 + 出口代理）、登录业务封装。
// host 与 http.Client 可注入（测试用 httptest，不触真实商旅）。
package sgccclockin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"shade/internal/integrations"
)

const (
	defaultHostH5 = "gwslapi.esgcc.com.cn"   // jsonm 与 jsonx(default)
	defaultHostZB = "gwslapizb.esgcc.com.cn" // jsonx(slapp)
	// poolTTL 池内 JWT 30min 过期，超 25min 强制重拉（实测口径）。
	poolTTL = 25 * time.Minute
)

// Protocol 商旅协议客户端。
type Protocol struct {
	Keys     Keys
	HostH5   string       // 可注入（默认 gwslapi.esgcc.com.cn）
	HostZB   string       // 可注入（默认 gwslapizb.esgcc.com.cn）
	HTTP     *http.Client // 可注入（仅无代理时生效；默认 20s 超时）
	Scheme   string       // 可注入（默认 https；测试用 httptest 置 http）
	ProxyURL string       // SGCC_PROXY_URL：出口代理（Cfg 未接入时的静态回退值）
	Cfg      ConfigStore  // 运行时配置（可注入；nil 用静态字段）——密钥/代理变更后下一次调用热生效
	Now      func() time.Time

	mu         sync.Mutex
	poolM      []string
	poolAT     time.Time
	poolX      []string
	poolXA     time.Time
	fp         string         // 密钥/代理指纹（变化清池）
	transports transportCache // 代理传输层缓存（地址变化重建）
	// 补池单飞闸门（按通道各一）：ads HTTP 往返在池锁之外，同通道并发只拉一次池
	initM poolInitGate
	initX poolInitGate
}

// poolInitGate 单通道补池闸门：同一通道同时只有一次 ads 初始化在跑，其余请求等待后
// 复取新池（双检）；ctx 取消可退出等待。
type poolInitGate struct {
	once sync.Once
	ch   chan struct{}
}

func (g *poolInitGate) lock(ctx context.Context) (func(), error) {
	g.once.Do(func() { g.ch = make(chan struct{}, 1) })
	select {
	case g.ch <- struct{}{}:
		return func() { <-g.ch }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// NewProtocol 构造协议客户端（keys 从 env/config 注入）。
func NewProtocol(keys Keys) *Protocol {
	return &Protocol{
		Keys:   keys,
		HostH5: defaultHostH5,
		HostZB: defaultHostZB,
		HTTP:   newSGCCHTTPClient(directTransport()),
		Now:    time.Now,
	}
}

// newSGCCHTTPClient 与 Node https.request 同口径：不自动跟随重定向。
// 默认 http.Client 会跳到 Location 指定的第三地址，既偏离旧实现，也会把真实网关响应藏成另一地址的超时。
func newSGCCHTTPClient(rt http.RoundTripper) *http.Client {
	return &http.Client{
		Transport: rt,
		Timeout:   20 * time.Second,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

func (p *Protocol) now() time.Time {
	if p.Now != nil {
		return p.Now()
	}
	return time.Now()
}

// post 发起协议请求（POST /api/<path>）。配置代理时经 proxyURL（socks5/http 代理，见 proxy.go）。
func (p *Protocol) post(ctx context.Context, proxyURL, host, path string, headers map[string]string, body string) (status int, raw string, err error) {
	ctx = integrations.WithServiceName(ctx, "sgcc")
	var transport http.RoundTripper
	if proxyURL != "" {
		transport, err = p.transports.get(proxyURL)
		if err != nil {
			return 0, "", err
		}
	}
	var c *http.Client
	switch {
	case p.HTTP != nil && proxyURL == "":
		c = p.HTTP
	case transport != nil:
		c = newSGCCHTTPClient(transport)
	default:
		c = newSGCCHTTPClient(directTransport())
	}
	scheme := p.Scheme
	if scheme == "" {
		scheme = "https"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		scheme+"://"+host+"/api/"+strings.TrimPrefix(path, "/"), strings.NewReader(body))
	if err != nil {
		return 0, "", err
	}
	for k, v := range headers {
		// 直接写原始键，禁止 CanonicalMIMEHeaderKey 把 uniID/systemVersion 等改成另一形态；
		// Node 保留 camelCase，商旅网关按这些原始键名校验，大小写变化会返回 503。
		req.Header[k] = []string{v}
	}
	// Node https.request 默认不带 User-Agent；调用方未显式给出时用空切片阻止 Go 自动补
	// Go-http-client/1.1（实测会触发网关 302）。JSONX 自带的商旅 App UA 必须原样保留。
	if _, ok := req.Header["User-Agent"]; !ok {
		req.Header["User-Agent"] = []string{}
	}
	req.ContentLength = int64(len(body))
	resp, err := c.Do(req)
	if err != nil {
		if proxyURL != "" {
			return 0, "", errors.New(strings.ReplaceAll(err.Error(), proxyURL, maskProxyURL(proxyURL)))
		}
		return 0, "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 && resp.StatusCode < 400 {
		location := resp.Header.Get("Location")
		if u, parseErr := url.Parse(location); parseErr == nil {
			u.User = nil
			u.RawQuery = ""
			u.Fragment = ""
			location = u.String()
		}
		return resp.StatusCode, "", fmt.Errorf("商旅接口返回重定向（HTTP %d，已停止跟随）：%s", resp.StatusCode, location)
	}
	buf, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return resp.StatusCode, "", err
	}
	return resp.StatusCode, string(buf), nil
}

// InitSession jsonm 拉 uniID 池（/api/ads，AES+SM2）。
func (p *Protocol) InitSession(ctx context.Context) error {
	k, proxy := p.resolve(ctx)
	return p.initSessionM(ctx, k, proxy, p.fingerprint(k, proxy))
}

func (p *Protocol) initSessionM(ctx context.Context, k Keys, proxyURL, fp string) error {
	r := randR()
	body, err := k.jsonmEncryptBody(map[string]any{
		"adClientId": "android",
		"adSecret":   mustNeed("SGCC_AD_SECRET", k.AdSecret),
	}, r)
	if err != nil {
		return err
	}
	uniID, err := k.MakeJWT("tenant")
	if err != nil {
		return err
	}
	headers := map[string]string{
		"Content-Type":     "application/jsonm",
		"Accept":           "application/jsonm",
		"tenant":           "newh5",
		"uniID":            uniID,
		"version":          k.version(),
		"X-Requested-With": "XMLHttpRequest",
	}
	headers["sign"] = MakeSignHeaderM(headers, r)
	status, raw, err := p.post(ctx, proxyURL, p.hostH5(), "ads", headers, body)
	if err != nil {
		return err
	}
	decoded, err := k.jsonmDecryptBody(raw)
	if err != nil {
		return fmt.Errorf("商旅 ads 应答解密失败（HTTP %d）：%w", status, err)
	}
	list := extractList(decoded)
	if status != 200 || len(list) == 0 {
		return fmt.Errorf("商旅 ads 初始化失败：%s", adsFailureSummary(status, decoded))
	}
	p.storePool(false, fp, list)
	return nil
}

// storePool 写回补池结果（锁内）：拉池期间密钥/代理指纹已变则丢弃，旧密钥语境的池不得覆盖新配置。
func (p *Protocol) storePool(slapp bool, fp string, list []string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if fp != "" && p.fp != fp {
		return
	}
	if slapp {
		p.poolX, p.poolXA = list, p.now()
		return
	}
	p.poolM, p.poolAT = list, p.now()
}

// adsFailureSummary 只保留状态与字段形状，不输出 uniID、密钥或原始应答。
func adsFailureSummary(status int, decoded map[string]any) string {
	parts := []string{fmt.Sprintf("HTTP %d", status)}
	if decoded == nil {
		return parts[0] + "，应答为空"
	}
	if v, ok := decoded["statusCode"]; ok {
		parts = append(parts, "statusCode="+jsString(v))
	}
	for _, key := range []string{"msg", "message"} {
		if msg, ok := decoded[key].(string); ok && strings.TrimSpace(msg) != "" {
			parts = append(parts, truncate(strings.TrimSpace(msg), 80))
			break
		}
	}
	data, _ := decoded["data"].(map[string]any)
	if data == nil {
		parts = append(parts, "缺少 data.list")
	} else if list, ok := data["list"].([]any); !ok {
		parts = append(parts, "data.list 不是数组")
	} else if len(list) == 0 {
		parts = append(parts, "uniID 列表为空")
	} else {
		parts = append(parts, "uniID 列表未正确读取")
	}
	return strings.Join(parts, "，")
}

func (p *Protocol) hostH5() string {
	if p.HostH5 != "" {
		return p.HostH5
	}
	return defaultHostH5
}

func (p *Protocol) hostZB() string {
	if p.HostZB != "" {
		return p.HostZB
	}
	return defaultHostZB
}

func mustNeed(name, v string) string {
	s, _ := need(name, v)
	return s
}

// extractList 从 ads 响应取 uniID 列表（decoded.data.list）。
func extractList(decoded map[string]any) []string {
	data, _ := decoded["data"].(map[string]any)
	list, _ := data["list"].([]any)
	out := make([]string, 0, len(list))
	for _, v := range list {
		if s, ok := v.(string); ok && s != "" {
			out = append(out, s)
		}
	}
	return out
}

// takeUniID 取池化 uniID（slapp/default 分池；池未耗尽但超 25min 也强制重拉）。
// 取池只在 p.mu 内做；补池（ads HTTP 往返）在锁外单飞：同通道并发请求只拉一次池，
// 其余等待后复取（双检），HTTP 期间不占用池锁。
func (p *Protocol) takeUniID(ctx context.Context, k Keys, proxyURL string, slapp bool, fp string) (string, error) {
	if id, ok := p.popUniID(slapp); ok {
		return id, nil
	}
	unlock, err := p.poolGate(slapp).lock(ctx)
	if err != nil {
		return "", err
	}
	defer unlock()
	// 双检：等闸门期间同通道可能已由其它请求补齐
	if id, ok := p.popUniID(slapp); ok {
		return id, nil
	}
	if slapp {
		err = p.initSessionX(ctx, k, proxyURL, fp)
	} else {
		err = p.initSessionM(ctx, k, proxyURL, fp)
	}
	if err != nil {
		return "", err
	}
	if id, ok := p.popUniID(slapp); ok {
		return id, nil
	}
	if slapp {
		return "", errors.New("商旅 uniID 池为空（slapp）")
	}
	return "", errors.New("商旅 uniID 池为空")
}

// poolGate 取通道对应的补池闸门。
func (p *Protocol) poolGate(slapp bool) *poolInitGate {
	if slapp {
		return &p.initX
	}
	return &p.initM
}

// popUniID 锁内取一枚；池空或超过 TTL（应重拉）返回 false。
func (p *Protocol) popUniID(slapp bool) (string, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	pool, at := &p.poolM, p.poolAT
	if slapp {
		pool, at = &p.poolX, p.poolXA
	}
	if len(*pool) == 0 || p.now().Sub(at) > poolTTL {
		return "", false
	}
	id := (*pool)[0]
	*pool = (*pool)[1:]
	return id, true
}

// InitSessionX slapp 通道拉 uniID 池（jsonx ads）。
func (p *Protocol) InitSessionX(ctx context.Context) error {
	k, proxy := p.resolve(ctx)
	return p.initSessionX(ctx, k, proxy, p.fingerprint(k, proxy))
}

func (p *Protocol) initSessionX(ctx context.Context, k Keys, proxyURL, fp string) error {
	r, err := p.callJSONX(ctx, k, proxyURL, "ads", map[string]any{
		"adClientId": "android",
		"adSecret":   mustNeed("SGCC_AD_SECRET", k.AdSecret),
	}, CallOpts{Tenant: "slapp", NoPool: true}, "")
	if err != nil {
		return err
	}
	list := extractList(r.Decoded)
	if r.Status != 200 || len(list) == 0 {
		return fmt.Errorf("商旅 ads 初始化失败（slapp 通道）：HTTP %d %s", r.Status, truncate(string(r.Raw), 200))
	}
	p.storePool(true, fp, list)
	return nil
}

// SelfCheck 启动通道自检：jsonm/slapp 两通道各拉一次 uniID 池。
func (p *Protocol) SelfCheck(ctx context.Context) error {
	if err := p.InitSession(ctx); err != nil {
		return err
	}
	return p.InitSessionX(ctx)
}

// CallOpts jsonx 调用选项。
type CallOpts struct {
	Token         string
	Tenant        string // default / slapp
	DeviceType    string
	SystemVersion string
	Mobile        string
	NoPool        bool
}

// CallResult 协议调用结果。
type CallResult struct {
	Status  int
	Decoded map[string]any
	Raw     string
}

// CallJSONM jsonm 业务调用（uniID 从池取）。密钥/代理每次现读，配置变更后热生效。
func (p *Protocol) CallJSONM(ctx context.Context, path string, plainObj map[string]any) (CallResult, error) {
	k, proxy := p.resolve(ctx)
	uniID, err := p.takeUniID(ctx, k, proxy, false, p.fingerprint(k, proxy))
	if err != nil {
		return CallResult{}, err
	}
	r := randR()
	body, err := k.jsonmEncryptBody(plainObj, r)
	if err != nil {
		return CallResult{}, err
	}
	headers := map[string]string{
		"Content-Type":     "application/jsonm",
		"Accept":           "application/jsonm",
		"tenant":           "newh5",
		"uniID":            uniID,
		"version":          k.version(),
		"X-Requested-With": "XMLHttpRequest",
	}
	headers["sign"] = MakeSignHeaderM(headers, r)
	status, raw, err := p.post(ctx, proxy, p.hostH5(), path, headers, body)
	if err != nil {
		return CallResult{}, err
	}
	var decoded map[string]any
	if j, derr := k.jsonmDecryptBody(raw); derr == nil {
		decoded = j
	}
	return CallResult{Status: status, Decoded: decoded, Raw: raw}, nil
}

// CallJSONX jsonx 业务调用（default 走 RSA，slapp 走 dCu/wLA）。密钥/代理每次现读，配置变更后热生效。
// 锁只保护 uniID 池与指纹，HTTP 往返在锁外（与 CallJSONM 同口径），不同请求可并行。
func (p *Protocol) CallJSONX(ctx context.Context, path string, plainObj map[string]any, opts CallOpts) (CallResult, error) {
	k, proxy := p.resolve(ctx)
	fp := p.fingerprint(k, proxy)
	uniID := ""
	if !opts.NoPool {
		var err error
		uniID, err = p.takeUniID(ctx, k, proxy, opts.Tenant == "slapp", fp)
		if err != nil {
			return CallResult{}, err
		}
	}
	return p.callJSONX(ctx, k, proxy, path, plainObj, opts, uniID)
}

// callJSONX 执行一次 jsonx 请求（uniID 由调用方取好，NoPool 时为空）：全程不持池锁，
// 并发请求只共享只读的密钥/代理快照。
func (p *Protocol) callJSONX(ctx context.Context, k Keys, proxyURL, path string, plainObj map[string]any, opts CallOpts, uniID string) (CallResult, error) {
	isSlapp := opts.Tenant == "slapp"
	tenant := opts.Tenant
	if tenant == "" {
		tenant = "default"
	}
	deviceType := opts.DeviceType
	if deviceType == "" {
		deviceType = "Pixel 7"
	}
	systemVersion := opts.SystemVersion
	if systemVersion == "" {
		systemVersion = "Android 13"
	}
	r := randR()
	body, err := k.jsonxEncryptBody(tenant, plainObj, r)
	if err != nil {
		return CallResult{}, err
	}
	headers := k.JSONXHeaders(tenant, deviceType, systemVersion, opts.Mobile,
		"application/jsonx", opts.Token, uniID)
	headers["sign"] = MakeSignHeaderX(headers, r)
	host := p.hostH5()
	if isSlapp {
		host = p.hostZB()
	}
	status, raw, err := p.post(ctx, proxyURL, host, path, headers, body)
	if err != nil {
		return CallResult{}, err
	}
	var decoded map[string]any
	if j, derr := k.jsonxDecryptBody(tenant, raw); derr == nil {
		decoded = j
	}
	return CallResult{Status: status, Decoded: decoded, Raw: truncate(raw, 300)}, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

// ---------- 业务封装（登录链路） ----------

// LoginBizError 平台业务失败（HTTP 通但平台拒发 token）：路由层映射 40037。
type LoginBizError struct {
	Prefix     string
	Msg        string
	Hint       string
	SgccStatus any
}

func (e *LoginBizError) Error() string {
	if e.Msg != "" {
		s := e.Prefix + "：" + e.Msg
		if e.Hint != "" {
			s += "（" + e.Hint + "）"
		}
		return s
	}
	s := e.Prefix + "，请稍后重试"
	if e.Hint != "" {
		s += "（" + e.Hint + "）"
	}
	return s
}

func loginBizError(prefix string, decoded map[string]any, hint string) *LoginBizError {
	msg := ""
	if decoded != nil {
		if v, ok := decoded["msg"].(string); ok {
			msg = truncate(v, 50)
		} else if v, ok := decoded["message"].(string); ok {
			msg = truncate(v, 50)
		}
	}
	var status any
	if decoded != nil {
		status = decoded["statusCode"]
	}
	return &LoginBizError{Prefix: prefix, Msg: msg, Hint: hint, SgccStatus: status}
}

// LoginCaptcha 取图形验证码（裸 base64；风控窗口期 99000，路由层据此降级滑块）。
func (p *Protocol) LoginCaptcha(ctx context.Context, mobile string) (string, error) {
	r, err := p.CallJSONM(ctx, "user/validcodeimg", map[string]any{"mobile": mobile})
	if err != nil {
		return "", err
	}
	if r.Decoded != nil {
		if data, ok := r.Decoded["data"].(string); ok && data != "" {
			return data, nil
		}
	}
	// 错误文案同 Node：JSON.stringify(decoded || status)
	shown := any(r.Status)
	if r.Decoded != nil {
		shown = any(r.Decoded)
	}
	b, _ := json.Marshal(shown)
	return "", fmt.Errorf("获取图形验证码失败：%s", string(b))
}

// LoginSendSmsV2 图形码通道发短信（v2）：statusCode=200 即成功。
func (p *Protocol) LoginSendSmsV2(ctx context.Context, mobile, checkImgCode string) (map[string]any, error) {
	r, err := p.CallJSONM(ctx, "user/sendSmsCode/v2", map[string]any{
		"mobile": mobile, "checkImgCode": checkImgCode,
	})
	if err != nil {
		return nil, err
	}
	return r.Decoded, nil
}

// LoginSendSmsSlapp 滑块通道发短信（v3，slapp/jsonx）：成败看 data.code（成功无 data 或 data.code=0）。
func (p *Protocol) LoginSendSmsSlapp(ctx context.Context, mobile, captchaToken, constID string, opts CallOpts) (map[string]any, error) {
	opts.Tenant = "slapp"
	opts.Mobile = mobile
	r, err := p.CallJSONX(ctx, "user/sendSmsCode/v3", map[string]any{
		"mobile": mobile, "source": "3", "constId": constID,
		"captchaToken": captchaToken, "riskFlag": "Y", "riskReviewFlag": true,
	}, opts)
	if err != nil {
		return nil, err
	}
	return r.Decoded, nil
}

// extractToken 从登录响应取 token。
func extractToken(decoded map[string]any) string {
	if decoded == nil {
		return ""
	}
	data := decoded["data"]
	if s, ok := data.(string); ok && s != "" {
		return s
	}
	m, ok := data.(map[string]any)
	if !ok {
		return ""
	}
	if s, ok := m["token"].(string); ok && s != "" {
		return s
	}
	if s, ok := m["accessToken"].(string); ok && s != "" {
		return s
	}
	return ""
}

// LoginBySmsSlapp 短信换 token（smstoken/v4，图形/滑块两通道统一走它）。
func (p *Protocol) LoginBySmsSlapp(ctx context.Context, mobile, checkCode, captchaToken, constID string, opts CallOpts) (string, error) {
	opts.Tenant = "slapp"
	opts.Mobile = mobile
	r, err := p.CallJSONX(ctx, "user/smstoken/v4", map[string]any{
		"mobile": mobile, "checkCode": checkCode, "source": "3",
		"constId": constID, "captchaToken": captchaToken, "riskFlag": "Y",
	}, opts)
	if err != nil {
		return "", err
	}
	token := extractToken(r.Decoded)
	if token == "" {
		return "", loginBizError("短信登录失败", r.Decoded, "")
	}
	return token, nil
}

// LoginByPasswordV3 图形码通道密码登录（token/v3，体无 source 字段）。
func (p *Protocol) LoginByPasswordV3(ctx context.Context, mobile, password, checkImgCode string, opts CallOpts) (string, error) {
	opts.Tenant = "slapp"
	opts.Mobile = mobile
	r, err := p.CallJSONX(ctx, "token/v3", map[string]any{
		"mobile": mobile, "password": password, "checkImgCode": checkImgCode,
		"constId": "", "captchaToken": "", "riskFlag": "Y",
	}, opts)
	if err != nil {
		return "", err
	}
	token := extractToken(r.Decoded)
	if token == "" {
		return "", loginBizError("密码登录失败", r.Decoded, "可改用短信验证码登录")
	}
	return token, nil
}

// LoginByPassword 滑块通道密码登录（user/token/v4，slapp）。
func (p *Protocol) LoginByPassword(ctx context.Context, mobile, password, captchaToken, constID string, opts CallOpts) (string, error) {
	opts.Tenant = "slapp"
	opts.Mobile = mobile
	r, err := p.CallJSONX(ctx, "user/token/v4", map[string]any{
		"mobile": mobile, "password": password, "source": "3",
		"constId": constID, "captchaToken": captchaToken,
		"riskFlag": "Y", "riskReviewFlag": true,
	}, opts)
	if err != nil {
		return "", err
	}
	token := extractToken(r.Decoded)
	if token == "" {
		return "", loginBizError("密码登录失败", r.Decoded, "可改用短信验证码登录")
	}
	return token, nil
}

// ProbeProxy 出口代理健康探测：仅 TCP 建连（不向商旅发请求）；未配置判 skipped。永不返回 error。
// 代理地址每次现读运行时配置，配置变更后热生效。
func (p *Protocol) ProbeProxy() (ok bool, skipped bool, errMsg string) {
	_, raw := p.resolve(context.Background())
	if raw == "" {
		return true, true, ""
	}
	u, err := url.Parse(raw)
	if err != nil {
		return false, false, fmt.Sprintf("代理地址无效：%v", err)
	}
	host := strings.Trim(u.Hostname(), "[]")
	port := u.Port()
	if port == "" {
		port = "1080"
	}
	conn, err := net.DialTimeout("tcp", net.JoinHostPort(host, port), 5*time.Second)
	if err != nil {
		return false, false, err.Error()
	}
	_ = conn.Close()
	return true, false, ""
}
