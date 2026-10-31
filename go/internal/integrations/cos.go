// 腾讯云 COS V5 对象存储适配（签名对齐 cos-nodejs-sdk-v5 sdk/util.js getAuth；
// 上传/删除/访问地址行为对齐 server/src/worklog/cos.js）：
// putObject 带 CacheControl "private, no-cache"、ContentType 缺省 image/jpeg，不改 ACL；
// deleteObject 不存在视为成功；publicUrl 自定义域名优先、否则标准域名拼接。
// 密钥/桶/地域/域名经 Provider 热读取（域名配置变更后即时生效），不硬编码。
package integrations

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

// ErrCOSNotConfigured COS 必需配置缺失。
var ErrCOSNotConfigured = errors.New("腾讯云 COS 未配置（COS_SECRET_ID / COS_SECRET_KEY / COS_BUCKET / COS_REGION）")

// ErrCOSObjectTooLarge 对象回源超出字节预算（显式报错，绝不返回截断内容）。
var ErrCOSObjectTooLarge = errors.New("COS 对象超出大小预算")

// 回源默认预算：单次读取超时与最大字节数（生产回源不允许无限期/无上限）。
const (
	defaultCOSGetTimeout  = 120 * time.Second
	defaultCOSGetMaxBytes = 64 << 20
)

// COS 配置键默认名（对齐 server/src/config.js 与 docker/.env.example）。
const (
	DefaultCOSSecretIDKey  = "COS_SECRET_ID"
	DefaultCOSSecretKeyKey = "COS_SECRET_KEY"
	DefaultCOSBucketKey    = "COS_BUCKET"
	DefaultCOSRegionKey    = "COS_REGION"
	DefaultCOSBaseURLKey   = "COS_WORKLOG_BASE_URL"
)

// defaultCOSExpires 签名有效期（秒，对齐 SDK 缺省 900）。
const defaultCOSExpires = 900

// COSStore 腾讯云 COS V5 对象存储。满足 worklog.ObjectStore（PutBuffer/DeleteObject/PublicURL）
// 与 safeday.ObjectStore、treehazard.ObjectStore（Put/PublicURL/Delete）。
type COSStore struct {
	// Cfg 配置热读取（每次调用现读，nil 视为全未配置）。
	Cfg Provider
	// 以下为配置键名（空用默认名）。
	SecretIDKey  string
	SecretKeyKey string
	BucketKey    string
	RegionKey    string
	BaseURLKey   string
	// Endpoint 可选请求端点（测试/私有部署，如 httptest 地址）；空走标准域名
	// https://{bucket}.cos.{region}.myqcloud.com。
	Endpoint string
	// Expires 签名有效期秒（默认 900）。
	Expires int64
	// Now 签名时间源（测试注入；nil 用 time.Now）。
	Now func() time.Time
	// HTTP 传输客户端（nil 用默认）。
	HTTP *http.Client
	// Retry 有界重试（缺省不重试；仅瞬时传输故障，4xx/业务失败不重试）。
	Retry RetryPolicy
	// GetTimeout 回源单次读取超时（默认 120s；调用方 ctx 仍有更短期限时以更短者为准）。
	GetTimeout time.Duration
	// GetMaxBytes 回源单对象字节预算（默认 64MiB；超出返回 ErrCOSObjectTooLarge）。
	GetMaxBytes int64
	// Recorder 外部调用结构化事件写入口（可注入 *logging.Logger；nil 不写日志）。
	Recorder Recorder
}

// record 写本客户端的外部调用事件（Recorder nil 静默）。
func (s *COSStore) record(ctx context.Context, level, event, messageCN, raw string, fields map[string]any) {
	if s == nil {
		return
	}
	record(s.Recorder, ctx, level, event, messageCN, raw, fields)
}

// cosCredentials 运行时 COS 配置。
type cosCredentials struct {
	SecretID  string
	SecretKey string
	Bucket    string
	Region    string
	BaseURL   string
}

// cfgKey 取配置键名（空回退默认）。
func cfgKey(name, def string) string {
	if name == "" {
		return def
	}
	return name
}

