package ui

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/zhangzhenyu91/j1toolkit/uvmp-toolkit/core-go/internal/quizsearch"
)

// FakeBackend 是 Backend 的内存实现（冒烟/演示用）：
// 任务真实走「启动→日志→进度→终态」事件流，配置/历史/题库/检查点在内存中持久，
// 驻留调度判定由 Due 开关控制。行为确定、无网络、无外部进程。
type FakeBackend struct {
	mu sync.Mutex

	cfg        Config
	apps       []AppInfo
	jobs       map[string]*Job
	jobLogs    map[string][]string
	jobCancels map[string]bool
	history    map[string][]HistoryRecord
	banks      []QuizBank
	bankQuiz   map[string][]QuizQuestion
	quizScan   QuizScanState
	quizSubs   map[*fakeQuizSub]struct{}
	checks     []CourseCheckpoint
	course     CourseStatus
	subs       map[*fakeSub]struct{}
	seq        int

	// StepDelay 模拟任务节奏（每个进度步间隔）；0 则任务同步完成。
	StepDelay time.Duration
	// DueDaily / DueTree / DueCrossing 决定对应 MaybeRun* 判定是否到点启动任务。
	DueDaily, DueTree, DueCrossing bool
	// OpenedPaths 记录 OpenPath / ShowItem 调用（冒烟断言用）。
	OpenedPaths []string
	// FailNextRun 使下一次 RunJob 启动失败（测试启动错误路径）。
	FailNextRun error
}

// NewFakeBackend 构造带演示数据的 FakeBackend。
func NewFakeBackend() *FakeBackend {
	f := &FakeBackend{
		cfg:        DefaultConfig(),
		jobs:       map[string]*Job{},
		jobLogs:    map[string][]string{},
		jobCancels: map[string]bool{},
		history:    map[string][]HistoryRecord{},
		bankQuiz:   map[string][]QuizQuestion{},
		quizScan: QuizScanState{
			Interval:   1000,
			Threshold:  0.6,
			Opacity:    0.9,
			BoxVisible: map[string]bool{"A": true, "B": true},
			BoxDebug:   map[string]*QuizBoxDebug{},
		},
		quizSubs:  map[*fakeQuizSub]struct{}{},
		subs:      map[*fakeSub]struct{}{},
		StepDelay: 2 * time.Millisecond,
	}
	f.apps = []AppInfo{
		{ID: "daily_sync", Name: "派车同步", Badge: "日", Desc: "定时导出当日派车单 xlsx 写入 U 盘", Order: 1},
		{ID: "tree_sync", Name: "树障同步", Badge: "树", Desc: "树障统计表 snapshot/replace 同步", Order: 2},
		{ID: "crossing_sync", Name: "交跨同步", Badge: "跨", Desc: "交跨统计表 snapshot/replace 同步", Order: 3},
		{ID: "pcd_export", Name: "轨迹导出", Badge: "迹", Desc: "按派车单号清单导出派车单/轨迹 PDF", Order: 4, HasBrowser: true},
		{ID: "quiz_search", Name: "题库搜题", Badge: "题", Desc: "本机题库导入与题干匹配", Order: 5},
		{ID: "course_autoplay", Name: "学堂刷课", Badge: "课", Desc: "受控浏览器自动学习国网学堂课程", Order: 6, HasBrowser: true},
	}
	f.course = CourseStatus{CDPURL: "http://127.0.0.1:9222", Playwright: true, StartURL: "https://edu.sgcc.com.cn/"}
	f.checks = []CourseCheckpoint{
		{TrainID: "train-001", ListName: "2026 春检培训班", Page: 3, Processed: 8, UpdatedAt: "2026-10-28 08:30:00"},
	}
	f.bankQuiz["demo-bank"] = []QuizQuestion{
		{Content: "工作票签发人应由熟悉人员技术水平熟悉本规程并具有相关工作经验的生产领导人技术人员或经本单位批准的人员担任", Answer: "A", Options: map[string]string{"A": "正确", "B": "错误"}, Type: "判断题"},
		{Content: "触电急救胸外按压与口对口人工呼吸同时进行时其节奏为单人抢救每按压多少次后吹气", Answer: "C", Options: map[string]string{"A": "5", "B": "10", "C": "15", "D": "30"}, Type: "单选题"},
		{Content: "高处作业是指在坠落高度基准面几米及以上位置有可能坠落的高处进行的作业", Answer: "B", Options: map[string]string{"A": "1", "B": "2", "C": "3", "D": "5"}, Type: "单选题"},
	}
	return f
}

