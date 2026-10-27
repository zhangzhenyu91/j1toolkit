// Dify 照片验证客户端（HTTP 实现，对应 server/src/worklog/dify.js）：
// POST {DIFY_API_URL}/v1/workflows/run（blocking）；inputs: date / destination / picture（remote_url）。
// 输出仅识别的水印信息；日期/地点比对在后端 CheckWatermark 完成。测试注入 fake，不实连 Dify。
package worklog

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"
)

// DifyHTTPClient 基于 Dify workflow run 的默认实现。
type DifyHTTPClient struct {
	BaseURL string // DIFY_API_URL（不带 /v1）
	APIKey  string // DIFY_WORKLOG_API_KEY
	HTTP    *http.Client
}

var _ DifyClient = (*DifyHTTPClient)(nil)

func (c *DifyHTTPClient) httpc() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return &http.Client{Timeout: 90 * time.Second}
}

// VerifyPhoto POST /v1/workflows/run（blocking）；任何异常一律归 OK=false。
func (c *DifyHTTPClient) VerifyPhoto(ctx context.Context, username, date, destination, url string) VerifyResult {
	if c.BaseURL == "" || c.APIKey == "" {
		return VerifyResult{}
	}
	body, _ := json.Marshal(map[string]any{
		"inputs": map[string]any{
			"date":        date,
			"destination": destination,
			// 单文件变量传对象（数组会报 invalid_param: must be a file）
			"picture": map[string]any{"transfer_method": "remote_url", "url": url, "type": "image"},
		},
		"response_mode": "blocking",
		"user":          username,
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		strings.TrimRight(c.BaseURL, "/")+"/v1/workflows/run", bytes.NewReader(body))
	if err != nil {
		return VerifyResult{}
	}
	req.Header.Set("Authorization", "Bearer "+c.APIKey)
	req.Header.Set("Content-Type", "application/json")
	res, err := c.httpc().Do(req)
	if err != nil {
		return VerifyResult{}
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(res.Body, 4<<20))
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return VerifyResult{}
	}
	var payload struct {
		Data struct {
			Outputs map[string]any `json:"outputs"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return VerifyResult{}
	}
	get := func(key string) string {
		v, ok := payload.Data.Outputs[key]
		if !ok || v == nil {
			return ""
		}
		return fmt.Sprint(v)
	}
	return VerifyResult{
		OK:          true,
		WorkContent: get("title"),
		Time:        get("time"),
		Weather:     NormalizeWeather(get("weather")),
		Location:    get("location"),
		Lng:         get("lng"),
		Lat:         get("lat"),
	}
}

// reWeatherLatex 归一视觉模型偶发的 LaTeX 温度写法（$17^{\circ} \mathrm{C}$ → 17℃）。
var reWeatherLatex = regexp.MustCompile(
	`\$?\s*(-?\d+(?:\.\d+)?)\s*(?:\\mathrm\s*\{\s*\^\{?\\circ\}?\s*C\s*\}|` +
		`\^\s*\{?\s*\\circ\s*\}?\s*\{?\s*\\(?:mathrm|text)\s*\{\s*C\s*\}\s*\}?)\s*\$?`)

// NormalizeWeather 温度写法归一 + 空白收敛。
func NormalizeWeather(s string) string {
	out := reWeatherLatex.ReplaceAllString(s, "${1}℃")
	out = regexp.MustCompile(`\s{2,}`).ReplaceAllString(out, " ")
	return strings.TrimSpace(out)
}
