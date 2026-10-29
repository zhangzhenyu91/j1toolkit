package ocr

import (
	"context"
	"errors"
	"fmt"
	"sync"
)

// 帧 ID 编码常量（同 JS _queueOcr：epoch*1e6 + (seq%500000)*2 + 框别奇偶）。
const (
	frameIDEpochSpan = 1000000
	frameIDSeqSpan   = 500000
)

// EncodeFrameID 帧 ID 编码：epoch*1e6 + (seq%5e5)*2 + 框别（0=A、1=B）。
// 结果侧解码见 DecodeFrameID（JS：(id/1e6)|0 取 epoch、id%2 取框别）。
func EncodeFrameID(epoch int64, seq int, box BoxID) int64 {
	return epoch*frameIDEpochSpan + int64(seq%frameIDSeqSpan)*2 + int64(int(box)&1)
}

// DecodeFrameID 帧 ID 解码：epoch 与框别（奇偶承载）。
func DecodeFrameID(id int64) (epoch int64, box BoxID) {
	epoch = id / frameIDEpochSpan
	if id%2 != 0 {
		box = BoxB
	} else {
		box = BoxA
	}
	return epoch, box
}

// Frame 一帧待识别位图（密集 BGRA，w*h*4）。
type Frame struct {
	ID            int64
	BGRA          []byte
	Width, Height int
}

// FrameResult 单帧识别结果。Epoch/Box 解码自帧 ID；OK=false 时 Err 非 nil。
// 同步请求（Recognize）的结果亦走此结构（ID 为 0 仅作占位）。
type FrameResult struct {
	ID     int64
	Epoch  int64
	Box    BoxID
	OK     bool
	Err    error
	Result Result
}

// EngineConfig 常驻 OCR 引擎配置。
type EngineConfig struct {
	// Factory det/rec 会话工厂（ORT 实现见子包 ort；测试注入 fake）。
	Factory SessionFactory
	// ModelsDir 模型目录（det.onnx/rec.onnx/ppocr_keys.txt）。
	ModelsDir string
	// QueueSize 帧队列深度（默认 4；inFlight 每框限一帧，队列满即丢帧）。
	QueueSize int
}

// Engine 常驻 OCR 引擎：持有 det/rec 会话（模型加载有耗时）的单个 worker goroutine
// 顺序消费帧（同 JS worker_threads 单线程语义），主线程/界面不被 CPU 密集计算阻塞；
// epoch 语义同 JS：停止/重启时 NextEpoch 作废旧帧结果（id 编码见 EncodeFrameID）。
type Engine struct {
	cfg     EngineConfig
	jobs    chan frameJob
	results chan FrameResult
	done    chan struct{}
	quit    chan struct{}

	mu        sync.Mutex
	epoch     int64
	seq       int
	inFlight  map[BoxID]bool
	started   bool
	closed    bool
	closeOnce sync.Once

	startOnce sync.Once
	startErr  error
}

// frameJob worker 队列项：扫描帧（reply 为 nil，结果走 Results 通道、按 epoch 过滤）
// 或同步请求（reply 非 nil，识别完直接回执，不做过滤）。
type frameJob struct {
	frame Frame
	epoch int64
	box   BoxID
	reply chan FrameResult
}

// NewEngine 创建引擎（会话未加载；Start 时加载）。
func NewEngine(cfg EngineConfig) (*Engine, error) {
	if cfg.Factory == nil {
		return nil, errors.New("ocr: SessionFactory 不可为 nil")
	}
	if cfg.ModelsDir == "" {
		return nil, errors.New("ocr: ModelsDir 不可为空")
	}
	q := cfg.QueueSize
	if q <= 0 {
		q = 4
	}
	return &Engine{
		cfg:      cfg,
		jobs:     make(chan frameJob, q),
		results:  make(chan FrameResult, q),
		done:     make(chan struct{}),
		quit:     make(chan struct{}),
		inFlight: map[BoxID]bool{},
	}, nil
}

// Start 加载模型并启动常驻 worker goroutine（阻塞至模型就绪；失败返回错误，
// 对齐 JS worker 的 init → ready/error 时序）。幂等：重复调用返回同一结果。
func (e *Engine) Start(ctx context.Context) error {
	e.startOnce.Do(func() {
		e.startErr = e.start(ctx)
	})
	return e.startErr
}

func (e *Engine) start(ctx context.Context) error {
	det, err := e.cfg.Factory.OpenDet(e.cfg.ModelsDir)
	if err != nil {
		return fmt.Errorf("ocr: 加载 det 会话失败: %w", err)
	}
	rec, err := e.cfg.Factory.OpenRec(e.cfg.ModelsDir)
	if err != nil {
		_ = det.Close()
		return fmt.Errorf("ocr: 加载 rec 会话失败: %w", err)
	}
	keys, err := LoadKeys(e.cfg.ModelsDir + "/ppocr_keys.txt")
	if err != nil {
		_ = det.Close()
		_ = rec.Close()
		return fmt.Errorf("ocr: 读取识别字典失败: %w", err)
	}
	p, err := NewPipeline(det, rec, BuildChars(keys))
	if err != nil {
		_ = det.Close()
		_ = rec.Close()
		return err
	}
	e.mu.Lock()
	e.started = true
	e.mu.Unlock()
	go e.worker(ctx, p)
	return nil
}