var _ Backend = (*FakeBackend)(nil)

// ---- SystemAPI ----

func (f *FakeBackend) Ping(context.Context) error { return nil }

func (f *FakeBackend) SystemInfo(context.Context) (SystemInfo, error) {
	return SystemInfo{
		Platform:         "windows",
		Machine:          "amd64",
		CoreVersion:      "fake-core",
		Chrome:           "",
		ConfigPath:       "mem://config.json",
		StateDir:         "mem://state",
		SuggestedOutDirs: []string{"mem://output"},
	}, nil
}

func (f *FakeBackend) OpenPath(_ context.Context, path string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.OpenedPaths = append(f.OpenedPaths, "open:"+path)
	return nil
}

func (f *FakeBackend) ShowItem(_ context.Context, path string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.OpenedPaths = append(f.OpenedPaths, "show:"+path)
	return nil
}

// Opened 已打开/定位路径快照（加锁读，冒烟断言用）。
func (f *FakeBackend) Opened() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.OpenedPaths...)
}

// ---- ConfigAPI ----

func (f *FakeBackend) GetConfig(context.Context) (Config, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	c := f.cfg
	// 密码不回读
	c.SSO.Password = ""
	c.SSO.HasPassword = f.cfg.SSO.Password != "" || f.cfg.SSO.HasPassword
	return c, nil
}

func (f *FakeBackend) SaveConfig(_ context.Context, patch ConfigPatch) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	applyPatch(&f.cfg, patch)
	return nil
}

func applyPatch(c *Config, p ConfigPatch) {
	if s := p.SSO; s != nil {
		c.SSO.Username = s.Username
		if s.Password != "" {
			c.SSO.Password = s.Password
			c.SSO.HasPassword = true
			c.SSO.PasswordSource = "config.json"
		}
	}
	if r := p.Render; r != nil {
		c.Render = *r
	}
	if o := p.OrderExport; o != nil {
		c.OrderExport = *o
	}
	if s := p.Schedule; s != nil {
		c.Schedule = *s
	}
	if u := p.UI; u != nil {
		c.UI = *u
	}
	if d := p.Dev; d != nil {
		c.Dev = *d
	}
	if u := p.USB; u != nil {
		c.USB = *u
	}
	if s := p.TreeSync; s != nil {
		c.TreeSync = *s
	}
	if s := p.CrossingSync; s != nil {
		c.CrossingSync = *s
	}
	if b := p.BrowserAutomation; b != nil {
		c.BrowserAutomation = *b
	}
}

// ---- AppAPI ----

func (f *FakeBackend) ListApps(context.Context) ([]AppInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := append([]AppInfo(nil), f.apps...)
	sort.SliceStable(out, func(i, j int) bool { return out[i].Order < out[j].Order })
	return out, nil
}

