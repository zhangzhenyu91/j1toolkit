// Dify workflow 调用（blocking）：全局 API URL/User/Timeout 从 config.Store 读取，
// apiKey 为规则独立密钥。未配置地址时直接报错、不发起网络请求（网络默认禁用）。
package wechatanswer

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"shade/internal/integrations"
)

// DifyHTTPClient 基于 {DIFY_API_URL}/v1/workflows/run 的默认实现。
type DifyHTTPClient struct {
	BaseURL string // DIFY_API_URL（不带 /v1）；空 = 未配置，禁用
	User    string // DIFY_USER
	Timeout time.Duration
	HTTP    *http.Client
	// Retries 失败重试次数（默认 2，共 3 次尝试，间隔 3s）。
	Retries int
}

var _ DifyClient = (*DifyHTTPClient)(nil)

func (c *DifyHTTPClient) httpc() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	to := c.Timeout
	if to <= 0 {
		to = 90 * time.Second
	}
	return &http.Client{Timeout: to}
}

// RunWorkflow POST /v1/workflows/run（blocking），inputs 原样透传；返回 data.outputs。
func (c *DifyHTTPClient) RunWorkflow(ctx context.Context, apiKey string, inputs map[string]string) (map[string]any, error) {
	ctx = integrations.WithServiceName(ctx, "dify")
	if strings.TrimSpace(c.BaseURL) == "" {
		return nil, errors.New("dify: 未配置 DIFY_API_URL，调用禁用")
	}
	if strings.TrimSpace(apiKey) == "" {
		return nil, errors.New("dify: 未配置 API Key")
	}
	retries := c.Retries
	if retries == 0 {
		retries = 2
	}
	var lastErr error
	for attempt := 0; attempt <= retries; attempt++ {
		out, err := c.runOnce(ctx, apiKey, inputs)
		if err == nil {
			return out, nil
		}
		lastErr = err
		if attempt < retries {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(3 * time.Second):
			}
		}
	}
	return nil, lastErr
}

func (c *DifyHTTPClient) runOnce(ctx context.Context, apiKey string, inputs map[string]string) (map[string]any, error) {
	user := c.User
	if user == "" {
		user = "shade-yixia"
	}
	body, err := json.Marshal(map[string]any{
		"inputs":        inputs,
		"response_mode": "blocking",
		"user":          user,
	})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		strings.TrimRight(c.BaseURL, "/")+"/v1/workflows/run", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Content-Type", "application/json")
	res, err := c.httpc().Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(res.Body, 4<<20))
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return nil, fmt.Errorf("dify 调用失败（HTTP %d）：%s", res.StatusCode, truncate(string(raw), 300))
	}
	var payload struct {
		Data struct {
			Outputs map[string]any `json:"outputs"`
			Status  string         `json:"status"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, fmt.Errorf("dify 返回非 JSON（HTTP %d）：%s", res.StatusCode, truncate(string(raw), 200))
	}
	return payload.Data.Outputs, nil
}

// difyFromConfig 每次调用从 config.Store 解析全局 Dify 配置（支持配置热更）。
type difyFromConfig struct {
	cfg ConfigGet
}

var _ DifyClient = (*difyFromConfig)(nil)

func (c *difyFromConfig) RunWorkflow(ctx context.Context, apiKey string, inputs map[string]string) (map[string]any, error) {
	base := c.cfgValue(ctx, "DIFY_API_URL", "")
	user := c.cfgValue(ctx, "DIFY_USER", "shade-yixia")
	timeoutMs, _ := strconv.Atoi(c.cfgValue(ctx, "DIFY_TIMEOUT_MS", "90000"))
	client := &DifyHTTPClient{
		BaseURL: base,
		User:    user,
		Timeout: time.Duration(timeoutMs) * time.Millisecond,
	}
	return client.RunWorkflow(ctx, apiKey, inputs)
}

func (c *difyFromConfig) cfgValue(ctx context.Context, key, def string) string {
	if c.cfg == nil {
		return def
	}
	v, err := c.cfg.Get(ctx, key)
	if err != nil || v == "" {
		return def
	}
	return v
}

// dify 取 Dify 客户端（注入优先；缺省走 config.Store）。
func (m *Module) dify() DifyClient {
	if m.Dify != nil {
		return m.Dify
	}
	return &difyFromConfig{cfg: m.Cfg}
}

// cfgValue 读运行时配置（缺省回落 def；Cfg 未注入或读取失败时取 def）。
func (m *Module) cfgValue(ctx context.Context, key, def string) string {
	if m.Cfg == nil {
		return def
	}
	v, err := m.Cfg.Get(ctx, key)
	if err != nil || v == "" {
		return def
	}
	return v
}

// parseWorkflowOutputs 解析 Dify 提取输出：outputs.result 可为 JSON 字符串或对象包裹，其余直接取 outputs。
func parseWorkflowOutputs(outputs map[string]any) map[string]any {
	if outputs == nil {
		return nil
	}
	if raw, ok := outputs["result"].(string); ok {
		var parsed map[string]any
		if err := json.Unmarshal([]byte(raw), &parsed); err == nil && parsed != nil {
			return parsed
		}
		return outputs
	}
	if inner, ok := outputs["result"].(map[string]any); ok && inner != nil {
		return inner
	}
	return outputs
}

// verdictAccept is_tree_hazard / is_crossing 终审：显式 false/否/不是/no/0 才否，其余视为是。
func verdictAccept(value any) bool {
	switch v := value.(type) {
	case nil:
		return true
	case bool:
		return v
	case float64:
		return v != 0
	case int:
		return v != 0
	case string:
		s := strings.TrimSpace(strings.ToLower(v))
		switch s {
		case "false", "否", "不是", "no", "0":
			return false
		}
		return true
	default:
		return true
	}
}
