package safeday

import (
	"context"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

// 旧回调/漏 batch_id 回调被忽略时必须写可检索结构化日志（含原因，不记 token）。
func TestCallbackIgnoredLoggedWithReason(t *testing.T) {
	env := newTestServer(t, identTeamA)
	env.module.Log = &fakeLogger{}
	rec, _, _ := env.store.Create(Record{
		Name: "安全活动学习", Date: "2026.10.27", FileName: "2026.10.27.docx",
		Team: "检修一班", Status: "processing", BatchID: "batch-new",
	})
	body := map[string]any{
		"date": "2026.10.27", "class": "检修一班",
		"accident_process": "事故经过", "cause_analysis": "原因",
	}
	resp := env.do(t, http.MethodPost, "/api/v1/safeday/callback?token=cb-token", body)
	if resp.Code != http.StatusOK {
		t.Fatalf("忽略回调应返回 200: %s", resp.Body.String())
	}
	if got, _ := env.store.Get(rec.ID); got.Status != "processing" {
		t.Fatalf("旧回调不得写新记录，实际 %s", got.Status)
	}
	logger := env.module.Log.(*fakeLogger)
	if !logger.has("safeday.callback.ignored") {
		t.Fatalf("应记录 safeday.callback.ignored，实际 %v", logger.eventList())
	}
	if reason := logger.lastFields()["reason"]; reason == "" {
		t.Fatalf("ignored 日志应包含 reason，实际 %v", logger.lastFields())
	}
}

// 生成超时清扫：启动即跑，超时置 failed + 明确 error + 结构化日志；新记录不动。
func TestProcessingSweeperMarksTimedOutRecordsFailed(t *testing.T) {
	env := newTestServer(t, identTeamA)
	logger := &fakeLogger{}
	env.module.Log = logger
	env.module.ProcessingTimeout = 30 * time.Minute
	old, _, _ := env.store.Create(Record{
		Name: "超时", Date: "2026.10.01", FileName: "old.docx", Team: "检修一班",
		Status: "processing", BatchID: "batch-old",
		CreatedAt: time.Now().Add(-31 * time.Minute).UTC().Format(time.RFC3339),
	})
	fresh, _, _ := env.store.Create(Record{
		Name: "新", Date: "2026.10.02", FileName: "new.docx", Team: "检修一班",
		Status: "processing", BatchID: "batch-new",
		CreatedAt: time.Now().UTC().Format(time.RFC3339),
	})
	if n := env.module.sweepProcessing(context.Background()); n != 1 {
		t.Fatalf("应清扫 1 条超时记录，实际 %d", n)
	}
	got, _ := env.store.Get(old.ID)
	if got.Status != "failed" || !strings.Contains(got.Error, "超时") || !strings.Contains(got.Error, "重新生成") {
		t.Fatalf("超时记录应 failed 且提示可重试: %+v", got)
	}
	if cur, _ := env.store.Get(fresh.ID); cur.Status != "processing" {
		t.Fatalf("新批次不得被误判超时: %+v", cur)
	}
	if !logger.has("safeday.processing.timeout") {
		t.Fatalf("应记录超时结构化日志，实际 %v", logger.eventList())
	}
	if logger.lastFields()["record_id"] != old.ID {
		t.Fatalf("超时日志应含 record_id，实际 %v", logger.lastFields())
	}
}

// 已 done/failed 的记录不得被清扫覆盖（CAS）。
func TestProcessingSweeperDoesNotOverwriteTerminalRecord(t *testing.T) {
	env := newTestServer(t, identTeamA)
	rec, _, _ := env.store.Create(Record{
		Name: "完成", Date: "2026.10.03", FileName: "done.docx", Team: "检修一班",
		Status: "processing", BatchID: "batch-done",
		CreatedAt: time.Now().Add(-2 * time.Hour).UTC().Format(time.RFC3339),
	})
	done := "done"
	env.store.Update(rec.ID, RecordPatch{Status: &done})
	if n := env.module.sweepProcessing(context.Background()); n != 0 {
		t.Fatalf("终态记录不应被清扫，实际 %d", n)
	}
	if got, _ := env.store.Get(rec.ID); got.Status != "done" {
		t.Fatalf("终态记录被覆盖: %+v", got)
	}
}

// 周期清扫：Start 立即跑一次且此后按间隔继续；stop 幂等。
func TestStartProcessingSweeperRunsImmediatelyAndPeriodically(t *testing.T) {
	env := newTestServer(t, identTeamA)
	env.module.ProcessingTimeout = time.Minute
	env.module.ProcessingSweepInterval = 20 * time.Millisecond
	first, _, _ := env.store.Create(Record{
		Name: "一", Date: "2026.10.04", FileName: "a.docx", Team: "检修一班", BatchID: "b1",
		Status: "processing", CreatedAt: time.Now().Add(-2 * time.Minute).UTC().Format(time.RFC3339),
	})
	stop := env.module.StartProcessingSweeper(context.Background())
	waitSafeday(t, func() bool { r, _ := env.store.Get(first.ID); return r.Status == "failed" })
	second, _, _ := env.store.Create(Record{
		Name: "二", Date: "2026.10.05", FileName: "b.docx", Team: "检修一班", BatchID: "b2",
		Status: "processing", CreatedAt: time.Now().Add(-2 * time.Minute).UTC().Format(time.RFC3339),
	})
	waitSafeday(t, func() bool { r, _ := env.store.Get(second.ID); return r.Status == "failed" })
	stop()
	stop()
}

func waitSafeday(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("等待条件超时")
}

// BackfillTeam 写盘失败必须返回错误，不能伪装成“无改动”。
func TestBackfillTeamReturnsWriteError(t *testing.T) {
	dir := t.TempDir()
	store, err := NewRecordStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.Create(Record{Name: "旧", Date: "2026.10.01", FileName: "a.docx"}); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dir, []byte("not-a-dir"), 0o600); err != nil {
		t.Skipf("无法构造写盘失败环境: %v", err)
	}
	n, err := store.BackfillTeam("检修一班")
	if err == nil {
		t.Fatalf("写盘失败应返回错误，实际 n=%d err=nil", n)
	}
	if n != 0 {
		t.Fatalf("失败时改动数应为 0，实际 %d", n)
	}
}

