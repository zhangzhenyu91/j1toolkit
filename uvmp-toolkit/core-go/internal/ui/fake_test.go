package ui

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// FakeBackend 全接口冒烟：无网络、无外部进程，覆盖配置/状态/任务/历史/题库/课程/调度/事件。

func TestFakeSmoke_SystemConfig(t *testing.T) {
	ctx := context.Background()
	f := NewFakeBackend()
	if err := f.Ping(ctx); err != nil {
		t.Fatalf("Ping: %v", err)
	}
	info, err := f.SystemInfo(ctx)
	if err != nil || info.StateDir == "" {
		t.Fatalf("SystemInfo: %+v %v", info, err)
	}

	cfg, err := f.GetConfig(ctx)
	if err != nil {
		t.Fatalf("GetConfig: %v", err)
	}
	if cfg.Schedule.Time != "09:15" || cfg.Schedule.Driver != "os" {
		t.Fatalf("默认调度配置不符: %+v", cfg.Schedule)
	}
	if cfg.OrderExport.QueryWorkers != 4 || cfg.OrderExport.RenderWorkers != 2 {
		t.Fatalf("默认并发配置不符: %+v", cfg.OrderExport)
	}

	err = f.SaveConfig(ctx, ConfigPatch{SSO: &SSOConfig{Username: "zhang", Password: "s3cret"}})
	if err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}
	cfg, _ = f.GetConfig(ctx)
	if cfg.SSO.Username != "zhang" || !cfg.SSO.HasPassword {
		t.Fatalf("SSO 保存后回读不符: %+v", cfg.SSO)
	}
	if cfg.SSO.Password != "" {
		t.Fatalf("GetConfig 不应回读密码，实际: %q", cfg.SSO.Password)
	}

	// 空密码 = 不修改
	err = f.SaveConfig(ctx, ConfigPatch{SSO: &SSOConfig{Username: "zhang2", Password: ""}})
	if err != nil {
		t.Fatalf("SaveConfig(空密码): %v", err)
	}
	cfg, _ = f.GetConfig(ctx)
	if cfg.SSO.Username != "zhang2" || !cfg.SSO.HasPassword {
		t.Fatalf("空密码不应清除已配置密码: %+v", cfg.SSO)
	}

	// Patch 序列化：空密码剥除 password 键
	p := ConfigPatch{SSO: &SSOConfig{Username: "u", Password: ""}}.Map()
	sso, _ := p["sso"].(map[string]any)
	if _, ok := sso["password"]; ok {
		t.Fatalf("空密码不应序列化 password 键: %v", sso)
	}
	p = ConfigPatch{SSO: &SSOConfig{Username: "u", Password: "pw"}}.Map()
	sso, _ = p["sso"].(map[string]any)
	if sso["password"] != "pw" {
		t.Fatalf("带密码应序列化 password 键: %v", sso)
	}
}

func TestFakeSmoke_AppsAndStatus(t *testing.T) {
	ctx := context.Background()
	f := NewFakeBackend()
	apps, err := f.ListApps(ctx)
	if err != nil || len(apps) != 6 {
		t.Fatalf("ListApps: %v %d", err, len(apps))
	}
	for i := 1; i < len(apps); i++ {
		if apps[i-1].Order > apps[i].Order {
			t.Fatalf("应用未按 order 排序: %+v", apps)
		}
	}

	raw, err := f.AppStatus(ctx, "daily_sync")
	if err != nil {
		t.Fatalf("AppStatus: %v", err)
	}
	ds := DailyStatusFrom(raw)
	if ds.Schedule.Time != "09:15" || ds.USB.Label != "GLKVM" {
		t.Fatalf("DailyStatus 解码不符: %+v", ds)
	}

	raw, _ = f.AppStatus(ctx, "tree_sync")
	ss := SyncStatusFrom("tree_sync", raw)
	if ss.Configured() {
		t.Fatalf("未配置内网表时 Configured 应为假: %+v", ss)
	}

	raw, _ = f.AppStatus(ctx, "course_autoplay")
	cs := CourseStatusFrom(raw)
	if cs.CDPURL == "" || !cs.Playwright {
		t.Fatalf("CourseStatus 解码不符: %+v", cs)
	}
}