// credentials 热读取密钥/桶/地域（BaseURL 可空）。
func (s *COSStore) credentials(ctx context.Context) (cosCredentials, error) {
	cred := cosCredentials{
		SecretID:  Lookup(ctx, s.Cfg, cfgKey(s.SecretIDKey, DefaultCOSSecretIDKey)),
		SecretKey: Lookup(ctx, s.Cfg, cfgKey(s.SecretKeyKey, DefaultCOSSecretKeyKey)),
		Bucket:    Lookup(ctx, s.Cfg, cfgKey(s.BucketKey, DefaultCOSBucketKey)),
		Region:    Lookup(ctx, s.Cfg, cfgKey(s.RegionKey, DefaultCOSRegionKey)),
		BaseURL:   Lookup(ctx, s.Cfg, cfgKey(s.BaseURLKey, DefaultCOSBaseURLKey)),
	}
	if cred.SecretID == "" || cred.SecretKey == "" || cred.Bucket == "" || cred.Region == "" {
		return cosCredentials{}, ErrCOSNotConfigured
	}
	return cred, nil
}

// objectURL 组装请求地址、Host 与签名用未编码路径（'/' + 对象键，对齐 SDK Pathname）。
func (s *COSStore) objectURL(cred cosCredentials, key string) (reqURL, host, signPath string) {
	signPath = "/" + key
	if s.Endpoint != "" {
		base := strings.TrimRight(s.Endpoint, "/")
		if u, err := url.Parse(base); err == nil && u.Host != "" {
			return base + "/" + camSafeURLPath(key), u.Host, signPath
		}
	}
	host = cred.Bucket + ".cos." + cred.Region + ".myqcloud.com"
	return "https://" + host + "/" + camSafeURLPath(key), host, signPath
}

// httpc 取传输客户端。
func (s *COSStore) httpc() *http.Client {
	if s.HTTP != nil {
		return s.HTTP
	}
	return &http.Client{Timeout: 30 * time.Second}
}

// statusError 对象存储 HTTP 非 2xx 错误。
type statusError struct {
	Op         string
	StatusCode int
	Body       string
}

// Error 错误文案。
func (e *statusError) Error() string {
	return fmt.Sprintf("cos: %s失败（HTTP %d）：%s", e.Op, e.StatusCode, e.Body)
}

// HTTPStatus 状态码（瞬时重试判定用）。
func (e *statusError) HTTPStatus() int { return e.StatusCode }

// signedRequest 构造已签名请求（回源/写入共用；密钥热读取）。
func (s *COSStore) signedRequest(ctx context.Context, method, key string, body io.Reader, extraHeaders map[string]string) (*http.Request, error) {
	cred, err := s.credentials(ctx)
	if err != nil {
		return nil, err
	}
	reqURL, host, signPath := s.objectURL(cred, key)
	req, err := http.NewRequestWithContext(ctx, method, reqURL, body)
	if err != nil {
		return nil, err
	}
	signHeaders := map[string]string{"Host": host}
	for k, v := range extraHeaders {
		req.Header.Set(k, v)
		signHeaders[k] = v
	}
	auth, err := cosSign(cosSignParams{
		SecretID:  cred.SecretID,
		SecretKey: cred.SecretKey,
		Method:    method,
		Path:      signPath,
		Headers:   signHeaders,
		Now:       s.Now,
		Expires:   s.Expires,
	})
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", auth)
	return req, nil
}

// do 发起对象操作请求（extraHeaders 参与签名）。
func (s *COSStore) do(ctx context.Context, method, key string, body []byte, extraHeaders map[string]string) (int, []byte, error) {
	ctx = WithServiceName(ctx, "cos")
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := s.signedRequest(ctx, method, key, reader, extraHeaders)
	if err != nil {
		return 0, nil, err
	}
	res, err := s.httpc().Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	return res.StatusCode, raw, nil
}

// getTimeout 回源超时（默认 120s）。
func (s *COSStore) getTimeout() time.Duration {
	if s.GetTimeout > 0 {
		return s.GetTimeout
	}
	return defaultCOSGetTimeout
}

// getMaxBytes 回源字节预算（默认 64MiB）。
func (s *COSStore) getMaxBytes() int64 {
	if s.GetMaxBytes > 0 {
		return s.GetMaxBytes
	}
	return defaultCOSGetMaxBytes
}

