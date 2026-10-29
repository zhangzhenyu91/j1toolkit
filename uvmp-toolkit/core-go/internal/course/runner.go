package course

import (
	"context"
	"fmt"
	"math"
	"path/filepath"
	"strings"
	"time"
)

// Options Runner 运行参数（trial.py argparse + backend.py params 合并口径）。
type Options struct {
	// Timeout 单步等待超时（trial.py --timeout 秒；≤0 取 120s）。
	Timeout time.Duration
	// MaxAttempts 单课尝试上限，达到后跳过（course_autoplay.max_attempts；≤0 取 3）。
	MaxAttempts int
	// OutDir 失败截图输出目录（state/logs）。
	OutDir string
	// DiagnosticPrefix 截图文件名前缀（<job_id>_failure）。
	DiagnosticPrefix string
	// Log 日志回调（无时间戳原样一行；job.Log 同口径）。
	Log func(msg string)
	// Progress 进度回调（job.SetProgress 同口径）。
	Progress func(current, total int, label string)
}

// 默认值（trial.py argparse 默认）。
const (
	DefaultTimeout     = 120 * time.Second
	DefaultMaxAttempts = 3
)

func (o Options) timeout() time.Duration {
	if o.Timeout <= 0 {
		return DefaultTimeout
	}
	return o.Timeout
}

func (o Options) maxAttempts() int {
	if o.MaxAttempts <= 0 {
		return DefaultMaxAttempts
	}
	return o.MaxAttempts
}

// CourseEntry 一门待处理课程及其列表定位（trial.py scan_courses 的 entry）。
type CourseEntry struct {
	Item     map[string]any
	Page     int
	Index    int
	PageSize int
	Reason   string // 阻塞原因（空=可学）
}

// ResourceID 课程资源 ID。
func (e CourseEntry) ResourceID() string { return strAny(e.Item["resourceid"]) }

// Name 课程名（缺省回退 resourceid）。
func (e CourseEntry) Name() string {
	if s := strings.TrimSpace(strAny(e.Item["coursename"])); s != "" {
		return s
	}
	return e.ResourceID()
}

// strAny 值转字符串（nil → 空串）。
func strAny(v any) string {
	if v == nil {
		return ""
	}
	return fmt.Sprintf("%v", v)
}

// ScanSummary 一轮分页扫描汇总（trial.py scan_courses 返回形状）。
type ScanSummary struct {
	Next      *CourseEntry // stop_first 时的下一门可学课程
	Pending   []CourseEntry
	Total     int
	Seen      int
	Studiable int
	Completed int
}

// Runner 课程自动学习状态机（trial.py CourseRunner 移植）：
// 页面识别/筛选/分页/防误点/弹窗/课件树/视频拖动与 ended/上报/其他课件/3 次跳过/
// 失败截图/暂停回滚/断点续刷。浏览器细节在 Browser/驱动层，决策全在此。
type Runner struct {
	opts    Options
	browser Browser
	monitor *ResponseMonitor

	listPage     ListPage
	activePopup  LearnPage
	diagnosticNo int
}

// NewRunner 创建状态机（browser 为已连接的受控浏览器会话）。
func NewRunner(browser Browser, opts Options) *Runner {
	return &Runner{opts: opts, browser: browser, monitor: browser.Monitor()}
}

// ListPage 当前接管的课程列表页（未接管为 nil）。
func (r *Runner) ListPage() ListPage { return r.listPage }

func (r *Runner) log(msg string) {
	if r.opts.Log != nil {
		r.opts.Log(msg)
		return
	}
	fmt.Println(msg)
}

func (r *Runner) reportProgress(current, total int, label string) {
	if r.opts.Progress != nil {
		r.opts.Progress(current, total, label)
	}
}

// sleep 可中断等待（trial.py _wait_for_timeout + cancel_check，200ms 切片）。
func (r *Runner) sleep(ctx context.Context, d time.Duration) error {
	deadline := time.Now().Add(d)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		remain := time.Until(deadline)
		if remain <= 0 {
			return nil
		}
		slice := 200 * time.Millisecond
		if remain < slice {
			slice = remain
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(slice):
		}
	}
}