func (f *FakeBackend) AppStatus(_ context.Context, appID string) (map[string]any, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	base := map[string]any{
		"schedule": map[string]any{
			"enabled":  f.cfg.Schedule.Enabled,
			"time":     f.cfg.Schedule.Time,
			"driver":   f.cfg.Schedule.Driver,
			"next_run": "明日 " + f.cfg.Schedule.Time,
		},
		"usb": map[string]any{
			"label":        f.cfg.USB.Label,
			"mount":        "/media/GLKVM",
			"fallback_dir": f.cfg.USB.FallbackDir,
		},
	}
	last := f.lastRunLocked(appID)
	if last != nil {
		base["last_run"] = map[string]any{"time": last.Time, "status": last.Status, "error": last.Error}
	}
	switch appID {
	case "daily_sync":
		base["stamp"] = map[string]any{"time": "2026-10-28 09:15:12", "count": 6, "file": "2026-10-28-派车单.xlsx"}
		base["due"] = map[string]any{"reason": reasonText(f.DueDaily, f.cfg.Schedule.Time)}
	case "tree_sync", "crossing_sync":
		base["stamp"] = map[string]any{"time": "2026-10-27 23:55:03"}
		syncCfg := f.cfg.TreeSync
		if appID == "crossing_sync" {
			syncCfg = f.cfg.CrossingSync
		}
		base[appID] = map[string]any{
			"local_path":   syncCfg.LocalPath,
			"local_exists": syncCfg.LocalPath != "",
			"file_name":    syncCfg.FileName,
			"nightly_time": syncCfg.NightlyTime,
		}
	case "course_autoplay":
		for k, v := range CourseStatusToMap(f.course) {
			base[k] = v
		}
	}
	return base, nil
}

func reasonText(due bool, t string) string {
	if due {
		return "已到 " + t + "，本次轮询将执行"
	}
	return "未到 " + t + "（或今日已成功）"
}

func (f *FakeBackend) LaunchAppBrowser(_ context.Context, appID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	switch appID {
	case "course_autoplay":
		f.course.Connected = true
		f.course.Pages = 2
		f.course.Browser = "fake-electron"
		return nil
	case "pcd_export":
		return nil
	default:
		return fmt.Errorf("应用 %s 不支持受控浏览器", appID)
	}
}

// ---- TaskAPI ----

func (f *FakeBackend) RunJob(_ context.Context, appID string, params map[string]any) (string, error) {
	f.mu.Lock()
	if err := f.FailNextRun; err != nil {
		f.FailNextRun = nil
		f.mu.Unlock()
		return "", err
	}
	if appID == "" {
		f.mu.Unlock()
		return "", errors.New("app_id 不能为空")
	}
	f.seq++
	id := fmt.Sprintf("job-%04d", f.seq)
	job := &Job{ID: id, AppID: appID, Status: JobRunning, StartedAt: time.Now()}
	f.jobs[id] = job
	f.jobLogs[id] = nil
	f.mu.Unlock()

	go f.simulate(id, appID, params)
	return id, nil
}

// simulate 模拟任务执行并推送事件流（日志→进度→终态）。
func (f *FakeBackend) simulate(id, appID string, params map[string]any) {
	steps := fakeSteps(appID)
	f.emit(Event{Kind: EventJobLog, JobID: id, Line: fmt.Sprintf("任务 %s 启动（应用 %s）", id, appID)})
	for i := 1; i <= steps; i++ {
		f.mu.Lock()
		cancelled := f.jobCancels[id]
		f.mu.Unlock()
		if cancelled {
			f.finish(id, JobCancelled, map[string]any{"paused": appID == "course_autoplay", "processed": i - 1}, "")
			return
		}
		f.emit(Event{Kind: EventJobLog, JobID: id, Line: fmt.Sprintf("步骤 %d/%d：处理中…", i, steps)})
		p := JobProgress{JobID: id, Current: i, Total: steps, Label: "执行中"}
		ev := Event{Kind: EventJobProgress, JobID: id, Progress: &p}
		f.emit(ev)
		if f.StepDelay > 0 {
			time.Sleep(f.StepDelay)
		}
	}
	f.finish(id, JobSuccess, fakeResult(appID, params), "")
}

func fakeSteps(appID string) int {
	switch appID {
	case "pcd_export":
		return 4
	case "course_autoplay":
		return 6
	default:
		return 3
	}
}

