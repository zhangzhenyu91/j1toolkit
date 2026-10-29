// Package job 后台任务模型（移植自 toolkit/jobs.py）：所有耗时操作统一走 Job。
//
//   - Job 在 goroutine 中执行，日志/进度经 OnLog / OnProgress 回调实时桥接（RPC 事件推送）
//   - 协作式取消：工作单元之间调用 job.CheckCancel()；长阻塞操作（CDP 渲染等）由业务
//     用 job.AddCancelHook() 注册「取消即销毁阻塞源」的钩子，实现即刻中断
//   - 结束后写任务档案（state/jobs/<id>.json）并追加应用历史（state/history/<app_id>.jsonl），
//     随即释放内存日志缓冲；终态 Job 在内存中只保留最近 50 个（档案/日志文件不受影响）
//
// 新应用接入时直接用 JobManager 包裹业务函数即可获得进度/日志/取消/历史能力。
package job

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime/debug"
	"sync"
	"time"

	"github.com/zhangzhenyu91/j1toolkit/uvmp-toolkit/core-go/internal/state"
)

// ErrCancelled Job.CheckCancel 抛出，表示用户请求取消。
var ErrCancelled = errors.New("任务已取消")

// 终态状态集合（Job 终态后不再变化）。
const (
	StatusPending   = "pending"
	StatusRunning   = "running"
	StatusSuccess   = "success"
	StatusFailed    = "failed"
	StatusCancelled = "cancelled"
)

// TerminalStatus 终态状态名集合。
var TerminalStatus = map[string]bool{
	StatusSuccess:   true,
	StatusFailed:    true,
	StatusCancelled: true,
}

// logEntry 内存环形日志的一条。
type logEntry struct {
	seq  int
	line string
}

// Job 一个后台任务（业务侧 Log/SetProgress/CheckCancel，查询侧 LogsSince/ToDict）。
type Job struct {
	ID     string
	AppID  string
	Title  string
	Status string

	// Echo CLI 模式置 true，日志同步打印到 stdout。
	Echo bool
	// OnLog GUI/RPC 桥接回调：fn(line)。
	OnLog func(line string)
	// OnProgress GUI/RPC 桥接回调：fn(current, total, label)。
	OnProgress func(current, total int, label string)
	// OnDone 终态回调：fn(job)，在状态落定后由 JobManager 调用。
	OnDone func(j *Job)

	// CreatedAt / FinishedAt "YYYY-MM-DD HH:MM:SS"。
	CreatedAt  string
	FinishedAt string
	// Result 业务自定义摘要（产出文件、数量等）。
	Result map[string]any
	Error  string

	mu          sync.Mutex
	progress    map[string]any
	logs        []logEntry // 环形缓冲，最多 1000 条
	seq         int
	cancelFlag  bool
	cancelHooks []func()
	logPath     string
	logFile     *os.File
}

// newJob 创建 Job 并预创建日志文件（logs/<id>.log，与 Python 同为创建即存在；
// 写句柄在首条日志时打开、终态收尾关闭，避免未运行任务泄漏句柄）。
func newJob(id, appID, title string, st *state.Store) *Job {
	j := &Job{
		ID:        id,
		AppID:     appID,
		Title:     title,
		Status:    StatusPending,
		CreatedAt: time.Now().Format("2006-01-02 15:04:05"),
		Result:    map[string]any{},
		progress:  map[string]any{"current": 0, "total": 0, "label": ""},
	}
	j.logPath = filepath.Join(st.LogsDir(), id+".log")
	if fp, err := os.OpenFile(j.logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644); err == nil {
		_ = fp.Close()
	}
	return j
}