// --------------------------------------------------------------- 页面识别

// FindListPage 识别并接管课程设置列表（trial.py find_list_page）：
// 优先可见标签页 + trainDetail 地址识别，回退页面内容标记（课程名称/学习进度/进入学习）。
func (r *Runner) FindListPage(ctx context.Context) error {
	deadline := time.Now().Add(minDuration(15*time.Second, r.opts.timeout()))
	recognition := "地址"
	var candidates []TabInfo
	var selected *TabInfo
	for {
		tabs, err := r.browser.Tabs(ctx)
		if err != nil {
			return err
		}
		visible := make([]TabInfo, 0, len(tabs))
		for _, t := range tabs {
			if t.Visible {
				visible = append(visible, t)
			}
		}
		search := visible
		if len(search) == 0 {
			search = tabs
		}
		candidates = nil
		recognition = "地址"
		for _, t := range search {
			if IsTrainDetailURL(t.URL) {
				candidates = append(candidates, t)
			}
		}
		if len(candidates) == 0 {
			recognition = "页面内容"
			for _, t := range search {
				text, err := r.browser.BodyText(ctx, t.ID)
				if err != nil {
					continue
				}
				if strings.Contains(text, "课程名称") && strings.Contains(text, "学习进度") && strings.Contains(text, "进入学习") {
					candidates = append(candidates, t)
				}
			}
		}
		selected = chooseTab(candidates, len(visible))
		if selected != nil || len(candidates) > 0 || time.Now().After(deadline) {
			break
		}
		if err := r.sleep(ctx, 500*time.Millisecond); err != nil {
			return err
		}
	}
	if selected == nil {
		tabs, _ := r.browser.Tabs(ctx)
		for _, t := range tabs {
			title := t.Title
			if title == "" {
				title = "(无法读取标题)"
			}
			if len([]rune(title)) > 100 {
				title = string([]rune(title)[:100])
			}
			r.log(fmt.Sprintf("受控标签页：%s | %s", title, SafePageAddress(t.URL)))
		}
		if len(candidates) > 0 {
			return fmt.Errorf("找到多个课程列表候选，请只将一个课程列表窗口置于前台")
		}
		return fmt.Errorf("未找到课程设置列表（地址和页面内容均未匹配）。请在受控浏览器中把需要刷课的课程列表标签页置于前台，等待页面加载完成后重试")
	}
	page, err := r.browser.AttachListPage(ctx, selected.ID)
	if err != nil {
		return err
	}
	r.listPage = page
	r.log(fmt.Sprintf("已通过%s识别并接管课程列表；运行期间请勿手动操作受控浏览器", recognition))
	return nil
}

// chooseTab 候选唯一即选中（trial.py find_list_page 的 choose）。
// Python 口径：全部候选只有 1 个→选它；否则在可见候选中唯一才选。
func chooseTab(candidates []TabInfo, _ int) *TabInfo {
	if len(candidates) == 1 {
		c := candidates[0]
		return &c
	}
	visible := make([]TabInfo, 0, len(candidates))
	for _, t := range candidates {
		if t.Visible {
			visible = append(visible, t)
		}
	}
	if len(visible) == 1 {
		c := visible[0]
		return &c
	}
	return nil
}

// --------------------------------------------------------------- 列表加载/筛选/分页

// reloadList 刷新课程列表并解析（trial.py _reload_list：失败截图 + 统一错误）。
func (r *Runner) reloadList(ctx context.Context) (*CoursePage, error) {
	data, err := r.listPage.ReloadList(ctx)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		r.saveDiagnostic(r.listPage, "list")
		return nil, fmt.Errorf("刷新课程列表失败。请确认已登录且位于「课程设置」页：%s", err)
	}
	return data, nil
}

// ResetFilters 清空筛选并回第一页（trial.py reset_filters）。
func (r *Runner) ResetFilters(ctx context.Context) (*CoursePage, error) {
	if _, err := r.reloadList(ctx); err != nil {
		return nil, err
	}
	data, err := r.listPage.ResetFilters(ctx)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, err
	}
	r.log("已清空课程筛选条件并回到第一页")
	return data, nil
}

