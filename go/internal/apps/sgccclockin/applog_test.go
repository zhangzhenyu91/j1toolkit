package sgccclockin

// 手动「从商旅同步」应用日志测试：开始/逐成员进度/结束/失败四类事件的
// 模块（app_code=sgcc-clockin）/类别（business）/级别（info/warn/error）与脱敏口径。
// fake AppLogger + fake SQL/Client，不实连外部服务。

import (
	"context"
	"database/sql/driver"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"shade/internal/logging"
)

type fakeAppLogEntry struct {
	level, category, event, messageCN, raw string
	fields                                 map[string]any
	traceID                                string
}

// fakeAppLogger 记录应用日志调用（后台 goroutine 写入，互斥保护）。
type fakeAppLogger struct {
	mu      sync.Mutex
	entries []fakeAppLogEntry
}

func (l *fakeAppLogger) add(ctx context.Context, level, category, event, messageCN, raw string, fields map[string]any) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.entries = append(l.entries, fakeAppLogEntry{level, category, event, messageCN, raw, fields, logging.TraceIDFromContext(ctx)})
	return nil
}

func (l *fakeAppLogger) Info(ctx context.Context, category, event, messageCN, raw string, fields map[string]any) error {
	return l.add(ctx, "info", category, event, messageCN, raw, fields)
}
func (l *fakeAppLogger) Warn(ctx context.Context, category, event, messageCN, raw string, fields map[string]any) error {
	return l.add(ctx, "warn", category, event, messageCN, raw, fields)
}
func (l *fakeAppLogger) Error(ctx context.Context, category, event, messageCN, raw string, fields map[string]any) error {
	return l.add(ctx, "error", category, event, messageCN, raw, fields)
}

func (l *fakeAppLogger) snapshot() []fakeAppLogEntry {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]fakeAppLogEntry(nil), l.entries...)
}

// waitJobDone 等后台同步任务收尾（2s 上限）。
func waitJobDone(t *testing.T, jobID int64) *SyncJob {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if job, ok := jobs.Get(jobID); ok && job.Finished {
			return job
		}
		time.Sleep(5 * time.Millisecond)
	}
	job, _ := jobs.Get(jobID)
	t.Fatalf("任务未完成: %+v", job)
	return nil
}

// assertNoSensitive 应用日志不得出现 token/手机号等敏感原文。
func assertNoSensitive(t *testing.T, entries []fakeAppLogEntry, secrets ...string) {
	t.Helper()
	for i, e := range entries {
		for _, s := range secrets {
			if strings.Contains(e.messageCN, s) || strings.Contains(e.raw, s) {
				t.Fatalf("第 %d 条日志泄露敏感原文 %q: %+v", i+1, s, e)
			}
		}
	}
}

// TestSyncPullAppLogMember 手动逐成员同步：start/member/end 三类事件，级别 info，类别 business。
func TestSyncPullAppLogMember(t *testing.T) {
	db, fdb := openFake(t)
	defer db.Close()
	m := New(db)
	m.API = &fakeAPI{dayNew: map[string]any{"statusCode": 200}}
	fl := &fakeAppLogger{}
	m.Log = fl
	h := newTestRouter(m, testIdentity())

	fdb.onRowsAlways("FROM sys_team", []string{"id", "name"}, []driver.Value{int64(1), "检修一班"})
	fdb.onRowsAlways("FROM worklog_member m", []string{"id"}, []driver.Value{int64(11)})
	fdb.onRowsAlways("FROM worklog_sgcc_account WHERE member_id",
		[]string{"id", "team_id", "user_id", "member_id", "mobile", "token", "device_type", "system_version", "token_status"},
		[]driver.Value{int64(1), int64(1), int64(7), int64(11), "13800138000", "tk-secret", "dev", "sys", int64(1)})
	fdb.onRowsAlways("SELECT name FROM worklog_member WHERE id", []string{"name"}, []driver.Value{"张三"})

	rec := doJSON(t, h, http.MethodPost, "/api/v1/sgcc/sync/pull",
		`{"member_id":11,"date":"2026-10-27"}`)
	env := decodeEnvelope(t, rec)
	if env["code"] != float64(0) {
		t.Fatalf("发起同步失败: %v", env)
	}
	jobID := int64(env["data"].(map[string]any)["jobId"].(float64))
	waitJobDone(t, jobID)

	entries := fl.snapshot()
	if len(entries) != 3 {
		t.Fatalf("应有 start/member/end 三条日志: %+v", entries)
	}
	want := []struct {
		event, level, name string
	}{
		{"sgcc.sync.pull.start", "info", "张三"},
		{"sgcc.sync.pull.member", "info", "张三"},
		{"sgcc.sync.pull.end", "info", "张三"},
	}
	for i, w := range want {
		e := entries[i]
		if e.event != w.event || e.level != w.level {
			t.Fatalf("第 %d 条事件/级别不符: %+v", i+1, e)
		}
		if e.category != "business" {
			t.Fatalf("第 %d 条类别应为 business: %+v", i+1, e)
		}
		if e.fields["job_id"] != jobID {
			t.Fatalf("第 %d 条 job_id 不符: %+v", i+1, e.fields)
		}
		if i < 2 && (e.fields["member_name"] != w.name || e.fields["member_id"] != int64(11)) {
			t.Fatalf("第 %d 条成员字段不符: %+v", i+1, e.fields)
		}
	}
	if entries[1].fields["result"] != "ok" {
		t.Fatalf("逐成员成功 result 应为 ok: %+v", entries[1])
	}
	if entries[2].fields["total"] != 1 || entries[2].fields["done"] != 1 || entries[2].fields["failed"] != 0 {
		t.Fatalf("收尾计数不符: %+v", entries[2].fields)
	}
	assertNoSensitive(t, entries, "tk-secret", "13800138000")
}

