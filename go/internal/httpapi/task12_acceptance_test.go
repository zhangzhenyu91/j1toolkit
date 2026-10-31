package httpapi_test

// Task 12 日志故障验收（fake/httptest/临时目录，不连真实服务）：
// 一次入站写操作串联 access + audit + 出站 external（同一 trace），
// 以及管理端列表/详情/健康/一次性票据 NDJSON 导出的端到端链路。

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"shade/internal/auth"
	"shade/internal/config"
	"shade/internal/httpapi"
	"shade/internal/integrations"
	"shade/internal/logging"
)

func task12Sink(t *testing.T, dir string) *logging.DurableSink {
	t.Helper()
	opts := logging.DefaultDurableOptions()
	opts.Stderr = io.Discard
	opts.FsyncMode = logging.FsyncAlways
	sink, err := logging.NewDurableSinkWithOptions(dir, opts)
	if err != nil {
		t.Fatalf("创建持久层失败: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = sink.CloseContext(ctx)
	})
	return sink
}

// task12Entries 读取目录内全部未压缩段并按写入顺序返回 JSON 行。
func task12Entries(t *testing.T, dir string) []map[string]any {
	t.Helper()
	files, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("读取日志目录: %v", err)
	}
	var names []string
	for _, f := range files {
		if !f.IsDir() && strings.HasSuffix(f.Name(), ".log") {
			names = append(names, f.Name())
		}
	}
	sort.Strings(names)
	var out []map[string]any
	for _, name := range names {
		f, err := os.Open(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("打开段 %s: %v", name, err)
		}
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			line := strings.TrimSpace(sc.Text())
			if line == "" {
				continue
			}
			var rec map[string]any
			if err := json.Unmarshal([]byte(line), &rec); err != nil {
				_ = f.Close()
				t.Fatalf("段 %s 含非 NDJSON 行: %v", name, err)
			}
			out = append(out, rec)
		}
		if err := sc.Err(); err != nil {
			_ = f.Close()
			t.Fatalf("扫描段 %s: %v", name, err)
		}
		_ = f.Close()
	}
	return out
}

func TestTask12AccessAuditOutboundShareTrace(t *testing.T) {
	dir := t.TempDir()
	sink := task12Sink(t, dir)

	integrations.SetObservedRecorder(logging.NewWithSink(sink, "system"))
	t.Cleanup(func() { integrations.SetObservedRecorder(nil) })

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "upstream failed", http.StatusInternalServerError)
	}))
	defer upstream.Close()
	client := &http.Client{Transport: integrations.NewSharedTransport()}

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, upstream.URL+"/v1/run?token=SECRET", nil)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		resp, err := client.Do(req)
		if err == nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	h := httpapi.WithAccessLogOptions(handler, httpapi.AccessLogOptions{Sink: sink})

	req := httptest.NewRequest(http.MethodPost, "/api/v1/worklog/logs?token=SECRET", strings.NewReader(`{}`))
	req.Header.Set("X-Trace-Id", "trace-task12-e2e")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("入站请求 status=%d", rec.Code)
	}
	if got := rec.Header().Get("X-Trace-Id"); got != "trace-task12-e2e" {
		t.Fatalf("响应头 trace 未回写: %q", got)
	}

	entries := task12Entries(t, dir)
	byCategory := map[string]map[string]any{}
	for _, e := range entries {
		cat, _ := e["category"].(string)
		if cat == "access" || cat == "audit" || cat == "external" {
			if prev, ok := byCategory[cat]; ok {
				t.Fatalf("类别 %s 出现多条（%v / %v）", cat, prev["event"], e["event"])
			}
			byCategory[cat] = e
		}
	}
	for _, cat := range []string{"access", "audit", "external"} {
		e, ok := byCategory[cat]
		if !ok {
			t.Fatalf("缺少 %s 日志：%+v", cat, entries)
		}
		if e["trace_id"] != "trace-task12-e2e" {
			t.Fatalf("%s trace 未串联: %v", cat, e["trace_id"])
		}
	}
	if byCategory["audit"]["event"] != "http.mutation" {
		t.Fatalf("audit 事件不符: %v", byCategory["audit"])
	}
	if byCategory["external"]["event"] != "http.outbound.failed" {
		t.Fatalf("出站事件不符: %v", byCategory["external"])
	}
	detail, _ := byCategory["external"]["detail"].(string)
	for _, want := range []string{`"status":500`, `"attempt":1`, `"service":"http"`, `"/v1/run"`, `"error_class":"http_status"`} {
		if !strings.Contains(detail, want) {
			t.Fatalf("出站 detail 缺 %q: %s", want, detail)
		}
	}
	all, _ := json.Marshal(entries)
	if strings.Contains(string(all), "SECRET") {
		t.Fatalf("日志泄漏 query/token 秘密: %s", all)
	}
}

