package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"
	"time"
)

// ---- 测试替身（fake）：可注入依赖的桩实现 ----

type fakeApps struct {
	apps []App
	err  error
}

func (f *fakeApps) ListApps(context.Context) ([]App, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.apps, nil
}

type panicApps struct{}

func (panicApps) ListApps(context.Context) ([]App, error) { panic("boom") }

type fakeCfg struct {
	items     map[string]ConfigItem
	getErr    error // Get 恒错（模拟数据库不可用）
	updateErr error // Update 恒错
	updates   []configUpdateItem
}

func (f *fakeCfg) Get(_ context.Context, key string) (ConfigItem, error) {
	if f.getErr != nil {
		return ConfigItem{}, f.getErr
	}
	it, ok := f.items[key]
	if !ok {
		return ConfigItem{}, NotFound("配置键不存在: " + key)
	}
	return it, nil
}

func (f *fakeCfg) List(_ context.Context) ([]ConfigItem, error) {
	if f.getErr != nil {
		return nil, f.getErr
	}
	out := make([]ConfigItem, 0, len(f.items))
	for _, it := range f.items {
		out = append(out, it)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out, nil
}

func (f *fakeCfg) Update(_ context.Context, key, value string) error {
	if f.updateErr != nil {
		return f.updateErr
	}
	it, ok := f.items[key]
	if !ok {
		return NotFound("配置键不存在: " + key)
	}
	it.Value = value
	f.items[key] = it
	f.updates = append(f.updates, configUpdateItem{Key: key, Value: value})
	return nil
}

type fakeLogs struct {
	page  LogPage
	err   error
	got   LogFilter
	calls int
}

func (f *fakeLogs) ListLogs(_ context.Context, fl LogFilter) (LogPage, error) {
	f.got = fl
	f.calls++
	if f.err != nil {
		return LogPage{}, f.err
	}
	return f.page, nil
}

// ---- 断言辅助 ----

// resp 统一信封的测试解码形状。
type resp struct {
	Code    any             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data"`
}

func doReq(t *testing.T, h http.Handler, method, target, body string) (int, resp) {
	t.Helper()
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(method, target, rd))
	var r resp
	if err := json.Unmarshal(rec.Body.Bytes(), &r); err != nil {
		t.Fatalf("响应不是 JSON 信封: %v, body=%s", err, rec.Body.String())
	}
	return rec.Code, r
}

// assertOK 断言成功信封：HTTP 200 + code=0。
func assertOK(t *testing.T, status int, r resp) {
	t.Helper()
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200", status)
	}
	if r.Code != float64(0) {
		t.Fatalf("code = %v, want 0", r.Code)
	}
}

// assertErr 断言错误信封：稳定字符串错误码 + 非空中文提示。
func assertErr(t *testing.T, status, wantStatus int, r resp, wantCode string) {
	t.Helper()
	if status != wantStatus {
		t.Fatalf("status = %d, want %d (body=%+v)", status, wantStatus, r)
	}
	if r.Code != wantCode {
		t.Fatalf("code = %v (%T), want %q", r.Code, r.Code, wantCode)
	}
	if strings.TrimSpace(r.Message) == "" {
		t.Fatalf("错误提示为空，要求清晰可读: %+v", r)
	}
}

// ---- /healthz ----

func TestHealthz(t *testing.T) {
	h := NewRouter(Deps{}) // 健康检查不依赖数据库
	status, r := doReq(t, h, http.MethodGet, "/healthz", "")
	assertOK(t, status, r)
	var data struct {
		Status string `json:"status"`
		Time   string `json:"time"`
	}
	if err := json.Unmarshal(r.Data, &data); err != nil {
		t.Fatalf("data 解码失败: %v", err)
	}
	if data.Status != "ok" {
		t.Fatalf("status = %q, want ok", data.Status)
	}
	if _, err := time.Parse(time.RFC3339, data.Time); err != nil {
		t.Fatalf("time 不是 RFC3339: %q", data.Time)
	}
}

