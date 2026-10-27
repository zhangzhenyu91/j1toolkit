package callme

// WeKnora 知识库内容管理（对应 server/src/services/weknora.js 知识库函数组）：列知识 / 上传知识文件 /
// 删单条知识。供落库类对接复用（聊天日志归档 chatlog 等）；只需 API key（agent 不参与），
// X-External-User-ID 取 WEKNORA_SYNC_USER（缺省 admin）。连接参数同样运行时读取、密钥 fail-closed。

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"strconv"
	"strings"
)

// KnowledgeItem 知识条目（查重用）。
type KnowledgeItem struct {
	ID   string
	Name string // 末段基名（file_name/fileName/title 兼容取值，可能带文件夹路径前缀）
}

// KnowledgeConflictError 上传同名冲突（HTTP 409）：携带响应里已有条目 id，调用方删旧重试。
type KnowledgeConflictError struct{ IDs []string }

func (e *KnowledgeConflictError) Error() string { return "weknora: 知识库已存在同名文件" }

// KnowledgeAPI 知识库内容管理接口（*HTTPClient 实现；调用方注入 fake 测试）。
type KnowledgeAPI interface {
	// ListFolderKnowledge 拉文件夹内全部条目（folder_path 精确匹配、子文件夹不递归；分页兜底）。
	ListFolderKnowledge(ctx context.Context, kbID, folderPath string) ([]KnowledgeItem, error)
	// UploadKnowledgeFile 上传知识文件（fileName 带 / 路径前缀即文件夹）；同名冲突返回 *KnowledgeConflictError。
	UploadKnowledgeFile(ctx context.Context, kbID, fileName, title, channel string, buf []byte) (string, error)
	// DeleteKnowledge 删单条知识（WeKnora 侧删除异步）。
	DeleteKnowledge(ctx context.Context, knowledgeID string) error
}

var _ KnowledgeAPI = (*HTTPClient)(nil)

// 列知识分页参数（翻页兜底上限防接口分页异常死循环）。
const (
	kbPageSize     = 100
	kbListMaxPages = 10
)

// resolveKB 知识库接口连接参数：仅要求 URL + API key（agent 不参与）；fail-closed。
func (c *HTTPClient) resolveKB(ctx context.Context) (wkConn, error) {
	cc := c.conn(ctx)
	if cc.baseURL == "" || cc.apiKey == "" {
		return wkConn{}, NotConfigured("WeKnora 未配置（WEKNORA_API_KEY）")
	}
	if cc.syncUser == "" {
		cc.syncUser = "admin"
	}
	return cc, nil
}

// doKBJSON 知识库 JSON 调用（X-External-User-ID 取 syncUser）；响应 {success:false} 视为上游失败。
func (c *HTTPClient) doKBJSON(ctx context.Context, method, path string, payload any) (map[string]any, error) {
	cc, err := c.resolveKB(ctx)
	if err != nil {
		return nil, err
	}
	var body io.Reader
	if payload != nil {
		raw, _ := json.Marshal(payload)
		body = bytes.NewReader(raw)
	}
	req, err := c.newReq(ctx, cc, method, path, cc.syncUser, body)
	if err != nil {
		return nil, err
	}
	res, err := c.httpc().Do(req)
	if err != nil {
		return nil, &WkError{Message: "WeKnora 请求失败", Err: err, Timeout: isTimeout(err)}
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(res.Body, 8<<20))
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return nil, &WkError{Status: res.StatusCode, Message: "WeKnora 服务返回错误", Err: fmt.Errorf("status %d", res.StatusCode)}
	}
	out := map[string]any{}
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &out)
	}
	if success, ok := out["success"].(bool); ok && !success {
		return nil, &WkError{Message: "WeKnora 响应异常",
			Err: fmt.Errorf("%s", firstNonEmpty(StringOf(out, "message"), StringOf(out, "error")))}
	}
	return out, nil
}

// ListFolderKnowledge 拉文件夹内全部条目（GET /knowledge-bases/{kbId}/knowledge，folder_path 精确匹配）。
func (c *HTTPClient) ListFolderKnowledge(ctx context.Context, kbID, folderPath string) ([]KnowledgeItem, error) {
	var out []KnowledgeItem
	for page := 1; page <= kbListMaxPages; page++ {
		q := url.Values{}
		q.Set("folder_path", folderPath)
		q.Set("folder_recursive", "false")
		q.Set("page", strconv.Itoa(page))
		q.Set("page_size", strconv.Itoa(kbPageSize))
		resp, err := c.doKBJSON(ctx, http.MethodGet,
			"/knowledge-bases/"+url.PathEscape(kbID)+"/knowledge?"+q.Encode(), nil)
		if err != nil {
			return nil, err
		}
		items := unwrapList(resp, "items", "list")
		for _, it := range items {
			out = append(out, KnowledgeItem{ID: kbItemID(it), Name: kbBaseName(it)})
		}
		if len(items) < kbPageSize {
			break
		}
	}
	return out, nil
}

