// 服务端暂存上传：浏览器分片先落 Shade 本地临时盘（蓝条），最后一片到齐后
// 自动在后台用 OpenList FsPut 写入网盘（绿条）；complete 请求只查询/确认，
// 用户蓝条满即可关页，后台任务继续。
package netdisk

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	stageChunkSize   = 4 << 20
	stageUploadLimit = 2 * time.Hour
	stageRecordTTL   = 30 * time.Minute
	stageStaleTTL    = 6 * time.Hour
)

var errStageClosed = errors.New("暂存上传已结束")

// stageUpload 一个已落临时盘/正在后台写网盘的上传。
type stageUpload struct {
	mu      sync.Mutex
	writeMu sync.Mutex

	m      *Module
	ol     OpenListClient
	id     string
	userID int64
	key    string
	abs    string
	size   int64
	chunk  int64
	total  int

	path          string
	file          *os.File
	received      []bool
	receivedBytes int64
	state         string // staging / uploading / completed / failed_permanent / aborted
	err           string
	progress      int
	started       bool
	cancel        context.CancelFunc
	createdAt     time.Time
	updatedAt     time.Time
	finishedAt    time.Time
}

type stageManager struct {
	mod     *Module
	mu      sync.Mutex
	dir     string
	initErr error
	byID    map[string]*stageUpload
	byKey   map[string]string
}

func newStageManager(mod *Module, dir string) *stageManager {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		dir = filepath.Join(os.TempDir(), "shade-netdisk-stage")
	}
	m := &stageManager{mod: mod, dir: dir, byID: map[string]*stageUpload{}, byKey: map[string]string{}}
	m.initErr = os.MkdirAll(dir, 0o700)
	if m.initErr == nil {
		m.cleanupOrphans()
	}
	return m
}

// cleanupOrphans 进程重启后内存表丢失，清掉超过 stale TTL 的遗留 .part。
func (m *stageManager) cleanupOrphans() {
	entries, err := os.ReadDir(m.dir)
	if err != nil {
		return
	}
	cutoff := time.Now().Add(-stageStaleTTL)
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".part") {
			continue
		}
		info, err := e.Info()
		if err == nil && info.ModTime().Before(cutoff) {
			_ = os.Remove(filepath.Join(m.dir, e.Name()))
		}
	}
}

func (m *Module) stageMgr() *stageManager {
	m.stageOnce.Do(func() { m.stage = newStageManager(m, m.StageDir) })
	return m.stage
}

func stageRandomID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

func stageKey(userID int64, space, rel, name string, size int64) string {
	return fmt.Sprintf("%d|%s|%s|%s|%d", userID, space, rel, name, size)
}

func (sm *stageManager) findOrCreate(ol OpenListClient, userID int64, space, rel, name, abs string, size int64) (*stageUpload, error) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	if sm.initErr != nil {
		return nil, sm.initErr
	}
	key := stageKey(userID, space, rel, name, size)
	sm.gcLocked(time.Now())
	if id, ok := sm.byKey[key]; ok {
		if st := sm.byID[id]; st != nil {
			st.mu.Lock()
			state := st.state
			st.mu.Unlock()
			if state == "staging" || state == "uploading" {
				return st, nil
			}
			sm.dropLocked(st)
		}
		delete(sm.byKey, key)
	}

	id := stageRandomID()
	path := filepath.Join(sm.dir, id+".part")
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return nil, fmt.Errorf("创建暂存文件失败: %w", err)
	}
	now := time.Now()
	st := &stageUpload{
		m: sm.mod, ol: ol, id: id, userID: userID, key: key,
		abs: abs, size: size, chunk: stageChunkSize,
		total: int((size + stageChunkSize - 1) / stageChunkSize), path: path, file: f,
		received: make([]bool, int((size+stageChunkSize-1)/stageChunkSize)),
		state:    "staging", createdAt: now, updatedAt: now,
	}
	sm.byID[id] = st
	sm.byKey[key] = id
	return st, nil
}

func (sm *stageManager) lookup(userID int64, id string) *stageUpload {
	sm.mu.Lock()
	st := sm.byID[id]
	sm.mu.Unlock()
	if st == nil || st.userID != userID {
		return nil
	}
	return st
}

