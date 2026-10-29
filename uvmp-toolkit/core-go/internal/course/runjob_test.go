package course

import (
	"context"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// logRecorder 任务日志收集（断言提示文案用）。
type logRecorder struct {
	mu    sync.Mutex
	lines []string
}

func (r *logRecorder) log(msg string) {
	r.mu.Lock()
	r.lines = append(r.lines, msg)
	r.mu.Unlock()
}

func (r *logRecorder) contains(sub string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, l := range r.lines {
		if strings.Contains(l, sub) {
			return true
		}
	}
	return false
}

func (r *logRecorder) all() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return strings.Join(r.lines, "\n")
}

// newFlowTest 搭模拟站 + fake 驱动 + RunJob 参数。
func newFlowTest(t *testing.T, train string) (*mockStation, *fakeBrowser, RunJobOptions, *logRecorder) {
	t.Helper()
	station := newMockStation()
	t.Cleanup(station.Close)
	browser := newFakeBrowser(station, train)
	dir := t.TempDir()
	rec := &logRecorder{}
	opts := RunJobOptions{
		Options: Options{
			Timeout:          10 * time.Second,
			MaxAttempts:      1,
			OutDir:           dir,
			DiagnosticPrefix: "test_failure",
			Log:              rec.log,
		},
		Dir:   dir,
		Store: &CheckpointsStore{Path: filepath.Join(dir, "course-autoplay", "checkpoints.json")},
	}
	return station, browser, opts, rec
}

// TestRunJobPauseSwitchResume 模拟站全流程（course_mock_e2e.py 同口径）：
// 视频课中途暂停→检查点保留 active；切 train2 完成；回 train1 续刷完成视频/文档，
// 卡住课 3 次跳过后汇总。
func TestRunJobPauseSwitchResume(t *testing.T) {
	station, browser, opts, rec := newFlowTest(t, "train1")

	// 第一轮：视频课拖到末尾时暂停（cancel 钩子模拟「暂停学习」）
	ctx1, cancel1 := context.WithCancel(context.Background())
	paused := false
	opts.Log = func(msg string) {
		rec.log(msg)
		if !paused && strings.Contains(msg, "视频已拖到末尾附近") {
			paused = true
			cancel1()
		}
	}
	res1, err := RunJob(ctx1, browser, opts)
	if err != nil {
		t.Fatalf("首轮暂停应返回 paused 结果而非错误: %v", err)
	}
	if res1["paused"] != true {
		t.Fatalf("首轮结果 = %v", res1)
	}
	if res1["list_key"] != "train:train1" {
		t.Fatalf("list_key = %v", res1["list_key"])
	}
	if opts.Store.ActiveCount() != 1 {
		t.Fatal("暂停后检查点应 active")
	}

	// 第二轮：切到 train2（独立检查点）
	browser.train = "train2"
	res2, err := RunJob(context.Background(), browser, opts)
	if err != nil {
		t.Fatalf("train2 运行失败: %v", err)
	}
	assertNum(t, res2, "completed", 1)
	if res2["pending"] != 0 {
		t.Fatalf("train2 pending = %v", res2["pending"])
	}
	if opts.Store.ActiveCount() != 1 {
		t.Fatalf("train2 完成后 train1 检查点仍应 active，got %d", opts.Store.ActiveCount())
	}

	// 第三轮：回 train1 恢复续刷
	browser.train = "train1"
	res3, err := RunJob(context.Background(), browser, opts)
	if err != nil {
		t.Fatalf("train1 恢复运行失败: %v", err)
	}
	assertNum(t, res3, "total", 3)
	assertNum(t, res3, "completed", 2)
	assertNum(t, res3, "pending", 1)
	assertNum(t, res3, "skipped", 1)
	pending, _ := res3["pending_courses"].([]any)
	if len(pending) != 1 {
		t.Fatalf("pending_courses = %v", res3["pending_courses"])
	}
	pc := pending[0].(map[string]any)
	if pc["name"] != "模拟系统卡住课" {
		t.Fatalf("未完成课程 = %v", pc)
	}
	assertNum(t, pc, "progress", 50)
	if !strings.Contains(strAny(pc["reason"]), "连续 1 次处理后仍为 50%") {
		t.Fatalf("跳过原因 = %v", pc["reason"])
	}

	// 检查点落点：v 真实尝试 1 次、active=false（新一轮才可重试旧跳过项）
	cps := opts.Store.Load()
	cp := cps["train:train1"]
	if cp == nil || cp.Active {
		t.Fatalf("完成轮检查点 = %+v", cp)
	}
	if cp.Attempts["v"] != 1 {
		t.Fatalf("attempts[v] = %v", cp.Attempts)
	}
	if len(cp.SkippedIDs) != 1 || cp.SkippedIDs[0] != "s" {
		t.Fatalf("skipped_ids = %v", cp.SkippedIDs)
	}
	if !rec.contains("恢复当前培训班") || !rec.contains("跳过课程《模拟系统卡住课》") {
		t.Fatalf("日志缺关键行:\n%s", rec.all())
	}
	if station.rateOf("v") != 100 || station.rateOf("d") != 100 || station.rateOf("s") != 50 {
		t.Fatalf("站点进度 v=%g d=%g s=%g", station.rateOf("v"), station.rateOf("d"), station.rateOf("s"))
	}
}

