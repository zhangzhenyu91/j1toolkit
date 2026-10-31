package httpapi

// 日志查询 API 回归测试：视图/键集分页/详情摘要、单条详情、健康快照、
// 文件 NDJSON 流式导出与非接入降级。全部使用 fake/httptest/临时目录，不连真实库。

import (
	"bytes"
	"compress/gzip"
	"context"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"shade/internal/auth"
	"shade/internal/logging"
)

// TestMain 把健康告警 stderr 默认改到丢弃，避免测试输出噪声；
// 需要验证 stderr 的用例在自身作用域内替换 writer。
func TestMain(m *testing.M) {
	logHealthAlertMu.Lock()
	logHealthAlertWriter = io.Discard
	logHealthAlertMu.Unlock()
	os.Exit(m.Run())
}

// ---- 测试替身 ----

// recordingLogs 记录筛选条件并返回预设分页/详情。
type recordingLogs struct {
	page      LogPage
	pageErr   error
	detail    LogEntry
	detailErr error
	got       LogFilter
	detailID  int64
}

func (f *recordingLogs) ListLogs(_ context.Context, fl LogFilter) (LogPage, error) {
	f.got = fl
	if f.pageErr != nil {
		return LogPage{}, f.pageErr
	}
	return f.page, nil
}

func (f *recordingLogs) GetLog(_ context.Context, id int64) (LogEntry, error) {
	f.detailID = id
	if f.detailErr != nil {
		return LogEntry{}, f.detailErr
	}
	return f.detail, nil
}

// fakeLogHealth 固定健康快照。
type fakeLogHealth struct{ h LogHealth }

func (f fakeLogHealth) LogHealth() LogHealth { return f.h }

// countingLogHealth 记录健康快照读取次数（验证 healthz 短缓存）。
type countingLogHealth struct {
	h     LogHealth
	calls int
}

func (c *countingLogHealth) LogHealth() LogHealth {
	c.calls++
	return c.h
}

// failingLogFiles 流式输出若干条后返回错误（模拟导出中途失败）。
type failingLogFiles struct {
	entries []LogEntry
	err     error
}

func (f failingLogFiles) Probe(context.Context) error { return nil }

func (f failingLogFiles) StreamLogs(
	_ context.Context,
	_ LogFilter,
	fn func(LogEntry) error,
) error {
	for _, e := range f.entries {
		if err := fn(e); err != nil {
			return err
		}
	}
	return f.err
}

// probeLogFiles 可分别注入 Probe / StreamLogs 失败的导出源。
type probeLogFiles struct {
	probeErr    error
	probeCalls  int
	streamErr   error
	streamCalls int
	entries     []LogEntry
}

func (f *probeLogFiles) Probe(context.Context) error {
	f.probeCalls++
	return f.probeErr
}

func (f *probeLogFiles) StreamLogs(
	_ context.Context,
	_ LogFilter,
	fn func(LogEntry) error,
) error {
	f.streamCalls++
	for _, e := range f.entries {
		if err := fn(e); err != nil {
			return err
		}
	}
	return f.streamErr
}

// exportMarker NDJSON 完成/失败哨兵。
type exportMarker struct {
	Kind             string `json:"__export__"`
	Count            int    `json:"count"`
	FileCopyComplete bool   `json:"file_copy_complete"`
	DiskFallbacks    int64  `json:"disk_fallbacks,omitempty"`
	FsyncErrors      int64  `json:"fsync_errors,omitempty"`
	CorruptLines     int64  `json:"corrupt_lines,omitempty"`
	HalfLines        int64  `json:"half_lines,omitempty"`
	DBOnly           int64  `json:"db_only,omitempty"`
	Scope            string `json:"scope,omitempty"`
	Error            string `json:"error,omitempty"`
}

// exportLine 导出流单行（日志正文 + 哨兵）。
type exportLine struct {
	LogEntry
	Marker string `json:"__export__"`
}

// adminRouter 超管身份路由。
func adminRouter(d Deps) http.Handler {
	d.Auth = &fakeAuth{ident: auth.Identity{ID: 1, Username: "admin", Role: "admin"}}
	return NewRouter(d)
}

// appLogListColumns 列表查询的扫描列（顺序与实现严格对应）。
var appLogListColumns = []string{
	"id", "log_id", "app_code", "level", "category", "event", "message", "message_cn",
	"raw_message", "detail", "detail_truncated", "trace_id", "user_id", "team_id",
	"occurred_at", "created_at", "source", "is_access",
}

// appLogDetailColumns 单条详情查询的扫描列。
var appLogDetailColumns = []string{
	"id", "log_id", "app_code", "level", "category", "event", "message", "message_cn",
	"raw_message", "context_json", "detail", "trace_id", "user_id", "team_id",
	"occurred_at", "created_at", "source", "is_access",
}

// findQuery 返回包含子串的首个 SQL 调用（未找到返回空串）。
func findQuery(db *fakeDB, substr string) fakeCall {
	db.mu.Lock()
	defer db.mu.Unlock()
	for _, c := range db.calls {
		if strings.Contains(c.query, substr) {
			return c
		}
	}
	return fakeCall{}
}

// hasQuery 是否出现包含子串的 SQL 调用。
func hasQuery(db *fakeDB, substr string) bool {
	return findQuery(db, substr).query != ""
}

// ---- 筛选解析 ----

func TestParseLogFilter_ViewAndKeyset(t *testing.T) {
	f, err := parseLogFilter(url.Values{})
	if err != nil {
		t.Fatalf("缺省解析失败: %v", err)
	}
	if f.View != "activity" {
		t.Fatalf("默认视图应为 activity（排除访问日志），got %q", f.View)
	}

	for _, c := range []struct {
		raw  string
		want string
	}{
		{"activity", "activity"},
		{"access", "access"},
		{"all", "all"},
		{"ACCESS", "access"},
	} {
		got, err := parseLogFilter(url.Values{"view": {c.raw}})
		if err != nil || got.View != c.want {
			t.Fatalf("view=%q 解析不符: view=%q err=%v", c.raw, got.View, err)
		}
	}
	if _, err := parseLogFilter(url.Values{"view": {"bogus"}}); err == nil {
		t.Fatal("非法 view 应返回参数错误")
	}

	before := "2026-10-30T08:00:00Z"
	f, err = parseLogFilter(url.Values{"before_time": {before}, "before_id": {"42"}})
	if err != nil {
		t.Fatalf("键集参数解析失败: %v", err)
	}
	if f.BeforeTime.Format(time.RFC3339) != before || f.BeforeID != 42 {
		t.Fatalf("键集参数不符: %+v", f)
	}

	for _, q := range []url.Values{
		{"before_time": {before}},
		{"before_id": {"42"}},
		{"before_time": {"yesterday"}, "before_id": {"42"}},
		{"before_time": {before}, "before_id": {"0"}},
		{"before_time": {before}, "before_id": {"x"}},
	} {
		if _, err := parseLogFilter(q); err == nil {
			t.Fatalf("非法键集参数应报错: %v", q)
		}
	}
}

