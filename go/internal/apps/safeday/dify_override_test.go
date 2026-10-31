package safeday

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// TestDifyWorkflowAPIKeyOverride 单次 APIKey 覆盖：非空优先于全局配置，空值保持原全局逻辑。
func TestDifyWorkflowAPIKeyOverride(t *testing.T) {
	var (
		mu     sync.Mutex
		auths  []string
		inputs []map[string]string
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Inputs map[string]string `json:"inputs"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		auths = append(auths, r.Header.Get("Authorization"))
		inputs = append(inputs, body.Inputs)
		mu.Unlock()
		_, _ = w.Write([]byte("data: {\"event\":\"workflow_finished\",\"data\":{\"status\":\"succeeded\"}}\n\n"))
	}))
	defer srv.Close()

	cfg := fakeConfig{"DIFY_API_URL": srv.URL, "DIFY_SAFEDAY_API_KEY": "global-key"}
	d := &DifyWorkflow{Cfg: cfg, HTTP: srv.Client()}
	if err := d.Run(context.Background(), WorkflowInput{URL: "u1", Date: "d1", Class: "c1", APIKey: "override-key"}); err != nil {
		t.Fatalf("覆盖 key 触发失败: %v", err)
	}
	if err := d.Run(context.Background(), WorkflowInput{URL: "u2", Date: "d2", Class: "c2"}); err != nil {
		t.Fatalf("全局 key 触发失败: %v", err)
	}
	mu.Lock()
	if len(auths) != 2 || auths[0] != "Bearer override-key" || auths[1] != "Bearer global-key" {
		mu.Unlock()
		t.Fatalf("Authorization 不符: %v", auths)
	}
	if inputs[0]["url"] != "u1" || inputs[0]["date"] != "d1" || inputs[0]["class"] != "c1" {
		mu.Unlock()
		t.Fatalf("工作流入参仍应仅 url/date/class: %v", inputs[0])
	}
	mu.Unlock()

	// 全局 key 缺失但 override 存在 → 可触发
	cfgNoKey := fakeConfig{"DIFY_API_URL": srv.URL}
	d2 := &DifyWorkflow{Cfg: cfgNoKey, HTTP: srv.Client()}
	if err := d2.Run(context.Background(), WorkflowInput{URL: "u3", APIKey: "only-override"}); err != nil {
		t.Fatalf("仅 override 应可触发: %v", err)
	}
	// 全空 → 原报错文案
	err := d2.Run(context.Background(), WorkflowInput{URL: "u4"})
	if err == nil || !strings.Contains(err.Error(), "未配置 DIFY_API_URL 或 DIFY_SAFEDAY_API_KEY") {
		t.Fatalf("未配置报错文案不符: %v", err)
	}
}