// GetBuffer 签名回源读取对象（超时 + 字节预算；非 2xx/超预算返回错误且不交出半截内容）。
func (s *COSStore) GetBuffer(ctx context.Context, key string) ([]byte, error) {
	ctx = WithServiceName(ctx, "cos")
	ctx, cancel := context.WithTimeout(ctx, s.getTimeout())
	defer cancel()
	req, err := s.signedRequest(ctx, http.MethodGet, key, nil, nil)
	if err != nil {
		return nil, err
	}
	// 用 ctx 的显式预算约束整个读取；避免 http.Client 的固定 Timeout 把大对象回源提前掐断。
	client := *s.httpc()
	client.Timeout = 0
	res, err := client.Do(req)
	if err != nil {
		class := errorClass(err)
		s.record(ctx, failureLevel(class), "cos.get.failed",
			"对象回源失败（"+errorClassCN(class)+"）",
			fmt.Sprintf("[cos] get object failed path=%s: %v", key, err),
			map[string]any{"op": "get", "path": key, "error_class": class})
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		raw, _ := io.ReadAll(io.LimitReader(res.Body, 4096))
		err := &statusError{Op: "回源对象", StatusCode: res.StatusCode, Body: truncateRunes(string(raw), 200)}
		s.record(ctx, "error", "cos.get.failed", "对象回源失败（接口状态异常）",
			fmt.Sprintf("[cos] get object failed path=%s status=%d", key, res.StatusCode),
			map[string]any{"op": "get", "path": key, "status": res.StatusCode, "error_class": classHTTPStatus})
		return nil, err
	}
	max := s.getMaxBytes()
	if res.ContentLength > max {
		err := fmt.Errorf("%w（%d > %d 字节）", ErrCOSObjectTooLarge, res.ContentLength, max)
		s.record(ctx, "warn", "cos.get.too_large", "对象回源超出大小预算",
			fmt.Sprintf("[cos] get object too large path=%s size=%d limit=%d", key, res.ContentLength, max),
			map[string]any{"op": "get", "path": key, "size": res.ContentLength, "limit": max})
		return nil, err
	}
	buf, err := io.ReadAll(io.LimitReader(res.Body, max+1))
	if err != nil {
		return nil, err
	}
	if int64(len(buf)) > max {
		err := fmt.Errorf("%w（> %d 字节）", ErrCOSObjectTooLarge, max)
		s.record(ctx, "warn", "cos.get.too_large", "对象回源超出大小预算",
			fmt.Sprintf("[cos] get object too large path=%s limit=%d", key, max),
			map[string]any{"op": "get", "path": key, "limit": max})
		return nil, err
	}
	s.record(ctx, "info", "cos.get", "对象回源成功",
		fmt.Sprintf("[cos] get object ok path=%s bytes=%d", key, len(buf)),
		map[string]any{"op": "get", "path": key, "bytes": len(buf)})
	return buf, nil
}

// PutBuffer 上传对象（ContentType 缺省 image/jpeg，CacheControl private, no-cache；不改 ACL）。
// 完成后经 Recorder 写结构化事件（成功 info / 失败分类），nil 不写。
func (s *COSStore) PutBuffer(ctx context.Context, key string, buf []byte, contentType string) error {
	ct := contentType
	if ct == "" {
		ct = "image/jpeg"
	}
	err := s.Retry.Do(ctx, transientError, func(ctx context.Context, _ int) error {
		code, resp, err := s.do(ctx, http.MethodPut, key, buf, map[string]string{
			"Content-Type":  ct,
			"Cache-Control": "private, no-cache",
		})
		if err != nil {
			return err
		}
		if code >= 200 && code < 300 {
			return nil
		}
		return &statusError{Op: "上传对象", StatusCode: code, Body: truncateRunes(string(resp), 200)}
	})
	if err != nil {
		class := errorClass(err)
		s.record(ctx, failureLevel(class), "cos.put.failed",
			"对象上传失败（"+errorClassCN(class)+"）",
			fmt.Sprintf("[cos] put object failed path=%s: %v", key, err),
			map[string]any{"op": "put", "path": key, "error_class": class})
		return err
	}
	s.record(ctx, "info", "cos.put", "对象上传成功",
		fmt.Sprintf("[cos] put object ok path=%s bytes=%d", key, len(buf)),
		map[string]any{"op": "put", "path": key, "bytes": len(buf)})
	return nil
}

