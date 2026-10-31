package office

// Task 6 回源边界回归：保存回调拉回必须 2xx、有超时与字节预算；网盘实现流式写回时优先流式。

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// recNetdisk 记录写回调用的网盘桩（仅 NetdiskDoc 方法集）。
type recNetdisk struct {
	writes  int32
	streams int32
	got     []byte
}

func (r *recNetdisk) Fetch(context.Context, NDP, string) ([]byte, string, error) {
	return nil, "", ErrDocNotFound
}

func (r *recNetdisk) WriteBack(_ context.Context, _ NDP, data []byte) error {
	atomic.AddInt32(&r.writes, 1)
	r.got = append([]byte(nil), data...)
	return nil
}

// streamNetdisk 额外实现流式写回能力。
type streamNetdisk struct {
	recNetdisk
	streamBytes int64
}

func (s *streamNetdisk) WriteFileStream(_ context.Context, space, username string, teamID int64, relPath string, body io.Reader, size int64) error {
	atomic.AddInt32(&s.streams, 1)
	data, err := io.ReadAll(body)
	s.streamBytes = int64(len(data))
	return err
}

// TestWriteBackRejectsNon2xx 回源非 2xx 不得落盘。
func TestWriteBackRejectsNon2xx(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("upstream-broken"))
	}))
	defer srv.Close()
	nd := &recNetdisk{}
	m := New(nil)
	m.SaveTempDir = t.TempDir()
	m.Netdisk = nd
	if err := m.writeBackFromURL(context.Background(), NDP{Space: "my", User: "u", Path: "/a.docx"}, srv.URL+"/doc"); err == nil {
		t.Fatal("非 2xx 回源应报错")
	}
	if atomic.LoadInt32(&nd.writes) != 0 {
		t.Fatal("非 2xx 不得写回网盘")
	}
}

// TestWriteBackTimeout 回源有超时，慢上游快速失败。
func TestWriteBackTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(400 * time.Millisecond)
		_, _ = w.Write([]byte("late"))
	}))
	defer srv.Close()
	nd := &recNetdisk{}
	m := New(nil)
	m.SaveTempDir = t.TempDir()
	m.Netdisk = nd
	m.SaveFetchTimeout = 40 * time.Millisecond
	start := time.Now()
	if err := m.writeBackFromURL(context.Background(), NDP{Space: "my", User: "u", Path: "/a.docx"}, srv.URL+"/doc"); err == nil {
		t.Fatal("慢回源应超时失败")
	}
	if elapsed := time.Since(start); elapsed > 300*time.Millisecond {
		t.Fatalf("超时未生效，耗时 %s", elapsed)
	}
	if atomic.LoadInt32(&nd.writes) != 0 {
		t.Fatal("超时不得写回网盘")
	}
}

// TestWriteBackByteBudget 回源字节预算显式报错且不写回。
func TestWriteBackByteBudget(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("0123456789"))
	}))
	defer srv.Close()
	nd := &recNetdisk{}
	m := New(nil)
	m.SaveTempDir = t.TempDir()
	m.Netdisk = nd
	m.SaveMaxBytes = 8
	if err := m.writeBackFromURL(context.Background(), NDP{Space: "my", User: "u", Path: "/a.docx"}, srv.URL+"/doc"); err == nil {
		t.Fatal("超字节预算应报错")
	}
	if atomic.LoadInt32(&nd.writes) != 0 {
		t.Fatal("超预算不得写回网盘")
	}
}

// TestWriteBackStreamsWhenSupported 网盘实现流式写回时不得整读入内存。
func TestWriteBackStreamsWhenSupported(t *testing.T) {
	payload := strings.Repeat("x", 1<<20)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "1048576")
		_, _ = w.Write([]byte(payload))
	}))
	defer srv.Close()
	nd := &streamNetdisk{}
	m := New(nil)
	m.SaveTempDir = t.TempDir()
	m.Netdisk = nd
	if err := m.writeBackFromURL(context.Background(), NDP{Space: "my", User: "u", Path: "/a.docx"}, srv.URL+"/doc"); err != nil {
		t.Fatalf("流式写回应成功: %v", err)
	}
	if atomic.LoadInt32(&nd.streams) != 1 {
		t.Fatalf("应走流式写回，实际 stream=%d fallback=%d", nd.streams, nd.writes)
	}
	if nd.streamBytes != int64(len(payload)) {
		t.Fatalf("流式字节 = %d want %d", nd.streamBytes, len(payload))
	}
}

// TestWriteBackEmptyBody 空产物不得写回。
func TestWriteBackEmptyBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer srv.Close()
	nd := &recNetdisk{}
	m := New(nil)
	m.SaveTempDir = t.TempDir()
	m.Netdisk = nd
	if err := m.writeBackFromURL(context.Background(), NDP{Path: "/a.docx"}, srv.URL+"/doc"); err == nil {
		t.Fatal("空产物应报错")
	}
	if atomic.LoadInt32(&nd.writes) != 0 {
		t.Fatal("空产物不得写回")
	}
}