func TestAppLogs_PassesViewAndKeyset(t *testing.T) {
	logs := &recordingLogs{page: LogPage{Total: -1}}
	h := adminRouter(Deps{Logs: logs})
	status, r := doReqAuth(t, h, http.MethodGet,
		"/api/v1/app-logs?view=access&before_time=2026-10-30T08:00:00Z&before_id=42", "")
	assertOK(t, status, r)
	if logs.got.View != "access" || logs.got.BeforeID != 42 ||
		logs.got.BeforeTime.Format(time.RFC3339) != "2026-10-30T08:00:00Z" {
		t.Fatalf("视图/键集未透传: %+v", logs.got)
	}
}

func TestAppLogs_ListReturnsOccurredAtAndSummary(t *testing.T) {
	occurred := time.Date(2026, 10, 30, 8, 0, 0, 0, time.UTC)
	logs := &recordingLogs{page: LogPage{
		Items: []LogEntry{{
			ID: 9, LogID: "lg-9", AppCode: "work-log", Level: "warn", Category: "business",
			Event: "worklog.task.failed", Message: "boom", MessageCN: "出工任务失败",
			OccurredAt: occurred, CreatedAt: occurred.Add(time.Second), Source: "file",
			UserID: 5, TeamID: 2, Detail: `{"photo":1}`, DetailTruncated: true,
		}},
		Total: 1,
	}}
	h := adminRouter(Deps{Logs: logs})
	status, r := doReqAuth(t, h, http.MethodGet, "/api/v1/app-logs", "")
	assertOK(t, status, r)

	var page LogPage
	if err := json.Unmarshal(r.Data, &page); err != nil {
		t.Fatalf("data 解码失败: %v", err)
	}
	it := page.Items[0]
	if it.LogID != "lg-9" || !it.OccurredAt.Equal(occurred) || it.Source != "file" ||
		!it.DetailTruncated || it.UserID != 5 || it.TeamID != 2 {
		t.Fatalf("列表字段不符: %+v", it)
	}
}

// ---- SQL 查询 ----

func TestSQLLogReader_ActivityDefaultUsesOccurredAtIndex(t *testing.T) {
	db, fdb := newFakeDB(t)
	occurred := time.Date(2026, 10, 30, 8, 0, 0, 0, time.UTC)
	created := occurred.Add(time.Second)
	fdb.onRows("COUNT(*)", []string{"n"}, []driver.Value{int64(1)})
	fdb.onRows("FROM app_log", appLogListColumns, []driver.Value{
		int64(9), "lg-9", "work-log", "warn", "business", "worklog.task.failed",
		"boom", "出工任务失败", "raw boom", `{"photo":1}`, int64(1),
		"tr-9", int64(5), int64(2), occurred, created, "file", int64(0),
	})

	page, err := (SQLLogReader{DB: db}).ListLogs(context.Background(), LogFilter{Page: 1, PageSize: 20})
	if err != nil {
		t.Fatalf("查询失败: %v", err)
	}
	if page.Total != 1 {
		t.Fatalf("首页计数应可用: %+v", page)
	}
	if len(page.Items) != 1 {
		t.Fatalf("条目数不符: %+v", page.Items)
	}
	it := page.Items[0]
	if it.LogID != "lg-9" || !it.OccurredAt.Equal(occurred) || !it.DetailTruncated ||
		it.Detail != `{"photo":1}` || it.Source != "file" || it.IsAccess {
		t.Fatalf("条目映射不符: %+v", it)
	}

	sel := findQuery(fdb, "ORDER BY occurred_at")
	if !strings.Contains(sel.query, "ORDER BY occurred_at DESC, id DESC") {
		t.Fatalf("必须按 occurred_at/id 排序: %s", sel.query)
	}
	if !strings.Contains(sel.query, "is_access = ?") {
		t.Fatalf("activity 视图必须使用 is_access 索引: %s", sel.query)
	}
	if !strings.Contains(sel.query, "LEFT(") || !strings.Contains(sel.query, "detail_truncated") {
		t.Fatalf("列表必须只取 detail 摘要: %s", sel.query)
	}
	if strings.Contains(sel.query, "created_at >=") || strings.Contains(sel.query, "ORDER BY created_at") {
		t.Fatalf("查询不得继续按 created_at 排序/过滤: %s", sel.query)
	}
	if got := fmt.Sprint(sel.arg(5)); got != "0" {
		t.Fatalf("activity 视图 is_access 参数应为 0，got %#v (args=%s)", sel.arg(5), sel.text())
	}
	if !hasQuery(fdb, "COUNT(*)") {
		t.Fatal("简单首页查询应返回总量")
	}
}

func TestSQLLogReader_KeywordSkipsCount(t *testing.T) {
	db, fdb := newFakeDB(t)
	fdb.onRows("ORDER BY occurred_at", appLogListColumns)
	page, err := (SQLLogReader{DB: db}).ListLogs(context.Background(), LogFilter{
		Keyword: "boom", Page: 1, PageSize: 20,
	})
	if err != nil {
		t.Fatalf("关键词查询失败: %v", err)
	}
	if page.Total != -1 {
		t.Fatalf("关键词查询不应强算全表 COUNT，total 应为 -1（未知），got %d", page.Total)
	}
	if hasQuery(fdb, "COUNT(*)") {
		t.Fatal("关键词查询不得执行 COUNT(*)")
	}
}

func TestSQLLogReader_KeysetPagination(t *testing.T) {
	db, fdb := newFakeDB(t)
	fdb.onRows("ORDER BY occurred_at", appLogListColumns)
	before := time.Date(2026, 10, 30, 8, 0, 0, 0, time.UTC)
	page, err := (SQLLogReader{DB: db}).ListLogs(context.Background(), LogFilter{
		View: "all", BeforeTime: before, BeforeID: 42, Page: 3, PageSize: 10,
	})
	if err != nil {
		t.Fatalf("键集查询失败: %v", err)
	}
	if page.Total != -1 || page.Page != 3 || page.PageSize != 10 {
		t.Fatalf("键集分页响应不符: %+v", page)
	}
	sel := findQuery(fdb, "ORDER BY occurred_at")
	if !strings.Contains(sel.query, "(occurred_at < ? OR (occurred_at = ? AND id < ?))") {
		t.Fatalf("键集条件不符: %s", sel.query)
	}
	if strings.Contains(sel.query, "OFFSET") {
		t.Fatalf("键集分页不得使用 OFFSET: %s", sel.query)
	}
	if strings.Contains(sel.query, "is_access = ?") {
		t.Fatalf("view=all 不得过滤 is_access: %s", sel.query)
	}
	args := sel.text()
	if !strings.Contains(args, "42") || !strings.Contains(args, before.Format("2006-01-02")) {
		t.Fatalf("键集参数未透传: %s", args)
	}
	if hasQuery(fdb, "COUNT(*)") {
		t.Fatal("键集分页请求不得回表计数")
	}
}

