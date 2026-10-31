package callme

// WeKnora 客户端：认证 X-API-Key + X-External-User-ID（登录账号）；会话/消息/附件/模型/Agent/
// 联网搜索提供商/流式对话。接口可注入（测试用 httptest / fake），HTTP 实现不硬编码密钥。

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"shade/internal/integrations"
)

// WeKnora 错误（可注入客户端与 HTTP 实现共用）。
type WkError struct {
	Status  int    // HTTP 状态码（0 = 非 HTTP 错误）
	Message string // 面向用户的中文提示（expose=true 的配置错误）
	Expose  bool   // true 时提示可直接下发
	Timeout bool   // 上游超时
	Err     error
}

func (e *WkError) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("weknora: %s: %v", e.Message, e.Err)
	}
	return "weknora: " + e.Message
}

func (e *WkError) Unwrap() error { return e.Err }

// NotConfigured 未配置错误（expose）。
func NotConfigured(msg string) *WkError { return &WkError{Message: msg, Expose: true} }

// ChatRequest 单轮对话入参。
type ChatRequest struct {
	Query           string   `json:"query"`
	Images          []Image  `json:"images,omitempty"`
	AttachmentIDs   []string `json:"attachment_ids,omitempty"`
	WebSearch       bool     `json:"web_search_enabled"`
	ModelID         string   `json:"summary_model_id,omitempty"`
	ReasoningEffort string   `json:"reasoning_effort,omitempty"`
}

// Image 图片（dataURL）。
type Image struct {
	Data string `json:"data"`
}

// Attachment 会话附件（WeKnora 临时文档）。
type Attachment struct {
	ID       string `json:"id"`
	FileName string `json:"file_name"`
	FileSize int64  `json:"file_size"`
	Status   string `json:"status"`
	Error    string `json:"error_message,omitempty"`
}

// Model 模型条目（capabilities / parameters 双位置兼容由调用方处理）。
type Model struct {
	ID           string         `json:"id"`
	Name         string         `json:"name"`
	DisplayName  string         `json:"display_name"`
	Type         string         `json:"type"`
	Capabilities map[string]any `json:"capabilities"`
	Parameters   map[string]any `json:"parameters"`
}

// NameOf 展示名（display_name 优先）。
func (m Model) NameOf() string {
	if m.DisplayName != "" {
		return m.DisplayName
	}
	return m.Name
}

// ThinkingLevels 思考强度档位（capabilities / parameters 两处兼容）。
func (m Model) ThinkingLevels() []string {
	return anyToStrings(m.Capabilities["thinking_levels"], m.Parameters["thinking_levels"])
}

// SupportsVision 视觉能力（两处兼容）。
func (m Model) SupportsVision() bool {
	return anyToBool(m.Capabilities["supports_vision"]) || anyToBool(m.Parameters["supports_vision"])
}

// WeKnoraClient WeKnora API（全部方法携带 username 作 X-External-User-ID）。
type WeKnoraClient interface {
	CreateSession(ctx context.Context, username, title string) (map[string]any, error)
	ListSessions(ctx context.Context, username string) (map[string]any, error)
	GetSession(ctx context.Context, username, id string) (map[string]any, error)
	ListMessages(ctx context.Context, username, id string, limit int, beforeTime string) (map[string]any, error)
	UpdateSession(ctx context.Context, username, id, title string) (map[string]any, error)
	DeleteSession(ctx context.Context, username, id string) error
	DeleteMessage(ctx context.Context, username, sessionID, messageID string) error
	UploadAttachment(ctx context.Context, username, sessionID, filename, mimetype string, buf []byte) (Attachment, error)
	ListAttachments(ctx context.Context, username, sessionID string) ([]Attachment, error)
	ListModels(ctx context.Context, username string) ([]Model, error)
	GetAgent(ctx context.Context, username string) (map[string]any, error)
	ListWebSearchProviders(ctx context.Context, username string) ([]map[string]any, error)
	// AgentChatStream 发起 agent 对话，返回 SSE 字节流（调用方负责关闭）。
	AgentChatStream(ctx context.Context, username, sessionID string, req ChatRequest) (io.ReadCloser, error)
}

// HTTPClient WeKnora HTTP 实现：连接参数运行时经 Cfg 读取（WEKNORA_API_URL / WEKNORA_API_KEY /
// WEKNORA_AGENT_ID / WEKNORA_SYNC_USER），每次请求解析、配置热更即时生效；密钥缺失 fail-closed
// （返回 NotConfigured，绝不带空密钥外呼）。测试注入 fake 或 Cfg 桩 + httptest，不实连真实服务。
type HTTPClient struct {
	Cfg  ConfigReader // 运行时配置（WEKNORA_*；*config.Store 或测试桩）
	HTTP *http.Client // 可注入；nil 时普通请求 30s 超时、流式不限总时长
}

