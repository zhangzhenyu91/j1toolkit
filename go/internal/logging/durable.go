// 文件优先的持久日志层：每条日志先同步写入 NDJSON 段文件，再由唯一 Importer
// 异步批量补录到 MySQL。DB 故障/未连接不影响文件落盘，重启后按游标续传；
// log_id 唯一键保证重复回放幂等；段文件仅在 Importer 读尽后压缩归档。
package logging

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"shade/internal/config"
)

const (
	// FsyncBatch 只对 warn/error/audit 每条 fsync，其余由 Importer/关闭时批量 fsync。
	FsyncBatch = "batch"
	// FsyncAlways 每条日志都 fsync。
	FsyncAlways = "always"

	defaultDurableBatchSize    = 100
	defaultSegmentMaxBytes     = int64(64 << 20)
	defaultDurablePollInterval = 100 * time.Millisecond
	cursorFileName             = "cursor.json"
	integrityFileName          = "integrity.json"
	segmentNamePrefix          = "app-"
	segmentFileExt             = ".log"
	segmentArchiveExt          = ".log.gz"
	importDBTimeout            = 15 * time.Second
)

// IntegrityState 文件持久层的失败/损坏累计（写盘、fsync、DB 回退、坏行），
// 落 integrity.json，重启后仍能判定导出不完整。
type IntegrityState struct {
	WriteErrors   int64     `json:"write_errors"`
	DiskFallbacks int64     `json:"disk_fallbacks"`
	FsyncErrors   int64     `json:"fsync_errors"`
	CorruptLines  int64     `json:"corrupt_lines"`
	UpdatedAt     time.Time `json:"updated_at,omitempty"`
}

// ReadIntegrity 读取日志目录的持久失败状态：文件不存在返回零值 nil（无历史失败）；
// 读取失败或 JSON 损坏返回错误（调用方必须按“完整性未知=不完整”处理，不能当作无故障）。
func ReadIntegrity(dir string) (IntegrityState, error) {
	data, err := os.ReadFile(filepath.Join(dir, integrityFileName))
	if errors.Is(err, os.ErrNotExist) {
		return IntegrityState{}, nil
	}
	if err != nil {
		return IntegrityState{}, fmt.Errorf("读取 %s: %w", integrityFileName, err)
	}
	var st IntegrityState
	if err := json.Unmarshal(data, &st); err != nil {
		return IntegrityState{}, fmt.Errorf("%s 损坏: %w", integrityFileName, err)
	}
	return st, nil
}

// ErrSinkClosed 日志文件已关闭，不再接受写入。
var ErrSinkClosed = errors.New("logging: 日志文件已关闭")

// DurableOptions 文件持久层参数（测试可注入；生产从 env 读取）。
type DurableOptions struct {
	BatchSize      int
	FsyncMode      string
	MaxSegmentSize int64
	PollInterval   time.Duration
	Stderr         io.Writer
}

// DefaultDurableOptions 生产默认：批量 100、fsync batch、单段 64MiB、100ms 轮询。
func DefaultDurableOptions() DurableOptions {
	return DurableOptions{
		BatchSize:      defaultDurableBatchSize,
		FsyncMode:      FsyncBatch,
		MaxSegmentSize: defaultSegmentMaxBytes,
		PollInterval:   defaultDurablePollInterval,
		Stderr:         os.Stderr,
	}
}

// DurableOptionsFromEnv 读取启动期 env：APP_LOG_BATCH_SIZE / APP_LOG_FSYNC；
// 非法值回退默认（这些键不进入运行配置注册表）。
func DurableOptionsFromEnv() DurableOptions {
	opts := DefaultDurableOptions()
	if raw := strings.TrimSpace(os.Getenv("APP_LOG_BATCH_SIZE")); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 {
			if n > 10000 {
				n = 10000
			}
			opts.BatchSize = n
		}
	}
	switch strings.ToLower(strings.TrimSpace(os.Getenv("APP_LOG_FSYNC"))) {
	case FsyncAlways:
		opts.FsyncMode = FsyncAlways
	case FsyncBatch:
		opts.FsyncMode = FsyncBatch
	}
	return opts
}

func (o DurableOptions) normalized() DurableOptions {
	def := DefaultDurableOptions()
	if o.BatchSize <= 0 {
		o.BatchSize = def.BatchSize
	}
	switch o.FsyncMode {
	case FsyncAlways, FsyncBatch:
	default:
		o.FsyncMode = FsyncBatch
	}
	if o.MaxSegmentSize <= 0 {
		o.MaxSegmentSize = def.MaxSegmentSize
	}
	if o.PollInterval <= 0 {
		o.PollInterval = def.PollInterval
	}
	if o.Stderr == nil {
		o.Stderr = os.Stderr
	}
	return o
}

// durableRecord NDJSON 行格式（字段与 AppLogEntry 一一对应）。
type durableRecord struct {
	LogID       string    `json:"log_id"`
	OccurredAt  time.Time `json:"occurred_at"`
	AppCode     string    `json:"app_code"`
	Level       string    `json:"level"`
	Category    string    `json:"category"`
	Event       string    `json:"event"`
	Message     string    `json:"message"`
	MessageCN   string    `json:"message_cn"`
	RawMessage  string    `json:"raw_message"`
	ContextJSON string    `json:"context_json"`
	TraceID     string    `json:"trace_id"`
	UserID      int64     `json:"user_id"`
	TeamID      int64     `json:"team_id"`
	Detail      string    `json:"detail"`
	Source      string    `json:"source"`
	IsAccess    bool      `json:"is_access"`
}