func TestSQLLogReader_PageOverflowRejected(t *testing.T) {
	db, fdb := newFakeDB(t)
	_, err := (SQLLogReader{DB: db}).ListLogs(context.Background(), LogFilter{
		Page: math.MaxInt, PageSize: 100,
	})
	if err == nil || !errors.Is(err, ErrInvalidParam) {
		t.Fatalf("超大 page 必须返回参数错误（不得溢出/发出负 OFFSET）: %v", err)
	}
	if len(fdb.calls) != 0 {
		t.Fatalf("超大 page 不得执行任何 SQL: %+v", fdb.calls)
	}
}

func TestSQLLogReader_PageOffsetClamped(t *testing.T) {
	db, fdb := newFakeDB(t)
	_, err := (SQLLogReader{DB: db}).ListLogs(context.Background(), LogFilter{
		Page: 1000000, PageSize: 100,
	})
	if err == nil || !errors.Is(err, ErrInvalidParam) {
		t.Fatalf("过深 page 必须引导使用键集分页: %v", err)
	}
	if len(fdb.calls) != 0 {
		t.Fatalf("过深 page 不得执行任何 SQL: %+v", fdb.calls)
	}
}

func TestFixtureLogsKeysetNoRepeat(t *testing.T) {
	logs := FixtureDeps().Logs
	first, err := logs.ListLogs(context.Background(), LogFilter{View: "activity", Page: 1, PageSize: 2})
	if err != nil {
		t.Fatalf("fixture 首页失败: %v", err)
	}
	if len(first.Items) != 2 {
		t.Fatalf("fixture 首页条数不符: %+v", first.Items)
	}
	last := first.Items[len(first.Items)-1]
	second, err := logs.ListLogs(context.Background(), LogFilter{
		View: "activity", Page: 1, PageSize: 2,
		BeforeTime: last.OccurredAt, BeforeID: last.ID,
	})
	if err != nil {
		t.Fatalf("fixture 键集页失败: %v", err)
	}
	if len(second.Items) != 1 {
		t.Fatalf("fixture 键集页应只剩 1 条: %+v", second.Items)
	}
	seen := map[int64]bool{}
	for _, it := range append(first.Items, second.Items...) {
		if seen[it.ID] {
			t.Fatalf("键集分页重复返回 id=%d", it.ID)
		}
		seen[it.ID] = true
	}
}

func TestSQLLogReader_AccessView(t *testing.T) {
	db, fdb := newFakeDB(t)
	fdb.onRows("SELECT COUNT(*)", []string{"n"}, []driver.Value{int64(0)})
	fdb.onRows("ORDER BY occurred_at", appLogListColumns)
	if _, err := (SQLLogReader{DB: db}).ListLogs(context.Background(), LogFilter{
		View: "access", Page: 1, PageSize: 20,
	}); err != nil {
		t.Fatalf("access 查询失败: %v", err)
	}
	sel := findQuery(fdb, "ORDER BY occurred_at")
	if got := fmt.Sprint(sel.arg(5)); got != "1" {
		t.Fatalf("access 视图 is_access 参数应为 1，got %#v (args=%s)", sel.arg(5), sel.text())
	}
	if !strings.Contains(sel.query, "is_access = ?") {
		t.Fatalf("access 视图必须过滤 is_access: %s", sel.query)
	}
}

func TestSQLLogReader_GetLogFullDetail(t *testing.T) {
	db, fdb := newFakeDB(t)
	occurred := time.Date(2026, 10, 30, 8, 0, 0, 0, time.UTC)
	longDetail := strings.Repeat("详情", 400)
	fdb.onRows("WHERE id =", appLogDetailColumns, []driver.Value{
		int64(9), "lg-9", "work-log", "warn", "business", "worklog.task.failed",
		"boom", "出工任务失败", "raw boom", `{"ctx":1}`, longDetail,
		"tr-9", int64(5), int64(2), occurred, occurred, "file", int64(1),
	})
	entry, err := (SQLLogReader{DB: db}).GetLog(context.Background(), 9)
	if err != nil {
		t.Fatalf("详情查询失败: %v", err)
	}
	if entry.ID != 9 || entry.Detail != longDetail || entry.ContextJSON != `{"ctx":1}` ||
		!entry.IsAccess || entry.TraceID != "tr-9" || entry.LogID != "lg-9" {
		t.Fatalf("详情字段不符: %+v", entry)
	}
	if entry.DetailTruncated {
		t.Fatal("单条详情不得标记截断")
	}
}

func TestSQLLogReader_GetLogNotFound(t *testing.T) {
	db, _ := newFakeDB(t)
	_, err := (SQLLogReader{DB: db}).GetLog(context.Background(), 404)
	if err == nil || !errors.Is(err, ErrNotFound) {
		t.Fatalf("不存在应返回 not_found: %v", err)
	}
}

// ---- 路由：单条详情 ----

func TestAppLogDetail(t *testing.T) {
	logs := &recordingLogs{detail: LogEntry{ID: 42, Detail: "完整细节", MessageCN: "中文"}}
	h := adminRouter(Deps{Logs: logs})
	status, r := doReqAuth(t, h, http.MethodGet, "/api/v1/app-logs/detail?id=42", "")
	assertOK(t, status, r)
	var entry LogEntry
	if err := json.Unmarshal(r.Data, &entry); err != nil {
		t.Fatalf("data 解码失败: %v", err)
	}
	if logs.detailID != 42 || entry.ID != 42 || entry.Detail != "完整细节" {
		t.Fatalf("详情接口不符: id=%d entry=%+v", logs.detailID, entry)
	}

	for _, target := range []string{"/api/v1/app-logs/detail", "/api/v1/app-logs/detail?id=0", "/api/v1/app-logs/detail?id=x"} {
		status, r := doReqAuth(t, h, http.MethodGet, target, "")
		assertErr(t, status, http.StatusBadRequest, r, CodeInvalidParam)
	}

	logs2 := &recordingLogs{detailErr: NotFound("日志不存在")}
	h2 := adminRouter(Deps{Logs: logs2})
	status, r = doReqAuth(t, h2, http.MethodGet, "/api/v1/app-logs/detail?id=7", "")
	assertErr(t, status, http.StatusNotFound, r, CodeNotFound)

	h3 := adminRouter(Deps{})
	status, r = doReqAuth(t, h3, http.MethodGet, "/api/v1/app-logs/detail?id=7", "")
	assertErr(t, status, http.StatusServiceUnavailable, r, CodeDependencyUnavailable)
}

// ---- 健康 ----

