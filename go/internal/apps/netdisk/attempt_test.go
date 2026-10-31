package netdisk

// OpenList 401 重登递归重试 attempt：重试请求必须带 attempt=2。

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"shade/internal/integrations"
)

type olAttemptRecorder struct {
	mu     sync.Mutex
	events []olAttemptEvent
}

type olAttemptEvent struct {
	fields map[string]any
}

func (r *olAttemptRecorder) Record(_ context.Context, _, _, _ string, _, _ string, fields map[string]any) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, olAttemptEvent{fields: fields})
	return nil
}

func (r *olAttemptRecorder) forPath(path string) []olAttemptEvent {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []olAttemptEvent
	for _, e := range r.events {
		if e.fields["path"] == path {
			out = append(out, e)
		}
	}
	return out
}

func TestOpenList401RetryRecordsRealAttempt(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/auth/login":
			_, _ = w.Write([]byte(`{"code":200,"data":{"token":"tk-1"}}`))
		case "/api/fs/list":
			calls++
			if calls == 1 {
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = w.Write([]byte(`{"code":401,"message":"expired"}`))
				return
			}
			_, _ = w.Write([]byte(`{"code":200,"data":{"content":[]}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	rec := &olAttemptRecorder{}
	rt := integrations.NewSharedTransport()
	rt.Recorder = rec
	ol := &OpenListHTTP{BaseURL: srv.URL, Username: "u", Password: "p", HTTP: &http.Client{Transport: rt}}
	if _, err := ol.Call(context.Background(), http.MethodPost, "/api/fs/list", CallOpts{Body: map[string]any{"path": "/"}}); err != nil {
		t.Fatalf("Call: %v", err)
	}
	if calls != 2 {
		t.Fatalf("列表调用次数 = %d，期望 2", calls)
	}
	events := rec.forPath("/api/fs/list")
	if len(events) != 2 {
		t.Fatalf("列表事件 = %d，期望每次尝试一条：%+v", len(events), events)
	}
	if events[0].fields["attempt"] != 1 || events[1].fields["attempt"] != 2 {
		t.Fatalf("attempt 序列 = %v/%v，期望 1/2", events[0].fields["attempt"], events[1].fields["attempt"])
	}
}
