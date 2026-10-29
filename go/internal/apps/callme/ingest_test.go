package callme

// /ingest-chat 群内提问直推与 SSE collectAnswer 归一化测试。
// fake WeKnora + fake DB，不访问真实外部服务。

import (
	"context"
	"database/sql/driver"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"shade/internal/auth"
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

// ===== 步骤树文案与形状（agentsteps.js 口径） =====

func TestToolTitlesMatchAgentSteps(t *testing.T) {
	if got := PendingTitle("web_search"); got != "正在调用 网络搜索…" {
		t.Fatalf("进行中标题不符: %q", got)
	}
	if got := PendingTitle("shell_exec"); got != "正在执行沙箱命令…" {
		t.Fatalf("进行中特殊文案不符: %q", got)
	}
	// 完成态标题：搜索类带查询词（官方「：「query」」口径）。
	if got := DoneTitle("web_search", map[string]any{"query": "电阻"}, nil, true); got != "网络搜索：「电阻」" {
		t.Fatalf("完成态标题不符: %q", got)
	}
	if got := DoneTitle("knowledge_search", nil, map[string]any{"query": "带电"}, true); got != "检索知识库：「带电」" {
		t.Fatalf("知识库完成态标题不符: %q", got)
	}
	// 失败口径：专属文案 +「失败」；直显族与兜底为「调用 {名} 失败」。
	if got := DoneTitle("web_search", nil, nil, false); got != "网络搜索失败" {
		t.Fatalf("失败标题不符: %q", got)
	}
	if got := DoneTitle("shell_exec", nil, nil, false); got != "调用 执行沙箱命令 失败" {
		t.Fatalf("直显族失败标题不符: %q", got)
	}
	// 摘要文案。
	if got := ToolSummary("web_search", map[string]any{"count": float64(3)}); got != "找到 3 条网页" {
		t.Fatalf("web_search 摘要不符: %q", got)
	}
	if got := ToolSummary("grep_chunks", map[string]any{
		"total_matches": float64(30), "document_count": float64(24),
	}); got != "找到 30 个匹配片段，来自 24 个文档" {
		t.Fatalf("grep 摘要不符: %q", got)
	}
	if got := ToolSummary("knowledge_search", map[string]any{
		"count": float64(2), "kb_counts": map[string]any{"a": float64(1), "b": float64(1)},
	}); got != "找到 2 个结果，来自 2 个文件" {
		t.Fatalf("知识库摘要不符: %q", got)
	}
}

func TestBuildStepsContract(t *testing.T) {
	// WeKnora 历史消息 agent_steps 形状：{reasoning_content|thought, tool_calls:[{name,args,duration,result}]}。
	agentSteps := []map[string]any{
		{"reasoning_content": "先想一想", "tool_calls": []any{
			map[string]any{
				"name": "knowledge_search", "args": map[string]any{"query": "带电"},
				"duration": float64(1200),
				"result":   map[string]any{"success": true, "data": map[string]any{"count": float64(5)}},
			},
		}},
		{"tool_calls": []any{
			map[string]any{
				"name": "web_search",
				"result": map[string]any{
					"success": false, "error": "上游超时",
				},
			},
		}},
	}
	steps, meta := BuildSteps(agentSteps)
	if meta.Rounds != 2 || meta.Tools != 2 {
		t.Fatalf("meta 应为 rounds=2 tools=2: %+v", meta)
	}
	if len(steps) != 3 {
		t.Fatalf("应 1 思考 + 2 工具: %+v", steps)
	}
	if steps[0].Kind != "thought" || steps[0].Text != "先想一想" {
		t.Fatalf("思考节点形状不符: %+v", steps[0])
	}
	if steps[1].Kind != "tool" || steps[1].Status != "done" || steps[1].Name != "knowledge_search" {
		t.Fatalf("工具节点形状不符: %+v", steps[1])
	}
	if steps[1].Title != "检索知识库：「带电」" || steps[1].Summary != "找到 5 个结果" {
		t.Fatalf("工具标题/摘要不符: %+v", steps[1])
	}
	if steps[1].DurationMs != 1200 {
		t.Fatalf("耗时不符: %+v", steps[1])
	}
	if steps[2].Status != "error" || steps[2].Title != "网络搜索失败" || steps[2].Summary != "上游超时" {
		t.Fatalf("失败工具节点不符: %+v", steps[2])
	}
}

// errReader 读若干字节后报错的流（模拟上游连接中断）。
type errReader struct {
	data []byte
	done bool
}

func (e *errReader) Read(p []byte) (int, error) {
	if !e.done && len(e.data) > 0 {
		n := copy(p, e.data)
		e.data = e.data[n:]
		return n, nil
	}
	return 0, errors.New("connection reset")
}

func (e *errReader) Close() error { return nil }

func TestCollectAnswerStreamErrorNoDone(t *testing.T) {
	wk := &fakeWeKnora{body: &errReader{data: []byte(`data:{"response_type":"answer","content":"半截"}` + "\n\n")}}
	var events []ChatEvent
	_, err := CollectAnswer(context.Background(), wk, "u1", "s1",
		ChatRequest{Query: "问"}, func(ev ChatEvent) { events = append(events, ev) })
	if err == nil || !errors.Is(err, ErrEmitted) {
		t.Fatalf("连接中断应 ErrEmitted: %v", err)
	}
	for _, ev := range events {
		if ev.Type == "done" {
			t.Fatalf("连接中断不得下发 done（端侧会误判成功）: %+v", events)
		}
	}
	last := events[len(events)-1]
	if last.Type != "error" || last.Content != "AI 服务连接中断" {
		t.Fatalf("末事件应为 error: %+v", events)
	}
}

// ===== POST /chat 入参与 web_search 缺省 =====

func TestChatParamsAndWebSearchDefault(t *testing.T) {
	newReq := func(body string) (*httptest.ResponseRecorder, *fakeWeKnora) {
		wk := &fakeWeKnora{}
		m := &Module{DB: nil, WeKnora: wk, Permit: AllowAllPermit{}}
		req := httptest.NewRequest(http.MethodPost, "/api/v1/callme/chat", strings.NewReader(body))
		req = req.WithContext(context.WithValue(req.Context(), identityContextKey,
			auth.Identity{ID: 1, Username: "u1", Role: "user"}))
		rec := httptest.NewRecorder()
		m.handleChat(rec, req)
		return rec, wk
	}

	// 缺省不传 web_search → false（端侧关闭联网搜索时不传字段，不得默认开启）。
	rec, wk := newReq(`{"session_id":"s1","query":"问"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("应 200: %d %s", rec.Code, rec.Body.String())
	}
	if len(wk.chatReqs) != 1 || wk.chatReqs[0].WebSearch {
		t.Fatalf("web_search 缺省应为 false: %+v", wk.chatReqs)
	}

	// 显式 true 才开启。
	_, wk = newReq(`{"session_id":"s1","query":"问","web_search":true}`)
	if !wk.chatReqs[0].WebSearch {
		t.Fatalf("web_search=true 未透传: %+v", wk.chatReqs)
	}

	// 类型校验错误码逐一对应 Node（40043–40049）。
	cases := []struct {
		body string
		code int
	}{
		{`{"session_id":"s1","query":"问","web_search":"1"}`, codeWebSearch},
		{`{"session_id":"s1","query":"问","web_search":null}`, codeWebSearch},
		{`{"session_id":"s1","query":"问","model_id":3}`, codeModelID},
		{`{"session_id":"s1","query":"问","model_id":null}`, 0}, // null 合法（走默认模型）
		{`{"session_id":"s1","query":"问","reasoning_effort":"nope"}`, codeReasoningEffort},
		{`{"session_id":"s1","query":"问","attachment_ids":[""]}`, codeAttachIDs},
		{`{"session_id":"s1","query":"问","attachment_ids":"a1"}`, codeAttachIDs},
		{`{"session_id":"s1","query":"问","attachment_ids":null}`, codeAttachIDs},
		{`{"session_id":"s1","query":"问","images":null}`, codeImageCount},
		{`{"session_id":"s1","query":"问","images":["data:text/plain;base64,aGk="]}`, codeImageFormat},
		{`{"session_id":"s1","query":"问","images":[{"data":"data:text/plain;base64,aGk="}]}`, codeImageFormat},
		{`{"query":"问"}`, codeParam},
	}
	for _, c := range cases {
		rec, _ := newReq(c.body)
		var env Envelope
		_ = json.Unmarshal(rec.Body.Bytes(), &env)
		if c.code == 0 {
			if env.Code != codeOK {
				t.Fatalf("应成功: %s → %+v", c.body, env)
			}
			continue
		}
		if env.Code != c.code {
			t.Fatalf("入参 %s 应 %d 实际 %d", c.body, c.code, env.Code)
		}
	}

	// 纯 dataURL 字符串图片（Node 兼容形态）应放行。
	rec, wk = newReq(`{"session_id":"s1","query":"问","images":["data:image/png;base64,aGk="]}`)
	if rec.Code != http.StatusOK || len(wk.chatReqs) != 1 || len(wk.chatReqs[0].Images) != 1 {
		t.Fatalf("字符串形态图片应放行: %d %s", rec.Code, rec.Body.String())
	}
}

// ===== SSE 传输：逐帧 Flush 穿透包装层（思考增量不得憋到流末尾） =====

// unwrapOnlyWriter 模拟 httpapi statusRecorder：只透传 Unwrap、不实现 Flush。
type unwrapOnlyWriter struct {
	http.ResponseWriter
}

func (u *unwrapOnlyWriter) Unwrap() http.ResponseWriter { return u.ResponseWriter }

// countFlushWriter 统计 Flush 次数。
type countFlushWriter struct {
	http.ResponseWriter
	flushes int
}

func (c *countFlushWriter) Flush() {
	c.flushes++
	if f, ok := c.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// TestChatSseFlushThroughUnwrapper 回归：访问日志中间件的包装器不实现 http.Flusher，
// handleChat 须经 http.NewResponseController 穿透 Unwrap 链逐事件 Flush，否则 thinking
// 增量被 net/http 缓冲憋到流末尾整批到达（迁移回归根因）。
func TestChatSseFlushThroughUnwrapper(t *testing.T) {
	wk := &fakeWeKnora{stream: strings.Join([]string{
		`data:{"response_type":"thinking","content":"想一想"}`,
		``,
		`data:{"response_type":"answer","content":"答案","done":true,"request_id":"r1"}`,
		``,
	}, "\n")}
	m := &Module{DB: nil, WeKnora: wk, Permit: AllowAllPermit{}}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/callme/chat", strings.NewReader(`{"session_id":"s1","query":"问"}`))
	req = req.WithContext(context.WithValue(req.Context(), identityContextKey,
		auth.Identity{ID: 1, Username: "u1", Role: "user"}))
	base := &countFlushWriter{ResponseWriter: httptest.NewRecorder()}
	m.handleChat(&unwrapOnlyWriter{ResponseWriter: base}, req)

	body := base.ResponseWriter.(*httptest.ResponseRecorder).Body.String()
	if !strings.Contains(body, `"type":"thinking"`) || !strings.Contains(body, `"type":"answer"`) ||
		!strings.Contains(body, `"type":"done"`) {
		t.Fatalf("thinking/answer/done 应逐帧转发: %s", body)
	}
	// 初始 Flush 1 次 + thinking/answer/done 三帧各 1 次。
	if base.flushes < 4 {
		t.Fatalf("应逐帧 Flush（≥4 次），实得 %d —— 事件被缓冲憋到流末尾", base.flushes)
	}
}

// ===== SSE 心跳：handler 返回前须等心跳 goroutine 退出 =====

// blockingStreamBody 模拟上游长流：Read 阻塞至 Close（客户端断连经 Close 解除）。
type blockingStreamBody struct {
	closed chan struct{}
}

func (b *blockingStreamBody) Read([]byte) (int, error) {
	<-b.closed
	return 0, io.EOF
}

func (b *blockingStreamBody) Close() error {
	select {
	case <-b.closed:
	default:
		close(b.closed)
	}
	return nil
}

// heartbeatProbeWriter 扣住首个 : ping 写入待放行；记录 handler 返回后的写入。
type heartbeatProbeWriter struct {
	http.ResponseWriter
	pingOnce  sync.Once
	pingCh    chan struct{}
	release   chan struct{}
	returned  atomic.Bool
	lateWrite atomic.Bool
}

func (h *heartbeatProbeWriter) Unwrap() http.ResponseWriter { return h.ResponseWriter }

func (h *heartbeatProbeWriter) Write(b []byte) (int, error) {
	if h.returned.Load() {
		h.lateWrite.Store(true)
	}
	if strings.Contains(string(b), ": ping") {
		h.pingOnce.Do(func() {
			close(h.pingCh)
			<-h.release
		})
	}
	return h.ResponseWriter.Write(b)
}

// TestChatSseHeartbeatExitBeforeReturn 回归：心跳写入未完成时 handler 不得返回（退出
// 窗口），否则 handler 返回后心跳仍会写 ResponseWriter（use-after-return）；放行后
// handler 须随心跳退出而返回，且返回后不得再有 SSE 写入。
func TestChatSseHeartbeatExitBeforeReturn(t *testing.T) {
	oldSecs := chatHeartbeatSecs
	chatHeartbeatSecs = 1
	defer func() { chatHeartbeatSecs = oldSecs }()

	wk := &fakeWeKnora{body: &blockingStreamBody{closed: make(chan struct{})}}
	m := &Module{DB: nil, WeKnora: wk, Permit: AllowAllPermit{}}
	ctx, cancel := context.WithCancel(context.WithValue(context.Background(), identityContextKey,
		auth.Identity{ID: 1, Username: "u1", Role: "user"}))
	defer cancel()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/callme/chat", strings.NewReader(`{"session_id":"s1","query":"问"}`))
	req = req.WithContext(ctx)
	probe := &heartbeatProbeWriter{
		ResponseWriter: httptest.NewRecorder(),
		pingCh:         make(chan struct{}),
		release:        make(chan struct{}),
	}
	returned := make(chan struct{})
	go func() {
		m.handleChat(probe, req)
		probe.returned.Store(true)
		close(returned)
	}()

	// 心跳 tick 落入写入并被扣住（心跳 goroutine 停在 write 中）。
	select {
	case <-probe.pingCh:
	case <-time.After(5 * time.Second):
		t.Fatal("心跳未触发")
	}
	// 模拟客户端断连：收流经 ctx.Done 退出且不再写帧，handler 试图返回。
	cancel()
	select {
	case <-returned:
		t.Fatal("心跳写入未完成 handler 即已返回（返回后心跳仍会写 ResponseWriter）")
	case <-time.After(200 * time.Millisecond):
	}
	// 放行 → 心跳 goroutine 退出 → handler 随即返回。
	close(probe.release)
	select {
	case <-returned:
	case <-time.After(5 * time.Second):
		t.Fatal("放行后 handler 未返回")
	}
	time.Sleep(50 * time.Millisecond)
	if probe.lateWrite.Load() {
		t.Fatal("handler 返回后仍有 SSE 写入")
	}
}