func recordFromEntry(e config.AppLogEntry) durableRecord {
	return durableRecord{
		LogID: e.LogID, OccurredAt: e.OccurredAt, AppCode: e.AppCode, Level: e.Level,
		Category: e.Category, Event: e.Event, Message: e.Message, MessageCN: e.MessageCN,
		RawMessage: e.RawMessage, ContextJSON: e.ContextJSON, TraceID: e.TraceID,
		UserID: e.UserID, TeamID: e.TeamID, Detail: e.Detail, Source: e.Source, IsAccess: e.IsAccess,
	}
}

func (r durableRecord) entry() config.AppLogEntry {
	return config.AppLogEntry{
		LogID: r.LogID, OccurredAt: r.OccurredAt, AppCode: r.AppCode, Level: r.Level,
		Category: r.Category, Event: r.Event, Message: r.Message, MessageCN: r.MessageCN,
		RawMessage: r.RawMessage, ContextJSON: r.ContextJSON, TraceID: r.TraceID,
		UserID: r.UserID, TeamID: r.TeamID, Detail: r.Detail, Source: r.Source, IsAccess: r.IsAccess,
	}
}

type durableCursor struct {
	Segment string `json:"segment"`
	Offset  int64  `json:"offset"`
}

// logSegment 段文件信息；base 去掉 .gz 后缀用于排序。
type logSegment struct {
	name  string
	path  string
	base  string
	plain bool
	size  int64
}

// Health 持久日志层健康统计（供 /app-logs/health 与管理端展示）。
type Health struct {
	Dir              string
	ActiveSegment    string
	SegmentBytes     int64
	Written          int64
	Imported         int64
	Pending          int64
	WriteErrors      int64
	DiskFallbacks    int64
	FsyncErrors      int64
	CorruptLines     int64
	InsertErrors     int64
	Syncs            int64
	ArchivedSegments int
	DBConnected      bool
	CursorSegment    string
	CursorOffset     int64
	LastImport       time.Time
	LastError        string
}

// DurableSink 文件持久日志写入目标；实现 Sink 与 CloseContext。
type DurableSink struct {
	dir  string
	opts DurableOptions

	mu          sync.Mutex
	file        *os.File
	segment     string
	segmentDate string
	segSeq      int
	size        int64
	closed      bool

	cursorMu    sync.Mutex
	cursor      durableCursor
	integrityMu sync.Mutex

	dbMu     sync.RWMutex
	db       *sql.DB
	attached bool

	importMu  sync.Mutex
	stopCh    chan struct{}
	doneCh    chan struct{}
	trigger   chan struct{}
	closeOnce sync.Once
	stopOnce  sync.Once

	written       atomic.Int64
	imported      atomic.Int64
	pending       atomic.Int64
	writeErrors   atomic.Int64
	diskFallbacks atomic.Int64
	fsyncErrors   atomic.Int64
	corruptLines  atomic.Int64
	insertErrors  atomic.Int64
	syncs         atomic.Int64
	lastImport    atomic.Int64
	// syncFn 可注入 fsync 实现（生产即 (*os.File).Sync；测试注入故障）。
	syncFn func(*os.File) error
	// syncedSize 已 fsync 到的段内偏移（s.mu 保护），用于 batch 模式按需 fsync。
	syncedSize int64

	errMu        sync.Mutex
	lastErr      string
	alertMu      sync.Mutex
	lastAlert    time.Time
	lastAlertMsg string
}

// NewDurableSink 按启动期 env 默认参数创建文件持久层（DB 由 AttachDB 后续接入）。
func NewDurableSink(dir string) (*DurableSink, error) {
	return NewDurableSinkWithOptions(dir, DurableOptionsFromEnv())
}

// NewDurableSinkWithOptions 创建目录/活跃段并读取游标；不依赖数据库，可在 main 最早期调用。
func NewDurableSinkWithOptions(dir string, opts DurableOptions) (*DurableSink, error) {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return nil, errors.New("logging: APP_LOG_DIR 不能为空")
	}
	opts = opts.normalized()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("logging: 创建日志目录 %s: %w", dir, err)
	}
	_ = os.Chmod(dir, 0o700)
	s := &DurableSink{
		dir:     dir,
		opts:    opts,
		stopCh:  make(chan struct{}),
		doneCh:  make(chan struct{}),
		trigger: make(chan struct{}, 1),
		syncFn:  func(f *os.File) error { return f.Sync() },
	}
	if err := s.loadCursor(); err != nil {
		s.alert("游标读取失败，将从最早段重新补录（log_id 幂等）: " + err.Error())
	}
	s.loadIntegrity()
	if err := s.openInitialSegment(); err != nil {
		return nil, err
	}
	if s.cursor.Segment == "" {
		if segs, err := s.listSegments(); err == nil && len(segs) > 0 {
			s.cursor = durableCursor{Segment: segs[0].name, Offset: 0}
		} else {
			s.cursor = durableCursor{Segment: s.segment, Offset: 0}
		}
	}
	s.recountPending()
	return s, nil
}

func (s *DurableSink) cursorPath() string { return filepath.Join(s.dir, cursorFileName) }

func (s *DurableSink) loadCursor() error {
	data, err := os.ReadFile(s.cursorPath())
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var cur durableCursor
	if err := json.Unmarshal(data, &cur); err != nil {
		return err
	}
	if cur.Offset < 0 {
		cur.Offset = 0
	}
	s.cursor = cur
	return nil
}