func TestAppLogsHealth(t *testing.T) {
	h := adminRouter(Deps{LogHealth: fakeLogHealth{h: LogHealth{
		Dir: "/data/logs", ActiveSegment: "app-20261031-000001.log", SegmentBytes: 4096,
		Written: 120, Imported: 100, Pending: 20000,
		WriteErrors: 1, DiskFallbacks: 2, InsertErrors: 3,
		ArchivedSegments: 7, DBConnected: false,
		CursorSegment: "app-20261030-000001.log", CursorOffset: 1024,
		LastImport: "2026-10-31T08:00:00Z", LastError: "insert: timeout",
		ArchiveOldest: "2025-01-01",
	}}})
	status, r := doReqAuth(t, h, http.MethodGet, "/api/v1/app-logs/health", "")
	assertOK(t, status, r)
	var health LogHealth
	if err := json.Unmarshal(r.Data, &health); err != nil {
		t.Fatalf("data 解码失败: %v", err)
	}
	if health.Pending != 20000 || health.WriteErrors != 1 || health.DiskFallbacks != 2 ||
		health.InsertErrors != 3 || health.ArchivedSegments != 7 || health.DBConnected ||
		health.LastImport != "2026-10-31T08:00:00Z" || health.ArchiveOldest != "2025-01-01" ||
		health.ActiveSegment != "app-20261031-000001.log" || health.SegmentBytes != 4096 {
		t.Fatalf("健康字段不符: %+v", health)
	}
	if health.Status != "degraded" || health.Alert == "" {
		t.Fatalf("异常计数必须给出降级状态与告警: %+v", health)
	}
	for _, want := range []string{"磁盘", "待补录", "入库失败"} {
		if !strings.Contains(health.Alert, want) {
			t.Fatalf("告警缺少 %q: %s", want, health.Alert)
		}
	}

	hNil := adminRouter(Deps{})
	status, r = doReqAuth(t, hNil, http.MethodGet, "/api/v1/app-logs/health", "")
	assertErr(t, status, http.StatusServiceUnavailable, r, CodeDependencyUnavailable)
}

func TestLogHealthAlertGoesToStderr(t *testing.T) {
	var buf bytes.Buffer
	logHealthAlertMu.Lock()
	oldWriter := logHealthAlertWriter
	oldAt, oldLast := logHealthAlertAt, logHealthAlertLast
	logHealthAlertWriter = &buf
	logHealthAlertAt, logHealthAlertLast = time.Time{}, ""
	logHealthAlertMu.Unlock()
	defer func() {
		logHealthAlertMu.Lock()
		logHealthAlertWriter = oldWriter
		logHealthAlertAt, logHealthAlertLast = oldAt, oldLast
		logHealthAlertMu.Unlock()
	}()

	h := LogHealth{Pending: 20000, DiskFallbacks: 1, DBConnected: false}
	annotateLogHealth(&h)
	if h.Status != "degraded" || !strings.Contains(buf.String(), "[logging]") ||
		!strings.Contains(buf.String(), "待补录") {
		t.Fatalf("关键告警必须同时写 stderr: status=%s stderr=%q", h.Status, buf.String())
	}
	first := buf.String()
	annotateLogHealth(&h) // 同消息 30s 内只输出一次
	if buf.String() != first {
		t.Fatalf("告警未按消息节流: %q -> %q", first, buf.String())
	}
}

func TestHealthzExposesLogDiskAndBacklog(t *testing.T) {
	h := NewRouter(Deps{LogHealth: fakeLogHealth{h: LogHealth{
		Pending: 30000, DiskFallbacks: 2, WriteErrors: 1, InsertErrors: 4, DBConnected: false,
	}}})
	status, r := doReq(t, h, http.MethodGet, "/healthz", "")
	if status != http.StatusOK {
		t.Fatalf("healthz 必须恒为 200: %d", status)
	}
	var data map[string]any
	if err := json.Unmarshal(r.Data, &data); err != nil {
		t.Fatalf("data 解码失败: %v", err)
	}
	if data["status"] != "ok" {
		t.Fatalf("进程存活状态应保持 ok: %+v", data)
	}
	logs, ok := data["logs"].(map[string]any)
	if !ok {
		t.Fatalf("healthz 必须携带 logs 健康段: %+v", data)
	}
	if logs["status"] != "degraded" {
		t.Fatalf("磁盘/积压异常应降级: %+v", logs)
	}
	alert, _ := logs["alert"].(string)
	for _, want := range []string{"磁盘", "待补录", "入库失败", "数据库"} {
		if !strings.Contains(alert, want) {
			t.Fatalf("healthz 告警缺少 %q: %s", want, alert)
		}
	}
	if logs["pending"] != float64(30000) || logs["db_connected"] != false {
		t.Fatalf("healthz 计数不符: %+v", logs)
	}

	hOK := NewRouter(Deps{LogHealth: fakeLogHealth{h: LogHealth{
		Imported: 10, DBConnected: true, LastImport: "2026-10-31T08:00:00Z",
	}}})
	status, r = doReq(t, hOK, http.MethodGet, "/healthz", "")
	_ = status
	logs = nil
	var okData map[string]any
	_ = json.Unmarshal(r.Data, &okData)
	if logs, _ = okData["logs"].(map[string]any); logs == nil || logs["status"] != "ok" || logs["alert"] != nil {
		t.Fatalf("健康时应为 ok 且无告警: %+v", okData)
	}
}

