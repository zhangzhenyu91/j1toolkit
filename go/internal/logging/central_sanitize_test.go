package logging

// 集中脱敏防线：Logger.Record / WriteEntry / DurableSink.Write 入口统一清洗 URL/userinfo/签名文本。

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"shade/internal/config"
)

const shortSecretURL = "https://bob:p%40ss@example.com:8443/p?fileInfoId=F1&Signature=SIG1"

func assertNoSecretText(t *testing.T, text string) {
	t.Helper()
	for _, secret := range []string{"bob", "p%40ss", "SIG1"} {
		if strings.Contains(text, secret) {
			t.Fatalf("集中脱敏遗漏 %q：%s", secret, text)
		}
	}
	if !strings.Contains(text, "fileInfoId=F1") {
		t.Fatalf("非敏感 fileInfoId 被误伤：%s", text)
	}
}

func TestLoggerRecordSanitizesEntryAndFields(t *testing.T) {
	sink := &actorCaptureSink{}
	l := NewWithSink(sink, "system")
	err := l.Record(context.Background(), "warn", "business", "sgcc.photo.download.failed",
		"下载失败 "+shortSecretURL, shortSecretURL, map[string]any{
			"url":    shortSecretURL,
			"error":  "Get " + shortSecretURL + " failed",
			"nested": map[string]string{"session_key": "sk-1", "path": "p"},
		})
	if err != nil {
		t.Fatalf("Record: %v", err)
	}
	e := sink.entries[0]
	text := e.Message + e.MessageCN + e.RawMessage + e.Detail
	assertNoSecretText(t, text)
	if strings.Contains(text, "sk-1") {
		t.Fatalf("session_key 泄漏：%s", text)
	}
	if !strings.Contains(e.Detail, `"path":"p"`) {
		t.Fatalf("路径类字段被误伤：%s", e.Detail)
	}
}

func TestWriteEntrySanitizesBeforeDB(t *testing.T) {
	db, fake := newFakeDB(t)
	err := WriteEntry(context.Background(), db, config.AppLogEntry{
		AppCode: "system", Message: "失败 " + shortSecretURL, MessageCN: shortSecretURL,
		RawMessage: shortSecretURL, Detail: `{"url":"` + shortSecretURL + `"}`,
	})
	if err != nil {
		t.Fatalf("WriteEntry: %v", err)
	}
	text := argsText(fake.inserts()[0].args)
	assertNoSecretText(t, text)
}

func TestDurableSinkWriteSanitizesNDJSON(t *testing.T) {
	dir := t.TempDir()
	sink, err := NewDurableSink(dir)
	if err != nil {
		t.Fatalf("NewDurableSink: %v", err)
	}
	if err := sink.Write(context.Background(), config.AppLogEntry{
		AppCode: "system", Level: "warn", Category: "business", Event: "ev",
		Message: "失败 " + shortSecretURL, MessageCN: shortSecretURL, RawMessage: shortSecretURL,
		Detail: `{"url":"` + shortSecretURL + `","session_key":"sk-2"}`,
	}); err != nil {
		t.Fatalf("Write: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), defaultDurablePollInterval+time.Second)
	defer cancel()
	_ = sink.CloseContext(ctx)
	files, _ := filepath.Glob(filepath.Join(dir, "app-*.log"))
	if len(files) == 0 {
		t.Fatal("未生成日志段")
	}
	data, err := os.ReadFile(files[0])
	if err != nil {
		t.Fatalf("读取日志段: %v", err)
	}
	text := string(data)
	assertNoSecretText(t, text)
	if strings.Contains(text, "sk-2") {
		t.Fatalf("session_key 泄漏：%s", text)
	}
}