// loadIntegrity 恢复历史失败计数（重启后导出仍能判定文件副本不完整）。
func (s *DurableSink) loadIntegrity() {
	st, err := ReadIntegrity(s.dir)
	if err != nil {
		s.alert("日志完整性状态损坏/不可读，历史写入失败未知（导出将按不完整处理）: " + err.Error())
		return
	}
	s.writeErrors.Store(st.WriteErrors)
	s.diskFallbacks.Store(st.DiskFallbacks)
	s.fsyncErrors.Store(st.FsyncErrors)
	s.corruptLines.Store(st.CorruptLines)
}

// persistIntegrity 原子持久化失败计数：临时文件 + fsync + rename（失败仅告警，不阻塞写日志）。
func (s *DurableSink) persistIntegrity() {
	st := IntegrityState{
		WriteErrors: s.writeErrors.Load(), DiskFallbacks: s.diskFallbacks.Load(),
		FsyncErrors: s.fsyncErrors.Load(), CorruptLines: s.corruptLines.Load(),
		UpdatedAt: time.Now().UTC(),
	}
	data, err := json.Marshal(st)
	if err != nil {
		return
	}
	s.integrityMu.Lock()
	defer s.integrityMu.Unlock()
	tmp := filepath.Join(s.dir, integrityFileName+".tmp")
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		s.alert("日志完整性状态写入失败: " + err.Error())
		return
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		s.alert("日志完整性状态写入失败: " + err.Error())
		return
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		s.alert("日志完整性状态 fsync 失败: " + err.Error())
		return
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		s.alert("日志完整性状态关闭失败: " + err.Error())
		return
	}
	if err := os.Rename(tmp, filepath.Join(s.dir, integrityFileName)); err != nil {
		_ = os.Remove(tmp)
		s.alert("日志完整性状态提交失败: " + err.Error())
	}
}

// repairSegmentTail 把段文件尾部未换行的半行截断到最后换行（保留完整行）；
// 无任何完整行时截断为 0。重启复用活跃段前调用，避免新日志拼接成坏行。
func repairSegmentTail(path string) (int64, error) {
	f, err := os.OpenFile(path, os.O_RDWR, 0o600)
	if err != nil {
		return 0, err
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return 0, err
	}
	size := info.Size()
	if size == 0 {
		return 0, nil
	}
	last := make([]byte, 1)
	if _, err := f.ReadAt(last, size-1); err != nil {
		return 0, err
	}
	if last[0] == '\n' {
		return size, nil
	}
	const chunk = 4096
	buf := make([]byte, chunk)
	pos := size
	for pos > 0 {
		n := int64(chunk)
		if pos < n {
			n = pos
		}
		start := pos - n
		if _, err := f.ReadAt(buf[:n], start); err != nil {
			return 0, err
		}
		if i := bytes.LastIndexByte(buf[:n], '\n'); i >= 0 {
			newSize := start + int64(i) + 1
			if err := f.Truncate(newSize); err != nil {
				return 0, err
			}
			if err := f.Sync(); err != nil {
				return 0, err
			}
			return newSize, nil
		}
		pos = start
	}
	if err := f.Truncate(0); err != nil {
		return 0, err
	}
	if err := f.Sync(); err != nil {
		return 0, err
	}
	return 0, nil
}

// openInitialSegment 复用当天未满的活跃段，否则新建段文件（0600）。
func (s *DurableSink) openInitialSegment() error {
	segs, err := s.listSegments()
	if err != nil {
		return fmt.Errorf("logging: 读取日志目录: %w", err)
	}
	now := time.Now()
	today := now.Format("20060102")
	for i := len(segs) - 1; i >= 0; i-- {
		seg := segs[i]
		if !seg.plain || len(seg.name) < len(segmentNamePrefix)+8 || seg.name[len(segmentNamePrefix):len(segmentNamePrefix)+8] != today {
			continue
		}
		// 崩溃可能留下无换行的半行；复用前截断到最后换行，避免新日志拼成坏行。
		size, rerr := repairSegmentTail(seg.path)
		if rerr != nil {
			s.alert("活跃段尾部修复失败，改用新段: " + rerr.Error())
			break
		}
		if size < seg.size {
			s.corruptLines.Add(1)
			s.persistIntegrity()
		}
		seg.size = size
		if seg.size >= s.opts.MaxSegmentSize {
			break
		}
		f, err := os.OpenFile(seg.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
		if err != nil {
			return fmt.Errorf("logging: 打开活跃段: %w", err)
		}
		_ = f.Chmod(0o600)
		s.file, s.segment, s.segmentDate, s.segSeq, s.size = f, seg.name, today, segmentSeq(seg.name), seg.size
		// 复用旧段时置 0，促使 Importer 首次批量 fsync 覆盖重启前的尾部。
		s.syncedSize = 0
		return nil
	}
	return s.rotateLocked(now)
}

// rotateLocked 关闭旧活跃段并新建当天下一序号段文件；调用方须持有 s.mu（构造期除外）。
func (s *DurableSink) rotateLocked(now time.Time) error {
	if s.file != nil {
		_ = s.file.Sync()
		_ = s.file.Close()
		s.file = nil
	}
	day := now.Format("20060102")
	seq := s.segSeq
	if s.segmentDate != day {
		seq = 0
	}
	if seq == 0 {
		seq = s.maxSeqForDay(day) + 1
	} else {
		seq++
	}
	name := fmt.Sprintf("%s%s-%06d%s", segmentNamePrefix, day, seq, segmentFileExt)
	path := filepath.Join(s.dir, name)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("logging: 创建段文件 %s: %w", name, err)
	}
	_ = f.Chmod(0o600)
	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return err
	}
	s.file, s.segment, s.segmentDate, s.segSeq, s.size = f, name, day, seq, info.Size()
	s.syncedSize = info.Size()
	return nil
}

