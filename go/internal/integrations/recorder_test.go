package integrations

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeRecorder 事件采集桩。
type fakeRecorder struct {
	mu     sync.Mutex
	events []recEvent
}

type recEvent struct {
	level, category, event, messageCN, raw string
	fields                                 map[string]any
}

func (f *fakeRecorder) Record(_ context.Context, level, category, event, messageCN, raw string, fields map[string]any) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.events = append(f.events, recEvent{level: level, category: category, event: event, messageCN: messageCN, raw: raw, fields: fields})
	return nil
}

// snapshot 返回事件快照。
func (f *fakeRecorder) snapshot() []recEvent {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]recEvent(nil), f.events...)
}

// waitEvents 等待事件数达到 n（异步 SSE 场景用）。
func (f *fakeRecorder) waitEvents(t *testing.T, n int) []recEvent {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		got := f.snapshot()
		if len(got) >= n {
			return got
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("事件数 = %d，期望 ≥ %d", len(f.snapshot()), n)
	return nil
}

// find 返回指定事件名的事件。
func find(events []recEvent, name string) (recEvent, bool) {
	for _, e := range events {
		if e.event == name {
			return e, true
		}
	}
	return recEvent{}, false
}

// ---- 分类与脱敏 ----

func TestErrorClass(t *testing.T) {
	cases := []struct {
		err  error
		want string
	}{
		{nil, ""},
		{ErrNotConfigured, classNotConfigured},
		{ErrCOSNotConfigured, classNotConfigured},
		{context.Canceled, classCanceled},
		{context.DeadlineExceeded, classTimeout},
		{&WorkflowError{Status: "failed", Message: "x"}, classWorkflowFailed},
		{&HTTPError{StatusCode: 500, Body: "b"}, classHTTPStatus},
		{&statusError{Op: "上传对象", StatusCode: 403}, classHTTPStatus},
		{ErrBadEnvelope, classBadResponse},
		{&json.SyntaxError{}, classBadResponse},
		{&urlErr{timeout: true}, classTimeout},
		{&urlErr{}, classNetwork},
		{errors.New("boom"), classUnknown},
		{fmt.Errorf("ctx: %w", &WorkflowError{Status: "stopped"}), classWorkflowFailed},
	}
	for _, c := range cases {
		if got := errorClass(c.err); got != c.want {
			t.Fatalf("errorClass(%v) = %q，期望 %q", c.err, got, c.want)
		}
	}
}

func TestErrorClassCN(t *testing.T) {
	if errorClassCN(classTimeout) != "调用超时" || errorClassCN(classNotConfigured) != "未配置" {
		t.Fatal("分类中文说明不符")
	}
	if errorClassCN("no-such-class") != "未知异常" {
		t.Fatal("未知分类应给未知异常")
	}
}

func TestScrubSecrets(t *testing.T) {
	cases := []struct{ in, want string }{
		{"https://x/y?key=abc123&keyword=hello&token=t1",
			"https://x/y?key=****&keyword=hello&token=****"},
		{"Authorization: Bearer eyJhbGciOi.abc", "Authorization: Bearer ****"},
		{`{"password":"p@ss","ok":1}`, `{"password":"****","ok":1}`},
		{"no secrets here", "no secrets here"},
		{"", ""},
	}
	for _, c := range cases {
		if got := scrubSecrets(c.in); got != c.want {
			t.Fatalf("scrubSecrets(%q) = %q，期望 %q", c.in, got, c.want)
		}
	}
}

func TestScrubFields(t *testing.T) {
	out := scrubFields(map[string]any{
		"key_name": "DIFY_WORKLOG_API_KEY",
		"path":     "worklog/a.jpg",
		"key":      "sekrit",
		"token":    "tok",
		"error":    "Get https://a?secret=s1 failed",
		"n":        3,
	})
	if out["key"] != "****" || out["token"] != "****" {
		t.Fatalf("敏感字段未脱敏：%v", out)
	}
	if out["key_name"] != "DIFY_WORKLOG_API_KEY" || out["path"] != "worklog/a.jpg" {
		t.Fatalf("非密钥字段被误伤：%v", out)
	}
	if got := out["error"]; got != "Get https://a?secret=**** failed" {
		t.Fatalf("字符串值未脱敏：%v", got)
	}
	if out["n"] != 3 {
		t.Fatalf("数值字段被改动：%v", out)
	}
}

func TestRecordNilNoopAndSwallows(t *testing.T) {
	record(nil, context.Background(), "info", "e", "中", "raw", nil) // nil 静默
	panicky := panicRecorder{}
	record(panicky, context.Background(), "info", "e", "中", "raw", nil) // panic 不外溢
}

// panicRecorder Record 必 panic 的桩。
type panicRecorder struct{}

func (panicRecorder) Record(context.Context, string, string, string, string, string, map[string]any) error {
	panic("record boom")
}

// ---- Dify 事件 ----

func TestWorkflowRunRecordsEvents(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"workflow_run_id":"wr-1","task_id":"t-1","data":{"status":"succeeded","outputs":{"title":"x"}}}`))
	}))
	defer srv.Close()

	rec := &fakeRecorder{}
	c := difyTestClient(srv.URL)
	c.Recorder = rec
	if _, err := c.Run(context.Background(), RunRequest{User: "tester"}); err != nil {
		t.Fatalf("Run = %v", err)
	}
	events := rec.snapshot()
	if len(events) != 1 {
		t.Fatalf("事件 %d 条", len(events))
	}
	e := events[0]
	if e.level != "info" || e.category != externalCategory || e.event != "dify.workflow.run" {
		t.Fatalf("事件形态不符：%+v", e)
	}
	if !strings.Contains(e.messageCN, "Dify 工作流调用成功") {
		t.Fatalf("messageCN = %q", e.messageCN)
	}
	if strings.Contains(e.raw, "test-key") {
		t.Fatalf("API Key 泄漏：%q", e.raw)
	}
	if e.fields["workflow_run_id"] != "wr-1" {
		t.Fatalf("fields = %v", e.fields)
	}
}

func TestWorkflowRunFailedClassified(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("boom key=sekrit"))
	}))
	defer srv.Close()

	rec := &fakeRecorder{}
	c := difyTestClient(srv.URL)
	c.Recorder = rec
	if _, err := c.Run(context.Background(), RunRequest{User: "tester"}); err == nil {
		t.Fatal("期望失败")
	}
	events := rec.snapshot()
	if len(events) != 1 {
		t.Fatalf("事件 %d 条", len(events))
	}
	e := events[0]
	if e.event != "dify.workflow.run.failed" || e.level != "error" {
		t.Fatalf("事件形态不符：%+v", e)
	}
	if e.fields["error_class"] != classHTTPStatus {
		t.Fatalf("error_class = %v", e.fields["error_class"])
	}
	if strings.Contains(e.raw, "test-key") {
		t.Fatalf("API Key 泄漏：%q", e.raw)
	}
	if strings.Contains(e.raw, "sekrit") {
		t.Fatalf("响应体密文未脱敏：%q", e.raw)
	}
}

func TestWorkflowRunNotConfiguredWarn(t *testing.T) {
	rec := &fakeRecorder{}
	c := &WorkflowClient{Cfg: NewMapProvider(nil), Recorder: rec}
	if _, err := c.Run(context.Background(), RunRequest{}); err == nil {
		t.Fatal("期望未配置错误")
	}
	e := rec.snapshot()[0]
	if e.level != "warn" || e.fields["error_class"] != classNotConfigured {
		t.Fatalf("事件形态不符：%+v", e)
	}
}

func TestWorkflowStartStreamEvents(t *testing.T) {
	// 失败流：触发 info + 流失败 error
	failed := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"event\": \"workflow_finished\", \"data\": {\"status\": \"failed\", \"error\": \"文档解析失败\"}}\n\n"))
		close(failed)
	}))
	defer srv.Close()

	rec := &fakeRecorder{}
	c := difyTestClient(srv.URL)
	c.Recorder = rec
	if err := c.Start(context.Background(), StreamInput{Inputs: map[string]any{"url": "u"}}); err != nil {
		t.Fatalf("Start = %v", err)
	}
	<-failed
	events := rec.waitEvents(t, 2)
	if _, ok := find(events, "dify.workflow.start"); !ok {
		t.Fatalf("缺触发事件：%+v", events)
	}
	streamFail, ok := find(events, "dify.workflow.stream.failed")
	if !ok {
		t.Fatalf("缺流失败事件：%+v", events)
	}
	if streamFail.level != "error" || streamFail.fields["error_class"] != classWorkflowFailed {
		t.Fatalf("流失败事件形态不符：%+v", streamFail)
	}

	// 成功流：触发 info + 流完成 info
	rec2 := &fakeRecorder{}
	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"event\": \"workflow_finished\", \"data\": {\"status\": \"succeeded\"}}\n\n"))
	}))
	defer srv2.Close()
	c2 := difyTestClient(srv2.URL)
	c2.Recorder = rec2
	if err := c2.Start(context.Background(), StreamInput{Inputs: map[string]any{"url": "u"}}); err != nil {
		t.Fatalf("Start = %v", err)
	}
	events2 := rec2.waitEvents(t, 2)
	fin, ok := find(events2, "dify.workflow.stream.finished")
	if !ok {
		t.Fatalf("缺流完成事件：%+v", events2)
	}
	if fin.level != "info" {
		t.Fatalf("流完成应为 info：%+v", fin)
	}
}

func TestWorkflowStartTriggerFailed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer srv.Close()
	rec := &fakeRecorder{}
	c := difyTestClient(srv.URL)
	c.Recorder = rec
	if err := c.Start(context.Background(), StreamInput{Inputs: map[string]any{}}); err == nil {
		t.Fatal("期望触发失败")
	}
	e := rec.snapshot()[0]
	if e.event != "dify.workflow.start.failed" || e.fields["error_class"] != classHTTPStatus {
		t.Fatalf("事件形态不符：%+v", e)
	}
}

// ---- COS 事件 ----

func TestCOSRecordsEvents(t *testing.T) {
	status := http.StatusOK
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(status)
	}))
	defer srv.Close()

	rec := &fakeRecorder{}
	store := cosTestStore(t, srv.URL)
	store.Recorder = rec
	if err := store.PutBuffer(context.Background(), "worklog/a.jpg", []byte("x"), "image/jpeg"); err != nil {
		t.Fatalf("PutBuffer = %v", err)
	}
	if err := store.DeleteObject(context.Background(), "worklog/a.jpg"); err != nil {
		t.Fatalf("DeleteObject = %v", err)
	}
	events := rec.snapshot()
	if len(events) != 2 {
		t.Fatalf("事件 %d 条：%+v", len(events), events)
	}
	if events[0].event != "cos.put" || events[0].level != "info" || events[0].fields["path"] != "worklog/a.jpg" {
		t.Fatalf("上传事件不符：%+v", events[0])
	}
	if events[1].event != "cos.delete" {
		t.Fatalf("删除事件不符：%+v", events[1])
	}
	for _, e := range events {
		if strings.Contains(e.raw, "SecretKeyTEST") || strings.Contains(e.raw, "AKIDTEST") {
			t.Fatalf("COS 密钥泄漏：%q", e.raw)
		}
	}

	// 失败：500 → 分类 http_status
	rec2 := &fakeRecorder{}
	store2 := cosTestStore(t, srv.URL)
	store2.Recorder = rec2
	status = http.StatusInternalServerError
	if err := store2.PutBuffer(context.Background(), "worklog/b.jpg", []byte("x"), "image/jpeg"); err == nil {
		t.Fatal("期望上传失败")
	}
	fe := rec2.snapshot()[0]
	if fe.event != "cos.put.failed" || fe.level != "error" || fe.fields["error_class"] != classHTTPStatus {
		t.Fatalf("失败事件不符：%+v", fe)
	}
}

// ---- AMap 事件 ----

func TestAMapRecordsEvents(t *testing.T) {
	// 成功
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v3/geocode/regeo":
			_, _ = w.Write([]byte(`{"status": "1", "regeocode": {
				"addressComponent": {"district": "尧都区", "township": "段店乡", "adcode": "141002"}}}`))
		case "/v3/weather/weatherInfo":
			_, _ = w.Write([]byte(`{"status": "1", "lives": [{"weather": "多云", "temperature": "31.6"}]}`))
		}
	}))
	defer srv.Close()
	rec := &fakeRecorder{}
	svc := amapTestService(srv.URL)
	svc.Recorder = rec
	got := svc.FetchLocationWeather(context.Background(), "116.48", "39.98")
	if got.Location == "" {
		t.Fatal("期望有地点")
	}
	e := rec.snapshot()[0]
	if e.event != "amap.location_weather" || e.level != "info" || e.category != externalCategory {
		t.Fatalf("成功事件不符：%+v", e)
	}
	if strings.Contains(e.raw, "test-amap-key") || strings.Contains(e.messageCN, "test-amap-key") {
		t.Fatalf("高德 key 泄漏：%q / %q", e.raw, e.messageCN)
	}

	// 业务失败（status != 1）→ warn + api_error
	rec2 := &fakeRecorder{}
	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"status": "0", "info": "INVALID_USER_KEY"}`))
	}))
	defer srv2.Close()
	svc2 := amapTestService(srv2.URL)
	svc2.Recorder = rec2
	if got := svc2.FetchLocationWeather(context.Background(), "116.48", "39.98"); got.Location != "" || got.Weather != "" {
		t.Fatalf("失败应返回空值：%+v", got)
	}
	fe := rec2.snapshot()[0]
	if fe.event != "amap.location_weather.failed" || fe.level != "warn" || fe.fields["error_class"] != classAPIError {
		t.Fatalf("失败事件不符：%+v", fe)
	}

	// 未配置 → warn + not_configured
	rec3 := &fakeRecorder{}
	svc3 := &AMapService{Cfg: NewMapProvider(nil), Recorder: rec3}
	svc3.FetchLocationWeather(context.Background(), "116.48", "39.98")
	ne := rec3.snapshot()[0]
	if ne.fields["error_class"] != classNotConfigured || ne.level != "warn" {
		t.Fatalf("未配置事件不符：%+v", ne)
	}
}

// ---- 分类辅助测试替身 ----

// urlErr 网络错误替身（实现 net.Error）。
type urlErr struct{ timeout bool }

func (e *urlErr) Error() string   { return "dial tcp: i/o timeout" }
func (e *urlErr) Timeout() bool   { return e.timeout }
func (e *urlErr) Temporary() bool { return true }
