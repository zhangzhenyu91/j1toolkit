package netdisk

// OpenList 性能回归：每次操作只解析一次配置、并发首登单飞、401 仅重试一次。
// 47h TTL / 60s 失败冷却口径不变（runtimecfg_test 覆盖热更新）。

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// countingCfg 记录配置读取次数（每次 Get 计 1）。
type countingCfg struct {
	mu     sync.Mutex
	values map[string]string
	reads  int
}

func (c *countingCfg) Get(_ context.Context, key string) (string, error) {
	c.mu.Lock()
	c.reads++
	c.mu.Unlock()
	return c.values[key], nil
}

func (c *countingCfg) reset() {
	c.mu.Lock()
	c.reads = 0
	c.mu.Unlock()
}

func (c *countingCfg) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.reads
}

// TestOpenListSingleConfigResolution 单次操作配置读取 ≤3（每个键至多一次）。
func TestOpenListSingleConfigResolution(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/api/auth/login":
			_, _ = w.Write([]byte(`{"code":200,"data":{"token":"tk"}}`))
		case r.URL.Path == "/api/fs/get":
			_, _ = w.Write([]byte(`{"code":200,"data":{"is_dir":false,"sign":"s1"}}`))
		case strings.HasPrefix(r.URL.Path, "/d/"):
			_, _ = w.Write([]byte("DATA"))
		default:
			_, _ = w.Write([]byte(`{"code":200,"data":{"content":[]}}`))
		}
	}))
	defer srv.Close()
	cfg := &countingCfg{values: map[string]string{
		"NETDISK_API_URL":  srv.URL,
		"NETDISK_USERNAME": "u",
		"NETDISK_PASSWORD": "p",
	}}
	ol := &OpenListHTTP{Cfg: cfg}

	if _, err := ol.Call(context.Background(), http.MethodPost, "/api/fs/list", CallOpts{Body: `{}`}); err != nil {
		t.Fatalf("Call: %v", err)
	}
	if got := cfg.count(); got > 3 {
		t.Fatalf("单次 Call 配置读取 = %d want ≤3（每键一次）", got)
	}

	cfg.reset()
	rc, _, err := ol.DownloadStream(context.Background(), "/x/a.txt")
	if err != nil {
		t.Fatalf("DownloadStream: %v", err)
	}
	_ = rc.Close()
	if got := cfg.count(); got > 3 {
		t.Fatalf("单次 DownloadStream 配置读取 = %d want ≤3", got)
	}
}

// TestOpenListConcurrentLoginSingleFlight 10 个并发首登只向 OpenList 发一次 login。
func TestOpenListConcurrentLoginSingleFlight(t *testing.T) {
	var logins int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/api/auth/login" {
			atomic.AddInt64(&logins, 1)
			time.Sleep(25 * time.Millisecond) // 拉长首登窗口，让并发请求都进入登录路径
			_, _ = w.Write([]byte(`{"code":200,"data":{"token":"tk"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"code":200,"data":{"content":[]}}`))
	}))
	defer srv.Close()
	ol := &OpenListHTTP{BaseURL: srv.URL, Username: "u", Password: "p"}

	const n = 10
	start := make(chan struct{})
	errs := make(chan error, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, err := ol.Call(context.Background(), http.MethodPost, "/api/fs/list", CallOpts{Body: `{}`})
			errs <- err
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("并发 Call 失败: %v", err)
		}
	}
	if got := atomic.LoadInt64(&logins); got != 1 {
		t.Fatalf("并发首登 login 次数 = %d want 1", got)
	}
}

// TestOpenListUnauthorizedRetriesOnce 401 只重试一次（重登 + 重放），绝不二次重试。
func TestOpenListUnauthorizedRetriesOnce(t *testing.T) {
	t.Run("重登后成功", func(t *testing.T) {
		var logins, listCalls int64
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			if r.URL.Path == "/api/auth/login" {
				n := atomic.AddInt64(&logins, 1)
				_, _ = fmt.Fprintf(w, `{"code":200,"data":{"token":"tk-%d"}}`, n)
				return
			}
			atomic.AddInt64(&listCalls, 1)
			if r.Header.Get("Authorization") == "tk-1" {
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = w.Write([]byte(`{"code":401,"message":"token expired"}`))
				return
			}
			_, _ = w.Write([]byte(`{"code":200,"data":{"content":[]}}`))
		}))
		defer srv.Close()
		ol := &OpenListHTTP{BaseURL: srv.URL, Username: "u", Password: "p"}
		if _, err := ol.Call(context.Background(), http.MethodPost, "/api/fs/list", CallOpts{Body: `{}`}); err != nil {
			t.Fatalf("401 重登后应成功: %v", err)
		}
		if got := atomic.LoadInt64(&listCalls); got != 2 {
			t.Fatalf("401 后请求次数 = %d want 2（原请求 + 一次重放）", got)
		}
		if got := atomic.LoadInt64(&logins); got != 2 {
			t.Fatalf("登录次数 = %d want 2（首登 + 强制重登）", got)
		}
	})

	t.Run("持续401不无限重试", func(t *testing.T) {
		var logins, listCalls int64
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			if r.URL.Path == "/api/auth/login" {
				n := atomic.AddInt64(&logins, 1)
				_, _ = fmt.Fprintf(w, `{"code":200,"data":{"token":"tk-%d"}}`, n)
				return
			}
			atomic.AddInt64(&listCalls, 1)
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"code":401,"message":"token expired"}`))
		}))
		defer srv.Close()
		ol := &OpenListHTTP{BaseURL: srv.URL, Username: "u", Password: "p"}
		_, err := ol.Call(context.Background(), http.MethodPost, "/api/fs/list", CallOpts{Body: `{}`})
		var ue *UpstreamError
		if !asUpstream(err, &ue) || ue.UpstreamCode != 401 {
			t.Fatalf("持续 401 错误归一错误: %v", err)
		}
		if got := atomic.LoadInt64(&listCalls); got != 2 {
			t.Fatalf("持续 401 请求次数 = %d want 2（只重试一次）", got)
		}
		if got := atomic.LoadInt64(&logins); got != 2 {
			t.Fatalf("持续 401 登录次数 = %d want 2", got)
		}
	})
}