func fakeResult(appID string, params map[string]any) map[string]any {
	switch {
	case appID == "daily_sync":
		date, _ := params["date"].(string)
		if date == "" {
			date = time.Now().Format("2006-01-02")
		}
		return map[string]any{"count": 6, "file": date + "-派车单.xlsx", "dest": "GLKVM", "dir": "mem://usb"}
	case appID == "tree_sync", appID == "crossing_sync":
		kind := "snapshot"
		if nightly, _ := params["nightly"].(bool); nightly {
			return map[string]any{"kind": "snapshot", "nightly": true, "file": "夜间兜底.xlsx"}
		}
		return map[string]any{"kind": kind, "cycle_id": "c-1028", "file": "统计表.xlsx"}
	case appID == "pcd_export":
		batch, _ := params["batch_name"].(string)
		return map[string]any{
			"total": 3, "pcd_ok": 3, "pcd_fail": 0, "track_ok": 2, "track_fail": 1,
			"not_found": []any{}, "dir": "mem://output/批次_导出",
			"batch_name": batch, "outdir": params["outdir"],
		}
	case appID == "course_autoplay":
		return map[string]any{
			"total": 10, "completed": 10, "pending": 0, "skipped": 0, "processed": 10,
			"scan_only": params["scan_only"], "max_attempts": params["max_attempts"], "timeout": params["timeout"],
		}
	case strings.HasPrefix(appID, "selftest:"):
		// 环境自检任务：cdp 产物 png/pdf（设置页「自检产物」按此回显）
		return map[string]any{
			"ok": true, "browser": "fake-electron",
			"png": "mem://selftest/自检截图.png", "pdf": "mem://selftest/自检输出.pdf",
		}
	default:
		return map[string]any{"ok": true}
	}
}

func (f *FakeBackend) finish(id string, st JobStatus, result map[string]any, errMsg string) {
	f.mu.Lock()
	job := f.jobs[id]
	if job == nil || job.Status.Terminal() {
		f.mu.Unlock()
		return
	}
	job.Status = st
	job.Result = result
	job.Error = errMsg
	job.FinishedAt = time.Now()
	rec := HistoryRecord{
		JobID:  id,
		Time:   job.FinishedAt.Format("2006-01-02 15:04:05"),
		Status: st,
		Error:  errMsg,
		Result: result,
	}
	if dir, ok := result["dir"].(string); ok {
		rec.Dir = dir
	}
	f.history[job.AppID] = append([]HistoryRecord{rec}, f.history[job.AppID]...)
	done := *job
	f.mu.Unlock()
	f.emit(Event{Kind: EventJobDone, JobID: id, Job: &done})
}

func (f *FakeBackend) CancelJob(_ context.Context, jobID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	job := f.jobs[jobID]
	if job == nil {
		return fmt.Errorf("任务不存在：%s", jobID)
	}
	if job.Status.Terminal() {
		return nil
	}
	f.jobCancels[jobID] = true
	return nil
}

func (f *FakeBackend) GetJob(_ context.Context, jobID string) (Job, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	job := f.jobs[jobID]
	if job == nil {
		return Job{}, fmt.Errorf("任务不存在：%s", jobID)
	}
	return *job, nil
}

func (f *FakeBackend) JobLog(_ context.Context, jobID string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.jobs[jobID]; !ok {
		return "", fmt.Errorf("任务不存在：%s", jobID)
	}
	return strings.Join(f.jobLogs[jobID], "\n"), nil
}

func (f *FakeBackend) Preview(_ context.Context, appID string, params map[string]any) (map[string]any, error) {
	if appID != "pcd_export" {
		return map[string]any{}, nil
	}
	var codes []any
	if text, ok := params["codes_text"].(string); ok && strings.TrimSpace(text) != "" {
		for i, line := range strings.FieldsFunc(text, func(r rune) bool { return r == '\n' || r == ',' || r == ' ' || r == '\t' || r == '\r' }) {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			codes = append(codes, map[string]any{"seq": i + 1, "code": line, "date_hint": ""})
		}
	} else if path, ok := params["xlsx_path"].(string); ok && path != "" {
		for i := 1; i <= 3; i++ {
			codes = append(codes, map[string]any{"seq": i, "code": fmt.Sprintf("PC2026%04d", i), "date_hint": "2026-10-28"})
		}
	}
	return map[string]any{"codes": codes}, nil
}

