// 照片操作任务（PhotoOp）：上传/改人名/删除与商旅远端同步的任务化执行。
// 同步校验通过后登记任务立即返回 opId，后台串行执行；端侧凭 GetActive / Status 渲染进度。
package sgccclockin

import (
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
	// tail 串行队列尾（enqueueRun 链式等待：全局一次只执行一个任务，同 Node photoOpQueue）。
	tail chan struct{}
}

// NewPhotoOpManager 构造任务管理器。
func NewPhotoOpManager() *PhotoOpManager {
	return &PhotoOpManager{
		ops:        map[int64]*PhotoOp{},
		activeLock: map[string]int64{},
	}
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

// Run 执行任务体（同步执行 fn；fn 返回错误时任务置失败；无论成败收尾解锁卡片）。
func (m *PhotoOpManager) Run(id int64, fn func(op *PhotoOp) error) {
	m.mu.Lock()
	op, ok := m.ops[id]
	if !ok {
		m.mu.Unlock()
		return
	}
	op.Step = "running"
	m.mu.Unlock()

	err := fn(op)

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

// enqueueRun 入串行后台队列执行（全局一次只跑一个任务：费用模板 read-modify-write 防并发互踩，
// 同 Node photoOpQueue）；fn 在前序任务收尾后才启动。
func (m *PhotoOpManager) enqueueRun(fn func()) {
	m.mu.Lock()
	prev := m.tail
	next := make(chan struct{})
	m.tail = next
	m.mu.Unlock()
	go func() {
		if prev != nil {
			<-prev
		}
		fn()
		close(next)
	}()
}
