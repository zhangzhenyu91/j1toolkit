package netdisk

// Task 6 资源边界回归：Office 回源 512MiB 预算、缩略图像素/下载/超时/可取消信号量、
// 空 COS 前缀/Base64 保存上限、ZIP 深度与条目上限、OpenList 登录冷却不缓存 ctx 取消、
// dirsize 全局并发与用户频率预算。全部使用桩 OpenList/httptest，不访问真实服务。

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"image"
	"image/color"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"shade/internal/auth"
)

// task6MetaOL 可编程 OpenList：fs/get 返回固定元信息，DownloadStream 返回固定内容并计次。
type task6MetaOL struct {
	mu         sync.Mutex
	size       int64
	isDir      bool
	body       string
	downloads  int
	listItems  map[string][]mapItemSrc
	listBlock  chan struct{}
	listEnter  chan struct{}
	inFlight   int32
	maxFlight  int32
	listCalled int32
}

func newTask6MetaOL() *task6MetaOL {
	return &task6MetaOL{listItems: map[string][]mapItemSrc{}}
}

func (s *task6MetaOL) EnsureConfigured() error { return nil }

func (s *task6MetaOL) Call(ctx context.Context, _ string, apiPath string, opts CallOpts) (json.RawMessage, error) {
	switch apiPath {
	case "/api/fs/get":
		return json.Marshal(map[string]any{
			"is_dir": s.isDir, "size": s.size, "modified": "2026-10-31 10:00:00",
		})
	case "/api/fs/list":
		if s.listEnter != nil {
			n := atomic.AddInt32(&s.inFlight, 1)
			for {
				old := atomic.LoadInt32(&s.maxFlight)
				if n <= old || atomic.CompareAndSwapInt32(&s.maxFlight, old, n) {
					break
				}
			}
			select {
			case s.listEnter <- struct{}{}:
			default:
			}
			if s.listBlock != nil {
				select {
				case <-s.listBlock:
				case <-ctx.Done():
					atomic.AddInt32(&s.inFlight, -1)
					return nil, ctx.Err()
				}
			}
			atomic.AddInt32(&s.inFlight, -1)
		}
		atomic.AddInt32(&s.listCalled, 1)
		return json.Marshal(map[string]any{"content": s.listItems[stubBodyPath(opts.Body)]})
	}
	return json.RawMessage("null"), nil
}

func (s *task6MetaOL) DownloadStream(context.Context, string) (io.ReadCloser, http.Header, error) {
	s.mu.Lock()
	s.downloads++
	body := s.body
	s.mu.Unlock()
	return io.NopCloser(strings.NewReader(body)), http.Header{}, nil
}

func (s *task6MetaOL) EnsureDir(context.Context, string) error { return nil }
func (s *task6MetaOL) PutEmpty(context.Context, string) error  { return nil }
func (s *task6MetaOL) FsPut(context.Context, string, io.Reader, int64) error {
	return nil
}
func (s *task6MetaOL) ArchiveDownloadStream(context.Context, string, string, string) (io.ReadCloser, http.Header, error) {
	return nil, nil, errors.New("stub: 不支持包内下载")
}

// TestBridgeReadFileSizeBudget Office 回源：元数据超 512MiB+1 直接拒绝且不下载；
// 流式超限用 limit+1 检出且不返回截断内容；恰好等于上限可通过。
func TestBridgeReadFileSizeBudget(t *testing.T) {
	m := New(nil)
	m.Base = BaseDirs{PersonalRoot: "/nd/my", PublicRoot: "/nd/pub"}
	ctx := context.Background()

	// 元数据优先拒绝：512MiB+1 不发起下载。
	ol := newTask6MetaOL()
	ol.size = 512<<20 + 1
	ol.body = "x"
	m.OL = ol
	if _, _, err := m.ReadFileBytes(ctx, "my", "alice", 0, "/big.docx"); !errors.Is(err, ErrFileTooLarge) {
		t.Fatalf("512MiB+1 应显式报错，实际 %v", err)
	}
	if ol.downloads != 0 {
		t.Fatalf("元数据已超限时不应下载，实际下载 %d 次", ol.downloads)
	}

	// 恰好 512MiB 不误拒（边界包含）。
	ol = newTask6MetaOL()
	ol.size = 512 << 20
	m.OL = ol
	if _, _, err := m.ReadFileBytes(ctx, "my", "alice", 0, "/edge.docx"); err != nil {
		t.Fatalf("恰好 512MiB 不应报超限: %v", err)
	}

	// 流式超限：元数据缺失/偏小时仍按 limit+1 检出，绝不能把截断内容当完整文件。
	ol = newTask6MetaOL()
	ol.size = 0
	ol.body = strings.Repeat("a", 33)
	m.OL = ol
	m.FileMaxBytes = 32
	data, _, err := m.ReadFileBytes(ctx, "my", "alice", 0, "/liar.docx")
	if !errors.Is(err, ErrFileTooLarge) {
		t.Fatalf("流超限应显式报错，实际 err=%v data=%d 字节", err, len(data))
	}
	if data != nil {
		t.Fatalf("超限不得返回截断内容，实际 %d 字节", len(data))
	}

	// 小于上限正常返回。
	ol = newTask6MetaOL()
	ol.size = 5
	ol.body = "hello"
	m.OL = ol
	data, _, err = m.ReadFileBytes(ctx, "my", "alice", 0, "/ok.docx")
	if err != nil || string(data) != "hello" {
		t.Fatalf("小文件应正常回源: %q %v", data, err)
	}
}

