package integrations

// Task 6 资源边界回归：COS 生产回源必须带签名、超时与字节预算；非 2xx 不得返回内容。

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// task6COS 构造指向 httptest 的 COSStore（端点注入，签名正常计算）。
func task6COS(endpoint string) *COSStore {
	return &COSStore{
		Cfg: NewMapProvider(map[string]string{
			"COS_SECRET_ID":  goldenSecretID,
			"COS_SECRET_KEY": goldenSecretKey,
			"COS_BUCKET":     "bucket-1250000000",
			"COS_REGION":     "ap-guangzhou",
		}),
		Endpoint: endpoint,
	}
}

// TestCOSGetBufferSignedStatusAndBudget GetBuffer 必须签名请求、非 2xx 报错、超字节预算显式报错。
func TestCOSGetBufferSignedStatusAndBudget(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		switch r.URL.Path {
		case "/missing.jpg":
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte("NoSuchKey"))
		case "/big.jpg":
			_, _ = w.Write([]byte("0123456789"))
		default:
			_, _ = w.Write([]byte("photo-bytes"))
		}
	}))
	defer srv.Close()

	store := task6COS(srv.URL)
	store.GetMaxBytes = 64

	// 正常读取：返回内容且请求已签名。
	buf, err := store.GetBuffer(context.Background(), "ok.jpg")
	if err != nil {
		t.Fatalf("GetBuffer: %v", err)
	}
	if string(buf) != "photo-bytes" {
		t.Fatalf("GetBuffer = %q", buf)
	}
	if !strings.HasPrefix(gotAuth, "q-sign-algorithm=sha1") {
		t.Fatalf("回源请求必须带 COS 签名，实际 %q", gotAuth)
	}

	// 非 2xx 不得返回内容。
	if buf, err := store.GetBuffer(context.Background(), "missing.jpg"); err == nil || len(buf) != 0 {
		t.Fatalf("404 应报错且不返回内容：buf=%q err=%v", buf, err)
	}

	// 超字节预算必须显式报错，不能截断后当完整内容。
	store.GetMaxBytes = 4
	buf, err = store.GetBuffer(context.Background(), "big.jpg")
	if err == nil {
		t.Fatalf("超预算应报错，实际返回 %q", buf)
	}
	if !errors.Is(err, ErrCOSObjectTooLarge) {
		t.Fatalf("超预算错误应可辨识，实际 %v", err)
	}
	if len(buf) != 0 {
		t.Fatalf("超预算不得返回截断内容：%q", buf)
	}
}

// TestCOSGetBufferTimeout 回源受超时/取消约束。
func TestCOSGetBufferTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(300 * time.Millisecond)
		_, _ = w.Write([]byte("late"))
	}))
	defer srv.Close()

	store := task6COS(srv.URL)
	store.GetTimeout = 40 * time.Millisecond
	start := time.Now()
	_, err := store.GetBuffer(context.Background(), "slow.jpg")
	if err == nil {
		t.Fatal("慢响应应超时报错")
	}
	if elapsed := time.Since(start); elapsed > 250*time.Millisecond {
		t.Fatalf("超时未生效，耗时 %s", elapsed)
	}

	// 调用方取消也必须中止回源。
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := store.GetBuffer(ctx, "x.jpg"); err == nil {
		t.Fatal("已取消的 ctx 应报错")
	}
}
