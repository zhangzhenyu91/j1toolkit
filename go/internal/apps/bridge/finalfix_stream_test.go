// Final review fix：bridge.KvmBridge 对真实 netdisk.Module 的流式转发（网盘 → 设备第一跳）。
package bridge

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"shade/internal/apps/netdisk"
	"shade/internal/auth"
)

// TestKvmBridgeDownloadStreamUsesRealNetdisk 生产适配：NewKvmBridge(*netdisk.Module) 返回
// 上游流式 body，首块在上游完成前可读，Close 后连接释放。
func TestKvmBridgeDownloadStreamUsesRealNetdisk(t *testing.T) {
	release := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/auth/login":
			_, _ = w.Write([]byte(`{"code":200,"data":{"token":"tk"}}`))
		case r.URL.Path == "/api/fs/get":
			_, _ = w.Write([]byte(`{"code":200,"data":{"size":1024,"is_dir":false,"sign":"s"}}`))
		case strings.HasPrefix(r.URL.Path, "/d/"):
			_, _ = io.WriteString(w, "FIRST")
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
			<-release
			_, _ = io.WriteString(w, strings.Repeat("x", 1019))
		default:
			_, _ = w.Write([]byte(`{"code":200,"data":null}`))
		}
	}))
	defer upstream.Close()
	defer close(release)

	nd := &netdisk.Module{
		OL:   &netdisk.OpenListHTTP{BaseURL: upstream.URL, Username: "u", Password: "p"},
		Base: netdisk.BaseDirs{PersonalRoot: "/nd/my"},
	}
	kb := NewKvmBridge(nd)
	rc, size, err := kb.DownloadStream(context.Background(), auth.Identity{Username: "alice"}, "my", "/big.bin")
	if err != nil {
		t.Fatalf("DownloadStream: %v", err)
	}
	defer rc.Close()
	if size != 1024 {
		t.Fatalf("size 应来自网盘元数据 1024，实际 %d", size)
	}
	buf := make([]byte, 5)
	done := make(chan error, 1)
	go func() { _, err := io.ReadFull(rc, buf); done <- err }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("读首块: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("首块未在上游完成前返回：仍是整读")
	}
	if string(buf) != "FIRST" {
		t.Fatalf("首块内容: %q", buf)
	}
}
