package auth

import (
	"context"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"
)

type testBlacklist map[string]bool

func (b testBlacklist) Add(_ context.Context, token string, _ time.Duration) error {
	b[token] = true
	return nil
}
func (b testBlacklist) Contains(_ context.Context, token string) (bool, error) {
	return b[token], nil
}

func TestLoginAuthenticateAndLogout(t *testing.T) {
	hash, err := bcrypt.GenerateFromPassword([]byte("correct-password"), bcrypt.DefaultCost)
	if err != nil {
		t.Fatal(err)
	}
	users := StaticUserStore{Users: map[string]struct {
		Identity Identity
		Hash     string
	}{
		"admin": {Identity: Identity{ID: 7, Username: "admin", Nickname: "管理员", Role: "admin", TeamID: 1}, Hash: string(hash)},
	}}
	blacklist := testBlacklist{}
	svc := NewService(users, blacklist, "test-jwt-secret", time.Hour, time.Minute)
	if _, err := svc.Login(context.Background(), "admin", "wrong-password", ""); err == nil {
		t.Fatal("错误密码不应登录成功")
	}
	result, err := svc.Login(context.Background(), "admin", "correct-password", "web")
	if err != nil || result.Token == "" {
		t.Fatalf("正确登录失败: %v", err)
	}
	identity, err := svc.Authenticate(context.Background(), result.Token)
	if err != nil || identity.ID != 7 || identity.Role != "admin" {
		t.Fatalf("JWT 鉴权失败: %+v %v", identity, err)
	}
	if err := svc.Logout(context.Background(), result.Token, time.Now().Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Authenticate(context.Background(), result.Token); err == nil {
		t.Fatal("黑名单令牌不应继续通过")
	}
	if _, err := svc.Authenticate(context.Background(), "not-a-token"); err == nil {
		t.Fatal("伪令牌不应通过")
	}
}

func TestIssueTokenAndPasswordlessSignIn(t *testing.T) {
	hash, err := bcrypt.GenerateFromPassword([]byte("correct-password"), bcrypt.DefaultCost)
	if err != nil {
		t.Fatal(err)
	}
	users := StaticUserStore{Users: map[string]struct {
		Identity Identity
		Hash     string
	}{
		"admin": {Identity: Identity{ID: 7, Username: "admin", Nickname: "管理员", Role: "admin", TeamID: 1}, Hash: string(hash)},
	}}
	svc := NewService(users, nil, "test-jwt-secret", time.Hour, time.Minute)

	// Login 填 expires_in（秒）；client=="web" 走短时效
	result, err := svc.Login(context.Background(), "admin", "correct-password", "")
	if err != nil || result.ExpiresIn != 3600 {
		t.Fatalf("expires_in 不符: %+v %v", result, err)
	}
	web, err := svc.Login(context.Background(), "admin", "correct-password", "web")
	if err != nil || web.ExpiresIn != 60 {
		t.Fatalf("web 短时效不符: %+v %v", web, err)
	}

	// IssueToken 免密签发（微信登录复用）：签出令牌可通过鉴权，声明形状不变
	issued, err := svc.IssueToken(context.Background(), Identity{ID: 7, Username: "admin"}, "")
	if err != nil || issued.Token == "" || issued.ExpiresIn != 3600 {
		t.Fatalf("免密签发失败: %+v %v", issued, err)
	}
	ident, err := svc.Authenticate(context.Background(), issued.Token)
	if err != nil || ident.ID != 7 {
		t.Fatalf("免密令牌鉴权失败: %+v %v", ident, err)
	}
}

func TestFixtureAuth(t *testing.T) {
	var a FixtureAuth
	result, err := a.Login(context.Background(), "demo", "demo", "web")
	if err != nil || result.Token != "fixture-token" {
		t.Fatalf("fixture 登录失败: %+v %v", result, err)
	}
	if _, err := a.Authenticate(context.Background(), "fixture-token"); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Authenticate(context.Background(), "bad"); err == nil {
		t.Fatal("非 fixture 令牌不应通过")
	}
}
