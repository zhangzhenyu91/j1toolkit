package bridge

// AskBridge / WF3Namer 测试：callme 直调（token 校验 + 业务核复用）与 Dify WF3 命名（httptest）。

import (
	"context"
	"database/sql/driver"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"shade/internal/apps/callme"
	"shade/internal/apps/wechatanswer"
)

// fakeCfg 配置桩沿用 bridge_test.go 定义。

// fakeWeKnora callme.WeKnoraClient 桩（只实现 ingest 流程用到的方法）。
type fakeWeKnora struct {
	sessions     []map[string]any
	created      int
	uploadName   string
	chatQuery    string
	answer       string
	streamErr    error
	listModels   []callme.Model
	modelsErr    error
	uploadDocID  string
	uploadErr    error
	sessListErr  error
	createSessID string
}

func (f *fakeWeKnora) CreateSession(_ context.Context, _, _ string) (map[string]any, error) {
	f.created++
	id := f.createSessID
	if id == "" {
		id = "sess-new"
	}
	return map[string]any{"data": map[string]any{"id": id}}, nil
}

func (f *fakeWeKnora) ListSessions(_ context.Context, _ string) (map[string]any, error) {
	if f.sessListErr != nil {
		return nil, f.sessListErr
	}
	list := make([]any, 0, len(f.sessions))
	for _, s := range f.sessions {
		list = append(list, s)
	}
	return map[string]any{"data": list}, nil
}

func (f *fakeWeKnora) GetSession(context.Context, string, string) (map[string]any, error) {
	return map[string]any{}, nil
}

func (f *fakeWeKnora) ListMessages(context.Context, string, string, int, string) (map[string]any, error) {
	return map[string]any{"data": []any{}}, nil
}

func (f *fakeWeKnora) UpdateSession(context.Context, string, string, string) (map[string]any, error) {
	return map[string]any{}, nil
}

func (f *fakeWeKnora) DeleteSession(context.Context, string, string) error { return nil }

func (f *fakeWeKnora) DeleteMessage(context.Context, string, string, string) error { return nil }

func (f *fakeWeKnora) UploadAttachment(_ context.Context, _, _, filename, _ string, _ []byte) (callme.Attachment, error) {
	f.uploadName = filename
	if f.uploadErr != nil {
		return callme.Attachment{}, f.uploadErr
	}
	id := f.uploadDocID
	if id == "" {
		id = "doc-1"
	}
	return callme.Attachment{ID: id, FileName: filename}, nil
}

func (f *fakeWeKnora) ListAttachments(context.Context, string, string) ([]callme.Attachment, error) {
	return nil, nil
}

func (f *fakeWeKnora) ListModels(context.Context, string) ([]callme.Model, error) {
	return f.listModels, f.modelsErr
}

func (f *fakeWeKnora) GetAgent(context.Context, string) (map[string]any, error) {
	return map[string]any{}, nil
}

func (f *fakeWeKnora) ListWebSearchProviders(context.Context, string) ([]map[string]any, error) {
	return nil, nil
}

func (f *fakeWeKnora) AgentChatStream(_ context.Context, _, _ string, req callme.ChatRequest) (io.ReadCloser, error) {
	f.chatQuery = req.Query
	if f.streamErr != nil {
		return nil, f.streamErr
	}
	payload, _ := json.Marshal(map[string]any{"response_type": "answer", "content": f.answer, "done": true})
	return io.NopCloser(strings.NewReader("data: " + string(payload) + "\n\n")), nil
}

var _ callme.WeKnoraClient = (*fakeWeKnora)(nil)

// newCallMe 构造带 fake 库与 WeKnora 桩的 callme 模块。
func newCallMe(t *testing.T, cfg fakeCfg, wk callme.WeKnoraClient) *callme.Module {
	t.Helper()
	db, fb := openFake(t)
	fb.on("FROM sys_user WHERE wxid", []string{"id", "username"}, [][]driver.Value{{int64(7), "zhang"}})
	cm := callme.New(db, cfg)
	cm.WeKnora = wk
	return cm
}

