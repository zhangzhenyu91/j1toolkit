package worklog

// Task 7 热路径测试：列表/附件/PDF 每请求只解析一次 COS 访问前缀（空前缀回退库内历史地址）、
// loadEntries 不再 UPDATE pending、验证并发峰值 ≤4、同一照片在飞去重；
// fake DB + 内存对象存储桩，不访问外部服务。

import (
	"context"
	"database/sql/driver"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

// countingObjects 统计 PublicURL 调用的对象存储桩；base 为空模拟运行时未配置（PublicURL 返回空串）。
type countingObjects struct {
	*memObjects
	mu    sync.Mutex
	calls int
	base  string
}

func (c *countingObjects) PublicURL(key string) string {
	c.mu.Lock()
	c.calls++
	c.mu.Unlock()
	if c.base == "" {
		return ""
	}
	return c.base + key
}

func (c *countingObjects) urlCalls() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls
}

// useCountingObjects 把测试环境对象存储替换为调用计数桩。
func (e *testEnv) useCountingObjects(base string) *countingObjects {
	co := &countingObjects{memObjects: e.objects, base: base}
	e.module.Store = co
	return co
}

// photoRow loadEntries 照片卫星行。
func photoRow(id, entryID int64, cosKey, url string) []driver.Value {
	return []driver.Value{id, entryID, cosKey, url, `["赵登"]`, "passed", "绝缘子清扫", "2026.08.18 09:30",
		"多云", "孝义市·现场", "111.7", "37.1", int64(1), int64(1), int64(1), "app", int64(0)}
}

// entryRowWithRemark 卡片行（remark_files 带一条 COS 附件）。
func entryRowWithRemark(id int64, logDate, filesJSON string) []driver.Value {
	row := entryLoadRow(id, logDate, "")
	row[5] = filesJSON
	return row
}

// TestLoadEntriesURLBaseResolvedOnce 列表接口（卡片 + 照片 + 备注附件）每请求只解析一次 PublicURL。
func TestLoadEntriesURLBaseResolvedOnce(t *testing.T) {
	env := newTestServer(t, identUser).withTeam()
	co := env.useCountingObjects("https://cos.test/")
	files := `[{"name":"通报.pdf","url":"https://legacy.example/r.pdf","cos_key":"worklog/remark/r.pdf","type":"doc","size":3}]`
	env.fake.on("FROM worklog_entry e", entryLoadCols(), [][]driver.Value{entryRowWithRemark(1, "2026-08-18", files)})
	env.fake.on("FROM worklog_entry_member", []string{"id", "entry_id", "member_id", "name", "checked", "sort"}, nil)
	env.fake.on("FROM worklog_photo WHERE entry_id IN", []string{
		"id", "entry_id", "cos_key", "url", "members", "verify_status", "work_content", "shot_time",
		"weather", "location", "lng", "lat", "date_ok", "dest_ok", "is_watermark", "source", "sgcc_synced",
	}, [][]driver.Value{photoRow(9, 1, "worklog/2026/p.jpg", "https://legacy.example/p.jpg")})

	rec := env.do(t, "GET", "/api/v1/worklog/logs?date=2026-08-18", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("列表应 200，实际 %d body=%s", rec.Code, rec.Body.String())
	}
	if got := co.urlCalls(); got != 1 {
		t.Fatalf("列表每请求应只解析一次 PublicURL，实际 %d 次", got)
	}
	envr := decodeEnv(t, rec)
	data, _ := envr.Data.(map[string]any)
	list, _ := data["list"].([]any)
	if len(list) != 1 {
		t.Fatalf("应 1 条卡片，实际 %#v", list)
	}
	entry := list[0].(map[string]any)
	photos, _ := entry["photos"].([]any)
	if len(photos) != 1 || photos[0].(map[string]any)["url"] != "https://cos.test/worklog/2026/p.jpg" {
		t.Fatalf("照片地址应按复用前缀拼接，实际 %#v", photos)
	}
	remarks, _ := entry["remark_files"].([]any)
	if len(remarks) != 1 || remarks[0].(map[string]any)["url"] != "https://cos.test/worklog/remark/r.pdf" {
		t.Fatalf("附件地址应按复用前缀拼接，实际 %#v", remarks)
	}
}

