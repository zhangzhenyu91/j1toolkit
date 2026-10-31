package safeday_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"shade/internal/apps/safeday"
	"shade/internal/integrations"
)

// recorderStub 记录 Dify 外部调用事件，供断言日志不含 API Key。
type recorderStub struct {
	mu     sync.Mutex
	events []string
	fields []map[string]any
}

func (r *recorderStub) Record(_ context.Context, _, _, event, messageCN, raw string, fields map[string]any) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, event+"|"+messageCN+"|"+raw)
	cp := map[string]any{}
	for k, v := range fields {
		cp[k] = v
	}
	r.fields = append(r.fields, cp)
	return nil
}

func (r *recorderStub) snapshot() ([]string, []map[string]any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string{}, r.events...), append([]map[string]any{}, r.fields...)
}

// TestStreamRunnerAPIKeyOverride 安全日 StreamRunner → WorkflowClient.Start 单次 key 覆盖；
// 空覆盖保持全局配置；日志不输出 key。
func TestStreamRunnerAPIKeyOverride(t *testing.T) {
	var (
		mu    sync.Mutex
		auths []string
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Inputs map[string]string `json:"inputs"`
			Mode   string            `json:"response_mode"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		auths = append(auths, r.Header.Get("Authorization"))
		mu.Unlock()
		_, _ = w.Write([]byte("data: {\"event\":\"workflow_finished\",\"data\":{\"status\":\"succeeded\"}}\n\n"))
	}))
	defer srv.Close()

	rec := &recorderStub{}
	cfg := integrations.NewMapProvider(map[string]string{"DIFY_API_URL": srv.URL})
	client := &integrations.WorkflowClient{
		Cfg: cfg, APIKey: "global-key", HTTP: srv.Client(),
		Recorder: rec, Retry: integrations.RetryPolicy{MaxAttempts: 1},
	}
	runner := &integrations.StreamRunner{Client: client}

	if err := runner.Run(context.Background(), safeday.WorkflowInput{
		URL: "u1", Date: "d1", Class: "c1", APIKey: "override-key",
	}); err != nil {
		t.Fatalf("override 触发失败: %v", err)
	}
	if err := runner.Run(context.Background(), safeday.WorkflowInput{URL: "u2", Date: "d2", Class: "c2"}); err != nil {
		t.Fatalf("全局 key 触发失败: %v", err)
	}
	// 全局 key 缺失，仅 override
	clientNoKey := &integrations.WorkflowClient{
		Cfg: cfg, HTTP: srv.Client(), Recorder: rec, Retry: integrations.RetryPolicy{MaxAttempts: 1},
	}
	if err := clientNoKey.Start(context.Background(), integrations.StreamInput{
		Inputs: map[string]any{"url": "u3", "date": "d3", "class": "c3"}, User: "u", APIKey: "only-override",
	}); err != nil {
		t.Fatalf("仅 override 应可触发: %v", err)
	}
	// 全空 → 未配置
	if err := clientNoKey.Start(context.Background(), integrations.StreamInput{
		Inputs: map[string]any{"url": "u4"}, User: "u",
	}); err == nil || !strings.Contains(err.Error(), "未配置") {
		t.Fatalf("未配置应报错: %v", err)
	}

	mu.Lock()
	got := append([]string{}, auths...)
	mu.Unlock()
	if len(got) != 3 || got[0] != "Bearer override-key" || got[1] != "Bearer global-key" || got[2] != "Bearer only-override" {
		t.Fatalf("Authorization 不符: %v", got)
	}

	time.Sleep(100 * time.Millisecond) // 等后台流消费/日志写入
	events, fields := rec.snapshot()
	for _, e := range events {
		for _, key := range []string{"override-key", "global-key", "only-override"} {
			if strings.Contains(e, key) {
				t.Fatalf("日志不得输出 API Key: %s", e)
			}
		}
	}
	for _, f := range fields {
		raw, _ := json.Marshal(f)
		for _, key := range []string{"override-key", "global-key", "only-override"} {
			if strings.Contains(string(raw), key) {
				t.Fatalf("日志字段不得输出 API Key: %s", raw)
			}
		}
	}
}