// ---- /api/v1/app/list ----

func TestAppList(t *testing.T) {
	h := NewRouter(Deps{Apps: &fakeApps{apps: []App{
		{Code: "crane", Name: "微信智能应答", Description: "群消息规则应答", Enabled: true},
		{Code: "netdisk", Name: "团队网盘", Enabled: false},
	}}})
	status, r := doReq(t, h, http.MethodGet, "/api/v1/app/list", "")
	assertOK(t, status, r)
	var data struct {
		Apps []App `json:"apps"`
	}
	if err := json.Unmarshal(r.Data, &data); err != nil {
		t.Fatalf("data 解码失败: %v", err)
	}
	if len(data.Apps) != 2 || data.Apps[0].Code != "crane" || data.Apps[1].Enabled {
		t.Fatalf("apps 不符: %+v", data.Apps)
	}
}

func TestAppList_NoCatalog(t *testing.T) {
	// 无数据库/依赖未接入：清晰的 dependency_unavailable 错误
	h := NewRouter(Deps{})
	status, r := doReq(t, h, http.MethodGet, "/api/v1/app/list", "")
	assertErr(t, status, http.StatusServiceUnavailable, r, CodeDependencyUnavailable)
}

func TestAppList_ImplError(t *testing.T) {
	// 实现返回未知错误：不泄漏底层信息，统一 internal_error
	h := NewRouter(Deps{Apps: &fakeApps{err: fmt.Errorf("dial tcp 10.0.0.9:3306: connect refused")}})
	status, r := doReq(t, h, http.MethodGet, "/api/v1/app/list", "")
	assertErr(t, status, http.StatusInternalServerError, r, CodeInternal)
	if strings.Contains(r.Message, "10.0.0.9") {
		t.Fatalf("响应泄漏底层错误信息: %q", r.Message)
	}
}

// ---- /api/v1/admin/config ----

func demoCfg() *fakeCfg {
	return &fakeCfg{items: map[string]ConfigItem{
		"app.secret":     {Key: "app.secret", Group: "微信智能应答", Value: "s3cr3t-token-abcdef", Sensitive: true},
		"app.base_url":   {Key: "app.base_url", Group: "基础配置", Value: "http://127.0.0.1:3000", Sensitive: false},
		"app.notify_key": {Key: "app.notify_key", Group: "微信智能应答", Value: "wx-notify-98765", Sensitive: true},
	}}
}

func TestConfigGet_MasksSensitive(t *testing.T) {
	h := NewRouter(Deps{Cfg: demoCfg()})
	status, r := doReq(t, h, http.MethodGet, "/api/v1/admin/config", "")
	assertOK(t, status, r)
	var data struct {
		Items []ConfigItem `json:"items"`
	}
	if err := json.Unmarshal(r.Data, &data); err != nil {
		t.Fatalf("data 解码失败: %v", err)
	}
	if len(data.Items) != 3 {
		t.Fatalf("items 数 = %d, want 3", len(data.Items))
	}
	body := string(r.Data)
	if strings.Contains(body, "s3cr3t-token-abcdef") || strings.Contains(body, "wx-notify-98765") {
		t.Fatalf("响应包含完整敏感配置: %s", body)
	}
	byKey := map[string]ConfigItem{}
	for _, it := range data.Items {
		byKey[it.Key] = it
	}
	if got := byKey["app.secret"]; got.Value != "s3****ef" || !got.Sensitive || got.Group != "微信智能应答" {
		t.Fatalf("敏感项脱敏不符: %+v", got)
	}
	if got := byKey["app.base_url"]; got.Value != "http://127.0.0.1:3000" || got.Sensitive {
		t.Fatalf("普通项被误脱敏: %+v", got)
	}
}

