package sgccclockin

// 协议并发回归：jsonx 的 HTTP 往返必须在池锁之外、并发取池 uniID 不重复、
// 同通道补池单飞（并发请求只拉一次 ads）。

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// newJSONXTestProtocol 指向 httptest 的 jsonx 协议客户端（池由调用方按用例预置）。
func newJSONXTestProtocol(t *testing.T, k Keys, srv *httptest.Server) *Protocol {
	t.Helper()
	p := NewProtocol(k)
	p.Scheme = "http"
	host := strings.TrimPrefix(srv.URL, "http://")
	p.HostH5, p.HostZB = host, host
	return p
}

// TestCallJSONXHTTPOutsidePoolLock jsonx 的 HTTP 往返不得占用池锁：
// 首个请求被服务端挂住时，第二个请求必须仍能到达服务端（旧实现整段持锁会排队）。
func TestCallJSONXHTTPOutsidePoolLock(t *testing.T) {
	k := testKeys(t)
	firstIn := make(chan struct{})
	releaseFirst := make(chan struct{})
	secondIn := make(chan struct{})
	var onceFirst, onceSecond sync.Once
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		if r.Header.Get("uniID") == "uid-1" {
			onceFirst.Do(func() { close(firstIn) })
			<-releaseFirst
		} else {
			onceSecond.Do(func() { close(secondIn) })
		}
		ct, _ := k.jsonxEncryptBody("slapp", map[string]any{"statusCode": 200}, 1)
		_, _ = w.Write([]byte(ct))
	}))
	defer srv.Close()

	p := newJSONXTestProtocol(t, k, srv)
	p.poolX = []string{"uid-1", "uid-2"}
	p.poolXA = time.Now()

	ctx := context.Background()
	opts := CallOpts{Tenant: "slapp"}
	done := make(chan error, 2)
	go func() {
		_, err := p.CallJSONX(ctx, "clockin/calendar/dayNew", map[string]any{}, opts)
		done <- err
	}()
	select {
	case <-firstIn:
	case <-time.After(2 * time.Second):
		t.Fatalf("首个请求未到达服务端")
	}
	go func() {
		_, err := p.CallJSONX(ctx, "clockin/calendar/dayNew", map[string]any{}, opts)
		done <- err
	}()
	select {
	case <-secondIn:
	case <-time.After(2 * time.Second):
		close(releaseFirst)
		t.Fatalf("第二个请求被池锁阻塞：CallJSONX 的 HTTP 必须在锁外")
	}
	close(releaseFirst)
	for i := 0; i < 2; i++ {
		if err := <-done; err != nil {
			t.Fatalf("并发调用失败: %v", err)
		}
	}
}

// TestCallJSONXConcurrentUniIDUnique 并发调用取池不重复：每枚 uniID 只被一个请求消耗。
func TestCallJSONXConcurrentUniIDUnique(t *testing.T) {
	k := testKeys(t)
	const n = 16
	var mu sync.Mutex
	seen := map[string]int{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		mu.Lock()
		seen[r.Header.Get("uniID")]++
		mu.Unlock()
		ct, _ := k.jsonxEncryptBody("slapp", map[string]any{"statusCode": 200}, 1)
		_, _ = w.Write([]byte(ct))
	}))
	defer srv.Close()

	p := newJSONXTestProtocol(t, k, srv)
	for i := 0; i < n; i++ {
		p.poolX = append(p.poolX, "uid-"+strings.Repeat("x", i+1))
	}
	p.poolXA = time.Now()

	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := p.CallJSONX(context.Background(), "clockin/calendar/dayNew",
				map[string]any{}, CallOpts{Tenant: "slapp"}); err != nil {
				t.Errorf("并发调用失败: %v", err)
			}
		}()
	}
	wg.Wait()

	mu.Lock()
	defer mu.Unlock()
	if len(seen) != n {
		t.Fatalf("并发取池应得到 %d 个不同 uniID，实际 %d", n, len(seen))
	}
	for id, c := range seen {
		if c != 1 {
			t.Fatalf("uniID %q 被重复使用 %d 次", id, c)
		}
	}
}

// TestPoolRefillSingleFlight 池空时并发调用只补池一次：ads 请求数=1，且业务请求确实并行
// （旧实现整段持锁，并发度恒为 1）。
func TestPoolRefillSingleFlight(t *testing.T) {
	k := testKeys(t)
	const n = 8
	var adsCalls int32
	var cur, peak int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		if strings.HasSuffix(r.URL.Path, "/ads") {
			atomic.AddInt32(&adsCalls, 1)
			ct, _ := k.jsonxEncryptBody("slapp", map[string]any{
				"statusCode": 200,
				"data": map[string]any{"list": []any{
					"p1", "p2", "p3", "p4", "p5", "p6", "p7", "p8",
				}},
			}, 1)
			_, _ = w.Write([]byte(ct))
			return
		}
		c := atomic.AddInt32(&cur, 1)
		for {
			old := atomic.LoadInt32(&peak)
			if c <= old || atomic.CompareAndSwapInt32(&peak, old, c) {
				break
			}
		}
		time.Sleep(40 * time.Millisecond)
		atomic.AddInt32(&cur, -1)
		ct, _ := k.jsonxEncryptBody("slapp", map[string]any{"statusCode": 200}, 1)
		_, _ = w.Write([]byte(ct))
	}))
	defer srv.Close()

	p := newJSONXTestProtocol(t, k, srv)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := p.CallJSONX(context.Background(), "clockin/calendar/dayNew",
				map[string]any{}, CallOpts{Tenant: "slapp"}); err != nil {
				t.Errorf("并发调用失败: %v", err)
			}
		}()
	}
	wg.Wait()

	if got := atomic.LoadInt32(&adsCalls); got != 1 {
		t.Fatalf("池空并发补池应只拉一次 ads，实际 %d 次", got)
	}
	if got := atomic.LoadInt32(&peak); got < 2 {
		t.Fatalf("补池后业务请求应并行（峰值 ≥2），实际峰值 %d：HTTP 仍在锁内", got)
	}
}
