// 照片操作任务（PhotoOp）：上传/改人名/删除与商旅远端同步的任务化执行。
// 同步校验通过后登记任务立即返回 opId，后台按并发闸门（跨卡片并行、同卡片互斥）执行；
// 端侧凭 GetActive / Status 渲染进度。
package sgccclockin

import (
	"context"
	"fmt"
	"log"
	"strconv"
	"sync"
	"time"
)

// PhotoOp 一次照片操作任务。
type PhotoOp struct {
	ID         int64          `json:"opId"`
	Kind       string         `json:"kind"` // upload / members / delete
	TeamID     int64          `json:"team_id"`
	EntryID    int64          `json:"entry_id"`
	PhotoID    int64          `json:"photo_id,omitempty"`
	Total      int            `json:"total"`    // 进度总单元（Node rec.total；未细分时按 0/0 呈现）
	Done       int            `json:"done"`     // 已完成单元（Node rec.done）
	Progress   int            `json:"progress"` // 0~100
	Step       string         `json:"step"`
	Failed     bool           `json:"failed"`
	FailedName string         `json:"failedName,omitempty"`
	Error      string         `json:"error,omitempty"`
	PhotoIDOut int64          `json:"photoId,omitempty"` // 任务收尾落库照片 id
	PhotoURL   string         `json:"photoUrl,omitempty"`
	Extra      map[string]any `json:"extra,omitempty"`
	CreatedAt  time.Time      `json:"created_at"`
	done       bool
}

// PhotoOpManager 任务管理器（并发安全；runners 异步执行）。
type PhotoOpManager struct {
	mu  sync.Mutex
	seq int64
	ops map[int64]*PhotoOp
	// activeLock 卡片级互斥：同卡片同时只允许一个进行中任务（对齐 activePhotoOp）。
	activeLock map[string]int64
	// gate 跨卡片并发闸门：不同卡片可并行（默认 2，收敛 1–4），同卡片仍由 activeLock 串行。
	gate gate
	// Limit 并发上限来源（按运行时配置现读；nil 用默认 2）。
	Limit func() int
}

// NewPhotoOpManager 构造任务管理器。
func NewPhotoOpManager() *PhotoOpManager {
	return &PhotoOpManager{
		ops:        map[int64]*PhotoOp{},
		activeLock: map[string]int64{},
	}
}

// limit 当前并发上限（未注入时用默认值，并收敛到允许区间）。
func (m *PhotoOpManager) limit() int {
	v := defaultPhotoConcurrency
	if m.Limit != nil {
		v = m.Limit()
	}
	return clampInt(v, 1, maxPhotoConcurrency)
}

func opKey(teamID, entryID int64) string {
	return "t" + strconv.FormatInt(teamID, 10) + ":e" + strconv.FormatInt(entryID, 10)
}

// ActivePhotoOp 查询卡片是否有进行中的照片任务。
func (m *PhotoOpManager) ActivePhotoOp(teamID, entryID int64) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.activeLock[opKey(teamID, entryID)]
	return ok
}

// New 登记新任务（卡片已有进行中任务时返回 nil）。
func (m *PhotoOpManager) New(kind string, teamID, entryID, photoID int64) *PhotoOp {
	m.mu.Lock()
	defer m.mu.Unlock()
	key := opKey(teamID, entryID)
	if _, busy := m.activeLock[key]; busy {
		return nil
	}
	m.seq++
	op := &PhotoOp{
		ID: m.seq, Kind: kind, TeamID: teamID, EntryID: entryID, PhotoID: photoID,
		Step: "queued", CreatedAt: time.Now(),
	}
	m.ops[op.ID] = op
	m.activeLock[key] = op.ID
	return op
}

// Status 查询任务状态（含保留的历史任务）。
func (m *PhotoOpManager) Status(id int64) (*PhotoOp, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	op, ok := m.ops[id]
	if !ok {
		return nil, false
	}
	cp := *op
	return &cp, true
}

// GetActive 返回全部进行中任务（端侧 /sync/active 的 ops 渲染用）。
func (m *PhotoOpManager) GetActive() []*PhotoOp {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]*PhotoOp, 0, len(m.activeLock))
	for _, id := range m.activeLock {
		if op, ok := m.ops[id]; ok {
			cp := *op
			out = append(out, &cp)
		}
	}
	return out
}

// Run 执行任务体（同步执行 fn；fn 返回错误或 panic 时任务置失败；无论成败收尾解锁卡片）。
// panic 必须在此收敛：任务跑在后台 goroutine 上，抛出会带崩整个服务进程且卡片永久锁死。
func (m *PhotoOpManager) Run(id int64, fn func(op *PhotoOp) error) {
	m.mu.Lock()
	op, ok := m.ops[id]
	if !ok {
		m.mu.Unlock()
		return
	}
	op.Step = "running"
	m.mu.Unlock()

	err := func() (err error) {
		defer func() {
			if rec := recover(); rec != nil {
				log.Printf("[商旅打卡] 照片任务 %d 异常终止：panic %v", id, rec)
				err = fmt.Errorf("任务异常终止：%v", rec)
			}
		}()
		return fn(op)
	}()

	m.mu.Lock()
	defer m.mu.Unlock()
	if err != nil {
		op.Failed = true
		op.Error = err.Error()
		op.Step = "failed"
	} else {
		op.Progress = 100
		op.Step = "done"
	}
	op.done = true
	key := opKey(op.TeamID, op.EntryID)
	if cur, ok := m.activeLock[key]; ok && cur == id {
		delete(m.activeLock, key)
	}
}

// SetProgress 更新进度（0~100）。
func (m *PhotoOpManager) SetProgress(id int64, progress int, step string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if op, ok := m.ops[id]; ok {
		op.Progress = progress
		if step != "" {
			op.Step = step
		}
	}
}

// SetCounts 更新进度计数（done/total，Node rec.prog 口径；端侧 /op/status 按 done/total 渲染）。
func (m *PhotoOpManager) SetCounts(id int64, done, total int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if op, ok := m.ops[id]; ok {
		op.Done, op.Total = done, total
	}
}

// SetFailedName 记录远端失败的成员姓名（任务执行期回调；并发安全）。
func (m *PhotoOpManager) SetFailedName(id int64, name string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if op, ok := m.ops[id]; ok {
		op.FailedName = name
	}
}

// SetPhoto 记录任务收尾产物照片（小程序任务收尾存相册用；并发安全）。
func (m *PhotoOpManager) SetPhoto(id int64, photoID int64, photoURL string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if op, ok := m.ops[id]; ok {
		op.PhotoIDOut = photoID
		op.PhotoURL = photoURL
	}
}

// enqueueRun 入后台并发队列执行：不同卡片并行（上限按 Limit 现读，默认 2，收敛 1–4），
// 同卡片仍由 activeLock 互斥；名额随任务收尾释放，fn panic 不得带崩进程或泄漏名额。
func (m *PhotoOpManager) enqueueRun(fn func()) {
	go func() {
		release, err := m.gate.acquire(context.Background(), m.limit())
		if err != nil {
			return
		}
		defer release()
		defer func() {
			// Run 已收敛任务体内的 panic；这里兜底 fn 自身的意外 panic，保证名额归还
			if rec := recover(); rec != nil {
				log.Printf("[商旅打卡] 照片任务调度异常：panic %v", rec)
			}
		}()
		fn()
	}()
}
