package rpc

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/zhangzhenyu91/j1toolkit/uvmp-toolkit/core-go/internal/apps"
	"github.com/zhangzhenyu91/j1toolkit/uvmp-toolkit/core-go/internal/config"
	"github.com/zhangzhenyu91/j1toolkit/uvmp-toolkit/core-go/internal/job"
	"github.com/zhangzhenyu91/j1toolkit/uvmp-toolkit/core-go/internal/state"
)

// syncBuffer 线程安全输出缓冲。
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// newTestServer 组装隔离环境的服务端（临时状态根 + 临时配置 + 空应用注册表）。
func newTestServer(t *testing.T, cfg config.Dict) (*Server, *syncBuffer) {
	t.Helper()
	state.SetRoot(t.TempDir())
	t.Setenv("UVMP_TOOLKIT_CONFIG", filepath.Join(t.TempDir(), "config.json"))
	t.Setenv("UVMP_TOOLKIT_CONFIG_INI", filepath.Join(t.TempDir(), "no.ini"))
	apps.Reset()
	buf := &syncBuffer{}
	s := &Server{
		Manager:  job.NewManager(&state.Store{Root: t.TempDir()}),
		out:      buf,
		LoadConf: func() config.Dict { return cfg },
		Now:      func() time.Time { return time.Date(2026, 10, 28, 10, 0, 0, 0, time.Local) },
	}
	return s, buf
}

// parseLines 把输出解析为 JSON 对象列表。
func parseLines(t *testing.T, out string) []map[string]any {
	t.Helper()
	objs := []map[string]any{}
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("输出不是合法 JSON: %q (%v)", line, err)
		}
		objs = append(objs, m)
	}
	return objs
}

func req(id any, method string, params map[string]any) string {
	b, _ := json.Marshal(map[string]any{"id": id, "method": method, "params": params})
	return string(b) + "\n"
}

func TestPingAndUnknownMethod(t *testing.T) {
	s, buf := newTestServer(t, config.Defaults())
	s.Serve(strings.NewReader(req(1, "ping", nil) + req(2, "noSuch", nil)))
	objs := parseLines(t, buf.String())
	if len(objs) != 2 {
		t.Fatalf("输出 %d 条: %v", len(objs), objs)
	}
	if objs[0]["ok"] != true || objs[0]["id"] != float64(1) {
		t.Fatalf("ping 响应 = %v", objs[0])
	}
	if objs[1]["ok"] != false || !strings.Contains(objs[1]["error"].(string), "未知方法") {
		t.Fatalf("未知方法响应 = %v", objs[1])
	}
}

func TestResponseASCIIEscaped(t *testing.T) {
	s, buf := newTestServer(t, config.Defaults())
	apps.Register(&apps.App{ID: "fake", Name: "假应用", Backend: &fakeBackend{}})
	s.Serve(strings.NewReader(req(1, "getApps", nil)))
	out := buf.String()
	// 中文必须 \uXXXX 转义（ensure_ascii 同口径）
	if strings.Contains(out, "题库") {
		t.Fatalf("非 ASCII 未转义: %s", out)
	}
	if !strings.Contains(out, "\\u") {
		t.Fatalf("缺少 \\uXXXX 转义: %s", out)
	}
	// 可被 JSON 还原
	objs := parseLines(t, out)
	_ = objs
}

func TestGetConfigSaveConfig(t *testing.T) {
	cfg := config.Defaults()
	s, _ := newTestServer(t, cfg)
	res, err := s.mGetConfig(nil)
	if err != nil {
		t.Fatal(err)
	}
	view := res.(config.Dict)
	sso := config.MapField(view, "sso")
	if sso["has_password"] != false {
		t.Fatalf("has_password = %v", sso["has_password"])
	}
	// 带密码保存
	if _, err := s.mSaveConfig(map[string]any{
		"sso": map[string]any{"username": "u1", "password": "p1"},
	}); err != nil {
		t.Fatal(err)
	}
	saved := state.ReadJSONMap(os.Getenv("UVMP_TOOLKIT_CONFIG"))
	if config.StrField(config.MapField(saved, "sso"), "password") != "p1" {
		t.Fatalf("saved = %v", saved)
	}
	// 密码留空 = 不修改
	if _, err := s.mSaveConfig(map[string]any{
		"sso": map[string]any{"username": "u2", "password": ""},
	}); err != nil {
		t.Fatal(err)
	}
	saved = state.ReadJSONMap(os.Getenv("UVMP_TOOLKIT_CONFIG"))
	ssoSaved := config.MapField(saved, "sso")
	if config.StrField(ssoSaved, "username") != "u2" || config.StrField(ssoSaved, "password") != "p1" {
		t.Fatalf("留空密码应保留原值: %v", ssoSaved)
	}
}

