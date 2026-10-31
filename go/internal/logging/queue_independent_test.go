package logging

// Queue 内部降级/关闭日志必须只写自身 stderr，不回到标准库 log（防止 log→Queue→log 递归）。

import (
	"bytes"
	"log"
	"strings"
	"testing"
)

func TestQueueCloseLogsToInjectedStderrOnly(t *testing.T) {
	db, fake := newQueueFakeDB(t)
	gate, started, release := gatePair()
	fake.block(gate, started)
	var stderr bytes.Buffer
	q := newQueue(db, 1, 1, &stderr)
	defer func() { release(); _ = q.Close() }()
	writeDroppedQueue(t, q, started)

	var stdlogBuf bytes.Buffer
	old := log.Writer()
	log.SetOutput(&stdlogBuf)
	defer log.SetOutput(old)
	release()
	if err := q.CloseContext(t.Context()); err != nil {
		t.Fatalf("CloseContext: %v", err)
	}
	if !strings.Contains(stderr.String(), "丢弃") {
		t.Fatalf("降级日志未写独立 stderr：%q", stderr.String())
	}
	if stdlogBuf.Len() != 0 {
		t.Fatalf("Queue 内部日志回流标准库 log：%q", stdlogBuf.String())
	}
}