// task6PNG 构造 w*h PNG。
func task6PNG(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.NRGBA{R: uint8(x), G: uint8(y), B: 32, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("png 编码: %v", err)
	}
	return buf.Bytes()
}

// TestThumbPixelBudget DecodeConfig 像素上限：超像素拒绝，边界内正常。
func TestThumbPixelBudget(t *testing.T) {
	th := NewWebpThumbnailer()
	th.MaxPixels = 16
	if _, err := th.MakeThumb(task6PNG(t, 8, 8)); !errors.Is(err, ErrThumbTooLarge) {
		t.Fatalf("64 像素应超限拒绝，实际 %v", err)
	}
	if _, err := th.MakeThumb(task6PNG(t, 4, 4)); err != nil {
		t.Fatalf("16 像素应允许: %v", err)
	}
}

// countingThumb 统计缩略图生成调用（验证超限时不会进入解码）。
type countingThumb struct{ calls int32 }

func (c *countingThumb) MakeThumb(src []byte) ([]byte, error) {
	atomic.AddInt32(&c.calls, 1)
	return []byte("webp"), nil
}

// TestThumbDownloadByteBudget 元数据偏小时实际下载仍受字节预算约束（不把截断当图片）。
func TestThumbDownloadByteBudget(t *testing.T) {
	ol := newTask6MetaOL()
	ol.size = 1
	ol.body = strings.Repeat("z", (1<<20)+1)
	m := New(nil)
	m.OL = ol
	m.Base = BaseDirs{PersonalRoot: "/nd/my", PublicRoot: "/nd/pub"}
	m.ThumbMaxMB = 1
	thumb := &countingThumb{}
	m.Thumb = thumb
	r := newTestRouter(t, m)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/netdisk/thumb?space=my&path=/pics/a.png", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("超下载预算应 413，实际 %d %s", rec.Code, rec.Body.String())
	}
	if atomic.LoadInt32(&thumb.calls) != 0 {
		t.Fatal("超限时不应进入解码生成")
	}
}

// TestThumbSemaphoreWaitCancellable 缩略图信号量等待必须可被请求取消打断。
func TestThumbSemaphoreWaitCancellable(t *testing.T) {
	ol := newTask6MetaOL()
	ol.size = 1
	ol.body = "x"
	m := New(nil)
	m.OL = ol
	m.Base = BaseDirs{PersonalRoot: "/nd/my", PublicRoot: "/nd/pub"}
	m.Thumb = &countingThumb{}
	m.thumbSem <- struct{}{}
	m.thumbSem <- struct{}{} // 占满并发闸 2
	r := newTestRouter(t, m)

	ctx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequest(http.MethodGet, "/api/v1/netdisk/thumb?space=my&path=/pics/a.png", nil).WithContext(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		r.ServeHTTP(httptest.NewRecorder(), req)
	}()
	time.Sleep(30 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("信号量等待未响应请求取消")
	}
}

