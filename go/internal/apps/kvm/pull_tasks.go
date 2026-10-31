// 网盘 → 设备后台传输任务：POST 只负责入队并立即返回，任务在独立 goroutine 内运行，
// 不绑定 HTTP 请求生命周期——用户关掉 Web/小程序后仍继续推送，回来可查任务状态。
//
// 任务为进程内存储（不落库）：服务重启会丢失，终态任务保留 pullTaskTTL 便于回看；
// 同一设备串行、全局最多 pullTaskWorkers 个任务并发，避免多批同时压设备。
package kvm

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"sort"
	"sync"
	"time"

	"shade/internal/auth"
)

const (
	pullTaskQueued  = "queued"
	pullTaskRunning = "running"
	pullTaskDone    = "done"
	pullTaskPartial = "partial"
	pullTaskFailed  = "failed"

	pullTaskWorkers = 3              // 全局并发任务数
	pullTaskTimeout = 2 * time.Hour  // 单任务总时长上限（覆盖大批次/大文件慢链路）
	pullTaskTTL     = 24 * time.Hour // 终态任务保留时长（供退出后回来查看）
	pullTaskMax     = 200            // 进程内任务上限（防内存无界增长）
	pullTaskListMax = 20             // 列表接口单次返回上限
)

// pullTaskItem 单文件结果（进度期间 items 只追加已完成/已失败的条目）。
type pullTaskItem struct {
	Name  string `json:"name"`
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
}

// pullTask 一个后台传输任务。字段由 mu 保护；CreatedAt/ID 等不可变字段在发布后只读。
type pullTask struct {
	mu sync.Mutex

	id         string
	userID     int64
	deviceID   int64
	deviceName string
	space      string

	state      string
	total      int
	done       int
	failed     int
	current    string
	totalBytes int64
	doneBytes  int64
	items      []pullTaskItem
	err        string

	createdAt  time.Time
	startedAt  time.Time
	finishedAt time.Time
}

// pullTaskSnapshot 对外快照（列表/创建响应）。
type pullTaskSnapshot struct {
	ID         string         `json:"id"`
	DeviceID   int64          `json:"device_id"`
	DeviceName string         `json:"device_name"`
	Space      string         `json:"space"`
	State      string         `json:"state"`
	Total      int            `json:"total"`
	Done       int            `json:"done"`
	Failed     int            `json:"failed"`
	Current    string         `json:"current,omitempty"`
	TotalBytes int64          `json:"total_bytes"`
	DoneBytes  int64          `json:"done_bytes"`
	Items      []pullTaskItem `json:"items"`
	Error      string         `json:"error,omitempty"`
	CreatedAt  string         `json:"created_at"`
	StartedAt  string         `json:"started_at,omitempty"`
	FinishedAt string         `json:"finished_at,omitempty"`
}

func (t *pullTask) terminalLocked() bool {
	return t.state == pullTaskDone || t.state == pullTaskPartial || t.state == pullTaskFailed
}

func (t *pullTask) expired(now time.Time) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.terminalLocked() && !t.finishedAt.IsZero() && now.Sub(t.finishedAt) > pullTaskTTL
}

func (t *pullTask) markRunning() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.state != pullTaskQueued {
		return
	}
	t.state = pullTaskRunning
	t.startedAt = time.Now()
}

func (t *pullTask) setCurrent(name string) {
	t.mu.Lock()
	t.current = name
	t.mu.Unlock()
}

func (t *pullTask) addTotalBytes(n int64) {
	if n <= 0 {
		return
	}
	t.mu.Lock()
	t.totalBytes += n
	t.mu.Unlock()
}

func (t *pullTask) addBytes(n int64) {
	if n <= 0 {
		return
	}
	t.mu.Lock()
	t.doneBytes += n
	t.mu.Unlock()
}

func (t *pullTask) addItem(it pullTaskItem) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if it.OK {
		t.done++
	} else {
		t.failed++
	}
	t.items = append(t.items, it)
}

// finish 收敛终态；cause 非空表示任务级失败（超时/取消/内部异常），单文件失败走 items。
func (t *pullTask) finish(cause error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.terminalLocked() {
		return
	}
	t.finishedAt = time.Now()
	t.current = ""
	if cause != nil {
		t.state = pullTaskFailed
		switch {
		case errors.Is(cause, context.DeadlineExceeded):
			t.err = "传输超时"
		case errors.Is(cause, context.Canceled):
			t.err = "任务已取消"
		default:
			t.err = cause.Error()
		}
		return
	}
	switch {
	case t.done+t.failed < t.total:
		t.state = pullTaskFailed
		t.err = "任务未完成"
	case t.total > 0 && t.failed == t.total:
		t.state = pullTaskFailed
		t.err = "全部文件推送失败"
	case t.failed > 0:
		t.state = pullTaskPartial
	default:
		t.state = pullTaskDone
	}
}

