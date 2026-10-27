// Package auth 提供 JWT 登录、用户状态校验与退出黑名单能力。
package auth

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/crypto/bcrypt"
)

var (
	ErrInvalidCredentials = errors.New("账号或密码不正确")
	ErrUnauthorized       = errors.New("未登录或登录已过期")
)

// Identity 是已通过 JWT 与数据库状态校验的用户身份。
type Identity struct {
	ID       int64  `json:"id"`
	Username string `json:"username"`
	Nickname string `json:"nickname"`
	Role     string `json:"role"`
	TeamID   int64  `json:"team_id"`
}

// LoginResult 登录成功后返回的令牌与用户信息（expires_in 为令牌有效期秒数，与 Node sign() 同口径）。
type LoginResult struct {
	Token     string   `json:"token"`
	ExpiresIn int64    `json:"expires_in"`
	User      Identity `json:"user"`
}

// UserStore 读取登录与鉴权所需的用户记录。
type UserStore interface {
	ByUsername(ctx context.Context, username string) (Identity, string, error)
	ByID(ctx context.Context, id int64) (Identity, error)
}

// Blacklist 是可选的退出令牌黑名单；nil 表示降级为只校验 JWT 与用户状态。
type Blacklist interface {
	Add(ctx context.Context, token string, ttl time.Duration) error
	Contains(ctx context.Context, token string) (bool, error)
}

// Service 统一认证服务。
type Service struct {
	users     UserStore
	blacklist Blacklist
	secret    []byte
	expires   time.Duration
	webExpire time.Duration
}

func NewService(users UserStore, blacklist Blacklist, jwtSecret string, expires, webExpires time.Duration) *Service {
	if expires <= 0 {
		expires = 7 * 24 * time.Hour
	}
	if webExpires <= 0 {
		webExpires = 12 * time.Hour
	}
	return &Service{users: users, blacklist: blacklist, secret: []byte(jwtSecret), expires: expires, webExpire: webExpires}
}

func (s *Service) Login(ctx context.Context, username, password, client string) (LoginResult, error) {
	username = strings.TrimSpace(username)
	if username == "" || password == "" {
		return LoginResult{}, ErrInvalidCredentials
	}
	identity, hash, err := s.users.ByUsername(ctx, username)
	if err != nil || hash == "" || bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) != nil {
		return LoginResult{}, ErrInvalidCredentials
	}
	return s.IssueToken(ctx, identity, client)
}

// IssueToken 为已通过校验的用户签发 JWT（微信登录等免密场景复用；client=="web" 走短时效）。
// 声明形状 {uid,username,iat,exp} 与 Node jsonwebtoken 签发一致。
func (s *Service) IssueToken(_ context.Context, user Identity, client string) (LoginResult, error) {
	ttl := s.expires
	if client == "web" {
		ttl = s.webExpire
	}
	now := time.Now()
	claims := jwt.MapClaims{"uid": user.ID, "username": user.Username, "iat": now.Unix(), "exp": now.Add(ttl).Unix()}
	token, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(s.secret)
	if err != nil {
		return LoginResult{}, err
	}
	return LoginResult{Token: token, ExpiresIn: int64(ttl / time.Second), User: user}, nil
}

func (s *Service) Authenticate(ctx context.Context, token string) (Identity, error) {
	if token == "" {
		return Identity{}, ErrUnauthorized
	}
	claims := jwt.MapClaims{}
	parsed, err := jwt.ParseWithClaims(token, &claims, func(t *jwt.Token) (any, error) {
		if t.Method != jwt.SigningMethodHS256 {
			return nil, fmt.Errorf("unexpected signing method: %s", t.Method.Alg())
		}
		return s.secret, nil
	})
	if err != nil || !parsed.Valid {
		return Identity{}, ErrUnauthorized
	}
	if s.blacklist != nil {
		blocked, err := s.blacklist.Contains(ctx, token)
		if err == nil && blocked {
			return Identity{}, ErrUnauthorized
		}
	}
	uid := claimUID(claims)
	if uid <= 0 {
		return Identity{}, ErrUnauthorized
	}
	identity, err := s.users.ByID(ctx, uid)
	if err != nil {
		return Identity{}, ErrUnauthorized
	}
	return identity, nil
}

// claimUID 解析 uid 声明，兼容旧小程序/Node 签发的 JWT：
// jsonwebtoken 数值声明解出 float64，json.Number / 十进制字符串也接受。
func claimUID(claims jwt.MapClaims) int64 {
	switch v := claims["uid"].(type) {
	case float64:
		return int64(v)
	case int64:
		return v
	case int:
		return int64(v)
	case json.Number:
		n, err := v.Int64()
		if err != nil {
			return 0
		}
		return n
	case string:
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			return 0
		}
		return n
	default:
		return 0
	}
}

func (s *Service) Logout(ctx context.Context, token string, expiresAt time.Time) error {
	if s.blacklist == nil {
		return nil
	}
	ttl := time.Until(expiresAt)
	if ttl < time.Second {
		ttl = time.Second
	}
	return s.blacklist.Add(ctx, token, ttl)
}

// StaticUserStore 用于开发测试的内存用户源；生产请使用 SQLUserStore。
type StaticUserStore struct {
	Users map[string]struct {
		Identity Identity
		Hash     string
	}
}

func (s StaticUserStore) ByUsername(_ context.Context, username string) (Identity, string, error) {
	entry, ok := s.Users[username]
	if !ok {
		return Identity{}, "", sql.ErrNoRows
	}
	return entry.Identity, entry.Hash, nil
}

func (s StaticUserStore) ByID(_ context.Context, id int64) (Identity, error) {
	for _, entry := range s.Users {
		if entry.Identity.ID == id {
			return entry.Identity, nil
		}
	}
	return Identity{}, sql.ErrNoRows
}

// TimingSafeEqual 供登录错误分支以外的简单密钥比较使用，避免普通字符串直接比较。
func TimingSafeEqual(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}
