package office

// 文件票据与 OnlyOffice JWT：票据短时效绑定单来源单文件（{src,wls|sdr|wlr|thd|crs|ndp} 载荷）；
// 回调双校验 = OnlyOffice JWT（OO_JWT_SECRET）+ 本服务回调票据。

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// TicketPayload 回源/回调票据载荷。
type TicketPayload struct {
	Src  string `json:"src"` // ndp 网盘 / wls 任务单 / sdr 安全日 / wlr 备注附件 / thd 树障 / crs 交跨
	NDP  *NDP   `json:"ndp,omitempty"`
	Ref  string `json:"ref,omitempty"` // 只读源引用（由来源模块解释）
	Name string `json:"name,omitempty"`
	Exp  int64  `json:"exp"`
}

// NDP 网盘来源载荷（复用网盘预览口径）。
type NDP struct {
	Space string `json:"space"`
	User  string `json:"user"`
	Path  string `json:"path"`
	Team  int64  `json:"team"`
}

// ErrTicketInvalid 票据无效/过期。
var ErrTicketInvalid = errors.New("文件票据无效或已过期")

// SignTicket 生成文件票据（HMAC-SHA256；ttlSeconds 默认 600）。
func (m *Module) SignTicket(ctx context.Context, p TicketPayload, ttlSeconds int) (string, error) {
	secret, err := m.ticketSecret(ctx)
	if err != nil {
		return "", err
	}
	if ttlSeconds <= 0 {
		ttlSeconds = 600
	}
	p.Exp = time.Now().Add(time.Duration(ttlSeconds) * time.Second).Unix()
	raw, _ := json.Marshal(p)
	payload := base64.RawURLEncoding.EncodeToString(raw)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(payload))
	return payload + "." + hex.EncodeToString(mac.Sum(nil)), nil
}

// VerifyTicket 校验并解析票据。
func (m *Module) VerifyTicket(ctx context.Context, token string) (TicketPayload, error) {
	var p TicketPayload
	secret, err := m.ticketSecret(ctx)
	if err != nil {
		return p, err
	}
	dot := strings.LastIndexByte(token, '.')
	if dot <= 0 {
		return p, ErrTicketInvalid
	}
	payload, sig := token[:dot], token[dot+1:]
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(payload))
	if !hmac.Equal([]byte(hex.EncodeToString(mac.Sum(nil))), []byte(sig)) {
		return p, ErrTicketInvalid
	}
	raw, err := base64.RawURLEncoding.DecodeString(payload)
	if err != nil {
		return p, ErrTicketInvalid
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		return p, ErrTicketInvalid
	}
	if p.Exp > 0 && time.Now().Unix() > p.Exp {
		return p, ErrTicketInvalid
	}
	return p, nil
}

func (m *Module) ticketSecret(ctx context.Context) (string, error) {
	if m.TicketSecret != "" {
		return m.TicketSecret, nil
	}
	if m.Cfg != nil {
		// OO_TICKET_SECRET 独立票据密钥；未配置回落 OO_JWT_SECRET（单密钥部署口径）。
		if v, err := m.Cfg.Get(ctx, "OO_TICKET_SECRET"); err == nil && v != "" {
			return v, nil
		}
		if v, err := m.Cfg.Get(ctx, "OO_JWT_SECRET"); err == nil && v != "" {
			return v, nil
		}
	}
	return "", errors.New("票据密钥未配置")
}

// ttlSeconds 读时长配置（秒）；未配置/非法取默认（OO_FILE_TICKET_TTL 600 / OO_CALLBACK_TICKET_TTL 43200）。
func (m *Module) ttlSeconds(ctx context.Context, key string, def int) int {
	if m.Cfg == nil {
		return def
	}
	v, err := m.Cfg.Get(ctx, key)
	if err != nil {
		return def
	}
	n, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil || n <= 0 {
		return def
	}
	return n
}

// SignOOJWT OnlyOffice 文档服务器 JWT（HS256，claims 全量透传）。
func (m *Module) SignOOJWT(ctx context.Context, claims map[string]any) (string, error) {
	secret, err := m.ooSecret(ctx)
	if err != nil {
		return "", err
	}
	t := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims(claims))
	return t.SignedString([]byte(secret))
}

// VerifyOOJWT 校验 OnlyOffice 回调 JWT（Node 口径：双校验必过，无 token / 验签失败一律失败）。
func (m *Module) VerifyOOJWT(ctx context.Context, token string) (map[string]any, error) {
	if strings.TrimSpace(token) == "" {
		return nil, errors.New("OnlyOffice 回调 JWT 缺失")
	}
	secret, err := m.ooSecret(ctx)
	if err != nil {
		return nil, err
	}
	claims := jwt.MapClaims{}
	parsed, err := jwt.ParseWithClaims(token, &claims, func(t *jwt.Token) (any, error) {
		return []byte(secret), nil
	})
	if err != nil || !parsed.Valid {
		return nil, errors.New("OnlyOffice JWT 校验失败")
	}
	return claims, nil
}

func (m *Module) ooSecret(ctx context.Context) (string, error) {
	if m.OOJWTSecret != "" {
		return m.OOJWTSecret, nil
	}
	if m.Cfg == nil {
		return "", errors.New("OnlyOffice JWT 密钥未配置")
	}
	v, err := m.Cfg.Get(ctx, "OO_JWT_SECRET")
	if err != nil {
		return "", err
	}
	if v == "" {
		return "", errors.New("OnlyOffice JWT 密钥未配置")
	}
	return v, nil
}

// NewTicketID 随机票据 id（回调路由用短 id）。
func NewTicketID() string {
	var b [12]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}
