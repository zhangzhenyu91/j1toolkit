package config

import (
	"context"
	"database/sql/driver"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

// schemaState 模拟老库 app_log 的列/索引与回填进度（information_schema 查询结果）。
type schemaState struct {
	mu        sync.Mutex
	columns   map[string]bool
	indexes   map[string]bool
	alters    []string
	occurredN int64
	accessN   int64
	nullable  bool
	modified  bool
}

func newSchemaState(old bool) *schemaState {
	s := &schemaState{columns: map[string]bool{}, indexes: map[string]bool{}}
	full := []string{"id", "app_code", "level", "category", "event", "message", "message_cn",
		"raw_message", "context_json", "trace_id", "user_id", "team_id", "detail", "created_at"}
	for _, c := range full {
		s.columns[c] = true
	}
	if !old {
		s.columns["log_id"] = true
		s.columns["occurred_at"] = true
		s.columns["source"] = true
		s.columns["is_access"] = true
	} else {
		s.nullable = true
	}
	for _, idx := range []string{"PRIMARY", "idx_app_log_created", "idx_app_log_app_created", "idx_app_log_level_created", "idx_app_log_category_created"} {
		s.indexes[idx] = true
	}
	if !old {
		for _, idx := range []string{"uk_app_log_log_id", "idx_app_log_trace", "idx_app_log_occurred", "idx_app_log_access_occurred"} {
			s.indexes[idx] = true
		}
	}
	return s
}

func (s *schemaState) wire(fdb *fakeDB) {
	fdb.onQuery = func(q string, _ []driver.Value) (*fakeRows, error) {
		s.mu.Lock()
		defer s.mu.Unlock()
		switch {
		case strings.Contains(q, "IS_NULLABLE"):
			nullable := "NO"
			if s.nullable {
				nullable = "YES"
			}
			return rowsOf([]string{"IS_NULLABLE"}, []driver.Value{nullable}), nil
		case strings.Contains(q, "information_schema.COLUMNS"):
			names := make([]string, 0, len(s.columns))
			for n := range s.columns {
				names = append(names, n)
			}
			sort.Strings(names)
			rows := make([][]driver.Value, 0, len(names))
			for _, n := range names {
				rows = append(rows, []driver.Value{n})
			}
			return rowsOf([]string{"COLUMN_NAME"}, rows...), nil
		case strings.Contains(q, "information_schema.STATISTICS"):
			names := make([]string, 0, len(s.indexes))
			for n := range s.indexes {
				names = append(names, n)
			}
			sort.Strings(names)
			rows := make([][]driver.Value, 0, len(names))
			for _, n := range names {
				rows = append(rows, []driver.Value{n})
			}
			return rowsOf([]string{"INDEX_NAME"}, rows...), nil
		}
		return rowsOf([]string{"v"}), nil
	}
	fdb.onExec = func(q string, _ []driver.Value) (driver.Result, error) {
		s.mu.Lock()
		defer s.mu.Unlock()
		switch {
		case strings.Contains(q, "ALTER TABLE app_log ADD COLUMN"):
			s.alters = append(s.alters, q)
			name := strings.Fields(strings.SplitN(q, "ADD COLUMN ", 2)[1])[0]
			s.columns[name] = true
			return fakeResult(0), nil
		case strings.Contains(q, "ALTER TABLE app_log ADD UNIQUE KEY"):
			s.alters = append(s.alters, q)
			s.indexes["uk_app_log_log_id"] = true
			return fakeResult(0), nil
		case strings.Contains(q, "ALTER TABLE app_log ADD INDEX"):
			s.alters = append(s.alters, q)
			rest := strings.SplitN(q, "ADD INDEX ", 2)[1]
			s.indexes[strings.Fields(rest)[0]] = true
			return fakeResult(0), nil
		case strings.Contains(q, "UPDATE app_log SET occurred_at"):
			n := s.occurredN
			s.occurredN = 0
			return fakeResult(n), nil
		case strings.Contains(q, "UPDATE app_log SET is_access"):
			n := s.accessN
			s.accessN = 0
			return fakeResult(n), nil
		case strings.Contains(q, "ALTER TABLE app_log MODIFY"):
			s.nullable = false
			s.modified = true
			return fakeResult(0), nil
		}
		return fakeResult(1), nil
	}
}

// TestMigrateAppLogSchemaIdempotent 老库补列/补索引/分批回填 occurred_at 与 access 标记，
// 重复执行不重复 ADD COLUMN / ADD INDEX。
func TestMigrateAppLogSchemaIdempotent(t *testing.T) {
	sdb, fdb := openFake(t)
	state := newSchemaState(true)
	state.occurredN = 7
	state.accessN = 3
	state.wire(fdb)

	ctx := context.Background()
	if err := EnsureAppLogSchema(ctx, sdb); err != nil {
		t.Fatalf("EnsureAppLogSchema: %v", err)
	}
	if err := BackfillAppLog(ctx, sdb, 5); err != nil {
		t.Fatalf("BackfillAppLog: %v", err)
	}
	state.mu.Lock()
	for _, col := range []string{"log_id", "occurred_at", "source", "is_access"} {
		if !state.columns[col] {
			t.Fatalf("缺少列 %q", col)
		}
	}
	for _, idx := range []string{"uk_app_log_log_id", "idx_app_log_trace", "idx_app_log_occurred", "idx_app_log_access_occurred"} {
		if !state.indexes[idx] {
			t.Fatalf("缺少索引 %q", idx)
		}
	}
	if !state.modified {
		t.Fatal("回填完成后应把 occurred_at 改为 NOT NULL")
	}
	first := len(state.alters)
	state.mu.Unlock()

	if err := EnsureAppLogSchema(ctx, sdb); err != nil {
		t.Fatalf("重复 EnsureAppLogSchema: %v", err)
	}
	if err := BackfillAppLog(ctx, sdb, 5); err != nil {
		t.Fatalf("重复 BackfillAppLog: %v", err)
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if len(state.alters) != first {
		t.Fatalf("重复迁移又执行了 %d 条 ALTER", len(state.alters)-first)
	}
}

// TestEnsureAppLogSchemaFreshNoAlter 全新建表后无需补列。
func TestEnsureAppLogSchemaFreshNoAlter(t *testing.T) {
	sdb, fdb := openFake(t)
	state := newSchemaState(false)
	state.wire(fdb)
	if err := EnsureAppLogSchema(context.Background(), sdb); err != nil {
		t.Fatalf("EnsureAppLogSchema: %v", err)
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if len(state.alters) != 0 {
		t.Fatalf("全新库不应执行 ALTER：%v", state.alters)
	}
}

// TestInsertAppLogBatch 批量事务 + ON DUPLICATE KEY UPDATE log_id=log_id，禁止 INSERT IGNORE。
func TestInsertAppLogBatch(t *testing.T) {
	sdb, fdb := openFake(t)
	fixed := time.Date(2026, 10, 31, 8, 30, 0, 0, time.Local)
	entries := []AppLogEntry{
		{LogID: "l-1", OccurredAt: fixed, AppCode: "work-log", Level: "warn", Message: "m1", UserID: 7},
		{LogID: "l-2", AppCode: "system", Category: "access", Message: "m2", Detail: `{"a":1}`},
		{AppCode: "quiz", Message: "m3"},
	}
	if err := InsertAppLogBatch(context.Background(), sdb, entries); err != nil {
		t.Fatalf("InsertAppLogBatch: %v", err)
	}
	ins := fdb.execsWith("INSERT INTO app_log")
	if len(ins) != 1 {
		t.Fatalf("批量写入次数 = %d，期望 1", len(ins))
	}
	q := ins[0].query
	if !strings.Contains(q, "ON DUPLICATE KEY UPDATE log_id=log_id") {
		t.Fatalf("缺少幂等更新：%s", q)
	}
	if strings.Contains(q, "INSERT IGNORE") {
		t.Fatalf("禁止使用 INSERT IGNORE：%s", q)
	}
	args := ins[0].args
	const cols = 16
	if len(args) != cols*len(entries) {
		t.Fatalf("参数数 = %d，期望 %d", len(args), cols*len(entries))
	}
	if id, _ := args[12].(string); id != "l-1" {
		t.Fatalf("log_id[0] = %v", args[12])
	}
	if at, ok := args[13].(time.Time); !ok || !at.Equal(fixed) {
		t.Fatalf("occurred_at[0] = %v，期望 %v", args[13], fixed)
	}
	if got, _ := args[15].(int64); got != 0 {
		t.Fatalf("is_access[0] = %v", args[15])
	}
	if id, _ := args[12+cols].(string); id != "l-2" {
		t.Fatalf("log_id[1] = %v", args[12+cols])
	}
	if got, _ := args[15+cols].(int64); got != 1 {
		t.Fatalf("access 类别应自动标记 is_access=1，得到 %v", args[15+cols])
	}
	if id, _ := args[12+2*cols].(string); id == "" {
		t.Fatal("缺省 log_id 应自动生成")
	}
	if at, ok := args[13+2*cols].(time.Time); !ok || at.IsZero() {
		t.Fatalf("缺省 occurred_at 应回填为当前时间，得到 %v", args[13+2*cols])
	}
	if fdb.commits != 1 || fdb.rollbacks != 0 {
		t.Fatalf("事务提交 %d / 回滚 %d，期望 1/0", fdb.commits, fdb.rollbacks)
	}
}

// TestInsertAppLogBatchEmpty 空批次不开启事务。
func TestInsertAppLogBatchEmpty(t *testing.T) {
	sdb, fdb := openFake(t)
	if err := InsertAppLogBatch(context.Background(), sdb, nil); err != nil {
		t.Fatalf("空批次应成功：%v", err)
	}
	if len(fdb.execsWith("INSERT INTO app_log")) != 0 {
		t.Fatal("空批次不应写入")
	}
}

// TestNewAppLogIDUnique 日志 ID 非空且唯一，长度不超过列上限。
func TestNewAppLogIDUnique(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 2000; i++ {
		id := NewAppLogID()
		if id == "" || len(id) > 64 {
			t.Fatalf("非法 log_id：%q", id)
		}
		if seen[id] {
			t.Fatalf("log_id 重复：%q", id)
		}
		seen[id] = true
	}
}

// TestParseAppLogRetention 0/空表示永久；正数 1-3650；非法值报错（绝不回退 7 天）。
func TestParseAppLogRetention(t *testing.T) {
	for _, raw := range []string{"", "0", " 0 "} {
		n, err := ParseAppLogRetention(raw)
		if err != nil || n != 0 {
			t.Fatalf("%q 应解析为永久 0，得到 %d/%v", raw, n, err)
		}
	}
	for _, raw := range []string{"1", "7", "3650"} {
		n, err := ParseAppLogRetention(raw)
		if err != nil || n == 0 {
			t.Fatalf("%q 应解析为正数，得到 %d/%v", raw, n, err)
		}
	}
	for _, raw := range []string{"-1", "3651", "abc", "7天", "1.5"} {
		if n, err := ParseAppLogRetention(raw); err == nil {
			t.Fatalf("%q 应报错，得到 %d", raw, n)
		}
	}
}

// TestCleanupAppLogForever 保留 0 表示永久，绝不 DELETE；非法配置也不删除。
func TestCleanupAppLogForever(t *testing.T) {
	sdb, fdb := openFake(t)
	n, err := CleanupAppLog(context.Background(), sdb, 0)
	if err != nil || n != 0 {
		t.Fatalf("保留 0 应永久保留，得到 %d/%v", n, err)
	}
	if len(fdb.execsWith("DELETE FROM app_log")) != 0 {
		t.Fatal("保留 0 不得执行 DELETE")
	}
	for _, invalid := range []int{-1, 3651} {
		if _, err := CleanupAppLog(context.Background(), sdb, invalid); err == nil {
			t.Fatalf("保留 %d 天应报错", invalid)
		}
		if len(fdb.execsWith("DELETE FROM app_log")) != 0 {
			t.Fatalf("非法配置 %d 不得删除", invalid)
		}
	}
}

// TestCleanupAppLogUsesOccurredAt 正数保留期按 occurred_at 清理（不使用 created_at）。
func TestCleanupAppLogUsesOccurredAt(t *testing.T) {
	sdb, fdb := openFake(t)
	fdb.onExec = func(string, []driver.Value) (driver.Result, error) { return fakeResult(4), nil }
	now := time.Now()
	n, err := CleanupAppLog(context.Background(), sdb, 30)
	if err != nil || n != 4 {
		t.Fatalf("CleanupAppLog(30) = %d/%v", n, err)
	}
	dels := fdb.execsWith("DELETE FROM app_log")
	if len(dels) != 1 {
		t.Fatalf("DELETE 次数 = %d", len(dels))
	}
	if !strings.Contains(dels[0].query, "occurred_at") || strings.Contains(dels[0].query, "created_at") {
		t.Fatalf("清理应使用 occurred_at：%s", dels[0].query)
	}
	cutoff, _ := dels[0].args[0].(time.Time)
	if !approxTime(cutoff, now.AddDate(0, 0, -30), 2*time.Minute) {
		t.Fatalf("cutoff = %v", cutoff)
	}
}

// TestCleanupAppLogBatches 正数保留期必须分批 DELETE LIMIT，循环到不足一批为止。
func TestCleanupAppLogBatches(t *testing.T) {
	sdb, fdb := openFake(t)
	var mu sync.Mutex
	calls := 0
	fdb.onExec = func(q string, args []driver.Value) (driver.Result, error) {
		if !strings.Contains(q, "DELETE FROM app_log") {
			return fakeResult(1), nil
		}
		mu.Lock()
		calls++
		n := calls
		mu.Unlock()
		if !strings.Contains(q, "LIMIT") {
			t.Errorf("分批清理缺少 LIMIT：%s", q)
		}
		if len(args) < 2 {
			t.Errorf("分批清理缺少 LIMIT 参数：%v", args)
		}
		if n == 1 {
			return fakeResult(appLogCleanupBatch), nil
		}
		return fakeResult(3), nil
	}
	n, err := CleanupAppLog(context.Background(), sdb, 30)
	if err != nil {
		t.Fatalf("CleanupAppLog: %v", err)
	}
	if n != appLogCleanupBatch+3 {
		t.Fatalf("删除总数 = %d，期望 %d", n, appLogCleanupBatch+3)
	}
	if calls != 2 {
		t.Fatalf("DELETE 次数 = %d，期望 2", calls)
	}
}

// TestCleanupAppLogStopsOnCanceledContext ctx 取消后停止继续分批。
func TestCleanupAppLogStopsOnCanceledContext(t *testing.T) {
	sdb, fdb := openFake(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	calls := 0
	fdb.onExec = func(q string, args []driver.Value) (driver.Result, error) {
		if !strings.Contains(q, "DELETE FROM app_log") {
			return fakeResult(1), nil
		}
		calls++
		cancel()
		return fakeResult(appLogCleanupBatch), nil
	}
	n, err := CleanupAppLog(ctx, sdb, 30)
	if err == nil {
		t.Fatal("ctx 取消后应返回错误")
	}
	if calls != 1 {
		t.Fatalf("ctx 取消后仍执行了 %d 次 DELETE", calls)
	}
	if n != appLogCleanupBatch {
		t.Fatalf("取消前已删行数 = %d", n)
	}
}

// TestAppLogDaysBefore 返回待清理区间内存在日志的日期（本地日）。
func TestAppLogDaysBefore(t *testing.T) {
	sdb, fdb := openFake(t)
	day1 := time.Date(2020, 1, 1, 0, 0, 0, 0, time.Local)
	day3 := time.Date(2020, 1, 3, 0, 0, 0, 0, time.Local)
	fdb.onQuery = func(q string, args []driver.Value) (*fakeRows, error) {
		if !strings.Contains(q, "DATE(occurred_at)") {
			return rowsOf([]string{"v"}), nil
		}
		return rowsOf([]string{"d"}, []driver.Value{day1}, []driver.Value{day3}), nil
	}
	days, err := AppLogDaysBefore(context.Background(), sdb, time.Date(2026, 1, 1, 0, 0, 0, 0, time.Local))
	if err != nil {
		t.Fatalf("AppLogDaysBefore: %v", err)
	}
	if len(days) != 2 || !days[0].Equal(day1) || !days[1].Equal(day3) {
		t.Fatalf("日期 = %v", days)
	}
}