// openLogLocked 懒打开日志写句柄（需持 j.mu）。
func (j *Job) openLogLocked() {
	if j.logFile == nil && j.logPath != "" {
		j.logFile, _ = os.OpenFile(j.logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	}
}

// ---- 业务侧接口 ----

// Log 记一行日志（"[HH:MM:SS] msg"），写日志文件 + 环形缓冲 + 回调。
func (j *Job) Log(msg string) {
	line := fmt.Sprintf("[%s] %s", time.Now().Format("15:04:05"), msg)
	j.mu.Lock()
	j.seq++
	j.logs = append(j.logs, logEntry{seq: j.seq, line: line})
	if len(j.logs) > 1000 {
		j.logs = j.logs[len(j.logs)-1000:]
	}
	j.openLogLocked()
	if j.logFile != nil {
		_, _ = j.logFile.WriteString(line + "\n")
		_ = j.logFile.Sync()
	}
	j.mu.Unlock()
	if j.Echo {
		fmt.Println(line)
	}
	if cb := j.OnLog; cb != nil {
		func() {
			defer func() { _ = recover() }()
			cb(line)
		}()
	}
}

// SetProgress 更新进度（current/total/label）并触发回调。
func (j *Job) SetProgress(current, total int, label string) {
	j.mu.Lock()
	j.progress = map[string]any{"current": current, "total": total, "label": label}
	j.mu.Unlock()
	if cb := j.OnProgress; cb != nil {
		func() {
			defer func() { _ = recover() }()
			cb(current, total, label)
		}()
	}
}

// Cancelled 是否已请求取消。
func (j *Job) Cancelled() bool {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.cancelFlag
}

// Cancel 请求取消并触发全部取消钩子（销毁阻塞源）。
func (j *Job) Cancel() {
	j.mu.Lock()
	j.cancelFlag = true
	hooks := append([]func(){}, j.cancelHooks...)
	j.mu.Unlock()
	for _, fn := range hooks {
		func() {
			defer func() { _ = recover() }()
			fn()
		}()
	}
}

// AddCancelHook 注册「取消即触发」的钩子：协作式 CheckCancel 要等当前工作单元结束，
// 而长阻塞操作（CDP 渲染等待等）需要即时中断——钩子负责销毁阻塞源
// （如杀掉渲染浏览器进程，在途 CDP 调用立即报错退出）。业务在 Run() 里注册。
// 注册前已取消：立即补触发。
func (j *Job) AddCancelHook(fn func()) {
	j.mu.Lock()
	j.cancelHooks = append(j.cancelHooks, fn)
	already := j.cancelFlag
	j.mu.Unlock()
	if already {
		func() {
			defer func() { _ = recover() }()
			fn()
		}()
	}
}

// CheckCancel 已取消时返回 ErrCancelled（业务以 error 传播即可）。
func (j *Job) CheckCancel() error {
	if j.Cancelled() {
		return ErrCancelled
	}
	return nil
}

// ---- 查询侧接口 ----

// LogsSince 返回 offset 之后的 [[seq, line], ...] 与当前最大 seq。
func (j *Job) LogsSince(offset int) (lines [][2]any, maxSeq int) {
	j.mu.Lock()
	defer j.mu.Unlock()
	for _, e := range j.logs {
		if e.seq > offset {
			lines = append(lines, [2]any{e.seq, e.line})
		}
	}
	return lines, j.seq
}

// Progress 当前进度快照。
func (j *Job) Progress() map[string]any {
	j.mu.Lock()
	defer j.mu.Unlock()
	out := map[string]any{}
	for k, v := range j.progress {
		out[k] = v
	}
	return out
}

// ToDict 任务摘要（RPC getJob / job.done 事件的 job 字段形状）。
func (j *Job) ToDict() map[string]any {
	j.mu.Lock()
	defer j.mu.Unlock()
	return map[string]any{
		"id":          j.ID,
		"app_id":      j.AppID,
		"title":       j.Title,
		"status":      j.Status,
		"progress":    map[string]any{"current": j.progress["current"], "total": j.progress["total"], "label": j.progress["label"]},
		"created_at":  j.CreatedAt,
		"finished_at": j.FinishedAt,
		"result":      j.Result,
		"error":       j.Error,
	}
}

// ---------------------------------------------------------------- JobManager

// MaxTerminalJobs 终态 Job 在内存中只保留最近这么多个（超出即回收对象，
// 档案/日志文件不受影响）。
const MaxTerminalJobs = 50

// JobManager 任务工厂 + 注册表。
type JobManager struct {
	mu    sync.Mutex
	jobs  map[string]*Job
	seq   int
	store *state.Store
	now   func() time.Time
}

// NewManager 创建 JobManager；store 为 nil 用默认状态目录。
func NewManager(store *state.Store) *JobManager {
	if store == nil {
		store = state.Default()
	}
	return &JobManager{jobs: map[string]*Job{}, store: store, now: time.Now}
}

// Store 绑定的状态目录。
func (m *JobManager) Store() *state.Store { return m.store }

// Create 新建任务（job_id = YYYYMMDDHHMMSS-%02d，秒内序号取模 100，与 Python 同格式）。
func (m *JobManager) Create(appID, title string) *Job {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.seq++
	id := fmt.Sprintf("%s-%02d", m.now().Format("20060102150405"), m.seq%100)
	j := newJob(id, appID, title, m.store)
	m.jobs[id] = j
	return j
}

// Get 按 job_id 取任务（无则 nil）。
func (m *JobManager) Get(id string) *Job {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.jobs[id]
}

// Count 内存中任务数（ping 用）。
func (m *JobManager) Count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.jobs)
}

// HasActive 该应用是否有 pending/running 任务。
func (m *JobManager) HasActive(appID string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, j := range m.jobs {
		if j.AppID == appID && (j.Status == StatusPending || j.Status == StatusRunning) {
			return true
		}
	}
	return false
}

// List 任务摘要列表（新→旧，appID 空=全部，最多 limit 条）。
func (m *JobManager) List(appID string, limit int) []map[string]any {
	m.mu.Lock()
	js := make([]*Job, 0, len(m.jobs))
	for _, j := range m.jobs {
		js = append(js, j)
	}
	m.mu.Unlock()
	// 按 id 降序（id 含时间戳，字典序即时间序）
	for i := 0; i < len(js); i++ {
		for k := i + 1; k < len(js); k++ {
			if js[k].ID > js[i].ID {
				js[i], js[k] = js[k], js[i]
			}
		}
	}
	out := []map[string]any{}
	for _, j := range js {
		if appID != "" && j.AppID != appID {
			continue
		}
		out = append(out, j.ToDict())
		if len(out) >= limit {
			break
		}
	}
	return out
}

