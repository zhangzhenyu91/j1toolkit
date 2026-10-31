package netdisk

// 可编程 OpenList 客户端桩（测试专用）：按路径注入目录/文件/错误，记录目录列表调用，
// 供 dirsize 缓存/单飞与 ZIP 预取测试使用；不访问真实 OpenList。

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
)

// stubOL 实现 OpenListClient；未识别接口默认成功（写操作只需触发失效语义）。
type stubOL struct {
	mu sync.Mutex

	listData   map[string][]mapItemSrc
	listErr    map[string]error
	listHook   func(ctx context.Context, abs string) error
	listStarts map[string]int
	getErr     error
	isDir      bool
	files      map[string]string
	fileErr    map[string]error
	download   func(abs string) (io.ReadCloser, http.Header, error)
	failAPI    map[string]error
}

func newStubOL() *stubOL {
	return &stubOL{
		listData:   map[string][]mapItemSrc{},
		listErr:    map[string]error{},
		listStarts: map[string]int{},
		files:      map[string]string{},
		fileErr:    map[string]error{},
		failAPI:    map[string]error{},
		isDir:      true,
	}
}

func (s *stubOL) EnsureConfigured() error { return nil }

func (s *stubOL) Call(ctx context.Context, _ string, apiPath string, opts CallOpts) (json.RawMessage, error) {
	s.mu.Lock()
	fail := s.failAPI[apiPath]
	s.mu.Unlock()
	if fail != nil {
		return nil, fail
	}
	switch apiPath {
	case "/api/fs/get":
		if s.getErr != nil {
			return nil, s.getErr
		}
		return json.Marshal(map[string]any{"is_dir": s.isDir})
	case "/api/fs/list":
		abs := stubBodyPath(opts.Body)
		s.mu.Lock()
		s.listStarts[abs]++
		hook := s.listHook
		err := s.listErr[abs]
		items := s.listData[abs]
		s.mu.Unlock()
		if hook != nil {
			if err := hook(ctx, abs); err != nil {
				return nil, err
			}
		}
		if err != nil {
			return nil, err
		}
		return json.Marshal(map[string]any{"content": items})
	}
	return json.RawMessage("null"), nil
}

func (s *stubOL) DownloadStream(_ context.Context, absPath string) (io.ReadCloser, http.Header, error) {
	if s.download != nil {
		return s.download(absPath)
	}
	s.mu.Lock()
	content, ok := s.files[absPath]
	err := s.fileErr[absPath]
	s.mu.Unlock()
	if err != nil {
		return nil, nil, err
	}
	if !ok {
		return nil, nil, fmt.Errorf("stub: 文件不存在 %s", absPath)
	}
	return io.NopCloser(strings.NewReader(content)), http.Header{}, nil
}

func (s *stubOL) EnsureDir(context.Context, string) error { return nil }
func (s *stubOL) PutEmpty(context.Context, string) error  { return nil }
func (s *stubOL) FsPut(context.Context, string, io.Reader, int64) error {
	return nil
}
func (s *stubOL) ArchiveDownloadStream(context.Context, string, string, string) (io.ReadCloser, http.Header, error) {
	return nil, nil, errors.New("stub: 不支持包内下载")
}

func (s *stubOL) listCount(abs string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.listStarts[abs]
}

func (s *stubOL) totalListCalls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	total := 0
	for _, n := range s.listStarts {
		total += n
	}
	return total
}

// stubBodyPath 取 /api/fs/list 请求体中的 path。
func stubBodyPath(body any) string {
	m, ok := body.(map[string]any)
	if !ok {
		return ""
	}
	p, _ := m["path"].(string)
	return p
}