func (t *pullTask) snapshot() pullTaskSnapshot {
	t.mu.Lock()
	defer t.mu.Unlock()
	snap := pullTaskSnapshot{
		ID:         t.id,
		DeviceID:   t.deviceID,
		DeviceName: t.deviceName,
		Space:      t.space,
		State:      t.state,
		Total:      t.total,
		Done:       t.done,
		Failed:     t.failed,
		Current:    t.current,
		TotalBytes: t.totalBytes,
		DoneBytes:  t.doneBytes,
		Items:      append([]pullTaskItem{}, t.items...),
		Error:      t.err,
		CreatedAt:  t.createdAt.Format(time.RFC3339),
	}
	if !t.startedAt.IsZero() {
		snap.StartedAt = t.startedAt.Format(time.RFC3339)
	}
	if !t.finishedAt.IsZero() {
		snap.FinishedAt = t.finishedAt.Format(time.RFC3339)
	}
	return snap
}

// pullTaskManager 任务表 + 并发闸 + 设备串行闸。
type pullTaskManager struct {
	mu    sync.Mutex
	seq   int64
	tasks map[string]*pullTask
	gates map[int64]chan struct{}
	sem   chan struct{}
}

var (
	errPullTaskNotFound = errors.New("传输任务不存在")
	errPullTaskRunning  = errors.New("传输任务尚未结束，暂不能删除")
)

func (m *pullTaskManager) delete(userID, deviceID int64, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	t, ok := m.tasks[id]
	if !ok || t.userID != userID || t.deviceID != deviceID {
		return errPullTaskNotFound
	}
	t.mu.Lock()
	terminal := t.terminalLocked()
	t.mu.Unlock()
	if !terminal {
		return errPullTaskRunning
	}
	delete(m.tasks, id)
	return nil
}

func newPullTaskManager() *pullTaskManager {
	return &pullTaskManager{
		tasks: map[string]*pullTask{},
		gates: map[int64]chan struct{}{},
		sem:   make(chan struct{}, pullTaskWorkers),
	}
}

func (m *pullTaskManager) newTask(ident auth.Identity, dev Device, deviceName, space string, paths []string) *pullTask {
	m.mu.Lock()
	m.seq++
	seq := m.seq
	m.mu.Unlock()
	return &pullTask{
		id:         fmt.Sprintf("pt-%d-%d", time.Now().UnixNano(), seq),
		userID:     ident.ID,
		deviceID:   dev.ID,
		deviceName: deviceName,
		space:      space,
		state:      pullTaskQueued,
		total:      len(paths),
		createdAt:  time.Now(),
	}
}

func (m *pullTaskManager) gate(deviceID int64) chan struct{} {
	m.mu.Lock()
	defer m.mu.Unlock()
	g := m.gates[deviceID]
	if g == nil {
		g = make(chan struct{}, 1)
		m.gates[deviceID] = g
	}
	return g
}

// enqueue 登记任务并异步启动；run 返回任务级错误（单文件失败由 run 内记录）。
// base 仅用于继承 values（鉴权/链路标记），取消信号被 WithoutCancel 剥离。
func (m *pullTaskManager) enqueue(t *pullTask, base context.Context, run func(context.Context) error) error {
	m.mu.Lock()
	m.gcLocked(time.Now())
	if len(m.tasks) >= pullTaskMax {
		m.mu.Unlock()
		return errors.New("后台传输任务过多，请稍后再试")
	}
	m.tasks[t.id] = t
	m.mu.Unlock()

	go func() {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(base), pullTaskTimeout)
		defer cancel()

		gate := m.gate(t.deviceID)
		if err := acquire(ctx, gate); err != nil {
			t.finish(err)
			return
		}
		defer func() { <-gate }()

		if err := acquire(ctx, m.sem); err != nil {
			t.finish(err)
			return
		}
		defer func() { <-m.sem }()

		t.markRunning()
		var cause error
		defer func() {
			if r := recover(); r != nil {
				cause = fmt.Errorf("内部错误：%v", r)
			}
			t.finish(cause)
		}()
		cause = run(ctx)
	}()
	return nil
}