// ---- HistoryAPI ----

func (f *FakeBackend) History(_ context.Context, appID string) ([]HistoryRecord, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]HistoryRecord(nil), f.history[appID]...), nil
}

func (f *FakeBackend) DeleteHistory(_ context.Context, appID string, jobID string) (DeleteResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var kept []HistoryRecord
	var res DeleteResult
	for _, r := range f.history[appID] {
		if jobID == "" || r.JobID == jobID {
			res.Records++
			res.Files++
			delete(f.jobLogs, r.JobID)
			delete(f.jobs, r.JobID)
			continue
		}
		kept = append(kept, r)
	}
	f.history[appID] = kept
	return res, nil
}

func (f *FakeBackend) ClearLogs(context.Context) (ClearResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := len(f.jobLogs)
	f.jobLogs = map[string][]string{}
	return ClearResult{Removed: n}, nil
}

func (f *FakeBackend) LogStats(context.Context) (LogStats, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return LogStats{Dir: "mem://state/logs", Files: len(f.jobLogs), Bytes: int64(len(f.jobLogs)) * 128}, nil
}

// ---- QuizAPI ----

func (f *FakeBackend) QuizBanks(context.Context) ([]QuizBank, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]QuizBank(nil), f.banks...), nil
}

func (f *FakeBackend) QuizImportBank(_ context.Context, path string) (QuizBank, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	name := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	f.seq++
	b := QuizBank{
		ID:         fmt.Sprintf("bank-%04d", f.seq),
		Name:       name,
		SourceFile: path,
		Count:      3,
		ImportedAt: time.Now(),
		Enabled:    true,
	}
	f.banks = append(f.banks, b)
	f.bankQuiz[b.ID] = f.bankQuiz["demo-bank"] // 演示题目集
	f.recomputeQuestionCountLocked()
	return b, nil
}

// recomputeQuestionCountLocked 按启用题库合计刷新扫描态 QuestionCount（持锁调用）。
func (f *FakeBackend) recomputeQuestionCountLocked() {
	n := 0
	for _, b := range f.banks {
		if b.Enabled {
			n += b.Count
		}
	}
	f.quizScan.QuestionCount = n
}

func (f *FakeBackend) QuizSetBankEnabled(_ context.Context, bankID string, enabled bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := range f.banks {
		if f.banks[i].ID == bankID {
			f.banks[i].Enabled = enabled
			f.recomputeQuestionCountLocked()
			return nil
		}
	}
	return fmt.Errorf("题库不存在：%s", bankID)
}

func (f *FakeBackend) QuizRenameBank(_ context.Context, bankID string, name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := range f.banks {
		if f.banks[i].ID == bankID {
			f.banks[i].Name = name
			return nil
		}
	}
	return fmt.Errorf("题库不存在：%s", bankID)
}

func (f *FakeBackend) QuizRemoveBank(_ context.Context, bankID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := range f.banks {
		if f.banks[i].ID == bankID {
			f.banks = append(f.banks[:i], f.banks[i+1:]...)
			delete(f.bankQuiz, bankID)
			return nil
		}
	}
	return fmt.Errorf("题库不存在：%s", bankID)
}

// QuizMatch 题干相似度匹配（字符二元组 Jaccard，确定性、无依赖）。
func (f *FakeBackend) QuizMatch(_ context.Context, text string) (QuizMatch, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var best QuizMatch
	for _, b := range f.banks {
		if !b.Enabled {
			continue
		}
		for _, q := range f.bankQuiz[b.ID] {
			score := similarity(text, q.Content)
			qq := q
			qq.BankName = b.Name
			if score > best.Score {
				best.Score = score
				best.Question = qq
				near := qq
				best.Near = &near
			}
		}
	}
	if best.Score >= 0.35 {
		best.Matched = true
		return best, nil
	}
	return QuizMatch{Matched: false, Score: best.Score, Near: best.Near}, nil
}