// gotoPage 跳页续刷（trial.py _goto_page）。
func (r *Runner) gotoPage(ctx context.Context, page int) (*CoursePage, error) {
	data, err := r.listPage.JumpToPage(ctx, page)
	if err != nil {
		return nil, err
	}
	r.log(fmt.Sprintf("刷新后直接跳转到第 %d 页续刷", page))
	return data, nil
}

// --------------------------------------------------------------- 分页扫描

// ScanCourses 扫描全部分页收集未完成课程（trial.py scan_courses）。
// stopFirst=true 时遇到第一门可学课程即返回；startPage>1 先跳页续扫，末页后从第 1 页查缺补漏。
func (r *Runner) ScanCourses(ctx context.Context, stopFirst bool, firstPage *CoursePage, skipIDs map[string]bool, startPage int) (*ScanSummary, error) {
	if startPage < 1 {
		return nil, fmt.Errorf("续刷页码必须大于 0")
	}
	var err error
	page := firstPage
	if page == nil {
		if page, err = r.reloadList(ctx); err != nil {
			return nil, err
		}
	}
	if skipIDs == nil {
		skipIDs = map[string]bool{}
	}
	items, total := page.Items, page.Total
	pageSize := len(items)
	totalPages := 0
	if pageSize > 0 {
		totalPages = (total + pageSize - 1) / pageSize
	}
	if startPage > maxInt(1, totalPages) {
		r.log("续刷页码超出当前总页数，改为从第 1 页扫描")
		startPage = 1
	}
	if startPage > 1 {
		var newPage *CoursePage
		if newPage, err = r.gotoPage(ctx, startPage); err != nil {
			return nil, err
		}
		if newPage.Total != total {
			return nil, fmt.Errorf("跳转续刷页期间课程总数发生变化，请重试")
		}
		items, total = newPage.Items, newPage.Total
	}
	pageNumber := startPage
	seen := (startPage - 1) * pageSize
	studiable, completed := 0, 0
	pending := []CourseEntry{}
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		r.log(fmt.Sprintf("扫描第 %d 页：本页 %d 门，列表总数 %d", pageNumber, len(items), total))
		for index, item := range items {
			if !CourseHasProgress(item) {
				continue
			}
			studiable++
			if CourseIsComplete(item) {
				completed++
				continue
			}
			entry := CourseEntry{
				Item:     item,
				Page:     pageNumber,
				Index:    index,
				PageSize: len(items),
				Reason:   CourseBlockReason(item),
			}
			pending = append(pending, entry)
			if entry.Reason == "" && stopFirst && !skipIDs[entry.ResourceID()] {
				return &ScanSummary{
					Next:      &entry,
					Pending:   pending,
					Total:     total,
					Seen:      seen,
					Studiable: studiable,
					Completed: completed,
				}, nil
			}
		}
		seen += len(items)
		if seen >= total {
			if startPage > 1 {
				r.log("已到末页，开始从第 1 页查缺补漏")
				startPage = 1
				if page, err = r.reloadList(ctx); err != nil {
					return nil, err
				}
				items, total = page.Items, page.Total
				pageSize = len(items)
				pageNumber = 1
				seen, studiable, completed = 0, 0, 0
				pending = []CourseEntry{}
				continue
			}
			break
		}
		if len(items) == 0 {
			return nil, fmt.Errorf("课程列表未到总数就出现空页，停止以避免漏课")
		}
		if pageNumber >= 1000 {
			return nil, fmt.Errorf("课程分页超过 1000 页，疑似分页异常")
		}
		var newPage *CoursePage
		if newPage, err = r.NextPage(ctx, pageNumber+1); err != nil {
			return nil, err
		}
		if newPage.Total != total {
			return nil, fmt.Errorf("翻页期间课程总数发生变化，请停止人工操作后重试")
		}
		pageNumber++
		items = newPage.Items
	}
	return &ScanSummary{
		Pending:   pending,
		Total:     total,
		Seen:      seen,
		Studiable: studiable,
		Completed: completed,
	}, nil
}

