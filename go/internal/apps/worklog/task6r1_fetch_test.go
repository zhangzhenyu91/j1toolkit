package worklog

// Task 6 fix round 1：对象存储 GetBuffer 失败（超预算/非 2xx/网络）不得静默回退公网 URL 二次请求。

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

// errGetObjects 对象存储 GetBuffer 恒失败。
type errGetObjects struct{ err error }

func (e errGetObjects) PutBuffer(context.Context, string, []byte, string) error { return nil }
func (e errGetObjects) DeleteObject(context.Context, string) error              { return nil }
func (e errGetObjects) PublicURL(string) string                                 { return "" }
func (e errGetObjects) GetBuffer(context.Context, string) ([]byte, error)       { return nil, e.err }

// TestFetchObjectBytesNoFallbackOnGetError GetBuffer 报错后不得再请求公开 URL。
func TestFetchObjectBytesNoFallbackOnGetError(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		_, _ = w.Write([]byte("should-not-fetch"))
	}))
	defer srv.Close()

	store := errGetObjects{err: errors.New("COS 对象超出大小预算")}
	buf, err := fetchObjectBytes(context.Background(), store, "worklog/x.jpg", srv.URL+"/x.jpg")
	if err == nil {
		t.Fatalf("GetBuffer 失败应直接报错，实际返回 %q", buf)
	}
	if len(buf) != 0 {
		t.Fatalf("失败不得返回内容：%q", buf)
	}
	if got := atomic.LoadInt32(&hits); got != 0 {
		t.Fatalf("GetBuffer 失败后不得回退公网二次请求，实际 %d 次", got)
	}
}
