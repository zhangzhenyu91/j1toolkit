// Final review fix：Office raw 网盘回源必须经生产 NewNetdiskDoc 流式转发，
// 大文档首块在上游完成前即可到达客户端，不整读 512MiB。使用真实 netdisk.Module + httptest OpenList。
package office_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"shade/internal/apps/netdisk"
	"shade/internal/apps/office"
)

func itoa(n int64) string {
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

// TestRawNetdiskStreamsThroughProductionBridge 真实装配：office.Module.Netdisk = NewNetdiskDoc(netdisk.Module)，
// /raw 在 OnlyOffice 上游未写完时即可读到首块，并带正确 Content-Length/总长度。
func TestRawNetdiskStreamsThroughProductionBridge(t *testing.T) {
	const first = "FIRST-8K!"
	const total = int64(2 << 20)
	release := make(chan struct{})
	var releaseOnce sync.Once
	releaseNow := func() { releaseOnce.Do(func() { close(release) }) }
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/auth/login":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"code":200,"data":{"token":"tk"}}`))
		case r.URL.Path == "/api/fs/get":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"code":200,"data":{"size":` + itoa(total) + `,"is_dir":false,"sign":"s1"}}`))
		case strings.HasPrefix(r.URL.Path, "/d/"):
			w.Header().Set("Content-Type", "application/octet-stream")
			_, _ = io.WriteString(w, first)
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
			<-release
			_, _ = io.WriteString(w, strings.Repeat("x", int(total)-len(first)))
		default:
			_, _ = w.Write([]byte(`{"code":200,"data":null}`))
		}
	}))
	defer upstream.Close()

	nd := &netdisk.Module{
		OL:   &netdisk.OpenListHTTP{BaseURL: upstream.URL, Username: "u", Password: "p"},
		Base: netdisk.BaseDirs{PersonalRoot: "/nd/my"},
	}
	m := &office.Module{TicketSecret: "tk-secret", Netdisk: office.NewNetdiskDoc(nd)}
	token, err := m.SignTicket(context.Background(), office.TicketPayload{
		Src: "ndp", Name: "big.bin", NDP: &office.NDP{Space: "my", User: "alice", Path: "/big.bin"},
	}, 60)
	if err != nil {
		t.Fatalf("SignTicket: %v", err)
	}
	r := chi.NewRouter()
	r.Route("/api/v1", func(r chi.Router) { m.RegisterRoutes(r) })
	srv := httptest.NewServer(r)
	defer srv.Close()
	defer releaseNow()

	resp, err := http.Get(srv.URL + "/api/v1/office/raw/" + token + "/big.bin")
	if err != nil {
		t.Fatalf("raw 请求: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("raw status=%d body=%s", resp.StatusCode, body)
	}
	if cl := resp.Header.Get("Content-Length"); cl != itoa(total) {
		t.Fatalf("Content-Length 应来自网盘元数据 %d，实际 %q", total, cl)
	}
	buf := make([]byte, len(first))
	readDone := make(chan error, 1)
	go func() {
		_, err := io.ReadFull(resp.Body, buf)
		readDone <- err
	}()
	select {
	case err := <-readDone:
		if err != nil {
			t.Fatalf("读取首块: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("上游未完成前 raw 未输出首块：仍是整读")
	}
	if string(buf) != first {
		t.Fatalf("首块内容不符: %q", buf)
	}
	releaseNow()
	rest, err := io.Copy(io.Discard, resp.Body)
	if err != nil {
		t.Fatalf("读取剩余: %v", err)
	}
	if got := int64(len(first)) + rest; got != total {
		t.Fatalf("总长度 %d，期望 %d", got, total)
	}
}
