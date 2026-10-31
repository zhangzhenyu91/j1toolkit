package logging

import (
	"bytes"
	"compress/gzip"
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"shade/internal/config"
)

// ---- 文件持久层假库：支持事务、按 log_id 幂等、可注入失败（纯标准库，不连真实库）----

const durableFakeCols = 16

type durableFakeDB struct {
	mu      sync.Mutex
	ids     map[string]bool
	rows    [][]driver.Value
	execErr error
	commits int
}

func (d *durableFakeDB) setExecErr(err error) {
	d.mu.Lock()
	d.execErr = err
	d.mu.Unlock()
}

func (d *durableFakeDB) checkErr() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.execErr
}

// apply 按 log_id 去重后落行（模拟唯一键 + ON DUPLICATE KEY UPDATE log_id=log_id）。
func (d *durableFakeDB) apply(args []driver.Value) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.execErr != nil {
		return d.execErr
	}
	for i := 0; i+durableFakeCols <= len(args); i += durableFakeCols {
		logID, _ := args[i+12].(string)
		if logID == "" {
			return errors.New("log_id 为空")
		}
		if d.ids[logID] {
			continue
		}
		d.ids[logID] = true
		d.rows = append(d.rows, append([]driver.Value(nil), args[i:i+durableFakeCols]...))
	}
	return nil
}

func (d *durableFakeDB) count() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.rows)
}

func (d *durableFakeDB) row(i int) []driver.Value {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]driver.Value(nil), d.rows[i]...)
}

func (d *durableFakeDB) commitCount() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.commits
}

type durableTx struct {
	conn    *durableConn
	pending []driver.Value
	done    bool
}

func (t *durableTx) Commit() error {
	if t.done {
		return nil
	}
	t.done = true
	t.conn.tx = nil
	if err := t.conn.db.checkErr(); err != nil {
		return err
	}
	if err := t.conn.db.apply(t.pending); err != nil {
		return err
	}
	t.conn.db.mu.Lock()
	t.conn.db.commits++
	t.conn.db.mu.Unlock()
	return nil
}

func (t *durableTx) Rollback() error {
	t.done = true
	t.conn.tx = nil
	return nil
}

type durableConn struct {
	db *durableFakeDB
	tx *durableTx
}

func (c *durableConn) Prepare(string) (driver.Stmt, error) { return nil, errors.New("unsupported") }
func (c *durableConn) Close() error                        { return nil }

func (c *durableConn) Begin() (driver.Tx, error) {
	c.tx = &durableTx{conn: c}
	return c.tx, nil
}

func (c *durableConn) BeginTx(context.Context, driver.TxOptions) (driver.Tx, error) {
	c.tx = &durableTx{conn: c}
	return c.tx, nil
}

func (c *durableConn) ExecContext(_ context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	vals := make([]driver.Value, len(args))
	for i, a := range args {
		vals[i] = a.Value
	}
	if !strings.Contains(query, "INSERT INTO app_log") {
		return fakeResult(1), nil
	}
	if c.tx != nil {
		if err := c.db.checkErr(); err != nil {
			return nil, err
		}
		c.tx.pending = append(c.tx.pending, vals...)
		return fakeResult(1), nil
	}
	if err := c.db.apply(vals); err != nil {
		return nil, err
	}
	return fakeResult(1), nil
}

type durableDriver struct{ db *durableFakeDB }

func (d durableDriver) Open(string) (driver.Conn, error) {
	return &durableConn{db: d.db}, nil
}

var durableSeq atomic.Int64

func newDurableFakeDB(t *testing.T) (*sql.DB, *durableFakeDB) {
	t.Helper()
	f := &durableFakeDB{ids: map[string]bool{}}
	name := fmt.Sprintf("durable-fake-%d", durableSeq.Add(1))
	sql.Register(name, durableDriver{db: f})
	db, err := sql.Open(name, "")
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db, f
}

// ---- 测试辅助 ----

func durableTestEntry(message string) config.AppLogEntry {
	return config.AppLogEntry{
		AppCode: "quiz", Level: "info", Category: "business", Event: "ev.test",
		Message: message, MessageCN: message, RawMessage: message,
	}
}

func testDurableOptions(stderr io.Writer) DurableOptions {
	return DurableOptions{
		BatchSize:      100,
		FsyncMode:      FsyncBatch,
		PollInterval:   5 * time.Millisecond,
		MaxSegmentSize: 64 << 20,
		Stderr:         stderr,
	}
}