func (f *FakeBackend) QuizSaveTemplate(_ context.Context, path string) error {
	const tpl = "题干,答案,选项A,选项B,选项C,选项D,题型,解析\n示例：触电急救胸外按压…,A,正确,错误,,,,判断题,\n"
	return os.WriteFile(path, []byte("\ufeff"+tpl), 0o644)
}

// ---- QuizAPI 扫描控制（模拟扫描服务：状态/启停/显隐/重置/选项/诊断/自检/事件）----

func (f *FakeBackend) QuizScanState(context.Context) (QuizScanState, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return cloneScanState(f.quizScan), nil
}

func (f *FakeBackend) QuizScanStart(context.Context) (QuizScanState, error) {
	f.mu.Lock()
	if len(f.banks) == 0 {
		f.mu.Unlock()
		return QuizScanState{}, errors.New("请先导入题库")
	}
	changed := !f.quizScan.Scanning
	if changed {
		f.quizScan.Scanning = true
		f.quizScan.EngineReady = true
		f.quizScan.EngineError = ""
		f.quizScan.CaptureError = ""
	}
	out := cloneScanState(f.quizScan)
	f.mu.Unlock()
	if changed {
		f.emitQuiz(QuizEvent{Type: QuizEventScan, Scanning: true, Ready: true})
		f.emitQuiz(QuizEvent{Type: QuizEventEngine, Ready: true})
	}
	return out, nil
}

func (f *FakeBackend) QuizScanStop(context.Context) (QuizScanState, error) {
	f.mu.Lock()
	changed := f.quizScan.Scanning
	if changed {
		f.quizScan.Scanning = false
		f.quizScan.EngineReady = false
	}
	out := cloneScanState(f.quizScan)
	f.mu.Unlock()
	if changed {
		f.emitQuiz(QuizEvent{Type: QuizEventScan, Scanning: false, Ready: true})
	}
	return out, nil
}

func (f *FakeBackend) QuizSetBoxVisible(_ context.Context, box string, visible bool) (QuizScanState, error) {
	id := strings.ToUpper(strings.TrimSpace(box))
	if id != "A" && id != "B" {
		return QuizScanState{}, fmt.Errorf("扫描框编号只能是 A/B：%q", box)
	}
	f.mu.Lock()
	f.quizScan.BoxVisible[id] = visible
	out := cloneScanState(f.quizScan)
	f.mu.Unlock()
	if !visible {
		f.emitQuiz(QuizEvent{Type: QuizEventBoxHidden, Box: id})
	}
	return out, nil
}

func (f *FakeBackend) QuizResetBoxes(context.Context) (QuizScanState, error) {
	f.mu.Lock()
	f.quizScan.BoxVisible = map[string]bool{"A": true, "B": true}
	out := cloneScanState(f.quizScan)
	f.mu.Unlock()
	return out, nil
}

func (f *FakeBackend) QuizSetOptions(_ context.Context, opt QuizScanOptions) (QuizScanState, error) {
	f.mu.Lock()
	if opt.Interval != nil {
		f.quizScan.Interval = quizsearch.ClampInterval(*opt.Interval)
	}
	if opt.Threshold != nil {
		f.quizScan.Threshold = quizsearch.ClampThreshold(*opt.Threshold)
	}
	if opt.Opacity != nil {
		f.quizScan.Opacity = quizsearch.ClampOpacity(*opt.Opacity)
	}
	out := cloneScanState(f.quizScan)
	f.mu.Unlock()
	return out, nil
}

func (f *FakeBackend) QuizDebugCapture(_ context.Context, dir string) (string, error) {
	f.mu.Lock()
	scanning := f.quizScan.Scanning
	f.mu.Unlock()
	if !scanning {
		return "", errors.New("扫描框未开（先开始搜题）")
	}
	base := dir
	if base == "" {
		base = "mem://quiz-search"
	}
	return fmt.Sprintf("%s/题图调试_框A_%d.png", base, time.Now().UnixMilli()), nil
}