// fakeBackend 测试应用后端。
type fakeBackend struct {
	runs int
}

func (f *fakeBackend) Run(j *job.Job, params map[string]any) (map[string]any, error) {
	f.runs++
	j.Log("测试日志·中文")
	j.SetProgress(1, 2, "步骤一")
	if params != nil && params["fail"] == true {
		return nil, fmt.Errorf("业务失败")
	}
	return map[string]any{"ok": true}, nil
}

func (f *fakeBackend) Status(cfg map[string]any) (map[string]any, error) {
	return map[string]any{"fake": true}, nil
}

func TestRunJobEvents(t *testing.T) {
	s, buf := newTestServer(t, config.Defaults())
	fb := &fakeBackend{}
	apps.Register(&apps.App{ID: "fake", Name: "假应用", Backend: fb})

	s.Serve(strings.NewReader(req(1, "runJob", map[string]any{
		"app_id": "fake", "params": map[string]any{},
	})))
	objs := parseLines(t, buf.String())
	kinds := []string{}
	for _, o := range objs {
		if ev, ok := o["event"].(string); ok {
			kinds = append(kinds, ev)
		}
	}
	joined := strings.Join(kinds, ",")
	if !strings.Contains(joined, "job.log") || !strings.Contains(joined, "job.progress") || !strings.Contains(joined, "job.done") {
		t.Fatalf("事件不全: %v", kinds)
	}
	// job.done 携带 job 摘要（success）
	var done map[string]any
	for _, o := range objs {
		if o["event"] == "job.done" {
			done = o
		}
	}
	summary := done["job"].(map[string]any)
	if summary["status"] != "success" {
		t.Fatalf("job 摘要 = %v", summary)
	}
	// 响应（id=1）存在且 ok（与事件先后顺序不作断言：任务 goroutine 可能先发事件）
	var resp map[string]any
	for _, o := range objs {
		if o["id"] == float64(1) {
			resp = o
		}
	}
	if resp == nil || resp["ok"] != true {
		t.Fatalf("缺 runJob 响应: %v", objs)
	}
}

func TestRunJobSingleActive(t *testing.T) {
	s, _ := newTestServer(t, config.Defaults())
	block := make(chan struct{})
	finished := make(chan struct{})
	apps.Register(&apps.App{ID: "slow", Name: "慢应用", Backend: slowBackend{block}})
	j := s.Manager.Create("slow", "慢应用")
	j.OnDone = func(*job.Job) { close(finished) }
	s.Manager.Start(j, func(*job.Job) (map[string]any, error) {
		<-block
		return nil, nil
	})
	_, err := s.mRunJob(map[string]any{"app_id": "slow"})
	if err == nil || !strings.Contains(err.Error(), "已有任务在运行") {
		t.Fatalf("err = %v", err)
	}
	close(block)
	<-finished // 等后台任务收尾（档案/日志落盘），避免 TempDir 清理竞态
}

type slowBackend struct{ block chan struct{} }

func (s slowBackend) Run(j *job.Job, _ map[string]any) (map[string]any, error) {
	<-s.block
	return nil, nil
}