func TestConfigGet_ByKeyAndGroup(t *testing.T) {
	h := NewRouter(Deps{Cfg: demoCfg()})

	status, r := doReq(t, h, http.MethodGet, "/api/v1/admin/config?key=app.secret", "")
	assertOK(t, status, r)
	var one struct {
		Items []ConfigItem `json:"items"`
	}
	if err := json.Unmarshal(r.Data, &one); err != nil {
		t.Fatalf("data 解码失败: %v", err)
	}
	if len(one.Items) != 1 || one.Items[0].Key != "app.secret" || one.Items[0].Value != "s3****ef" {
		t.Fatalf("单键查询不符: %+v", one.Items)
	}

	status, r = doReq(t, h, http.MethodGet, "/api/v1/admin/config?group=%E5%BE%AE%E4%BF%A1%E6%99%BA%E8%83%BD%E5%BA%94%E7%AD%94", "")
	assertOK(t, status, r)
	var grp struct {
		Items []ConfigItem `json:"items"`
	}
	if err := json.Unmarshal(r.Data, &grp); err != nil {
		t.Fatalf("data 解码失败: %v", err)
	}
	if len(grp.Items) != 2 {
		t.Fatalf("分组过滤不符: %+v", grp.Items)
	}
}

func TestConfigGet_UnknownKey(t *testing.T) {
	h := NewRouter(Deps{Cfg: demoCfg()})
	status, r := doReq(t, h, http.MethodGet, "/api/v1/admin/config?key=nope", "")
	assertErr(t, status, http.StatusNotFound, r, CodeNotFound)
}

func TestConfigUpdate_MasksResponse(t *testing.T) {
	cfg := demoCfg()
	h := NewRouter(Deps{Cfg: cfg})
	body := `{"items":[{"key":"app.secret","value":"new-secret-value-42"}]}`
	status, r := doReq(t, h, http.MethodPut, "/api/v1/admin/config", body)
	assertOK(t, status, r)

	// 实现收到的是完整明文
	if len(cfg.updates) != 1 || cfg.updates[0].Value != "new-secret-value-42" {
		t.Fatalf("更新未收到完整明文: %+v", cfg.updates)
	}
	// 响应脱敏、不回显完整新值
	var data struct {
		Items []ConfigItem `json:"items"`
	}
	if err := json.Unmarshal(r.Data, &data); err != nil {
		t.Fatalf("data 解码失败: %v", err)
	}
	if len(data.Items) != 1 || data.Items[0].Value != "ne****42" || !data.Items[0].Sensitive {
		t.Fatalf("更新响应脱敏不符: %+v", data.Items)
	}
	if strings.Contains(string(r.Data), "new-secret-value-42") {
		t.Fatalf("更新响应回显完整敏感值: %s", r.Data)
	}
}

