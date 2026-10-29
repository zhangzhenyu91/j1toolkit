package ui

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/zhangzhenyu91/j1toolkit/uvmp-toolkit/core-go/internal/apps"
	"github.com/zhangzhenyu91/j1toolkit/uvmp-toolkit/core-go/internal/job"
	"github.com/zhangzhenyu91/j1toolkit/uvmp-toolkit/core-go/internal/state"
)

// 生产 Backend 测试：真实走 job/state/config/quizsearch 本地文件，
// 业务应用以可控 fake 注入（无网络、无外部进程）。

type testAppBackend struct {
	mu    sync.Mutex
	runs  int
	block chan struct{} // 非 nil 时 Run 阻塞至关闭
	fail  error
}

func (a *testAppBackend) Run(j *job.Job, _ map[string]any) (map[string]any, error) {
	a.mu.Lock()
	a.runs++
	block, fail := a.block, a.fail
	a.mu.Unlock()
	j.Log("测试日志")
	j.SetProgress(1, 2, "步骤一")
	if block != nil {
		<-block
	}
	if fail != nil {
		return nil, fail
	}
	return map[string]any{"ok": true, "dir": "mem://out"}, nil
}

// setupCore 隔离配置/状态目录并注册可控测试应用。
// UserConfigDir 指向临时目录：题库 DataDir 解析（ResolveDataDir）不触达真实用户目录。
func setupCore(t *testing.T) (*CoreBackend, *testAppBackend) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("UVMP_TOOLKIT_CONFIG", filepath.Join(dir, "config.json"))
	t.Setenv("UVMP_TOOLKIT_CONFIG_INI", filepath.Join(dir, "none.ini"))
	t.Setenv("APPDATA", dir)
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("HOME", dir)
	state.SetRoot(filepath.Join(dir, "state"))
	t.Cleanup(func() { state.SetRoot("") })
	ta := &testAppBackend{}
	apps.Register(&apps.App{ID: "test_app", Name: "测试应用", Order: 90, Backend: ta})
	b := NewCoreBackend()
	t.Cleanup(b.Dispose)
	if b.quiz == nil {
		t.Fatal("题库目录未初始化")
	}
	return b, ta
}

func waitEvent(t *testing.T, sub Subscription, kind EventKind, timeout time.Duration) Event {
	t.Helper()
	deadline := time.After(timeout)
	for {
		select {
		case ev, ok := <-sub.Events():
			if !ok {
				t.Fatalf("事件流关闭，未等到 %s", kind)
			}
			if ev.Kind == kind {
				return ev
			}
		case <-deadline:
			t.Fatalf("等待事件 %s 超时", kind)
		}
	}
}