func segmentSeq(name string) int {
	trimmed := strings.TrimSuffix(name, segmentArchiveExt)
	trimmed = strings.TrimSuffix(trimmed, segmentFileExt)
	parts := strings.Split(trimmed, "-")
	if len(parts) < 3 {
		return 0
	}
	n, _ := strconv.Atoi(parts[len(parts)-1])
	return n
}

func segmentDay(name string) (time.Time, bool) {
	trimmed := strings.TrimPrefix(name, segmentNamePrefix)
	trimmed = strings.TrimSuffix(trimmed, segmentArchiveExt)
	trimmed = strings.TrimSuffix(trimmed, segmentFileExt)
	if len(trimmed) < 8 {
		return time.Time{}, false
	}
	day, err := time.ParseInLocation("20060102", trimmed[:8], time.Local)
	if err != nil {
		return time.Time{}, false
	}
	return day, true
}

func (s *DurableSink) maxSeqForDay(day string) int {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return 0
	}
	max := 0
	prefix := segmentNamePrefix + day + "-"
	for _, e := range entries {
		name := e.Name()
		if !strings.HasPrefix(name, prefix) {
			continue
		}
		if !strings.HasSuffix(name, segmentFileExt) && !strings.HasSuffix(name, segmentArchiveExt) {
			continue
		}
		if seq := segmentSeq(name); seq > max {
			max = seq
		}
	}
	return max
}

func (s *DurableSink) listSegments() ([]logSegment, error) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return nil, err
	}
	byBase := map[string]logSegment{}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasPrefix(name, segmentNamePrefix) {
			continue
		}
		var seg logSegment
		switch {
		case strings.HasSuffix(name, segmentArchiveExt):
			seg = logSegment{name: name, path: filepath.Join(s.dir, name),
				base: strings.TrimSuffix(name, ".gz"), plain: false}
			info, ierr := e.Info()
			if ierr != nil {
				continue
			}
			seg.size = info.Size()
		case strings.HasSuffix(name, segmentFileExt):
			seg = logSegment{name: name, path: filepath.Join(s.dir, name),
				base: name, plain: true}
			info, ierr := e.Info()
			if ierr != nil {
				continue
			}
			seg.size = info.Size()
		default:
			continue
		}
		if prev, ok := byBase[seg.base]; ok && prev.plain {
			continue // 已存在未解压版本时优先它
		}
		byBase[seg.base] = seg
	}
	out := make([]logSegment, 0, len(byBase))
	for _, seg := range byBase {
		out = append(out, seg)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].name < out[j].name })
	return out, nil
}

// Write 立即 write(2) 追加一行 NDJSON（不在用户态缓存）；warn/error/audit 与
// FsyncAlways 每条 fsync；磁盘失败回退带 log_id 的 DB 直写并 stderr/health 告警。
func (s *DurableSink) Write(ctx context.Context, e config.AppLogEntry) error {
	if s == nil {
		return ErrSinkClosed
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if e.TraceID == "" {
		e.TraceID = TraceIDFromContext(ctx)
	}
	fillActor(&e, ctx)
	sanitizeEntry(&e)
	if e.LogID == "" {
		e.LogID = config.NewAppLogID()
	}
	if e.OccurredAt.IsZero() {
		e.OccurredAt = time.Now()
	}
	if e.Category == "" {
		e.Category = "business"
	}
	if e.Category == "access" {
		e.IsAccess = true
	}
	line, err := json.Marshal(recordFromEntry(e))
	if err != nil {
		return s.fallbackDB(ctx, e, err)
	}
	line = append(line, '\n')

	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return ErrSinkClosed
	}
	if err := s.rotateIfNeededLocked(); err != nil {
		s.mu.Unlock()
		s.writeErrors.Add(1)
		s.persistIntegrity()
		s.alert("段文件轮换失败，回退数据库直写: " + err.Error())
		return s.fallbackDB(ctx, e, err)
	}
	var writeErr error
	if s.file == nil {
		writeErr = errors.New("日志文件不可用")
	} else {
		start := s.size
		n, werr := s.file.Write(line)
		if werr == nil && n != len(line) {
			werr = io.ErrShortWrite
		}
		if werr != nil {
			_ = s.file.Truncate(start)
			_ = s.file.Close()
			s.file = nil
			writeErr = werr
		} else {
			s.size += int64(n)
			s.written.Add(1)
			s.pending.Add(1)
		}
	}
	urgent := urgentEntry(e) || s.opts.FsyncMode == FsyncAlways
	s.mu.Unlock()

	if writeErr != nil {
		s.writeErrors.Add(1)
		s.persistIntegrity()
		s.alert("日志写盘失败，回退数据库直写: " + writeErr.Error())
		return s.fallbackDB(ctx, e, writeErr)
	}
	if urgent {
		// fsync 失败不能只告警：同一条日志按同一 log_id 回退 DB，文件行保留给 Importer 幂等补录。
		if err := s.syncFile(); err != nil {
			return s.fallbackDB(ctx, e, err)
		}
	}
	s.triggerImport()
	return nil
}

// rotateIfNeededLocked 跨日或达到 64MiB 时切换活跃段；调用方持有 s.mu。
func (s *DurableSink) rotateIfNeededLocked() error {
	now := time.Now()
	day := now.Format("20060102")
	if s.file != nil && day == s.segmentDate && s.size < s.opts.MaxSegmentSize {
		return nil
	}
	return s.rotateLocked(now)
}