// NewHTTPClient 构造 WeKnora HTTP 客户端（连接参数每次请求从 cfg 解析）。
func NewHTTPClient(cfg ConfigReader) *HTTPClient { return &HTTPClient{Cfg: cfg} }

var _ WeKnoraClient = (*HTTPClient)(nil)

// wkConn 单次请求解析出的连接参数。
type wkConn struct {
	baseURL  string
	apiKey   string
	agentID  string
	syncUser string // 知识库接口 X-External-User-ID（缺省 admin）
}

func (c *HTTPClient) httpc() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return &http.Client{Timeout: 30 * time.Second}
}

// cfgValue 读运行时配置（未接入/键不存在返回空串）。
func (c *HTTPClient) cfgValue(ctx context.Context, key string) string {
	if c == nil || c.Cfg == nil {
		return ""
	}
	v, err := c.Cfg.Get(ctx, key)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(v)
}

// conn 运行时解析连接参数。
func (c *HTTPClient) conn(ctx context.Context) wkConn {
	return wkConn{
		baseURL:  c.cfgValue(ctx, "WEKNORA_API_URL"),
		apiKey:   c.cfgValue(ctx, "WEKNORA_API_KEY"),
		agentID:  c.cfgValue(ctx, "WEKNORA_AGENT_ID"),
		syncUser: c.cfgValue(ctx, "WEKNORA_SYNC_USER"),
	}
}

// resolveAgent 对话/会话/附件/模型等 agent 侧接口：URL / API key / agent 任一缺失 fail-closed。
func (c *HTTPClient) resolveAgent(ctx context.Context) (wkConn, error) {
	cc := c.conn(ctx)
	if cc.baseURL == "" || cc.apiKey == "" || cc.agentID == "" {
		return wkConn{}, NotConfigured("WeKnora 未配置（WEKNORA_API_KEY / WEKNORA_AGENT_ID）")
	}
	return cc, nil
}

func (c *HTTPClient) newReq(ctx context.Context, cc wkConn, method, path, username string, body io.Reader) (*http.Request, error) {
	ctx = integrations.WithServiceName(ctx, "weknora")
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(cc.baseURL, "/")+path, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-API-Key", cc.apiKey)
	req.Header.Set("X-External-User-ID", username)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return req, nil
}

func (c *HTTPClient) doJSON(ctx context.Context, method, path, username string, payload any) (map[string]any, error) {
	ctx = integrations.WithServiceName(ctx, "weknora")
	cc, err := c.resolveAgent(ctx)
	if err != nil {
		return nil, err
	}
	var body io.Reader
	if payload != nil {
		raw, _ := json.Marshal(payload)
		body = bytes.NewReader(raw)
	}
	req, err := c.newReq(ctx, cc, method, path, username, body)
	if err != nil {
		return nil, err
	}
	res, err := c.httpc().Do(req)
	if err != nil {
		return nil, &WkError{Message: "WeKnora 请求失败", Err: err, Timeout: isTimeout(err)}
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(res.Body, 8<<20))
	if res.StatusCode == 401 {
		return nil, &WkError{Status: 401, Message: "WeKnora 认证失败，请检查 WEKNORA_API_KEY"}
	}
	if res.StatusCode == 404 {
		return nil, &WkError{Status: 404, Message: "会话不存在或已被删除"}
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return nil, &WkError{Status: res.StatusCode, Message: "WeKnora 服务返回错误", Err: fmt.Errorf("status %d", res.StatusCode)}
	}
	out := map[string]any{}
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &out)
	}
	return out, nil
}

func isTimeout(err error) bool {
	type timeouter interface{ Timeout() bool }
	if t, ok := err.(timeouter); ok {
		return t.Timeout()
	}
	return false
}

// unwrapList 从 WeKnora 响应里取列表（data 数组或 data.list / data.sessions / data.messages）。
func unwrapList(resp map[string]any, keys ...string) []map[string]any {
	if resp == nil {
		return nil
	}
	if arr, ok := asAnySlice(resp["data"]); ok {
		return toMaps(arr)
	}
	if arr, ok := resp["data"].([]map[string]any); ok {
		return arr
	}
	if data, ok := resp["data"].(map[string]any); ok {
		for _, k := range append(keys, "list", "sessions", "messages") {
			if arr, ok := asAnySlice(data[k]); ok {
				return toMaps(arr)
			}
			if arr, ok := data[k].([]map[string]any); ok {
				return arr
			}
		}
	}
	for _, k := range keys {
		if arr, ok := asAnySlice(resp[k]); ok {
			return toMaps(arr)
		}
	}
	return nil
}