func (sm *stageManager) dropLocked(st *stageUpload) {
	delete(sm.byID, st.id)
	if sm.byKey[st.key] == st.id {
		delete(sm.byKey, st.key)
	}
	st.mu.Lock()
	if st.file != nil {
		_ = st.file.Close()
		st.file = nil
	}
	if st.cancel != nil {
		st.cancel()
		st.cancel = nil
	}
	path := st.path
	st.mu.Unlock()
	_ = os.Remove(path)
}

func (sm *stageManager) gcLocked(now time.Time) {
	for id, st := range sm.byID {
		st.mu.Lock()
		state, updated, finished := st.state, st.updatedAt, st.finishedAt
		st.mu.Unlock()
		expired := false
		switch state {
		case "completed", "failed_permanent", "aborted":
			expired = !finished.IsZero() && now.Sub(finished) > stageRecordTTL
		default:
			expired = now.Sub(updated) > stageStaleTTL
		}
		if expired {
			sm.dropLocked(st)
			delete(sm.byID, id)
		}
	}
}

func (st *stageUpload) snapshot() map[string]any {
	st.mu.Lock()
	defer st.mu.Unlock()
	return st.snapshotLocked()
}

func (st *stageUpload) setProgress(n int64) {
	if n < 0 || st.size <= 0 {
		return
	}
	pct := int(n * 100 / st.size)
	if pct >= 100 {
		pct = 99
	}
	st.mu.Lock()
	if st.state == "uploading" && pct > st.progress {
		st.progress = pct
		st.updatedAt = time.Now()
	}
	st.mu.Unlock()
}

// writeChunk 顺序写同一上传的分片；接收齐最后一片后启动后台写网盘。
func (st *stageUpload) writeChunk(ctx context.Context, idx int, r io.Reader, length int64) error {
	expected := st.chunk
	if off := int64(idx) * st.chunk; off+expected > st.size {
		expected = st.size - off
	}
	if idx < 0 || idx >= st.total || length != expected {
		return errStageChunkBad
	}

	st.writeMu.Lock()
	defer st.writeMu.Unlock()

	st.mu.Lock()
	if st.state != "staging" {
		state := st.state
		st.mu.Unlock()
		if state == "uploading" || state == "completed" {
			return nil
		}
		return errStageClosed
	}
	f := st.file
	st.mu.Unlock()
	if f == nil {
		return errStageClosed
	}

	buf := make([]byte, 64<<10)
	off := int64(idx) * st.chunk
	written := int64(0)
	for written < expected {
		readBuf := buf
		if remain := expected - written; int64(len(readBuf)) > remain {
			readBuf = readBuf[:remain]
		}
		n, rerr := r.Read(readBuf)
		if n > 0 {
			if _, werr := f.WriteAt(readBuf[:n], off+written); werr != nil {
				return fmt.Errorf("写暂存文件失败: %w", werr)
			}
			written += int64(n)
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			return rerr
		}
	}
	if written != expected {
		return errStageChunkBad
	}

	st.mu.Lock()
	if !st.received[idx] {
		st.received[idx] = true
		st.receivedBytes += written
	}
	st.updatedAt = time.Now()
	ready := st.receivedBytes >= st.size
	if ready && st.state == "staging" {
		st.state = "uploading"
		st.updatedAt = time.Now()
		if st.file != nil {
			_ = st.file.Sync()
			_ = st.file.Close()
			st.file = nil
		}
	}
	st.mu.Unlock()

	if ready {
		st.m.startStage(st)
	}
	return nil
}

var errStageChunkBad = errors.New("分片长度不符")

func (m *Module) startStage(st *stageUpload) {
	st.mu.Lock()
	if st.started || st.state != "uploading" {
		st.mu.Unlock()
		return
	}
	st.started = true
	ctx, cancel := context.WithTimeout(context.WithoutCancel(context.Background()), stageUploadLimit)
	st.cancel = cancel
	ol, path, abs, size := st.ol, st.path, st.abs, st.size
	st.mu.Unlock()

	go func() {
		defer cancel()
		f, err := os.Open(path)
		if err == nil {
			pr := &stageProgressReader{r: f, st: st, size: size}
			err = ol.FsPut(ctx, abs, pr, size)
			_ = f.Close()
		}
		st.finish(err)
	}()
}