func (s *DurableSink) syncFile() error {
	// 持锁完成 Sync，避免与 rotateLocked 关闭旧句柄竞争产生假告警。
	s.mu.Lock()
	defer s.mu.Unlock()
	s.syncs.Add(1)
	if s.file == nil {
		return nil
	}
	sync := s.syncFn
	if sync == nil {
		sync = func(f *os.File) error { return f.Sync() }
	}
	if err := sync(s.file); err != nil {
		s.fsyncErrors.Add(1)
		s.writeErrors.Add(1)
		s.setLastError("日志 fsync 失败: " + err.Error())
		s.persistIntegrity()
		s.alert("日志 fsync 失败，回退数据库直写: " + err.Error())
		return err
	}
	if s.size > s.syncedSize {
		s.syncedSize = s.size
	}
	return nil
}

// needsSync batch 模式按需 fsync：仅当当前段存在尚未落盘的写入。
func (s *DurableSink) needsSync() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.file != nil && s.size > s.syncedSize
}

func (s *DurableSink) triggerImport() {
	select {
	case s.trigger <- struct{}{}:
	default:
	}
}

func urgentEntry(e config.AppLogEntry) bool {
	switch strings.ToLower(e.Level) {
	case "warn", "warning", "error", "audit":
		return true
	}
	switch strings.ToLower(e.Category) {
	case "audit", "security":
		return true
	}
	return false
}

func (s *DurableSink) fallbackDB(ctx context.Context, e config.AppLogEntry, cause error) error {
	db := s.currentDB()
	if db == nil {
		s.setLastError("磁盘写入失败且数据库未连接: " + cause.Error())
		return fmt.Errorf("%w: %v", ErrNoDatabase, cause)
	}
	wctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if err := config.InsertAppLog(wctx, db, e); err != nil {
		s.setLastError("磁盘与数据库直写均失败: " + err.Error())
		s.persistIntegrity()
		s.alert("日志磁盘与数据库直写均失败: " + err.Error())
		return err
	}
	s.diskFallbacks.Add(1)
	s.persistIntegrity()
	s.setLastError("")
	return nil
}

func (s *DurableSink) currentDB() *sql.DB {
	if s == nil {
		return nil
	}
	s.dbMu.RLock()
	defer s.dbMu.RUnlock()
	return s.db
}

// AttachDB 接入数据库并启动唯一 Importer；重复调用只保留首个连接。
func (s *DurableSink) AttachDB(db *sql.DB) {
	if s == nil || db == nil {
		return
	}
	s.mu.Lock()
	closed := s.closed
	s.mu.Unlock()
	if closed {
		return
	}
	s.dbMu.Lock()
	if s.attached {
		s.dbMu.Unlock()
		return
	}
	s.db = db
	s.attached = true
	s.dbMu.Unlock()
	go s.importer()
	s.triggerImport()
}

func (s *DurableSink) importer() {
	ticker := time.NewTicker(s.opts.PollInterval)
	defer ticker.Stop()
	defer close(s.doneCh)
	for {
		select {
		case <-s.stopCh:
			return
		case <-ticker.C:
		case <-s.trigger:
		}
		ctx, cancel := context.WithTimeout(context.Background(), importDBTimeout)
		err := s.runImport(ctx)
		cancel()
		if err != nil && !errors.Is(err, ErrNoDatabase) {
			s.insertErrors.Add(1)
			s.setLastError(err.Error())
			s.alert("日志补录失败（游标保留，稍后重试）: " + err.Error())
		} else if err == nil {
			s.lastImport.Store(time.Now().UnixNano())
		}
		// batch 模式的批量 fsync 与入库成败解耦：DB 故障时文件仍需按批落盘。
		if s.opts.FsyncMode == FsyncBatch && s.needsSync() {
			s.syncFile()
		}
	}
}

// Flush 同步驱动一次完整补录（含封存段压缩），直到游标不再前进；ctx 控制超时。
func (s *DurableSink) Flush(ctx context.Context) error {
	if s == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if s.currentDB() == nil {
		return ErrNoDatabase
	}
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		before := s.cursorSnapshot()
		if err := s.runImport(ctx); err != nil {
			return err
		}
		if s.cursorSnapshot() == before {
			return nil
		}
	}
}

func (s *DurableSink) cursorSnapshot() durableCursor {
	s.cursorMu.Lock()
	defer s.cursorMu.Unlock()
	return s.cursor
}

// runImport 从游标开始顺序消费段文件，每批事务成功后原子推进游标。
func (s *DurableSink) runImport(ctx context.Context) error {
	if s.currentDB() == nil {
		return ErrNoDatabase
	}
	s.importMu.Lock()
	defer s.importMu.Unlock()
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		cur := s.cursorSnapshot()
		segs, err := s.listSegments()
		if err != nil {
			return err
		}
		idx := resolveSegmentIndex(segs, cur.Segment)
		if idx < 0 {
			return nil
		}
		n, err := s.importSegment(ctx, segs[idx], cur.Offset)
		if err != nil {
			return err
		}
		if n == 0 {
			// 可能只完成了封存段压缩/游标推进，继续处理下一段。
			if s.cursorSnapshot() == cur {
				return nil
			}
			continue
		}
		if s.cursorSnapshot() == cur {
			return nil
		}
	}
}

func resolveSegmentIndex(segs []logSegment, segment string) int {
	if len(segs) == 0 {
		return -1
	}
	if segment == "" {
		return 0
	}
	base := strings.TrimSuffix(segment, ".gz")
	for i, seg := range segs {
		if seg.name == segment || seg.base == base {
			return i
		}
	}
	for i, seg := range segs {
		if seg.base > base {
			return i
		}
	}
	return -1
}

