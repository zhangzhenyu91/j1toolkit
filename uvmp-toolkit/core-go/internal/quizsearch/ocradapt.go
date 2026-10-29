// 题库搜题：ocr.Engine → quizsearch.Engine 适配（Go 核心常驻 OCR 引擎接入扫描编排）。
//
// 两者契约差异（适配层负责抹平，扫描编排语义不变）：
//   - 任务 id：本包 Submit 由调用方编码（EncodeTaskID：扫描 epoch + 框别奇偶），
//     ocr.Engine.Submit 自行生成帧 id（EncodeFrameID：引擎 epoch + 自增 seq）；
//     适配层维护「帧 id → 任务 id」映射并在结果侧还原，扫描 epoch 作废过滤才成立；
//   - 提交回执：本包 Submit 无回执（扫描侧按「每框在途限一」记账，必有结果回来清账）；
//     ocr.Engine.Submit 返回是否受理（同框在途限一 / 队列满丢帧）。被拒帧补发失败结果，
//     否则扫描侧 inFlight 永不清除、该框停摆；
//   - 结果形状：OCRResult{Text, Lines []string, ElapsedMs, Error string} 对
//     ocr.FrameResult{Result{Text, Lines []Line, ElapsedMs}, Err error}。
package quizsearch

import (
	"context"
	"errors"
	"sync"

	"github.com/zhangzhenyu91/j1toolkit/uvmp-toolkit/core-go/internal/ocr"
)

// ocrAdapter 常驻 ocr.Engine 的 Engine 适配。
type ocrAdapter struct {
	factory ocr.SessionFactory

	results chan OCRResult
	rejects chan OCRResult
	started chan struct{} // Init 成功后关闭
	stopped chan struct{} // Close 时关闭

	mu        sync.Mutex
	eng       *ocr.Engine
	modelsDir string
	ids       map[int64]int64 // 引擎帧 id → 调用方任务 id
	inited    bool
	closed    bool

	stopOnce sync.Once
	initMu   sync.Mutex // 串行化 Init（Start 加载模型有耗时，防并发双建引擎）
}

// NewOCREngineFactory 用 Go 核心 ocr.Engine 构造扫描引擎工厂（Start/Stop 各建一个实例，
// 对齐 worker 线程 fork/terminate 生命周期）。factory 为 ORT 实现（ocr/ort 子包）或测试 fake。
func NewOCREngineFactory(factory ocr.SessionFactory) EngineFactory {
	return func() Engine {
		return newOCRAdapter(factory)
	}
}

func newOCRAdapter(factory ocr.SessionFactory) *ocrAdapter {
	a := &ocrAdapter{
		factory: factory,
		results: make(chan OCRResult, 8),
		rejects: make(chan OCRResult, 8),
		started: make(chan struct{}),
		stopped: make(chan struct{}),
		ids:     map[int64]int64{},
	}
	// 结果转发协程构造即启动：Init 未成功 / 从未 Init 时 Close 也能收口结果流。
	go a.pump()
	return a
}

var _ Engine = (*ocrAdapter)(nil)

// Init 加载模型并启动常驻引擎（阻塞至模型就绪）。
func (a *ocrAdapter) Init(modelsDir string) error {
	a.initMu.Lock()
	defer a.initMu.Unlock()
	a.mu.Lock()
	if a.closed {
		a.mu.Unlock()
		return errors.New("ocr: 引擎已关闭")
	}
	if a.inited {
		a.mu.Unlock()
		return nil
	}
	a.mu.Unlock()
	eng, err := ocr.NewEngine(ocr.EngineConfig{Factory: a.factory, ModelsDir: modelsDir})
	if err != nil {
		return err
	}
	if err := eng.Start(context.Background()); err != nil {
		return err
	}
	a.mu.Lock()
	if a.closed {
		a.mu.Unlock()
		_ = eng.Close()
		return errors.New("ocr: 引擎已关闭")
	}
	a.eng = eng
	a.modelsDir = modelsDir
	a.inited = true
	close(a.started)
	a.mu.Unlock()
	return nil
}

