package netdisk

// Task 6 fix round 1：OpenList 客户端自身 HTTP 超时（非 caller cancel）也不得写入全局登录冷却。

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// TestOpenListClientTimeoutNotCached 客户端超时后下一次请求必须重新登录，不能命中冷却。
func TestOpenListClientTimeoutNotCached(t *testing.T) {
	var logins int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/auth/login":
			n := atomic.AddInt32(&logins, 1)
			if n == 1 {
				time.Sleep(150 * time.Millisecond) // 首次触发 client timeout（ctx 未取消）
			}
			_, _ = w.Write([]byte(`{"code":200,"data":{"token":"tk"}}`))
		default:
			_, _ = w.Write([]byte(`{"code":200,"data":{}}`))
		}
	}))
	defer srv.Close()

	c := &OpenListHTTP{
		BaseURL: srv.URL, Username: "u", Password: "p",
		HTTP: &http.Client{Timeout: 30 * time.Millisecond},
	}
	if _, err := c.Call(context.Background(), http.MethodPost, "/api/fs/list", CallOpts{Body: map[string]any{}}); err == nil {
		t.Fatal("首次应超时报错")
	}
	if _, err := c.Call(context.Background(), http.MethodPost, "/api/fs/list", CallOpts{Body: map[string]any{}}); err != nil {
		t.Fatalf("客户端超时不得进入全局冷却，第二次应成功: %v（登录 %d 次）", err, atomic.LoadInt32(&logins))
	}
	if got := atomic.LoadInt32(&logins); got != 2 {
		t.Fatalf("登录次数 = %d，期望 2（client timeout 不缓存）", got)
	}
}