// TestSyncPullAppLogMemberFail 手动逐成员同步失败：member warn + failed error，原因脱敏截断。
func TestSyncPullAppLogMemberFail(t *testing.T) {
	db, fdb := openFake(t)
	defer db.Close()
	m := New(db)
	m.API = &fakeAPI{dayNewErr: errors.New("boom")}
	fl := &fakeAppLogger{}
	m.Log = fl
	h := newTestRouter(m, testIdentity())

	fdb.onRowsAlways("FROM sys_team", []string{"id", "name"}, []driver.Value{int64(1), "检修一班"})
	fdb.onRowsAlways("FROM worklog_member m", []string{"id"}, []driver.Value{int64(11)})
	fdb.onRowsAlways("FROM worklog_sgcc_account WHERE member_id",
		[]string{"id", "team_id", "user_id", "member_id", "mobile", "token", "device_type", "system_version", "token_status"},
		[]driver.Value{int64(1), int64(1), int64(7), int64(11), "13800138000", "tk-secret", "dev", "sys", int64(1)})

	rec := doJSON(t, h, http.MethodPost, "/api/v1/sgcc/sync/pull",
		`{"member_id":11,"date":"2026-10-27"}`)
	env := decodeEnvelope(t, rec)
	if env["code"] != float64(0) {
		t.Fatalf("发起同步失败: %v", env)
	}
	jobID := int64(env["data"].(map[string]any)["jobId"].(float64))
	job := waitJobDone(t, jobID)
	if job.Failed != 1 {
		t.Fatalf("应有 1 步失败: %+v", job)
	}

	entries := fl.snapshot()
	if len(entries) != 3 {
		t.Fatalf("应有 start/member/failed 三条日志: %+v", entries)
	}
	if entries[0].event != "sgcc.sync.pull.start" || entries[0].level != "info" {
		t.Fatalf("start 不符: %+v", entries[0])
	}
	if entries[1].event != "sgcc.sync.pull.member" || entries[1].level != "warn" ||
		entries[1].fields["result"] != "fail" {
		t.Fatalf("member 失败记录不符: %+v", entries[1])
	}
	if !strings.Contains(entries[1].raw, "boom") {
		t.Fatalf("member 失败原因应保留: %+v", entries[1])
	}
	if entries[2].event != "sgcc.sync.pull.failed" || entries[2].level != "error" {
		t.Fatalf("整体失败应记 error 级 failed 事件: %+v", entries[2])
	}
	assertNoSensitive(t, entries, "tk-secret", "13800138000")
}