// NextPage 下一页并解析（trial.py _next_page，page 为新页码）。
func (r *Runner) NextPage(ctx context.Context, page int) (*CoursePage, error) {
	return r.listPage.NextPage(ctx, page)
}

// ReportScan 输出未完成清单与扫描汇总（trial.py report_scan）。
func (r *Runner) ReportScan(summary *ScanSummary, skipReasons map[string]string) {
	if summary == nil {
		return
	}
	if skipReasons == nil {
		skipReasons = map[string]string{}
	}
	for _, entry := range summary.Pending {
		item := entry.Item
		reason := entry.Reason
		if detail, ok := skipReasons[entry.ResourceID()]; ok {
			if reason != "" {
				reason += "；" + detail
			} else {
				reason = detail
			}
		}
		suffix := ""
		if reason != "" {
			suffix = "；" + reason
		}
		r.log(fmt.Sprintf("未完成：第 %d 页《%s》当前 %s%s",
			entry.Page, entry.Name(), FormatProgress(item), suffix))
	}
	r.log(fmt.Sprintf("扫描汇总：列表 %d 门，纳入进度判断 %d 门，已 100%% %d 门，未完成 %d 门",
		summary.Total, summary.Studiable, summary.Completed, len(summary.Pending)))
}

// --------------------------------------------------------------- 课程处理

// courseRow 防误点：等课程表格行就绪并校验行文本（trial.py _course_row）。
func (r *Runner) courseRow(ctx context.Context, entry CourseEntry) error {
	expected := strings.Join(strings.Fields(strAny(entry.Item["coursename"])), " ")
	rowText, err := r.listPage.WaitRow(ctx, entry.Index, entry.PageSize, expected)
	if err != nil {
		return err
	}
	actual := strings.Join(strings.Fields(rowText), " ")
	if expected != "" && !strings.Contains(actual, expected) {
		return fmt.Errorf("课程行校验失败，已阻止误点其他课程")
	}
	return nil
}

// ProcessCourse 处理一门课（trial.py _process_course：进入学习→弹窗→课件树→逐课件）。
func (r *Runner) ProcessCourse(ctx context.Context, entry CourseEntry) error {
	item := entry.Item
	if err := r.courseRow(ctx, entry); err != nil {
		return err
	}
	mark := r.monitor.Seq()
	popup, err := r.listPage.EnterLearning(ctx, entry.Index)
	if err != nil {
		return err
	}
	r.activePopup = popup
	warePayload, err := r.monitor.WaitSuccess(ctx, popup.ID(), WareListAPI, mark, r.opts.timeout(), "课件清单")
	if err != nil {
		return err
	}
	wares, err := FlattenWares(warePayload)
	if err != nil {
		return err
	}
	expectedID := strAny(item["resourceid"])
	for _, ware := range wares {
		if cid := strAny(ware["courseId"]); cid != "" && cid != expectedID {
			return fmt.Errorf("课件清单与当前课程不一致，已停止处理")
		}
	}
	r.log(fmt.Sprintf("学习页已打开，共 %d 个课件", len(wares)))
	if len(wares) > 1 {
		if err := popup.WaitWareTree(ctx, len(wares)); err != nil {
			return err
		}
	}
	for index, ware := range wares {
		if err := ctx.Err(); err != nil {
			return err
		}
		saveMark := r.monitor.Seq()
		if index > 0 {
			if err := popup.ClickTreeItem(ctx, index); err != nil {
				return err
			}
			if wt := fmt.Sprintf("%v", ware["wareType"]); wt == "1" || wt == "2" {
				if _, err := r.monitor.WaitSuccess(ctx, popup.ID(), LastStudyAPI, saveMark, r.opts.timeout(), "课件切换响应"); err != nil {
					return err
				}
			}
			if err := r.sleep(ctx, 400*time.Millisecond); err != nil {
				return err
			}
		}
		name := wareName(ware, index)
		r.log(fmt.Sprintf("处理课件 %d/%d：《%s》，wareType=%v", index+1, len(wares), name, ware["wareType"]))
		isMP4 := fmt.Sprintf("%v", ware["wareType"]) == "1" && strings.TrimPrefix(strings.ToLower(wareSuffix(ware)), ".") == "mp4"
		if isMP4 {
			if err := r.processVideo(ctx, popup, saveMark); err != nil {
				return err
			}
		} else {
			if err := r.processOther(ctx, popup, saveMark, ware); err != nil {
				return err
			}
		}
		r.log(fmt.Sprintf("课件 %d/%d 页面流程完成", index+1, len(wares)))
	}
	return nil
}

