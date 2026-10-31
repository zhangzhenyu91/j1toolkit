package logging

// 标准库 log 接入：多行拆分、脱敏、写入失败回退、启动早期 log.Fatal 同步落文件。

import (
	"bytes"
	"context"
	"errors"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"shade/internal/config"
)

// lineSink 记录每条日志的原文与类别。
type lineSink struct {
	mu      sync.Mutex
	entries []config.AppLogEntry
	err     error
}

func (s *lineSink) Write(_ context.Context, e config.AppLogEntry) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.entries = append(s.entries, e)
	return s.err
}

func (s *lineSink) snapshot() []config.AppLogEntry {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]config.AppLogEntry(nil), s.entries...)
}

func TestWriterSplitsLinesAndSkipsBlank(t *testing.T) {
	sink := &lineSink{}
	w := &Writer{Sink: sink, AppCode: "system"}
	raw := []byte("first line\nsecond line\n\nthird line")
	n, err := w.Write(raw)
	if err != nil || n != len(raw) {
		t.Fatalf("Write = %d, %v", n, err)
	}
	entries := sink.snapshot()
	if len(entries) != 3 {
		t.Fatalf("条目 = %d，期望 3（空行跳过）", len(entries))
	}
	for i, want := range []string{"first line", "second line", "third line"} {
		e := entries[i]
		if e.Message != want || e.RawMessage != want {
			t.Fatalf("第 %d 条原文 = %q/%q", i, e.Message, e.RawMessage)
		}
		if e.Category != "runtime" || e.Event != "runtime.log" || e.AppCode != "system" || e.Level != "info" {
			t.Fatalf("第 %d 条形态不符：%+v", i, e)
		}
		if e.LogID == "" || e.OccurredAt.IsZero() {
			t.Fatalf("第 %d 条缺 log_id/occurred_at：%+v", i, e)
		}
	}
}

func TestWriterFallsBackOnSinkError(t *testing.T) {
	var fallback bytes.Buffer
	w := &Writer{Sink: &lineSink{err: errors.New("sink down")}, AppCode: "system", Fallback: &fallback}
	raw := []byte("keep me\n")
	n, err := w.Write(raw)
	if err != nil || n != len(raw) {
		t.Fatalf("Write = %d, %v（标准日志写入失败不应报错）", n, err)
	}
	if !strings.Contains(fallback.String(), "keep me") {
		t.Fatalf("回退未收到原文：%q", fallback.String())
	}
}

func TestInstallStandardLoggerWritesToSink(t *testing.T) {
	sink := &lineSink{}
	var old bytes.Buffer
	log.SetOutput(&old)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	restore := InstallStandardLogger(sink, "system")
	log.Print("through-standard-log")
	restore()
	log.Print("after-restore")
	entries := sink.snapshot()
	if len(entries) != 1 || !strings.Contains(entries[0].Message, "through-standard-log") {
		t.Fatalf("标准日志未落 sink：%+v", entries)
	}
	if !strings.Contains(old.String(), "after-restore") {
		t.Fatalf("restore 未恢复原输出：%q", old.String())
	}
}

func TestStandardLogFatalWritesDurableFile(t *testing.T) {
	if os.Getenv("SHADE_LOG_FATAL_HELPER") == "1" {
		t.Skip("helper 由父进程显式运行")
	}
	dir := t.TempDir()
	cmd := exec.Command(os.Args[0], "-test.run=^TestStandardLogFatalHelperProcess$", "-test.timeout=30s")
	cmd.Env = append(os.Environ(),
		"SHADE_LOG_FATAL_HELPER=1",
		"SHADE_LOG_FATAL_DIR="+dir,
	)
	out, err := cmd.CombinedOutput()
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode() != 1 {
		t.Fatalf("log.Fatal 退出码应 1，err=%v out=%s", err, out)
	}
	files, globErr := filepath.Glob(filepath.Join(dir, "app-*.log"))
	if globErr != nil || len(files) == 0 {
		t.Fatalf("未生成日志段文件：%v files=%v", globErr, files)
	}
	data, readErr := os.ReadFile(files[0])
	if readErr != nil {
		t.Fatalf("读取日志段: %v", readErr)
	}
	text := string(data)
	for _, want := range []string{"fatal-before-db", `"app_code":"system"`, `"category":"runtime"`, `"event":"runtime.log"`} {
		if !strings.Contains(text, want) {
			t.Fatalf("日志段缺 %q：%s", want, text)
		}
	}
}

// TestStandardLogFatalHelperProcess 子进程：创建 DurableSink 后直接 log.Fatal。
func TestStandardLogFatalHelperProcess(t *testing.T) {
	if os.Getenv("SHADE_LOG_FATAL_HELPER") != "1" {
		return
	}
	sink, err := NewDurableSink(os.Getenv("SHADE_LOG_FATAL_DIR"))
	if err != nil {
		os.Exit(2)
	}
	InstallStandardLogger(sink, "system")
	log.Fatal("fatal-before-db")
}

func TestWriterMasksSecrets(t *testing.T) {
	sink := &lineSink{}
	w := &Writer{Sink: sink, AppCode: "system"}
	if _, err := w.Write([]byte("Authorization: Bearer SECRETTOKEN token=abc\n")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	entries := sink.snapshot()
	if len(entries) != 1 {
		t.Fatalf("条目 = %d", len(entries))
	}
	text := entries[0].Message + entries[0].RawMessage + entries[0].MessageCN
	if strings.Contains(text, "SECRETTOKEN") || strings.Contains(text, "token=abc") {
		t.Fatalf("运行日志秘密未脱敏：%s", text)
	}
}
