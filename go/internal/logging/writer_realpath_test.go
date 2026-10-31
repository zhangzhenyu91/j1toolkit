package logging

// 真实生产路径验证：标准库 log.Print → Writer → DurableSink；关闭后故障回退；独立 writer 不递归。
// 不使用自定义“会 log.Print 的 Sink”作为唯一证据；DurableSink/Queue 内部只写各自 stderr。

import (
	"bytes"
	"context"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func logWithTimeout(t *testing.T, msg string) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		log.Print(msg)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("标准日志写入超时（疑似递归/死锁）")
	}
}

func TestStandardLogDurableSinkRealPathNoDeadlock(t *testing.T) {
	dir := t.TempDir()
	sink, err := NewDurableSink(dir)
	if err != nil {
		t.Fatalf("NewDurableSink: %v", err)
	}
	restore := InstallStandardLogger(sink, "system")
	logWithTimeout(t, "real-durable-line")
	restore()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = sink.CloseContext(ctx)
	files, _ := filepath.Glob(filepath.Join(dir, "app-*.log"))
	if len(files) == 0 {
		t.Fatal("未生成日志段")
	}
	data, _ := os.ReadFile(files[0])
	if !strings.Contains(string(data), "real-durable-line") {
		t.Fatalf("真实 log→DurableSink 未落盘：%s", data)
	}
}

func TestStandardLogClosedDurableSinkFallsBackNoDeadlock(t *testing.T) {
	dir := t.TempDir()
	sink, err := NewDurableSink(dir)
	if err != nil {
		t.Fatalf("NewDurableSink: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	if err := sink.CloseContext(ctx); err != nil {
		t.Fatalf("CloseContext: %v", err)
	}
	cancel()
	var fallback bytes.Buffer
	w := &Writer{Sink: sink, AppCode: "system", Fallback: &fallback}
	old := log.Writer()
	log.SetOutput(w)
	defer log.SetOutput(old)
	done := make(chan struct{})
	go func() {
		log.Print("closed-sink-line")
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("关闭 sink 后标准日志超时（疑似递归/死锁）")
	}
	if !strings.Contains(fallback.String(), "closed-sink-line") {
		t.Fatalf("故障诱导未回退：%q", fallback.String())
	}
}

func TestDurableSinkInternalAlertUsesInjectedStderrOnly(t *testing.T) {
	var stderr bytes.Buffer
	dir := t.TempDir()
	sink, err := NewDurableSinkWithOptions(dir, DurableOptions{Stderr: &stderr})
	if err != nil {
		t.Fatalf("NewDurableSinkWithOptions: %v", err)
	}
	t.Cleanup(func() { _ = sink.CloseContext(context.Background()) })
	var stdlogBuf bytes.Buffer
	old := log.Writer()
	log.SetOutput(&stdlogBuf)
	defer log.SetOutput(old)
	sink.alert("alert-boom")
	if !strings.Contains(stderr.String(), "alert-boom") {
		t.Fatalf("内部告警未写注入 stderr：%q", stderr.String())
	}
	if stdlogBuf.Len() != 0 {
		t.Fatalf("DurableSink 内部告警回流标准库 log：%q", stdlogBuf.String())
	}
}
