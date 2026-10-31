package office

// Task 6 fix round 1：生产网盘桥必须支持流式写回（NewNetdiskDoc 转发），
// 回调先落本地临时文件（限大小），空体/超限/读取失败不触达上游，成功后流式上传并清理。

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

// streamBridge 同时实现 NetdiskBridge 与可选流式写回接口（模拟生产 netdisk.Module）。
type streamBridge struct {
	mu        sync.Mutex
	streamed  []byte
	size      int64
	streams   int
	writeBack int
	err       error
}

func (b *streamBridge) ReadFileBytes(context.Context, string, string, int64, string) ([]byte, string, error) {
	return nil, "", ErrDocNotFound
}

func (b *streamBridge) WriteFileBytes(context.Context, string, string, int64, string, []byte) error {
	b.mu.Lock()
	b.writeBack++
	b.mu.Unlock()
	return nil
}

func (b *streamBridge) DocKey(context.Context, string, string, int64, string) (string, error) {
	return "k", nil
}

func (b *streamBridge) WriteFileStream(_ context.Context, _, _ string, _ int64, _ string, body io.Reader, size int64) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.streams++
	b.size = size
	data, err := io.ReadAll(body)
	b.streamed = data
	if err != nil {
		return err
	}
	return b.err
}

func (b *streamBridge) snapshot() (streams, writeBack int, data []byte, size int64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.streams, b.writeBack, append([]byte(nil), b.streamed...), b.size
}

// TestNewNetdiskDocImplementsStreamWriter 生产桥必须转发流式写回，而不是只有自带桩。
func TestNewNetdiskDocImplementsStreamWriter(t *testing.T) {
	doc := NewNetdiskDoc(&streamBridge{})
	if _, ok := doc.(NetdiskStreamWriter); !ok {
		t.Fatal("NewNetdiskDoc 产物必须实现 NetdiskStreamWriter（生产装配可达）")
	}
	sb := &streamBridge{}
	doc = NewNetdiskDoc(sb)
	sw := doc.(NetdiskStreamWriter)
	if err := sw.WriteFileStream(context.Background(), "my", "u", 0, "/a.docx",
		strings.NewReader("stream-bytes"), int64(len("stream-bytes"))); err != nil {
		t.Fatalf("WriteFileStream: %v", err)
	}
	streams, writes, data, size := sb.snapshot()
	if streams != 1 || writes != 0 || string(data) != "stream-bytes" || size != int64(len("stream-bytes")) {
		t.Fatalf("转发不符: streams=%d writes=%d data=%q size=%d", streams, writes, data, size)
	}
}

// TestWriteBackTempFileStreaming 回调先写临时文件、再流式上传，成功后清理临时文件。
func TestWriteBackTempFileStreaming(t *testing.T) {
	payload := bytes.Repeat([]byte("x"), 1<<20)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "1048576")
		_, _ = w.Write(payload)
	}))
	defer srv.Close()

	tmpDir := t.TempDir()
	sb := &streamBridge{}
	m := New(nil)
	m.Netdisk = NewNetdiskDoc(sb)
	m.SaveTempDir = tmpDir
	if err := m.writeBackFromURL(context.Background(), NDP{Space: "my", User: "u", Path: "/a.docx"}, srv.URL+"/doc"); err != nil {
		t.Fatalf("写回应成功: %v", err)
	}
	streams, writes, data, size := sb.snapshot()
	if streams != 1 || writes != 0 {
		t.Fatalf("应走流式写回: streams=%d writes=%d", streams, writes)
	}
	if !bytes.Equal(data, payload) || size != int64(len(payload)) {
		t.Fatalf("流式内容/长度不符: len=%d size=%d", len(data), size)
	}
	entries, _ := os.ReadDir(tmpDir)
	if len(entries) != 0 {
		t.Fatalf("临时文件未清理: %v", entries)
	}
}