func (f *FakeBackend) QuizSelftest(context.Context) (QuizSelftestResult, error) {
	return QuizSelftestResult{
		OK:        true,
		ModelsDir: "mem://models",
		Missing:   []string{},
		Engine:    map[string]any{"ok": true, "engine": "fake"},
	}, nil
}

func (f *FakeBackend) QuizSubscribe(context.Context) (QuizEventStream, error) {
	s := &fakeQuizSub{ch: make(chan QuizEvent, 64), f: f}
	f.mu.Lock()
	f.quizSubs[s] = struct{}{}
	f.mu.Unlock()
	return s, nil
}

// emitQuiz 扫描事件扇出（不持锁发送，订阅通道满则丢最旧保最新）。
func (f *FakeBackend) emitQuiz(ev QuizEvent) {
	f.mu.Lock()
	subs := make([]*fakeQuizSub, 0, len(f.quizSubs))
	for s := range f.quizSubs {
		subs = append(subs, s)
	}
	f.mu.Unlock()
	for _, s := range subs {
		s.send(ev)
	}
}

// cloneScanState 防御性拷贝（map 不外泄内部引用）。
func cloneScanState(st QuizScanState) QuizScanState {
	out := st
	out.BoxVisible = map[string]bool{}
	for k, v := range st.BoxVisible {
		out.BoxVisible[k] = v
	}
	out.BoxDebug = map[string]*QuizBoxDebug{}
	for k, v := range st.BoxDebug {
		if v == nil {
			out.BoxDebug[k] = nil
			continue
		}
		cp := *v
		out.BoxDebug[k] = &cp
	}
	return out
}

// fakeQuizSub 扫描事件订阅句柄（Close 幂等）。
type fakeQuizSub struct {
	f      *FakeBackend
	ch     chan QuizEvent
	mu     sync.Mutex
	closed bool
	once   sync.Once
}

func (s *fakeQuizSub) Events() <-chan QuizEvent { return s.ch }

func (s *fakeQuizSub) Close() error {
	s.once.Do(func() {
		s.f.mu.Lock()
		delete(s.f.quizSubs, s)
		s.f.mu.Unlock()
		s.mu.Lock()
		s.closed = true
		close(s.ch)
		s.mu.Unlock()
	})
	return nil
}

// send 投递事件（满则丢最旧保最新；订阅者关闭后丢弃）。
func (s *fakeQuizSub) send(ev QuizEvent) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	for {
		select {
		case s.ch <- ev:
			return
		default:
			select {
			case <-s.ch:
			default:
			}
		}
	}
}

// similarity 字符二元组 Jaccard 相似度（短文本退化为单字符组）。
func similarity(a, b string) float64 {
	ra, rb := bigrams(a), bigrams(b)
	if len(ra) == 0 || len(rb) == 0 {
		return 0
	}
	inter := 0
	for g := range ra {
		if _, ok := rb[g]; ok {
			inter++
		}
	}
	union := len(ra) + len(rb) - inter
	if union == 0 {
		return 0
	}
	return float64(inter) / float64(union)
}

func bigrams(s string) map[string]struct{} {
	r := []rune(strings.TrimSpace(s))
	out := map[string]struct{}{}
	if len(r) == 0 {
		return out
	}
	if len(r) == 1 {
		out[string(r)] = struct{}{}
		return out
	}
	for i := 0; i+1 < len(r); i++ {
		out[string(r[i:i+2])] = struct{}{}
	}
	return out
}

// ---- CourseAPI ----

func (f *FakeBackend) CourseStatus(context.Context) (CourseStatus, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.course, nil
}

func (f *FakeBackend) CourseOpenBrowser(_ context.Context, startURL string) error {
	f.mu.Lock()
	if startURL != "" {
		f.course.StartURL = startURL
	}
	f.course.Connected = true
	f.course.Pages = 2
	f.course.Browser = "fake-electron"
	f.mu.Unlock()
	return nil
}