// TestRunJobScanOnly 只扫描：不进入课程，结果形状为正式汇总。
func TestRunJobScanOnly(t *testing.T) {
	_, browser, opts, rec := newFlowTest(t, "train1")
	opts.ScanOnly = true
	res, err := RunJob(context.Background(), browser, opts)
	if err != nil {
		t.Fatal(err)
	}
	if res["paused"] != false {
		t.Fatalf("结果 = %v", res)
	}
	assertNum(t, res, "total", 3)
	assertNum(t, res, "completed", 0)
	assertNum(t, res, "pending", 3)
	assertNum(t, res, "skipped", 0)
	if res["list_key"] != "" {
		t.Fatalf("scan_only list_key = %v", res["list_key"])
	}
	if !rec.contains("只扫描模式结束，未进入任何课程") {
		t.Fatalf("日志:\n%s", rec.all())
	}
	if opts.Store.ActiveCount() != 0 {
		t.Fatal("scan_only 不应留 active 检查点")
	}
}

// TestRunJobAntiMisclick 行文本与课程名不符时阻止误点（失败→按尝试上限跳过）。
func TestRunJobAntiMisclick(t *testing.T) {
	station, browser, opts, rec := newFlowTest(t, "train1")
	browser.rowTextOverride = func(index int, items []map[string]any) (string, bool) {
		return "完全无关的课程 0% 进入学习", true
	}
	res, err := RunJob(context.Background(), browser, opts)
	if err != nil {
		t.Fatal(err)
	}
	assertNum(t, res, "skipped", 3) // 三门课全部因误点保护被跳过
	if !rec.contains("已阻止误点其他课程") {
		t.Fatalf("日志:\n%s", rec.all())
	}
	if station.rateOf("v") != 0 {
		t.Fatal("误点保护下不应完成课程")
	}
}

// TestRunJobVideoNoSeek 视频禁止快进：3 次拖动失败→单课失败→跳过继续。
func TestRunJobVideoNoSeek(t *testing.T) {
	_, browser, opts, rec := newFlowTest(t, "train1")
	browser.noSeek = true
	res, err := RunJob(context.Background(), browser, opts)
	if err != nil {
		t.Fatal(err)
	}
	if !rec.contains("连续 3 次拖动均未生效") {
		t.Fatalf("日志:\n%s", rec.all())
	}
	assertNum(t, res, "skipped", 2) // v 拖不动失败跳过；s 卡住不达 100 跳过（d 正常完成）
}

// TestRunJobVideoNeverEnds 视频拖到尾但不 ended：3 轮失败→单课失败。
func TestRunJobVideoNeverEnds(t *testing.T) {
	_, browser, opts, rec := newFlowTest(t, "train1")
	browser.noEnd = true
	_, err := RunJob(context.Background(), browser, opts)
	if err != nil {
		t.Fatal(err)
	}
	if !rec.contains("连续 3 轮拖动后，视频均未在 30 秒内播放结束") {
		t.Fatalf("日志:\n%s", rec.all())
	}
}

// TestRunJobCourseIDMismatch 课件清单与当前课程不一致：立即停止该课。
func TestRunJobCourseIDMismatch(t *testing.T) {
	_, browser, opts, rec := newFlowTest(t, "train1")
	browser.badCourseID = true
	_, err := RunJob(context.Background(), browser, opts)
	if err != nil {
		t.Fatal(err)
	}
	if !rec.contains("课件清单与当前课程不一致，已停止处理") {
		t.Fatalf("日志:\n%s", rec.all())
	}
}

// TestRunJobMultiWare 多课件树：视频+文档逐个处理，切换等课件切换响应。
func TestRunJobMultiWare(t *testing.T) {
	station, browser, opts, rec := newFlowTest(t, "train2")
	browser.waresOverride = []any{
		map[string]any{"wareType": float64(1), "courseId": "x", "wareName": "视频课件", "suffixInfo": ".mp4"},
		map[string]any{"wareType": float64(2), "courseId": "x", "wareName": "文档课件", "suffixInfo": ".pdf"},
	}
	res, err := RunJob(context.Background(), browser, opts)
	if err != nil {
		t.Fatal(err)
	}
	assertNum(t, res, "completed", 1)
	if !rec.contains("学习页已打开，共 2 个课件") || !rec.contains("课件 2/2 页面流程完成") {
		t.Fatalf("日志:\n%s", rec.all())
	}
	if station.rateOf("x") != 100 {
		t.Fatal("多课件完成后应 100%")
	}
}