// TestWriteBackEmptyOrOverBudgetNoUpstream 空体/超预算不得触达上游，且不残留临时文件。
func TestWriteBackEmptyOrOverBudgetNoUpstream(t *testing.T) {
	cases := []struct {
		name    string
		body    string
		max     int64
		wantErr error
	}{
		{"empty", "", 1024, nil},
		{"over", "0123456789", 8, ErrDocTooLarge},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = w.Write([]byte(c.body))
			}))
			defer srv.Close()
			tmpDir := t.TempDir()
			sb := &streamBridge{}
			m := New(nil)
			m.Netdisk = NewNetdiskDoc(sb)
			m.SaveTempDir = tmpDir
			m.SaveMaxBytes = c.max
			err := m.writeBackFromURL(context.Background(), NDP{Path: "/a.docx"}, srv.URL+"/doc")
			if err == nil {
				t.Fatal("应报错")
			}
			if c.wantErr != nil && !errors.Is(err, c.wantErr) {
				t.Fatalf("错误 = %v，期望 %v", err, c.wantErr)
			}
			streams, writes, _, _ := sb.snapshot()
			if streams != 0 || writes != 0 {
				t.Fatalf("空体/超限不得触达上游: streams=%d writes=%d", streams, writes)
			}
			entries, _ := os.ReadDir(tmpDir)
			if len(entries) != 0 {
				t.Fatalf("临时文件未清理: %v", entries)
			}
		})
	}
}

// TestWriteBackReadFailureNoUpstream 读取中途失败不得触达上游。
func TestWriteBackReadFailureNoUpstream(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "100")
		_, _ = w.Write([]byte("short"))
		// 直接断开，模拟读流失败。
		hj, _ := w.(http.Hijacker)
		if conn, _, err := hj.Hijack(); err == nil {
			_ = conn.Close()
		}
	}))
	defer srv.Close()
	tmpDir := t.TempDir()
	sb := &streamBridge{}
	m := New(nil)
	m.Netdisk = NewNetdiskDoc(sb)
	m.SaveTempDir = tmpDir
	_ = m.writeBackFromURL(context.Background(), NDP{Path: "/a.docx"}, srv.URL+"/doc")
	streams, writes, _, _ := sb.snapshot()
	if streams != 0 || writes != 0 {
		t.Fatalf("读取失败不得触达上游: streams=%d writes=%d", streams, writes)
	}
	entries, _ := os.ReadDir(tmpDir)
	if len(entries) != 0 {
		t.Fatalf("临时文件未清理: %v", entries)
	}
}

// TestWriteBackTimeoutWithTemp 慢回源超时后不得触达上游。
func TestWriteBackTimeoutWithTemp(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(300 * time.Millisecond)
		_, _ = w.Write([]byte("late"))
	}))
	defer srv.Close()
	tmpDir := t.TempDir()
	sb := &streamBridge{}
	m := New(nil)
	m.Netdisk = NewNetdiskDoc(sb)
	m.SaveTempDir = tmpDir
	m.SaveFetchTimeout = 40 * time.Millisecond
	if err := m.writeBackFromURL(context.Background(), NDP{Path: "/a.docx"}, srv.URL+"/doc"); err == nil {
		t.Fatal("超时应报错")
	}
	streams, writes, _, _ := sb.snapshot()
	if streams != 0 || writes != 0 {
		t.Fatalf("超时不得触达上游: streams=%d writes=%d", streams, writes)
	}
	entries, _ := os.ReadDir(tmpDir)
	if len(entries) != 0 {
		t.Fatalf("临时文件未清理: %v", entries)
	}
}

// TestOfficeOversizeMaps413 普通 HTTP（/config、/raw）超大小映射 413；OO 回调遵循契约 200+error:1。
func TestOfficeOversizeMaps413(t *testing.T) {
	rec := httptest.NewRecorder()
	rawFetchFail(rec, ErrDocTooLarge)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("rawFetchFail 超大小应 413，实际 %d", rec.Code)
	}
	rec = httptest.NewRecorder()
	refFail(rec, ErrDocTooLarge)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("refFail 超大小应 413，实际 %d", rec.Code)
	}

	rec = httptest.NewRecorder()
	writeCallbackWriteBackErr(rec, ErrDocTooLarge)
	if rec.Code != http.StatusOK {
		t.Fatalf("OO 回调写回失败应 200，实际 %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"error":1`) || !strings.Contains(rec.Body.String(), "超出大小限制") {
		t.Fatalf("OO 回调超限应答不符: %s", rec.Body.String())
	}
}