func asAnySlice(v any) ([]any, bool) {
	arr, ok := v.([]any)
	return arr, ok
}

func toMaps(arr []any) []map[string]any {
	out := make([]map[string]any, 0, len(arr))
	for _, it := range arr {
		if m, ok := it.(map[string]any); ok {
			out = append(out, m)
		}
	}
	return out
}

// unwrapData 取 data 对象（无 data 时返回原响应）。
func unwrapData(resp map[string]any) map[string]any {
	if data, ok := resp["data"].(map[string]any); ok {
		return data
	}
	return resp
}

func (c *HTTPClient) CreateSession(ctx context.Context, username, title string) (map[string]any, error) {
	return c.doJSON(ctx, http.MethodPost, "/sessions", username, map[string]any{"title": title})
}

func (c *HTTPClient) ListSessions(ctx context.Context, username string) (map[string]any, error) {
	return c.doJSON(ctx, http.MethodGet, "/sessions", username, nil)
}

func (c *HTTPClient) GetSession(ctx context.Context, username, id string) (map[string]any, error) {
	return c.doJSON(ctx, http.MethodGet, "/sessions/"+url.PathEscape(id), username, nil)
}

func (c *HTTPClient) ListMessages(ctx context.Context, username, id string, limit int, beforeTime string) (map[string]any, error) {
	q := url.Values{}
	if limit > 0 {
		q.Set("limit", strconv.Itoa(limit))
	}
	if beforeTime != "" {
		q.Set("before_time", beforeTime)
	}
	path := "/messages/" + url.PathEscape(id) + "/load"
	if len(q) > 0 {
		path += "?" + q.Encode()
	}
	return c.doJSON(ctx, http.MethodGet, path, username, nil)
}

func (c *HTTPClient) UpdateSession(ctx context.Context, username, id, title string) (map[string]any, error) {
	return c.doJSON(ctx, http.MethodPut, "/sessions/"+url.PathEscape(id), username, map[string]any{"title": title})
}

func (c *HTTPClient) DeleteSession(ctx context.Context, username, id string) error {
	_, err := c.doJSON(ctx, http.MethodDelete, "/sessions/"+url.PathEscape(id), username, nil)
	return err
}

func (c *HTTPClient) DeleteMessage(ctx context.Context, username, sessionID, messageID string) error {
	_, err := c.doJSON(ctx, http.MethodDelete,
		"/messages/"+url.PathEscape(sessionID)+"/"+url.PathEscape(messageID), username, nil)
	return err
}

// UploadAttachment 会话附件（multipart：file + agent_id）。
func (c *HTTPClient) UploadAttachment(ctx context.Context, username, sessionID, filename, mimetype string, buf []byte) (Attachment, error) {
	cc, err := c.resolveAgent(ctx)
	if err != nil {
		return Attachment{}, err
	}
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	fw, _ := mw.CreateFormFile("file", filename)
	_, _ = fw.Write(buf)
	_ = mw.WriteField("agent_id", cc.agentID)
	_ = mw.Close()
	req, err := c.newReq(ctx, cc, http.MethodPost,
		"/sessions/"+url.PathEscape(sessionID)+"/attachments", username, &body)
	if err != nil {
		return Attachment{}, err
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	res, err := c.httpc().Do(req)
	if err != nil {
		return Attachment{}, &WkError{Message: "WeKnora 请求失败", Err: err, Timeout: isTimeout(err)}
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(res.Body, 4<<20))
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return Attachment{}, &WkError{Status: res.StatusCode, Message: "附件上传失败"}
	}
	resp := map[string]any{}
	_ = json.Unmarshal(raw, &resp)
	doc := unwrapData(resp)
	return Attachment{
		ID:       StringOf(doc, "id"),
		FileName: StringOf(doc, "file_name"),
		FileSize: anyToInt64(doc["file_size"]),
		Status:   StringOf(doc, "status"),
		Error:    StringOf(doc, "error_message"),
	}, nil
}