func TestAskBridgeIngestChatDirect(t *testing.T) {
	cfg := fakeCfg{"CALLME_INGEST_TOKEN": "tok-1"}
	wk := &fakeWeKnora{answer: "这是回答", createSessID: "sess-9"}
	cm := newCallMe(t, cfg, wk)
	a := NewAskBridge(cm, cfg)
	answer, err := a.IngestChat(context.Background(), wechatanswer.AskRequest{
		Wxid: "wxid_a", Query: "问题内容", SessionTitle: "检修一班群内提问", WebSearch: true,
	})
	if err != nil {
		t.Fatalf("IngestChat: %v", err)
	}
	if answer != "这是回答" {
		t.Fatalf("answer = %q", answer)
	}
	if wk.chatQuery != "问题内容" {
		t.Fatalf("chatQuery = %q", wk.chatQuery)
	}
	if wk.created != 1 {
		t.Fatalf("created sessions = %d", wk.created)
	}
}

func TestAskBridgeTokenFailClosed(t *testing.T) {
	// 直调仍校验 ingest token（进程内不免密钥）：桥传令牌与模块配置不等 → ingest_token_invalid。
	cm := newCallMe(t, fakeCfg{"CALLME_INGEST_TOKEN": "tok-1"}, &fakeWeKnora{answer: "x"})
	a := NewAskBridge(cm, fakeCfg{"CALLME_INGEST_TOKEN": "tok-wrong"})

	_, err := a.IngestChat(context.Background(), wechatanswer.AskRequest{Wxid: "w", Query: "q", SessionTitle: "t"})
	if err == nil || !strings.Contains(err.Error(), "ingest_token_invalid") {
		t.Fatalf("err = %v", err)
	}
	var ae *wechatanswer.AskError
	if errorAs(err, &ae) && ae.Code != "ingest_token_invalid" {
		t.Fatalf("code = %s", ae.Code)
	}
	// 模块密钥未配置 → ingest_token_missing（fail-closed，不带空密钥放行）。
	cm2 := newCallMe(t, fakeCfg{}, &fakeWeKnora{answer: "x"})
	b := NewAskBridge(cm2, fakeCfg{})
	_, err = b.IngestChat(context.Background(), wechatanswer.AskRequest{Wxid: "w", Query: "q", SessionTitle: "t"})
	if err == nil || !strings.Contains(err.Error(), "ingest_token_missing") {
		t.Fatalf("err = %v", err)
	}
}

func TestAskBridgeErrorMapping(t *testing.T) {
	cfg := fakeCfg{"CALLME_INGEST_TOKEN": "tok-1"}
	db, fb := openFake(t)
	fb.on("FROM sys_user WHERE wxid", []string{"id", "username"}, [][]driver.Value{}) // 0 命中
	cm := callme.New(db, cfg)
	cm.WeKnora = &fakeWeKnora{}
	a := NewAskBridge(cm, cfg)
	_, err := a.IngestChat(context.Background(), wechatanswer.AskRequest{Wxid: "w", Query: "q", SessionTitle: "t"})
	var ae *wechatanswer.AskError
	if !errorAs(err, &ae) || ae.Code != "user_not_found" {
		t.Fatalf("err = %v", err)
	}
}

// errorAs errors.As 的短名（避免测试里反复 import）。
func errorAs(err error, target any) bool {
	for err != nil {
		if e, ok := err.(*wechatanswer.AskError); ok {
			if p, ok := target.(**wechatanswer.AskError); ok {
				*p = e
				return true
			}
		}
		u, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}

func TestWF3Namer(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/workflows/run" {
			t.Errorf("path = %s", r.URL.Path)
		}
		_ = json.NewDecoder(r.Body).Decode(&got)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": map[string]any{"outputs": map[string]any{"filename": "现场问题/排查:记录"}},
		})
	}))
	defer srv.Close()
	cfg := fakeCfg{"DIFY_API_URL": srv.URL, "DIFY_WF3_API_KEY": "wf3-key"}
	n := NewWF3Namer(cfg)
	name := n.NameFile(context.Background(), "zhang", "会话标题", "问题", "回答")
	if name != "现场问题排查记录" {
		t.Fatalf("name = %q", name)
	}
	inputs, _ := got["inputs"].(map[string]any)
	if inputs == nil || inputs["question"] != "问题" {
		t.Fatalf("inputs = %#v", got["inputs"])
	}

	// 未配置 → 空串（callme 回退会话标题）。
	empty := NewWF3Namer(fakeCfg{})
	if s := empty.NameFile(context.Background(), "u", "t", "q", "a"); s != "" {
		t.Fatalf("unconfigured name = %q", s)
	}
}