// importSegment 读取单个段：完整行批量入库并推进游标；尾部半行不消费。
// 封存段读尽后 gzip 归档并推进到下一段。
func (s *DurableSink) importSegment(ctx context.Context, seg logSegment, offset int64) (int, error) {
	// 文件被截断/替换时把游标收敛到当前 EOF，避免永久跳过后续追加行。
	if seg.plain && offset > seg.size {
		offset = seg.size
		if err := s.advanceCursor(seg.name, offset); err != nil {
			return 0, err
		}
	}
	reader, closeFn, err := openSegmentReader(seg, offset)
	if err != nil {
		return 0, err
	}
	closed := false
	defer func() {
		if !closed {
			closeFn()
		}
	}()
	buf := bufio.NewReaderSize(reader, 64<<10)
	pos := offset
	pending := make([]config.AppLogEntry, 0, s.opts.BatchSize)
	imported := 0

	flush := func(cursorEnd int64) error {
		if len(pending) == 0 {
			return nil
		}
		if err := config.InsertAppLogBatch(ctx, s.currentDB(), pending); err != nil {
			return err
		}
		imported += len(pending)
		s.imported.Add(int64(len(pending)))
		s.pending.Add(-int64(len(pending)))
		pending = pending[:0]
		if err := s.advanceCursor(seg.name, cursorEnd); err != nil {
			return err
		}
		return nil
	}

	for {
		line, readErr := buf.ReadBytes('\n')
		if len(line) > 0 && line[len(line)-1] == '\n' {
			var rec durableRecord
			if err := json.Unmarshal(line, &rec); err != nil {
				// 完整坏行：先落库此前完整行，再跳过坏行并推进游标，避免永久阻塞。
				if err := flush(pos); err != nil {
					return imported, err
				}
				s.setLastError("日志行解析失败: " + err.Error())
				s.corruptLines.Add(1)
				s.persistIntegrity()
				s.alert("日志行解析失败，已跳过一行: " + err.Error())
				if s.pending.Load() > 0 {
					s.pending.Add(-1)
				}
				pos += int64(len(line))
				if err := s.advanceCursor(seg.name, pos); err != nil {
					return imported, err
				}
				if readErr != nil && !errors.Is(readErr, io.EOF) {
					return imported, readErr
				}
				continue
			}
			entry := rec.entry()
			if entry.LogID == "" {
				entry.LogID = config.NewAppLogID()
			}
			pending = append(pending, entry)
			pos += int64(len(line))
			if len(pending) >= s.opts.BatchSize {
				if err := flush(pos); err != nil {
					return imported, err
				}
			}
		}
		if readErr == nil {
			continue
		}
		if !errors.Is(readErr, io.EOF) {
			return imported, readErr
		}
		if err := flush(pos); err != nil {
			return imported, err
		}
		closed = true
		closeFn()
		return imported, s.finishSegment(seg, pos)
	}
}

// finishSegment 段 EOF 处理：活跃段保持未压缩；封存段读尽后压缩归档并推进游标。
func (s *DurableSink) finishSegment(seg logSegment, pos int64) error {
	if seg.name == s.activeSegmentName() {
		// 活跃段仍在增长：半行等待后续写入，绝不跳过。
		return nil
	}
	next := s.nextSegmentAfter(seg.base)
	if !seg.plain {
		// 归档 gzip 段读尽（或仅剩不可恢复半行）→ 下一段。
		if next != "" {
			return s.advanceCursor(next, 0)
		}
		return nil
	}
	info, err := os.Stat(seg.path)
	if err != nil {
		if os.IsNotExist(err) {
			if next != "" {
				return s.advanceCursor(next, 0)
			}
			return nil
		}
		return err
	}
	size := info.Size()
	if size > pos {
		complete, cerr := hasCompleteLineAfter(seg.path, pos, size)
		if cerr != nil {
			return cerr
		}
		if complete {
			// 封存后又被追加了完整行：保留游标，下一轮补读。
			return nil
		}
		// 封存段尾部是不可恢复半行：跳过并推进，避免永久阻塞后续段。
		s.setLastError("封存段尾部半行不可恢复，已跳过")
		s.corruptLines.Add(1)
		s.persistIntegrity()
		s.alert(fmt.Sprintf("封存段 %s 尾部半行不可恢复，已跳过并进入下一段", seg.name))
		if next != "" {
			return s.advanceCursor(next, 0)
		}
		return nil
	}
	if next == "" {
		return nil
	}
	if err := compressSegment(seg.path); err != nil {
		// 压缩失败（如文件被占用）不阻塞后续段：保留原文件并继续推进游标。
		s.setLastError("段文件压缩失败（保留原文件）: " + err.Error())
		s.alert("段文件压缩失败（保留原文件）: " + err.Error())
	}
	return s.advanceCursor(next, 0)
}

// hasCompleteLineAfter 判断 [offset,size) 内是否存在以换行结尾的完整行。
func hasCompleteLineAfter(path string, offset, size int64) (bool, error) {
	if size <= offset {
		return false, nil
	}
	f, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer func() { _ = f.Close() }()
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return false, err
	}
	reader := bufio.NewReaderSize(io.LimitReader(f, size-offset), 64<<10)
	for {
		line, err := reader.ReadBytes('\n')
		if len(line) > 0 && line[len(line)-1] == '\n' {
			return true, nil
		}
		if errors.Is(err, io.EOF) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
	}
}

func (s *DurableSink) activeSegmentName() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.segment
}

func (s *DurableSink) nextSegmentAfter(base string) string {
	segs, err := s.listSegments()
	if err != nil {
		return ""
	}
	for _, seg := range segs {
		if seg.base > base {
			return seg.name
		}
	}
	return ""
}

