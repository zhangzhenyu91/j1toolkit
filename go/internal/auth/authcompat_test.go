package auth

// 旧小程序 / Node 签发 JWT 兼容测试：jsonwebtoken 的 {uid,username,iat,exp} 声明形状
// 必须继续可通过（claimUID 兼容 float64 / json.Number / 十进制字符串）。

import (
	"context"
	"encoding/json"
	"strconv"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// nodeStyleToken 模拟 Node jsonwebtoken.sign({uid, username}, secret, {expiresIn}) 输出。
func nodeStyleToken(t *testing.T, secret string, claims jwt.MapClaims) string {
	t.Helper()
	token, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(secret))
	if err != nil {
		t.Fatal(err)
	}
	return token
}

func TestAuthenticateLegacyMiniProgramJWT(t *testing.T) {
	users := StaticUserStore{Users: map[string]struct {
		Identity Identity
		Hash     string
	}{
		"admin": {Identity: Identity{ID: 7, Username: "admin", Nickname: "管理员", Role: "admin", TeamID: 1}},
	}}
	svc := NewService(users, nil, "legacy-secret", time.Hour, time.Hour)
	now := time.Now()

	cases := map[string]jwt.MapClaims{
		"标准数值声明": {
			"uid": float64(7), "username": "admin",
			"iat": now.Unix(), "exp": now.Add(time.Hour).Unix(),
		},
		"json.Number uid": {
			"uid": json.Number("7"), "username": "admin",
			"iat": now.Unix(), "exp": now.Add(time.Hour).Unix(),
		},
		"字符串 uid": {
			"uid": "7", "username": "admin",
			"iat": now.Unix(), "exp": now.Add(time.Hour).Unix(),
		},
		"整型 uid": {
			"uid": 7, "username": "admin",
			"iat": now.Unix(), "exp": now.Add(time.Hour).Unix(),
		},
	}
	for name, claims := range cases {
		tok := nodeStyleToken(t, "legacy-secret", claims)
		ident, err := svc.Authenticate(context.Background(), tok)
		if err != nil || ident.ID != 7 || ident.Username != "admin" {
			t.Fatalf("%s 旧 JWT 兼容失败: %+v err=%v", name, ident, err)
		}
	}

	// 缺 uid / 非法 uid 必须拒绝
	for name, claims := range map[string]jwt.MapClaims{
		"缺 uid":   {"username": "admin", "exp": now.Add(time.Hour).Unix()},
		"uid 为 0": {"uid": float64(0), "exp": now.Add(time.Hour).Unix()},
		"uid 非数":  {"uid": "abc", "exp": now.Add(time.Hour).Unix()},
	} {
		tok := nodeStyleToken(t, "legacy-secret", claims)
		if _, err := svc.Authenticate(context.Background(), tok); err == nil {
			t.Fatalf("%s 不应通过鉴权", name)
		}
	}

	// 过期令牌拒绝
	tok := nodeStyleToken(t, "legacy-secret", jwt.MapClaims{
		"uid": float64(7), "exp": now.Add(-time.Minute).Unix(),
	})
	if _, err := svc.Authenticate(context.Background(), tok); err == nil {
		t.Fatal("过期令牌不应通过")
	}

	// 异常签名拒绝
	tok = nodeStyleToken(t, "other-secret", jwt.MapClaims{
		"uid": float64(7), "exp": now.Add(time.Hour).Unix(),
	})
	if _, err := svc.Authenticate(context.Background(), tok); err == nil {
		t.Fatal("异密钥令牌不应通过")
	}
}

// 确认 claimUID 边界（超大数值不 panic）。
func TestClaimUIDEdges(t *testing.T) {
	if got := claimUID(jwt.MapClaims{"uid": strconv.Itoa(1 << 40)}); got != 1<<40 {
		t.Fatalf("字符串大整数解析失败: %d", got)
	}
	if got := claimUID(jwt.MapClaims{"uid": true}); got != 0 {
		t.Fatalf("布尔 uid 应为 0: %d", got)
	}
}
