package worklog

// Task 6 fix round 2：PDF 并发名额只覆盖「生成」阶段；生成完成后立即释放，
// 慢客户端写响应 / save-netdisk 网络上传不得继续占名额。

import (
	"context"
	"database/sql/driver"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"shade/internal/auth"
)

// gateObjects 回源阻塞到 gate 放行（生成阶段未完成）。
type gateObjects struct {
	*memObjects
	gate    chan struct{}
	started chan struct{}
	once    sync.Once
}

func (g *gateObjects) GetBuffer(ctx context.Context, key string) ([]byte, error) {
	g.once.Do(func() { close(g.started) })
	select {
	case <-g.gate:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return g.memObjects.GetBuffer(ctx, key)
}

// blockingWriter 在首次 Write（响应体）时阻塞，用于观察「生成完成后写响应」期间的名额状态。
type blockingWriter struct {
	http.ResponseWriter
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func (b *blockingWriter) Write(p []byte) (int, error) {
	b.once.Do(func() {
		close(b.started)
		<-b.release
	})
	return b.ResponseWriter.Write(p)
}

// TestPDFBudgetHeldDuringGeneration 生成未完成时名额必须仍被占用。
func TestPDFBudgetHeldDuringGeneration(t *testing.T) {
	env := newTestServer(t, identUser).withTeam()
	env.fake.on("FROM worklog_photo p JOIN worklog_entry e", []string{"entry_id", "cos_key", "url"},
		[][]driver.Value{{int64(1), "worklog/a/1-1.png", ""}})
	store := &gateObjects{
		memObjects: env.objects, gate: make(chan struct{}), started: make(chan struct{}),
	}
	env.objects.items["worklog/a/1-1.png"] = tinyPNG(t)
	env.module.Store = store

	done := make(chan struct{})
	go func() {
		defer close(done)
		req := httptest.NewRequest(http.MethodGet, "/api/v1/worklog/photos.pdf?from=2026-08-18&to=2026-08-18", nil)
		req.Header.Set("Authorization", "Bearer test")
		env.handler.ServeHTTP(httptest.NewRecorder(), req)
	}()
	<-store.started
	if got := len(env.module.pdfJobSem); got != 1 {
		t.Fatalf("生成中应占用 1 个名额，实际 %d/%d", got, cap(env.module.pdfJobSem))
	}
	close(store.gate)
	<-done
	if got := len(env.module.pdfJobSem); got != 0 {
		t.Fatalf("生成完成后名额未释放：%d", got)
	}
}

// TestPDFBudgetReleasedBeforeSlowResponseWrite 慢客户端写响应期间名额必须已释放。
func TestPDFBudgetReleasedBeforeSlowResponseWrite(t *testing.T) {
	env := newTestServer(t, identUser).withTeam()
	env.fake.on("FROM worklog_photo p JOIN worklog_entry e", []string{"entry_id", "cos_key", "url"},
		[][]driver.Value{{int64(1), "worklog/a/1-1.png", ""}})
	env.objects.items["worklog/a/1-1.png"] = tinyPNG(t)

	bw := &blockingWriter{
		ResponseWriter: httptest.NewRecorder(),
		started:        make(chan struct{}),
		release:        make(chan struct{}),
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		req := httptest.NewRequest(http.MethodGet, "/api/v1/worklog/photos.pdf?from=2026-08-18&to=2026-08-18", nil)
		req.Header.Set("Authorization", "Bearer test")
		env.handler.ServeHTTP(bw, req)
	}()
	select {
	case <-bw.started:
	case <-time.After(3 * time.Second):
		t.Fatal("未开始写响应")
	}
	if got := len(env.module.pdfJobSem); got != 0 {
		t.Fatalf("写响应期间名额应已释放，实际占用 %d/%d", got, cap(env.module.pdfJobSem))
	}
	close(bw.release)
	<-done
}

// blockSaver save-netdisk 网络上传阻塞桩。
type blockSaver struct {
	started chan struct{}
	release chan struct{}
}

func (s *blockSaver) SaveToNetdisk(context.Context, auth.Identity, NetdiskSaveRequest) (string, error) {
	close(s.started)
	<-s.release
	return "我的空间/出工日志/x.pdf", nil
}

// TestPDFBudgetReleasedBeforeSaveUpload 生成完成、save-netdisk 上传期间名额必须已释放。
func TestPDFBudgetReleasedBeforeSaveUpload(t *testing.T) {
	env := newTestServer(t, identUser).withTeam()
	env.fake.on("FROM worklog_photo p JOIN worklog_entry e", []string{"entry_id", "cos_key", "url"},
		[][]driver.Value{{int64(1), "worklog/a/1-1.png", ""}})
	env.objects.items["worklog/a/1-1.png"] = tinyPNG(t)
	saver := &blockSaver{started: make(chan struct{}), release: make(chan struct{})}
	env.module.Netdisk = saver

	done := make(chan struct{})
	go func() {
		defer close(done)
		rec := env.do(t, http.MethodPost, "/api/v1/worklog/photos-pdf/save-netdisk",
			map[string]any{"from": "2026-08-18", "to": "2026-08-18"})
		if rec.Code != http.StatusOK {
			t.Errorf("保存应成功: %d %s", rec.Code, rec.Body.String())
		}
	}()
	select {
	case <-saver.started:
	case <-time.After(3 * time.Second):
		t.Fatal("未开始 save-netdisk 上传")
	}
	if got := len(env.module.pdfJobSem); got != 0 {
		t.Fatalf("上传期间名额应已释放，实际占用 %d/%d", got, cap(env.module.pdfJobSem))
	}
	close(saver.release)
	<-done
}