func TestCoreBackendConfigRoundTrip(t *testing.T) {
	b, _ := setupCore(t)
	ctx := context.Background()

	cfg, err := b.GetConfig(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.USB.Label != "GLKVM" || !cfg.Render.Builtin || cfg.SSO.HasPassword {
		t.Fatalf("默认配置 = %+v", cfg)
	}

	if err := b.SaveConfig(ctx, ConfigPatch{SSO: &SSOConfig{Username: "u1", Password: "p1"}}); err != nil {
		t.Fatal(err)
	}
	cfg, _ = b.GetConfig(ctx)
	if cfg.SSO.Username != "u1" || cfg.SSO.Password != "" || !cfg.SSO.HasPassword || cfg.SSO.PasswordSource != "config.json" {
		t.Fatalf("保存后配置 = %+v", cfg.SSO)
	}

	// 空密码 = 不修改
	if err := b.SaveConfig(ctx, ConfigPatch{SSO: &SSOConfig{Username: "u2"}}); err != nil {
		t.Fatal(err)
	}
	cfg, _ = b.GetConfig(ctx)
	if cfg.SSO.Username != "u2" || !cfg.SSO.HasPassword {
		t.Fatalf("空密码应保留原密码状态：%+v", cfg.SSO)
	}
}

func TestCoreBackendRunJobEventsAndHistory(t *testing.T) {
	b, ta := setupCore(t)
	ctx := context.Background()
	sub, err := b.Subscribe(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()

	id, err := b.RunJob(ctx, "test_app", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	if id == "" {
		t.Fatal("job_id 为空")
	}
	done := waitEvent(t, sub, EventJobDone, 5*time.Second)
	if done.Job == nil || done.Job.ID != id || done.Job.Status != JobSuccess {
		t.Fatalf("job.done = %+v", done.Job)
	}
	if done.Job.Result["ok"] != true {
		t.Fatalf("result = %+v", done.Job.Result)
	}

	j, err := b.GetJob(ctx, id)
	if err != nil || j.Status != JobSuccess || j.AppID != "test_app" {
		t.Fatalf("GetJob = %+v err=%v", j, err)
	}
	logText, err := b.JobLog(ctx, id)
	if err != nil || logText == "" {
		t.Fatalf("JobLog = %q err=%v", logText, err)
	}

	his, err := b.History(ctx, "test_app")
	if err != nil || len(his) != 1 || his[0].JobID != id || his[0].Dir != "mem://out" {
		t.Fatalf("History = %+v err=%v", his, err)
	}
	if ta.runs != 1 {
		t.Fatalf("runs = %d", ta.runs)
	}

	// 预览：无 PreviewBackend 的应用返回空 map（不报错）
	if m, err := b.Preview(ctx, "test_app", nil); err != nil || len(m) != 0 {
		t.Fatalf("Preview = %+v err=%v", m, err)
	}

	del, err := b.DeleteHistory(ctx, "test_app", id)
	if err != nil || del.Records != 1 {
		t.Fatalf("DeleteHistory = %+v err=%v", del, err)
	}
	if his, _ := b.History(ctx, "test_app"); len(his) != 0 {
		t.Fatalf("删除后仍有记录：%+v", his)
	}
	if _, err := b.JobLog(ctx, id); err == nil {
		t.Fatal("删除后日志应不可读")
	}

	clr, err := b.ClearLogs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	_ = clr
	st, err := b.LogStats(ctx)
	if err != nil || st.Dir == "" {
		t.Fatalf("LogStats = %+v err=%v", st, err)
	}
}

func TestCoreBackendRunJobBusyAndCancel(t *testing.T) {
	b, ta := setupCore(t)
	ctx := context.Background()
	ta.block = make(chan struct{})
	sub, _ := b.Subscribe(ctx)
	defer sub.Close()

	id, err := b.RunJob(ctx, "test_app", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.RunJob(ctx, "test_app", nil); err == nil {
		t.Fatal("并发启动同一应用应报错")
	}
	if err := b.CancelJob(ctx, id); err != nil {
		t.Fatal(err)
	}
	close(ta.block)

	// 等 job.done（finalize 落盘之后）再断言，避免与任务收尾写文件竞态
	done := waitEvent(t, sub, EventJobDone, 5*time.Second)
	if done.Job == nil || done.Job.ID != id {
		t.Fatalf("job.done = %+v", done.Job)
	}
	if done.Job.Status != JobCancelled {
		t.Fatalf("状态 = %s", done.Job.Status)
	}
}

func TestCoreBackendRunJobFail(t *testing.T) {
	b, ta := setupCore(t)
	ctx := context.Background()
	ta.fail = errors.New("业务失败样例")
	sub, _ := b.Subscribe(ctx)
	defer sub.Close()
	id, err := b.RunJob(ctx, "test_app", nil)
	if err != nil {
		t.Fatal(err)
	}
	done := waitEvent(t, sub, EventJobDone, 5*time.Second)
	if done.Job.Status != JobFailed || done.Job.Error == "" {
		t.Fatalf("job.done = %+v", done.Job)
	}
	if done.Job.ID != id {
		t.Fatalf("job_id = %s want %s", done.Job.ID, id)
	}
}

func TestCoreBackendListAppsAndStatus(t *testing.T) {
	b, _ := setupCore(t)
	ctx := context.Background()
	list, err := b.ListApps(ctx)
	if err != nil || len(list) == 0 {
		t.Fatalf("ListApps = %+v err=%v", list, err)
	}
	found := false
	for _, a := range list {
		if a.ID == "test_app" {
			found = true
		}
	}
	if !found {
		t.Fatal("测试应用未登记")
	}
	st, err := b.AppStatus(ctx, "test_app")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := st["schedule"]; !ok {
		t.Fatalf("状态缺 schedule 段：%+v", st)
	}
	if _, ok := st["usb"]; !ok {
		t.Fatalf("状态缺 usb 段：%+v", st)
	}
}

func TestCoreBackendQuizFlow(t *testing.T) {
	b, _ := setupCore(t)
	ctx := context.Background()
	dir := t.TempDir()

	tpl := filepath.Join(dir, "tpl.xlsx")
	if err := b.QuizSaveTemplate(ctx, tpl); err != nil {
		t.Fatal(err)
	}
	if st, err := os.Stat(tpl); err != nil || st.Size() == 0 {
		t.Fatalf("模板未写出: %v", err)
	}

	bank, err := b.QuizImportBank(ctx, tpl)
	if err != nil {
		t.Fatal(err)
	}
	if bank.Count != 3 || !bank.Enabled || bank.Name != "tpl" {
		t.Fatalf("导入 = %+v", bank)
	}
	banks, err := b.QuizBanks(ctx)
	if err != nil || len(banks) != 1 || banks[0].ID != bank.ID {
		t.Fatalf("题库列表 = %+v err=%v", banks, err)
	}

	m, err := b.QuizMatch(ctx, "我国安全生产工作的方针是")
	if err != nil {
		t.Fatal(err)
	}
	if !m.Matched || m.Question.Content == "" || m.Question.Options["A"] == "" {
		t.Fatalf("匹配 = %+v", m)
	}

	if err := b.QuizSetBankEnabled(ctx, bank.ID, false); err != nil {
		t.Fatal(err)
	}
	if m, _ := b.QuizMatch(ctx, "我国安全生产工作的方针是"); m.Matched {
		t.Fatalf("停用库不应参与匹配：%+v", m)
	}
	if err := b.QuizSetBankEnabled(ctx, bank.ID, true); err != nil {
		t.Fatal(err)
	}
	if err := b.QuizRenameBank(ctx, bank.ID, "安规题库"); err != nil {
		t.Fatal(err)
	}
	if banks, _ := b.QuizBanks(ctx); banks[0].Name != "安规题库" {
		t.Fatalf("重命名失败：%+v", banks)
	}
	if err := b.QuizRemoveBank(ctx, bank.ID); err != nil {
		t.Fatal(err)
	}
	if banks, _ := b.QuizBanks(ctx); len(banks) != 0 {
		t.Fatalf("删除后仍有题库：%+v", banks)
	}
}

func TestCoreBackendSelftestSSOMock(t *testing.T) {
	b, _ := setupCore(t)
	ctx := context.Background()
	if err := b.SaveConfig(ctx, ConfigPatch{Dev: &DevConfig{Mock: true}}); err != nil {
		t.Fatal(err)
	}
	sub, _ := b.Subscribe(ctx)
	defer sub.Close()
	id, err := b.RunJob(ctx, "selftest:sso", nil)
	if err != nil {
		t.Fatal(err)
	}
	done := waitEvent(t, sub, EventJobDone, 10*time.Second)
	if done.Job == nil || done.Job.Status != JobSuccess || done.Job.ID != id {
		t.Fatalf("自检任务 = %+v", done.Job)
	}
	if done.Job.Result["mock"] != true || done.Job.Result["ok"] != true {
		t.Fatalf("自检结果 = %+v", done.Job.Result)
	}
}

func TestCoreBackendCourseCheckpoints(t *testing.T) {
	b, _ := setupCore(t)
	dir := filepath.Join(state.StateDir(), "course-autoplay")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	data := `{
  "train-001|2026 春检培训班": {
    "version": 1,
    "active": true,
    "resume_page": 3,
    "processed": 8,
    "last_errors": {"r1": "x"},
    "skipped_ids": ["r2", "r3"],
    "updated_at": "2026-10-28 08:30:00"
  }
}`
	if err := os.WriteFile(filepath.Join(dir, "checkpoints.json"), []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
	cks, err := b.CourseCheckpoints(context.Background())
	if err != nil || len(cks) != 1 {
		t.Fatalf("检查点 = %+v err=%v", cks, err)
	}
	c := cks[0]
	if c.Page != 3 || c.Processed != 8 || c.Failed != 1 || c.Skipped != 2 ||
		c.ListName != "train-001|2026 春检培训班" || c.UpdatedAt != "2026-10-28 08:30:00" {
		t.Fatalf("检查点映射 = %+v", c)
	}
}

func TestCoreBackendScheduleGates(t *testing.T) {
	b, _ := setupCore(t)
	ctx := context.Background()
	// driver=os 时 U 盘轮询秒退不启动任务
	if err := b.SaveConfig(ctx, ConfigPatch{Schedule: &ScheduleConfig{Enabled: true, Time: "09:15", Driver: "os"}}); err != nil {
		t.Fatal(err)
	}
	r, err := b.MaybeRunTreeSyncTick(ctx)
	if err != nil || r.Started {
		t.Fatalf("tree tick = %+v err=%v", r, err)
	}
	// driver=app 但未配内网表路径：同样不启动
	if err := b.SaveConfig(ctx, ConfigPatch{Schedule: &ScheduleConfig{Enabled: true, Time: "09:15", Driver: "app"}}); err != nil {
		t.Fatal(err)
	}
	r, err = b.MaybeRunCrossingSyncTick(ctx)
	if err != nil || r.Started {
		t.Fatalf("crossing tick = %+v err=%v", r, err)
	}
	// 未到点：不启动但有原因
	d, err := b.MaybeRunDaily(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if d.Started {
		t.Fatalf("不该到点启动：%+v", d)
	}
	if d.Reason == "" {
		t.Fatalf("未到点应给出原因：%+v", d)
	}
}

func TestCoreBackendSystemInfoAndPathErrors(t *testing.T) {
	b, _ := setupCore(t)
	ctx := context.Background()
	info, err := b.SystemInfo(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if info.Platform == "" || info.ConfigPath == "" || info.StateDir == "" {
		t.Fatalf("SystemInfo = %+v", info)
	}
	if err := b.OpenPath(ctx, filepath.Join(t.TempDir(), "not-exist")); err == nil {
		t.Fatal("不存在路径应报错")
	}
	if err := b.Ping(ctx); err != nil {
		t.Fatal(err)
	}
	// 应用不存在/不支持受控浏览器
	if err := b.LaunchAppBrowser(ctx, "no_such_app"); err == nil {
		t.Fatal("不存在应用应报错")
	}
	if err := b.LaunchAppBrowser(ctx, "test_app"); err == nil {
		t.Fatal("无 BrowserBackend 应报错")
	}
}