// task12Logs 固定分页/详情的内存日志读取器。
type task12Logs struct {
	page     httpapi.LogPage
	detail   httpapi.LogEntry
	got      httpapi.LogFilter
	detailID int64
}

func (f *task12Logs) ListLogs(_ context.Context, fl httpapi.LogFilter) (httpapi.LogPage, error) {
	f.got = fl
	return f.page, nil
}

func (f *task12Logs) GetLog(_ context.Context, id int64) (httpapi.LogEntry, error) {
	f.detailID = id
	return f.detail, nil
}

func task12AdminRouter(d httpapi.Deps) http.Handler {
	d.Auth = auth.FixtureAuth{}
	return httpapi.NewRouter(d)
}

func task12Req(t *testing.T, h http.Handler, method, target string) (int, map[string]any) {
	t.Helper()
	req := httptest.NewRequest(method, target, nil)
	req.Header.Set("Authorization", "Bearer fixture-token")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	var envelope struct {
		Code any             `json:"code"`
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("响应不是 JSON 信封: %v body=%s", err, rec.Body.String())
	}
	var data map[string]any
	if len(envelope.Data) > 0 {
		_ = json.Unmarshal(envelope.Data, &data)
	}
	return rec.Code, map[string]any{"code": envelope.Code, "data": data, "raw": envelope.Data}
}