func (f *FakeBackend) CourseStart(_ context.Context, opt CourseOptions) (string, error) {
	id, err := f.RunJob(context.Background(), "course_autoplay", map[string]any{
		"scan_only":    opt.ScanOnly,
		"max_attempts": opt.MaxAttempts,
		"timeout":      opt.Timeout,
	})
	return id, err
}

func (f *FakeBackend) CoursePause(_ context.Context, jobID string) error {
	return f.CancelJob(context.Background(), jobID)
}

func (f *FakeBackend) CourseCheckpoints(context.Context) ([]CourseCheckpoint, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]CourseCheckpoint(nil), f.checks...), nil
}

// ---- ScheduleAPI ----

func (f *FakeBackend) MaybeRunDaily(context.Context) (DailyTickResult, error) {
	f.mu.Lock()
	due := f.DueDaily
	f.mu.Unlock()
	res := DailyTickResult{Reason: reasonText(due, f.cfg.Schedule.Time)}
	if !due {
		return res, nil
	}
	id, err := f.RunJob(context.Background(), "daily_sync", map[string]any{})
	if err != nil {
		return res, err
	}
	res.Started = true
	res.JobID = id
	if f.DueTree {
		res.TreeSyncNightly = &TickResult{Started: true, JobID: "job-nightly-tree"}
	}
	if f.DueCrossing {
		res.CrossingSyncNightly = &TickResult{Started: true, JobID: "job-nightly-crossing"}
	}
	return res, nil
}

func (f *FakeBackend) MaybeRunTreeSyncTick(context.Context) (TickResult, error) {
	if !f.DueTree {
		return TickResult{}, nil
	}
	id, err := f.RunJob(context.Background(), "tree_sync", map[string]any{"tick": true})
	if err != nil {
		return TickResult{}, err
	}
	return TickResult{Started: true, JobID: id}, nil
}

func (f *FakeBackend) MaybeRunCrossingSyncTick(context.Context) (TickResult, error) {
	if !f.DueCrossing {
		return TickResult{}, nil
	}
	id, err := f.RunJob(context.Background(), "crossing_sync", map[string]any{"tick": true})
	if err != nil {
		return TickResult{}, err
	}
	return TickResult{Started: true, JobID: id}, nil
}

// ---- EventAPI ----

func (f *FakeBackend) Subscribe(context.Context) (Subscription, error) {
	s := &fakeSub{ch: make(chan Event, 256), f: f}
	f.mu.Lock()
	f.subs[s] = struct{}{}
	f.mu.Unlock()
	return s, nil
}

func (f *FakeBackend) emit(ev Event) {
	f.mu.Lock()
	line := ev.Line
	f.mu.Unlock()
	if ev.Kind == EventJobLog && ev.JobID != "" {
		f.mu.Lock()
		f.jobLogs[ev.JobID] = append(f.jobLogs[ev.JobID], line)
		f.mu.Unlock()
	}
	f.mu.Lock()
	subs := make([]*fakeSub, 0, len(f.subs))
	for s := range f.subs {
		subs = append(subs, s)
	}
	f.mu.Unlock()
	for _, s := range subs {
		select {
		case s.ch <- ev:
		default: // 订阅者跟不上时丢弃（演示后端不阻塞任务）
		}
	}
}

func (f *FakeBackend) lastRunLocked(appID string) *HistoryRecord {
	if len(f.history[appID]) == 0 {
		return nil
	}
	return &f.history[appID][0]
}

type fakeSub struct {
	ch   chan Event
	f    *FakeBackend
	once sync.Once
}

func (s *fakeSub) Events() <-chan Event { return s.ch }

func (s *fakeSub) Close() error {
	s.once.Do(func() {
		s.f.mu.Lock()
		delete(s.f.subs, s)
		s.f.mu.Unlock()
		close(s.ch)
	})
	return nil
}
