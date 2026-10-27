package callme

// /ingest-chat 群内提问直推与 SSE collectAnswer 归一化测试。
// fake WeKnora + fake DB，不访问真实外部服务。

import (
	"context"
	"database/sql/driver"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// fakeNamer Dify WF3 文件名桩（记录调用）。
type fakeNamer struct {
	got struct{ title, query, answer string }
	ret string
}

func (f *fakeNamer) NameFile(_ context.Context, _ string, sessionTitle, query, answer string) string {
	f.got.title, f.got.query, f.got.answer = sessionTitle, query, answer
	return f.ret
}

// fixedPermit 按 appKey 决定放行。
type fixedPermit map[string]bool

func (p fixedPermit) Allow(_ context.Context, _ int64, appKey string) (bool, error) {
	return p[appKey], nil
}

// fixedCfg 配置桩。
type fixedCfg map[string]string

func (c fixedCfg) Get(_ context.Context, key string) (string, error) { return c[key], nil }

func newIngestEnv(t *testing.T) (*Module, *fakeDB, *fakeWeKnora) {
	t.Helper()
	db, fdb := openFake(t)
	wk := &fakeWeKnora{models: []Model{{ID: "m1", Name: "模型一", Type: "KnowledgeQA"}}}
	m := &Module{
		DB:          db,
		WeKnora:     wk,
		Permit:      fixedPermit{"call-me": true},
		IngestToken: "tok-1",
	}
	return m, fdb, wk
}

func postIngest(t *testing.T, m *Module, body map[string]any, token string) (int, ingestResponse) {
	t.Helper()
	raw, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/callme/ingest-chat", strings.NewReader(string(raw)))
	if token != "" {
		req.Header.Set("X-Ingest-Token", token)
	}
	rec := httptest.NewRecorder()
	m.handleIngestChat(rec, req)
	var resp ingestResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	return rec.Code, resp
}

func TestIngestTokenGate(t *testing.T) {
	m, _, _ := newIngestEnv(t)

	m.IngestToken = ""
	code, resp := postIngest(t, m, map[string]any{"wxid": "w", "query": "q", "session_title": "t"}, "")
	if code != http.StatusServiceUnavailable || resp.Code != "ingest_token_missing" {
		t.Fatalf("未配置密钥应 503 ingest_token_missing: %d %+v", code, resp)
	}

	m.IngestToken = "tok-1"
	code, resp = postIngest(t, m, map[string]any{"wxid": "w", "query": "q", "session_title": "t"}, "bad")
	if code != http.StatusUnauthorized || resp.Code != "ingest_token_invalid" {
		t.Fatalf("密钥错误应 401 ingest_token_invalid: %d %+v", code, resp)
	}
}

func TestIngestParamAndUser(t *testing.T) {
	m, fdb, _ := newIngestEnv(t)

	code, resp := postIngest(t, m, map[string]any{"wxid": "", "query": "q", "session_title": "t"}, "tok-1")
	if code != http.StatusOK || resp.Code != "param_invalid" {
		t.Fatalf("缺参应 param_invalid: %d %+v", code, resp)
	}

	// wxid 未绑定。
	fdb.on("FROM sys_user WHERE wxid", []string{"id", "username"}, nil)
	_, resp = postIngest(t, m, map[string]any{"wxid": "w1", "query": "q", "session_title": "t"}, "tok-1")
	if resp.Code != "user_not_found" || resp.OK {
		t.Fatalf("未绑定应 user_not_found: %+v", resp)
	}

	// wxid 多绑（0 命中已覆盖；多行 → 绑定多个）。
	fdb.on("FROM sys_user WHERE wxid", []string{"id", "username"}, [][]driver.Value{
		{int64(1), "a"}, {int64(2), "b"},
	})
	_, resp = postIngest(t, m, map[string]any{"wxid": "w1", "query": "q", "session_title": "t"}, "tok-1")
	if resp.Code != "user_not_found" || !strings.Contains(resp.Message, "多个") {
		t.Fatalf("多绑应 user_not_found「多个」: %+v", resp)
	}
}

func TestIngestPermissionAndSuccess(t *testing.T) {
	m, fdb, wk := newIngestEnv(t)
	wk.sessions = []map[string]any{{"id": "s-old", "title": "别名", "updated_at": "2026-01-01"}}
	wk.stream = strings.Join([]string{
		`data:{"response_type":"thinking","content":"思考中"}`,
		``,
		`data:{"response_type":"answer","content":"第一段"}`,
		``,
		`data:{"response_type":"answer","content":"第二段","done":true,"request_id":"r9"}`,
		``,
	}, "\n")
	fdb.on("FROM sys_user WHERE wxid", []string{"id", "username"}, [][]driver.Value{{int64(7), "zhang"}})
	_ = fdb

	// 无 call-me 权限。
	m.Permit = fixedPermit{"call-me": false}
	_, resp := postIngest(t, m, map[string]any{"wxid": "w1", "query": "你好", "session_title": "班组问答"}, "tok-1")
	if resp.Code != "no_permission" {
		t.Fatalf("无权限应 no_permission: %+v", resp)
	}

	// 成功：同名会话不存在 → 新建；Dify WF3 命名器注入。
	m.Permit = fixedPermit{"call-me": true}
	namer := &fakeNamer{ret: "班组问答-001.md"}
	m.Namer = namer
	code, resp := postIngest(t, m, map[string]any{"wxid": "w1", "query": "你好", "session_title": "班组问答"}, "tok-1")
	if code != http.StatusOK || !resp.OK {
		t.Fatalf("应成功: %d %+v", code, resp)
	}
	if resp.Data["answer"] != "第一段第二段" {
		t.Fatalf("回答拼接错误: %v", resp.Data["answer"])
	}
	if resp.Data["session_title"] != "班组问答" || resp.Data["file_name"] != "班组问答-001.md" {
		t.Fatalf("会话/文件名错误: %+v", resp.Data)
	}
	if namer.got.answer != "第一段第二段" {
		t.Fatalf("namer 入参错误: %+v", namer.got)
	}

	// 同名会话已存在 → 复用（updated_at 最新者）。
	wk.sessions = []map[string]any{
		{"id": "s-1", "title": "班组问答", "updated_at": "2026-01-01"},
		{"id": "s-2", "title": "班组问答", "updated_at": "2026-06-01"},
	}
	_, resp = postIngest(t, m, map[string]any{"wxid": "w1", "query": "你好", "session_title": "班组问答"}, "tok-1")
	if !resp.OK || resp.Data["session_id"] != "s-2" {
		t.Fatalf("应复用最新同名会话 s-2: %+v", resp)
	}
}

func TestIngestModelHintAndFiles(t *testing.T) {
	m, fdb, wk := newIngestEnv(t)
	fdb.on("FROM sys_user WHERE wxid", []string{"id", "username"}, [][]driver.Value{{int64(7), "zhang"}})

	// 模型提示未命中 → model_not_found 附 models（候选须含 ASCII 才视为提示）。
	_, resp := postIngest(t, m, map[string]any{
		"wxid": "w1", "query": "请使用 no-such-model 回答", "session_title": "t",
	}, "tok-1")
	if resp.Code != "model_not_found" || len(resp.Models) != 1 || resp.Models[0]["id"] != "m1" {
		t.Fatalf("应 model_not_found 附模型表: %+v", resp)
	}

	// 模型提示命中 → summary_model_id 透传。
	_, resp = postIngest(t, m, map[string]any{
		"wxid": "w1", "query": "请使用 m1 回答", "session_title": "t",
	}, "tok-1")
	if !resp.OK {
		t.Fatalf("应成功: %+v", resp)
	}
	if len(wk.chatReqs) == 0 || wk.chatReqs[len(wk.chatReqs)-1].ModelID != "m1" {
		t.Fatalf("model_id 未透传: %+v", wk.chatReqs)
	}
	if resp.Data["model_used"] == nil {
		t.Fatalf("model_used 缺失: %+v", resp.Data)
	}

	// 图片格式/大小校验。
	_, resp = postIngest(t, m, map[string]any{
		"wxid": "w1", "query": "q", "session_title": "t", "image": "data:text/plain;base64,aGk=",
	}, "tok-1")
	if resp.Code != "unsupported_file" {
		t.Fatalf("坏图片应 unsupported_file: %+v", resp)
	}
	big := "data:image/png;base64," + base64.StdEncoding.EncodeToString(make([]byte, 9<<20))
	_, resp = postIngest(t, m, map[string]any{
		"wxid": "w1", "query": "q", "session_title": "t", "image": big,
	}, "tok-1")
	if resp.Code != "file_too_large" {
		t.Fatalf("超大图片应 file_too_large: %+v", resp)
	}

	// 附件：格式白名单 + 空内容。
	_, resp = postIngest(t, m, map[string]any{
		"wxid": "w1", "query": "q", "session_title": "t",
		"file": map[string]any{"name": "run.exe", "data_base64": base64.StdEncoding.EncodeToString([]byte("x"))},
	}, "tok-1")
	if resp.Code != "unsupported_file" || !strings.Contains(resp.Message, "不支持") {
		t.Fatalf("exe 应 unsupported_file: %+v", resp)
	}

	// 附件合法 → 预上传并随对话发送。
	_, resp = postIngest(t, m, map[string]any{
		"wxid": "w1", "query": "q", "session_title": "t",
		"file": map[string]any{"name": "资料.pdf", "data_base64": base64.StdEncoding.EncodeToString([]byte("pdf"))},
	}, "tok-1")
	if !resp.OK {
		t.Fatalf("附件应成功: %+v", resp)
	}
	if len(wk.uploaded) == 0 || wk.uploaded[0].filename != "资料.pdf" {
		t.Fatalf("附件未上传: %+v", wk.uploaded)
	}
	last := wk.chatReqs[len(wk.chatReqs)-1]
	if len(last.AttachmentIDs) != 1 || last.AttachmentIDs[0] != "att-1" {
		t.Fatalf("附件未随对话发送: %+v", last)
	}
}

// ===== collectAnswer 归一化 =====

func TestCollectAnswerPreambleAndTools(t *testing.T) {
	wk := &fakeWeKnora{stream: strings.Join([]string{
		`data:{"response_type":"answer","content":"过渡语"}`,
		``,
		`data:{"response_type":"tool_call","data":{"tool_call_id":"t1","tool_name":"web_search"}}`,
		``,
		`data:{"response_type":"tool_call","data":{"tool_call_id":"t1","tool_name":"web_search","arguments":{"query":"电阻"}}}`,
		``,
		`data:{"response_type":"tool_result","response_type":"tool_result","data":{"tool_call_id":"t1","tool_name":"web_search","success":true,"count":3},"response_type":"tool_result"}`,
		``,
		`data:{"response_type":"answer","content":"最终答案","done":true,"request_id":"r7"}`,
		``,
	}, "\n")}
	// 修正 tool_result 帧（上面重复键由 JSON 解析取后者，这里换干净文本）。
	wk.stream = strings.Join([]string{
		`data:{"response_type":"answer","content":"过渡语"}`,
		``,
		`data:{"response_type":"tool_call","data":{"tool_call_id":"t1","tool_name":"web_search"}}`,
		``,
		`data:{"response_type":"tool_call","data":{"tool_call_id":"t1","tool_name":"web_search","arguments":{"query":"电阻"}}}`,
		``,
		`data:{"response_type":"tool_result","data":{"tool_call_id":"t1","tool_name":"web_search","success":true,"count":3}}`,
		``,
		`data:{"response_type":"answer","content":"最终答案","done":true,"request_id":"r7"}`,
		``,
	}, "\n")

	var events []ChatEvent
	res, err := CollectAnswer(context.Background(), wk, "u1", "s1",
		ChatRequest{Query: "问"}, func(ev ChatEvent) { events = append(events, ev) })
	if err != nil {
		t.Fatalf("CollectAnswer 失败: %v", err)
	}
	if res.Answer != "最终答案" || res.RequestID != "r7" {
		t.Fatalf("回答/请求 id 错误: %+v", res)
	}
	types := map[string]int{}
	var toolUpdates []ChatEvent
	for _, ev := range events {
		types[ev.Type]++
		if ev.Type == "tool_start" {
			toolUpdates = append(toolUpdates, ev)
		}
	}
	if types["preamble"] != 1 {
		t.Fatalf("应有一次 preamble（过渡语挪入思考区）: %+v", events)
	}
	if types["tool_start"] != 2 || types["tool_end"] != 1 {
		t.Fatalf("tool_call 双帧应 2 次 tool_start（第二次 update）: %+v", events)
	}
	if !toolUpdates[1].Update {
		t.Fatalf("第二帧 tool_call 应 update=true: %+v", toolUpdates)
	}
	if types["done"] != 1 {
		t.Fatalf("应有 done 事件")
	}
}

func TestCollectAnswerErrorFrame(t *testing.T) {
	wk := &fakeWeKnora{stream: `data:{"response_type":"error","content":"上游炸了"}` + "\n\n"}
	_, err := CollectAnswer(context.Background(), wk, "u1", "s1", ChatRequest{Query: "问"}, nil)
	if err == nil || !strings.Contains(err.Error(), "上游炸了") && !strings.Contains(err.Error(), "AI 服务返回错误") {
		t.Fatalf("回答级 error 应判失败: %v", err)
	}
}