// wareName 课件名（wareName → resName → 「课件 N」；trial.py _process_course 同口径）。
func wareName(ware map[string]any, index int) string {
	for _, key := range []string{"wareName", "resName"} {
		if s := strings.TrimSpace(strAny(ware[key])); s != "" {
			return s
		}
	}
	return fmt.Sprintf("课件 %d", index+1)
}

// wareSuffix 课件后缀（suffixInfo 缺省取 resName/wareName 扩展名）。
func wareSuffix(ware map[string]any) string {
	if s := strAny(ware["suffixInfo"]); s != "" {
		return s
	}
	for _, key := range []string{"resName", "wareName"} {
		if s := strAny(ware[key]); s != "" {
			if i := strings.LastIndex(s, "."); i >= 0 {
				return s[i:]
			}
		}
	}
	return ""
}

// processVideo 视频课件（trial.py _process_video）：拖尾 + 真实 ended + 上报确认。
func (r *Runner) processVideo(ctx context.Context, popup LearnPage, saveMark int) error {
	for waitAttempt := 1; waitAttempt <= 3; waitAttempt++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := r.dragVideoToEnd(ctx, popup); err != nil {
			return err
		}
		err := popup.WaitVideoEnded(ctx, 30*time.Second)
		if err == nil {
			break
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if waitAttempt == 3 {
			return fmt.Errorf("连续 3 轮拖动后，视频均未在 30 秒内播放结束")
		}
		r.log("等待视频结束超过 30 秒，重新拖动进度条")
	}
	r.log("已收到视频真实 ended 事件，等待学习记录上报")
	if _, err := r.monitor.WaitSuccess(ctx, popup.ID(), SaveStudyAPI, saveMark, r.opts.timeout(), "学习记录上报"); err != nil {
		return err
	}
	return r.sleep(ctx, time.Second)
}

// dragVideoToEnd 拖视频进度条到末尾附近（trial.py _drag_video_to_end：最多 3 次拖动）。
func (r *Runner) dragVideoToEnd(ctx context.Context, popup LearnPage) error {
	if err := popup.WaitVideoReady(ctx); err != nil {
		return err
	}
	if err := popup.InstallEndedHook(ctx); err != nil {
		return err
	}
	state, err := popup.VideoState(ctx)
	if err != nil {
		return err
	}
	duration := state.Duration
	target := math.Max(0, duration-math.Min(1.0, math.Max(0.05, duration*0.001)))
	startRatio := math.Min(0.99, math.Max(0, safeDiv(state.Current, duration)))
	remaining := duration
	for attempt := 1; attempt <= 3; attempt++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		if state, err = popup.VideoState(ctx); err != nil {
			return err
		}
		if state.Ended {
			remaining = 0
			break
		}
		if err := popup.DragProgress(ctx, startRatio, target/duration); err != nil {
			return err
		}
		if err := r.sleep(ctx, 400*time.Millisecond); err != nil {
			return err
		}
		if state, err = popup.VideoState(ctx); err != nil {
			return err
		}
		remaining = duration - state.Current
		threshold := math.Max(2.0, math.Min(6.0, duration*0.002))
		if remaining <= threshold {
			r.log(fmt.Sprintf("视频已拖到末尾附近（剩余 %.1f 秒）", math.Max(0, remaining)))
			break
		}
		r.log(fmt.Sprintf("第 %d 次拖动未到达末尾，重试", attempt))
		if attempt == 3 {
			return fmt.Errorf("连续 3 次拖动均未生效，课程可能禁止快进或播放器已改版")
		}
	}
	if state, err = popup.VideoState(ctx); err != nil {
		return err
	}
	if !state.Ended {
		return popup.Play(ctx)
	}
	return nil
}

