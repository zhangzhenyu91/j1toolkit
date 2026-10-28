package auth

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// tokenModeConfig 手动凭证模式配置（不触发任何登录流量）。
func tokenModeConfig(gateway string) Config {
	return Config{
		Gateway:      gateway,
		Mode:         "token",
		Token:        "tok1",
		RefreshToken: "rt1",
		Mtk:          "efbfbd071c72efbfbd071c72efbfbd071c72efbfbd071c72efbfbd071c72",
	}
}

// TestManagerTokenLogin token 模式登录：直接落定三件套，Generation=1。
func TestManagerTokenLogin(t *testing.T) {
	m := NewManager(tokenModeConfig("http://gw/"))
	creds, err := m.Login(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if creds.Token != "tok1" || creds.RefreshToken != "rt1" || creds.Generation != 1 {
		t.Fatalf("creds = %+v", creds)
	}
	if m.Gateway() != "http://gw/" {
		t.Fatalf("gateway = %s", m.Gateway())
	}
	// 再登录一次 Generation 继续自增（每批一次登录，跨批递增）
	creds2, err := m.Login(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if creds2.Generation != 2 {
		t.Fatalf("generation = %d, want 2", creds2.Generation)
	}
}

// TestManagerRefreshSingleflight 并发 401 后的 singleflight 刷新：
// N 个协程带同一 staleGen 调 Refresh，tokenRefresh 只打一次，全部拿到新代凭证。
func TestManagerRefreshSingleflight(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/tokenRefresh" {
			t.Errorf("意外路径: %s", r.URL.Path)
		}
		hits.Add(1)
		q := r.URL.Query()
		if q.Get("T") != "tok1" || q.Get("R") != "rt1" {
			t.Errorf("T/R 参数错: %s / %s", q.Get("T"), q.Get("R"))
		}
		time.Sleep(50 * time.Millisecond) // 拉长刷新窗口，让并发真正撞上
		fmt.Fprint(w, `{"accessToken":"tok2"}`)
	}))
	defer srv.Close()

	m := NewManager(tokenModeConfig(srv.URL + "/"))
	first, err := m.Login(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	const n = 8
	var wg sync.WaitGroup
	results := make([]Credentials, n)
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i], errs[i] = m.Refresh(context.Background(), first.Generation)
		}(i)
	}
	wg.Wait()
	for i := 0; i < n; i++ {
		if errs[i] != nil {
			t.Fatalf("goroutine %d: %v", i, errs[i])
		}
		if results[i].Token != "tok2" || results[i].Generation != 2 {
			t.Fatalf("goroutine %d: %+v", i, results[i])
		}
	}
	if got := hits.Load(); got != 1 {
		t.Fatalf("tokenRefresh 命中 %d 次, want 1", got)
	}
}

// TestManagerRefreshStaleGen 已被其他协程刷新过：直接返回新快照，不发请求。
func TestManagerRefreshStaleGen(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		fmt.Fprint(w, `{"accessToken":"tok2"}`)
	}))
	defer srv.Close()
	m := NewManager(tokenModeConfig(srv.URL + "/"))
	if _, err := m.Login(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Refresh(context.Background(), 1); err != nil {
		t.Fatal(err)
	}
	// staleGen=1 已过期（当前 2）：不再发请求
	creds, err := m.Refresh(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	if creds.Generation != 2 || creds.Token != "tok2" {
		t.Fatalf("creds = %+v", creds)
	}
	if hits.Load() != 1 {
		t.Fatalf("tokenRefresh 命中 %d 次, want 1", hits.Load())
	}
}

// TestManagerRefreshExpired refresh 凭证过期：报错且凭证不变。
func TestManagerRefreshExpired(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"tokenRefreshExpired":true}`)
	}))
	defer srv.Close()
	m := NewManager(tokenModeConfig(srv.URL + "/"))
	before, err := m.Login(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	_, err = m.Refresh(context.Background(), before.Generation)
	if err == nil {
		t.Fatal("过期应报错")
	}
	after := m.Snapshot()
	if after.Token != "tok1" || after.Generation != before.Generation {
		t.Fatalf("失败后凭证不应变化: %+v", after)
	}
}

// TestManagerRefreshCtxCancel 等待刷新期间 ctx 取消应返回 ctx 错误。
func TestManagerRefreshCtxCancel(t *testing.T) {
	block := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-block
		fmt.Fprint(w, `{"accessToken":"tok2"}`)
	}))
	defer srv.Close()
	defer close(block)

	m := NewManager(tokenModeConfig(srv.URL + "/"))
	first, err := m.Login(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(30 * time.Millisecond)
		cancel()
	}()
	if _, err := m.Refresh(ctx, first.Generation); err == nil {
		t.Fatal("ctx 取消应报错")
	}
}
