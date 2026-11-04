// 服务端暂存上传：浏览器分片先落 Shade 本地临时盘（蓝条），最后一片到齐后
// 自动在后台走 OpenList multipart 写入网盘（绿条）；complete 请求只查询/确认，
// 用户蓝条满即可关页，后台任务继续。
package netdisk

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	// 暂存分片对齐 Cloudreve 默认 25MB：WAF 单请求最多扫描 5MB，
	// 25MB 分片只需扫描 1/5 数据，且请求数比 4MB 少 6 倍。
	stageChunkSize = 25 << 20
	// 暂存阶段浏览器并发 3；4MB 直传路径仍由前端默认并发 6。
	stageChunkConcurrency = 3
	stageUploadLimit      = 2 * time.Hour
	stageRecordTTL        = 30 * time.Minute
	stageStaleTTL         = 6 * time.Hour
)

var errStageClosed = errors.New("暂存上传已结束")

// stageUpload 一个已落临时盘/正在后台写网盘的上传。
type stageUpload struct {
	mu sync.Mutex

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
	// 预分配：并发分片按 offset 写入时不再反复扩展文件，也不会读到未定义的短文件。
	if err := f.Truncate(size); err != nil {
		_ = f.Close()
		_ = os.Remove(path)
		return nil, fmt.Errorf("预分配暂存文件失败: %w", err)
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
		case "completed", "aborted":
			expired = !finished.IsZero() && now.Sub(finished) > stageRecordTTL
		case "failed_permanent":
			expired = !finished.IsZero() && now.Sub(finished) > stageStaleTTL
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

// writeChunk 按偏移并发写同一上传的分片；接收齐最后一片后启动后台写网盘。
// os.File.WriteAt 并发安全，且每片写入独立区间，不需要全局写锁串行化请求体。
func (st *stageUpload) writeChunk(ctx context.Context, idx int, r io.Reader, length int64) error {
	expected := st.chunk
	if off := int64(idx) * st.chunk; off+expected > st.size {
		expected = st.size - off
	}
	if idx < 0 || idx >= st.total || (length >= 0 && length != expected) {
		return errStageChunkBad
	}

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
		// 文件句柄交给后台 multipart 读完后统一 cleanup；此处不关闭，
		// 避免并发分片仍在写时提前 Close（WriteAt 会直接失败）。
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
	st.mu.Unlock()

	go func() {
		defer cancel()
		st.finish(st.runMultipart(ctx))
	}()
}

// stageOLInit / stageOLSnapshot 只取 OpenList multipart 契约里我们需要的字段。
type stageOLInit struct {
	UploadID    string `json:"upload_id"`
	ChunkSize   int64  `json:"chunk_size"`
	TotalChunks int    `json:"total_chunks"`
}

type stageOLSnapshot struct {
	State           string  `json:"state"`
	ReceivedBytes   int64   `json:"received_bytes"`
	StorageProgress float64 `json:"storage_progress"`
	Error           string  `json:"error"`
}

func (st *stageUpload) applyOLSnapshot(s *stageOLSnapshot) {
	if s == nil {
		return
	}
	pct := 0
	if s.StorageProgress > 0 {
		pct = int(math.Round(s.StorageProgress))
		if pct > 99 && s.State != "completed" {
			pct = 99
		}
		if pct > 100 {
			pct = 100
		}
	}
	st.mu.Lock()
	if pct > st.progress && st.state == "uploading" {
		st.progress = pct
	}
	st.updatedAt = time.Now()
	st.mu.Unlock()
}

func stageSleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// runMultipart 用暂存文件作为源，走 OpenList multipart：init → 3 并发分片（429/409/5xx
// 重试）→ complete；临时故障与 failed_retriable 会话直接重读暂存文件独立重试。
func (st *stageUpload) runMultipart(ctx context.Context) error {
	if st.ol == nil {
		return ErrNotConfigured
	}
	dir, _ := splitAbsPath(st.abs)
	if dir == "" {
		return errors.New("网盘路径不合法")
	}
	var lastErr error
	for attempt := 1; attempt <= 3; attempt++ {
		err := st.ol.EnsureDir(ctx, dir)
		if err == nil {
			err = st.runMultipartAttempt(ctx)
		}
		if err == nil {
			return nil
		}
		lastErr = err
		var ue *UpstreamError
		if errors.As(err, &ue) && ue.State == "failed_permanent" {
			return err
		}
		if attempt == 3 {
			break
		}
		st.mu.Lock()
		if st.state == "uploading" {
			st.progress = 0
		}
		st.updatedAt = time.Now()
		st.mu.Unlock()
		if err := stageSleep(ctx, time.Duration(attempt)*time.Second); err != nil {
			return err
		}
	}
	return fmt.Errorf("网盘后台同步多次失败: %w", lastErr)
}

func (st *stageUpload) runMultipartAttempt(ctx context.Context) error {
	f, err := os.Open(st.path)
	if err != nil {
		return fmt.Errorf("打开暂存文件失败: %w", err)
	}
	defer f.Close()

	data, err := st.ol.Call(ctx, http.MethodPost, "/api/fs/multipart/init", CallOpts{
		Headers: map[string]string{
			"File-Path":    encodeURIPath(st.abs),
			"X-File-Size":  strconv.FormatInt(st.size, 10),
			"X-Chunk-Size": strconv.FormatInt(stageChunkSize, 10),
		},
		Timeout: -1,
	})
	if err != nil {
		return err
	}
	var init stageOLInit
	if err := json.Unmarshal(data, &init); err != nil || strings.TrimSpace(init.UploadID) == "" {
		return fmt.Errorf("网盘分片初始化响应异常")
	}
	chunkSize := init.ChunkSize
	if chunkSize <= 0 {
		chunkSize = stageChunkSize
	}
	total := init.TotalChunks
	if total <= 0 {
		total = int((st.size + chunkSize - 1) / chunkSize)
	}

	pollCtx, stopPoll := context.WithCancel(ctx)
	var pollWG sync.WaitGroup
	pollWG.Add(1)
	go func() {
		defer pollWG.Done()
		st.pollMultipart(pollCtx, init.UploadID)
	}()
	defer func() {
		stopPoll()
		pollWG.Wait()
	}()

	if err := st.uploadMultipartChunks(ctx, f, init.UploadID, chunkSize, total); err != nil {
		return err
	}
	data, err = st.ol.Call(ctx, http.MethodPost, "/api/fs/multipart/complete", CallOpts{
		Headers: map[string]string{"X-Upload-Id": init.UploadID},
		Timeout: -1,
	})
	if err != nil {
		return err
	}
	var snap stageOLSnapshot
	_ = json.Unmarshal(data, &snap)
	st.applyOLSnapshot(&snap)
	return nil
}

func (st *stageUpload) pollMultipart(ctx context.Context, uploadID string) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			data, err := st.ol.Call(ctx, http.MethodGet, "/api/fs/multipart/status", CallOpts{
				Query: map[string]string{"upload_id": uploadID},
			})
			if err != nil {
				continue
			}
			var snap stageOLSnapshot
			if json.Unmarshal(data, &snap) == nil {
				st.applyOLSnapshot(&snap)
			}
		}
	}
}