// TestLoadEntriesURLBaseEmptyFallback 前缀未配置（空 base）时回退库内历史地址，且仍只解析一次。
func TestLoadEntriesURLBaseEmptyFallback(t *testing.T) {
	env := newTestServer(t, identUser).withTeam()
	co := env.useCountingObjects("")
	files := `[{"name":"通报.pdf","url":"https://legacy.example/r.pdf","cos_key":"worklog/remark/r.pdf","type":"doc","size":3}]`
	env.fake.on("FROM worklog_entry e", entryLoadCols(), [][]driver.Value{entryRowWithRemark(1, "2026-08-18", files)})
	env.fake.on("FROM worklog_entry_member", []string{"id", "entry_id", "member_id", "name", "checked", "sort"}, nil)
	env.fake.on("FROM worklog_photo WHERE entry_id IN", []string{
		"id", "entry_id", "cos_key", "url", "members", "verify_status", "work_content", "shot_time",
		"weather", "location", "lng", "lat", "date_ok", "dest_ok", "is_watermark", "source", "sgcc_synced",
	}, [][]driver.Value{photoRow(9, 1, "worklog/2026/p.jpg", "https://legacy.example/p.jpg")})

	rec := env.do(t, "GET", "/api/v1/worklog/logs?date=2026-08-18", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("列表应 200，实际 %d body=%s", rec.Code, rec.Body.String())
	}
	if got := co.urlCalls(); got != 1 {
		t.Fatalf("空 base 每请求也应只解析一次 PublicURL，实际 %d 次", got)
	}
	envr := decodeEnv(t, rec)
	data, _ := envr.Data.(map[string]any)
	list, _ := data["list"].([]any)
	entry := list[0].(map[string]any)
	photos, _ := entry["photos"].([]any)
	if len(photos) != 1 || photos[0].(map[string]any)["url"] != "https://legacy.example/p.jpg" {
		t.Fatalf("空 base 应回退库内历史照片地址，实际 %#v", photos)
	}
	remarks, _ := entry["remark_files"].([]any)
	if len(remarks) != 1 || remarks[0].(map[string]any)["url"] != "https://legacy.example/r.pdf" {
		t.Fatalf("空 base 应回退库内历史附件地址，实际 %#v", remarks)
	}
}

// TestPhotoListURLBaseResolvedOnce 照片清单每请求只解析一次 PublicURL。
func TestPhotoListURLBaseResolvedOnce(t *testing.T) {
	env := newTestServer(t, identUser).withTeam()
	co := env.useCountingObjects("https://cos.test/")
	env.fake.on("FROM worklog_photo p JOIN worklog_entry e",
		[]string{"id", "cos_key", "url", "log_date", "day", "members"},
		[][]driver.Value{
			{int64(3), "worklog/a/3-1.jpg", "https://legacy.example/3-1.jpg", "2026-08-18", int64(18), `["赵登"]`},
			{int64(4), "worklog/a/4-1.jpg", "https://legacy.example/4-1.jpg", "2026-08-18", int64(18), `["赵登"]`},
		})
	rec := env.do(t, "GET", "/api/v1/worklog/photos?from=2026-08-18&to=2026-08-18", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("照片清单应 200，实际 %d body=%s", rec.Code, rec.Body.String())
	}
	if got := co.urlCalls(); got != 1 {
		t.Fatalf("照片清单每请求应只解析一次 PublicURL，实际 %d 次", got)
	}
	envr := decodeEnv(t, rec)
	data, _ := envr.Data.(map[string]any)
	list, _ := data["list"].([]any)
	if len(list) != 2 || list[0].(map[string]any)["url"] != "https://cos.test/worklog/a/3-1.jpg" {
		t.Fatalf("照片地址应按复用前缀拼接，实际 %#v", list)
	}
}

// TestPhotosPDFURLBaseResolvedOnce 组合 PDF 每请求只解析一次 PublicURL（多张照片也不逐张解析）。
func TestPhotosPDFURLBaseResolvedOnce(t *testing.T) {
	env := newTestServer(t, identUser).withTeam()
	co := env.useCountingObjects("https://cos.test/")
	png := tinyPNG(t)
	env.objects.items["worklog/a/1.jpg"] = png
	env.objects.items["worklog/a/2.jpg"] = png
	env.fake.on("FROM worklog_photo p JOIN worklog_entry e", []string{"entry_id", "cos_key", "url"},
		[][]driver.Value{
			{int64(1), "worklog/a/1.jpg", "https://legacy.example/1.jpg"},
			{int64(1), "worklog/a/2.jpg", "https://legacy.example/2.jpg"},
		})
	rec := env.do(t, "GET", "/api/v1/worklog/photos.pdf?from=2026-08-18&to=2026-08-18", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("组合 PDF 应 200，实际 %d body=%s", rec.Code, rec.Body.String())
	}
	if got := co.urlCalls(); got != 1 {
		t.Fatalf("组合 PDF 每请求应只解析一次 PublicURL，实际 %d 次", got)
	}
	if !strings.HasPrefix(rec.Header().Get("Content-Type"), "application/pdf") {
		t.Fatalf("应返回 PDF，实际 %q", rec.Header().Get("Content-Type"))
	}
}