func TestHealthzLogHealthShortCache(t *testing.T) {
	now := time.Date(2026, 10, 31, 8, 0, 0, 0, time.UTC)
	src := &countingLogHealth{h: LogHealth{Imported: 1, DBConnected: true}}
	a := &api{
		d:      Deps{LogHealth: src},
		health: newLogHealthCache(2*time.Second, func() time.Time { return now }),
	}
	for i := 0; i < 2; i++ {
		rec := httptest.NewRecorder()
		a.healthz(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("healthz status=%d", rec.Code)
		}
	}
	if src.calls != 1 {
		t.Fatalf("短缓存内不应重复读取健康快照: calls=%d", src.calls)
	}
	now = now.Add(3 * time.Second)
	rec := httptest.NewRecorder()
	a.healthz(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if src.calls != 2 {
		t.Fatalf("缓存过期后应重新读取: calls=%d", src.calls)
	}
}

func TestDurableLogHealthAdapter(t *testing.T) {
	dir := t.TempDir()
	writeGzipLines(t, filepath.Join(dir, "app-20250101-000001.log.gz"), nil)
	sink, err := logging.NewDurableSinkWithOptions(dir, logging.DefaultDurableOptions())
	if err != nil {
		t.Fatalf("创建持久层失败: %v", err)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = sink.CloseContext(ctx)
	}()
	health := (DurableLogHealth{Sink: sink}).LogHealth()
	if health.Dir != dir || health.Written != 0 || health.ArchiveOldest != "2025-01-01" {
		t.Fatalf("健康适配不符: %+v", health)
	}
	if health.ActiveSegment == "" || health.DBConnected {
		t.Fatalf("活跃段/数据库状态不符: %+v", health)
	}
}

// ---- 一次性导出 ticket ----

func TestLogTicketStoreExpiryAndSingleUse(t *testing.T) {
	now := time.Date(2026, 10, 31, 8, 0, 0, 0, time.UTC)
	store := newLogTicketStore(time.Minute, func() time.Time { return now })
	issued, err := store.issue(LogFilter{View: "activity", AppCode: "work-log"})
	if err != nil {
		t.Fatalf("签发 ticket 失败: %v", err)
	}
	if len(issued.Token) != 64 || strings.Contains(issued.Token, ".") {
		t.Fatalf("ticket 应为 32 字节随机十六进制而非 JWT: %q", issued.Token)
	}
	if issued.ExpiresIn != 60 || !strings.Contains(issued.URL, "/api/v1/app-logs/export/download?ticket=") {
		t.Fatalf("ticket 元数据不符: %+v", issued)
	}
	if !strings.Contains(issued.Filename, ".ndjson") {
		t.Fatalf("ticket 文件名不符: %q", issued.Filename)
	}
	got, _, ok := store.redeem(issued.Token)
	if !ok || got.AppCode != "work-log" || got.View != "activity" {
		t.Fatalf("ticket 首次兑换失败: ok=%v filter=%+v", ok, got)
	}
	if _, _, ok := store.redeem(issued.Token); ok {
		t.Fatal("ticket 必须一次性，不能重复兑换")
	}

	expiring, err := store.issue(LogFilter{View: "all"})
	if err != nil {
		t.Fatalf("签发第二个 ticket 失败: %v", err)
	}
	now = now.Add(61 * time.Second)
	if _, _, ok := store.redeem(expiring.Token); ok {
		t.Fatal("过期 ticket 必须拒绝")
	}
}

func TestAppLogsExportTicketFlow(t *testing.T) {
	dir := t.TempDir()
	writeGzipLines(t, filepath.Join(dir, "app-20261030-000001.log.gz"), []LogEntry{
		{ID: 1, LogID: "lg-ticket", AppCode: "work-log", Level: "info", Category: "business",
			MessageCN: "照片上传成功", OccurredAt: time.Date(2026, 10, 30, 8, 0, 0, 0, time.UTC)},
	})
	h := adminRouter(Deps{
		LogFiles:  NDJSONLogSource{Dir: dir},
		LogHealth: fakeLogHealth{h: LogHealth{DBConnected: true}},
	})
	status, r := doReqAuth(t, h, http.MethodPost, "/api/v1/app-logs/export/ticket?view=activity", "")
	assertOK(t, status, r)
	var data struct {
		Ticket    string `json:"ticket"`
		URL       string `json:"url"`
		Filename  string `json:"filename"`
		ExpiresIn int    `json:"expires_in"`
	}
	if err := json.Unmarshal(r.Data, &data); err != nil {
		t.Fatalf("ticket data 解码失败: %v", err)
	}
	if data.Ticket == "" || data.ExpiresIn != 60 || data.Filename == "" ||
		!strings.HasPrefix(data.URL, "/api/v1/app-logs/export/download?ticket=") {
		t.Fatalf("ticket 响应不符: %+v", data)
	}

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, data.URL, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("ticket 下载失败: %d %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"lg-ticket"`) || !strings.Contains(body, `"__export__":"complete"`) {
		t.Fatalf("ticket 下载内容不符: %s", body)
	}

	status, r = doReq(t, h, http.MethodGet, data.URL, "")
	assertErr(t, status, http.StatusForbidden, r, "forbidden")
}

func TestAppLogsExportTicketDiskFallbackFields(t *testing.T) {
	dir := t.TempDir()
	writeGzipLines(t, filepath.Join(dir, "app-20261030-000001.log.gz"), []LogEntry{
		{LogID: "lg-disk-ticket", AppCode: "system", Category: "runtime",
			OccurredAt: time.Date(2026, 10, 30, 8, 0, 0, 0, time.UTC)},
	})
	h := adminRouter(Deps{
		LogFiles:  NDJSONLogSource{Dir: dir},
		LogHealth: fakeLogHealth{h: LogHealth{DBConnected: true, DiskFallbacks: 3}},
	})
	status, r := doReqAuth(t, h, http.MethodPost, "/api/v1/app-logs/export/ticket?view=activity", "")
	assertOK(t, status, r)
	var data struct {
		FileCopyIncomplete bool  `json:"file_copy_incomplete"`
		DiskFallbacks      int64 `json:"disk_fallbacks"`
	}
	if err := json.Unmarshal(r.Data, &data); err != nil {
		t.Fatalf("ticket data 解码失败: %v", err)
	}
	if !data.FileCopyIncomplete || data.DiskFallbacks != 3 {
		t.Fatalf("ticket 必须携带磁盘回退警告字段: %+v", data)
	}
}

func TestAppLogsExportDownloadRejectsInvalidTicket(t *testing.T) {
	h := NewRouter(Deps{LogFiles: NDJSONLogSource{Dir: t.TempDir()}})
	for _, target := range []string{
		"/api/v1/app-logs/export/download",
		"/api/v1/app-logs/export/download?ticket=bogus",
	} {
		status, r := doReq(t, h, http.MethodGet, target, "")
		assertErr(t, status, http.StatusForbidden, r, "forbidden")
	}
}

func TestLogExportPublicTicketException(t *testing.T) {
	// 唯一例外：GET /api/v1/app-logs/export/download?ticket= 凭超管签发的 60s 一次性
	// export capability ticket 访问；其余 app-logs 路径（含 ticket 签发）仍超管。
	if !publicAuthPath("/api/v1/app-logs/export/download") {
		t.Fatal("export/download 必须凭 ticket 放行")
	}
	for _, p := range []string{
		"/api/v1/app-logs/export",
		"/api/v1/app-logs/export/ticket",
		"/api/v1/app-logs/export/download/",
		"/api/v1/app-logs/health",
		"/api/v1/app-logs/detail",
	} {
		if publicAuthPath(p) {
			t.Fatalf("%s 不得免登录放行（仅 download 精确路径例外）", p)
		}
	}
	for _, p := range []string{
		"/api/v1/app-logs",
		"/api/v1/app-logs/health",
		"/api/v1/app-logs/detail",
		"/api/v1/app-logs/export",
		"/api/v1/app-logs/export/ticket",
		"/api/v1/app-logs/export/download",
		"/api/v1/app-logs/anything",
	} {
		if !isAdminPath(p) {
			t.Fatalf("%s 必须纳入超管门控（download 由 ticket 例外放行）", p)
		}
	}
}

func TestNDJSONLogSourceProbe(t *testing.T) {
	ctx := context.Background()
	missing := NDJSONLogSource{Dir: filepath.Join(t.TempDir(), "missing")}
	if err := missing.Probe(ctx); err == nil || !errors.Is(err, ErrDependencyUnavailable) {
		t.Fatalf("目录缺失必须 Probe 失败: %v", err)
	}
	empty := NDJSONLogSource{Dir: t.TempDir()}
	if err := empty.Probe(ctx); err != nil {
		t.Fatalf("空目录应可导出空流: %v", err)
	}

	dir := t.TempDir()
	writeGzipLines(t, filepath.Join(dir, "app-20261030-000001.log.gz"), []LogEntry{
		{LogID: "lg-probe", AppCode: "system", Category: "runtime",
			OccurredAt: time.Date(2026, 10, 30, 8, 0, 0, 0, time.UTC)},
	})
	src := NDJSONLogSource{Dir: dir}
	if err := src.Probe(ctx); err != nil {
		t.Fatalf("有效 gzip 应 Probe 成功: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "app-20261030-000002.log.gz"), []byte("not gzip"), 0o600); err != nil {
		t.Fatalf("写坏 gzip 失败: %v", err)
	}
	if err := src.Probe(ctx); err == nil || !errors.Is(err, ErrDependencyUnavailable) {
		t.Fatalf("损坏 gzip 必须 Probe 失败: %v", err)
	}
}

func TestAppLogsExportTicketProbeFailure(t *testing.T) {
	src := &probeLogFiles{probeErr: DependencyUnavailable("日志归档不可读：app-20261030-000001.log.gz")}
	h := adminRouter(Deps{LogFiles: src})
	status, r := doReqAuth(t, h, http.MethodPost, "/api/v1/app-logs/export/ticket?view=activity", "")
	assertErr(t, status, http.StatusServiceUnavailable, r, CodeDependencyUnavailable)
	if !strings.Contains(r.Message, "归档") {
		t.Fatalf("应返回真实 Probe 错误: %+v", r)
	}
	if src.probeCalls != 1 || src.streamCalls != 0 {
		t.Fatalf("Probe 失败不得发票据/开始流: probe=%d stream=%d", src.probeCalls, src.streamCalls)
	}
	if strings.Contains(string(r.Data), "ticket") {
		t.Fatalf("Probe 失败响应不得含 ticket: %s", r.Data)
	}
}

func TestAppLogsExportDownloadFirstSegmentFailureSentinel(t *testing.T) {
	src := &probeLogFiles{streamErr: errors.New("gzip: invalid header")}
	h := adminRouter(Deps{LogFiles: src})
	status, r := doReqAuth(t, h, http.MethodPost, "/api/v1/app-logs/export/ticket?view=activity", "")
	assertOK(t, status, r)
	var data struct {
		URL string `json:"url"`
	}
	if err := json.Unmarshal(r.Data, &data); err != nil {
		t.Fatalf("ticket data 解码失败: %v", err)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, data.URL, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("下载首段失败也必须 200 NDJSON: %d %s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/x-ndjson") {
		t.Fatalf("首段失败响应必须为 NDJSON: %q", ct)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"__export__":"failed"`) || !strings.Contains(body, `"count":0`) ||
		!strings.Contains(body, "gzip: invalid header") {
		t.Fatalf("首段失败必须带 failed 哨兵与真实错误: %s", body)
	}
	if strings.Contains(body, `"code":"dependency_unavailable"`) || strings.Contains(body, `"code":"internal_error"`) {
		t.Fatalf("下载失败不得保存 JSON 错误信封: %s", body)
	}
	if got := rec.Header().Get("X-Log-Export-Status"); got != "failed" {
		t.Fatalf("首段失败 trailer 必须 failed: %q", got)
	}
	if src.streamCalls != 1 {
		t.Fatalf("下载应调用一次 StreamLogs，got %d", src.streamCalls)
	}
}

func TestAppLogsExportDownloadNilSourceKeepsTicket(t *testing.T) {
	store := newLogTicketStore(time.Minute, time.Now)
	issued, err := store.issue(LogFilter{View: "activity"})
	if err != nil {
		t.Fatalf("签发 ticket 失败: %v", err)
	}
	a := &api{d: Deps{LogFiles: nil}, tickets: store}
	rec := httptest.NewRecorder()
	a.appLogsExportDownload(rec, httptest.NewRequest(http.MethodGet, issued.URL, nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("LogFiles 未接入应 503: %d %s", rec.Code, rec.Body.String())
	}
	if _, _, ok := store.redeem(issued.Token); !ok {
		t.Fatal("LogFiles 为空时不得先消耗 ticket")
	}
}

func TestLogTicketQueryMaskedInAccessLog(t *testing.T) {
	dir := t.TempDir()
	writeGzipLines(t, filepath.Join(dir, "app-20261030-000001.log.gz"), []LogEntry{
		{LogID: "lg-mask", AppCode: "system", Category: "runtime", MessageCN: "启动",
			OccurredAt: time.Date(2026, 10, 30, 8, 0, 0, 0, time.UTC)},
	})
	sink := &captureSink{}
	base := adminRouter(Deps{LogFiles: NDJSONLogSource{Dir: dir}})
	h := WithAccessLogOptions(base, AccessLogOptions{
		Sink: sink,
		Auth: &fakeAuth{ident: auth.Identity{ID: 1, Role: "admin"}},
	})
	status, r := doReqAuth(t, h, http.MethodPost, "/api/v1/app-logs/export/ticket?view=activity", "")
	assertOK(t, status, r)
	var data struct {
		Ticket string `json:"ticket"`
		URL    string `json:"url"`
	}
	if err := json.Unmarshal(r.Data, &data); err != nil {
		t.Fatalf("ticket data 解码失败: %v", err)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, data.URL, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("下载失败: %d", rec.Code)
	}

	found := false
	for _, e := range sink.snapshot() {
		if e.Event != "http.request" || !strings.Contains(e.Message, "/api/v1/app-logs/export/download") {
			continue
		}
		found = true
		var detail struct {
			Query map[string]string `json:"query"`
		}
		if err := json.Unmarshal([]byte(e.Detail), &detail); err != nil {
			t.Fatalf("访问日志 detail 不是 JSON: %v %s", err, e.Detail)
		}
		if detail.Query["ticket"] != "****" {
			t.Fatalf("ticket query 未脱敏: %+v", detail.Query)
		}
		if strings.Contains(e.Detail, data.Ticket) || strings.Contains(e.Message, data.Ticket) {
			t.Fatalf("访问日志泄露 ticket: detail=%s message=%s", e.Detail, e.Message)
		}
	}
	if !found {
		t.Fatalf("未找到导出下载访问日志: %+v", sink.snapshot())
	}
}

// ---- 导出 ----

func TestAppLogsExportStreamsNDJSON(t *testing.T) {
	dir := t.TempDir()
	older := time.Date(2026, 10, 30, 8, 0, 0, 0, time.UTC)
	newer := time.Date(2026, 10, 31, 8, 0, 0, 0, time.UTC)
	accessAt := time.Date(2026, 10, 30, 9, 0, 0, 0, time.UTC)
	writeGzipLines(t, filepath.Join(dir, "app-20261030-000001.log.gz"), []LogEntry{
		{ID: 1, LogID: "lg-1", AppCode: "work-log", Level: "info", Category: "business", Event: "photo.upload",
			MessageCN: "照片上传成功", Message: "photo upload", OccurredAt: older, RawMessage: "uploaded", Detail: "完整细节"},
		{ID: 2, LogID: "lg-2", AppCode: "work-log", Level: "info", Category: "access", Event: "http.request",
			MessageCN: "访问成功", Message: "GET /api/v1/worklog", OccurredAt: accessAt, IsAccess: true},
	})
	active := filepath.Join(dir, "app-20261031-000001.log")
	lines := []string{
		mustJSON(t, LogEntry{ID: 3, LogID: "lg-3", AppCode: "system", Level: "error", Category: "runtime",
			Event: "runtime.panic", MessageCN: "照片任务异常", Message: "panic", OccurredAt: newer}),
		"{not-json",
		`{"log_id":"half-line","occurred_at":"2026-10-31T09:00:00Z","message_cn":"照片半行"}`,
	}
	if err := os.WriteFile(active, []byte(strings.Join(lines, "\n")), 0o600); err != nil {
		t.Fatalf("写入活跃段失败: %v", err)
	}

	src := NDJSONLogSource{Dir: dir}
	h := adminRouter(Deps{LogFiles: src})
	q := "?view=activity&since=2026-10-30T00:00:00Z&until=2026-11-01T00:00:00Z&keyword=%E7%85%A7%E7%89%87"
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/app-logs/export"+q, nil)
	req.Header.Set("Authorization", "Bearer good-token")
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("导出失败: %d %s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/x-ndjson") {
		t.Fatalf("导出必须为 NDJSON: %q", ct)
	}
	if cd := rec.Header().Get("Content-Disposition"); !strings.Contains(cd, ".ndjson") {
		t.Fatalf("缺少下载文件名: %q", cd)
	}
	body := rec.Body.String()
	if strings.HasPrefix(strings.TrimSpace(body), "{") && strings.Contains(body, `"code"`) && !strings.Contains(body, `"log_id"`) {
		t.Fatalf("导出不得走 JSON 信封: %s", body)
	}
	var got []LogEntry
	var markers []exportMarker
	for _, line := range strings.Split(strings.TrimSpace(body), "\n") {
		if line == "" {
			continue
		}
		var probe struct {
			Export string `json:"__export__"`
		}
		if err := json.Unmarshal([]byte(line), &probe); err != nil {
			t.Fatalf("导出行不是 JSON: %v %s", err, line)
		}
		if probe.Export != "" {
			var m exportMarker
			if err := json.Unmarshal([]byte(line), &m); err != nil {
				t.Fatalf("导出哨兵不是 JSON: %v %s", err, line)
			}
			markers = append(markers, m)
			continue
		}
		var e LogEntry
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			t.Fatalf("导出行不是 JSON: %v %s", err, line)
		}
		got = append(got, e)
	}
	if len(got) != 2 {
		t.Fatalf("导出应含 2 条活动日志（含未入库、跳过 access/坏行/半行），got %d: %s", len(got), body)
	}
	// 存在坏行与半行时仍导出可读条目，但必须明确 incomplete（不得标 complete）。
	if len(markers) != 1 || markers[0].Kind != "incomplete" || markers[0].Count != 2 {
		t.Fatalf("坏行/半行导出必须只有一个 incomplete 哨兵: %+v", markers)
	}
	if markers[0].CorruptLines < 1 || markers[0].HalfLines < 1 {
		t.Fatalf("哨兵应声明坏行/半行数量: %+v", markers[0])
	}
	if got[0].LogID != "lg-1" || got[1].LogID != "lg-3" {
		t.Fatalf("导出顺序/内容不符: %+v", got)
	}
	if got[0].Detail != "完整细节" {
		t.Fatalf("导出应包含完整 detail: %+v", got[0])
	}
	if got[0].CreatedAt.IsZero() || got[1].CreatedAt.IsZero() {
		t.Fatalf("文件日志缺少 created_at 时应回退事件时间: %+v", got)
	}
	if status := rec.Header().Get("X-Log-Export-Status"); status != "incomplete" {
		t.Fatalf("含坏行/半行时 trailer 必须 incomplete，got %q", status)
	}
	if count := rec.Header().Get("X-Log-Export-Count"); count != "2" {
		t.Fatalf("完成 trailer 条数不符: %q", count)
	}

	// access 视图只导出访问日志
	rec2 := httptest.NewRecorder()
	req2 := httptest.NewRequest(http.MethodGet, "/api/v1/app-logs/export?view=access", nil)
	req2.Header.Set("Authorization", "Bearer good-token")
	h.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusOK || !strings.Contains(rec2.Body.String(), `"lg-2"`) ||
		strings.Contains(rec2.Body.String(), `"lg-1"`) {
		t.Fatalf("access 视图导出不符: %d %s", rec2.Code, rec2.Body.String())
	}
}

func TestAppLogsExportFailureMarker(t *testing.T) {
	entry := LogEntry{LogID: "lg-fail", AppCode: "system", Category: "runtime",
		MessageCN: "启动", OccurredAt: time.Date(2026, 10, 30, 8, 0, 0, 0, time.UTC)}
	h := adminRouter(Deps{
		LogFiles:  failingLogFiles{entries: []LogEntry{entry}, err: errors.New("gzip: invalid checksum")},
		LogHealth: fakeLogHealth{h: LogHealth{DBConnected: true}},
	})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/app-logs/export?view=activity", nil)
	req.Header.Set("Authorization", "Bearer good-token")
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("已开始输出后仍应为 200: %d %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"lg-fail"`) || !strings.Contains(body, `"__export__":"failed"`) {
		t.Fatalf("中途失败必须写 failed 哨兵: %s", body)
	}
	if strings.Contains(body, `"__export__":"complete"`) {
		t.Fatalf("失败流不得出现 complete 哨兵: %s", body)
	}
	if status := rec.Header().Get("X-Log-Export-Status"); status != "failed" {
		t.Fatalf("失败流必须设置 failed trailer/标记，got %q", status)
	}
	var marker exportMarker
	for _, line := range strings.Split(strings.TrimSpace(body), "\n") {
		if strings.Contains(line, `"__export__"`) {
			if err := json.Unmarshal([]byte(line), &marker); err != nil {
				t.Fatalf("失败哨兵解析失败: %v", err)
			}
		}
	}
	if marker.Kind != "failed" || marker.Count != 1 || marker.Error == "" {
		t.Fatalf("失败哨兵内容不符: %+v", marker)
	}
}

func TestAppLogsExportDiskFallbackWarning(t *testing.T) {
	dir := t.TempDir()
	writeGzipLines(t, filepath.Join(dir, "app-20261030-000001.log.gz"), []LogEntry{
		{LogID: "lg-disk", AppCode: "system", Category: "runtime", MessageCN: "启动",
			OccurredAt: time.Date(2026, 10, 30, 8, 0, 0, 0, time.UTC)},
	})
	h := adminRouter(Deps{
		LogFiles:  NDJSONLogSource{Dir: dir},
		LogHealth: fakeLogHealth{h: LogHealth{DBConnected: true, DiskFallbacks: 2}},
	})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/app-logs/export?view=activity", nil)
	req.Header.Set("Authorization", "Bearer good-token")
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("导出失败: %d %s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("X-Log-Export-File-Copy"); got != "incomplete" {
		t.Fatalf("磁盘回退时必须声明文件副本不完整，got %q", got)
	}
	if got := rec.Header().Get("X-Log-Export-Disk-Fallbacks"); got != "2" {
		t.Fatalf("磁盘回退次数头不符: %q", got)
	}
	var marker exportMarker
	for _, line := range strings.Split(strings.TrimSpace(rec.Body.String()), "\n") {
		if strings.Contains(line, `"__export__"`) {
			if err := json.Unmarshal([]byte(line), &marker); err != nil {
				t.Fatalf("哨兵解析失败: %v", err)
			}
		}
	}
	if marker.FileCopyComplete || marker.DiskFallbacks != 2 {
		t.Fatalf("完成哨兵必须声明文件副本不完整: %+v", marker)
	}
}

func TestAppLogsExportRealHTTPTrailer(t *testing.T) {
	dir := t.TempDir()
	writeGzipLines(t, filepath.Join(dir, "app-20261030-000001.log.gz"), []LogEntry{
		{LogID: "lg-http", AppCode: "system", Category: "runtime", MessageCN: "启动",
			OccurredAt: time.Date(2026, 10, 30, 8, 0, 0, 0, time.UTC)},
	})
	srv := httptest.NewServer(adminRouter(Deps{
		LogFiles:  NDJSONLogSource{Dir: dir},
		LogHealth: fakeLogHealth{h: LogHealth{DBConnected: true}},
	}))
	defer srv.Close()
	req, err := http.NewRequest(http.MethodGet, srv.URL+"/api/v1/app-logs/export?view=activity", nil)
	if err != nil {
		t.Fatalf("构造请求失败: %v", err)
	}
	req.Header.Set("Authorization", "Bearer good-token")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("真实 HTTP 导出失败: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("导出 status=%d body=%s", resp.StatusCode, body)
	}
	if got := resp.Trailer.Get("X-Log-Export-Status"); got != "complete" {
		t.Fatalf("真实 HTTP trailer 未送达: %q", got)
	}
	if got := resp.Trailer.Get("X-Log-Export-Count"); got != "1" {
		t.Fatalf("真实 HTTP trailer count 不符: %q", got)
	}
	if !strings.Contains(string(body), `"__export__":"complete"`) {
		t.Fatalf("真实 HTTP 导出缺少完成哨兵: %s", body)
	}
}

func TestNDJSONLogSourceCorruptGzip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "app-20261030-000001.log.gz")
	writeGzipLines(t, path, []LogEntry{
		{LogID: "lg-gz", AppCode: "system", Category: "runtime", MessageCN: "启动",
			OccurredAt: time.Date(2026, 10, 30, 8, 0, 0, 0, time.UTC)},
	})
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat gzip: %v", err)
	}
	if err := os.Truncate(path, info.Size()-8); err != nil {
		t.Fatalf("截断 gzip: %v", err)
	}
	src := NDJSONLogSource{Dir: dir}
	err = src.StreamLogs(context.Background(), LogFilter{View: "activity"}, func(LogEntry) error { return nil })
	if err == nil {
		t.Fatal("损坏 gzip 必须返回读取错误，不能静默当作完整导出")
	}
}

