package worklog

// 组合 PDF 后台任务测试：fake DB + httptest + 内存对象存储桩，覆盖任务创建、
// 真实进度推进（按实际回源照片数）、完成取件、失败错误态、无照片 40402 兼容
// 与归属/应用权限；不访问外部服务。

import (
	"context"
	"database/sql/driver"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"shade/internal/auth"
	"shade/internal/httpapi"
)

// hookObjects 带回源钩子的对象存储桩：GetBuffer 前回调 onFetch，可阻塞以观察任务进度。
type hookObjects struct {
	*memObjects
	onFetch func(key string)
}

func (h *hookObjects) GetBuffer(ctx context.Context, key string) ([]byte, error) {
	if h.onFetch != nil {
		h.onFetch(key)
	}
	return h.memObjects.GetBuffer(ctx, key)
}

// failObjects 回源必失败的对象存储桩（无公开地址兜底）。
type failObjects struct{}

func (failObjects) PutBuffer(context.Context, string, []byte, string) error { return nil }
func (failObjects) DeleteObject(context.Context, string) error              { return nil }
func (failObjects) PublicURL(string) string                                 { return "" }
func (failObjects) GetBuffer(context.Context, string) ([]byte, error) {
	return nil, errors.New("回源失败")
}

// denyPermit 拒绝全部用户的应用权限桩。
type denyPermit struct{}

func (denyPermit) Allow(context.Context, int64, string) (bool, error) { return false, nil }

// asUser 以另一身份访问同一模块（任务归属可见性校验用）。
func asUser(env *testEnv, ident auth.Identity) http.Handler {
	r := chi.NewRouter()
	r.Route("/api/v1", func(r chi.Router) { env.module.RegisterRoutes(r) })
	return httpapi.AuthMiddleware(r, fakeAuth{ident: ident})
}

// pdfJobStatus 查任务进度并解包 data。
func pdfJobStatus(t *testing.T, env *testEnv, jobID string) map[string]any {
	t.Helper()
	envr := decodeEnv(t, env.do(t, "GET", "/api/v1/worklog/photos.pdf/jobs/"+jobID, nil))
	if envr.Code != codeOK {
		t.Fatalf("查进度应成功，实际 %d %s", envr.Code, envr.Message)
	}
	data, _ := envr.Data.(map[string]any)
	return data
}

