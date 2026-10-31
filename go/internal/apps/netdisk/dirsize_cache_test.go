package netdisk

// dirsize 缓存与单飞回归：5 分钟/128 条缓存、同 key 单飞、leader 脱离请求取消、
// 全部写操作成功后失效。

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// newDirSizeModule 构造 dirsize 测试模块：stub 目录树 /nd/my/alice/docs{/sub}。
func newDirSizeModule(t *testing.T) (*Module, *stubOL, http.Handler) {
	t.Helper()
	ol := newStubOL()
	ol.listData["/nd/my/alice/docs"] = []mapItemSrc{
		{Name: "a.txt", Size: 3, Modified: "2026-10-27 10:00:00"},
		{Name: "sub", IsDir: true, Modified: "2026-10-27 10:00:00"},
	}
	ol.listData["/nd/my/alice/docs/sub"] = []mapItemSrc{
		{Name: "b.txt", Size: 5, Modified: "2026-10-27 10:00:00"},
	}
	m := New(nil)
	m.OL = ol
	m.Base = BaseDirs{PersonalRoot: "/nd/my", PublicRoot: "/nd/pub"}
	m.JWTSecret = "s"
	return m, ol, newTestRouter(t, m)
}

// getDirSize 发一次 /dirsize 请求并返回统计（失败即终止）。
func getDirSize(t *testing.T, r http.Handler) map[string]any {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/netdisk/dirsize?space=my&path=/docs", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("dirsize 失败: %d %s", rec.Code, rec.Body.String())
	}
	var env struct {
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("dirsize 响应解析失败: %v", err)
	}
	if env.Data["size"].(float64) != 8 || env.Data["files"].(float64) != 2 || env.Data["dirs"].(float64) != 1 {
		t.Fatalf("dirsize 统计错误: %v", env.Data)
	}
	return env.Data
}

// TestDirSizeCacheHit 同 key 5 分钟内命中缓存，不再遍历上游。
func TestDirSizeCacheHit(t *testing.T) {
	_, ol, r := newDirSizeModule(t)
	getDirSize(t, r)
	if got := ol.listCount("/nd/my/alice/docs"); got != 1 {
		t.Fatalf("首次遍历 docs 次数 = %d want 1", got)
	}
	if got := ol.totalListCalls(); got != 2 {
		t.Fatalf("首次遍历总次数 = %d want 2", got)
	}
	getDirSize(t, r)
	if got := ol.totalListCalls(); got != 2 {
		t.Fatalf("缓存命中后仍遍历上游: %d want 2", got)
	}
}

// TestDirSizeSingleFlight 同 key 并发请求只由一个 leader 遍历上游。
func TestDirSizeSingleFlight(t *testing.T) {
	_, ol, r := newDirSizeModule(t)
	release := make(chan struct{})
	started := make(chan struct{}, 1)
	ol.listHook = func(ctx context.Context, abs string) error {
		if abs != "/nd/my/alice/docs" {
			return nil
		}
		select {
		case started <- struct{}{}:
		default:
		}
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}

	const n = 8
	var wg sync.WaitGroup
	recs := make([]*httptest.ResponseRecorder, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			req := httptest.NewRequest(http.MethodGet, "/api/v1/netdisk/dirsize?space=my&path=/docs", nil)
			rec := httptest.NewRecorder()
			r.ServeHTTP(rec, req)
			recs[i] = rec
		}(i)
	}
	<-started
	time.Sleep(80 * time.Millisecond) // 其余请求进入同 key 单飞等待
	close(release)
	wg.Wait()

	for i, rec := range recs {
		if rec.Code != http.StatusOK {
			t.Fatalf("第 %d 个请求失败: %d %s", i, rec.Code, rec.Body.String())
		}
	}
	if got := ol.listCount("/nd/my/alice/docs"); got != 1 {
		t.Fatalf("并发同 key 遍历次数 = %d want 1（单飞）", got)
	}
}

