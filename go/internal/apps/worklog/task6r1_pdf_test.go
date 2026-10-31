package worklog

// Task 6 fix round 1：组合 PDF 全局并发预算必须覆盖同步 GET /photos.pdf 与 save-netdisk，
// 占满时立即拒绝（明确业务码），不能排队等待。

import (
	"context"
	"database/sql/driver"
	"net/http"
	"strings"
	"testing"
	"time"
)

// fillPDFBudget 占满全局 PDF 并发预算。
func fillPDFBudget(t *testing.T, m *Module) {
	t.Helper()
	m.ensurePDFJobSem()
	for i := 0; i < cap(m.pdfJobSem); i++ {
		m.pdfJobSem <- struct{}{}
	}
	t.Cleanup(func() {
		for i := 0; i < cap(m.pdfJobSem); i++ {
			select {
			case <-m.pdfJobSem:
			default:
			}
		}
	})
}

// ctxBlockObjects 回源阻塞到 ctx 结束的对象存储桩（验证 PDF 任务总超时）。
type ctxBlockObjects struct{ *memObjects }

func (c *ctxBlockObjects) GetBuffer(ctx context.Context, key string) ([]byte, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

// TestPhotosPDFJobTotalTimeout 后台任务有总超时，超时后任务失败并给出明确错误。
func TestPhotosPDFJobTotalTimeout(t *testing.T) {
	env := newTestServer(t, identUser).withTeam()
	env.module.DisableAsync = false
	env.module.PDFTimeout = 60 * time.Millisecond
	env.fake.on("FROM worklog_photo p JOIN worklog_entry e", []string{"entry_id", "cos_key", "url"},
		[][]driver.Value{{int64(1), "worklog/a/1-1.png", ""}})
	env.module.Store = &ctxBlockObjects{memObjects: env.objects}

	rec := env.do(t, "POST", "/api/v1/worklog/photos.pdf/jobs",
		map[string]any{"from": "2026-08-18", "to": "2026-08-18"})
	jobID := pdfJobIDOf(t, decodeEnv(t, rec))
	st := waitPDFJob(t, env, jobID, pdfJobFailed)
	if msg, _ := st["error"].(string); !strings.Contains(msg, "超时") {
		t.Fatalf("超时任务错误应说明超时，实际 %q", msg)
	}
}

func TestPhotosPDFSyncConcurrencyBudget(t *testing.T) {
	env := newTestServer(t, identUser).withTeam()
	fetched := make(chan struct{}, 2)
	env.module.Store = &hookObjects{memObjects: env.objects, onFetch: func(string) { fetched <- struct{}{} }}
	fillPDFBudget(t, env.module)

	cases := []struct {
		method, path string
		body         any
	}{
		{"GET", "/api/v1/worklog/photos.pdf?from=2026-08-18&to=2026-08-18", nil},
		{"POST", "/api/v1/worklog/photos-pdf/save-netdisk",
			map[string]any{"from": "2026-08-18", "to": "2026-08-18"}},
	}
	for _, c := range cases {
		rec := env.do(t, c.method, c.path, c.body)
		envr := decodeEnv(t, rec)
		if rec.Code != http.StatusConflict || envr.Code != codeOpRunning {
			t.Fatalf("%s 并发占满应 409/%d，实际 %d/%d %s",
				c.path, codeOpRunning, rec.Code, envr.Code, envr.Message)
		}
	}
	select {
	case <-fetched:
		t.Fatal("并发占满时不得开始回源")
	case <-time.After(100 * time.Millisecond):
	}
}