type stageProgressReader struct {
	r    io.Reader
	st   *stageUpload
	size int64
	read int64
}

func (p *stageProgressReader) Read(b []byte) (int, error) {
	n, err := p.r.Read(b)
	if n > 0 {
		p.read += int64(n)
		p.st.setProgress(p.read)
	}
	return n, err
}

func (st *stageUpload) finish(cause error) {
	st.mu.Lock()
	if st.state == "aborted" {
		st.mu.Unlock()
		st.cleanup()
		return
	}
	st.finishedAt = time.Now()
	st.updatedAt = st.finishedAt
	if cause != nil {
		st.state = "failed_permanent"
		st.err = errMessage(cause)
	} else {
		st.state = "completed"
		st.progress = 100
		st.receivedBytes = st.size
	}
	st.mu.Unlock()
	if cause == nil {
		st.m.dirSize.invalidate()
	}
	st.cleanup()
}

func (st *stageUpload) cleanup() {
	st.mu.Lock()
	if st.file != nil {
		_ = st.file.Close()
		st.file = nil
	}
	path := st.path
	st.mu.Unlock()
	_ = os.Remove(path)
}

func (m *Module) handleStagedChunk(w http.ResponseWriter, r *http.Request, st *stageUpload, idx int) {
	length := r.ContentLength
	if length < 0 || length > 64<<20 {
		writeFail(w, http.StatusRequestEntityTooLarge, codeTooLarge, "分片内容超限（64 MB）", nil)
		return
	}
	if err := st.writeChunk(r.Context(), idx, r.Body, length); err != nil {
		if errors.Is(err, errStageChunkBad) {
			writeFail(w, http.StatusBadRequest, codeParam, "分片长度与上传会话不符", nil)
			return
		}
		writeFail(w, http.StatusBadGateway, codeInternal, "写入暂存文件失败", nil)
		return
	}
	writeOK(w, st.snapshot(), "")
}

func (m *Module) handleStagedComplete(w http.ResponseWriter, st *stageUpload) {
	st.mu.Lock()
	state, ready := st.state, st.receivedBytes >= st.size
	st.mu.Unlock()
	if state == "staging" && !ready {
		writeFail(w, http.StatusBadRequest, codeParam, "尚有分片未到齐，不能完成上传", nil)
		return
	}
	if state == "staging" && ready {
		st.mu.Lock()
		st.state = "uploading"
		if st.file != nil {
			_ = st.file.Sync()
			_ = st.file.Close()
			st.file = nil
		}
		st.mu.Unlock()
		m.startStage(st)
	}
	writeOK(w, st.snapshot(), "")
}

func (m *Module) handleStagedAbort(w http.ResponseWriter, st *stageUpload) {
	st.mu.Lock()
	if st.state != "completed" && st.state != "failed_permanent" && st.state != "aborted" {
		st.state = "aborted"
		st.finishedAt = time.Now()
		st.updatedAt = st.finishedAt
		if st.cancel != nil {
			st.cancel()
			st.cancel = nil
		}
	}
	if st.file != nil {
		_ = st.file.Close()
		st.file = nil
	}
	snap := st.snapshotLocked()
	st.mu.Unlock()
	st.cleanup()
	writeOK(w, snap, "")
}

func (st *stageUpload) snapshotLocked() map[string]any {
	received := make([][2]int, 0)
	start := -1
	for i, ok := range st.received {
		if ok && start < 0 {
			start = i
		}
		if !ok && start >= 0 {
			received = append(received, [2]int{start, i - 1})
			start = -1
		}
	}
	if start >= 0 {
		received = append(received, [2]int{start, len(st.received) - 1})
	}
	return map[string]any{
		"upload_id": st.id, "state": st.state, "staged": true, "size": st.size,
		"chunk_size": st.chunk, "total_chunks": st.total, "received_bytes": st.receivedBytes,
		"received": received, "storage_progress": st.progress, "frontier": 0, "error": st.err,
	}
}