// DeleteObject 删除对象（不存在视为成功）。完成后经 Recorder 写结构化事件，nil 不写。
func (s *COSStore) DeleteObject(ctx context.Context, key string) error {
	err := s.Retry.Do(ctx, transientError, func(ctx context.Context, _ int) error {
		code, resp, err := s.do(ctx, http.MethodDelete, key, nil, nil)
		if err != nil {
			return err
		}
		if (code >= 200 && code < 300) || code == http.StatusNotFound {
			return nil
		}
		return &statusError{Op: "删除对象", StatusCode: code, Body: truncateRunes(string(resp), 200)}
	})
	if err != nil {
		class := errorClass(err)
		s.record(ctx, failureLevel(class), "cos.delete.failed",
			"对象删除失败（"+errorClassCN(class)+"）",
			fmt.Sprintf("[cos] delete object failed path=%s: %v", key, err),
			map[string]any{"op": "delete", "path": key, "error_class": class})
		return err
	}
	s.record(ctx, "info", "cos.delete", "对象删除成功",
		fmt.Sprintf("[cos] delete object ok path=%s", key),
		map[string]any{"op": "delete", "path": key})
	return nil
}

// PublicURL 按对象键生成访问地址（自定义域名优先，配置热读取、域名变更即时生效）；
// 未配置返回空串。
func (s *COSStore) PublicURL(key string) string {
	ctx := context.Background()
	base := Lookup(ctx, s.Cfg, cfgKey(s.BaseURLKey, DefaultCOSBaseURLKey))
	if base == "" {
		bucket := Lookup(ctx, s.Cfg, cfgKey(s.BucketKey, DefaultCOSBucketKey))
		region := Lookup(ctx, s.Cfg, cfgKey(s.RegionKey, DefaultCOSRegionKey))
		if bucket == "" || region == "" {
			return ""
		}
		base = "https://" + bucket + ".cos." + region + ".myqcloud.com"
	}
	return strings.TrimRight(base, "/") + "/" + key
}

// Put 上传对象并返回公开访问地址（safeday.ObjectStore / treehazard.ObjectStore）。
func (s *COSStore) Put(ctx context.Context, key string, data []byte, contentType string) (string, error) {
	if err := s.PutBuffer(ctx, key, data, contentType); err != nil {
		return "", err
	}
	return s.PublicURL(key), nil
}

// Delete 删除对象（safeday.ObjectStore / treehazard.ObjectStore）。
func (s *COSStore) Delete(ctx context.Context, key string) error {
	return s.DeleteObject(ctx, key)
}

// ===== COS V5 请求签名（对齐 cos-nodejs-sdk-v5 sdk/util.js getAuth） =====

// cosSignParams 签名入参。
type cosSignParams struct {
	SecretID  string
	SecretKey string
	Method    string            // HTTP 方法（内部转小写）
	Path      string            // 已含前导 / 的未编码路径（'/' + 对象键）
	Query     map[string]string // 参与签名的查询参数（可空）
	Headers   map[string]string // 参与签名的请求头（含 Host；按白名单过滤）
	KeyTime   string            // "start;end"；空按 Now/Expires 推出
	Now       func() time.Time  // 时间源（测试注入）
	Expires   int64             // 签名有效期秒（默认 900）
}

// cosSignableHeaders SDK signHeaders 白名单。
var cosSignableHeaders = map[string]bool{
	"cache-control": true, "content-disposition": true, "content-encoding": true,
	"content-length": true, "content-md5": true, "content-type": true, "expect": true,
	"expires": true, "host": true, "if-match": true, "if-modified-since": true,
	"if-none-match": true, "if-unmodified-since": true, "origin": true, "range": true,
	"transfer-encoding": true, "pic-operations": true,
}

// cosSignableHeader 可签头判定（白名单 + x-cos-* + x-ci-*，对齐 SDK getSignHeaderObj）。
func cosSignableHeader(k string) bool {
	lk := strings.ToLower(k)
	return strings.HasPrefix(lk, "x-cos-") || strings.HasPrefix(lk, "x-ci-") || cosSignableHeaders[lk]
}

