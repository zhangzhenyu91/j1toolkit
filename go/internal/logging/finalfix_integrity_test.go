// Final review fix：fsync 失败必须向上返回并回退 DB（同 log_id 幂等），健康计数与
// 写入/回退/损坏状态持久化，重启后仍可识别；文件 log_id 对账遇到坏行/半行必须失败关闭。
package logging

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// TestDurableSinkFsyncFailureFallsBackAndPersists fsync 失败不得静默成功：
// 同一条日志按同一 log_id 回退 DB，文件行保留；重启后健康计数仍能识别不完整。
func TestDurableSinkFsyncFailureFallsBackAndPersists(t *testing.T) {
	dir := t.TempDir()
	stderr := &stderrBuffer{}
	s := openTestSink(t, dir, testDurableOptions(stderr))
	db, fake := newDurableFakeDB(t)
	s.AttachDB(db)
	syncCalls := 0
	s.syncFn = func(*os.File) error {
		syncCalls++
		return errors.New("fsync 注入失败")
	}

	entry := durableTestEntry("fsync-fail")
	entry.Level = "warn"
	entry.LogID = "log-fsync-1"
	if err := s.Write(context.Background(), entry); err != nil {
		t.Fatalf("fsync 失败但 DB 回退成功时 Write 仍应吞掉错误: %v", err)
	}
	if syncCalls == 0 {
		t.Fatal("warn 日志必须触发 fsync")
	}
	if err := s.syncFile(); err == nil {
		t.Fatal("syncFile 必须向上返回 fsync 错误，不能只写 stderr")
	}
	if fake.count() != 1 {
		t.Fatalf("fsync 失败应按同一 log_id 回退 DB，实际 DB 行数 %d", fake.count())
	}
	got, _ := fake.row(0)[12].(string)
	if got != "log-fsync-1" {
		t.Fatalf("回退 DB 的 log_id 应保持 %q，实际 %q", "log-fsync-1", got)
	}
	if h := s.Health(); h.FsyncErrors == 0 || h.WriteErrors == 0 || h.DiskFallbacks == 0 {
		t.Fatalf("健康计数应记录 fsync/写失败/DB 回退: %+v", h)
	}
	// 关闭路径同样会 fsync；故障注入只针对上面的写入验证，恢复后关闭。
	s.syncFn = nil
	closeSink(t, s)

	// 重启：计数必须从持久状态恢复，Importer 重放不得重复插入。
	s2 := openTestSink(t, dir, testDurableOptions(stderr))
	h2 := s2.Health()
	if h2.FsyncErrors == 0 || h2.WriteErrors == 0 || h2.DiskFallbacks == 0 {
		t.Fatalf("重启后应识别历史写入/fsync/回退失败: %+v", h2)
	}
	s2.AttachDB(db)
	if err := s2.Flush(context.Background()); err != nil {
		t.Fatalf("重启后补录: %v", err)
	}
	if fake.count() != 1 {
		t.Fatalf("同一 log_id 不得双写重复，DB 行数 %d", fake.count())
	}
	lines := readActiveLogLines(t, dir)
	if len(lines) != 1 || !strings.Contains(lines[0], "log-fsync-1") {
		t.Fatalf("文件行必须保留供幂等补录: %v", lines)
	}
	closeSink(t, s2)
}

// TestDurableSinkWriteErrorPersistsAcrossRestart 磁盘写失败 + DB 回退在重启后仍被识别。
func TestDurableSinkWriteErrorPersistsAcrossRestart(t *testing.T) {
	dir := t.TempDir()
	stderr := &stderrBuffer{}
	s := openTestSink(t, dir, testDurableOptions(stderr))
	db, _ := newDurableFakeDB(t)
	s.AttachDB(db)
	s.mu.Lock()
	_ = s.file.Close()
	s.mu.Unlock()
	if err := s.Write(context.Background(), durableTestEntry("disk-fail-persist")); err != nil {
		t.Fatalf("磁盘失败应回退 DB: %v", err)
	}
	closeSink(t, s)

	s2 := openTestSink(t, dir, testDurableOptions(stderr))
	defer closeSink(t, s2)
	if h := s2.Health(); h.WriteErrors == 0 || h.DiskFallbacks == 0 {
		t.Fatalf("重启后应保留磁盘失败/回退计数: %+v", h)
	}
}

// TestLogIDSetCompleteAndFailClosed 完整文件返回 log_id 集合；坏行/半行必须报错（保留清理失败关闭）。
func TestLogIDSetCompleteAndFailClosed(t *testing.T) {
	dir := t.TempDir()
	s := openTestSink(t, dir, testDurableOptions(&stderrBuffer{}))
	defer closeSink(t, s)
	old := filepath.Join(dir, "app-20200101-000001.log")
	contents := `{"log_id":"id-1","occurred_at":"2020-01-01T00:00:00Z"}` + "\n" +
		`{"log_id":"id-2","occurred_at":"2020-01-01T00:00:01Z"}` + "\n"
	if err := os.WriteFile(old, []byte(contents), 0o600); err != nil {
		t.Fatalf("写段: %v", err)
	}
	ids, err := s.LogIDSet(context.Background())
	if err != nil {
		t.Fatalf("完整文件不应报错: %v", err)
	}
	if _, ok := ids["id-1"]; !ok {
		t.Fatalf("缺少 id-1: %v", ids)
	}
	if _, ok := ids["id-2"]; !ok {
		t.Fatalf("缺少 id-2: %v", ids)
	}

	// 追加坏行：必须失败关闭。
	f, err := os.OpenFile(old, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatalf("追加打开: %v", err)
	}
	if _, err := f.WriteString("{bad json}\n"); err != nil {
		t.Fatalf("追加坏行: %v", err)
	}
	_ = f.Close()
	if _, err := s.LogIDSet(context.Background()); err == nil {
		t.Fatal("坏行必须让 LogIDSet 失败关闭")
	}

	// 半行（无换行）同样失败关闭。
	if err := os.WriteFile(old, []byte(contents+`{"log_id":"id-3"`), 0o600); err != nil {
		t.Fatalf("写半行: %v", err)
	}
	if _, err := s.LogIDSet(context.Background()); err == nil {
		t.Fatal("半行必须让 LogIDSet 失败关闭")
	}
}

// readActiveLogLines 读取目录内所有 .log 的完整行（测试辅助）。
func readActiveLogLines(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	var out []string
	for _, e := range entries {
		name := e.Name()
		if !strings.HasPrefix(name, "app-") || !strings.HasSuffix(name, ".log") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("ReadFile: %v", err)
		}
		for _, line := range strings.Split(string(data), "\n") {
			if strings.TrimSpace(line) != "" {
				out = append(out, line)
			}
		}
	}
	return out
}

// stderrBuffer 并发安全的 stderr 收集器（DurableSink 告警异步写）。
type stderrBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *stderrBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *stderrBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}
