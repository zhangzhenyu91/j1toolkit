package worklog

// Task 6 资源边界回归：组合 PDF 只选水印照片与日期/张数/累计字节/任务并发预算；
// ZIP 空 COS 前缀 fail-closed；Dify 识别总时间预算；对象回源字节预算。

import (
	"context"
	"database/sql/driver"
	"errors"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestDispatchUploadBudget 派车单 multipart 在读取内容前按元数据限文件数/单文件大小。
func TestDispatchUploadBudget(t *testing.T) {
	ok := []*multipart.FileHeader{{Filename: "a.xlsx", Size: dispatchMaxFileBytes}}
	if err := checkDispatchUploadBudget(ok); err != nil {
		t.Fatalf("预算内文件不应拒绝: %v", err)
	}
	if err := checkDispatchUploadBudget([]*multipart.FileHeader{{Filename: "big.xlsx", Size: dispatchMaxFileBytes + 1}}); err == nil {
		t.Fatal("单文件超限应拒绝")
	}
	many := make([]*multipart.FileHeader, 0, dispatchMaxFiles+1)
	for i := 0; i <= dispatchMaxFiles; i++ {
		many = append(many, &multipart.FileHeader{Filename: "a.xlsx", Size: 1})
	}
	if err := checkDispatchUploadBudget(many); !errors.Is(err, errDispatchTooManyFiles) {
		t.Fatalf("文件数超限应 errDispatchTooManyFiles，实际 %v", err)
	}
}

// TestPhotosPDFOnlyWatermarkPhotos 组合 PDF 查询必须只选水印照片。
func TestPhotosPDFOnlyWatermarkPhotos(t *testing.T) {
	env := newTestServer(t, identUser).withTeam()
	env.fake.on("FROM worklog_photo p JOIN worklog_entry e", []string{"entry_id", "cos_key", "url"}, nil)
	env.do(t, "POST", "/api/v1/worklog/photos.pdf/jobs",
		map[string]any{"from": "2026-08-18", "to": "2026-08-18"})
	q, ok := env.fake.lastQuery("FROM worklog_photo p JOIN worklog_entry e")
	if !ok {
		t.Fatal("未发起照片查询")
	}
	if !strings.Contains(q.query, "p.is_watermark = 1") {
		t.Fatalf("组合 PDF 未过滤水印照片：%s", q.query)
	}

	// 批量下载清单恢复「全部照片」口径：不得过滤水印（PDF 才过滤）。
	env.do(t, "GET", "/api/v1/worklog/photos?from=2026-08-18&to=2026-08-18", nil)
	q, ok = env.fake.lastQuery("FROM worklog_photo p JOIN worklog_entry e")
	if !ok {
		t.Fatal("未发起照片清单查询")
	}
	if strings.Contains(q.query, "is_watermark") {
		t.Fatalf("照片清单应包含全部照片（不得过滤水印）：%s", q.query)
	}
}

// TestPhotosPDFRejectsTooLongRange 日期跨度超预算在解析照片前拒绝。
func TestPhotosPDFRejectsTooLongRange(t *testing.T) {
	env := newTestServer(t, identUser).withTeam()
	cases := []struct {
		method, path string
		body         any
	}{
		{"GET", "/api/v1/worklog/photos.pdf?from=2026-01-01&to=2026-06-30", nil},
		{"POST", "/api/v1/worklog/photos.pdf/jobs", map[string]any{"from": "2026-01-01", "to": "2026-06-30"}},
		{"POST", "/api/v1/worklog/photos-pdf/save-netdisk", map[string]any{"from": "2026-01-01", "to": "2026-06-30"}},
	}
	for _, c := range cases {
		rec := env.do(t, c.method, c.path, c.body)
		envr := decodeEnv(t, rec)
		if envr.Code != codeDateRange || rec.Code != http.StatusBadRequest {
			t.Fatalf("%s 超长日期应 %d/%d，实际 %d/%d %s",
				c.path, http.StatusBadRequest, codeDateRange, rec.Code, envr.Code, envr.Message)
		}
	}
}

// TestPhotosPDFRejectsTooManyPhotos 张数超预算拒绝（同步与后台任务同口径）。
func TestPhotosPDFRejectsTooManyPhotos(t *testing.T) {
	env := newTestServer(t, identUser).withTeam()
	env.module.PDFMaxPhotos = 2
	rows := [][]driver.Value{
		{int64(1), "worklog/a/1.jpg", ""},
		{int64(2), "worklog/a/2.jpg", ""},
		{int64(3), "worklog/a/3.jpg", ""},
	}
	env.fake.on("FROM worklog_photo p JOIN worklog_entry e", []string{"entry_id", "cos_key", "url"}, rows)

	rec := env.do(t, "POST", "/api/v1/worklog/photos.pdf/jobs",
		map[string]any{"from": "2026-08-18", "to": "2026-08-18"})
	envr := decodeEnv(t, rec)
	if rec.Code != http.StatusBadRequest || envr.Code != codeFileCount {
		t.Fatalf("张数超限应 400/%d，实际 %d/%d %s", codeFileCount, rec.Code, envr.Code, envr.Message)
	}
	if !strings.Contains(envr.Message, "上限") {
		t.Fatalf("提示应说明上限：%q", envr.Message)
	}
}

// TestPhotosPDFCumulativeByteBudget 回源累计字节超预算显式失败（不静默丢图/继续拼装）。
func TestPhotosPDFCumulativeByteBudget(t *testing.T) {
	env := newTestServer(t, identUser).withTeam()
	env.module.PDFMaxBytes = 10
	env.objects.items["a1"] = []byte("123456")
	env.objects.items["a2"] = []byte("123456")
	groups := []photoGroup{{entryID: 1, refs: []photoRef{{cosKey: "a1"}, {cosKey: "a2"}}}}
	_, _, err := env.module.buildPhotosPDFFromGroups(context.Background(), groups, nil)
	if !errors.Is(err, errPDFBudgetExceeded) {
		t.Fatalf("累计字节超限应显式报错，实际 %v", err)
	}
}

// TestPhotosPDFJobConcurrencyBudget 全局并发预算占满时立即拒绝（不再无限排队等待）。
func TestPhotosPDFJobConcurrencyBudget(t *testing.T) {
	env := newTestServer(t, identUser).withTeam()
	env.module.DisableAsync = false
	env.fake.on("FROM worklog_photo p JOIN worklog_entry e", []string{"entry_id", "cos_key", "url"},
		[][]driver.Value{{int64(1), "worklog/a/1-1.png", ""}})
	env.objects.items["worklog/a/1-1.png"] = tinyPNG(t)
	fetched := make(chan struct{}, 4)
	env.module.Store = &hookObjects{memObjects: env.objects, onFetch: func(string) { fetched <- struct{}{} }}

	// 占满全局任务预算。
	env.module.ensurePDFJobSem()
	env.module.pdfJobSem <- struct{}{}
	env.module.pdfJobSem <- struct{}{}

	rec := env.do(t, "POST", "/api/v1/worklog/photos.pdf/jobs",
		map[string]any{"from": "2026-08-18", "to": "2026-08-18"})
	envr := decodeEnv(t, rec)
	if rec.Code != http.StatusConflict || envr.Code != codeOpRunning {
		t.Fatalf("并发占满应 409/%d，实际 %d/%d %s", codeOpRunning, rec.Code, envr.Code, envr.Message)
	}
	select {
	case <-fetched:
		t.Fatal("并发预算占满时不应开始回源")
	case <-time.After(100 * time.Millisecond):
	}
}

// prefixlessObjects COS 未配置（PublicURL("") 为空）的对象存储桩。
type prefixlessObjects struct{ *memObjects }

func (p prefixlessObjects) PublicURL(string) string { return "" }

// TestZipEmptyCOSPrefixFailsClosed 空 COS 前缀时任何地址都不得被当作「本仓地址」回源。
func TestZipEmptyCOSPrefixFailsClosed(t *testing.T) {
	env := newTestServer(t, identUser).withTeam()
	env.module.Store = prefixlessObjects{memObjects: env.objects}
	rec := env.do(t, "POST", "/api/v1/worklog/zip", map[string]any{
		"photos": []map[string]any{{"url": "https://evil.example/x.jpg", "name": "a.jpg"}},
	})
	envr := decodeEnv(t, rec)
	if rec.Code == http.StatusOK || envr.Code == codeOK {
		t.Fatalf("空 COS 前缀必须 fail-closed，实际 %d/%d", rec.Code, envr.Code)
	}
	if envr.Code != codeNotConfigured {
		t.Fatalf("空 COS 前缀应报未配置，实际 %d %s", envr.Code, envr.Message)
	}
}

// advancingDify 每次调用推进注入时钟的 Dify 桩（识别结果始终不可用）。
type advancingDify struct {
	now   *time.Time
	calls int
}

func (d *advancingDify) VerifyPhoto(context.Context, string, string, string, string) VerifyResult {
	d.calls++
	*d.now = d.now.Add(time.Minute)
	return VerifyResult{}
}

// TestRecognizeTotalTimeBudget 识别业务重试必须有总时间预算，不能 4 轮全额叠加。
func TestRecognizeTotalTimeBudget(t *testing.T) {
	oldNow, oldBudget := verifyNow, verifyBudget
	defer func() { verifyNow, verifyBudget = oldNow, oldBudget }()
	now := time.Date(2026, 10, 31, 10, 0, 0, 0, time.UTC)
	verifyNow = func() time.Time { return now }
	verifyBudget = 2 * time.Minute
	client := &advancingDify{now: &now}
	recognize(context.Background(), client, "u", "2026.10.31", "d", "https://cos/x.jpg")
	if client.calls < 1 || client.calls >= maxVerifyRetry+1 {
		t.Fatalf("总时间预算应提前停止业务重试，实际 %d 次（上限 %d）", client.calls, maxVerifyRetry+1)
	}
}

// TestFetchObjectBytesByteBudget URL 回源兜底同样有字节预算，超限不得当完整内容。
func TestFetchObjectBytesByteBudget(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("0123456789"))
	}))
	defer srv.Close()
	old := objectFetchMaxBytes
	objectFetchMaxBytes = 8
	defer func() { objectFetchMaxBytes = old }()
	buf, err := fetchObjectBytes(context.Background(), nil, "", srv.URL)
	if err == nil {
		t.Fatalf("超字节预算应报错，实际返回 %q", buf)
	}
	if len(buf) != 0 {
		t.Fatalf("超限不得返回截断内容：%q", buf)
	}
}