func TestNDJSONLogSourceActiveAppendRace(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "app-20261031-000001.log")
	first := mustJSON(t, LogEntry{LogID: "lg-race-1", AppCode: "system", Category: "runtime",
		MessageCN: "一", OccurredAt: time.Date(2026, 10, 31, 8, 0, 0, 0, time.UTC)})
	half := `{"log_id":"lg-race-2","app_code":"system","message_cn":"二","occurred_at":"2026-10-31T08:01:00Z"`
	if err := os.WriteFile(path, []byte(first+"\n"+half), 0o600); err != nil {
		t.Fatalf("写活跃段失败: %v", err)
	}
	src := NDJSONLogSource{Dir: dir}
	count := 0
	err := src.StreamLogs(context.Background(), LogFilter{View: "all"}, func(LogEntry) error {
		count++
		return nil
	})
	var inc *LogExportIncomplete
	if !errors.As(err, &inc) || inc.HalfLines != 1 {
		t.Fatalf("尾部半行必须返回结构化不完整（half_lines=1），实际 %v", err)
	}
	if count != 1 {
		t.Fatalf("读取竞态首轮应只导出完整行，got %d", count)
	}
	if err := os.WriteFile(path, []byte(first+"\n"+half+"}\n"), 0o600); err != nil {
		t.Fatalf("补全半行失败: %v", err)
	}
	count = 0
	if err := src.StreamLogs(context.Background(), LogFilter{View: "all"}, func(LogEntry) error {
		count++
		return nil
	}); err != nil {
		t.Fatalf("补全后读取失败: %v", err)
	}
	if count != 2 {
		t.Fatalf("补全后应导出 2 条，got %d", count)
	}
}