func openSegmentReader(seg logSegment, offset int64) (io.Reader, func(), error) {
	f, err := os.Open(seg.path)
	if err != nil {
		return nil, nil, err
	}
	if !seg.plain {
		gz, err := gzip.NewReader(f)
		if err != nil {
			_ = f.Close()
			return nil, nil, err
		}
		if offset > 0 {
			if _, err := io.CopyN(io.Discard, gz, offset); err != nil && !errors.Is(err, io.EOF) {
				_ = gz.Close()
				_ = f.Close()
				return nil, nil, err
			}
		}
		return gz, func() {
			_ = gz.Close()
			_ = f.Close()
		}, nil
	}
	if offset > seg.size {
		offset = seg.size
	}
	if offset > 0 {
		if _, err := f.Seek(offset, io.SeekStart); err != nil {
			_ = f.Close()
			return nil, nil, err
		}
	}
	return f, func() { _ = f.Close() }, nil
}

func compressSegment(path string) error {
	src, err := os.Open(path)
	if err != nil {
		return err
	}
	tmp := path + ".gz.tmp"
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		_ = src.Close()
		return err
	}
	zw := gzip.NewWriter(out)
	if _, err := io.Copy(zw, src); err != nil {
		_ = src.Close()
		_ = zw.Close()
		_ = out.Close()
		_ = os.Remove(tmp)
		return err
	}
	// Windows 下必须先关闭源文件句柄才能删除原段。
	if err := src.Close(); err != nil {
		_ = zw.Close()
		_ = out.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := zw.Close(); err != nil {
		_ = out.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := out.Sync(); err != nil {
		_ = out.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := out.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path+".gz"); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return os.Remove(path)
}

// advanceCursor 用临时文件 + fsync + rename 原子推进游标（DB 事务成功后才调用）。
func (s *DurableSink) advanceCursor(segment string, offset int64) error {
	s.cursorMu.Lock()
	defer s.cursorMu.Unlock()
	cur := durableCursor{Segment: segment, Offset: offset}
	data, err := json.Marshal(cur)
	if err != nil {
		return err
	}
	tmp := s.cursorPath() + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, s.cursorPath()); err != nil {
		_ = os.Remove(s.cursorPath())
		if err2 := os.Rename(tmp, s.cursorPath()); err2 != nil {
			_ = os.Remove(tmp)
			return err
		}
	}
	s.cursor = cur
	return nil
}

// recountPending 按游标/文件实际内容重算未导入完整行数（重启后 Health.Pending 不失真）。
func (s *DurableSink) recountPending() {
	segs, err := s.listSegments()
	if err != nil {
		s.alert("扫描待导入日志失败: " + err.Error())
		return
	}
	cur := s.cursorSnapshot()
	idx := resolveSegmentIndex(segs, cur.Segment)
	if idx < 0 {
		s.pending.Store(0)
		return
	}
	var total int64
	for i := idx; i < len(segs); i++ {
		offset := int64(0)
		if i == idx {
			offset = cur.Offset
		}
		reader, closeFn, err := openSegmentReader(segs[i], offset)
		if err != nil {
			s.alert("扫描待导入日志失败: " + err.Error())
			continue
		}
		total += countCompleteLines(reader)
		closeFn()
	}
	s.pending.Store(total)
}

// countCompleteLines 统计以换行结尾的完整行；尾部半行不计。
func countCompleteLines(reader io.Reader) int64 {
	br := bufio.NewReaderSize(reader, 64<<10)
	var n int64
	for {
		line, err := br.ReadBytes('\n')
		if len(line) > 0 && line[len(line)-1] == '\n' {
			n++
		}
		if err != nil {
			return n
		}
	}
}

func (s *DurableSink) setLastError(msg string) {
	s.errMu.Lock()
	s.lastErr = msg
	s.errMu.Unlock()
}

func (s *DurableSink) lastError() string {
	s.errMu.Lock()
	defer s.errMu.Unlock()
	return s.lastErr
}

// alert 写 stderr（串行化并按消息节流，避免磁盘/DB 持续故障刷屏）。
func (s *DurableSink) alert(msg string) {
	s.alertMu.Lock()
	defer s.alertMu.Unlock()
	now := time.Now()
	if msg == s.lastAlertMsg && now.Sub(s.lastAlert) < 30*time.Second {
		return
	}
	s.lastAlertMsg, s.lastAlert = msg, now
	fmt.Fprintln(s.opts.Stderr, "[logging] "+msg)
}

// Health 返回健康快照。
func (s *DurableSink) Health() Health {
	if s == nil {
		return Health{}
	}
	s.mu.Lock()
	active, size := s.segment, s.size
	s.mu.Unlock()
	s.dbMu.RLock()
	connected := s.db != nil
	s.dbMu.RUnlock()
	cur := s.cursorSnapshot()
	written, imported := s.written.Load(), s.imported.Load()
	pending := s.pending.Load()
	if pending < 0 {
		pending = 0
	}
	archived := 0
	if entries, err := os.ReadDir(s.dir); err == nil {
		for _, e := range entries {
			if strings.HasSuffix(e.Name(), segmentArchiveExt) {
				archived++
			}
		}
	}
	var last time.Time
	if ns := s.lastImport.Load(); ns > 0 {
		last = time.Unix(0, ns)
	}
	return Health{
		Dir: s.dir, ActiveSegment: active, SegmentBytes: size,
		Written: written, Imported: imported, Pending: pending,
		WriteErrors: s.writeErrors.Load(), DiskFallbacks: s.diskFallbacks.Load(),
		FsyncErrors: s.fsyncErrors.Load(), CorruptLines: s.corruptLines.Load(),
		InsertErrors: s.insertErrors.Load(), Syncs: s.syncs.Load(),
		ArchivedSegments: archived, DBConnected: connected,
		CursorSegment: cur.Segment, CursorOffset: cur.Offset,
		LastImport: last, LastError: s.lastError(),
	}
}

