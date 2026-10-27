package netdisk

// OpenList 运行时配置与热更新测试：Cfg 非空优先、静态字段回退、凭证变化强制重登；不访问真实 OpenList。

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

// mapCfg 配置桩（可变，模拟库内热更新）。
type mapCfg map[string]string

func (c mapCfg) Get(_ context.Context, key string) (string, error) { return c[key], nil }

// credProbe 假 OpenList：记录登录凭证并放行。
func credProbe(t *testing.T, logins *int64, gotUser, gotPass *string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/api/auth/login" {
			atomic.AddInt64(logins, 1)
			var cred map[string]string
			raw, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
			_ = json.Unmarshal(raw, &cred)
			*gotUser, *gotPass = cred["username"], cred["password"]
			_, _ = w.Write([]byte(`{"code":200,"data":{"token":"tk"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"code":200,"data":{"content":[]}}`))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestOpenListRuntimeCredsHotUpdate(t *testing.T) {
	var logins int64
	var user, pass string
	srv := credProbe(t, &logins, &user, &pass)
	cfg := mapCfg{
		"NETDISK_API_URL":  srv.URL,
		"NETDISK_USERNAME": "runtime-user",
		"NETDISK_PASSWORD": "runtime-pass",
	}
	// 静态字段故意与运行时不同：应以运行时为准。
	ol := &OpenListHTTP{BaseURL: "http://static.invalid", Username: "static-user", Password: "static-pass", Cfg: cfg}
	if _, err := ol.Call(context.Background(), http.MethodPost, "/api/fs/list", CallOpts{Body: `{}`}); err != nil {
		t.Fatalf("Call: %v", err)
	}
	if user != "runtime-user" || pass != "runtime-pass" {
		t.Fatalf("登录凭证应取运行时配置: %q %q", user, pass)
	}
	if n := atomic.LoadInt64(&logins); n != 1 {
		t.Fatalf("登录次数 = %d want 1", n)
	}
	// 未变更：复用 token 不重登。
	if _, err := ol.Call(context.Background(), http.MethodPost, "/api/fs/list", CallOpts{Body: `{}`}); err != nil {
		t.Fatalf("第二次 Call: %v", err)
	}
	if n := atomic.LoadInt64(&logins); n != 1 {
		t.Fatalf("凭证未变不应重登: %d", n)
	}
	// 热更新密码：下一跳强制重登（tokenID 不匹配）。
	cfg["NETDISK_PASSWORD"] = "rotated-pass"
	if _, err := ol.Call(context.Background(), http.MethodPost, "/api/fs/list", CallOpts{Body: `{}`}); err != nil {
		t.Fatalf("热更新后 Call: %v", err)
	}
	if n := atomic.LoadInt64(&logins); n != 2 {
		t.Fatalf("凭证变化应重登: 登录次数 = %d want 2", n)
	}
	if pass != "rotated-pass" {
		t.Fatalf("重登应使用新密码: %q", pass)
	}
}

func TestOpenListStaticFallback(t *testing.T) {
	var logins int64
	var user, pass string
	srv := credProbe(t, &logins, &user, &pass)
	// Cfg 为空值/nil 时回退静态字段（向后兼容静态测试配置）。
	ol := &OpenListHTTP{BaseURL: srv.URL, Username: "u", Password: "p", Cfg: mapCfg{}}
	if err := ol.EnsureConfigured(); err != nil {
		t.Fatalf("EnsureConfigured: %v", err)
	}
	if _, err := ol.Call(context.Background(), http.MethodPost, "/api/fs/list", CallOpts{Body: `{}`}); err != nil {
		t.Fatalf("Call: %v", err)
	}
	if user != "u" || pass != "p" {
		t.Fatalf("应回退静态凭证: %q %q", user, pass)
	}
	// 静态也未配置 → ErrNotConfigured。
	ol2 := &OpenListHTTP{BaseURL: srv.URL, Cfg: mapCfg{"NETDISK_API_URL": ""}}
	if err := ol2.EnsureConfigured(); err != ErrNotConfigured {
		t.Fatalf("未配置应报 ErrNotConfigured, got %v", err)
	}
}

func TestBaseDirsHotUpdate(t *testing.T) {
	m := New(nil)
	m.Base = BaseDirs{PersonalRoot: "/static/my", PublicRoot: "/static/pub"}
	// Cfg 缺值回退静态。
	got := m.baseDirs(context.Background())
	if got.PersonalRoot != "/static/my" || got.PublicRoot != "/static/pub" {
		t.Fatalf("应回退静态空间根: %+v", got)
	}
	// 运行时配置非空优先（热更新）。
	m.Cfg = mapCfg{"NETDISK_PERSONAL_ROOT": "/rt/my", "NETDISK_PUBLIC_ROOT": "/rt/pub"}
	got = m.baseDirs(context.Background())
	if got.PersonalRoot != "/rt/my" || got.PublicRoot != "/rt/pub" {
		t.Fatalf("应取运行时空间根: %+v", got)
	}
	// 单键热更新：只覆盖有值的键。
	m.Cfg = mapCfg{"NETDISK_PUBLIC_ROOT": "/rt2/pub"}
	got = m.baseDirs(context.Background())
	if got.PersonalRoot != "/static/my" || got.PublicRoot != "/rt2/pub" {
		t.Fatalf("单键覆盖错误: %+v", got)
	}
}
