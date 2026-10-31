package netdisk

// ZIP 目录打包性能回归：深度 1 预取下一文件、在飞流峰值 ≤2、条目顺序与 zip.Store 不变、
// 所有错误路径关闭在飞流并保持 panic(http.ErrAbortHandler)。

import (
	"archive/zip"
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// streamGauge 统计同时在飞下载流数量与峰值。
type streamGauge struct {
	mu      sync.Mutex
	open    int
	max     int
	created int
}

func (g *streamGauge) enter() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.open++
	g.created++
	if g.open > g.max {
		g.max = g.open
	}
}

func (g *streamGauge) exit() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.open--
}

func (g *streamGauge) snapshot() (open, max, created int) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.open, g.max, g.created
}

// gaugeStream 关闭时归还并发额度（Close 幂等）。
type gaugeStream struct {
	r    io.Reader
	g    *streamGauge
	once sync.Once
}

func (s *gaugeStream) Read(p []byte) (int, error) { return s.r.Read(p) }
func (s *gaugeStream) Close() error {
	s.once.Do(s.g.exit)
	return nil
}

// zipTestModule 构造 zip 测试模块与 gauge 下载器。
func zipTestModule(files map[string]string) (*Module, *stubOL, *streamGauge) {
	ol := newStubOL()
	g := &streamGauge{}
	for abs, content := range files {
		ol.files[abs] = content
	}
	ol.download = func(abs string) (io.ReadCloser, http.Header, error) {
		content, ok := files[abs]
		if !ok {
			return nil, nil, fmt.Errorf("stub: 文件不存在 %s", abs)
		}
		g.enter()
		return &gaugeStream{r: strings.NewReader(content), g: g}, http.Header{}, nil
	}
	return New(nil), ol, g
}

// TestDirZipPrefetchOrderPeakAndStore 顺序/压缩方式不变，深度 1 预取峰值恰为 2，结束后流全关。
func TestDirZipPrefetchOrderPeakAndStore(t *testing.T) {
	m, ol, g := zipTestModule(map[string]string{
		"/root/a.txt":     "AAA",
		"/root/sub/x.txt": "X",
		"/root/b.txt":     "BBB",
		"/root/c.txt":     "CCC",
	})
	ol.listData["/root"] = []mapItemSrc{
		{Name: "a.txt", Size: 3},
		{Name: "sub", IsDir: true},
		{Name: "b.txt", Size: 3},
		{Name: "c.txt", Size: 3},
	}
	ol.listData["/root/sub"] = []mapItemSrc{{Name: "x.txt", Size: 1}}

	rec := httptest.NewRecorder()
	m.pipeDirZip(rec, httptest.NewRequest(http.MethodGet, "/", nil), ol, "/root", "root")
	if rec.Code != http.StatusOK {
		t.Fatalf("打包失败: %d %s", rec.Code, rec.Body.String())
	}
	zr, err := zip.NewReader(bytes.NewReader(rec.Body.Bytes()), int64(rec.Body.Len()))
	if err != nil {
		t.Fatalf("ZIP 解析失败: %v", err)
	}
	want := []string{"root/", "root/a.txt", "root/sub/", "root/sub/x.txt", "root/b.txt", "root/c.txt"}
	if len(zr.File) != len(want) {
		t.Fatalf("条目数 = %d want %d", len(zr.File), len(want))
	}
	for i, name := range want {
		if zr.File[i].Name != name {
			t.Fatalf("条目顺序错误 [%d] = %q want %q", i, zr.File[i].Name, name)
		}
	}
	for _, f := range zr.File {
		if strings.HasSuffix(f.Name, "/") {
			continue
		}
		if f.Method != zip.Store {
			t.Fatalf("文件 %s 压缩方式 = %d want zip.Store", f.Name, f.Method)
		}
	}
	open, max, created := g.snapshot()
	if open != 0 {
		t.Fatalf("打包结束后仍有 %d 个在飞流未关闭", open)
	}
	if max > 2 {
		t.Fatalf("在飞下载流峰值 = %d want ≤2", max)
	}
	if max != 2 {
		t.Fatalf("深度 1 预取未生效：峰值 = %d want 2", max)
	}
	if created != 4 {
		t.Fatalf("打开下载流次数 = %d want 4", created)
	}
}

// TestDirZipErrorClosesInflight 错误路径关闭在飞流并 abort 连接。
func TestDirZipErrorClosesInflight(t *testing.T) {
	t.Run("读取中断", func(t *testing.T) {
		m, ol, g := zipTestModule(map[string]string{"/root/a.txt": "AAA", "/root/c.txt": "CCC"})
		// b.txt 下载流读时出错（已预取 c.txt，两流都必须在错误路径关闭）。
		ol.download = func(abs string) (io.ReadCloser, http.Header, error) {
			switch abs {
			case "/root/a.txt":
				g.enter()
				return &gaugeStream{r: strings.NewReader("AAA"), g: g}, http.Header{}, nil
			case "/root/b.txt":
				g.enter()
				return &gaugeStream{r: errReader{}, g: g}, http.Header{}, nil
			case "/root/c.txt":
				g.enter()
				return &gaugeStream{r: strings.NewReader("CCC"), g: g}, http.Header{}, nil
			}
			return nil, nil, fmt.Errorf("stub: 文件不存在 %s", abs)
		}
		ol.listData["/root"] = []mapItemSrc{{Name: "a.txt"}, {Name: "b.txt"}, {Name: "c.txt"}}
		assertZipAbort(t, m, ol)
		if open, max, created := g.snapshot(); open != 0 || max > 2 || created != 3 {
			t.Fatalf("错误路径流状态 open=%d max=%d created=%d want 0/≤2/3", open, max, created)
		}
	})

	t.Run("预取下载失败", func(t *testing.T) {
		m, ol, g := zipTestModule(map[string]string{"/root/a.txt": "AAA"})
		ol.download = func(abs string) (io.ReadCloser, http.Header, error) {
			if abs == "/root/a.txt" {
				g.enter()
				return &gaugeStream{r: strings.NewReader("AAA"), g: g}, http.Header{}, nil
			}
			return nil, nil, fmt.Errorf("stub: 预取失败 %s", abs)
		}
		ol.listData["/root"] = []mapItemSrc{{Name: "a.txt"}, {Name: "b.txt"}}
		assertZipAbort(t, m, ol)
		if open, _, _ := g.snapshot(); open != 0 {
			t.Fatalf("预取失败后在飞流未关闭: %d", open)
		}
	})

	t.Run("非法名称", func(t *testing.T) {
		m, ol, g := zipTestModule(map[string]string{"/root/a.txt": "AAA"})
		ol.listData["/root"] = []mapItemSrc{{Name: "a.txt"}, {Name: "bad/name"}}
		assertZipAbort(t, m, ol)
		if open, _, created := g.snapshot(); open != 0 || created != 1 {
			t.Fatalf("非法名称路径应只打开 a.txt 并关闭: open=%d created=%d", open, created)
		}
	})
}

// errReader 读取即失败。
type errReader struct{}

func (errReader) Read([]byte) (int, error) { return 0, fmt.Errorf("stub: 读取中断") }

// assertZipAbort 断言 pipeDirZip 以上游错误 abort（panic http.ErrAbortHandler）。
func assertZipAbort(t *testing.T, m *Module, ol *stubOL) {
	t.Helper()
	defer func() {
		rec := recover()
		if rec != http.ErrAbortHandler {
			t.Fatalf("错误路径应 panic(http.ErrAbortHandler)，实际: %v", rec)
		}
	}()
	m.pipeDirZip(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil), ol, "/root", "root")
}