// TestDirSizeLeaderSurvivesRequestCancel leader 使用 WithoutCancel+35s，请求取消后仍完成并写缓存。
func TestDirSizeLeaderSurvivesRequestCancel(t *testing.T) {
	_, ol, r := newDirSizeModule(t)
	release := make(chan struct{})
	started := make(chan struct{}, 1)
	ol.listHook = func(ctx context.Context, abs string) error {
		if abs != "/nd/my/alice/docs" {
			return nil
		}
		select {
		case started <- struct{}{}:
		default:
		}
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		req := httptest.NewRequest(http.MethodGet, "/api/v1/netdisk/dirsize?space=my&path=/docs", nil).WithContext(ctx)
		r.ServeHTTP(httptest.NewRecorder(), req)
	}()
	<-started
	cancel()
	close(release)
	<-done

	// leader 已在后台完成并写缓存：下一跳不再遍历上游。
	getDirSize(t, r)
	if got := ol.listCount("/nd/my/alice/docs"); got != 1 {
		t.Fatalf("取消后 leader 未完成遍历: 次数 = %d want 1", got)
	}
	if got := ol.totalListCalls(); got != 2 {
		t.Fatalf("取消后缓存未生效: 总遍历 = %d want 2", got)
	}
}

// TestDirSizeInvalidatedByWrites 所有写操作成功后收口失效缓存。
func TestDirSizeInvalidatedByWrites(t *testing.T) {
	cases := []struct {
		name   string
		method string
		path   string
		body   string
	}{
		{"mkdir", http.MethodPost, "/api/v1/netdisk/mkdir", `{"space":"my","path":"/","name":"new"}`},
		{"rename", http.MethodPost, "/api/v1/netdisk/rename", `{"space":"my","path":"/docs/a.txt","name":"b.txt"}`},
		{"rename_batch", http.MethodPost, "/api/v1/netdisk/rename/batch", `{"space":"my","items":[{"path":"/docs/a.txt","name":"c.txt"}]}`},
		{"remove", http.MethodPost, "/api/v1/netdisk/remove", `{"space":"my","paths":["/docs/a.txt"]}`},
		{"move", http.MethodPost, "/api/v1/netdisk/move", `{"src_space":"my","dst_space":"my","src_dir":"/docs","dst_dir":"/other","names":["a.txt"]}`},
		{"copy", http.MethodPost, "/api/v1/netdisk/copy", `{"src_space":"my","dst_space":"my","src_dir":"/docs","dst_dir":"/other","names":["a.txt"]}`},
		{"decompress", http.MethodPost, "/api/v1/netdisk/archive/decompress", `{"space":"my","path":"/docs/a.zip","dst_dir":"/out"}`},
		{"save", http.MethodPost, "/api/v1/netdisk/save", `{"space":"my","dir":"docs","name":"x.txt","data":"aGVsbG8="}`},
		{"upload_init_empty", http.MethodPost, "/api/v1/netdisk/upload/init", `{"space":"my","path":"/docs","name":"e.txt","size":0}`},
		{"upload_complete", http.MethodPost, "/api/v1/netdisk/upload/complete", `{"upload_id":"abcd1234abcd1234"}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, ol, r := newDirSizeModule(t)
			getDirSize(t, r)
			if got := ol.listCount("/nd/my/alice/docs"); got != 1 {
				t.Fatalf("预热遍历次数 = %d want 1", got)
			}
			req := httptest.NewRequest(c.method, c.path, strings.NewReader(c.body))
			rec := httptest.NewRecorder()
			r.ServeHTTP(rec, req)
			if rec.Code != http.StatusOK {
				t.Fatalf("写操作失败: %d %s", rec.Code, rec.Body.String())
			}
			getDirSize(t, r)
			if got := ol.listCount("/nd/my/alice/docs"); got != 2 {
				t.Fatalf("写操作后缓存未失效: 遍历次数 = %d want 2", got)
			}
		})
	}
}

// TestDirSizeKeptOnWriteFailure 写操作失败不得误清缓存。
func TestDirSizeKeptOnWriteFailure(t *testing.T) {
	_, ol, r := newDirSizeModule(t)
	getDirSize(t, r)
	ol.failAPI["/api/fs/mkdir"] = &UpstreamError{Exposable: true, Status: 502, Code: codeUpstream, Message: "mkdir failed"}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/netdisk/mkdir", strings.NewReader(`{"space":"my","path":"/","name":"new"}`))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code == http.StatusOK {
		t.Fatalf("写操作本应失败: %s", rec.Body.String())
	}
	getDirSize(t, r)
	if got := ol.listCount("/nd/my/alice/docs"); got != 1 {
		t.Fatalf("失败写操作误清缓存: 遍历次数 = %d want 1", got)
	}
}

// TestDirSizeCacheTTLAndCap 缓存 5 分钟过期、条目上限 128。
func TestDirSizeCacheTTLAndCap(t *testing.T) {
	var c dirSizeCache
	base := time.Now()
	c.mu.Lock()
	c.storeLocked("/p", map[string]any{"size": int64(1)}, base)
	if _, ok := c.lookupLocked("/p", base.Add(dirSizeTTL-time.Minute)); !ok {
		c.mu.Unlock()
		t.Fatal("TTL 内应命中")
	}
	if _, ok := c.lookupLocked("/p", base.Add(dirSizeTTL)); ok {
		c.mu.Unlock()
		t.Fatal("超过 5 分钟不应命中")
	}
	for i := 0; i < dirSizeMaxKeys+10; i++ {
		c.storeLocked(fmt.Sprintf("/k%d", i), map[string]any{"size": int64(i)}, base)
	}
	if got := len(c.entries); got != dirSizeMaxKeys {
		c.mu.Unlock()
		t.Fatalf("缓存条目 = %d want %d", got, dirSizeMaxKeys)
	}
	c.mu.Unlock()

	c.invalidate()
	c.mu.Lock()
	if len(c.entries) != 0 || c.gen != 1 {
		c.mu.Unlock()
		t.Fatalf("失效后代数/条目错误: gen=%d entries=%d", c.gen, len(c.entries))
	}
	c.mu.Unlock()
}

// TestDirSizeWriteDuringFlightNotCached 遍历期间发生写操作：旧 leader 结果不得回填缓存。
func TestDirSizeWriteDuringFlightNotCached(t *testing.T) {
	_, ol, r := newDirSizeModule(t)
	release := make(chan struct{})
	started := make(chan struct{}, 1)
	ol.listHook = func(ctx context.Context, abs string) error {
		if abs != "/nd/my/alice/docs" {
			return nil
		}
		select {
		case started <- struct{}{}:
		default:
		}
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		r.ServeHTTP(httptest.NewRecorder(),
			httptest.NewRequest(http.MethodGet, "/api/v1/netdisk/dirsize?space=my&path=/docs", nil))
	}()
	<-started
	// 遍历进行中写入：清空缓存并推进代数。
	req := httptest.NewRequest(http.MethodPost, "/api/v1/netdisk/mkdir", strings.NewReader(`{"space":"my","path":"/","name":"new"}`))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("写操作失败: %d %s", rec.Code, rec.Body.String())
	}
	close(release)
	<-done

	getDirSize(t, r)
	if got := ol.listCount("/nd/my/alice/docs"); got != 2 {
		t.Fatalf("旧 leader 结果回填了缓存: 遍历次数 = %d want 2", got)
	}
}

// TestDirSizeInvalidateBypassesStaleFlight 写操作失效后，新请求不得 join 旧 flight：
// 旧 leader 在飞时新请求必须另起 flight 拿到写后结果，旧 leader 完成后不得误删新 flight。
func TestDirSizeInvalidateBypassesStaleFlight(t *testing.T) {
	const abs = "/nd/my/alice/docs"
	m, ol, _ := newDirSizeModule(t)
	releaseOld := make(chan struct{})
	releaseNew := make(chan struct{})
	oldStarted := make(chan struct{})
	newStarted := make(chan struct{})
	var calls atomic.Int32
	ol.listHook = func(ctx context.Context, path string) error {
		if path != abs {
			return nil
		}
		switch calls.Add(1) {
		case 1:
			close(oldStarted)
			select {
			case <-releaseOld:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		case 2:
			close(newStarted)
			select {
			case <-releaseNew:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		return nil
	}

	oldDone := make(chan struct{})
	go func() {
		defer close(oldDone)
		_, _ = m.dirSizeStat(context.Background(), ol, abs)
	}()
	select {
	case <-oldStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("旧 leader 未进入遍历")
	}

	// 遍历进行中写入：目录内容变化 + 缓存失效。
	ol.mu.Lock()
	ol.listData[abs] = []mapItemSrc{
		{Name: "a.txt", Size: 10, Modified: "2026-10-27 10:00:00"},
		{Name: "sub", IsDir: true, Modified: "2026-10-27 10:00:00"},
	}
	ol.mu.Unlock()
	m.dirSize.invalidate()

	type dirSizeResult struct {
		stat map[string]any
		err  error
	}
	newDone := make(chan dirSizeResult, 1)
	go func() {
		stat, err := m.dirSizeStat(context.Background(), ol, abs)
		newDone <- dirSizeResult{stat: stat, err: err}
	}()
	select {
	case <-newStarted:
	case <-time.After(time.Second):
		close(releaseOld)
		<-oldDone
		t.Fatal("失效后的新请求 join 了旧 flight（未另起遍历）")
	}

	// 旧 leader 先完成：只能清理自己的 flight，不得误删新 flight。
	close(releaseOld)
	<-oldDone
	m.dirSize.mu.Lock()
	inflight := m.dirSize.flights[abs]
	m.dirSize.mu.Unlock()
	if inflight == nil {
		close(releaseNew)
		t.Fatal("旧 leader 误删了新 flight")
	}

	close(releaseNew)
	var res dirSizeResult
	select {
	case res = <-newDone:
	case <-time.After(2 * time.Second):
		t.Fatal("新请求未完成")
	}
	if res.err != nil {
		t.Fatalf("新请求失败: %v", res.err)
	}
	if got, _ := res.stat["size"].(int64); got != 15 {
		t.Fatalf("新请求应拿到写后结果 size=15，实际 %v", res.stat["size"])
	}
	// 旧 leader 的写前结果不得覆盖缓存。
	stat, err := m.dirSizeStat(context.Background(), ol, abs)
	if err != nil {
		t.Fatalf("回读缓存失败: %v", err)
	}
	if got, _ := stat["size"].(int64); got != 15 {
		t.Fatalf("缓存被旧 leader 覆盖: size=%v want 15", stat["size"])
	}
}

// TestDirSizeLeaderPanicCleansFlight leader panic 后必须清理 flight 并释放等待者：
// 后续同目录请求可重新计算，等待者拿到中止错误而不是永久阻塞。
func TestDirSizeLeaderPanicCleansFlight(t *testing.T) {
	const abs = "/nd/my/alice/docs"
	m, ol, _ := newDirSizeModule(t)
	var panicOnce atomic.Bool
	panicOnce.Store(true)
	release := make(chan struct{})
	entered := make(chan struct{}, 1)
	ol.listHook = func(_ context.Context, path string) error {
		if path != abs || !panicOnce.Load() {
			return nil
		}
		select {
		case entered <- struct{}{}:
		default:
		}
		<-release
		panic("dirsize boom")
	}

	leaderDone := make(chan any, 1)
	go func() {
		defer func() { leaderDone <- recover() }()
		_, _ = m.dirSizeStat(context.Background(), ol, abs)
	}()
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("leader 未进入遍历")
	}

	// 等待者加入同一 flight，blocked 到 leader 完成。
	waiterCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	type dirSizeResult struct {
		stat map[string]any
		err  error
	}
	waiterDone := make(chan dirSizeResult, 1)
	go func() {
		stat, err := m.dirSizeStat(waiterCtx, ol, abs)
		waiterDone <- dirSizeResult{stat: stat, err: err}
	}()
	time.Sleep(30 * time.Millisecond) // 等待者进入单飞等待
	close(release)

	select {
	case p := <-leaderDone:
		if p == nil {
			t.Fatal("leader 应 panic")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("leader 未按预期 panic")
	}
	select {
	case res := <-waiterDone:
		if res.stat != nil {
			t.Fatalf("等待者不应拿到统计: %v", res.stat)
		}
		if res.err == nil {
			t.Fatal("等待者应收到中止错误")
		}
	case <-time.After(time.Second):
		t.Fatal("等待者未被释放（panic 后 flight 未关闭）")
	}

	m.dirSize.mu.Lock()
	_, inflight := m.dirSize.flights[abs]
	m.dirSize.mu.Unlock()
	if inflight {
		t.Fatal("panic 后 flight 未删除")
	}

	// panic 结束后同 key 可重新计算。
	panicOnce.Store(false)
	stat, err := m.dirSizeStat(context.Background(), ol, abs)
	if err != nil || stat == nil {
		t.Fatalf("panic 后重算失败: stat=%v err=%v", stat, err)
	}
	if got := ol.listCount(abs); got != 2 {
		t.Fatalf("panic 后未重新遍历: 次数 = %d want 2", got)
	}
}
