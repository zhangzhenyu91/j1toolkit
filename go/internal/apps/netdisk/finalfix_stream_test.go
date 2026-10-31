// Final review fix：网盘文档源必须提供真正的流式回源接口（OpenFileStream），
// 大文件首块可在上游完成前读取，Close 终止上游连接。全部本地 httptest。
package netdisk

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// gatedOpenList 返回门控下载：先写 first 字节并 flush，等待 release 再写剩余内容。
func gatedOpenList(t *testing.T, size int64, first, rest string, release <-chan struct{}, upstreamClosed chan<- struct{}) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/auth/login":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"code":200,"data":{"token":"tk"}}`))
		case r.URL.Path == "/api/fs/get":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"code":200,"data":{"size":` + itoa64(size) + `,"is_dir":false,"sign":"s1"}}`))
		case strings.HasPrefix(r.URL.Path, "/d/"):
			w.Header().Set("Content-Type", "application/octet-stream")
			_, _ = io.WriteString(w, first)
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
			select {
			case <-release:
				_, _ = io.WriteString(w, rest)
			case <-r.Context().Done():
				if upstreamClosed != nil {
					close(upstreamClosed)
				}
			}
		default:
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"code":200,"data":null}`))
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func itoa64(n int64) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}

func streamTestModule(srv *httptest.Server) *Module {
	return &Module{
		OL:   &OpenListHTTP{BaseURL: srv.URL, Username: "u", Password: "p"},
		Base: BaseDirs{PersonalRoot: "/nd/my", PublicRoot: "/nd/pub"},
	}
}

// TestOpenFileStreamFirstChunkBeforeUpstreamDone 首块必须在上游写完后即可读到（流式），
// 且返回元数据大小；Close 后上游连接被取消。
func TestOpenFileStreamFirstChunkBeforeUpstreamDone(t *testing.T) {
	release := make(chan struct{})
	closed := make(chan struct{}, 1)
	srv := gatedOpenList(t, 1<<20, "FIRST-CHUNK", strings.Repeat("x", 1<<20), release, closed)
	m := streamTestModule(srv)

	rc, ct, size, err := m.OpenFileStream(context.Background(), "my", "alice", 0, "/big.bin")
	if err != nil {
		t.Fatalf("OpenFileStream: %v", err)
	}
	if size != 1<<20 || ct == "" {
		t.Fatalf("元数据不符 size=%d ct=%q", size, ct)
	}
	buf := make([]byte, len("FIRST-CHUNK"))
	readDone := make(chan error, 1)
	go func() {
		_, err := io.ReadFull(rc, buf)
		readDone <- err
	}()
	select {
	case err := <-readDone:
		if err != nil {
			t.Fatalf("读取首块: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("上游未完成前读不到首块：回源被整读阻塞")
	}
	if string(buf) != "FIRST-CHUNK" {
		t.Fatalf("首块内容不符: %q", buf)
	}
	_ = rc.Close()
	select {
	case <-closed:
	case <-time.After(2 * time.Second):
		t.Fatal("Close 未终止上游下载连接")
	}
}

// TestReadFileBytesStillWorksForSmallFiles 旧整读适配器仍可用（兼容路径）。
func TestReadFileBytesStillWorksForSmallFiles(t *testing.T) {
	release := make(chan struct{})
	close(release)
	srv := gatedOpenList(t, 5, "hello", "", release, nil)
	m := streamTestModule(srv)
	data, ct, err := m.ReadFileBytes(context.Background(), "my", "alice", 0, "/a.txt")
	if err != nil {
		t.Fatalf("ReadFileBytes: %v", err)
	}
	if string(data) != "hello" || ct == "" {
		t.Fatalf("整读内容不符: %q %q", data, ct)
	}
}
