package kvm

// KVM 手工重试 attempt：AUTH_EXPIRED 重登后的第二次请求必须带 attempt=2。

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"shade/internal/integrations"
)

type attemptRecorder struct {
	mu     sync.Mutex
	events []attemptEvent
}

type attemptEvent struct {
	event  string
	fields map[string]any
}

func (r *attemptRecorder) Record(_ context.Context, _, _, event, _, _ string, fields map[string]any) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, attemptEvent{event: event, fields: fields})
	return nil
}

func (r *attemptRecorder) forPath(path string) []attemptEvent {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []attemptEvent
	for _, e := range r.events {
		if e.fields["path"] == path {
			out = append(out, e)
		}
	}
	return out
}

func TestGLKVMCallRecordsRealAttempt(t *testing.T) {
	deviceCalls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/login":
			_, _ = w.Write([]byte(`{"ok":true,"data":{"token":"tok"}}`))
		case "/api/devices":
			deviceCalls++
			if strings.Contains(r.Header.Get("Authorization"), "stale") {
				_, _ = w.Write([]byte(`{"ok":false,"code":"AUTH_EXPIRED","message":"会话过期"}`))
				return
			}
			_, _ = w.Write([]byte(`{"ok":true,"data":{"items":[]}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	rec := &attemptRecorder{}
	rt := integrations.NewSharedTransport()
	rt.Recorder = rec
	c := NewGLKVMClient(srv.URL, "pw")
	c.HTTP = &http.Client{Transport: rt}
	c.mu.Lock()
	c.activeURL, c.activePass = srv.URL, "pw"
	c.sessions["emp"] = cachedSession{token: "stale", expiresAt: time.Now().Add(time.Hour)}
	c.mu.Unlock()
	if _, err := c.ListDevices(context.Background(), url.Values{}, "emp"); err != nil {
		t.Fatalf("ListDevices: %v", err)
	}
	if deviceCalls != 2 {
		t.Fatalf("设备调用次数 = %d，期望 2", deviceCalls)
	}
	events := rec.forPath("/api/devices")
	if len(events) != 2 {
		t.Fatalf("设备事件 = %d，期望每次尝试一条：%+v", len(events), events)
	}
	if events[0].fields["attempt"] != 1 || events[1].fields["attempt"] != 2 {
		t.Fatalf("attempt 序列 = %v/%v，期望 1/2", events[0].fields["attempt"], events[1].fields["attempt"])
	}
}