// TestLoadEntriesNoPendingUpdate 列表装配不再顺手 UPDATE pending（清扫归 sweeper）。
func TestLoadEntriesNoPendingUpdate(t *testing.T) {
	env := newTestServer(t, identUser).withTeam()
	env.fake.on("FROM worklog_entry e", entryLoadCols(), [][]driver.Value{entryLoadRow(1, "2026-08-18", "")})
	env.withEmptySatellites()
	rec := env.do(t, "GET", "/api/v1/worklog/logs?date=2026-08-18", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("列表应 200，实际 %d body=%s", rec.Code, rec.Body.String())
	}
	if got := env.fake.execCount("verify_status = 'pending' AND created_at"); got != 0 {
		t.Fatalf("loadEntries 不应再 UPDATE pending，实际执行 %d 次", got)
	}
}

// blockingDify 可阻塞的 Dify 桩：记录峰值并发与调用次数，收到 release 后放行。
type blockingDify struct {
	mu      sync.Mutex
	active  int
	peak    int
	calls   int
	release chan struct{}
}

func (d *blockingDify) VerifyPhoto(context.Context, string, string, string, string) VerifyResult {
	d.mu.Lock()
	d.active++
	d.calls++
	if d.active > d.peak {
		d.peak = d.active
	}
	d.mu.Unlock()
	<-d.release
	d.mu.Lock()
	d.active--
	d.mu.Unlock()
	return VerifyResult{OK: true, WorkContent: "绝缘子清扫", Time: "2026.08.18 09:30",
		Weather: "多云", Location: "孝义市·现场"}
}

func (d *blockingDify) snapshot() (peak, calls int) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.peak, d.calls
}

// waitUntil 轮询等待条件成立（fake 环境下毫秒级）。
func waitUntil(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("等待%s超时", what)
}

// TestVerifyConcurrencyCap 验证流水线全局限 4 路：10 张照片并发验证，Dify 峰值恰为 4。
func TestVerifyConcurrencyCap(t *testing.T) {
	env := newTestServer(t, identUser).withTeam()
	dify := &blockingDify{release: make(chan struct{})}
	env.module.Dify = dify
	env.fake.on("FROM worklog_photo p JOIN worklog_entry e",
		[]string{"log_date", "destination_name"}, [][]driver.Value{{"2026-08-18", "孝义市"}})

	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			env.module.VerifyAndWriteBack(context.Background(), int64(100+i), "u", "2026-08-18", "孝义市", "https://cos.test/x.jpg")
		}(i)
	}
	waitUntil(t, "验证并发填满 4 路", func() bool {
		_, calls := dify.snapshot()
		return calls >= 4
	})
	// 再等一拍，确认第 5 路没有越过信号量
	time.Sleep(20 * time.Millisecond)
	peak, _ := dify.snapshot()
	if peak != 4 {
		t.Fatalf("验证并发峰值应为 4，实际 %d", peak)
	}
	close(dify.release)
	wg.Wait()
	if peak, _ := dify.snapshot(); peak != 4 {
		t.Fatalf("验证全程峰值应为 4，实际 %d", peak)
	}
}

// TestVerifyInFlightDedup 同一照片在飞时不重复识别（在飞集合去重）。
func TestVerifyInFlightDedup(t *testing.T) {
	env := newTestServer(t, identUser).withTeam()
	dify := &blockingDify{release: make(chan struct{})}
	env.module.Dify = dify
	env.fake.on("FROM worklog_photo p JOIN worklog_entry e",
		[]string{"log_date", "destination_name"}, [][]driver.Value{{"2026-08-18", "孝义市"}})

	done := make(chan struct{})
	go func() {
		env.module.VerifyAndWriteBack(context.Background(), 321, "u", "2026-08-18", "孝义市", "https://cos.test/x.jpg")
		close(done)
	}()
	waitUntil(t, "首路验证进入识别", func() bool {
		_, calls := dify.snapshot()
		return calls >= 1
	})
	second := make(chan struct{})
	go func() {
		env.module.VerifyAndWriteBack(context.Background(), 321, "u", "2026-08-18", "孝义市", "https://cos.test/x.jpg")
		close(second)
	}()
	select {
	case <-second:
	case <-time.After(time.Second):
		t.Fatalf("同一照片在飞时应直接跳过，不应排队等待")
	}
	if _, calls := dify.snapshot(); calls != 1 {
		t.Fatalf("同一照片在飞时不应重复识别，实际调用 %d 次", calls)
	}
	close(dify.release)
	<-done
}
