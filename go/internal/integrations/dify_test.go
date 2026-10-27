package integrations

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// difyTestClient 构造指向假服务的客户端（快速重试）。
func difyTestClient(baseURL string) *WorkflowClient {
	return &WorkflowClient{
		Cfg:       NewMapProvider(map[string]string{"DIFY_API_URL": baseURL}),
		APIKeyKey: "DIFY_TEST_API_KEY",
		APIKey:    "test-key",
		Timeout:   2 * time.Second,
		Retry: RetryPolicy{
			MaxAttempts: 3,
			BaseWait:    time.Millisecond,
			MaxWait:     time.Millisecond,
			Sleep:       func(context.Context, time.Duration) error { return nil },
		},
	}
}

// TestDifyRunRequestShape blocking 请求形态与 outputs 解析。
func TestDifyRunRequestShape(t *testing.T) {
	var gotPath, gotAuth, gotCT string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		gotCT = r.Header.Get("Content-Type")
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &gotBody)
		_, _ = w.Write([]byte(`{
			"workflow_run_id": "wr-1", "task_id": "task-1",
			"data": {"id": "d-1", "status": "succeeded",
				"outputs": {"title": "杆塔巡检", "lng": 116.480881, "empty": null},
				"elapsed_time": 1.5}
		}`))
	}))
	defer srv.Close()

	c := difyTestClient(srv.URL)
	res, err := c.Run(context.Background(), RunRequest{
		Inputs: map[string]any{"date": "2026.10.27", "picture": map[string]any{"transfer_method": "remote_url"}},
		User:   "tester",
	})
	if err != nil {
		t.Fatalf("Run = %v", err)
	}
	if gotPath != "/v1/workflows/run" {
		t.Fatalf("path = %s", gotPath)
	}
	if gotAuth != "Bearer test-key" || gotCT != "application/json" {
		t.Fatalf("headers = %q / %q", gotAuth, gotCT)
	}
	if gotBody["response_mode"] != "blocking" || gotBody["user"] != "tester" {
		t.Fatalf("body = %v", gotBody)
	}
	inputs, _ := gotBody["inputs"].(map[string]any)
	if inputs["date"] != "2026.10.27" {
		t.Fatalf("inputs = %v", inputs)
	}
	if res.WorkflowRunID != "wr-1" || res.TaskID != "task-1" || res.Status != "succeeded" {
		t.Fatalf("result = %+v", res)
	}
	if res.Outputs.String("title") != "杆塔巡检" || res.Outputs.String("empty") != "" ||
		res.Outputs.String("missing") != "" {
		t.Fatalf("outputs 解析 = %v", res.Outputs)
	}
}

// TestDifyRunURLJoin 基地址拼接口径：带/不带 /v1、尾斜杠。
func TestDifyRunURLJoin(t *testing.T) {
	cases := map[string]string{
		"https://dify.example.com":     "https://dify.example.com/v1/workflows/run",
		"https://dify.example.com/v1":  "https://dify.example.com/v1/workflows/run",
		"https://dify.example.com/v1/": "https://dify.example.com/v1/workflows/run",
		"https://dify.example.com/":    "https://dify.example.com/v1/workflows/run",
	}
	for base, want := range cases {
		if got := runURL(base); got != want {
			t.Fatalf("runURL(%q) = %s，期望 %s", base, got, want)
		}
	}
}

// TestDifyRunWorkflowFailed 工作流业务失败：WorkflowError，不重试。
func TestDifyRunWorkflowFailed(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		_, _ = w.Write([]byte(`{"data": {"status": "failed", "error": "视觉模型超时"}}`))
	}))
	defer srv.Close()
	c := difyTestClient(srv.URL)
	_, err := c.Run(context.Background(), RunRequest{})
	var we *WorkflowError
	if !errors.As(err, &we) || we.Message != "视觉模型超时" {
		t.Fatalf("Run = %v，期望 WorkflowError", err)
	}
	if calls != 1 {
		t.Fatalf("调用次数 = %d，期望 1（业务失败不重试）", calls)
	}
}