func TestConfigUpdate_Invalid(t *testing.T) {
	h := NewRouter(Deps{Cfg: demoCfg()})
	cases := []struct {
		name, body string
	}{
		{"非法 JSON", `{`},
		{"items 为空", `{"items":[]}`},
		{"键为空", `{"items":[{"key":"  ","value":"x"}]}`},
		{"未知字段", `{"items":[{"key":"app.secret","value":"x"}],"oops":1}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			status, r := doReq(t, h, http.MethodPut, "/api/v1/admin/config", c.body)
			assertErr(t, status, http.StatusBadRequest, r, CodeInvalidParam)
		})
	}
}

func TestConfigUpdate_UnknownKey(t *testing.T) {
	h := NewRouter(Deps{Cfg: demoCfg()})
	status, r := doReq(t, h, http.MethodPut, "/api/v1/admin/config", `{"items":[{"key":"nope","value":"x"}]}`)
	assertErr(t, status, http.StatusNotFound, r, CodeNotFound)
}

func TestConfig_NoAdmin(t *testing.T) {
	h := NewRouter(Deps{})
	status, r := doReq(t, h, http.MethodGet, "/api/v1/admin/config", "")
	assertErr(t, status, http.StatusServiceUnavailable, r, CodeDependencyUnavailable)
	status, r = doReq(t, h, http.MethodPut, "/api/v1/admin/config", `{"items":[{"key":"a","value":"b"}]}`)
	assertErr(t, status, http.StatusServiceUnavailable, r, CodeDependencyUnavailable)
}

func TestConfig_DbUnavailable(t *testing.T) {
	// 实现侧报依赖不可用（如数据库未配置）→ 503 dependency_unavailable
	h := NewRouter(Deps{Cfg: &fakeCfg{getErr: ErrDependencyUnavailable}})
	status, r := doReq(t, h, http.MethodGet, "/api/v1/admin/config", "")
	assertErr(t, status, http.StatusServiceUnavailable, r, CodeDependencyUnavailable)
}

// ---- /api/v1/app-logs ----

func TestAppLogs_FilterAndPagination(t *testing.T) {
	logs := &fakeLogs{page: LogPage{
		Items: []LogEntry{
			{ID: 11, AppCode: "crane", Level: "warn", Message: "规则命中失败", MessageCN: "规则命中失败", CreatedAt: time.Date(2026, 10, 27, 8, 0, 0, 0, time.UTC)},
			{ID: 12, AppCode: "crane", Level: "info", Message: "rule matched", CreatedAt: time.Date(2026, 10, 27, 9, 0, 0, 0, time.UTC)},
		},
		Total: 42,
	}}
	apps := &fakeApps{apps: []App{{Code: "crane", Name: "微信智能应答", Enabled: true}}}
	h := NewRouter(Deps{Apps: apps, Logs: logs})

	q := "?app_code=crane&level=WARN&trace_id=tr-1&keyword=%E5%91%BD%E4%B8%AD" +
		"&since=2026-10-27T00:00:00Z&until=2026-10-28T00:00:00Z&page=2&page_size=10"
	status, r := doReq(t, h, http.MethodGet, "/api/v1/app-logs"+q, "")
	assertOK(t, status, r)

	// 筛选条件透传（level 小写化、时间解析、分页）
	got := logs.got
	if got.AppCode != "crane" || got.Level != "warn" || got.TraceID != "tr-1" || got.Keyword != "命中" {
		t.Fatalf("筛选条件不符: %+v", got)
	}
	if !got.Since.Equal(time.Date(2026, 10, 27, 0, 0, 0, 0, time.UTC)) ||
		!got.Until.Equal(time.Date(2026, 10, 28, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("时间解析不符: %+v", got)
	}
	if got.Page != 2 || got.PageSize != 10 {
		t.Fatalf("分页不符: %+v", got)
	}

	var page LogPage
	if err := json.Unmarshal(r.Data, &page); err != nil {
		t.Fatalf("data 解码失败: %v", err)
	}
	if page.Total != 42 || page.Page != 2 || page.PageSize != 10 || len(page.Items) != 2 {
		t.Fatalf("分页响应不符: %+v", page)
	}
	if page.Items[0].MessageCN != "规则命中失败" || page.Items[1].Message != "rule matched" {
		t.Fatalf("条目不符: %+v", page.Items)
	}
}

func TestAppLogs_DefaultPaging(t *testing.T) {
	logs := &fakeLogs{page: LogPage{Total: 0}}
	h := NewRouter(Deps{Logs: logs})
	status, r := doReq(t, h, http.MethodGet, "/api/v1/app-logs", "")
	assertOK(t, status, r)
	if logs.got.Page != 1 || logs.got.PageSize != defaultPageSize {
		t.Fatalf("缺省分页不符: %+v", logs.got)
	}
	var page LogPage
	if err := json.Unmarshal(r.Data, &page); err != nil {
		t.Fatalf("data 解码失败: %v", err)
	}
	if page.Items == nil || len(page.Items) != 0 {
		t.Fatalf("无数据时 items 应为空数组: %s", r.Data)
	}
}

func TestAppLogs_InvalidParams(t *testing.T) {
	h := NewRouter(Deps{Logs: &fakeLogs{}})
	cases := []struct{ name, q string }{
		{"非法级别", "?level=bogus"},
		{"page 越界", "?page=0"},
		{"page 非数字", "?page=x"},
		{"page_size 超上限", "?page_size=101"},
		{"since 非法", "?since=yesterday"},
		{"until 早于 since", "?since=2026-10-28T00:00:00Z&until=2026-10-27T00:00:00Z"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			status, r := doReq(t, h, http.MethodGet, "/api/v1/app-logs"+c.q, "")
			assertErr(t, status, http.StatusBadRequest, r, CodeInvalidParam)
		})
	}
}

func TestAppLogs_UnknownApp(t *testing.T) {
	// 应用目录可用时校验 app_code 存在性
	h := NewRouter(Deps{
		Apps: &fakeApps{apps: []App{{Code: "crane", Name: "微信智能应答"}}},
		Logs: &fakeLogs{},
	})
	status, r := doReq(t, h, http.MethodGet, "/api/v1/app-logs?app_code=ghost", "")
	assertErr(t, status, http.StatusNotFound, r, CodeAppNotFound)
}

func TestAppLogs_SystemAndIntegrationChannels(t *testing.T) {
	for _, code := range []string{"system", "office", "chat-log", "sgcc-clockin"} {
		logs := &fakeLogs{page: LogPage{Total: 1}}
		h := NewRouter(Deps{Apps: &fakeApps{apps: []App{}}, Logs: logs})
		status, r := doReq(t, h, http.MethodGet, "/api/v1/app-logs?app_code="+code, "")
		assertOK(t, status, r)
		if logs.got.AppCode != code {
			t.Fatalf("公共日志渠道 %s 未透传: %+v", code, logs.got)
		}
	}
}

func TestAppLogs_AppCheckSkippedWithoutCatalog(t *testing.T) {
	// 目录未接入时不校验应用存在性，交由 LogReader 处理
	logs := &fakeLogs{page: LogPage{Total: 1}}
	h := NewRouter(Deps{Logs: logs})
	status, r := doReq(t, h, http.MethodGet, "/api/v1/app-logs?app_code=ghost", "")
	assertOK(t, status, r)
	if logs.got.AppCode != "ghost" {
		t.Fatalf("app_code 未透传: %+v", logs.got)
	}
}

func TestAppLogs_NoReader(t *testing.T) {
	h := NewRouter(Deps{})
	status, r := doReq(t, h, http.MethodGet, "/api/v1/app-logs", "")
	assertErr(t, status, http.StatusServiceUnavailable, r, CodeDependencyUnavailable)
}

func TestAppLogs_DbUnavailable(t *testing.T) {
	// 无数据库时实现返回 ErrDependencyUnavailable → 清晰 503
	h := NewRouter(Deps{Logs: &fakeLogs{err: ErrDependencyUnavailable}})
	status, r := doReq(t, h, http.MethodGet, "/api/v1/app-logs", "")
	assertErr(t, status, http.StatusServiceUnavailable, r, CodeDependencyUnavailable)
}

func TestAppLogs_ReaderNotFound(t *testing.T) {
	h := NewRouter(Deps{Logs: &fakeLogs{err: errors.Join(errors.New("wrap"), NotFound("应用不存在: x"))}})
	status, r := doReq(t, h, http.MethodGet, "/api/v1/app-logs", "")
	assertErr(t, status, http.StatusNotFound, r, CodeNotFound)
}

// ---- panic 兜底 ----

func TestRecoverer(t *testing.T) {
	h := NewRouter(Deps{Apps: panicApps{}})
	status, r := doReq(t, h, http.MethodGet, "/api/v1/app/list", "")
	assertErr(t, status, http.StatusInternalServerError, r, CodeInternal)
	if strings.Contains(string(r.Data), "boom") {
		t.Fatalf("panic 信息写入了响应: %s", r.Data)
	}
}