// TestSaveBase64PreLimit /save base64 在解码前限总量。
func TestSaveBase64PreLimit(t *testing.T) {
	m := New(nil)
	m.OL = newTask6MetaOL()
	m.Base = BaseDirs{PersonalRoot: "/nd/my", PublicRoot: "/nd/pub"}
	m.MaxUploadMB = 1
	r := newTestRouter(t, m)

	raw := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte("a"), (1<<20)+1))
	body := `{"space":"my","dir":"docs","name":"x.bin","data":"` + raw + `"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/netdisk/save", strings.NewReader(body))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("超上传上限 base64 应 413，实际 %d %s", rec.Code, rec.Body.String())
	}
}

// TestDirZipDepthAndEntryLimits ZIP 打包必须有深度与条目上限。
func TestDirZipDepthAndEntryLimits(t *testing.T) {
	m := New(nil)
	ol := newTask6MetaOL()
	// 构造深度超过上限的链式目录：/root → /root/d → ... → 第 zipMaxDepth 层仍要递归。
	path := "/root"
	for i := 0; i < zipMaxDepth; i++ {
		ol.listItems[path] = []mapItemSrc{{Name: "d", IsDir: true}}
		path += "/d"
	}
	ol.listItems[path] = []mapItemSrc{{Name: "leaf.txt"}}
	m.OL = ol
	r := httptest.NewRequest(http.MethodGet, "/api/v1/netdisk/download/dir?space=my&path=/x", nil)
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	if err := m.zipWalk(r, ol, zw, "/root", "root"); err == nil {
		t.Fatal("超过深度上限应中止打包")
	}
	_ = zw.Close()

	// 条目上限：超过 zipMaxEntries 个文件即中止。
	m2 := New(nil)
	ol2 := newTask6MetaOL()
	items := make([]mapItemSrc, 0, zipMaxEntries+1)
	for i := 0; i <= zipMaxEntries; i++ {
		items = append(items, mapItemSrc{Name: "f" + strconv.Itoa(i) + ".txt"})
	}
	ol2.listItems["/flat"] = items
	ol2.body = ""
	m2.OL = ol2
	var buf2 bytes.Buffer
	zw2 := zip.NewWriter(&buf2)
	if err := m2.zipWalk(r, ol2, zw2, "/flat", "flat"); err == nil {
		t.Fatal("超过条目上限应中止打包")
	}
}

// TestOpenListLoginCancelNotCached 登录因 ctx 取消失败不得写入全局冷却。
func TestOpenListLoginCancelNotCached(t *testing.T) {
	var logins int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/auth/login":
			n := atomic.AddInt32(&logins, 1)
			if n == 1 {
				time.Sleep(150 * time.Millisecond) // 首次被调用方取消
			}
			_, _ = w.Write([]byte(`{"code":200,"data":{"token":"tk"}}`))
		default:
			_, _ = w.Write([]byte(`{"code":200,"data":{}}`))
		}
	}))
	defer srv.Close()
	c := &OpenListHTTP{BaseURL: srv.URL, Username: "u", Password: "p", HTTP: &http.Client{}}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	_, _ = c.Call(ctx, http.MethodPost, "/api/fs/list", CallOpts{Body: map[string]any{}})
	cancel()

	// 第二次（正常 ctx）必须重新登录，而不是命中「取消」留下的全局冷却。
	if _, err := c.Call(context.Background(), http.MethodPost, "/api/fs/list", CallOpts{Body: map[string]any{}}); err != nil {
		t.Fatalf("取消后的下一次调用应成功，实际 %v（登录 %d 次）", err, atomic.LoadInt32(&logins))
	}
	if got := atomic.LoadInt32(&logins); got != 2 {
		t.Fatalf("登录次数 = %d，期望 2（取消不得进冷却）", got)
	}
}

// TestDirSizeGlobalConcurrency dirsize leader 计算受全局并发预算约束。
func TestDirSizeGlobalConcurrency(t *testing.T) {
	ol := newTask6MetaOL()
	ol.listBlock = make(chan struct{})
	ol.listEnter = make(chan struct{}, 16)
	m := New(nil)
	m.OL = ol
	paths := []string{"/d1", "/d2", "/d3", "/d4"}
	for _, p := range paths {
		ol.listItems[p] = nil
	}
	var wg sync.WaitGroup
	for _, p := range paths {
		wg.Add(1)
		go func(p string) {
			defer wg.Done()
			_, _ = m.dirSizeStat(context.Background(), ol, p)
		}(p)
	}
	// 等前两个 leader 进入，确认第三个被预算挡住。
	deadline := time.After(2 * time.Second)
	for i := 0; i < 2; i++ {
		select {
		case <-ol.listEnter:
		case <-deadline:
			t.Fatal("前两个 leader 未启动")
		}
	}
	select {
	case <-ol.listEnter:
		t.Fatal("第 3 个 leader 不应并发进入（全局并发预算未生效）")
	case <-time.After(80 * time.Millisecond):
	}
	close(ol.listBlock)
	wg.Wait()
	if got := atomic.LoadInt32(&ol.maxFlight); got > dirSizeGlobalConcurrency {
		t.Fatalf("并发峰值 = %d > %d", got, dirSizeGlobalConcurrency)
	}
}

// TestDirSizeUserRateLimit 同一用户高频 leader 计算受限。
func TestDirSizeUserRateLimit(t *testing.T) {
	ol := newTask6MetaOL()
	ol.listItems["/r"] = nil
	m := New(nil)
	m.OL = ol
	ctx := context.WithValue(context.Background(), identityContextKey, auth.Identity{ID: 42, Username: "alice"})
	var last error
	for i := 0; i <= dirSizeUserLimit; i++ {
		abs := "/r" + strconv.Itoa(i)
		ol.listItems[abs] = nil
		_, last = m.dirSizeStat(ctx, ol, abs)
	}
	if !errors.Is(last, errDirSizeRateLimited) {
		t.Fatalf("第 %d 次 leader 应被频率预算拒绝，实际 %v", dirSizeUserLimit+1, last)
	}
}