func TestFakeSmoke_JobLifecycle(t *testing.T) {
	ctx := context.Background()
	f := NewFakeBackend()
	f.StepDelay = 0

	sub, err := f.Subscribe(ctx)
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	defer sub.Close()

	var logs []string
	var progress []JobProgress
	doneCh := make(chan *Job, 1) // 终态经通道交接（建立 happens-before，-race 干净）
	go func() {
		for ev := range sub.Events() {
			switch ev.Kind {
			case EventJobLog:
				logs = append(logs, ev.Line)
			case EventJobProgress:
				progress = append(progress, *ev.Progress)
			case EventJobDone:
				doneCh <- ev.Job
				return
			}
		}
	}()

	id, err := f.RunJob(ctx, "daily_sync", map[string]any{"date": "2026-10-28"})
	if err != nil || id == "" {
		t.Fatalf("RunJob: %q %v", id, err)
	}
	var done *Job
	select {
	case done = <-doneCh:
	case <-time.After(2 * time.Second):
		t.Fatal("等待 job.done 超时")
	}
	if done.Status != JobSuccess || done.ID != id {
		t.Fatalf("终态不符: %+v", done)
	}
	if len(logs) == 0 || len(progress) == 0 {
		t.Fatalf("事件不完整: logs=%d progress=%d", len(logs), len(progress))
	}
	res := DailyResultFrom(done.Result)
	if res.Count == 0 || res.File == "" {
		t.Fatalf("daily_sync 结果不符: %+v", res)
	}
	if s := DailySummary(res); !strings.Contains(s, "条") {
		t.Fatalf("摘要文案不符: %q", s)
	}

	text, err := f.JobLog(ctx, id)
	if err != nil || text == "" {
		t.Fatalf("JobLog: %q %v", text, err)
	}
	hist, _ := f.History(ctx, "daily_sync")
	if len(hist) != 1 || hist[0].JobID != id {
		t.Fatalf("History 不符: %+v", hist)
	}
	del, err := f.DeleteHistory(ctx, "daily_sync", id)
	if err != nil || del.Records != 1 {
		t.Fatalf("DeleteHistory: %+v %v", del, err)
	}
	hist, _ = f.History(ctx, "daily_sync")
	if len(hist) != 0 {
		t.Fatalf("删除后仍有记录: %+v", hist)
	}
}

func TestFakeSmoke_CancelAndCoursePause(t *testing.T) {
	ctx := context.Background()
	f := NewFakeBackend()
	f.StepDelay = 4 * time.Millisecond

	// 普通任务取消
	id, err := f.RunJob(ctx, "tree_sync", map[string]any{"tick": true})
	if err != nil {
		t.Fatalf("RunJob: %v", err)
	}
	if err := f.CancelJob(ctx, id); err != nil {
		t.Fatalf("CancelJob: %v", err)
	}
	waitJobTerminal(t, f, id, JobCancelled)

	// 课程暂停：result.paused=true（用户暂停不是失败）
	cid, err := f.CourseStart(ctx, CourseOptions{ScanOnly: true, MaxAttempts: 3, Timeout: 120})
	if err != nil {
		t.Fatalf("CourseStart: %v", err)
	}
	if err := f.CoursePause(ctx, cid); err != nil {
		t.Fatalf("CoursePause: %v", err)
	}
	job := waitJobTerminal(t, f, cid, JobCancelled)
	if !job.Paused() {
		t.Fatalf("课程暂停任务 result.paused 应为 true: %+v", job.Result)
	}
}