// processOther 非视频课件（trial.py _process_other）：音频/iframe 预览 + 上报确认。
func (r *Runner) processOther(ctx context.Context, popup LearnPage, saveMark int, ware map[string]any) error {
	wareType := fmt.Sprintf("%v", ware["wareType"])
	switch wareType {
	case "1":
		if err := popup.WaitAudioReady(ctx); err != nil {
			return err
		}
	case "2", "3":
		selector := "iframe"
		if wareType == "3" {
			selector = "#my_iframe"
		}
		if err := popup.WaitIframeLoaded(ctx, selector, r.opts.timeout()); err != nil {
			return err
		}
	default:
		return fmt.Errorf("暂不支持 wareType=%s 的课件加载流程", wareType)
	}
	if _, err := r.monitor.WaitSuccess(ctx, popup.ID(), SaveStudyAPI, saveMark, r.opts.timeout(), "非视频学习记录上报"); err != nil {
		return err
	}
	return r.sleep(ctx, 500*time.Millisecond)
}

// ClosePopup 关闭自己打开的学习弹窗（trial.py close_popup：只关弹窗）。
func (r *Runner) ClosePopup() {
	popup := r.activePopup
	r.activePopup = nil
	if popup == nil || popup.Closed() {
		return
	}
	if err := popup.Close(); err != nil {
		r.log(fmt.Sprintf("关闭学习弹窗时出现警告：%s", trimErr(err)))
	}
}

// saveDiagnostic 失败现场截图（trial.py save_diagnostic：<prefix>_<NN>_<tag>.png 落 OutDir）。
func (r *Runner) saveDiagnostic(page DiagnosticTarget, tag string) {
	if page == nil || page.Closed() {
		return
	}
	r.diagnosticNo++
	name := fmt.Sprintf("%s_%02d_%s.png", diagnosticPrefix(r.opts.DiagnosticPrefix), r.diagnosticNo, tag)
	path := name
	if r.opts.OutDir != "" {
		path = filepath.Join(r.opts.OutDir, name)
	}
	if err := page.Screenshot(path); err != nil {
		r.log(fmt.Sprintf("失败截图保存失败：%s", trimErr(err)))
		return
	}
	r.log(fmt.Sprintf("失败现场截图：%s", path))
}

// DiagnosticTarget 诊断截图目标（ListPage/LearnPage 共用）。
type DiagnosticTarget interface {
	Closed() bool
	Screenshot(path string) error
}

func diagnosticPrefix(p string) string {
	if p == "" {
		return "failure"
	}
	return p
}

// --------------------------------------------------------------- 主循环（续刷/跳过/回滚）