// camSafeURLEncode 对齐 SDK camSafeUrlEncode：encodeURIComponent 再转义 ! ' ( ) *，大写十六进制。
func camSafeURLEncode(s string) string {
	const upperhex = "0123456789ABCDEF"
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case 'a' <= c && c <= 'z', 'A' <= c && c <= 'Z', '0' <= c && c <= '9',
			c == '-', c == '_', c == '.', c == '~':
			b.WriteByte(c)
		default:
			b.WriteByte('%')
			b.WriteByte(upperhex[c>>4])
			b.WriteByte(upperhex[c&0xf])
		}
	}
	return b.String()
}

// camSafeURLPath 对象键转请求路径：同 camSafeURLEncode 但保留 '/'（对齐 SDK getUrl）。
func camSafeURLPath(key string) string {
	return strings.ReplaceAll(camSafeURLEncode(key), "%2F", "/")
}

// sortKeysCI 按小写键名排序（对齐 SDK getObjectKeys 排序比较器）。
func sortKeysCI(keys []string) {
	sort.SliceStable(keys, func(i, j int) bool {
		a, b := strings.ToLower(keys[i]), strings.ToLower(keys[j])
		if a == b {
			return false
		}
		return a < b
	})
}

// cosKVString 参数串（k=v&...，键排序、键值 camSafeUrlEncode、键可小写，对齐 SDK obj2str）。
func cosKVString(m map[string]string, lowerKey bool) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sortKeysCI(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		ek := camSafeURLEncode(k)
		if lowerKey {
			ek = strings.ToLower(ek)
		}
		parts = append(parts, ek+"="+camSafeURLEncode(m[k]))
	}
	return strings.Join(parts, "&")
}

// cosParamList q-header-list / q-url-param-list（编码小写键名排序拼分号）。
func cosParamList(m map[string]string) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, camSafeURLEncode(k))
	}
	sortKeysCI(keys)
	for i := range keys {
		keys[i] = strings.ToLower(keys[i])
	}
	return strings.Join(keys, ";")
}

// hmacSHA1Hex HMAC-SHA1 十六进制（key 为 HMAC 密钥）。
func hmacSHA1Hex(key, msg string) string {
	m := hmac.New(sha1.New, []byte(key))
	m.Write([]byte(msg))
	return hex.EncodeToString(m.Sum(nil))
}

// sha1Hex SHA-1 十六进制。
func sha1Hex(msg string) string {
	sum := sha1.Sum([]byte(msg))
	return hex.EncodeToString(sum[:])
}

// cosSign 计算 COS V5 请求签名（q-sign-algorithm=sha1 版）：
// SignKey=HMAC-SHA1(SecretKey,KeyTime)；
// HttpString=method\npath\nparams\nheaders\n；
// StringToSign=sha1\nKeyTime\nSHA1(HttpString)\n；Signature=HMAC-SHA1(SignKey,StringToSign)。
func cosSign(p cosSignParams) (string, error) {
	if p.SecretID == "" || p.SecretKey == "" {
		return "", ErrCOSNotConfigured
	}
	keyTime := p.KeyTime
	if keyTime == "" {
		now := time.Now()
		if p.Now != nil {
			now = p.Now()
		}
		exp := p.Expires
		if exp <= 0 {
			exp = defaultCOSExpires
		}
		start := now.Unix()
		keyTime = fmt.Sprintf("%d;%d", start, start+exp)
	}
	headers := map[string]string{}
	for k, v := range p.Headers {
		if v == "" {
			continue // 空头不签（对齐 SDK getAuthorizationAsync）
		}
		if cosSignableHeader(k) {
			headers[k] = v
		}
	}
	signKey := hmacSHA1Hex(p.SecretKey, keyTime)
	httpString := strings.Join([]string{
		strings.ToLower(p.Method),
		p.Path,
		cosKVString(p.Query, true),
		cosKVString(headers, true),
		"",
	}, "\n")
	stringToSign := "sha1\n" + keyTime + "\n" + sha1Hex(httpString) + "\n"
	signature := hmacSHA1Hex(signKey, stringToSign)
	return "q-sign-algorithm=sha1" +
		"&q-ak=" + p.SecretID +
		"&q-sign-time=" + keyTime +
		"&q-key-time=" + keyTime +
		"&q-header-list=" + cosParamList(headers) +
		"&q-url-param-list=" + cosParamList(p.Query) +
		"&q-signature=" + signature, nil
}
