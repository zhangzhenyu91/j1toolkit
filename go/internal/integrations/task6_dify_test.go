package integrations

// Task 6 Dify 流终态与重试预算回归：
// 1) SSE 流在收到 workflow_finished 前结束必须判失败（不能把截断流当成功）；
// 2) 触发阶段有总超时（响应头迟迟不来时 Start 必须快速失败）；
// 3) 照片识别适配不得在业务 4 轮重试之上再叠传输层 3 次（避免 4×3=12）。

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestDifyStreamWithoutFinishFails 流结束但无 workflow_finished：OnFailed 必须回调明确错误。
func TestDifyStreamWithoutFinishFails(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"event\": \"workflow_started\", \"data\": {\"id\": \"1\"}}\n\n"))
		// 流在此结束，没有 workflow_finished。
	}))
	defer srv.Close()

	failed := make(chan error, 1)
	c := difyTestClient(srv.URL)
	if err := c.Start(context.Background(), StreamInput{
		Inputs:   map[string]any{"url": "u"},
		OnFailed: func(e error) { failed <- e },
	}); err != nil {
		t.Fatalf("Start = %v", err)
	}
	select {
	case e := <-failed:
		if !strings.Contains(e.Error(), "完成事件") {
			t.Fatalf("OnFailed = %v，期望提示缺少完成事件", e)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("流未完成却未回调 OnFailed")
	}
}

// TestDifyStartTriggerTimeout 触发阶段总超时：服务端迟迟不回响应头时 Start 快速失败。
func TestDifyStartTriggerTimeout(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
		_, _ = w.Write([]byte("data: {\"event\": \"workflow_finished\", \"data\": {\"status\": \"succeeded\"}}\n\n"))
	}))
	defer srv.Close()
	defer close(release)

	c := difyTestClient(srv.URL)
	c.TriggerTimeout = 40 * time.Millisecond
	start := time.Now()
	err := c.Start(context.Background(), StreamInput{Inputs: map[string]any{"url": "u"}})
	if err == nil {
		t.Fatal("触发超时应报错")
	}
	if elapsed := time.Since(start); elapsed > 500*time.Millisecond {
		t.Fatalf("触发超时未生效，耗时 %s", elapsed)
	}
	if !strings.Contains(err.Error(), "超时") {
		t.Fatalf("错误应说明超时：%v", err)
	}
}

// TestPhotoVerifierSingleTransportAttempt 照片识别接线后传输层只尝试 1 次（业务重试在 worklog 侧）。
func TestPhotoVerifierSingleTransportAttempt(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("boom"))
	}))
	defer srv.Close()

	client := &WorkflowClient{
		Cfg:       NewMapProvider(map[string]string{"DIFY_API_URL": srv.URL}),
		APIKeyKey: "DIFY_WORKLOG_API_KEY",
		APIKey:    "k",
	}
	verifier := NewPhotoVerifier(client)
	res := verifier.VerifyPhoto(context.Background(), "u", "2026.10.27", "d", "https://cos/x.jpg")
	if res.OK {
		t.Fatal("上游 500 应归识别失败")
	}
	if calls != 1 {
		t.Fatalf("照片识别传输层调用 %d 次，期望 1 次（业务 4 轮之外不得再乘 3）", calls)
	}
}

// TestDifyHealthyStreamNotCanceledAfterHeaders 响应头已到（定时器应停止），慢体不得被触发超时误取消。
func TestDifyHealthyStreamNotCanceledAfterHeaders(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		time.Sleep(80 * time.Millisecond)
		_, _ = w.Write([]byte("data: {\"event\": \"workflow_finished\", \"data\": {\"status\": \"succeeded\"}}\n\n"))
	}))
	defer srv.Close()
	failed := make(chan error, 1)
	c := difyTestClient(srv.URL)
	c.TriggerTimeout = 30 * time.Millisecond
	if err := c.Start(context.Background(), StreamInput{
		Inputs:   map[string]any{"url": "u"},
		OnFailed: func(e error) { failed <- e },
	}); err != nil {
		t.Fatalf("响应头已到达，Start 应成功: %v", err)
	}
	select {
	case e := <-failed:
		t.Fatalf("健康流不得被超时误取消：%v", e)
	case <-time.After(300 * time.Millisecond):
	}
}