// TestRunJobUnsupportedWareType 未知 wareType 明确报错。
func TestRunJobUnsupportedWareType(t *testing.T) {
	_, browser, opts, rec := newFlowTest(t, "train2")
	browser.waresOverride = []any{
		map[string]any{"wareType": float64(5), "courseId": "x", "wareName": "未知课件", "suffixInfo": ".xyz"},
	}
	_, err := RunJob(context.Background(), browser, opts)
	if err != nil {
		t.Fatal(err)
	}
	if !rec.contains("暂不支持 wareType=5 的课件加载流程") {
		t.Fatalf("日志:\n%s", rec.all())
	}
}

// TestRunJobPaginationTotalDrift 翻页期间总数变化：中止并报错。
func TestRunJobPaginationTotalDrift(t *testing.T) {
	_, browser, opts, _ := newFlowTest(t, "train1")
	browser.totalDriftNext = true
	_, err := RunJob(context.Background(), browser, opts)
	if err == nil || !strings.Contains(err.Error(), "翻页期间课程总数发生变化") {
		t.Fatalf("错误 = %v", err)
	}
}

// TestRunJobResumeFromPage2 续刷页码>1：先跳页续扫，末页后从第 1 页查缺补漏。
func TestRunJobResumeFromPage2(t *testing.T) {
	_, browser, opts, rec := newFlowTest(t, "train1")
	// 预置 active 检查点：续刷页 2
	cp := NewCheckpoint()
	cp.Active = true
	cp.ResumePage = 2
	all := opts.Store.Load()
	opts.Store.Persist(all, "train:train1", cp)

	res, err := RunJob(context.Background(), browser, opts)
	if err != nil {
		t.Fatal(err)
	}
	if !rec.contains("刷新后直接跳转到第 2 页续刷") || !rec.contains("已到末页，开始从第 1 页查缺补漏") {
		t.Fatalf("日志:\n%s", rec.all())
	}
	assertNum(t, res, "total", 3)
	if !rec.contains("恢复当前培训班：第 2 页") {
		t.Fatalf("恢复日志:\n%s", rec.all())
	}
}

// TestRunJobFailScreenshot 失败现场截图落盘（<prefix>_<NN>_course.png）。
func TestRunJobFailScreenshot(t *testing.T) {
	_, browser, opts, rec := newFlowTest(t, "train1")
	browser.failEnter = true
	_, err := RunJob(context.Background(), browser, opts)
	if err != nil {
		t.Fatal(err)
	}
	if !rec.contains("失败现场截图") {
		t.Fatalf("日志:\n%s", rec.all())
	}
	if len(browser.shots) == 0 {
		t.Fatal("应有失败截图")
	}
	base := filepath.Base(browser.shots[0])
	if !strings.HasPrefix(base, "test_failure_") || !strings.HasSuffix(base, "_course.png") {
		t.Fatalf("截图名 = %s", base)
	}
}

// TestFindListPageRecognition 页面识别：多候选报错、无候选报错、内容识别回退。
func TestFindListPageRecognition(t *testing.T) {
	station, browser, opts, rec := newFlowTest(t, "train1")

	// 多个 trainDetail 候选
	browser.extraTabs = []TabInfo{
		{ID: "other", URL: station.ListURL("train2"), Title: "另一个", Visible: true},
	}
	_, err := RunJob(context.Background(), browser, opts)
	if err == nil || !strings.Contains(err.Error(), "找到多个课程列表候选") {
		t.Fatalf("多候选错误 = %v", err)
	}

	// 无地址匹配也无内容标记（识别窗口按 Timeout 收短，避免长等）
	browser.extraTabs = nil
	browser.listTabURL = "https://other.example/home"
	browser.bodyText = "随便什么页面"
	opts.Timeout = time.Second
	_, err = RunJob(context.Background(), browser, opts)
	if err == nil || !strings.Contains(err.Error(), "未找到课程设置列表") {
		t.Fatalf("无候选错误 = %v", err)
	}
	_ = rec
}

// assertNum map 数值断言（JSON 往返后为 float64）。
func assertNum(t *testing.T, m map[string]any, key string, want float64) {
	t.Helper()
	got, ok := toFloat(m[key])
	if !ok || got != want {
		t.Fatalf("%s = %v，want %v", key, m[key], want)
	}
}