func TestFakeSmoke_PreviewAndQuiz(t *testing.T) {
	ctx := context.Background()
	f := NewFakeBackend()
	f.StepDelay = 0

	// pcd_export 单号预览
	r, err := f.Preview(ctx, "pcd_export", map[string]any{"codes_text": "PC001\nPC002, PC003"})
	if err != nil {
		t.Fatalf("Preview: %v", err)
	}
	codes, _ := r["codes"].([]any)
	if len(codes) != 3 {
		t.Fatalf("codes 解析不符: %v", r)
	}

	// 题库：导入 → 匹配 → 停用 → 删除
	bank, err := f.QuizImportBank(ctx, filepath.Join(t.TempDir(), "安规题库.xlsx"))
	if err != nil || bank.Count == 0 {
		t.Fatalf("QuizImportBank: %+v %v", bank, err)
	}
	m, err := f.QuizMatch(ctx, "高处作业是指在坠落高度基准面几米及以上位置有可能坠落的高处进行的作业")
	if err != nil || !m.Matched {
		t.Fatalf("QuizMatch 应命中: %+v %v", m, err)
	}
	if m.Question.Answer == "" {
		t.Fatalf("命中题目缺答案: %+v", m.Question)
	}
	if err := f.QuizSetBankEnabled(ctx, bank.ID, false); err != nil {
		t.Fatalf("QuizSetBankEnabled: %v", err)
	}
	m, _ = f.QuizMatch(ctx, "高处作业是指在坠落高度基准面几米")
	if m.Matched {
		t.Fatalf("停用题库不应参与匹配: %+v", m)
	}
	if err := f.QuizRenameBank(ctx, bank.ID, "新名字"); err != nil {
		t.Fatalf("QuizRenameBank: %v", err)
	}
	if err := f.QuizRemoveBank(ctx, bank.ID); err != nil {
		t.Fatalf("QuizRemoveBank: %v", err)
	}
	banks, _ := f.QuizBanks(ctx)
	if len(banks) != 0 {
		t.Fatalf("删除后题库列表不为空: %+v", banks)
	}

	// 模板保存
	tpl := filepath.Join(t.TempDir(), "题库模板.xlsx")
	if err := f.QuizSaveTemplate(ctx, tpl); err != nil {
		t.Fatalf("QuizSaveTemplate: %v", err)
	}
}

func TestFakeSmoke_CourseAndSchedule(t *testing.T) {
	ctx := context.Background()
	f := NewFakeBackend()
	f.StepDelay = 0

	if err := f.CourseOpenBrowser(ctx, "https://edu.example/"); err != nil {
		t.Fatalf("CourseOpenBrowser: %v", err)
	}
	st, _ := f.CourseStatus(ctx)
	if !st.Connected || st.StartURL != "https://edu.example/" {
		t.Fatalf("CourseStatus 不符: %+v", st)
	}
	cks, err := f.CourseCheckpoints(ctx)
	if err != nil || len(cks) == 0 {
		t.Fatalf("CourseCheckpoints: %+v %v", cks, err)
	}

	// 未到点：不启动
	f.DueDaily, f.DueTree, f.DueCrossing = false, false, false
	d, _ := f.MaybeRunDaily(ctx)
	if d.Started {
		t.Fatalf("未到点不应启动: %+v", d)
	}
	if tr, _ := f.MaybeRunTreeSyncTick(ctx); tr.Started {
		t.Fatalf("树障未到点不应启动: %+v", tr)
	}

	// 到点：启动并带回 job_id
	f.DueDaily, f.DueTree, f.DueCrossing = true, true, true
	d, _ = f.MaybeRunDaily(ctx)
	if !d.Started || d.JobID == "" {
		t.Fatalf("到点应启动: %+v", d)
	}
	if d.TreeSyncNightly == nil || !d.TreeSyncNightly.Started {
		t.Fatalf("树障夜间兜底缺失: %+v", d)
	}
	if tr, _ := f.MaybeRunTreeSyncTick(ctx); !tr.Started || tr.JobID == "" {
		t.Fatalf("树障到点应启动: %+v", tr)
	}
	if cr, _ := f.MaybeRunCrossingSyncTick(ctx); !cr.Started {
		t.Fatalf("交跨到点应启动: %+v", cr)
	}
}

func TestFakeSmoke_OpenShowPath(t *testing.T) {
	ctx := context.Background()
	f := NewFakeBackend()
	if err := f.OpenPath(ctx, "/tmp/out"); err != nil {
		t.Fatalf("OpenPath: %v", err)
	}
	if err := f.ShowItem(ctx, "/tmp/out/1.pdf"); err != nil {
		t.Fatalf("ShowItem: %v", err)
	}
	if len(f.OpenedPaths) != 2 {
		t.Fatalf("OpenedPaths 不符: %v", f.OpenedPaths)
	}
	ls, err := f.LogStats(ctx)
	if err != nil || ls.Dir == "" {
		t.Fatalf("LogStats: %+v %v", ls, err)
	}
	if _, err := f.ClearLogs(ctx); err != nil {
		t.Fatalf("ClearLogs: %v", err)
	}
}