func openTestSink(t *testing.T, dir string, opts DurableOptions) *DurableSink {
	t.Helper()
	s, err := NewDurableSinkWithOptions(dir, opts)
	if err != nil {
		t.Fatalf("NewDurableSinkWithOptions: %v", err)
	}
	return s
}

func closeSink(t *testing.T, s *DurableSink) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := s.CloseContext(ctx); err != nil {
		t.Fatalf("CloseContext: %v", err)
	}
}

func logFiles(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	var out []string
	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, "app-") && strings.HasSuffix(name, ".log") {
			out = append(out, filepath.Join(dir, name))
		}
	}
	sort.Strings(out)
	return out
}

func gzFiles(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	var out []string
	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, "app-") && strings.HasSuffix(name, ".log.gz") {
			out = append(out, filepath.Join(dir, name))
		}
	}
	sort.Strings(out)
	return out
}

func readLines(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile %s: %v", path, err)
	}
	text := strings.TrimSuffix(string(data), "\n")
	if text == "" {
		return nil
	}
	return strings.Split(text, "\n")
}

func allLogLines(t *testing.T, dir string) []string {
	t.Helper()
	var out []string
	for _, f := range logFiles(t, dir) {
		out = append(out, readLines(t, f)...)
	}
	return out
}

// ---- 无 DB / 高峰 / 事件时间 ----

func TestDurableSinkWritesNDJSONWithoutDB(t *testing.T) {
	dir := t.TempDir()
	s := openTestSink(t, dir, testDurableOptions(io.Discard))
	fixed := time.Date(2026, 10, 31, 8, 30, 0, 0, time.UTC)
	ctx := WithTraceID(context.Background(), "trace-file")
	if err := s.Write(ctx, config.AppLogEntry{
		AppCode: "work-log", Level: "info", Category: "business", Event: "ev.file",
		Message: "原始", MessageCN: "中文", RawMessage: "原始", Detail: `{"n":1}`,
		OccurredAt: fixed, UserID: 7, TeamID: 42,
	}); err != nil {
		t.Fatalf("Write: %v", err)
	}
	closeSink(t, s)

	lines := allLogLines(t, dir)
	if len(lines) != 1 {
		t.Fatalf("NDJSON 行数 = %d，期望 1", len(lines))
	}
	var rec map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &rec); err != nil {
		t.Fatalf("NDJSON 解析失败: %v\n%s", err, lines[0])
	}
	if rec["log_id"] == "" || rec["log_id"] == nil {
		t.Fatalf("缺 log_id：%v", rec)
	}
	if rec["trace_id"] != "trace-file" || rec["app_code"] != "work-log" || rec["event"] != "ev.file" {
		t.Fatalf("字段不符：%v", rec)
	}
	if rec["detail"] != `{"n":1}` {
		t.Fatalf("detail = %v", rec["detail"])
	}
	at, err := time.Parse(time.RFC3339Nano, rec["occurred_at"].(string))
	if err != nil || !at.Equal(fixed) {
		t.Fatalf("occurred_at = %v（%v），期望 %v", rec["occurred_at"], err, fixed)
	}
	if uid, _ := rec["user_id"].(float64); uid != 7 {
		t.Fatalf("user_id = %v", rec["user_id"])
	}
}

func TestDurableSinkNoDropUnderInfoPeak(t *testing.T) {
	dir := t.TempDir()
	s := openTestSink(t, dir, testDurableOptions(io.Discard))
	const workers, perWorker = 16, 200
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < perWorker; i++ {
				if err := s.Write(context.Background(), durableTestEntry(fmt.Sprintf("peak-%d-%d", w, i))); err != nil {
					t.Errorf("Write: %v", err)
					return
				}
			}
		}(w)
	}
	wg.Wait()
	closeSink(t, s)

	lines := allLogLines(t, dir)
	if len(lines) != workers*perWorker {
		t.Fatalf("高峰写入 %d 条，期望 %d（不得丢弃）", len(lines), workers*perWorker)
	}
	ids := map[string]bool{}
	for _, line := range lines {
		var rec map[string]any
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("半行/坏行: %v", err)
		}
		id, _ := rec["log_id"].(string)
		if id == "" || ids[id] {
			t.Fatalf("log_id 为空或重复: %q", id)
		}
		ids[id] = true
	}
	if h := s.Health(); h.WriteErrors != 0 || h.DiskFallbacks != 0 {
		t.Fatalf("无故障时健康统计异常: %+v", h)
	}
}

// ---- 入库 / 游标恢复 / 幂等 / 半行 ----

