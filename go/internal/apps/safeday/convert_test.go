package safeday

// LibreOffice 转换日志：耗时/退出状态/截断 stderr；用测试二进制充当 soffice，不实际调用 LibreOffice。

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"unicode/utf8"

	"shade/internal/config"
	"shade/internal/logging"
)

type convertSink struct {
	mu      sync.Mutex
	entries []config.AppLogEntry
}

func (s *convertSink) Write(_ context.Context, e config.AppLogEntry) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.entries = append(s.entries, e)
	return nil
}

func (s *convertSink) snapshot() []config.AppLogEntry {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]config.AppLogEntry(nil), s.entries...)
}

func converterWithHelper(mode string) (*LibreOfficeConverter, *convertSink) {
	sink := &convertSink{}
	c := &LibreOfficeConverter{
		Log: logging.NewWithSink(sink, "safe-day"),
	}
	c.CommandContext = func(ctx context.Context, _ string, args ...string) *exec.Cmd {
		helperArgs := append([]string{"-test.run=^TestSofficeHelperProcess$", "--"}, args...)
		cmd := exec.CommandContext(ctx, os.Args[0], helperArgs...)
		cmd.Env = append(os.Environ(), "GO_WANT_SOFFICE_HELPER=1", "SOFFICE_HELPER_MODE="+mode)
		return cmd
	}
	return c, sink
}

func TestLibreOfficeConverterLogsSuccess(t *testing.T) {
	c, sink := converterWithHelper("ok")
	out, err := c.ToPDF(context.Background(), []byte("hello"), "docx")
	if err != nil {
		t.Fatalf("ToPDF: %v", err)
	}
	if !strings.HasPrefix(string(out), "%PDF") {
		t.Fatalf("输出不是 PDF：%q", out)
	}
	entries := sink.snapshot()
	if len(entries) != 1 {
		t.Fatalf("事件 = %d，期望 1：%+v", len(entries), entries)
	}
	e := entries[0]
	if e.Event != "safeday.convert" || e.Level != "info" || e.Category != "business" {
		t.Fatalf("成功事件形态不符：%+v", e)
	}
	for _, key := range []string{"duration_ms", "exit_status", "ext", "output_bytes"} {
		if !strings.Contains(e.Detail, `"`+key+`"`) {
			t.Fatalf("detail 缺 %s：%s", key, e.Detail)
		}
	}
	if strings.Contains(e.Detail, `"exit_status":3`) {
		t.Fatalf("成功退出状态不符：%s", e.Detail)
	}
}

func TestLibreOfficeConverterLogsFailureTruncatedStderr(t *testing.T) {
	c, sink := converterWithHelper("fail")
	_, err := c.ToPDF(context.Background(), []byte("hello"), "xlsx")
	if err == nil {
		t.Fatal("期望转换失败")
	}
	entries := sink.snapshot()
	if len(entries) != 1 {
		t.Fatalf("事件 = %d，期望 1：%+v", len(entries), entries)
	}
	e := entries[0]
	if e.Event != "safeday.convert.failed" || e.Level != "error" {
		t.Fatalf("失败事件形态不符：%+v", e)
	}
	for _, want := range []string{`"exit_status":3`, `"duration_ms"`} {
		if !strings.Contains(e.Detail, want) {
			t.Fatalf("detail 缺 %s：%s", want, e.Detail)
		}
	}
	if strings.Contains(e.Detail, strings.Repeat("E", 201)) {
		t.Fatalf("stderr 未截断：%s", e.Detail)
	}
	if !strings.Contains(e.Detail, strings.Repeat("E", 50)) {
		t.Fatalf("detail 缺 stderr 摘要：%s", e.Detail)
	}
}

// TestSofficeHelperProcess 测试二进制的 soffice 替身：ok 写出 source.pdf，fail 写长 stderr 并退出 3。
func TestSofficeHelperProcess(t *testing.T) {
	if os.Getenv("GO_WANT_SOFFICE_HELPER") != "1" {
		return
	}
	args := os.Args
	sep := -1
	for i, a := range args {
		if a == "--" {
			sep = i
			break
		}
	}
	if sep < 0 {
		os.Exit(2)
	}
	sofficeArgs := args[sep+1:]
	if os.Getenv("SOFFICE_HELPER_MODE") == "fail" {
		_, _ = os.Stderr.WriteString(strings.Repeat("E", 500))
		os.Exit(3)
	}
	if os.Getenv("SOFFICE_HELPER_MODE") == "fail_utf8" {
		_, _ = os.Stderr.WriteString(strings.Repeat("你", 300))
		os.Exit(3)
	}
	outDir := ""
	input := ""
	for i, a := range sofficeArgs {
		if a == "--outdir" && i+1 < len(sofficeArgs) {
			outDir = sofficeArgs[i+1]
		}
		input = a
	}
	if outDir == "" || input == "" {
		os.Exit(2)
	}
	_ = input
	if err := os.WriteFile(filepath.Join(outDir, "source.pdf"), []byte("%PDF-1.4 fake"), 0o644); err != nil {
		os.Exit(2)
	}
	os.Exit(0)
}

func TestLibreOfficeConverterTruncatesStderrByRune(t *testing.T) {
	c, sink := converterWithHelper("fail_utf8")
	if _, err := c.ToPDF(context.Background(), []byte("hello"), "docx"); err == nil {
		t.Fatal("期望转换失败")
	}
	e := sink.snapshot()[0]
	if !utf8.ValidString(e.Detail) {
		t.Fatalf("stderr 截断产生非法 UTF-8：%q", e.Detail)
	}
	if strings.Contains(e.Detail, "�") {
		t.Fatalf("stderr 截断切坏多字节字符：%q", e.Detail)
	}
	if got := strings.Count(e.Detail, "你"); got != 200 {
		t.Fatalf("stderr rune 截断 = %d，期望 200：%q", got, e.Detail)
	}
}