// UploadKnowledgeFile 上传知识库文件（POST /knowledge-bases/{kbId}/knowledge/file，multipart：
// file + fileName（带目录路径前缀即文件夹）+ title + channel）；同名冲突返回 *KnowledgeConflictError。
func (c *HTTPClient) UploadKnowledgeFile(ctx context.Context, kbID, fileName, title, channel string, buf []byte) (string, error) {
	cc, err := c.resolveKB(ctx)
	if err != nil {
		return "", err
	}
	baseName := fileName
	if i := strings.LastIndexByte(baseName, '/'); i >= 0 {
		baseName = baseName[i+1:]
	}
	partName := firstNonEmpty(title, baseName)
	ctype := "application/octet-stream"
	if fileExt(baseName) == "xlsx" {
		ctype = "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"
	}
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	header := make(textproto.MIMEHeader)
	header.Set("Content-Disposition",
		fmt.Sprintf(`form-data; name="file"; filename="%s"`, escapeQuotes(partName)))
	header.Set("Content-Type", ctype)
	fw, err := mw.CreatePart(header)
	if err != nil {
		return "", err
	}
	_, _ = fw.Write(buf)
	_ = mw.WriteField("fileName", fileName)
	if title != "" {
		_ = mw.WriteField("title", title)
	}
	if channel != "" {
		_ = mw.WriteField("channel", channel)
	}
	_ = mw.Close()
	req, err := c.newReq(ctx, cc, http.MethodPost,
		"/knowledge-bases/"+url.PathEscape(kbID)+"/knowledge/file", cc.syncUser, &body)
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	res, err := c.httpc().Do(req)
	if err != nil {
		return "", &WkError{Message: "WeKnora 请求失败", Err: err, Timeout: isTimeout(err)}
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(res.Body, 8<<20))
	if res.StatusCode == http.StatusConflict {
		// 409 同名冲突：取响应已有条目 id 供调用方删旧重试（data 形态未实测，多形态兼容）。
		resp := map[string]any{}
		_ = json.Unmarshal(raw, &resp)
		return "", &KnowledgeConflictError{IDs: kbConflictIDs(resp["data"])}
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return "", &WkError{Status: res.StatusCode, Message: "WeKnora 服务返回错误", Err: fmt.Errorf("status %d", res.StatusCode)}
	}
	resp := map[string]any{}
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &resp)
	}
	if success, ok := resp["success"].(bool); ok && !success {
		return "", &WkError{Message: "上传知识文件失败",
			Err: fmt.Errorf("%s", firstNonEmpty(StringOf(resp, "message"), StringOf(resp, "error")))}
	}
	doc := unwrapData(resp)
	return firstNonEmpty(StringOf(doc, "id"), StringOf(doc, "knowledge_id")), nil
}

// DeleteKnowledge 删单条知识（DELETE /knowledge/{knowledgeId}；删除异步）。
func (c *HTTPClient) DeleteKnowledge(ctx context.Context, knowledgeID string) error {
	_, err := c.doKBJSON(ctx, http.MethodDelete, "/knowledge/"+url.PathEscape(knowledgeID), nil)
	return err
}

// kbItemID 列表条目 id（id / knowledge_id 兼容）。
func kbItemID(m map[string]any) string {
	return firstNonEmpty(StringOf(m, "id"), StringOf(m, "knowledge_id"))
}

// kbBaseName 列表条目文件名（file_name/fileName/title 兼容取值；可能带 folder_path 前缀，取末段基名）。
func kbBaseName(m map[string]any) string {
	raw := strings.TrimSpace(firstNonEmpty(StringOf(m, "file_name"), StringOf(m, "fileName"), StringOf(m, "title")))
	if i := strings.LastIndexByte(raw, '/'); i >= 0 {
		raw = raw[i+1:]
	}
	return raw
}

// kbConflictIDs 409 响应里已有条目 id（data 形态未实测：单对象 / 数组 / {items|list} 列表均兼容）。
func kbConflictIDs(data any) []string {
	var arr []any
	switch t := data.(type) {
	case nil:
		return nil
	case []any:
		arr = t
	case map[string]any:
		if inner, ok := asAnySlice(t["items"]); ok {
			arr = inner
		} else if inner, ok := asAnySlice(t["list"]); ok {
			arr = inner
		} else {
			arr = []any{t}
		}
	default:
		return nil
	}
	var out []string
	for _, it := range arr {
		switch k := it.(type) {
		case map[string]any:
			if id := kbItemID(k); id != "" {
				out = append(out, id)
			}
		case string:
			if k != "" {
				out = append(out, k)
			}
		case float64:
			out = append(out, strconv.FormatInt(int64(k), 10))
		}
	}
	return out
}

// escapeQuotes Content-Disposition 文件名引号转义。
func escapeQuotes(s string) string {
	return strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s)
}