// pruneTerminal 终态 Job 只保留最近 MaxTerminalJobs 个（按 job_id 降序）；需持 m.mu 调用。
// 只回收内存对象——任务档案（state/jobs/）与日志文件（state/logs/）不受影响。
func (m *JobManager) pruneTerminal() {
	done := []*Job{}
	for _, j := range m.jobs {
		if TerminalStatus[j.Status] {
			done = append(done, j)
		}
	}
	for i := 0; i < len(done); i++ {
		for k := i + 1; k < len(done); k++ {
			if done[k].ID > done[i].ID {
				done[i], done[k] = done[k], done[i]
			}
		}
	}
	if len(done) > MaxTerminalJobs {
		for _, j := range done[MaxTerminalJobs:] {
			delete(m.jobs, j.ID)
		}
	}
}

// finalize 终态收尾：关日志文件、写任务档案、追加历史、释放内存日志缓冲。
func (m *JobManager) finalize(j *Job) {
	j.mu.Lock()
	j.FinishedAt = time.Now().Format("2006-01-02 15:04:05")
	if j.logFile != nil {
		_ = j.logFile.Close()
		j.logFile = nil
	}
	summary := j.toDictLocked()
	// 摘要附最后 50 行日志（log_tail）
	tail := []any{}
	start := 0
	if len(j.logs) > 50 {
		start = len(j.logs) - 50
	}
	for _, e := range j.logs[start:] {
		tail = append(tail, e.line)
	}
	summary["log_tail"] = tail
	j.mu.Unlock()

	_ = state.WriteJSON(filepath.Join(m.store.JobsDir(), j.ID+".json"), summary)
	m.store.AppendHistory(j.AppID, map[string]any{
		"job_id": j.ID,
		"title":  j.Title,
		"status": j.Status,
		"time":   j.FinishedAt,
		"result": j.Result,
		"error":  j.Error,
	})
	m.mu.Lock()
	j.mu.Lock()
	j.logs = nil // 释放完成日志缓冲（全文已在 logs/<id>.log，摘要已入档案）
	j.mu.Unlock()
	m.pruneTerminal()
	m.mu.Unlock()
}

// toDictLocked ToDict 的无锁版（finalize 内部用）。
func (j *Job) toDictLocked() map[string]any {
	return map[string]any{
		"id":          j.ID,
		"app_id":      j.AppID,
		"title":       j.Title,
		"status":      j.Status,
		"progress":    map[string]any{"current": j.progress["current"], "total": j.progress["total"], "label": j.progress["label"]},
		"created_at":  j.CreatedAt,
		"finished_at": j.FinishedAt,
		"result":      j.Result,
		"error":       j.Error,
	}
}

// runner 执行体：状态推进 + 异常归类 + 终态收尾 + OnDone。
func (m *JobManager) runner(j *Job, fn func(*Job) (map[string]any, error)) {
	j.mu.Lock()
	j.Status = StatusRunning
	j.mu.Unlock()
	func() {
		defer func() {
			if r := recover(); r != nil {
				j.mu.Lock()
				j.Status = StatusFailed
				j.Error = fmt.Sprintf("%v", r)
				j.mu.Unlock()
				j.Log(fmt.Sprintf("任务失败: %v", r))
				j.Log(string(debug.Stack()))
			}
		}()
		result, err := fn(j)
		j.mu.Lock()
		if result != nil {
			j.Result = result
		}
		switch {
		case err != nil && errors.Is(err, ErrCancelled):
			j.Status = StatusCancelled
		case j.cancelFlag:
			j.Status = StatusCancelled
		case err != nil:
			j.Status = StatusFailed
			j.Error = err.Error()
		default:
			j.Status = StatusSuccess
		}
		failed := j.Status == StatusFailed
		cancelled := j.Status == StatusCancelled
		errMsg := j.Error
		j.mu.Unlock()
		if failed {
			j.Log("任务失败: " + errMsg)
			j.Log(string(debug.Stack()))
		}
		if cancelled {
			j.Log("任务已取消")
		}
	}()
	m.finalize(j)
	if cb := j.OnDone; cb != nil {
		func() {
			defer func() { _ = recover() }()
			cb(j)
		}()
	}
}

// Start 后台 goroutine 执行（GUI/RPC 模式）。
func (m *JobManager) Start(j *Job, fn func(*Job) (map[string]any, error)) *Job {
	go m.runner(j, fn)
	return j
}

// RunSync 当前 goroutine 执行（CLI/定时器模式），echo=true 时日志同步到 stdout。
func (m *JobManager) RunSync(j *Job, fn func(*Job) (map[string]any, error), echo bool) *Job {
	j.Echo = echo
	m.runner(j, fn)
	return j
}