// ArchiveCoverage 返回最早归档段的日期（无归档时为 false）。
func (s *DurableSink) ArchiveCoverage() (time.Time, bool) {
	if s == nil {
		return time.Time{}, false
	}
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return time.Time{}, false
	}
	var oldest time.Time
	found := false
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, segmentArchiveExt) {
			continue
		}
		day, ok := segmentDay(name)
		if !ok {
			continue
		}
		if !found || day.Before(oldest) {
			oldest, found = day, true
		}
	}
	return oldest, found
}

// CleanupArchives 删除超过保留天数的 .log.gz；retainDays=0 永久保留，非法值报错且不删除。
// 只处理归档，绝不删除活跃/封存的 .log。
func (s *DurableSink) CleanupArchives(retainDays int) (int, error) {
	if s == nil {
		return 0, nil
	}
	if retainDays == 0 {
		return 0, nil
	}
	if retainDays < 0 || retainDays > config.AppLogMaxRetentionDays {
		return 0, fmt.Errorf("logging: 归档保留天数 %d 越界（0 永久或 1-%d）", retainDays, config.AppLogMaxRetentionDays)
	}
	// 与 Importer 互斥：不删除正在读取/压缩的段；游标所在段永不删除。
	s.importMu.Lock()
	defer s.importMu.Unlock()
	cur := s.cursorSnapshot()
	cursorBase := strings.TrimSuffix(cur.Segment, ".gz")
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return 0, err
	}
	cutoff := time.Now().AddDate(0, 0, -retainDays)
	removed := 0
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, segmentArchiveExt) {
			continue
		}
		if strings.TrimSuffix(name, ".gz") == cursorBase {
			continue
		}
		day, ok := segmentDay(name)
		if !ok {
			info, ierr := e.Info()
			if ierr != nil {
				continue
			}
			day = info.ModTime()
		}
		if !day.Before(cutoff) {
			continue
		}
		if err := os.Remove(filepath.Join(s.dir, name)); err != nil {
			return removed, err
		}
		removed++
	}
	return removed, nil
}

// CloseContext 先停止接收、排空在飞写入并 fsync，再停止 Importer；
// Importer 超时不影响已写入文件的尾部日志（下次启动按游标补录）。
func (s *DurableSink) CloseContext(ctx context.Context) error {
	if s == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	var syncErr error
	s.closeOnce.Do(func() {
		s.mu.Lock()
		s.closed = true
		s.mu.Unlock()
		syncErr = s.syncFile()
		s.mu.Lock()
		if s.file != nil {
			_ = s.file.Close()
			s.file = nil
		}
		s.mu.Unlock()
		s.stopOnce.Do(func() { close(s.stopCh) })
	})
	s.dbMu.RLock()
	started := s.attached
	s.dbMu.RUnlock()
	if !started {
		return syncErr
	}
	select {
	case <-s.doneCh:
		return syncErr
	case <-ctx.Done():
		return ctx.Err()
	}
}

// LogIDSet 扫描全部段文件返回 log_id 集合；坏行/半行/空 log_id 返回错误（保留清理必须失败关闭）。
// 持 importMu 与 Importer/归档清理互斥，避免读到正在压缩或被删除的段。
func (s *DurableSink) LogIDSet(ctx context.Context) (map[string]struct{}, error) {
	if s == nil {
		return nil, errors.New("logging: 日志持久层未接入")
	}
	s.importMu.Lock()
	defer s.importMu.Unlock()
	segs, err := s.listSegments()
	if err != nil {
		return nil, err
	}
	out := make(map[string]struct{})
	for _, seg := range segs {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		reader, closeFn, err := openSegmentReader(seg, 0)
		if err != nil {
			return nil, fmt.Errorf("读取日志段 %s: %w", seg.name, err)
		}
		br := bufio.NewReaderSize(reader, 64<<10)
		for {
			line, readErr := br.ReadBytes('\n')
			if len(line) > 0 {
				if line[len(line)-1] != '\n' {
					closeFn()
					return nil, fmt.Errorf("日志段 %s 存在半行，无法证明文件完整", seg.name)
				}
				body := bytes.TrimSpace(line)
				if len(body) > 0 {
					var rec durableRecord
					if err := json.Unmarshal(body, &rec); err != nil {
						closeFn()
						return nil, fmt.Errorf("日志段 %s 存在坏行: %w", seg.name, err)
					}
					if rec.LogID == "" {
						closeFn()
						return nil, fmt.Errorf("日志段 %s 存在空 log_id 行，无法与数据库对账", seg.name)
					}
					out[rec.LogID] = struct{}{}
				}
			}
			if readErr == nil {
				continue
			}
			closeFn()
			if errors.Is(readErr, io.EOF) {
				break
			}
			return nil, fmt.Errorf("读取日志段 %s: %w", seg.name, readErr)
		}
	}
	return out, nil
}

// SegmentDays 返回所有段文件（.log/.log.gz）覆盖的日期集合（YYYY-MM-DD，本地日）。
// 供保留清理校验待删数据库行是否都有文件副本。
func (s *DurableSink) SegmentDays() (map[string]bool, error) {
	segs, err := s.listSegments()
	if err != nil {
		return nil, err
	}
	out := make(map[string]bool, len(segs))
	for _, seg := range segs {
		if day, ok := segmentDay(seg.name); ok {
			out[day.Format("2006-01-02")] = true
		}
	}
	return out, nil
}