// waitPDFJob 轮询等待任务进入指定状态（fake 环境下毫秒级完成）。
func waitPDFJob(t *testing.T, env *testEnv, jobID, want string) map[string]any {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		st := pdfJobStatus(t, env, jobID)
		if st["status"] == want {
			return st
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("等待任务状态 %s 超时: %+v", want, pdfJobStatus(t, env, jobID))
	return nil
}

// pdfJobIDOf 从建任务响应取 job_id。
func pdfJobIDOf(t *testing.T, envr Envelope) string {
	t.Helper()
	data, _ := envr.Data.(map[string]any)
	jobID, _ := data["job_id"].(string)
	if envr.Code != codeOK || jobID == "" {
		t.Fatalf("建任务应成功且带 job_id，实际 %d %s %+v", envr.Code, envr.Message, data)
	}
	return jobID
}

// ---- 状态推进 + 完成取件 ----

func TestPhotosPDFJobProgressAndPickup(t *testing.T) {
	env := newTestServer(t, identUser).withTeam()
	env.module.DisableAsync = false // 后台异步跑，便于观察中途进度
	env.objects.items["worklog/a/1-1.png"] = tinyPNG(t)
	env.objects.items["worklog/a/2-1.png"] = tinyPNG(t)
	env.fake.on("FROM worklog_photo p JOIN worklog_entry e", []string{"entry_id", "cos_key", "url"},
		[][]driver.Value{{int64(1), "worklog/a/1-1.png", ""}, {int64(2), "worklog/a/2-1.png", ""}})

	started := make(chan string, 2)
	release := make(chan struct{}, 2)
	env.module.Store = &hookObjects{memObjects: env.objects, onFetch: func(key string) {
		started <- key
		<-release
	}}

	rec := env.do(t, "POST", "/api/v1/worklog/photos.pdf/jobs",
		map[string]any{"from": "2026-08-18", "to": "2026-08-18"})
	envr := decodeEnv(t, rec)
	jobID := pdfJobIDOf(t, envr)
	data, _ := envr.Data.(map[string]any)
	if data["total"] != float64(2) {
		t.Fatalf("total 应 2，实际 %v", data["total"])
	}

	// 第一张回源阻塞中：running、进度 0/2，取件应 40909
	<-started
	st := pdfJobStatus(t, env, jobID)
	if st["status"] != pdfJobRunning || st["current"] != float64(0) || st["total"] != float64(2) {
		t.Fatalf("进行中状态不符: %+v", st)
	}
	rec = env.do(t, "GET", "/api/v1/worklog/photos.pdf?job_id="+jobID, nil)
	if envr = decodeEnv(t, rec); envr.Code != codeOpRunning {
		t.Fatalf("进行中取件应 %d，实际 %d %s", codeOpRunning, envr.Code, envr.Message)
	}

	// 放行第一张：进度推进到 1/2（第二张随即阻塞在回源）
	release <- struct{}{}
	<-started
	st = pdfJobStatus(t, env, jobID)
	if st["status"] != pdfJobRunning || st["current"] != float64(1) {
		t.Fatalf("进度应推进到 1，实际 %+v", st)
	}

	// 放行第二张：任务完成 2/2
	release <- struct{}{}
	st = waitPDFJob(t, env, jobID, pdfJobDone)
	if st["current"] != float64(2) || st["total"] != float64(2) || st["error"] != "" {
		t.Fatalf("完成状态不符: %+v", st)
	}

	// 取件：缓存 PDF（文件名与同步口径一致）
	rec = env.do(t, "GET", "/api/v1/worklog/photos.pdf?job_id="+jobID, nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Header().Get("Content-Type"), "application/pdf") {
		t.Fatalf("取件应 PDF，实际 %d %s", rec.Code, rec.Header().Get("Content-Type"))
	}
	if rec.Body.Len() == 0 {
		t.Fatal("缓存 PDF 为空")
	}
	if cd := rec.Header().Get("Content-Disposition"); !strings.Contains(cd, encURI("水印照片-2026-08-18.pdf")) {
		t.Fatalf("取件文件名不符: %s", cd)
	}
}

// ---- 失败错误态 ----

func TestPhotosPDFJobFailedBadImage(t *testing.T) {
	env := newTestServer(t, identUser).withTeam() // DisableAsync 同步跑完，结果确定
	env.objects.items["worklog/a/1-1.png"] = []byte("not-an-image")
	env.fake.on("FROM worklog_photo p JOIN worklog_entry e", []string{"entry_id", "cos_key", "url"},
		[][]driver.Value{{int64(1), "worklog/a/1-1.png", ""}})

	rec := env.do(t, "POST", "/api/v1/worklog/photos.pdf/jobs",
		map[string]any{"from": "2026-08-18", "to": "2026-08-18"})
	jobID := pdfJobIDOf(t, decodeEnv(t, rec))

	st := pdfJobStatus(t, env, jobID)
	if st["status"] != pdfJobFailed || st["error"] == "" {
		t.Fatalf("坏图应失败态且带错误，实际 %+v", st)
	}
	rec = env.do(t, "GET", "/api/v1/worklog/photos.pdf?job_id="+jobID, nil)
	if envr := decodeEnv(t, rec); envr.Code != codeInternal {
		t.Fatalf("失败取件应 %d，实际 %d %s", codeInternal, envr.Code, envr.Message)
	}
}

func TestPhotosPDFJobFetchFailNoPhotos(t *testing.T) {
	env := newTestServer(t, identUser).withTeam()
	env.module.Store = failObjects{} // 回源全失败 → 产物为空 → 按无照片 40402 失败
	env.fake.on("FROM worklog_photo p JOIN worklog_entry e", []string{"entry_id", "cos_key", "url"},
		[][]driver.Value{{int64(1), "worklog/a/1-1.png", ""}})

	rec := env.do(t, "POST", "/api/v1/worklog/photos.pdf/jobs",
		map[string]any{"from": "2026-08-18", "to": "2026-08-18"})
	jobID := pdfJobIDOf(t, decodeEnv(t, rec))

	st := pdfJobStatus(t, env, jobID)
	if st["status"] != pdfJobFailed || st["error"] == "" || st["current"] != float64(1) {
		t.Fatalf("回源全失败应失败态，实际 %+v", st)
	}
	rec = env.do(t, "GET", "/api/v1/worklog/photos.pdf?job_id="+jobID, nil)
	if envr := decodeEnv(t, rec); envr.Code != codeNoPhotos {
		t.Fatalf("无产物取件应 %d，实际 %d %s", codeNoPhotos, envr.Code, envr.Message)
	}
}

// ---- 无照片兼容现有 40402 ----

func TestPhotosPDFJobCreateNoPhotos(t *testing.T) {
	env := newTestServer(t, identUser).withTeam()
	env.fake.on("FROM worklog_photo p JOIN worklog_entry e", []string{"entry_id", "cos_key", "url"}, nil)
	rec := env.do(t, "POST", "/api/v1/worklog/photos.pdf/jobs",
		map[string]any{"from": "2026-08-18", "to": "2026-08-18"})
	if envr := decodeEnv(t, rec); envr.Code != codeNoPhotos {
		t.Fatalf("无照片应 %d，实际 %d %s", codeNoPhotos, envr.Code, envr.Message)
	}
}

// ---- 权限（应用权限 + 任务归属）----

func TestPhotosPDFJobPermission(t *testing.T) {
	env := newTestServer(t, identUser).withTeam()
	env.module.Permit = denyPermit{}
	rec := env.do(t, "POST", "/api/v1/worklog/photos.pdf/jobs",
		map[string]any{"from": "2026-08-18", "to": "2026-08-18"})
	if envr := decodeEnv(t, rec); envr.Code != codeAppDenied {
		t.Fatalf("无应用权限应 %d，实际 %d %s", codeAppDenied, envr.Code, envr.Message)
	}

	env2 := newTestServer(t, identUser).withTeam()
	env2.objects.items["worklog/a/1-1.png"] = tinyPNG(t)
	env2.fake.on("FROM worklog_photo p JOIN worklog_entry e", []string{"entry_id", "cos_key", "url"},
		[][]driver.Value{{int64(1), "worklog/a/1-1.png", ""}})
	rec = env2.do(t, "POST", "/api/v1/worklog/photos.pdf/jobs",
		map[string]any{"from": "2026-08-18", "to": "2026-08-18"})
	jobID := pdfJobIDOf(t, decodeEnv(t, rec))

	// 他人（同班组不同用户）查进度与取件均视同不存在
	other := *env2
	other.handler = asUser(env2, auth.Identity{ID: 99, Username: "x", Nickname: "他人", Role: "user", TeamID: 1})
	rec = (&other).do(t, "GET", "/api/v1/worklog/photos.pdf/jobs/"+jobID, nil)
	if envr := decodeEnv(t, rec); envr.Code != codeMissing {
		t.Fatalf("他人查进度应 %d，实际 %d %s", codeMissing, envr.Code, envr.Message)
	}
	rec = (&other).do(t, "GET", "/api/v1/worklog/photos.pdf?job_id="+jobID, nil)
	if envr := decodeEnv(t, rec); envr.Code != codeMissing {
		t.Fatalf("他人取件应 %d，实际 %d %s", codeMissing, envr.Code, envr.Message)
	}
}

// ---- 原 GET from/to 同步口径兼容 ----

func TestPhotosPDFSyncCompat(t *testing.T) {
	env := newTestServer(t, identUser).withTeam()
	env.objects.items["worklog/a/1-1.png"] = tinyPNG(t)
	env.fake.on("FROM worklog_photo p JOIN worklog_entry e", []string{"entry_id", "cos_key", "url"},
		[][]driver.Value{{int64(1), "worklog/a/1-1.png", ""}})
	rec := env.do(t, "GET", "/api/v1/worklog/photos.pdf?from=2026-08-18&to=2026-08-18", nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Header().Get("Content-Type"), "application/pdf") {
		t.Fatalf("同步 GET from/to 应仍出 PDF，实际 %d %s", rec.Code, rec.Header().Get("Content-Type"))
	}
	if rec.Body.Len() == 0 {
		t.Fatal("同步 GET 应有 PDF 内容")
	}
}
