package callme

// WeKnora HTTP 客户端测试：运行时配置解析、密钥 fail-closed、知识库三接口。
// 仅 httptest / 配置桩，不访问真实外部服务。

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
)

// varCfg 可变配置桩（验证每次请求运行时解析）。
type varCfg struct {
	mu sync.Mutex
	m  map[string]string
}

func (c *varCfg) Get(_ context.Context, key string) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.m[key], nil
}

func (c *varCfg) set(key, val string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.m[key] = val
}

func agentCfg(url string) *varCfg {
	return &varCfg{m: map[string]string{
		"WEKNORA_API_URL":   url,
		"WEKNORA_API_KEY":   "key-1",
		"WEKNORA_AGENT_ID":  "agent-1",
		"WEKNORA_SYNC_USER": "syncer",
	}}
}

func TestHTTPClientFailClosed(t *testing.T) {
	ctx := context.Background()

	// 全空：agent 侧与知识库侧都拒绝（不带空密钥外呼）
	c := NewHTTPClient(&varCfg{m: map[string]string{}})
	if _, err := c.ListModels(ctx, "u"); !isNotConfigured(err) {
		t.Fatalf("缺配置应 NotConfigured: %v", err)
	}
	if _, err := c.ListFolderKnowledge(ctx, "kb", "2026/10.23"); !isNotConfigured(err) {
		t.Fatalf("缺配置应 NotConfigured: %v", err)
	}

	// 缺 agent：agent 侧拒绝；知识库侧只要求 API key，可放行
	c = NewHTTPClient(&varCfg{m: map[string]string{
		"WEKNORA_API_URL": "http://127.0.0.1:1", "WEKNORA_API_KEY": "k",
	}})
	if _, err := c.ListModels(ctx, "u"); !isNotConfigured(err) {
		t.Fatalf("缺 agent 应 NotConfigured: %v", err)
	}

	// 缺 API key：一律拒绝
	c = NewHTTPClient(&varCfg{m: map[string]string{
		"WEKNORA_API_URL": "http://127.0.0.1:1", "WEKNORA_AGENT_ID": "a",
	}})
	if _, err := c.CreateSession(ctx, "u", "t"); !isNotConfigured(err) {
		t.Fatalf("缺 API key 应 NotConfigured: %v", err)
	}
}

func isNotConfigured(err error) bool {
	var wk *WkError
	return errors.As(err, &wk) && wk.Expose
}

func TestHTTPClientRuntimeConfig(t *testing.T) {
	var gotKey, gotUser string
	hit := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hit++
		gotKey, gotUser = r.Header.Get("X-API-Key"), r.Header.Get("X-External-User-ID")
		_ = json.NewEncoder(w).Encode(map[string]any{"data": []any{}})
	}))
	defer srv.Close()

	cfg := agentCfg(srv.URL)
	c := NewHTTPClient(cfg)
	if _, err := c.ListModels(context.Background(), "u1"); err != nil {
		t.Fatalf("ListModels: %v", err)
	}
	if gotKey != "key-1" || gotUser != "u1" || hit != 1 {
		t.Fatalf("请求头/次数不符: key=%q user=%q hit=%d", gotKey, gotUser, hit)
	}

	// 配置热更：换 API key 后下一次请求即生效（每次请求运行时解析）
	cfg.set("WEKNORA_API_KEY", "key-2")
	if _, err := c.ListModels(context.Background(), "u1"); err != nil {
		t.Fatalf("ListModels: %v", err)
	}
	if gotKey != "key-2" {
		t.Fatalf("API key 应运行时解析: %q", gotKey)
	}
}