// 超时阈值：DIFY_TIMEOUT_MS 更大时跟随，最小 30 分钟；不可读/非法回退 30m；显式字段优先。
func TestProcessingTimeoutDerivedFromDifyTimeout(t *testing.T) {
	cases := []struct {
		name  string
		cfg   ConfigReader
		want  time.Duration
		field time.Duration
	}{
		{"45 分钟工作流", fakeConfig{"DIFY_TIMEOUT_MS": "2700000"}, 45 * time.Minute, 0},
		{"90 分钟工作流", fakeConfig{"DIFY_TIMEOUT_MS": "5400000"}, 90 * time.Minute, 0},
		{"短超时保底 30m", fakeConfig{"DIFY_TIMEOUT_MS": "600000"}, 30 * time.Minute, 0},
		{"非法值回退", fakeConfig{"DIFY_TIMEOUT_MS": "abc"}, 30 * time.Minute, 0},
		{"缺键回退", fakeConfig{}, 30 * time.Minute, 0},
		{"无配置源", nil, 30 * time.Minute, 0},
		{"显式覆盖优先", fakeConfig{"DIFY_TIMEOUT_MS": "5400000"}, 2 * time.Hour, 2 * time.Hour},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := &Module{Cfg: tc.cfg, ProcessingTimeout: tc.field}
			if got := m.processingTimeout(); got != tc.want {
				t.Fatalf("processingTimeout = %s, want %s", got, tc.want)
			}
		})
	}
}

// 清扫写盘/CAS 错误必须写结构化日志，不得静默 continue。
func TestProcessingSweeperLogsWriteError(t *testing.T) {
	env := newTestServer(t, identTeamA)
	logger := &fakeLogger{}
	env.module.Log = logger
	env.module.ProcessingTimeout = time.Minute
	rec, _, _ := env.store.Create(Record{
		Name: "写盘失败", Date: "2026.10.06", FileName: "c.docx", Team: "检修一班",
		Status: "processing", BatchID: "b3",
		CreatedAt: time.Now().Add(-2 * time.Minute).UTC().Format(time.RFC3339),
	})
	dir := env.store.DataDir()
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dir, []byte("not-a-dir"), 0o600); err != nil {
		t.Skipf("无法构造写盘失败环境: %v", err)
	}
	if n := env.module.sweepProcessing(context.Background()); n != 0 {
		t.Fatalf("写盘失败不应计为清扫成功，实际 %d", n)
	}
	if !logger.has("safeday.processing.sweep_failed") {
		t.Fatalf("应记录清扫写盘错误，实际 %v", logger.eventList())
	}
	if got, _ := env.store.Get(rec.ID); got.Status != "processing" {
		t.Fatalf("写盘失败时缓存状态不应改变: %+v", got)
	}
}
