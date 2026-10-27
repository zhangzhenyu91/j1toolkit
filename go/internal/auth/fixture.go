package auth

import (
	"context"
	"time"
)

// FixtureAuth 是仅用于本地演示的认证，不接受生产 JWT。
type FixtureAuth struct{}

func (FixtureAuth) Login(_ context.Context, username, password, _ string) (LoginResult, error) {
	if username == "" || password == "" {
		return LoginResult{}, ErrInvalidCredentials
	}
	return LoginResult{Token: "fixture-token", ExpiresIn: 86400, User: Identity{ID: 1, Username: username, Nickname: "本地演示管理员", Role: "admin", TeamID: 1}}, nil
}

// IssueToken 免密签发（微信登录等场景的 fixture 对应物）。
func (FixtureAuth) IssueToken(_ context.Context, user Identity, _ string) (LoginResult, error) {
	return LoginResult{Token: "fixture-token", ExpiresIn: 86400, User: user}, nil
}

func (FixtureAuth) Authenticate(_ context.Context, token string) (Identity, error) {
	if token != "fixture-token" {
		return Identity{}, ErrUnauthorized
	}
	return Identity{ID: 1, Username: "fixture-admin", Nickname: "本地演示管理员", Role: "admin", TeamID: 1}, nil
}

func (FixtureAuth) Logout(context.Context, string, time.Time) error { return nil }