func TestListFolderKnowledge(t *testing.T) {
	var paths []string
	page := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.RawQuery)
		page++
		items := []any{}
		n := 1 // 第二页 1 条（<100 停止翻页）
		if page == 1 {
			n = 100 // 第一页满页触发翻页
		}
		for i := 0; i < n; i++ {
			items = append(items, map[string]any{
				"id": fmt.Sprintf("k%d-%d", page, i), "file_name": "2026/10.23/纪要_2026-10-23.md",
			})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "data": map[string]any{"items": items}})
	}))
	defer srv.Close()

	c := NewHTTPClient(agentCfg(srv.URL))
	list, err := c.ListFolderKnowledge(context.Background(), "kb-1", "2026/10.23")
	if err != nil {
		t.Fatalf("ListFolderKnowledge: %v", err)
	}
	if len(list) != 101 {
		t.Fatalf("应翻页共 101 条: %d", len(list))
	}
	if list[0].ID != "k1-0" || list[0].Name != "纪要_2026-10-23.md" {
		t.Fatalf("条目取值不符: %+v", list[0])
	}
	for _, q := range paths {
		if !strings.Contains(q, "folder_path=2026%2F10.23") || !strings.Contains(q, "folder_recursive=false") {
			t.Fatalf("查询参数不符: %s", q)
		}
	}
	if len(paths) != 2 || !strings.Contains(paths[0], "page=1") || !strings.Contains(paths[1], "page=2") {
		t.Fatalf("分页参数不符: %v", paths)
	}
}

func TestUploadKnowledgeFile(t *testing.T) {
	var gotPath, gotDisposition, gotContentType string
	var fields map[string]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		if err := r.ParseMultipartForm(64 << 20); err != nil {
			t.Errorf("ParseMultipartForm: %v", err)
			return
		}
		fields = map[string]string{}
		for k, vs := range r.MultipartForm.Value {
			if len(vs) > 0 {
				fields[k] = vs[0]
			}
		}
		if fhs := r.MultipartForm.File["file"]; len(fhs) > 0 {
			gotDisposition = fhs[0].Header.Get("Content-Disposition")
			gotContentType = fhs[0].Header.Get("Content-Type")
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"success": true, "data": map[string]any{"knowledge_id": "k-9"},
		})
	}))
	defer srv.Close()

	c := NewHTTPClient(agentCfg(srv.URL))
	id, err := c.UploadKnowledgeFile(context.Background(), "kb-1",
		"2026/10.23/报表.xlsx", "报表.xlsx", "chatlog", []byte("BIN"))
	if err != nil || id != "k-9" {
		t.Fatalf("UploadKnowledgeFile: id=%q err=%v", id, err)
	}
	if gotPath != "/knowledge-bases/kb-1/knowledge/file" {
		t.Fatalf("路径不符: %s", gotPath)
	}
	if fields["fileName"] != "2026/10.23/报表.xlsx" || fields["title"] != "报表.xlsx" || fields["channel"] != "chatlog" {
		t.Fatalf("表单字段不符: %v", fields)
	}
	if !strings.Contains(gotDisposition, `filename="报表.xlsx"`) {
		t.Fatalf("文件名不符: %s", gotDisposition)
	}
	if gotContentType != "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet" {
		t.Fatalf("xlsx Content-Type 不符: %s", gotContentType)
	}
}

func TestUploadKnowledgeConflict(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(`{"success":false,"data":[{"id":"c1"},{"knowledge_id":"c2"}]}`))
	}))
	defer srv.Close()

	c := NewHTTPClient(agentCfg(srv.URL))
	_, err := c.UploadKnowledgeFile(context.Background(), "kb-1", "2026/10.23/a.md", "a.md", "chatlog", []byte("x"))
	var ce *KnowledgeConflictError
	if !errors.As(err, &ce) {
		t.Fatalf("409 应返回 KnowledgeConflictError: %v", err)
	}
	if len(ce.IDs) != 2 || ce.IDs[0] != "c1" || ce.IDs[1] != "c2" {
		t.Fatalf("冲突 id 不符: %v", ce.IDs)
	}
}

func TestDeleteKnowledge(t *testing.T) {
	var gotPath, gotUser string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotUser = r.URL.Path, r.Header.Get("X-External-User-ID")
		_ = json.NewEncoder(w).Encode(map[string]any{"success": true})
	}))
	defer srv.Close()

	c := NewHTTPClient(agentCfg(srv.URL))
	if err := c.DeleteKnowledge(context.Background(), "k1"); err != nil {
		t.Fatalf("DeleteKnowledge: %v", err)
	}
	if gotPath != "/knowledge/k1" {
		t.Fatalf("路径不符: %s", gotPath)
	}
	if gotUser != "syncer" {
		t.Fatalf("知识库接口应带 WEKNORA_SYNC_USER: %q", gotUser)
	}
}