// Run 主循环（trial.py CourseRunner.run）：扫描→逐课处理→3 次跳过→末页复核→汇总。
// checkpoint 原地更新（attempts/last_progress/last_errors/skipped/skip_reasons/resume_page/processed）。
// ctx 取消为暂停：回滚当次尝试、不记失败，返回已积累的 summary 与 ctx 错误。
func (r *Runner) Run(ctx context.Context, firstPage *CoursePage, ckpt *Checkpoint) (*ScanSummary, error) {
	if ckpt == nil {
		ckpt = NewCheckpoint()
	}
	if ckpt.Version == 0 {
		ckpt.Version = CheckpointVersion
	}
	if ckpt.Attempts == nil {
		ckpt.Attempts = map[string]int{}
	}
	if ckpt.LastProgress == nil {
		ckpt.LastProgress = map[string]float64{}
	}
	if ckpt.LastErrors == nil {
		ckpt.LastErrors = map[string]string{}
	}
	if ckpt.SkipReasons == nil {
		ckpt.SkipReasons = map[string]string{}
	}
	skippedIDs := map[string]bool{}
	for _, id := range ckpt.SkippedIDs {
		skippedIDs[id] = true
	}
	processed := ckpt.Processed
	nextPageData := firstPage
	resumePage := ckpt.ResumePage
	if resumePage < 1 {
		resumePage = 1
	}

	skipCourse := func(entry CourseEntry, reason string) {
		id := entry.ResourceID()
		skippedIDs[id] = true
		ckpt.SkipReasons[id] = reason
		ids := make([]string, 0, len(skippedIDs))
		for k := range skippedIDs {
			ids = append(ids, k)
		}
		ckpt.SkippedIDs = ids
		r.log(fmt.Sprintf("跳过课程《%s》：%s；继续处理后续课程", entry.Name(), reason))
	}

	r.reportProgress(processed, 0, "准备扫描课程列表")
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		summary, err := r.ScanCourses(ctx, true, nextPageData, skippedIDs, resumePage)
		nextPageData = nil
		if err != nil {
			return nil, err
		}
		entry := summary.Next
		if entry == nil {
			if len(summary.Pending) > 0 {
				r.log("========== 最终未完成课程 ==========")
				r.ReportScan(summary, ckpt.SkipReasons)
				r.log(fmt.Sprintf("本轮刷课结束：仍有 %d 门课程未达到 100%%，详见上方清单", len(summary.Pending)))
			} else {
				r.ReportScan(summary, nil)
				r.log("全部课程进度均为 100%，刷课完成")
			}
			ckpt.ResumePage = 1
			ckpt.Processed = processed
			r.reportProgress(summary.Completed, summary.Total, "本轮扫描完成")
			return summary, nil
		}
		resumePage = entry.Page
		ckpt.ResumePage = resumePage
		item := entry.Item
		id := entry.ResourceID()
		name := entry.Name()
		progress, hasProgress := CourseProgress(item)
		attempt := ckpt.Attempts[id] + 1
		if attempt > r.opts.maxAttempts() {
			reason := fmt.Sprintf("连续 %d 次处理后仍为 %s", r.opts.maxAttempts(), FormatProgress(item))
			if lastErr, ok := ckpt.LastErrors[id]; ok {
				reason += "；最后错误：" + lastErr
			}
			skipCourse(*entry, reason)
			continue
		}
		ckpt.Attempts[id] = attempt
		if prev, ok := ckpt.LastProgress[id]; ok && hasProgress {
			if progress <= prev {
				r.log(fmt.Sprintf("注意：《%s》上次处理后进度未提升", name))
			}
		}
		if hasProgress {
			ckpt.LastProgress[id] = progress
		}
		r.log(fmt.Sprintf("开始课程《%s》（第 %d 页，当前 %s，第 %d/%d 次尝试）",
			name, entry.Page, FormatProgress(item), attempt, r.opts.maxAttempts()))
		r.reportProgress(processed, 0, fmt.Sprintf("第 %d 页 · %s", entry.Page, name))
		err = r.ProcessCourse(ctx, *entry)
		if err != nil {
			if ctx.Err() != nil {
				// 暂停回滚当次尝试、不记失败（trial.py interrupt 分支）
				ckpt.Attempts[id] = maxInt(0, attempt-1)
				return summary, err
			}
			ckpt.LastErrors[id] = trimErr(err)
			target := DiagnosticTarget(r.activePopup)
			if target == nil {
				target = r.listPage
			}
			r.saveDiagnostic(target, "course")
			r.log(fmt.Sprintf("课程《%s》本次处理失败：%s", name, err))
			r.ClosePopup()
			if attempt >= r.opts.maxAttempts() {
				skipCourse(*entry, fmt.Sprintf("连续 %d 次处理失败；最后错误：%s", r.opts.maxAttempts(), ckpt.LastErrors[id]))
			}
			continue
		}
		r.ClosePopup()
		processed++
		ckpt.Processed = processed
		r.reportProgress(processed, 0, fmt.Sprintf("页面流程完成 · %s", name))
		if err := r.sleep(ctx, 1500*time.Millisecond); err != nil {
			return summary, err
		}
	}
}

// --------------------------------------------------------------- 小工具

func minDuration(a, b time.Duration) time.Duration {
	if a < b {
		return a
	}
	return b
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func safeDiv(a, b float64) float64 {
	if b <= 0 {
		return 0
	}
	return a / b
}

// trimErr 错误文本截断（trial.py str(exc)[:300]）。
func trimErr(err error) string {
	if err == nil {
		return ""
	}
	s := err.Error()
	if len([]rune(s)) > 300 {
		return string([]rune(s)[:300])
	}
	return s
}
