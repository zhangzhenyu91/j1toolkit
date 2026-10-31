// Final review fix：KVM 网盘 → 设备必须经 NetdiskStreamBridge 流式两跳，
// 大文件不整读；推送开始即向设备发送字节，结束后关闭网盘 body。本地 httptest。
package kvm

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"shade/internal/auth"
)

// gatedNetdisk 流式网盘桩：先吐 first，等待 release 再吐 rest；Close 记录关闭。
type gatedNetdisk struct {
	first, rest string
	release     chan struct{}
	once        sync.Once
	closed      atomic.Bool
	downloaded  atomic.Bool
}

func (n *gatedNetdisk) AssertAccess(context.Context, int64) error { return nil }
func (n *gatedNetdisk) Download(context.Context, auth.Identity, string, string) ([]byte, error) {
	return nil, context.DeadlineExceeded
}
func (n *gatedNetdisk) Save(context.Context, auth.Identity, string, string, string, io.Reader, int64) (string, error) {
	return "", nil
}
func (n *gatedNetdisk) DownloadStream(context.Context, auth.Identity, string, string) (io.ReadCloser, int64, error) {
	n.downloaded.Store(true)
	return &gatedReadCloser{n: n}, int64(len(n.first) + len(n.rest)), nil
}

type gatedReadCloser struct {
	n    *gatedNetdisk
	step int
}

func (r *gatedReadCloser) Read(p []byte) (int, error) {
	switch r.step {
	case 0:
		r.step++
		return copy(p, r.n.first), nil
	case 1:
		r.step++
		<-r.n.release
		return copy(p, r.n.rest), nil
	default:
		return 0, io.EOF
	}
}

func (r *gatedReadCloser) Close() error {
	r.n.closed.Store(true)
	r.n.once.Do(func() { close(r.n.release) })
	return nil
}

// TestPullFromNetdiskStreamsBeforeFullDownload 生产处理器优先走流式桥：设备收到字节时
// 网盘 body 尚未读尽；完成后 body 关闭。
func TestPullFromNetdiskStreamsBeforeFullDownload(t *testing.T) {
	started := make(chan struct{})
	var startOnce sync.Once
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/fileshare/push" {
			http.NotFound(w, r)
			return
		}
		buf := make([]byte, 1)
		if _, err := io.ReadFull(r.Body, buf); err == nil {
			startOnce.Do(func() { close(started) })
		}
		_, _ = io.Copy(io.Discard, r.Body)
		_, _ = w.Write([]byte(`{"pushed":1}`))
	}))
	defer srv.Close()

	env := newTestServer(t, identUser)
	env.fake.on("FROM sys_team", teamRule().cols, teamRule().rows)
	env.withProxy(srv.URL)
	gated := &gatedNetdisk{first: "FIRST", rest: "REST", release: make(chan struct{})}
	env.module.Netdisk = gated

	done := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		done <- env.do(t, http.MethodPost, "/api/v1/kvm/devices/11/pull-from-netdisk", map[string]any{
			"space": "my", "paths": []string{"/docs/big.bin"},
		})
	}()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		gated.once.Do(func() { close(gated.release) })
		t.Fatal("网盘 body 未读尽前设备未收到推送：仍是整读")
	}
	if gated.closed.Load() {
		t.Fatal("推送尚未结束就关闭了网盘 body")
	}
	gated.once.Do(func() { close(gated.release) })
	var rec *httptest.ResponseRecorder
	select {
	case rec = <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("推送未在释放后完成")
	}
	if !gated.downloaded.Load() {
		t.Fatal("应走 DownloadStream 流式桥")
	}
	var body struct {
		Code int `json:"code"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	if rec.Code != http.StatusOK || body.Code != 0 {
		t.Fatalf("推送应成功: %d %s", rec.Code, rec.Body.String())
	}
	if !gated.closed.Load() {
		t.Fatal("推送结束后必须关闭网盘 body")
	}
	if !strings.Contains(rec.Body.String(), `"ok":true`) {
		t.Fatalf("条目应成功: %s", rec.Body.String())
	}
}