// Submit 提交一帧识别任务。id 由 EncodeTaskID 生成；引擎拒帧时补发失败结果清扫描侧记账。
func (a *ocrAdapter) Submit(id int64, bitmap []byte, width, height int) {
	box := ocr.BoxA
	if id%2 != 0 {
		box = ocr.BoxB
	}
	a.mu.Lock()
	eng, closed := a.eng, a.closed
	a.mu.Unlock()
	if eng == nil || closed {
		a.reject(id, "OCR 引擎未就绪")
		return
	}
	frameID, ok := eng.Submit(box, bitmap, width, height)
	if !ok {
		a.reject(id, "OCR 引擎忙，本帧跳过")
		return
	}
	a.mu.Lock()
	a.ids[frameID] = id
	a.mu.Unlock()
}

// reject 补发失败结果（丢最旧保最新，不阻塞截屏轮询）。
func (a *ocrAdapter) reject(id int64, msg string) {
	r := OCRResult{ID: id, OK: false, Error: msg}
	for {
		select {
		case a.rejects <- r:
			return
		case <-a.stopped:
			return
		default:
			select {
			case <-a.rejects:
			default:
			}
		}
	}
}

// pump 结果转发协程：唯一的结果通道关闭者（Init 未成功时 Close 也能收口）。
func (a *ocrAdapter) pump() {
	defer close(a.results)
	select {
	case <-a.started:
	case <-a.stopped:
		return
	}
	a.mu.Lock()
	inner := a.eng.Results()
	a.mu.Unlock()
	for {
		select {
		case fr, ok := <-inner:
			if !ok {
				return
			}
			a.deliver(a.convert(fr))
		case r := <-a.rejects:
			a.deliver(r)
		case <-a.stopped:
			return
		}
	}
}

// convert 引擎帧结果 → 扫描结果（帧 id 还原为调用方任务 id）。
func (a *ocrAdapter) convert(fr ocr.FrameResult) OCRResult {
	a.mu.Lock()
	id, known := a.ids[fr.ID]
	delete(a.ids, fr.ID)
	a.mu.Unlock()
	if !known {
		id = fr.ID
	}
	lines := make([]string, 0, len(fr.Result.Lines))
	for _, l := range fr.Result.Lines {
		lines = append(lines, l.Text)
	}
	r := OCRResult{
		ID:        id,
		OK:        fr.OK,
		Text:      fr.Result.Text,
		Lines:     lines,
		ElapsedMs: fr.Result.ElapsedMs,
	}
	if fr.Err != nil {
		r.OK = false
		r.Error = fr.Err.Error()
	}
	return r
}

// deliver 结果入列（丢最旧保最新；pump 停止后丢弃）。
func (a *ocrAdapter) deliver(r OCRResult) {
	for {
		select {
		case a.results <- r:
			return
		case <-a.stopped:
			return
		default:
			select {
			case <-a.results:
			default:
			}
		}
	}
}

// Results 识别结果流；Close 后关闭。
func (a *ocrAdapter) Results() <-chan OCRResult { return a.results }

// Close 释放引擎（幂等）：停止受理、退出常驻 worker、关闭结果流。
func (a *ocrAdapter) Close() {
	a.stopOnce.Do(func() {
		a.mu.Lock()
		a.closed = true
		eng := a.eng
		a.mu.Unlock()
		close(a.stopped)
		if eng != nil {
			_ = eng.Close()
		}
	})
}

// Selftest 引擎自检细节（推理库版本/就绪态；模型文件齐备性由 Scanner.Selftest 检查）。
func (a *ocrAdapter) Selftest() map[string]any {
	a.mu.Lock()
	eng, modelsDir, inited := a.eng, a.modelsDir, a.inited
	a.mu.Unlock()
	out := map[string]any{"ok": true, "engine": "ocr.Engine", "ready": inited, "models_dir": modelsDir}
	if v, ok := a.factory.(ocr.RuntimeVersioner); ok {
		out["ort_version"] = v.RuntimeVersion()
	}
	if eng != nil {
		out["ready"] = eng.Ready()
	}
	return out
}