func TestCancelAndJobLog(t *testing.T) {
	s, buf := newTestServer(t, config.Defaults())
	block := make(chan struct{})
	apps.Register(&apps.App{ID: "slow2", Name: "慢应用", Backend: slowBackend{block}})
	j := s.Manager.Create("slow2", "慢应用")
	s.wireEvents(j)
	finished := make(chan struct{})
	baseDone := j.OnDone
	j.OnDone = func(jj *job.Job) {
		if baseDone != nil {
			baseDone(jj)
		}
		close(finished)
	}
	s.Manager.Start(j, func(j2 *job.Job) (map[string]any, error) {
		<-block
		return nil, j2.CheckCancel()
	})
	// cancelJob
	res, err := s.mCancelJob(map[string]any{"job_id": j.ID})
	if err != nil || res.(map[string]any)["ok"] != true {
		t.Fatalf("cancel = %v/%v", res, err)
	}
	close(block)
	<-finished
	// 任务不存在
	if _, err := s.mCancelJob(map[string]any{"job_id": "20261028000000-99"}); err == nil {
		t.Fatal("不存在任务应报错")
	}
	// getJobLog：参数校验 + 内容
	state.WriteJSON(filepath.Join(state.LogsDir(), "20261028101500-01.json"), map[string]any{})
	if err := os.WriteFile(filepath.Join(state.LogsDir(), "20261028101500-01.log"), []byte("hello 日志"), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err = s.mGetJobLog(map[string]any{"job_id": "20261028101500-01"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.(map[string]any)["text"].(string), "hello 日志") {
		t.Fatalf("text = %v", res)
	}
	if _, err := s.mGetJobLog(map[string]any{"job_id": "../evil"}); err == nil {
		t.Fatal("非法 job_id 应报错")
	}
	if _, err := s.mGetJobLog(map[string]any{"job_id": "20261028101500-99"}); err == nil {
		t.Fatal("缺失日志应报错")
	}
	_ = buf
}

func TestHistoryAndLogStats(t *testing.T) {
	s, _ := newTestServer(t, config.Defaults())
	state.AppendHistory("fake", map[string]any{"job_id": "20261028101500-01", "status": "success"})
	res, err := s.mGetHistory(map[string]any{"app_id": "fake"})
	if err != nil {
		t.Fatal(err)
	}
	if runs := res.([]map[string]any); len(runs) != 1 {
		t.Fatalf("history = %v", runs)
	}
	// deleteHistory 单条
	if _, err := s.mDeleteHistory(map[string]any{"app_id": "fake", "job_id": "20261028101500-01"}); err != nil {
		t.Fatal(err)
	}
	res, _ = s.mGetHistory(map[string]any{"app_id": "fake"})
	if len(res.([]map[string]any)) != 0 {
		t.Fatalf("删除后 = %v", res)
	}
	if _, err := s.mDeleteHistory(map[string]any{"app_id": "fake", "job_id": "bad id"}); err == nil {
		t.Fatal("非法 job_id 应报错")
	}
	// clearLogs / getLogStats
	if err := os.WriteFile(filepath.Join(state.LogsDir(), "x.log"), []byte("abc"), 0o644); err != nil {
		t.Fatal(err)
	}
	res, _ = s.mGetLogStats(nil)
	stats := res.(map[string]any)
	if stats["files"].(int) < 1 || stats["bytes"].(int64) < 3 {
		t.Fatalf("stats = %v", stats)
	}
	res, _ = s.mClearLogs(nil)
	if res.(map[string]any)["removed"].(int) < 1 {
		t.Fatalf("clearLogs = %v", res)
	}
}

func TestGetAppsCapabilities(t *testing.T) {
	s, _ := newTestServer(t, config.Defaults())
	apps.Register(&apps.App{ID: "fake", Name: "假应用", Backend: &fakeBackend{}})
	res, _ := s.mGetApps(nil)
	list := res.([]map[string]any)
	found := false
	for _, a := range list {
		if a["id"] == "fake" {
			found = true
			if a["has_status"] != true || a["has_preview"] != false {
				t.Fatalf("能力标记 = %v", a)
			}
		}
	}
	if !found {
		t.Fatal("fake 应用缺失")
	}
	// getAppStatus
	res, _ = s.mGetAppStatus(map[string]any{"app_id": "fake"})
	if res.(map[string]any)["fake"] != true {
		t.Fatalf("status = %v", res)
	}
	// getAppStatus 不支持 → 空对象
	apps.Register(&apps.App{ID: "bare", Name: "裸应用", Backend: &fakeBackendRunsOnly{}})
	res, _ = s.mGetAppStatus(map[string]any{"app_id": "bare"})
	if len(res.(map[string]any)) != 0 {
		t.Fatalf("应为空对象: %v", res)
	}
	// preview 不支持 → 报错
	if _, err := s.mPreview(map[string]any{"app_id": "bare"}); err == nil {
		t.Fatal("不支持预览应报错")
	}
	// launchAppBrowser 不支持 → 报错
	if _, err := s.mLaunchAppBrowser(map[string]any{"app_id": "bare"}); err == nil {
		t.Fatal("不支持受控浏览器应报错")
	}
}

type fakeBackendRunsOnly struct{}

func (f *fakeBackendRunsOnly) Run(j *job.Job, _ map[string]any) (map[string]any, error) {
	return nil, nil
}

func TestSelftestUnknownKind(t *testing.T) {
	s, _ := newTestServer(t, config.Defaults())
	if _, err := s.mRunJob(map[string]any{"app_id": "selftest:nope"}); err == nil {
		t.Fatal("未知自检类型应报错")
	}
}

func TestMaybeRunTreeSyncTickDriverGate(t *testing.T) {
	cfg := config.Defaults()
	s, _ := newTestServer(t, cfg)
	res, _ := s.mMaybeRunTreeSyncTick(nil)
	if res.(map[string]any)["reason"] != "由系统定时器调度" {
		t.Fatalf("res = %v", res)
	}
	// driver=app 但未配置 local_path
	cfg["schedule"] = config.Dict{"enabled": true, "time": "09:15", "driver": "app"}
	res, _ = s.mMaybeRunTreeSyncTick(nil)
	if !strings.Contains(res.(map[string]any)["reason"].(string), "tree_sync.local_path") {
		t.Fatalf("res = %v", res)
	}
}

func TestMaybeRunDailyNotDue(t *testing.T) {
	cfg := config.Defaults() // driver=os → 驻留调度门闸
	s, _ := newTestServer(t, cfg)
	res, _ := s.mMaybeRunDaily(nil)
	r := res.(map[string]any)
	if r["started"] != false {
		t.Fatalf("res = %v", r)
	}
	if r["reason"] != "由系统定时器调度" {
		t.Fatalf("reason = %v", r["reason"])
	}
	// 携带夜间兜底字段（未配置 → started false）
	if _, ok := r["tree_sync_nightly"]; !ok {
		t.Fatalf("缺 tree_sync_nightly: %v", r)
	}
	if _, ok := r["crossing_sync_nightly"]; !ok {
		t.Fatalf("缺 crossing_sync_nightly: %v", r)
	}
}

func TestMaybeRunDailyRunsJob(t *testing.T) {
	cfg := config.Defaults()
	cfg["schedule"] = config.Dict{"enabled": true, "time": "09:15", "driver": "app"}
	s, buf := newTestServer(t, cfg)
	apps.Register(&apps.App{ID: "daily_sync", Name: "派车同步", Backend: &fakeBackend{}})
	res, _ := s.mMaybeRunDaily(nil)
	r := res.(map[string]any)
	if r["started"] != true || r["job_id"] == nil {
		t.Fatalf("res = %v", r)
	}
	// 尝试戳已写（分钟级 tick 失败重试间隔闸）
	if state.GetStamp("daily_export_attempt") == nil {
		t.Fatal("应写尝试戳")
	}
	// 等后台任务收尾（事件推完、档案落盘），避免 TempDir 清理竞态
	jobID := r["job_id"].(string)
	deadline := time.Now().Add(5 * time.Second)
	for {
		jj := s.Manager.Get(jobID)
		if jj != nil && (jj.Status == job.StatusSuccess || jj.Status == job.StatusFailed || jj.Status == job.StatusCancelled) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("后台任务超时")
		}
		time.Sleep(10 * time.Millisecond)
	}
	_ = buf
}

func TestGetSystemInfoKeys(t *testing.T) {
	s, _ := newTestServer(t, config.Defaults())
	res, _ := s.mGetSystemInfo(nil)
	info := res.(map[string]any)
	for _, k := range []string{"platform", "machine", "core", "chrome", "state_dir", "config_path", "suggested_outdirs"} {
		if _, ok := info[k]; !ok {
			t.Fatalf("缺字段 %s: %v", k, info)
		}
	}
}

func TestOpenPathMissing(t *testing.T) {
	s, _ := newTestServer(t, config.Defaults())
	if _, err := s.mOpenPath(map[string]any{"path": filepath.Join(t.TempDir(), "不存在")}); err == nil {
		t.Fatal("路径不存在应报错")
	}
}

func TestMarshalASCII(t *testing.T) {
	b, err := marshalASCII(map[string]any{"t": "派车单"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "派") {
		t.Fatalf("未转义: %s", b)
	}
	if !strings.Contains(string(b), "\\u6D3E") {
		t.Fatalf("缺 \\u 转义: %s", b)
	}
	// 代理对（𠮷 = U+20BB7）
	b2, _ := marshalASCII(map[string]any{"t": "\U00020BB7"})
	if !strings.Contains(string(b2), "\\uD842\\uDFB7") {
		t.Fatalf("代理对转义错误: %s", b2)
	}
}
