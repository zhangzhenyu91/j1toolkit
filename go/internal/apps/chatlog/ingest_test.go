package chatlog

// /ingest 每日归档直推测试：fake 知识库客户端 + httptest，不访问真实外部服务。

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"shade/internal/apps/callme"
)

// fixedCfg 配置桩。
type fixedCfg map[string]string

func (c fixedCfg) Get(_ context.Context, key string) (string, error) { return c[key], nil }

// kbUpload 上传记录。
type kbUpload struct {
	fileName, title, channel string
	size                     int
}

// fakeKB WeKnora 知识库客户端桩（记录调用，上传错误按次序弹出）。
type fakeKB struct {
	mu        sync.Mutex
	items     []callme.KnowledgeItem
	uploads   []kbUpload
	deletes   []string
	uploadErr []error // 按次序返回；耗尽后成功返回 "k-new"
	listErr   error
}

func (f *fakeKB) ListFolderKnowledge(context.Context, string, string) ([]callme.KnowledgeItem, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.listErr != nil {
		return nil, f.listErr
	}
	return f.items, nil
}

func (f *fakeKB) UploadKnowledgeFile(_ context.Context, _, fileName, title, channel string, buf []byte) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.uploads = append(f.uploads, kbUpload{fileName, title, channel, len(buf)})
	if len(f.uploadErr) > 0 {
		err := f.uploadErr[0]
		f.uploadErr = f.uploadErr[1:]
		if err != nil {
			return "", err
		}
	}
	return "k-new", nil
}

func (f *fakeKB) DeleteKnowledge(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deletes = append(f.deletes, id)
	return nil
}

func newEnv(t *testing.T) (*Module, *fakeKB) {
	t.Helper()
	kb := &fakeKB{}
	m := New(fixedCfg{
		"CHATLOG_INGEST_TOKEN":  "tok-1",
		"WEKNORA_API_KEY":       "wk-key",
		"WEKNORA_CHATLOG_KB_ID": "kb-1",
	}, kb)
	return m, kb
}