// worker 常驻消费 goroutine：顺序执行推理，退出时关闭会话与结果通道。
// jobs 通道永不关闭（Close 只拉起 quit）：Submit/Recognize 与 Close 并发时向 jobs
// 投递不会触发 send on closed channel panic（已踩坑）；已入队未处理的帧随退出丢弃。
func (e *Engine) worker(ctx context.Context, p *Pipeline) {
	defer close(e.done)
	defer close(e.results)
	defer func() {
		_ = p.det.Close()
		_ = p.rec.Close()
	}()
	for {
		select {
		case <-ctx.Done():
			return
		case <-e.quit:
			return
		case job, ok := <-e.jobs:
			if !ok {
				return
			}
			e.process(p, job)
		}
	}
}

// process 执行单帧识别并分发结果（结果通道背压时可被 quit 打断，避免关停死锁）。
func (e *Engine) process(p *Pipeline, job frameJob) {
	res, err := p.Run(job.frame.BGRA, job.frame.Width, job.frame.Height)
	out := FrameResult{
		ID:     job.frame.ID,
		Epoch:  job.epoch,
		Box:    job.box,
		OK:     err == nil,
		Err:    err,
		Result: res,
	}
	if job.reply != nil {
		job.reply <- out
		return
	}
	e.mu.Lock()
	stale := job.epoch != e.epoch
	if !stale {
		delete(e.inFlight, job.box)
	}
	e.mu.Unlock()
	if stale {
		return // 已停止/重启，丢弃旧帧（同 JS _onWorkerMessage）
	}
	select {
	case e.results <- out:
	case <-e.quit:
	case <-e.done:
	}
}

// Submit 提交一帧扫描位图（框别 A/B）。返回分配的帧 ID 与是否受理：
// 引擎未就绪、同框已有在途帧（JS inFlight 限流）或队列满时不受理。
func (e *Engine) Submit(box BoxID, bgra []byte, width, height int) (int64, bool) {
	e.mu.Lock()
	if !e.started || e.closed || e.inFlight[box] {
		e.mu.Unlock()
		return 0, false
	}
	id := EncodeFrameID(e.epoch, e.seq, box)
	e.seq++
	e.inFlight[box] = true
	epoch := e.epoch
	e.mu.Unlock()
	job := frameJob{
		frame: Frame{ID: id, BGRA: bgra, Width: width, Height: height},
		epoch: epoch,
		box:   box,
	}
	select {
	case e.jobs <- job:
		return id, true
	default:
		e.mu.Lock()
		delete(e.inFlight, box)
		e.mu.Unlock()
		return 0, false
	}
}

// Results 帧结果通道（worker 退出时关闭；旧 epoch 结果不入列）。
func (e *Engine) Results() <-chan FrameResult { return e.results }

// NextEpoch 推进 epoch 并作废全部在途帧结果（停止/重启时调用，同 JS ocrEpoch++）。
// 在途帧的 inFlight 标志同步清空（对齐 JS worker 退出重置）。
func (e *Engine) NextEpoch() int64 {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.epoch++
	e.inFlight = map[BoxID]bool{}
	return e.epoch
}

// Epoch 当前 epoch 值。
func (e *Engine) Epoch() int64 {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.epoch
}

// Ready 模型是否已加载、worker 是否在跑。
func (e *Engine) Ready() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.started && !e.closed
}

// RecognizePNG 识别一张 golden PNG（解码 → 同步走常驻 worker 识别），
// 供 selftest/冒烟与 quizsearch 离线验证用。
func (e *Engine) RecognizePNG(ctx context.Context, pngPath string) (Result, error) {
	bgra, w, h, err := LoadPNGToBGRA(pngPath)
	if err != nil {
		return Result{}, err
	}
	return e.Recognize(ctx, bgra, w, h)
}

// Recognize 同步识别一帧位图（不占 inFlight、不按 epoch 过滤；复用常驻会话）。
func (e *Engine) Recognize(ctx context.Context, bgra []byte, width, height int) (Result, error) {
	e.mu.Lock()
	started, closed := e.started, e.closed
	e.mu.Unlock()
	if !started || closed {
		return Result{}, errors.New("ocr: 引擎未就绪")
	}
	reply := make(chan FrameResult, 1)
	job := frameJob{
		frame: Frame{BGRA: bgra, Width: width, Height: height},
		reply: reply,
	}
	select {
	case e.jobs <- job:
	case <-ctx.Done():
		return Result{}, ctx.Err()
	case <-e.quit:
		return Result{}, errors.New("ocr: 引擎已关闭")
	case <-e.done:
		return Result{}, errors.New("ocr: 引擎已退出")
	}
	select {
	case out := <-reply:
		return out.Result, out.Err
	case <-ctx.Done():
		return Result{}, ctx.Err()
	case <-e.quit:
		return Result{}, errors.New("ocr: 引擎已关闭")
	case <-e.done:
		return Result{}, errors.New("ocr: 引擎已退出")
	}
}

// Close 停止受理并退出 worker（幂等）：置 closed 后拉起 quit，worker 退出时关闭
// 会话与结果通道。不关闭 jobs 通道——Submit/Recognize 的发送与 Close 并发时
// 安全（send on closed channel 竞态，已踩坑）；在途帧按丢弃处理（结果经
// epoch/quit 收口，不再排空推理）。
func (e *Engine) Close() error {
	e.closeOnce.Do(func() {
		e.mu.Lock()
		e.closed = true
		started := e.started
		e.mu.Unlock()
		close(e.quit)
		if started {
			<-e.done
		}
	})
	return nil
}