// TestSyncPullAppLogBatchCard 手动卡片级批量同步：start + 逐成员进度 + end 汇总计数。
func TestSyncPullAppLogBatchCard(t *testing.T) {
	db, fdb := openFake(t)
	defer db.Close()
	m := New(db)
	m.API = &fakeAPI{dayNew: map[string]any{"statusCode": 200}}
	fl := &fakeAppLogger{}
	m.Log = fl
	h := newTestRouter(m, adminIdentity())

	fdb.onRowsAlways("FROM sys_team", []string{"id", "name"}, []driver.Value{int64(1), "检修一班"})
	fdb.onRowsAlways("FROM worklog_entry WHERE id", []string{"log_date"}, []driver.Value{"2026-10-28"})
	fdb.onRowsAlways("FROM worklog_entry_member WHERE entry_id", []string{"member_id"},
		[]driver.Value{int64(11)}, []driver.Value{int64(12)})
	fdb.onRowsAlways("COUNT(DISTINCT m.id)", []string{"cnt"}, []driver.Value{int64(2)})
	fdb.onRowsAlways("WHERE m.team_id = ? AND m.id IN",
		[]string{"id", "team_id", "user_id", "member_id", "mobile", "token", "device_type", "system_version", "token_status", "card"},
		[]driver.Value{int64(1), int64(1), int64(7), int64(11), "13800138000", "tk-secret", "dev", "sys", int64(1), int64(11)},
		[]driver.Value{int64(2), int64(1), int64(8), int64(12), "13900139000", "tk-secret-2", "dev", "sys", int64(1), int64(12)})
	fdb.onRowsFunc("SELECT name FROM worklog_member WHERE id", func(args []driver.Value) ([]string, [][]driver.Value) {
		cols := []string{"name"}
		if len(args) > 0 {
			switch args[0] {
			case int64(11):
				return cols, [][]driver.Value{{"张三"}}
			case int64(12):
				return cols, [][]driver.Value{{"李四"}}
			}
		}
		return cols, nil
	})

	rec := doJSON(t, h, http.MethodPost, "/api/v1/sgcc/sync/pull", `{"entry_id":100}`)
	env := decodeEnvelope(t, rec)
	if env["code"] != float64(0) {
		t.Fatalf("卡片级拉取应成功: %v", env)
	}
	jobID := int64(env["data"].(map[string]any)["jobId"].(float64))
	job := waitJobDone(t, jobID)
	if job.Done != 2 || job.Failed != 0 {
		t.Fatalf("卡片级任务计数不符: %+v", job)
	}

	entries := fl.snapshot()
	if len(entries) != 4 {
		t.Fatalf("应有 start + 2 逐成员 + end 四条日志: %+v", entries)
	}
	if entries[0].event != "sgcc.sync.pull.start" || entries[0].level != "info" ||
		entries[0].fields["mode"] != "card" || entries[0].fields["total"] != 2 {
		t.Fatalf("start 不符: %+v", entries[0])
	}
	// 逐成员日志：成员并发后到达顺序不固定，按成员集合断言
	got := map[int64]string{}
	for _, e := range entries[1:3] {
		if e.event != "sgcc.sync.pull.member" || e.level != "info" ||
			e.fields["result"] != "ok" || e.fields["date"] != "2026-10-28" {
			t.Fatalf("逐成员日志形状不符: %+v", e)
		}
		id, _ := e.fields["member_id"].(int64)
		name, _ := e.fields["member_name"].(string)
		got[id] = name
	}
	want := map[int64]string{11: "张三", 12: "李四"}
	for id, name := range want {
		if got[id] != name {
			t.Fatalf("成员 %d 的日志姓名应 %q，实际 %q（全部 %v）", id, name, got[id], got)
		}
	}
	end := entries[3]
	if end.event != "sgcc.sync.pull.end" || end.level != "info" ||
		end.fields["done"] != 2 || end.fields["failed"] != 0 || end.fields["total"] != 2 {
		t.Fatalf("end 汇总不符: %+v", end)
	}
	assertNoSensitive(t, entries, "tk-secret", "tk-secret-2", "13800138000", "13900139000")
}

// TestSyncPullAppLogDefaultModule 未注入 Log 时以 DB 默认写入 app_log，模块标识 sgcc-clockin。
func TestSyncPullAppLogDefaultModule(t *testing.T) {
	db, fdb := openFake(t)
	defer db.Close()
	m := New(db)
	m.API = &fakeAPI{dayNew: map[string]any{"statusCode": 200}}
	h := newTestRouter(m, testIdentity())

	fdb.onRowsAlways("FROM sys_team", []string{"id", "name"}, []driver.Value{int64(1), "检修一班"})
	fdb.onRowsAlways("FROM worklog_member m", []string{"id"}, []driver.Value{int64(11)})
	fdb.onRowsAlways("FROM worklog_sgcc_account WHERE member_id",
		[]string{"id", "team_id", "user_id", "member_id", "mobile", "token", "device_type", "system_version", "token_status"},
		[]driver.Value{int64(1), int64(1), int64(7), int64(11), "13800138000", "tk-secret", "dev", "sys", int64(1)})

	rec := doJSON(t, h, http.MethodPost, "/api/v1/sgcc/sync/pull",
		`{"member_id":11,"date":"2026-10-27"}`)
	env := decodeEnvelope(t, rec)
	if env["code"] != float64(0) {
		t.Fatalf("发起同步失败: %v", env)
	}
	jobID := int64(env["data"].(map[string]any)["jobId"].(float64))
	waitJobDone(t, jobID)

	// InsertAppLog 参数序：app_code, level, message, trace_id, user_id, detail, category, event, ...
	calls := fdb.callsWith("INSERT INTO app_log")
	if len(calls) < 3 {
		t.Fatalf("应写入 start/member/end 至少三条 app_log: %d", len(calls))
	}
	sawStart := false
	for _, c := range calls {
		if len(c.args) < 8 || c.args[0] != "sgcc-clockin" || c.args[6] != "business" {
			t.Fatalf("app_log 模块/类别不符: %+v", c.args)
		}
		if c.args[7] == "sgcc.sync.pull.start" {
			sawStart = true
			if c.args[1] != "info" {
				t.Fatalf("start 级别应为 info: %+v", c.args)
			}
		}
	}
	if !sawStart {
		t.Fatalf("应有 sgcc.sync.pull.start 事件: %+v", calls)
	}
}