func (st *stageUpload) uploadMultipartChunks(ctx context.Context, f *os.File, uploadID string, chunkSize int64, total int) error {
	if total <= 0 {
		return errors.New("网盘分片数为空")
	}
	workers := stageChunkConcurrency
	if total < workers {
		workers = total
	}
	workerCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	var next atomic.Int64
	var wg sync.WaitGroup
	errCh := make(chan error, 1)
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				idx := int(next.Add(1)) - 1
				if idx >= total || workerCtx.Err() != nil {
					return
				}
				if err := st.sendMultipartChunk(workerCtx, f, uploadID, chunkSize, idx); err != nil {
					select {
					case errCh <- err:
					default:
					}
					cancel()
					return
				}
			}
		}()
	}
	wg.Wait()
	select {
	case err := <-errCh:
		return err
	default:
		return nil
	}
}

func (st *stageUpload) sendMultipartChunk(ctx context.Context, f *os.File, uploadID string, chunkSize int64, idx int) error {
	off := int64(idx) * chunkSize
	n := chunkSize
	if off+n > st.size {
		n = st.size - off
	}
	if n <= 0 {
		return errors.New("分片偏移非法")
	}
	var lastErr error
	for attempt := 1; attempt <= 5; attempt++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		section := io.NewSectionReader(f, off, n)
		data, err := st.ol.Call(ctx, http.MethodPut, "/api/fs/multipart/chunk", CallOpts{
			Headers: map[string]string{
				"X-Upload-Id":   uploadID,
				"X-Chunk-Index": strconv.Itoa(idx),
				"Content-Type":  "application/octet-stream",
			},
			Body:          section,
			ContentLength: n,
			Timeout:       -1,
		})
		if err == nil {
			var snap stageOLSnapshot
			if json.Unmarshal(data, &snap) == nil {
				st.applyOLSnapshot(&snap)
			}
			return nil
		}
		lastErr = err
		var ue *UpstreamError
		if errors.As(err, &ue) {
			if ue.State == "failed_permanent" || ue.State == "failed_retriable" {
				return err
			}
			if ue.UpstreamCode == 429 || ue.UpstreamCode == 409 || ue.Status >= 500 {
				if err := stageSleep(ctx, time.Duration(attempt)*time.Second); err != nil {
					return err
				}
				continue
			}
		}
		if err := stageSleep(ctx, time.Second); err != nil {
			return err
		}
	}
	return fmt.Errorf("分片 %d 上传失败: %w", idx, lastErr)
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
		if st.file != nil {
			_ = st.file.Close()
			st.file = nil
		}
	} else {
		st.state = "completed"
		st.progress = 100
		st.receivedBytes = st.size
	}
	st.mu.Unlock()
	if cause == nil {
		st.m.dirSize.invalidate()
		st.cleanup()
	}
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
	// HTTP/2 + nginx proxy_request_buffering off 可能以 chunked 转发（ContentLength=-1）；
	// 分片长度由 writeChunk 按 index/会话 size 校验，不能把 -1 当成超限。
	if length == 0 {
		writeFail(w, http.StatusBadRequest, codeParam, "分片内容为空", nil)
		return
	}
	if length > 64<<20 {
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
		"chunk_size": st.chunk, "chunk_concurrency": stageChunkConcurrency,
		"total_chunks": st.total, "received_bytes": st.receivedBytes,
		"received": received, "storage_progress": st.progress, "frontier": 0, "error": st.err,
	}
}
