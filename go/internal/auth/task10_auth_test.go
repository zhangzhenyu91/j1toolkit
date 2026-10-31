package auth

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// ttlBlacklist 记录 Add 的 TTL，用于验证登出黑名单过期时间。
type ttlBlacklist struct {
	ttl time.Duration
}

func (b *ttlBlacklist) Add(_ context.Context, _ string, ttl time.Duration) error {
	b.ttl = ttl
	return nil
}

func (b *ttlBlacklist) Contains(context.Context, string) (bool, error) { return false, nil }

// errBlacklist 存储故障黑名单。
type errBlacklist struct{}

func (errBlacklist) Add(context.Context, string, time.Duration) error {
	return errors.New("redis down")
}
func (errBlacklist) Contains(context.Context, string) (bool, error) {
	return false, errors.New("redis down")
}

func newTestService(t *testing.T, bl Blacklist, expires time.Duration) *Service {
	t.Helper()
	users := StaticUserStore{Users: map[string]struct {
		Identity Identity
		Hash     string
	}{
		"admin": {Identity: Identity{ID: 7, Username: "admin", Nickname: "管理员", Role: "admin", TeamID: 1}},
	}}
	return NewService(users, bl, "test-jwt-secret", expires, time.Minute)
}

// 登出黑名单 TTL 必须到 token exp，而不是调用方随意给出的时间（原实现按 handler 的 24h）。
func TestLogoutBlacklistTTLUsesTokenExpiry(t *testing.T) {
	bl := &ttlBlacklist{}
	svc := newTestService(t, bl, time.Hour)
	issued, err := svc.IssueToken(context.Background(), Identity{ID: 7, Username: "admin"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.Logout(context.Background(), issued.Token, time.Now().Add(48*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if bl.ttl < 55*time.Minute || bl.ttl > time.Hour {
		t.Fatalf("黑名单 TTL 应为 token 剩余有效期（约 1h），实际 %s", bl.ttl)
	}
}

// 已过期令牌登出也按自身 exp 计算（最短 1 秒），不回退到调用方给的 48h。
func TestLogoutExpiredTokenUsesItsOwnExpiry(t *testing.T) {
	bl := &ttlBlacklist{}
	svc := newTestService(t, bl, time.Hour)
	claims := jwt.MapClaims{"uid": 7, "username": "admin", "exp": time.Now().Add(-time.Hour).Unix()}
	token, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte("test-jwt-secret"))
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.Logout(context.Background(), token, time.Now().Add(48*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if bl.ttl > time.Second {
		t.Fatalf("过期令牌 TTL 应压到最短 1s，实际 %s", bl.ttl)
	}
}

// 黑名单存储故障时受保护请求必须 fail-closed（返回未登录）并触发告警钩子。
func TestAuthenticateBlacklistErrorFailClosedAndAlerts(t *testing.T) {
	svc := newTestService(t, errBlacklist{}, time.Hour)
	issued, err := svc.IssueToken(context.Background(), Identity{ID: 7, Username: "admin"}, "")
	if err != nil {
		t.Fatal(err)
	}
	var alerted error
	svc.OnBlacklistError = func(err error) { alerted = err }
	if _, err := svc.Authenticate(context.Background(), issued.Token); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("黑名单存储故障应 fail-closed 返回 ErrUnauthorized，实际 %v", err)
	}
	if alerted == nil {
		t.Fatal("黑名单存储故障应触发告警钩子")
	}
}

// 无黑名单（本地 fixture）行为保持不变。
func TestAuthenticateWithoutBlacklistUnaffected(t *testing.T) {
	svc := newTestService(t, nil, time.Hour)
	issued, err := svc.IssueToken(context.Background(), Identity{ID: 7, Username: "admin"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Authenticate(context.Background(), issued.Token); err != nil {
		t.Fatalf("无黑名单时正常令牌应通过：%v", err)
	}
}

// JWT_EXPIRES / JWT_WEB_EXPIRES 的 d/h/m/s 写法解析。
func TestParseTokenTTL(t *testing.T) {
	cases := map[string]time.Duration{
		"7d":   7 * 24 * time.Hour,
		"12h":  12 * time.Hour,
		"30m":  30 * time.Minute,
		"90s":  90 * time.Second,
		"3600": time.Hour,
		" 7d ": 7 * 24 * time.Hour,
	}
	for raw, want := range cases {
		got, err := ParseTokenTTL(raw)
		if err != nil || got != want {
			t.Fatalf("ParseTokenTTL(%q) = %s, %v; want %s", raw, got, err, want)
		}
	}
	if _, err := ParseTokenTTL("bogus"); err == nil {
		t.Fatal("非法有效期应报错")
	}
	if _, err := ParseTokenTTL("0d"); err == nil {
		t.Fatal("零有效期应报错")
	}
}

// Redis 黑名单故障告警 30s 节流：故障期连续请求只告警一次，窗口过后再告警。
func TestBlacklistAlertThrottled(t *testing.T) {
	svc := newTestService(t, errBlacklist{}, time.Hour)
	issued, err := svc.IssueToken(context.Background(), Identity{ID: 7, Username: "admin"}, "")
	if err != nil {
		t.Fatal(err)
	}
	alerts := 0
	svc.OnBlacklistError = func(error) { alerts++ }
	for i := 0; i < 3; i++ {
		if _, err := svc.Authenticate(context.Background(), issued.Token); !errors.Is(err, ErrUnauthorized) {
			t.Fatalf("第 %d 次仍应 fail-closed: %v", i, err)
		}
	}
	if alerts != 1 {
		t.Fatalf("30s 内应只告警 1 次，实际 %d", alerts)
	}
	svc.lastBlacklistAlert = time.Now().Add(-31 * time.Second)
	if _, err := svc.Authenticate(context.Background(), issued.Token); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("窗口后仍应 fail-closed: %v", err)
	}
	if alerts != 2 {
		t.Fatalf("窗口过后应再次告警，实际 %d", alerts)
	}
}
