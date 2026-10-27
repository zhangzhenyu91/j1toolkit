package callme

// 测试专用 fake WeKnora 客户端：预置会话/模型/SSE 流，记录调用；不访问真实外部服务。

import (
	"context"
	"io"
	"strings"
	"sync"
)

type fakeWeKnora struct {
	mu sync.Mutex

	sessions  []map[string]any // ListSessions 返回
	created   []createCall     // CreateSession 记录
	models    []Model          // ListModels 返回
	agent     map[string]any   // GetAgent 返回
	providers []map[string]any // ListWebSearchProviders 返回
	stream    string           // AgentChatStream SSE 文本
	streamErr error            // AgentChatStream 错误
	uploaded  []uploadCall     // UploadAttachment 记录
	deleted   []string         // DeleteSession/DeleteMessage 记录
	chatReqs  []ChatRequest    // AgentChatStream 入参
	chatUser  string
}

type createCall struct{ username, title string }

type uploadCall struct {
	username, sessionID, filename, mimetype string
	size                                    int
}

func (f *fakeWeKnora) CreateSession(_ context.Context, username, title string) (map[string]any, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.created = append(f.created, createCall{username, title})
	return map[string]any{"data": map[string]any{"id": "s-new", "title": title}}, nil
}

func (f *fakeWeKnora) ListSessions(context.Context, string) (map[string]any, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return map[string]any{"data": f.sessions}, nil
}

func (f *fakeWeKnora) GetSession(context.Context, string, string) (map[string]any, error) {
	return map[string]any{"data": map[string]any{"id": "s1", "title": "会话A"}}, nil
}

func (f *fakeWeKnora) ListMessages(context.Context, string, string, int, string) (map[string]any, error) {
	return map[string]any{"data": []any{
		map[string]any{"id": "m1", "role": "user", "content": "问题", "created_at": "2026-10-27T01:00:00Z", "request_id": "r1"},
		map[string]any{"id": "m2", "role": "assistant", "content": "回答", "created_at": "2026-10-27T01:00:01Z", "request_id": "r1",
			"agent_steps": []any{map[string]any{"type": "thinking", "content": "想一想"}}},
	}}, nil
}

func (f *fakeWeKnora) UpdateSession(context.Context, string, string, string) (map[string]any, error) {
	return map[string]any{"data": map[string]any{"id": "s1"}}, nil
}

func (f *fakeWeKnora) DeleteSession(_ context.Context, _ string, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deleted = append(f.deleted, "session:"+id)
	return nil
}

func (f *fakeWeKnora) DeleteMessage(_ context.Context, _ string, _ string, messageID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deleted = append(f.deleted, "msg:"+messageID)
	return nil
}

func (f *fakeWeKnora) UploadAttachment(_ context.Context, username, sessionID, filename, mimetype string, buf []byte) (Attachment, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.uploaded = append(f.uploaded, uploadCall{username, sessionID, filename, mimetype, len(buf)})
	return Attachment{ID: "att-1", FileName: filename, FileSize: int64(len(buf)), Status: "processing"}, nil
}

func (f *fakeWeKnora) ListAttachments(context.Context, string, string) ([]Attachment, error) {
	return []Attachment{{ID: "att-1", FileName: "a.pdf", FileSize: 10, Status: "completed"}}, nil
}

func (f *fakeWeKnora) ListModels(context.Context, string) ([]Model, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.models, nil
}

func (f *fakeWeKnora) GetAgent(context.Context, string) (map[string]any, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.agent == nil {
		return map[string]any{"data": map[string]any{}}, nil
	}
	return map[string]any{"data": f.agent}, nil
}

func (f *fakeWeKnora) ListWebSearchProviders(context.Context, string) ([]map[string]any, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.providers, nil
}

func (f *fakeWeKnora) AgentChatStream(_ context.Context, username, sessionID string, req ChatRequest) (io.ReadCloser, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.chatUser = username
	f.chatReqs = append(f.chatReqs, req)
	if f.streamErr != nil {
		return nil, f.streamErr
	}
	stream := f.stream
	if stream == "" {
		stream = "data:{\"response_type\":\"answer\",\"content\":\"你好\",\"done\":true,\"request_id\":\"r1\"}\n\n"
	}
	return io.NopCloser(strings.NewReader(stream)), nil
}