func acquire(ctx context.Context, ch chan struct{}) error {
	select {
	case ch <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// list 返回当前用户在指定设备上的任务（新的在前），供退出后回来查看。
func (m *pullTaskManager) list(userID, deviceID int64) []pullTaskSnapshot {
	m.mu.Lock()
	m.gcLocked(time.Now())
	tasks := make([]*pullTask, 0, len(m.tasks))
	for _, t := range m.tasks {
		if t.userID == userID && t.deviceID == deviceID {
			tasks = append(tasks, t)
		}
	}
	m.mu.Unlock()

	sort.Slice(tasks, func(i, j int) bool { return tasks[i].createdAt.After(tasks[j].createdAt) })
	if len(tasks) > pullTaskListMax {
		tasks = tasks[:pullTaskListMax]
	}
	out := make([]pullTaskSnapshot, 0, len(tasks))
	for _, t := range tasks {
		out = append(out, t.snapshot())
	}
	return out
}

// gcLocked 清理过期终态任务；调用方持 m.mu。任务自身 mu 只做短临界区，不会与 m.mu 反向加锁。
func (m *pullTaskManager) gcLocked(now time.Time) {
	for id, t := range m.tasks {
		if t.expired(now) {
			delete(m.tasks, id)
		}
	}
}

// runPullTask 逐文件 网盘拉流 → 设备推送；单文件失败继续，任务级错误仅超时/取消。
func (m *Module) runPullTask(ctx context.Context, t *pullTask, fs fileshare, ident auth.Identity, space string, paths []string) error {
	for _, p := range paths {
		if err := ctx.Err(); err != nil {
			return err
		}
		rel, valid := normRel(p)
		name := lastSegment(p)
		if !valid || rel == "/" {
			t.addItem(pullTaskItem{Name: name, OK: false, Error: "路径不合法"})
			continue
		}
		t.setCurrent(name)

		if sb, ok := m.Netdisk.(NetdiskStreamBridge); ok {
			rc, size, err := sb.DownloadStream(ctx, ident, space, rel)
			if err != nil || size == 0 {
				if rc != nil {
					_ = rc.Close()
				}
				reason := "网盘侧文件拉取为空"
				if err != nil {
					reason = err.Error()
				}
				t.addItem(pullTaskItem{Name: name, OK: false, Error: truncate(reason, 120)})
				continue
			}
			t.addTotalBytes(size)
			pushErr := pushOneFile(ctx, fs, name, &pullTaskReader{r: rc, t: t})
			if closeErr := rc.Close(); pushErr == nil && closeErr != nil {
				pushErr = closeErr
			}
			if pushErr != nil {
				t.addItem(pullTaskItem{Name: name, OK: false, Error: truncate(pushErr.Error(), 120)})
				continue
			}
			t.addItem(pullTaskItem{Name: name, OK: true})
			continue
		}

		data, err := m.Netdisk.Download(ctx, ident, space, rel)
		if err != nil || len(data) == 0 {
			reason := "网盘侧文件拉取为空"
			if err != nil {
				reason = err.Error()
			}
			t.addItem(pullTaskItem{Name: name, OK: false, Error: truncate(reason, 120)})
			continue
		}
		t.addTotalBytes(int64(len(data)))
		if err := pushOneFile(ctx, fs, name, &pullTaskReader{r: bytes.NewReader(data), t: t}); err != nil {
			t.addItem(pullTaskItem{Name: name, OK: false, Error: truncate(err.Error(), 120)})
			continue
		}
		t.addItem(pullTaskItem{Name: name, OK: true})
	}
	return nil
}

// pullTaskReader 统计实际读出的文件字节数（设备侧读多少算多少，失败时保留部分进度）。
type pullTaskReader struct {
	r io.Reader
	t *pullTask
}

func (r *pullTaskReader) Read(p []byte) (int, error) {
	n, err := r.r.Read(p)
	if n > 0 {
		r.t.addBytes(int64(n))
	}
	return n, err
}

// deviceDisplayName 设备展示名（平台 description/name 优先，缺省 ddns）。
func deviceDisplayName(d Device) string {
	for _, key := range []string{"description", "name"} {
		if v, ok := d.Raw[key].(string); ok && v != "" {
			return v
		}
	}
	if d.Ddns != "" {
		return d.Ddns
	}
	return "设备"
}