func waitJobTerminal(t *testing.T, f *FakeBackend, id string, want JobStatus) Job {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		job, err := f.GetJob(context.Background(), id)
		if err == nil && job.Status.Terminal() {
			if job.Status != want {
				t.Fatalf("任务 %s 状态 = %s，期望 %s", id, job.Status, want)
			}
			return job
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("等待任务 %s 终态超时", id)
	return Job{}
}

// TestFakeSmoke_QuizScanControl 扫描控制冒烟：启停/显隐/重置/选项钳制/自检/诊断 + scan/engine/boxHidden 事件流。
func TestFakeSmoke_QuizScanControl(t *testing.T) {
	ctx := context.Background()
	f := NewFakeBackend()
	f.StepDelay = 0

	// 无题库不能开始
	if _, err := f.QuizScanStart(ctx); err == nil {
		t.Fatal("无题库开始搜题应报错")
	}
	if _, err := f.QuizImportBank(ctx, "安规.xlsx"); err != nil {
		t.Fatalf("QuizImportBank: %v", err)
	}
	sub, err := f.QuizSubscribe(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()

	st, err := f.QuizScanStart(ctx)
	if err != nil || !st.Scanning || !st.EngineReady {
		t.Fatalf("QuizScanStart = %+v err=%v", st, err)
	}
	// scan + engine 事件
	deadline := time.After(3 * time.Second)
	var gotScan, gotEngine bool
	for !gotScan || !gotEngine {
		select {
		case ev := <-sub.Events():
			switch ev.Type {
			case QuizEventScan:
				gotScan = ev.Scanning
			case QuizEventEngine:
				gotEngine = ev.Ready
			}
		case <-deadline:
			t.Fatal("等待 scan/engine 事件超时")
		}
	}

	// 显隐 + boxHidden 事件 + 编号校验
	st, _ = f.QuizSetBoxVisible(ctx, "B", false)
	if st.BoxVisible["B"] {
		t.Fatalf("B 框应隐藏: %+v", st.BoxVisible)
	}
	select {
	case ev := <-sub.Events():
		if ev.Type != QuizEventBoxHidden || ev.Box != "B" {
			t.Fatalf("boxHidden 事件 = %+v", ev)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("等待 boxHidden 事件超时")
	}
	if _, err := f.QuizSetBoxVisible(ctx, "C", true); err == nil {
		t.Fatal("非法框编号应报错")
	}

	// 选项钳制 + 重置
	interval, threshold, opacity := 9999, 0.05, 5.0
	st, _ = f.QuizSetOptions(ctx, QuizScanOptions{Interval: &interval, Threshold: &threshold, Opacity: &opacity})
	if st.Interval != 3000 || st.Threshold != 0.3 || st.Opacity != 1 {
		t.Fatalf("选项未按口径钳制: interval=%d threshold=%.2f opacity=%.2f", st.Interval, st.Threshold, st.Opacity)
	}
	st, _ = f.QuizResetBoxes(ctx)
	if !st.BoxVisible["A"] || !st.BoxVisible["B"] {
		t.Fatalf("重置后应双框可见: %+v", st.BoxVisible)
	}

	// 自检 + 诊断
	r, err := f.QuizSelftest(ctx)
	if err != nil || !r.OK {
		t.Fatalf("QuizSelftest = %+v err=%v", r, err)
	}
	file, err := f.QuizDebugCapture(ctx, "")
	if err != nil || file == "" {
		t.Fatalf("QuizDebugCapture = %q err=%v", file, err)
	}

	// 停止 + 未扫描时诊断报错
	if st, err = f.QuizScanStop(ctx); err != nil || st.Scanning {
		t.Fatalf("QuizScanStop = %+v err=%v", st, err)
	}
	if _, err := f.QuizDebugCapture(ctx, ""); err == nil {
		t.Fatal("未扫描时保存题图应报错")
	}
}