func postIngest(t *testing.T, m *Module, body any, token string) (int, ingestResponse) {
	t.Helper()
	var reader *strings.Reader
	switch b := body.(type) {
	case string:
		reader = strings.NewReader(b)
	default:
		raw, _ := json.Marshal(body)
		reader = strings.NewReader(string(raw))
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/chatlog/ingest", reader)
	if token != "" {
		req.Header.Set("X-Ingest-Token", token)
	}
	rec := httptest.NewRecorder()
	m.handleIngest(rec, req)
	var resp ingestResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	return rec.Code, resp
}

func TestIngestTokenGate(t *testing.T) {
	m, _ := newEnv(t)

	m.Cfg = fixedCfg{"WEKNORA_API_KEY": "wk-key", "WEKNORA_CHATLOG_KB_ID": "kb-1"}
	code, resp := postIngest(t, m, map[string]any{"date": "2026-10-23"}, "")
	if code != http.StatusServiceUnavailable || resp.Code != "ingest_token_missing" {
		t.Fatalf("未配置密钥应 503 ingest_token_missing: %d %+v", code, resp)
	}

	m.Cfg = fixedCfg{"CHATLOG_INGEST_TOKEN": "tok-1", "WEKNORA_API_KEY": "wk-key", "WEKNORA_CHATLOG_KB_ID": "kb-1"}
	code, resp = postIngest(t, m, map[string]any{"date": "2026-10-23"}, "bad")
	if code != http.StatusUnauthorized || resp.Code != "ingest_token_invalid" {
		t.Fatalf("密钥错误应 401 ingest_token_invalid: %d %+v", code, resp)
	}
}

func TestIngestWeKnoraGate(t *testing.T) {
	m, _ := newEnv(t)

	m.Cfg = fixedCfg{"CHATLOG_INGEST_TOKEN": "tok-1", "WEKNORA_CHATLOG_KB_ID": "kb-1"}
	code, resp := postIngest(t, m, map[string]any{"date": "2026-10-23"}, "tok-1")
	if code != http.StatusServiceUnavailable || resp.Code != "weknora_not_configured" {
		t.Fatalf("缺 API key 应 503 weknora_not_configured: %d %+v", code, resp)
	}

	m.Cfg = fixedCfg{"CHATLOG_INGEST_TOKEN": "tok-1", "WEKNORA_API_KEY": "wk-key"}
	code, resp = postIngest(t, m, map[string]any{"date": "2026-10-23"}, "tok-1")
	if code != http.StatusServiceUnavailable || resp.Code != "weknora_not_configured" {
		t.Fatalf("缺知识库 ID 应 503 weknora_not_configured: %d %+v", code, resp)
	}

	m.KB = nil
	m.Cfg = fixedCfg{"CHATLOG_INGEST_TOKEN": "tok-1", "WEKNORA_API_KEY": "wk-key", "WEKNORA_CHATLOG_KB_ID": "kb-1"}
	code, resp = postIngest(t, m, map[string]any{"date": "2026-10-23"}, "tok-1")
	if code != http.StatusServiceUnavailable || resp.Code != "weknora_not_configured" {
		t.Fatalf("未接知识库客户端应 503 weknora_not_configured: %d %+v", code, resp)
	}
}

func TestParseBodyErrors(t *testing.T) {
	big := base64.StdEncoding.EncodeToString(make([]byte, fileMaxBytes+1))
	cases := []struct {
		name       string
		body       map[string]any
		code       string
		httpStatus int
	}{
		{"date 缺失", map[string]any{"group_name": "g", "kind": "digest", "digest_md": "x"}, "param_invalid", 400},
		{"date 格式", map[string]any{"date": "2026-1-3", "group_name": "g", "kind": "digest", "digest_md": "x"}, "param_invalid", 400},
		{"date 非有效日期", map[string]any{"date": "2026-02-30", "group_name": "g", "kind": "digest", "digest_md": "x"}, "param_invalid", 400},
		{"group_name 空", map[string]any{"date": "2026-10-23", "kind": "digest", "digest_md": "x"}, "param_invalid", 400},
		{"digest_md 空", map[string]any{"date": "2026-10-23", "group_name": "g", "kind": "digest", "digest_md": "  "}, "param_invalid", 400},
		{"kind 非法", map[string]any{"date": "2026-10-23", "group_name": "g", "kind": "other"}, "unsupported_kind", 400},
		{"file.name 空", map[string]any{"date": "2026-10-23", "group_name": "g", "kind": "file",
			"file": map[string]any{"name": "", "data_base64": "aGk="}}, "param_invalid", 400},
		{"扩展名白名单外", map[string]any{"date": "2026-10-23", "group_name": "g", "kind": "file",
			"file": map[string]any{"name": "a.exe", "data_base64": "aGk="}}, "unsupported_file", 200},
		{"无扩展名", map[string]any{"date": "2026-10-23", "group_name": "g", "kind": "file",
			"file": map[string]any{"name": "abc", "data_base64": "aGk="}}, "unsupported_file", 200},
		{"base64 空", map[string]any{"date": "2026-10-23", "group_name": "g", "kind": "file",
			"file": map[string]any{"name": "a.md", "data_base64": " "}}, "param_invalid", 400},
		{"base64 格式", map[string]any{"date": "2026-10-23", "group_name": "g", "kind": "file",
			"file": map[string]any{"name": "a.md", "data_base64": "aGk"}}, "param_invalid", 400},
		{"解码为空", map[string]any{"date": "2026-10-23", "group_name": "g", "kind": "file",
			"file": map[string]any{"name": "a.md", "data_base64": ""}}, "param_invalid", 400},
		{"超 30MB", map[string]any{"date": "2026-10-23", "group_name": "g", "kind": "file",
			"file": map[string]any{"name": "a.md", "data_base64": big}}, "file_too_large", 400},
	}
	for _, tc := range cases {
		item, perr := parseBody(tc.body)
		if item != nil || perr == nil {
			t.Fatalf("%s：应判非法", tc.name)
		}
		if perr.code != tc.code || perr.httpStatus != tc.httpStatus {
			t.Fatalf("%s：应 %s/%d，实际 %s/%d", tc.name, tc.code, tc.httpStatus, perr.code, perr.httpStatus)
		}
	}
}

func TestIngestDigest(t *testing.T) {
	m, kb := newEnv(t)
	code, resp := postIngest(t, m, map[string]any{
		"date": "2026-10-23", "group_name": "检修一班群", "kind": "digest", "digest_md": "# 纪要\n内容",
	}, "tok-1")
	if code != http.StatusOK || !resp.OK {
		t.Fatalf("纪要应成功: %d %+v", code, resp)
	}
	data := resp.Data
	if data["file_name"] != "检修一班群_2026-10-23.md" || data["folder"] != "2026/10.23" ||
		data["knowledge_id"] != "k-new" || data["replaced"] != float64(0) {
		t.Fatalf("响应字段不符: %+v", data)
	}
	if len(kb.uploads) != 1 {
		t.Fatalf("应上传 1 次: %+v", kb.uploads)
	}
	up := kb.uploads[0]
	if up.fileName != "2026/10.23/检修一班群_2026-10-23.md" || up.title != "检修一班群_2026-10-23.md" ||
		up.channel != "chatlog" || up.size != len("# 纪要\n内容") {
		t.Fatalf("上传参数不符: %+v", up)
	}
}

func TestIngestFileAndSafeName(t *testing.T) {
	m, kb := newEnv(t)
	// 群名/文档名净化非法字符（含 / 防路径逃逸）；date 零填充合法值 → 文件夹月.日去前导零
	code, resp := postIngest(t, m, map[string]any{
		"date": "2026-10-03", "group_name": `a/b:c*d?e"f<g>h|i`, "kind": "file",
		"file": map[string]any{"name": "日 志/报表.md", "data_base64": base64.StdEncoding.EncodeToString([]byte("hi"))},
	}, "tok-1")
	if code != http.StatusOK || !resp.OK {
		t.Fatalf("文档应成功: %d %+v", code, resp)
	}
	// 月.日不带前导零：2026-10-03 → 10.3
	if resp.Data["folder"] != "2026/10.3" || resp.Data["file_name"] != "日 志_报表.md" {
		t.Fatalf("文件夹/文件名不符: %+v", resp.Data)
	}
	if kb.uploads[0].fileName != "2026/10.3/日 志_报表.md" {
		t.Fatalf("上传路径不符: %+v", kb.uploads[0])
	}
}

func TestUpsertReplacesSameName(t *testing.T) {
	m, kb := newEnv(t)
	kb.items = []callme.KnowledgeItem{
		{ID: "k-1", Name: "g_2026-10-23.md"},
		{ID: "k-2", Name: "g_2026-10-23.md"},
		{ID: "k-3", Name: "other.md"},
	}
	_, resp := postIngest(t, m, map[string]any{
		"date": "2026-10-23", "group_name": "g", "kind": "digest", "digest_md": "x",
	}, "tok-1")
	if !resp.OK || resp.Data["replaced"] != float64(2) {
		t.Fatalf("同名先删后传 replaced=2: %+v", resp)
	}
	if len(kb.deletes) != 2 || kb.deletes[0] != "k-1" || kb.deletes[1] != "k-2" {
		t.Fatalf("应删同名两条: %+v", kb.deletes)
	}
}

func TestUpsertConflictRetry(t *testing.T) {
	m, kb := newEnv(t)
	kb.uploadErr = []error{&callme.KnowledgeConflictError{IDs: []string{"k-old"}}, nil}
	_, resp := postIngest(t, m, map[string]any{
		"date": "2026-10-23", "group_name": "g", "kind": "digest", "digest_md": "x",
	}, "tok-1")
	if !resp.OK || resp.Data["replaced"] != float64(1) || resp.Data["file_name"] != "g_2026-10-23.md" {
		t.Fatalf("409 删旧重试应成功: %+v", resp)
	}
	if len(kb.deletes) != 1 || kb.deletes[0] != "k-old" || len(kb.uploads) != 2 {
		t.Fatalf("应删已有 id 并重传: deletes=%v uploads=%v", kb.deletes, kb.uploads)
	}
}

func TestUpsertConflictRename(t *testing.T) {
	m, kb := newEnv(t)
	kb.uploadErr = []error{
		&callme.KnowledgeConflictError{IDs: []string{"k-a"}},
		&callme.KnowledgeConflictError{IDs: []string{"k-b"}},
		nil,
	}
	_, resp := postIngest(t, m, map[string]any{
		"date": "2026-10-23", "group_name": "g", "kind": "digest", "digest_md": "x",
	}, "tok-1")
	if !resp.OK || resp.Data["file_name"] != "g_2026-10-23-1.md" {
		t.Fatalf("再冲突应追加 -1 后缀: %+v", resp)
	}
	if len(kb.uploads) != 3 || kb.uploads[2].fileName != "2026/10.23/g_2026-10-23-1.md" {
		t.Fatalf("第三次应改名上传: %+v", kb.uploads)
	}
	// replaced 口径同 JS：dupIds + 首次 409 已删 id（第二次 409 不删、改名后缀不计）
	if resp.Data["replaced"] != float64(1) {
		t.Fatalf("replaced 应为首次冲突删除数: %+v", resp.Data)
	}
}

func TestUpsertUpstreamError(t *testing.T) {
	m, kb := newEnv(t)
	kb.listErr = errors.New("boom")
	code, resp := postIngest(t, m, map[string]any{
		"date": "2026-10-23", "group_name": "g", "kind": "digest", "digest_md": "x",
	}, "tok-1")
	if code != http.StatusBadGateway || resp.Code != "upstream_error" ||
		!strings.Contains(resp.Message, "写入知识库失败") {
		t.Fatalf("上游失败应 502 upstream_error: %d %+v", code, resp)
	}
}

func TestHelpers(t *testing.T) {
	if got := safeFileName(`a/b\c:d*e?f"g<h>i|j`); got != "a_b_c_d_e_f_g_h_i_j" {
		t.Fatalf("safeFileName: %q", got)
	}
	if got := safeFileName(" \t\n "); got != "未命名" {
		t.Fatalf("空名应落 未命名: %q", got)
	}
	seg := folderSegments("2026-10-03")
	if seg[0] != "2026" || seg[1] != "10.3" {
		t.Fatalf("folderSegments: %v", seg)
	}
	if got := renameWithSuffix("a.md", 1); got != "a-1.md" {
		t.Fatalf("renameWithSuffix: %q", got)
	}
	if got := renameWithSuffix("noext", 1); got != "noext-1" {
		t.Fatalf("renameWithSuffix 无扩展名: %q", got)
	}
}