func TestTask12AdminDetailAllHistoryHealthAndExport(t *testing.T) {
	dir := t.TempDir()
	sink := task12Sink(t, dir)
	occurred := time.Date(2026, 10, 31, 8, 30, 0, 0, time.UTC)
	for _, e := range []struct {
		app, cat, event, msg string
		access               bool
	}{
		{"work-log", "business", "worklog.created", "出工日志创建", false},
		{"system", "access", "http.request", "GET /api/v1/app/list", true},
	} {
		if err := sink.Write(context.Background(), configEntry(e.app, e.cat, e.event, e.msg, e.access, occurred)); err != nil {
			t.Fatalf("写日志: %v", err)
		}
	}

	full := httpapi.LogEntry{
		ID: 42, LogID: "lg-42", AppCode: "work-log", Level: "warn", Category: "business",
		Event: "worklog.detail", Message: "完整技术原文", MessageCN: "完整中文说明",
		RawMessage: "raw-full", ContextJSON: `{"k":"v"}`, Detail: "detail-full",
		TraceID: "trace-task12-detail", OccurredAt: occurred, CreatedAt: occurred,
	}
	logs := &task12Logs{
		page: httpapi.LogPage{Items: []httpapi.LogEntry{
			{ID: 42, LogID: "lg-42", AppCode: "work-log", Event: "worklog.detail", TraceID: "trace-task12-detail"},
			{ID: 41, LogID: "lg-41", AppCode: "system", Event: "http.request", IsAccess: true},
		}, Total: 2},
		detail: full,
	}
	h := task12AdminRouter(httpapi.Deps{
		Logs:      logs,
		LogFiles:  httpapi.NDJSONLogSource{Dir: dir},
		LogHealth: httpapi.DurableLogHealth{Sink: sink},
	})

	status, r := task12Req(t, h, http.MethodGet, "/api/v1/app-logs?view=all")
	if status != http.StatusOK || r["code"] != float64(0) {
		t.Fatalf("全部历史列表 status=%d r=%v", status, r)
	}
	if logs.got.View != "all" {
		t.Fatalf("全部历史视图未透传: %+v", logs.got)
	}

	status, r = task12Req(t, h, http.MethodGet, "/api/v1/app-logs/detail?id=42")
	if status != http.StatusOK || r["code"] != float64(0) {
		t.Fatalf("详情 status=%d r=%v", status, r)
	}
	var detail httpapi.LogEntry
	if err := json.Unmarshal(r["raw"].(json.RawMessage), &detail); err != nil {
		t.Fatalf("详情解码: %v", err)
	}
	if detail.Message != "完整技术原文" || detail.ContextJSON != `{"k":"v"}` || detail.TraceID != "trace-task12-detail" {
		t.Fatalf("详情不是完整内容: %+v", detail)
	}

	status, r = task12Req(t, h, http.MethodGet, "/api/v1/app-logs/health")
	if status != http.StatusOK || r["code"] != float64(0) {
		t.Fatalf("健康 status=%d r=%v", status, r)
	}
	var health httpapi.LogHealth
	if err := json.Unmarshal(r["raw"].(json.RawMessage), &health); err != nil {
		t.Fatalf("健康解码: %v", err)
	}
	if health.Pending != 2 || health.DBConnected || health.Status != "degraded" {
		t.Fatalf("健康快照不符: %+v", health)
	}

	status, r = task12Req(t, h, http.MethodPost, "/api/v1/app-logs/export/ticket?view=all")
	if status != http.StatusOK || r["code"] != float64(0) {
		t.Fatalf("ticket status=%d r=%v", status, r)
	}
	var ticket struct {
		URL       string `json:"url"`
		ExpiresIn int    `json:"expires_in"`
	}
	if err := json.Unmarshal(r["raw"].(json.RawMessage), &ticket); err != nil {
		t.Fatalf("ticket 解码: %v", err)
	}
	if ticket.ExpiresIn != 60 || !strings.HasPrefix(ticket.URL, "/api/v1/app-logs/export/download?ticket=") {
		t.Fatalf("ticket 形态不符: %+v", ticket)
	}

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, ticket.URL, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("凭 ticket 下载 status=%d body=%s", rec.Code, rec.Body.String())
	}
	var lines []map[string]any
	sc := bufio.NewScanner(strings.NewReader(rec.Body.String()))
	for sc.Scan() {
		if strings.TrimSpace(sc.Text()) == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal(sc.Bytes(), &m); err != nil {
			t.Fatalf("导出流非 NDJSON: %v %q", err, sc.Text())
		}
		lines = append(lines, m)
	}
	if len(lines) != 3 {
		t.Fatalf("导出应含 2 条日志 + 1 条哨兵，实际 %d: %s", len(lines), rec.Body.String())
	}
	marker := lines[len(lines)-1]
	if marker["__export__"] != "complete" || marker["count"] != float64(2) {
		t.Fatalf("导出哨兵不符: %+v", marker)
	}
	if rec.Header().Get("X-Log-Export-File-Copy") != "complete" {
		t.Fatalf("文件副本状态头不符: %q", rec.Header().Get("X-Log-Export-File-Copy"))
	}

	// 一次性：同一票据再次兑换必须 403。
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, ticket.URL, nil))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("ticket 应一次性，重复兑换 status=%d", rec.Code)
	}
}

func configEntry(app, category, event, msg string, access bool, at time.Time) config.AppLogEntry {
	return config.AppLogEntry{
		AppCode: app, Level: "info", Category: category, Event: event,
		Message: msg, MessageCN: msg, RawMessage: msg, TraceID: "trace-task12-export",
		OccurredAt: at, IsAccess: access,
	}
}