func TestAppLogsExportNoSource(t *testing.T) {
	h := adminRouter(Deps{})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/app-logs/export", nil)
	req.Header.Set("Authorization", "Bearer good-token")
	h.ServeHTTP(rec, req)
	var r resp
	if err := json.Unmarshal(rec.Body.Bytes(), &r); err != nil {
		t.Fatalf("响应应为 JSON 错误信封: %v %s", err, rec.Body.String())
	}
	assertErr(t, rec.Code, http.StatusServiceUnavailable, r, CodeDependencyUnavailable)
}

func TestNDJSONLogSourceRejectsMissingDir(t *testing.T) {
	src := NDJSONLogSource{Dir: filepath.Join(t.TempDir(), "missing")}
	err := src.StreamLogs(context.Background(), LogFilter{View: "all"}, func(LogEntry) error { return nil })
	if err == nil || !errors.Is(err, ErrDependencyUnavailable) {
		t.Fatalf("目录不存在应返回依赖不可用: %v", err)
	}
}

// ---- 辅助 ----

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("JSON 序列化失败: %v", err)
	}
	return string(data)
}

// writeGzipLines 写 gzip 段文件；每个条目一行 NDJSON。
func writeGzipLines(t *testing.T, path string, entries []LogEntry) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("创建 gzip 失败: %v", err)
	}
	zw := gzip.NewWriter(f)
	for _, e := range entries {
		if _, err := io.WriteString(zw, mustJSON(t, e)+"\n"); err != nil {
			t.Fatalf("写 gzip 失败: %v", err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("关闭 gzip 失败: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("关闭文件失败: %v", err)
	}
}