func TestDurableSinkImportsAndResumesFromCursor(t *testing.T) {
	dir := t.TempDir()
	opts := testDurableOptions(io.Discard)
	s1 := openTestSink(t, dir, opts)
	for i := 0; i < 3; i++ {
		if err := s1.Write(context.Background(), durableTestEntry(fmt.Sprintf("first-%d", i))); err != nil {
			t.Fatalf("Write: %v", err)
		}
	}
	db1, fake1 := newDurableFakeDB(t)
	s1.AttachDB(db1)
	if err := s1.Flush(context.Background()); err != nil {
		t.Fatalf("Flush: %v", err)
	}
	if got := fake1.count(); got != 3 {
		t.Fatalf("首次入库 %d 条，期望 3", got)
	}
	closeSink(t, s1)

	s2 := openTestSink(t, dir, opts)
	for i := 0; i < 2; i++ {
		if err := s2.Write(context.Background(), durableTestEntry(fmt.Sprintf("second-%d", i))); err != nil {
			t.Fatalf("Write: %v", err)
		}
	}
	db2, fake2 := newDurableFakeDB(t)
	s2.AttachDB(db2)
	if err := s2.Flush(context.Background()); err != nil {
		t.Fatalf("Flush: %v", err)
	}
	if got := fake2.count(); got != 2 {
		t.Fatalf("重启后应只补录新增 2 条，实际 %d（游标未恢复）", got)
	}
	closeSink(t, s2)
	if got := len(allLogLines(t, dir)); got != 5 {
		t.Fatalf("文件总行数 = %d，期望 5", got)
	}
}

func TestDurableSinkDuplicateReplayIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	opts := testDurableOptions(io.Discard)
	s1 := openTestSink(t, dir, opts)
	for i := 0; i < 3; i++ {
		if err := s1.Write(context.Background(), durableTestEntry(fmt.Sprintf("dup-%d", i))); err != nil {
			t.Fatalf("Write: %v", err)
		}
	}
	db, fake := newDurableFakeDB(t)
	s1.AttachDB(db)
	if err := s1.Flush(context.Background()); err != nil {
		t.Fatalf("Flush: %v", err)
	}
	if got := fake.count(); got != 3 {
		t.Fatalf("首次入库 %d 条", got)
	}
	closeSink(t, s1)

	// 模拟游标丢失/回退：从头重放同一批文件。
	if err := os.WriteFile(filepath.Join(dir, "cursor.json"), []byte(`{"segment":"","offset":0}`), 0o600); err != nil {
		t.Fatalf("重置游标: %v", err)
	}
	s2 := openTestSink(t, dir, opts)
	s2.AttachDB(db)
	if err := s2.Flush(context.Background()); err != nil {
		t.Fatalf("重复 Flush: %v", err)
	}
	closeSink(t, s2)
	if got := fake.count(); got != 3 {
		t.Fatalf("重复回放后行数 = %d，期望仍为 3（log_id 幂等）", got)
	}
}