func (c *HTTPClient) ListAttachments(ctx context.Context, username, sessionID string) ([]Attachment, error) {
	resp, err := c.doJSON(ctx, http.MethodGet,
		"/sessions/"+url.PathEscape(sessionID)+"/attachments", username, nil)
	if err != nil {
		return nil, err
	}
	var out []Attachment
	for _, m := range unwrapList(resp) {
		out = append(out, Attachment{
			ID:       StringOf(m, "id"),
			FileName: StringOf(m, "file_name"),
			FileSize: anyToInt64(m["file_size"]),
			Status:   StringOf(m, "status"),
			Error:    StringOf(m, "error_message"),
		})
	}
	return out, nil
}

func (c *HTTPClient) ListModels(ctx context.Context, username string) ([]Model, error) {
	resp, err := c.doJSON(ctx, http.MethodGet, "/models", username, nil)
	if err != nil {
		return nil, err
	}
	var out []Model
	for _, m := range unwrapList(resp) {
		raw, _ := json.Marshal(m)
		var model Model
		_ = json.Unmarshal(raw, &model)
		out = append(out, model)
	}
	return out, nil
}

func (c *HTTPClient) GetAgent(ctx context.Context, username string) (map[string]any, error) {
	cc, err := c.resolveAgent(ctx)
	if err != nil {
		return nil, err
	}
	return c.doJSON(ctx, http.MethodGet, "/agents/"+url.PathEscape(cc.agentID), username, nil)
}

func (c *HTTPClient) ListWebSearchProviders(ctx context.Context, username string) ([]map[string]any, error) {
	resp, err := c.doJSON(ctx, http.MethodGet, "/web-search-providers", username, nil)
	if err != nil {
		return nil, err
	}
	return unwrapList(resp), nil
}

// AgentChatStream POST /agent-chat/{sessionId}（SSE 流；不限总时长由调用方看门狗兜底）。
func (c *HTTPClient) AgentChatStream(ctx context.Context, username, sessionID string, req ChatRequest) (io.ReadCloser, error) {
	cc, err := c.resolveAgent(ctx)
	if err != nil {
		return nil, err
	}
	payload := map[string]any{
		"query":              req.Query,
		"agent_id":           cc.agentID,
		"agent_enabled":      true,
		"web_search_enabled": req.WebSearch,
		"channel":            "api",
	}
	if len(req.Images) > 0 {
		payload["images"] = req.Images
	}
	if len(req.AttachmentIDs) > 0 {
		payload["attachment_ids"] = req.AttachmentIDs
	}
	if req.ModelID != "" {
		payload["summary_model_id"] = req.ModelID
	}
	if strings.TrimSpace(req.ReasoningEffort) != "" {
		payload["reasoning_effort"] = req.ReasoningEffort
	}
	raw, _ := json.Marshal(payload)
	httpReq, err := c.newReq(ctx, cc, http.MethodPost,
		"/agent-chat/"+url.PathEscape(sessionID), username, bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	streamClient := &http.Client{} // 无总超时；静默看门狗在路由层
	if c.HTTP != nil {
		streamClient = c.HTTP
	}
	res, err := streamClient.Do(httpReq)
	if err != nil {
		return nil, &WkError{Message: "WeKnora 请求失败", Err: err, Timeout: isTimeout(err)}
	}
	if res.StatusCode == 401 {
		res.Body.Close()
		return nil, &WkError{Status: 401, Message: "WeKnora 认证失败，请检查 WEKNORA_API_KEY"}
	}
	if res.StatusCode == 404 {
		res.Body.Close()
		return nil, &WkError{Status: 404, Message: "会话不存在或已被删除"}
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		res.Body.Close()
		return nil, &WkError{Status: res.StatusCode, Message: "WeKnora 服务返回错误"}
	}
	return res.Body, nil
}

// ===== 小工具 =====

func anyToStrings(vals ...any) []string {
	for _, v := range vals {
		arr, ok := asAnySlice(v)
		if !ok {
			continue
		}
		var out []string
		for _, it := range arr {
			out = append(out, fmt.Sprint(it))
		}
		return out
	}
	return nil
}

func anyToBool(v any) bool {
	b, ok := v.(bool)
	return ok && b
}

func anyToInt64(v any) int64 {
	switch t := v.(type) {
	case float64:
		return int64(t)
	case int64:
		return t
	case string:
		n, _ := strconv.ParseInt(t, 10, 64)
		return n
	}
	return 0
}

// StringOf 取 map 字段字符串。
func StringOf(m map[string]any, key string) string {
	if m == nil {
		return ""
	}
	if v, ok := m[key]; ok && v != nil {
		return fmt.Sprint(v)
	}
	return ""
}