// TestDifyRunHTTPError 4xx 单次、5xx 有界重试至成功。
func TestDifyRunHTTPError(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"code": "server_error"}`))
			return
		}
		if calls == 2 {
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		_, _ = w.Write([]byte(`{"data": {"status": "succeeded", "outputs": {"ok": "1"}}}`))
	}))
	defer srv.Close()
	c := difyTestClient(srv.URL)
	res, err := c.Run(context.Background(), RunRequest{})
	if err != nil {
		t.Fatalf("Run = %v", err)
	}
	if calls != 3 {
		t.Fatalf("调用次数 = %d，期望 3（5xx/429 各重试一次）", calls)
	}
	if res.Outputs.String("ok") != "1" {
		t.Fatalf("result = %+v", res)
	}

	calls = 0
	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"code": "invalid_param", "message": "bad"}`))
	}))
	defer srv2.Close()
	_, err = difyTestClient(srv2.URL).Run(context.Background(), RunRequest{})
	var he *HTTPError
	if !errors.As(err, &he) || he.StatusCode != http.StatusBadRequest {
		t.Fatalf("Run = %v，期望 HTTPError 400", err)
	}
	if calls != 1 {
		t.Fatalf("4xx 调用次数 = %d，期望 1", calls)
	}
}

// TestDifyRunTimeout 单次尝试超时：可重试且错误可识别为超时。
func TestDifyRunTimeout(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		time.Sleep(300 * time.Millisecond)
		_, _ = w.Write([]byte(`{"data": {"status": "succeeded", "outputs": {}}}`))
	}))
	defer srv.Close()
	c := difyTestClient(srv.URL)
	c.Timeout = 50 * time.Millisecond
	c.Retry.MaxAttempts = 2
	_, err := c.Run(context.Background(), RunRequest{})
	if err == nil {
		t.Fatal("Run 应超时报错")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("错误 = %v，期望可识别为超时", err)
	}
	if calls != 2 {
		t.Fatalf("调用次数 = %d，期望 2（重试至上限）", calls)
	}
}

// TestDifyNotConfigured 缺配置不发请求。
func TestDifyNotConfigured(t *testing.T) {
	c := &WorkflowClient{Cfg: NewMapProvider(nil)}
	if _, err := c.Run(context.Background(), RunRequest{}); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("Run = %v，期望 ErrNotConfigured", err)
	}
	if err := c.Start(context.Background(), StreamInput{}); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("Start = %v，期望 ErrNotConfigured", err)
	}
}

// TestDifyStartStreaming streaming 触发：请求形态 + SSE 失败事件回调 OnFailed。
func TestDifyStartStreaming(t *testing.T) {
	failed := make(chan error, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)
		if body["response_mode"] != "streaming" {
			t.Errorf("response_mode = %v", body["response_mode"])
		}
		if u, _ := body["user"].(string); u != "safeday-web" {
			t.Errorf("user = %v", body["user"])
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"event\": \"workflow_started\", \"data\": {\"id\": \"1\"}}\n\n"))
		_, _ = w.Write([]byte("data: {\"event\": \"workflow_finished\", \"data\": {\"status\": \"failed\", \"error\": \"文档解析失败\"}}\n\n"))
	}))
	defer srv.Close()

	c := difyTestClient(srv.URL)
	err := c.Start(context.Background(), StreamInput{
		Inputs:   map[string]any{"url": "https://x/f.pdf", "date": "2026.10.27", "class": "检修一班"},
		User:     "safeday-web",
		OnFailed: func(e error) { failed <- e },
	})
	if err != nil {
		t.Fatalf("Start = %v", err)
	}
	select {
	case e := <-failed:
		if !strings.Contains(e.Error(), "文档解析失败") {
			t.Fatalf("OnFailed = %v", e)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("OnFailed 未回调")
	}
}

// TestDifyStartStreamingOK 工作流成功：不回调 OnFailed。
func TestDifyStartStreamingOK(t *testing.T) {
	failed := make(chan error, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"event\": \"workflow_finished\", \"data\": {\"status\": \"succeeded\"}}\n\n"))
	}))
	defer srv.Close()
	c := difyTestClient(srv.URL)
	if err := c.Start(context.Background(), StreamInput{
		Inputs:   map[string]any{"url": "u"},
		OnFailed: func(e error) { failed <- e },
	}); err != nil {
		t.Fatalf("Start = %v", err)
	}
	select {
	case e := <-failed:
		t.Fatalf("成功不应回调 OnFailed：%v", e)
	case <-time.After(300 * time.Millisecond):
	}
}

// TestDifyStartTriggerError 触发失败（非 2xx）：立即返回错误，不消费流。
func TestDifyStartTriggerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("bad"))
	}))
	defer srv.Close()
	c := difyTestClient(srv.URL)
	err := c.Start(context.Background(), StreamInput{Inputs: map[string]any{}})
	if err == nil || !strings.Contains(err.Error(), "HTTP 500") {
		t.Fatalf("Start = %v，期望触发失败文案", err)
	}
}