func TestDurableSinkPartialLineNotConsumed(t *testing.T) {
	dir := t.TempDir()
	s := openTestSink(t, dir, testDurableOptions(io.Discard))
	defer closeSink(t, s)
	if err := s.Write(context.Background(), durableTestEntry("complete")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	files := logFiles(t, dir)
	if len(files) != 1 {
		t.Fatalf("活跃段数 = %d", len(files))
	}
	full := `{"log_id":"partial-log-1","occurred_at":"2026-10-31T08:30:00Z","app_code":"quiz","level":"info","category":"business","event":"ev.partial","message":"partial","message_cn":"partial","raw_message":"partial","context_json":"","trace_id":"","user_id":0,"team_id":0,"detail":"","source":"","is_access":false}` + "\n"
	half := len(full) / 2
	appendToFile := func(text string) {
		f, err := os.OpenFile(files[0], os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			t.Fatalf("OpenFile: %v", err)
		}
		if _, err := f.WriteString(text); err != nil {
			t.Fatalf("追加文件: %v", err)
		}
		_ = f.Close()
	}
	appendToFile(full[:half])

	db, fake := newDurableFakeDB(t)
	s.AttachDB(db)
	if err := s.Flush(context.Background()); err != nil {
		t.Fatalf("Flush: %v", err)
	}
	if got := fake.count(); got != 1 {
		t.Fatalf("半行不应入库，实际 %d 条", got)
	}
	appendToFile(full[half:])
	if err := s.Flush(context.Background()); err != nil {
		t.Fatalf("补全后 Flush: %v", err)
	}
	if got := fake.count(); got != 2 {
		t.Fatalf("补全后应入库 2 条，实际 %d", got)
	}
}

func TestDurableSinkPreservesOccurredAtToDB(t *testing.T) {
	dir := t.TempDir()
	s := openTestSink(t, dir, testDurableOptions(io.Discard))
	fixed := time.Date(2026, 10, 31, 8, 30, 0, 0, time.Local)
	if err := s.Write(context.Background(), config.AppLogEntry{
		AppCode: "system", Message: "at", OccurredAt: fixed,
	}); err != nil {
		t.Fatalf("Write: %v", err)
	}
	db, fake := newDurableFakeDB(t)
	s.AttachDB(db)
	if err := s.Flush(context.Background()); err != nil {
		t.Fatalf("Flush: %v", err)
	}
	closeSink(t, s)
	if fake.count() != 1 {
		t.Fatalf("入库 %d 条", fake.count())
	}
	at, ok := fake.row(0)[13].(time.Time)
	if !ok || !at.Equal(fixed) {
		t.Fatalf("occurred_at = %v，期望 %v", fake.row(0)[13], fixed)
	}
}

func TestDurableSinkDBFailureKeepsCursor(t *testing.T) {
	dir := t.TempDir()
	opts := testDurableOptions(io.Discard)
	s := openTestSink(t, dir, opts)
	if err := s.Write(context.Background(), durableTestEntry("retry")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	db, fake := newDurableFakeDB(t)
	fake.setExecErr(errors.New("db down"))
	s.AttachDB(db)
	if err := s.Flush(context.Background()); err == nil {
		t.Fatal("DB 故障时 Flush 应返回错误")
	}
	if got := s.Health().CursorOffset; got != 0 {
		t.Fatalf("DB 失败后游标不应推进，实际 %d", got)
	}
	if got := len(allLogLines(t, dir)); got != 1 {
		t.Fatalf("DB 故障时文件仍应保留 1 行，实际 %d", got)
	}
	fake.setExecErr(nil)
	if err := s.Flush(context.Background()); err != nil {
		t.Fatalf("恢复后 Flush: %v", err)
	}
	if got := fake.count(); got != 1 {
		t.Fatalf("恢复后入库 %d 条", got)
	}
	closeSink(t, s)
}

// ---- 磁盘失败回退 DB + stderr 告警 ----

func TestDurableSinkDiskFailureFallsBackToDB(t *testing.T) {
	dir := t.TempDir()
	stderr := &bytes.Buffer{}
	s := openTestSink(t, dir, testDurableOptions(stderr))
	db, fake := newDurableFakeDB(t)
	s.AttachDB(db)
	if err := s.Write(context.Background(), durableTestEntry("fallback")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if err := s.Flush(context.Background()); err != nil {
		t.Fatalf("Flush: %v", err)
	}

	s.mu.Lock()
	if s.file != nil {
		_ = s.file.Close()
	}
	s.mu.Unlock()
	if err := s.Write(context.Background(), durableTestEntry("disk-fail")); err != nil {
		t.Fatalf("磁盘失败应回退 DB 并返回 nil：%v", err)
	}
	h := s.Health()
	if h.DiskFallbacks != 1 || h.WriteErrors != 1 {
		t.Fatalf("回退统计不符：%+v", h)
	}
	if !strings.Contains(stderr.String(), "回退") {
		t.Fatalf("磁盘失败未 stderr 告警：%s", stderr.String())
	}
	if got := fake.count(); got != 2 {
		t.Fatalf("DB 行数 = %d，期望 2（含回退直写）", got)
	}
	closeSink(t, s)
}

// ---- fsync 策略 ----

func TestDurableSinkFsyncBatchOnlyForUrgent(t *testing.T) {
	dir := t.TempDir()
	s := openTestSink(t, dir, testDurableOptions(io.Discard))
	defer closeSink(t, s)
	if err := s.Write(context.Background(), durableTestEntry("info")); err != nil {
		t.Fatalf("Write info: %v", err)
	}
	if got := s.Health().Syncs; got != 0 {
		t.Fatalf("batch 模式 info 不应每条 fsync，Syncs=%d", got)
	}
	if err := s.Write(context.Background(), config.AppLogEntry{Level: "warn", Message: "warn"}); err != nil {
		t.Fatalf("Write warn: %v", err)
	}
	if got := s.Health().Syncs; got != 1 {
		t.Fatalf("warn 应每条 fsync，Syncs=%d", got)
	}
	if err := s.Write(context.Background(), config.AppLogEntry{Level: "info", Category: "audit", Message: "audit"}); err != nil {
		t.Fatalf("Write audit: %v", err)
	}
	if got := s.Health().Syncs; got != 2 {
		t.Fatalf("audit 应每条 fsync，Syncs=%d", got)
	}
}

func TestDurableSinkFsyncAlways(t *testing.T) {
	dir := t.TempDir()
	opts := testDurableOptions(io.Discard)
	opts.FsyncMode = FsyncAlways
	s := openTestSink(t, dir, opts)
	defer closeSink(t, s)
	if err := s.Write(context.Background(), durableTestEntry("info")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if got := s.Health().Syncs; got != 1 {
		t.Fatalf("always 模式每条 fsync，Syncs=%d", got)
	}
}

// ---- 分段 / 归档 ----

func TestDurableSinkRotatesAndCompressesSealedSegments(t *testing.T) {
	dir := t.TempDir()
	stderr := &bytes.Buffer{}
	opts := testDurableOptions(stderr)
	opts.MaxSegmentSize = 200
	s := openTestSink(t, dir, opts)
	for i := 0; i < 10; i++ {
		if err := s.Write(context.Background(), durableTestEntry(fmt.Sprintf("rotate-%d", i))); err != nil {
			t.Fatalf("Write: %v", err)
		}
	}
	db, fake := newDurableFakeDB(t)
	s.AttachDB(db)
	if err := s.Flush(context.Background()); err != nil {
		t.Fatalf("Flush: %v", err)
	}
	if fake.count() != 10 {
		t.Fatalf("入库 %d 条，期望 10；stderr=%s", fake.count(), stderr.String())
	}
	if len(gzFiles(t, dir)) == 0 {
		t.Fatal("已读尽的封存段应压缩为 .log.gz")
	}
	if len(logFiles(t, dir)) == 0 {
		t.Fatal("活跃段必须保持未压缩")
	}
	closeSink(t, s)

	// 重启后从游标继续，归档段按 gzip 读取且不重复。
	s2 := openTestSink(t, dir, opts)
	defer closeSink(t, s2)
	if err := s2.Write(context.Background(), durableTestEntry("after-rotate")); err != nil {
		t.Fatalf("Write after restart: %v", err)
	}
	db2, fake2 := newDurableFakeDB(t)
	s2.AttachDB(db2)
	if err := s2.Flush(context.Background()); err != nil {
		t.Fatalf("Flush after restart: %v", err)
	}
	if got := fake2.count(); got != 1 {
		t.Fatalf("重启后新增入库 %d 条，期望 1", got)
	}
}

func TestDurableSinkCleanupArchivesOnlyWhenPositive(t *testing.T) {
	dir := t.TempDir()
	old := filepath.Join(dir, "app-20200101-000001.log.gz")
	var gz bytes.Buffer
	zw := gzip.NewWriter(&gz)
	if _, err := zw.Write([]byte(`{"log_id":"old-archive-1","occurred_at":"2020-01-01T00:00:00Z","app_code":"system","level":"info","category":"business","event":"ev.old","message":"old","message_cn":"old","raw_message":"old","context_json":"","trace_id":"","user_id":0,"team_id":0,"detail":"","source":"","is_access":false}` + "\n")); err != nil {
		t.Fatalf("gzip 写入: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("gzip close: %v", err)
	}
	if err := os.WriteFile(old, gz.Bytes(), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	oldTime := time.Now().AddDate(0, 0, -100)
	if err := os.Chtimes(old, oldTime, oldTime); err != nil {
		t.Fatalf("Chtimes: %v", err)
	}
	s := openTestSink(t, dir, testDurableOptions(io.Discard))
	defer closeSink(t, s)
	// 先把游标推进过归档段，归档才可被保留策略删除。
	db, _ := newDurableFakeDB(t)
	s.AttachDB(db)
	if err := s.Flush(context.Background()); err != nil {
		t.Fatalf("Flush: %v", err)
	}

	oldest, ok := s.ArchiveCoverage()
	if !ok || oldest.Year() != 2020 {
		t.Fatalf("归档覆盖 = %v/%v", oldest, ok)
	}
	if n, err := s.CleanupArchives(0); err != nil || n != 0 {
		t.Fatalf("保留 0 应永久保留：%d/%v", n, err)
	}
	if _, err := os.Stat(old); err != nil {
		t.Fatalf("永久保留时归档被删除：%v", err)
	}
	if n, err := s.CleanupArchives(-1); err == nil || n != 0 {
		t.Fatalf("非法保留期应报错且不删除：%d/%v", n, err)
	}
	n, err := s.CleanupArchives(30)
	if err != nil || n != 1 {
		t.Fatalf("CleanupArchives(30) = %d/%v", n, err)
	}
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Fatalf("过期归档未删除：%v", err)
	}
	if len(logFiles(t, dir)) != 1 {
		t.Fatal("活跃段不得被归档清理删除")
	}
}

// ---- 健康 / 关闭 / 权限 ----

func TestDurableSinkHealthAndClose(t *testing.T) {
	dir := t.TempDir()
	stderr := &bytes.Buffer{}
	s := openTestSink(t, dir, testDurableOptions(stderr))
	if err := s.Write(context.Background(), durableTestEntry("health")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	db, fake := newDurableFakeDB(t)
	s.AttachDB(db)
	s.AttachDB(db) // 重复 Attach 不得启动第二个 Importer
	if err := s.Flush(context.Background()); err != nil {
		t.Fatalf("Flush: %v", err)
	}
	h := s.Health()
	if !h.DBConnected || h.Imported != 1 || h.ActiveSegment == "" {
		t.Fatalf("健康统计不符：%+v", h)
	}
	if fake.commitCount() != 1 {
		t.Fatalf("提交次数 = %d，期望 1（唯一 Importer）", fake.commitCount())
	}
	closeSink(t, s)
	if err := s.Write(context.Background(), durableTestEntry("after-close")); !errors.Is(err, ErrSinkClosed) {
		t.Fatalf("关闭后写入应返回 ErrSinkClosed，得到 %v", err)
	}
	closeSink(t, s) // 幂等
}

func TestDurableSinkPermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows 不保证 POSIX 权限位")
	}
	dir := filepath.Join(t.TempDir(), "logs")
	s := openTestSink(t, dir, testDurableOptions(io.Discard))
	defer closeSink(t, s)
	di, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("Stat dir: %v", err)
	}
	if di.Mode().Perm()&0o077 != 0 {
		t.Fatalf("目录权限 = %o，期望 0700", di.Mode().Perm())
	}
	files := logFiles(t, dir)
	if len(files) == 0 {
		t.Fatal("缺少活跃段")
	}
	fi, err := os.Stat(files[0])
	if err != nil {
		t.Fatalf("Stat file: %v", err)
	}
	if fi.Mode().Perm()&0o077 != 0 {
		t.Fatalf("文件权限 = %o，期望 0600", fi.Mode().Perm())
	}
}

func TestDurableSinkCanceledRequestContextStillPersists(t *testing.T) {
	dir := t.TempDir()
	s := openTestSink(t, dir, testDurableOptions(io.Discard))
	defer closeSink(t, s)
	ctx, cancel := context.WithCancel(WithTraceID(context.Background(), "trace-canceled"))
	cancel()
	if err := s.Write(ctx, durableTestEntry("canceled")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	lines := allLogLines(t, dir)
	if len(lines) != 1 || !strings.Contains(lines[0], "trace-canceled") {
		t.Fatalf("取消 ctx 后未落文件：%v", lines)
	}
}

// ---- fields 不可序列化不丢整条 ----

func TestLoggerKeepsEntryWhenFieldsUnmarshalable(t *testing.T) {
	dir := t.TempDir()
	s := openTestSink(t, dir, testDurableOptions(io.Discard))
	l := NewWithSink(s, "quiz")
	if err := l.Info(context.Background(), "business", "ev.bad", "中文", "raw",
		map[string]any{"fn": func() {}}); err != nil {
		t.Fatalf("不可序列化 fields 不应导致整条丢失：%v", err)
	}
	closeSink(t, s)
	lines := allLogLines(t, dir)
	if len(lines) != 1 {
		t.Fatalf("文件行数 = %d，期望 1", len(lines))
	}
	if !strings.Contains(lines[0], "_marshal_error") {
		t.Fatalf("detail 未记录序列化失败：%s", lines[0])
	}
}

// ---- 启动期 env ----

func TestDurableOptionsFromEnv(t *testing.T) {
	t.Setenv("APP_LOG_BATCH_SIZE", "")
	t.Setenv("APP_LOG_FSYNC", "")
	opts := DurableOptionsFromEnv()
	if opts.BatchSize != 100 || opts.FsyncMode != FsyncBatch {
		t.Fatalf("默认选项 = %+v", opts)
	}
	t.Setenv("APP_LOG_BATCH_SIZE", "250")
	t.Setenv("APP_LOG_FSYNC", "always")
	opts = DurableOptionsFromEnv()
	if opts.BatchSize != 250 || opts.FsyncMode != FsyncAlways {
		t.Fatalf("env 覆盖 = %+v", opts)
	}
	t.Setenv("APP_LOG_BATCH_SIZE", "0")
	t.Setenv("APP_LOG_FSYNC", "weird")
	opts = DurableOptionsFromEnv()
	if opts.BatchSize != 100 || opts.FsyncMode != FsyncBatch {
		t.Fatalf("非法 env 应回退默认 = %+v", opts)
	}
}

// durableJSONLine 生成合法 NDJSON 行（测试手工构造段文件用）。
func durableJSONLine(logID, message string) string {
	rec := durableRecord{
		LogID: logID, OccurredAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		AppCode: "quiz", Level: "info", Category: "business", Event: "ev.test",
		Message: message, MessageCN: message, RawMessage: message,
	}
	data, _ := json.Marshal(rec)
	return string(data) + "\n"
}

// TestDurableSinkSealedPartialTailDoesNotBlockLaterSegments 封存段尾部不可恢复半行
// 必须被跳过并推进游标，否则后续活跃段永远不补录。
func TestDurableSinkSealedPartialTailDoesNotBlockLaterSegments(t *testing.T) {
	dir := t.TempDir()
	sealed := filepath.Join(dir, "app-20200101-000001.log")
	content := durableJSONLine("sealed-complete", "sealed") + `{"log_id":"torn-half`
	if err := os.WriteFile(sealed, []byte(content), 0o600); err != nil {
		t.Fatalf("写封存段: %v", err)
	}
	s := openTestSink(t, dir, testDurableOptions(io.Discard))
	db, fake := newDurableFakeDB(t)
	s.AttachDB(db)
	if err := s.Flush(context.Background()); err != nil {
		t.Fatalf("Flush: %v", err)
	}
	if got := fake.count(); got != 1 {
		t.Fatalf("封存段完整行入库 %d 条，期望 1", got)
	}
	if err := s.Write(context.Background(), durableTestEntry("after-sealed")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if err := s.Flush(context.Background()); err != nil {
		t.Fatalf("第二次 Flush: %v", err)
	}
	if got := fake.count(); got != 2 {
		t.Fatalf("半行封存段阻塞后续段：入库 %d 条，期望 2", got)
	}
	closeSink(t, s)
}

// TestDurableSinkRepairsActivePartialTailOnRestart 重启复用活跃段前必须截断尾部半行，
// 否则新日志直接拼接成坏行；修复后继续写新日志并能进入后续段。
func TestDurableSinkRepairsActivePartialTailOnRestart(t *testing.T) {
	dir := t.TempDir()
	opts := testDurableOptions(io.Discard)
	opts.MaxSegmentSize = 1024
	s1 := openTestSink(t, dir, opts)
	if err := s1.Write(context.Background(), durableTestEntry("first")); err != nil {
		t.Fatalf("Write first: %v", err)
	}
	closeSink(t, s1)
	files := logFiles(t, dir)
	if len(files) != 1 {
		t.Fatalf("活跃段数 = %d", len(files))
	}
	f, err := os.OpenFile(files[0], os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatalf("OpenFile: %v", err)
	}
	if _, err := f.WriteString(`{"log_id":"torn`); err != nil {
		t.Fatalf("写半行: %v", err)
	}
	_ = f.Close()

	s2 := openTestSink(t, dir, opts)
	if err := s2.Write(context.Background(), durableTestEntry("second")); err != nil {
		t.Fatalf("Write second: %v", err)
	}
	if err := s2.Write(context.Background(), durableTestEntry(strings.Repeat("x", 800))); err != nil {
		t.Fatalf("Write big: %v", err)
	}
	if err := s2.Write(context.Background(), durableTestEntry("fourth")); err != nil {
		t.Fatalf("Write fourth: %v", err)
	}
	lines := allLogLines(t, dir)
	if len(lines) != 4 {
		t.Fatalf("修复后文件行数 = %d，期望 4：%v", len(lines), lines)
	}
	for i, line := range lines {
		var rec map[string]any
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("第 %d 行不是合法 JSON（半行未修复）: %v\n%s", i+1, err, line)
		}
	}
	db, fake := newDurableFakeDB(t)
	s2.AttachDB(db)
	if err := s2.Flush(context.Background()); err != nil {
		t.Fatalf("Flush: %v", err)
	}
	if got := fake.count(); got != 4 {
		t.Fatalf("修复后半行重启应入库 4 条，实际 %d", got)
	}
	closeSink(t, s2)
	if len(gzFiles(t, dir)) == 0 {
		t.Fatal("修复后继续写入应产生已读尽并压缩的后续段")
	}
	if len(logFiles(t, dir)) == 0 {
		t.Fatal("活跃段必须保持未压缩")
	}
}

// TestDurableSinkPendingAfterRestart Pending 必须按游标/文件实际未导入状态计算。
func TestDurableSinkPendingAfterRestart(t *testing.T) {
	dir := t.TempDir()
	opts := testDurableOptions(io.Discard)
	s1 := openTestSink(t, dir, opts)
	for i := 0; i < 3; i++ {
		if err := s1.Write(context.Background(), durableTestEntry(fmt.Sprintf("pending-%d", i))); err != nil {
			t.Fatalf("Write: %v", err)
		}
	}
	if got := s1.Health().Pending; got != 3 {
		t.Fatalf("关闭前 Pending = %d，期望 3", got)
	}
	closeSink(t, s1)

	s2 := openTestSink(t, dir, opts)
	if got := s2.Health().Pending; got != 3 {
		t.Fatalf("重启后 Pending = %d，期望 3（按文件/游标实际未导入）", got)
	}
	db, fake := newDurableFakeDB(t)
	s2.AttachDB(db)
	if err := s2.Flush(context.Background()); err != nil {
		t.Fatalf("Flush: %v", err)
	}
	if got := s2.Health().Pending; got != 0 {
		t.Fatalf("补录后 Pending = %d，期望 0", got)
	}
	if fake.count() != 3 {
		t.Fatalf("入库 %d 条", fake.count())
	}
	closeSink(t, s2)
}

// TestDurableSinkWriteNilContextFallsBackWithoutPanic nil ctx 不得 panic（含磁盘失败回退）。
func TestDurableSinkWriteNilContextFallsBackWithoutPanic(t *testing.T) {
	dir := t.TempDir()
	s := openTestSink(t, dir, testDurableOptions(io.Discard))
	db, fake := newDurableFakeDB(t)
	s.AttachDB(db)
	s.mu.Lock()
	if s.file != nil {
		_ = s.file.Close()
	}
	s.mu.Unlock()
	if err := s.Write(nil, durableTestEntry("nil-ctx")); err != nil {
		t.Fatalf("nil ctx Write: %v", err)
	}
	if got := fake.count(); got != 1 {
		t.Fatalf("回退直写 %d 条，期望 1", got)
	}
	closeSink(t, s)
}

// TestDurableSinkSegmentDays 列出 .log/.log.gz 覆盖日期（不含游标/无关文件）。
func TestDurableSinkSegmentDays(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"app-20200101-000001.log.gz", "app-20200103-000001.log"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(""), 0o600); err != nil {
			t.Fatalf("写段文件: %v", err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "unrelated.txt"), []byte("x"), 0o600); err != nil {
		t.Fatalf("写无关文件: %v", err)
	}
	s := openTestSink(t, dir, testDurableOptions(io.Discard))
	defer closeSink(t, s)
	days, err := s.SegmentDays()
	if err != nil {
		t.Fatalf("SegmentDays: %v", err)
	}
	for _, want := range []string{"2020-01-01", "2020-01-03"} {
		if !days[want] {
			t.Fatalf("缺少覆盖日期 %s：%v", want, days)
		}
	}
	if days["unrelated"] {
		t.Fatalf("无关文件进入覆盖集合：%v", days)
	}
}

// TestDurableSinkUrgentFsyncDuringRotationNoFalseAlarm 轮换与批量 fsync 并发不得产生假告警。
func TestDurableSinkUrgentFsyncDuringRotationNoFalseAlarm(t *testing.T) {
	dir := t.TempDir()
	stderr := &bytes.Buffer{}
	opts := testDurableOptions(stderr)
	opts.MaxSegmentSize = 128
	s := openTestSink(t, dir, opts)
	db, _ := newDurableFakeDB(t)
	s.AttachDB(db)
	for i := 0; i < 100; i++ {
		if err := s.Write(context.Background(), config.AppLogEntry{
			Level: "warn", Category: "business", Event: "ev.sync", Message: fmt.Sprintf("sync-%d", i),
		}); err != nil {
			t.Fatalf("Write: %v", err)
		}
	}
	closeSink(t, s)
	if strings.Contains(stderr.String(), "fsync 失败") {
		t.Fatalf("轮换/同步并发产生假告警：%s", stderr.String())
	}
}
