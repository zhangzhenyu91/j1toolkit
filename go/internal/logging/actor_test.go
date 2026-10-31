package logging

// 日志归属与后台链路：WithActor 填 user/team，WithNewTraceID 派生新链路，
// Logger / WriteEntry 在条目未显式给出归属时从 ctx 补齐。

import (
	"context"
	"strings"
	"testing"

	"shade/internal/config"
)

// actorCaptureSink 记录写入条目（actor 回填断言用）。
type actorCaptureSink struct {
	entries []config.AppLogEntry
	err     error
}

func (s *actorCaptureSink) Write(_ context.Context, e config.AppLogEntry) error {
	s.entries = append(s.entries, e)
	return s.err
}

func TestWithActorRoundTrip(t *testing.T) {
	ctx := WithActor(context.Background(), 7, 42)
	userID, teamID := ActorFromContext(ctx)
	if userID != 7 || teamID != 42 {
		t.Fatalf("actor = %d/%d，期望 7/42", userID, teamID)
	}
	if userID, teamID := ActorFromContext(context.Background()); userID != 0 || teamID != 0 {
		t.Fatalf("空 context 应无归属，得到 %d/%d", userID, teamID)
	}
	if userID, teamID := ActorFromContext(nil); userID != 0 || teamID != 0 {
		t.Fatalf("nil context 应无归属，得到 %d/%d", userID, teamID)
	}
	// nil ctx 不 panic，返回带归属的 background 派生 ctx。
	ctx = WithActor(nil, 1, 2)
	if userID, teamID := ActorFromContext(ctx); userID != 1 || teamID != 2 {
		t.Fatalf("nil ctx 归属 = %d/%d", userID, teamID)
	}
}

func TestWithNewTraceIDDerivesFromParent(t *testing.T) {
	parent := WithActor(WithTraceID(context.Background(), "old-trace"), 7, 42)
	ctx := WithNewTraceID(parent)
	got := TraceIDFromContext(ctx)
	if len(got) != 32 || got == "old-trace" {
		t.Fatalf("派生 trace = %q，期望新 32 位 ID", got)
	}
	if userID, teamID := ActorFromContext(ctx); userID != 7 || teamID != 42 {
		t.Fatalf("派生 ctx 丢失归属：%d/%d", userID, teamID)
	}
	if again := TraceIDFromContext(WithNewTraceID(ctx)); again == got {
		t.Fatal("两次派生应产生不同 trace")
	}
	if TraceIDFromContext(WithNewTraceID(nil)) == "" {
		t.Fatal("nil ctx 也应派生 trace")
	}
}

func TestLoggerRecordFillsActorFromContext(t *testing.T) {
	sink := &actorCaptureSink{}
	l := NewWithSink(sink, "work-log")
	ctx := WithActor(WithTraceID(context.Background(), "trace-actor"), 9, 88)
	if err := l.Info(ctx, "business", "ev", "完成", "done", nil); err != nil {
		t.Fatalf("Record: %v", err)
	}
	if len(sink.entries) != 1 {
		t.Fatalf("条目 = %d", len(sink.entries))
	}
	e := sink.entries[0]
	if e.UserID != 9 || e.TeamID != 88 || e.TraceID != "trace-actor" {
		t.Fatalf("归属/trace 未回填：%+v", e)
	}
}

func TestWriteEntryFillsActorFromContext(t *testing.T) {
	db, fake := newFakeDB(t)
	ctx := WithActor(context.Background(), 11, 66)
	if err := WriteEntry(ctx, db, config.AppLogEntry{AppCode: "system", Message: "m"}); err != nil {
		t.Fatalf("WriteEntry: %v", err)
	}
	text := argsText(fake.inserts()[0].args)
	if !strings.Contains(text, "|11|") || !strings.Contains(text, "|66|") {
		t.Fatalf("user/team 未写入：%s", text)
	}
}

func TestWriteEntryKeepsExplicitActor(t *testing.T) {
	db, fake := newFakeDB(t)
	ctx := WithActor(context.Background(), 11, 66)
	if err := WriteEntry(ctx, db, config.AppLogEntry{AppCode: "system", Message: "m", UserID: 3, TeamID: 4}); err != nil {
		t.Fatalf("WriteEntry: %v", err)
	}
	text := argsText(fake.inserts()[0].args)
	if !strings.Contains(text, "|3|") || !strings.Contains(text, "|4|") ||
		strings.Contains(text, "|11|") || strings.Contains(text, "|66|") {
		t.Fatalf("显式归属应优先：%s", text)
	}
}
