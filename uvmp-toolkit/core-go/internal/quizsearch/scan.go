// 题库搜题：扫描服务编排（移植自 desktop/electron/quizsearch/index.cjs 的非 OCR 部分）。
// 职责：扫描框/结果窗生命周期、截屏轮询、OCR 引擎编排（接口注入，不绑定具体实现）、
// 题库匹配、设置与题库持久化、事件推送。设计要点（同 .cjs）：
//   - 双扫描框（A/B）独立拖动/缩放/显隐，几何持久化；两框各自 OCR 各自匹配，结果窗分区展示；
//   - 截屏按框裁剪（按所在显示器 scaleFactor 换算物理像素），逐帧哈希，画面未变跳过 OCR；
//   - 帧结果带 epoch（停止/重启作废旧帧）与框别（任务 id 低位奇偶），在途帧每框限一；
//   - _tick 忙门闩：截屏未完不重入，防并发抓帧互相拖慢甚至取到空画面。
package quizsearch

import (
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/png"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/zhangzhenyu91/j1toolkit/uvmp-toolkit/core-go/internal/overlay"
)

// BoxID 扫描框编号（A/B 双框）。
type BoxID string

const (
	BoxA BoxID = "A"
	BoxB BoxID = "B"
)

// OCRResult 一帧识别结果（对齐 ocr-worker.cjs 的 ocr-result 消息）。
type OCRResult struct {
	ID        int64
	OK        bool
	Text      string
	Lines     []string
	ElapsedMs int64
	Error     string
}

// Engine 本地 OCR 引擎接口：扫描编排只依赖本接口，
// 具体实现（onnxruntime 等）由调用方注入，避免绑定具体 OCR 落地。
type Engine interface {
	// Init 加载模型（modelsDir）；返回 nil 进入就绪态（对齐 worker 的 init→ready/error 语义）。
	Init(modelsDir string) error
	// Submit 提交一帧识别任务。bitmap 为稠密 BGRA8888（len=width*height*4），提交后所有权移交引擎。
	// id 由 EncodeTaskID 生成；结果异步投递到 Results()。
	Submit(id int64, bitmap []byte, width, height int)
	// Results 识别结果流；Close 后由实现关闭。
	Results() <-chan OCRResult
	// Close 释放引擎（幂等）。
	Close()
}

// EngineSelftester 可选扩展：具体引擎补充自检细节（如推理库版本/指令集）。
type EngineSelftester interface {
	Selftest() map[string]any
}

// EngineFactory 引擎工厂：Start 创建、Stop 销毁（对齐 worker 线程 fork/terminate 生命周期）。
type EngineFactory func() Engine

// BoxState 各框最新匹配（结果窗数据源）。
type BoxState struct {
	Matched bool      `json:"matched"`
	Score   float64   `json:"score,omitempty"`
	Q       *Question `json:"q,omitempty"`
	OCRText string    `json:"ocrText"`
}

// ResultState 结果窗推送载荷（对齐 quizsearch:state IPC payload）。
type ResultState struct {
	Boxes   map[string]*BoxState `json:"boxes"`
	OcrMs   int64                `json:"ocrMs"`
	Visible map[string]bool      `json:"visible"`
}

// PageEvent 页面事件（对齐 quizsearch:event IPC payload）。
// Type：scan（启停）/ engine（引擎就绪或失败）/ boxHidden（扫描框被隐藏）。
type PageEvent struct {
	Type     string `json:"type"`
	Scanning bool   `json:"scanning,omitempty"`
	Ready    bool   `json:"ready"`
	Error    string `json:"error,omitempty"`
	Box      string `json:"box,omitempty"`
}

// BoxDebug 各框最近 OCR 摘要（诊断用）。
type BoxDebug struct {
	Matched bool   `json:"matched"`
	Snippet string `json:"snippet"`
}

// State 服务状态（对齐 quizSearch:getState 返回形状）。
type State struct {
	Scanning      bool                 `json:"scanning"`
	EngineReady   bool                 `json:"engineReady"`
	EngineError   string               `json:"engineError"`
	CaptureError  string               `json:"captureError"`
	SessionType   string               `json:"sessionType"`
	OcrMs         int64                `json:"ocrMs"`
	BoxDebug      map[string]*BoxDebug `json:"boxDebug"`
	Settings      Settings             `json:"settings"`
	Banks         []BankMeta           `json:"banks"`
	QuestionCount int                  `json:"questionCount"`
}

// MatchOutcome 题干文本匹配自测结果（对齐 quizSearch:testMatch 返回形状）。
type MatchOutcome struct {
	Matched  bool      `json:"matched"`
	Score    float64   `json:"score"`
	Question *Question `json:"question"`
	Near     *Question `json:"near"`
	Norm     string    `json:"norm"`
}

// SelftestResult 引擎自检（模型文件齐备性；引擎细节经 EngineSelftester 附加）。
type SelftestResult struct {
	OK        bool           `json:"ok"`
	ModelsDir string         `json:"modelsDir"`
	Missing   []string       `json:"missing"`
	Engine    map[string]any `json:"engine,omitempty"`
}

// ModelFiles OCR 模型目录必需文件（自检四文件口径，与旧 Electron index.cjs selftest 一致：
// det/rec/cls + 词表；cls.onnx 随模型提供，det/rec 之外的分类器不参与识别管线但计入齐备性）。
var ModelFiles = []string{"det.onnx", "rec.onnx", "cls.onnx", "ppocr_keys.txt"}

// Config 扫描服务装配参数。
type Config struct {
	DataDir           string              // 数据目录（settings.json + banks-*）
	ModelsDir         string              // OCR 模型目录
	NewEngine         EngineFactory       // OCR 引擎工厂（注入，可 fake）
	Host              overlay.Host        // 悬浮窗/截屏宿主（注入，可 fake）
	OnScanStateChange func(scanning bool) // 启停回调（托盘菜单刷新等）
}

// Scanner 扫描服务（A/B 双框编排）。
type Scanner struct {
	cfg   Config
	store *BankStore

	settings     Settings
	settingsPath string

	mu          sync.Mutex
	scanning    bool
	engine      Engine
	engineReady bool
	engineError string
	ocrEpoch    int64
	ocrSeq      int64
	inFlight    map[BoxID]bool
	lastHash    map[BoxID]uint32
	lastOcrMs   int64
	tickErr     string
	tickBusy    atomic.Bool
	bankIndex   []IndexEntry
	indexDirty  bool
	boxState    map[BoxID]*BoxState
	lastPushed  string

	boxWins   map[BoxID]overlay.Window
	resultWin overlay.Window

	stopTimer chan struct{}

	events  chan PageEvent
	results chan ResultState

	wg sync.WaitGroup
}

// New 装配扫描服务（加载设置与题库目录）。
func New(cfg Config) (*Scanner, error) {
	if cfg.Host == nil {
		return nil, errors.New("缺少 overlay.Host")
	}
	if cfg.DataDir == "" {
		return nil, errors.New("缺少数据目录")
	}
	if err := os.MkdirAll(cfg.DataDir, 0o755); err != nil {
		return nil, fmt.Errorf("创建数据目录失败：%w", err)
	}
	store, err := NewBankStore(cfg.DataDir)
	if err != nil {
		return nil, err
	}
	s := &Scanner{
		cfg:          cfg,
		store:        store,
		settingsPath: filepath.Join(cfg.DataDir, "settings.json"),
		inFlight:     map[BoxID]bool{BoxA: false, BoxB: false},
		lastHash:     map[BoxID]uint32{},
		boxState:     map[BoxID]*BoxState{BoxA: nil, BoxB: nil},
		boxWins:      map[BoxID]overlay.Window{},
		events:       make(chan PageEvent, 32),
		results:      make(chan ResultState, 32),
	}
	s.settings = LoadSettings(s.settingsPath, workAreaOf(cfg.Host.Primary()))
	return s, nil
}

func workAreaOf(d overlay.Display) WorkArea {
	return WorkArea{X: d.WorkArea.X, Y: d.WorkArea.Y, W: d.WorkArea.W, H: d.WorkArea.H}
}

// Events 页面事件流（scan/engine/boxHidden）。缓冲溢出丢最旧保最新。
func (s *Scanner) Events() <-chan PageEvent { return s.events }

// Results 结果窗状态流（匹配变化时推送）。缓冲溢出丢最旧保最新。
func (s *Scanner) Results() <-chan ResultState { return s.results }

// ---------------------------------------------------------------- 任务 id 编码

// EncodeTaskID 生成 OCR 任务 id：epoch*1000000 + (seq%500000)*2 + 框别奇偶（A=0/B=1）。
// 结果侧据 id 高位判 epoch 作废、低位判框别（同 .cjs _queueOcr）。
func EncodeTaskID(epoch int64, seq int, box BoxID) int64 {
	parity := int64(0)
	if box == BoxB {
		parity = 1
	}
	return epoch*1000000 + int64(seq%500000)*2 + parity
}

// DecodeTaskID 还原任务 id 的 epoch 与框别。
func DecodeTaskID(id int64) (epoch int64, box BoxID) {
	epoch = id / 1000000
	if id%2 == 0 {
		return epoch, BoxA
	}
	return epoch, BoxB
}

// ---------------------------------------------------------------- 设置与题库

func (s *Scanner) box(id BoxID) *BoxGeom {
	if id == BoxB {
		return &s.settings.Boxes.B
	}
	return &s.settings.Boxes.A
}

func (s *Scanner) saveSettings() { SaveSettings(s.settingsPath, s.settings) }

// bankState 题库列表与启用库合并题数。
func (s *Scanner) bankState() ([]BankMeta, int) {
	return s.store.List(), len(s.store.AllEnabledQuestions())
}

func (s *Scanner) rebuildIndex() {
	s.bankIndex = BuildIndex(s.store.AllEnabledQuestions())
	s.indexDirty = false
}

// ---------------------------------------------------------------- 启停

// Start 开始搜题：需已有题库；创建引擎/扫描框/结果窗并启动截屏轮询。
func (s *Scanner) Start() (*State, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.scanning {
		return s.stateLocked(), nil
	}
	if len(s.store.List()) == 0 {
		return nil, errors.New("请先导入题库")
	}
	s.scanning = true
	s.ocrEpoch++ // 停止/重启时作废旧帧结果
	s.lastHash = map[BoxID]uint32{}
	s.boxState = map[BoxID]*BoxState{BoxA: nil, BoxB: nil}
	s.lastPushed = ""
	s.tickErr = ""
	s.engineReady = false
	s.engineError = ""

	s.startEngineLocked()
	s.syncBoxWindowsLocked()
	s.createResultWindowLocked()

	s.stopTimer = make(chan struct{})
	interval := s.settings.Interval
	stopCh := s.stopTimer
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		t := time.NewTicker(time.Duration(interval) * time.Millisecond)
		defer t.Stop()
		for {
			select {
			case <-stopCh:
				return
			case <-t.C:
				s.tick()
			}
		}
	}()

	if s.cfg.OnScanStateChange != nil {
		s.cfg.OnScanStateChange(true)
	}
	s.pushPageEvent(PageEvent{Type: "scan", Scanning: true, Ready: true})
	return s.stateLocked(), nil
}

// Stop 停止搜题：作废在途 OCR、销毁引擎与悬浮窗。
func (s *Scanner) Stop() *State {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.scanning {
		return s.stateLocked()
	}
	s.scanning = false
	s.ocrEpoch++
	if s.stopTimer != nil {
		close(s.stopTimer)
		s.stopTimer = nil
	}
	s.stopEngineLocked()
	s.syncBoxWindowsLocked()
	if s.resultWin != nil {
		s.resultWin.Close()
		s.resultWin = nil
	}
	if s.cfg.OnScanStateChange != nil {
		s.cfg.OnScanStateChange(false)
	}
	s.pushPageEvent(PageEvent{Type: "scan", Scanning: false})
	return s.stateLocked()
}

// startEngineLocked 创建并初始化引擎（异步：就绪/失败经 engine 事件推送）。
func (s *Scanner) startEngineLocked() {
	if s.cfg.NewEngine == nil {
		s.engineError = "未注入 OCR 引擎"
		s.pushPageEvent(PageEvent{Type: "engine", Ready: false, Error: s.engineError})
		return
	}
	eng := s.cfg.NewEngine()
	s.engine = eng
	modelsDir := s.cfg.ModelsDir
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		err := eng.Init(modelsDir)
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.engine != eng {
			return // 已停止/替换
		}
		if err != nil {
			s.engineError = "引擎初始化失败：" + err.Error()
			s.pushPageEvent(PageEvent{Type: "engine", Ready: false, Error: s.engineError})
			return
		}
		s.engineReady = true
		s.pushPageEvent(PageEvent{Type: "engine", Ready: true})
	}()
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		for r := range eng.Results() {
			s.onOCRResult(r)
		}
	}()
}

// stopEngineLocked 销毁引擎（对齐 _killWorker）。
func (s *Scanner) stopEngineLocked() {
	if s.engine != nil {
		eng := s.engine
		s.engine = nil
		s.engineReady = false
		eng.Close()
	}
	s.inFlight = map[BoxID]bool{BoxA: false, BoxB: false}
}

// Dispose 释放全部资源（幂等）。
func (s *Scanner) Dispose() {
	s.Stop()
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, w := range s.boxWins {
		if w != nil {
			w.Close()
			s.boxWins[id] = nil
		}
	}
	if s.resultWin != nil {
		s.resultWin.Close()
		s.resultWin = nil
	}
}

// ---------------------------------------------------------------- 悬浮窗生命周期

func (s *Scanner) syncBoxWindowsLocked() {
	for _, id := range []BoxID{BoxA, BoxB} {
		want := s.scanning && s.box(id).Visible
		have := s.boxWins[id] != nil
		if want && !have {
			s.createBoxWindowLocked(id)
		}
		if !want && have {
			s.boxWins[id].Close()
			s.boxWins[id] = nil
		}
	}
}

func (s *Scanner) createBoxWindowLocked(id BoxID) {
	g := *s.box(id)
	win, err := s.cfg.Host.NewWindow(overlay.WindowOpts{
		Bounds:  overlay.Rect{X: g.X, Y: g.Y, W: g.W, H: g.H},
		Title:   "搜题扫描框" + string(id),
		Opacity: 1,
	})
	if err != nil {
		s.tickErr = "创建扫描框失败：" + err.Error()
		return
	}
	s.boxWins[id] = win
	s.paintBoxWindow(id, win)
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		s.watchWindow(win, id, false)
	}()
}

func (s *Scanner) createResultWindowLocked() {
	wa := workAreaOf(s.cfg.Host.Primary())
	g := ResolveResultGeometry(s.settings.Result, wa)
	win, err := s.cfg.Host.NewWindow(overlay.WindowOpts{
		Bounds:  overlay.Rect{X: g.X, Y: g.Y, W: g.W, H: g.H},
		Title:   "搜题结果",
		Opacity: s.settings.Opacity,
	})
	if err != nil {
		s.tickErr = "创建结果窗失败：" + err.Error()
		return
	}
	s.resultWin = win
	s.paintResultWindowLocked()
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		s.watchWindow(win, "", true)
	}()
}

// paintBoxWindow 绘制扫描框内容（品牌蓝描边 + 标签 + 隐藏钮 + 缩放柄）。
func (s *Scanner) paintBoxWindow(id BoxID, win overlay.Window) {
	b := win.Bounds()
	buf := overlay.NewBuffer(b.W, b.H)
	overlay.DrawScanBox(buf, string(id))
	_ = win.Paint(buf)
}

// paintResultWindowLocked 绘制结果窗内容（对齐 result.html 分区布局）。
func (s *Scanner) paintResultWindowLocked() {
	if s.resultWin == nil {
		return
	}
	b := s.resultWin.Bounds()
	view := s.resultViewLocked()
	buf := overlay.NewBuffer(b.W, b.H)
	overlay.DrawResultCard(buf, view)
	_ = s.resultWin.Paint(buf)
}

// resultViewLocked 由 boxState 组装结果窗视图模型（对齐 resultpanel/main.js renderSec）。
func (s *Scanner) resultViewLocked() overlay.ResultView {
	v := overlay.ResultView{Title: "题库搜题", OcrMs: s.lastOcrMs}
	visible := make([]BoxID, 0, 2)
	for _, id := range []BoxID{BoxA, BoxB} {
		if s.box(id).Visible {
			visible = append(visible, id)
		}
	}
	showTag := len(visible) >= 2 // 单框布局整窗只显该框结果，不带框标
	for _, id := range visible {
		sec := overlay.ResultSection{Tag: string(id), ShowTag: showTag}
		st := s.boxState[id]
		if st == nil {
			sec.Waiting = true
			v.Sections = append(v.Sections, sec)
			continue
		}
		sec.Matched = st.Matched
		if !st.Matched {
			snip := []rune(st.OCRText)
			if len(snip) > 40 {
				snip = snip[:40]
			}
			sec.OCRSnippet = string(snip)
			v.Sections = append(v.Sections, sec)
			continue
		}
		if st.Q != nil {
			q := st.Q
			sec.Answer = strings.ToUpper(q.Answer)
			sec.BankName = q.BankName
			sec.ScorePct = int(math.Round(st.Score * 100))
			sec.Stem = q.Content
			if q.Type == "judge" && len(q.Options) <= 2 {
				// 判断题显示命中行 正确/错误（选项列多于 2 个时仍按字母显示）
				if sec.Answer == "A" {
					sec.JudgeLine = "正确"
				} else {
					sec.JudgeLine = "错误"
				}
			} else {
				const letters = "ABCDEF"
				for i, o := range q.Options {
					L := ""
					if i < len(letters) {
						L = string(letters[i])
					} else {
						L = itoaDec(i + 1)
					}
					sec.Options = append(sec.Options, overlay.ResultOpt{
						Letter: L,
						Text:   o,
						Hit:    strings.Contains(sec.Answer, L),
					})
				}
			}
		}
		v.Sections = append(v.Sections, sec)
	}
	return v
}

// itoaDec 十进制数字串（选项超过 F 时的序号）。
func itoaDec(v int) string {
	return fmt.Sprintf("%d", v)
}

// watchWindow 消费窗口输入事件：右上角关闭钮隐藏扫描框、缩放柄/拖动区启动 16ms 光标轮询拖拽。
func (s *Scanner) watchWindow(win overlay.Window, id BoxID, isResult bool) {
	for ev := range win.Events() {
		if ev.Kind != overlay.EventPointerPress {
			continue
		}
		var zone overlay.HitZone
		if isResult {
			zone = overlay.HitTestResult(ev.X, ev.Y, win.Bounds().W, win.Bounds().H)
		} else {
			zone = overlay.HitTestScanBox(ev.X, ev.Y, win.Bounds().W, win.Bounds().H)
		}
		switch zone {
		case overlay.ZoneClose:
			if !isResult {
				s.SetBoxVisible(id, false)
				s.pushPageEvent(PageEvent{Type: "boxHidden", Box: string(id)})
			}
		case overlay.ZoneMove, overlay.ZoneResize:
			mode := overlay.DragMove
			if zone == overlay.ZoneResize {
				mode = overlay.DragResize
			}
			s.runDrag(win, id, isResult, mode)
		}
	}
}

// runDrag 16ms 光标轮询拖拽（位移由 OS 光标与起点差值得出——渲染层合成输入位移不可靠，
// 已踩坑；窗口跟手后光标始终留在窗内，抬起靠光标按键态轮询，不依赖 pointer capture）。
// dragEnd 落盘几何，下次开搜恢复；5 分钟兜底防卡死（同 .cjs）。
func (s *Scanner) runDrag(win overlay.Window, id BoxID, isResult bool, mode overlay.DragMode) {
	cursor0, leftDown, err := s.cfg.Host.PointerState()
	if err != nil || !leftDown {
		return
	}
	bounds0 := win.Bounds()
	minW, minH := 160, 60
	if isResult {
		minW, minH = 260, 140
	}
	drag := overlay.NewDragger(mode, cursor0, bounds0, minW, minH)
	started := time.Now()
	ticker := time.NewTicker(overlay.DragPollInterval)
	defer ticker.Stop()
	for {
		<-ticker.C
		cur, down, err := s.cfg.Host.PointerState()
		if err != nil {
			return
		}
		if time.Since(started) > overlay.DragTimeout {
			s.endDrag(win, id, isResult)
			return // 兜底防卡死
		}
		if !down {
			s.endDrag(win, id, isResult)
			return
		}
		prev := win.Bounds()
		target := drag.Step(cur)
		win.SetBounds(target)
		if target.W != prev.W || target.H != prev.H {
			// 尺寸变化后按新几何重绘内容（原生缓冲不随窗缩放）
			if isResult {
				s.mu.Lock()
				s.paintResultWindowLocked()
				s.mu.Unlock()
			} else {
				s.paintBoxWindow(id, win)
			}
		}
	}
}

// endDrag 拖拽结束：几何落盘（对齐 _endBoxDrag / resultDragEnd）。
func (s *Scanner) endDrag(win overlay.Window, id BoxID, isResult bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	b := win.Bounds()
	if isResult {
		s.settings.Result = ResultGeom{X: b.X, Y: b.Y, W: b.W, H: b.H}
	} else {
		g := s.box(id)
		g.X, g.Y, g.W, g.H = b.X, b.Y, b.W, b.H
	}
	s.saveSettings()
}

// ---------------------------------------------------------------- 截屏轮询

// tick 单拍截屏：每涉及显示器每帧只抓一次（shotCache）；帧哈希未变跳过 OCR。
func (s *Scanner) tick() {
	s.mu.Lock()
	scanning := s.scanning
	s.mu.Unlock()
	if !scanning {
		return
	}
	// 忙门闩：截屏在弱机/大屏上可能超过扫描间隔，未完的拍不重入，
	// 防并发抓帧互相拖慢甚至取到空画面（已踩坑）
	if !s.tickBusy.CompareAndSwap(false, true) {
		return
	}
	defer s.tickBusy.Store(false)

	s.mu.Lock()
	active := make([]BoxID, 0, 2)
	for _, id := range []BoxID{BoxA, BoxB} {
		if s.box(id).Visible && s.boxWins[id] != nil {
			active = append(active, id)
		}
	}
	s.mu.Unlock()
	if len(active) == 0 {
		return
	}
	shotCache := map[int]*overlay.Frame{}
	for _, id := range active {
		cap, err := s.captureBox(id, shotCache)
		s.mu.Lock()
		if err != nil {
			s.tickErr = fmt.Sprintf("框%s 截屏异常：%v", id, err)
			s.mu.Unlock()
			continue
		}
		if cap == nil {
			s.tickErr = fmt.Sprintf("框%s 未取到屏幕画面（截屏为空）", id)
			s.mu.Unlock()
			continue
		}
		s.tickErr = ""
		hash := FrameHash(cap.Pix)
		if hash == s.lastHash[id] { // 画面未变
			s.mu.Unlock()
			continue
		}
		s.lastHash[id] = hash
		s.queueOCRLocked(id, cap)
		s.mu.Unlock()
	}
}

// captureBox 截取指定框当前覆盖的屏幕区域（shotCache：同帧同显示器复用整屏截图）。
// 返回 nil,nil = 截屏为空/区域过小（同 .cjs 返回 null）。
func (s *Scanner) captureBox(id BoxID, shotCache map[int]*overlay.Frame) (*overlay.Crop, error) {
	s.mu.Lock()
	win := s.boxWins[id]
	s.mu.Unlock()
	if win == nil {
		return nil, nil
	}
	bounds := win.Bounds() // DIP
	disp := s.cfg.Host.DisplayFor(bounds)
	if shotCache == nil {
		shotCache = map[int]*overlay.Frame{} // 同 .cjs：显式强抓时不复用缓存
	}
	frame, cached := shotCache[disp.ID]
	if !cached {
		var err error
		frame, err = s.cfg.Host.CaptureDisplay(disp, overlay.MaxThumb)
		if err != nil {
			return nil, err
		}
		shotCache[disp.ID] = frame
	}
	if frame == nil {
		return nil, nil
	}
	crop, err := overlay.CropFrame(frame, disp, bounds)
	if err != nil {
		return nil, nil // 区域过小等同「截屏为空」
	}
	return crop, nil
}

// queueOCRLocked 提交 OCR（每框在途限一；帧所有权移交引擎）。
func (s *Scanner) queueOCRLocked(id BoxID, cap *overlay.Crop) {
	if s.engine == nil || !s.engineReady || s.inFlight[id] {
		return
	}
	s.inFlight[id] = true
	taskID := EncodeTaskID(s.ocrEpoch, int(s.ocrSeq), id)
	s.ocrSeq++
	s.engine.Submit(taskID, cap.Pix, cap.Width, cap.Height)
}

// onOCRResult 处理识别结果：epoch 作废过滤、匹配、状态更新与去重推送。
func (s *Scanner) onOCRResult(r OCRResult) {
	s.mu.Lock()
	defer s.mu.Unlock()
	epoch, boxID := DecodeTaskID(r.ID)
	if epoch != s.ocrEpoch {
		return // 已停止/重启，丢弃旧帧
	}
	s.inFlight[boxID] = false
	if !r.OK {
		s.engineError = r.Error
		if s.engineError == "" {
			s.engineError = "OCR 失败"
		}
		return
	}
	s.lastOcrMs = r.ElapsedMs
	if s.indexDirty {
		s.rebuildIndex()
	}
	hit := FindBest(s.bankIndex, r.Text, s.settings.Threshold)
	snippet := r.Text
	if len([]rune(snippet)) > 200 {
		snippet = string([]rune(snippet)[:200])
	}
	var next *BoxState
	if hit != nil && hit.Question != nil {
		next = &BoxState{Matched: true, Score: float64(int(hit.Score*100)) / 100, Q: hit.Question, OCRText: snippet}
	} else {
		next = &BoxState{Matched: false, OCRText: snippet}
	}
	prev := s.boxState[boxID]
	s.boxState[boxID] = next
	if !reflect.DeepEqual(prev, next) {
		s.pushResultLocked()
	}
}

// pushResultLocked 推送结果窗状态（对齐 _pushResult：内容未变不重复推送）。
func (s *Scanner) pushResultLocked() {
	payload := ResultState{
		Boxes:   map[string]*BoxState{string(BoxA): s.boxState[BoxA], string(BoxB): s.boxState[BoxB]},
		OcrMs:   s.lastOcrMs,
		Visible: map[string]bool{string(BoxA): s.box(BoxA).Visible, string(BoxB): s.box(BoxB).Visible},
	}
	keyData, _ := json.Marshal([]any{payload.Boxes, payload.Visible})
	key := string(keyData)
	if key == s.lastPushed {
		return
	}
	s.lastPushed = key
	sendLatest(s.results, payload)
	s.paintResultWindowLocked()
}

func (s *Scanner) pushPageEvent(ev PageEvent) { sendLatest(s.events, ev) }

// sendLatest 通道满时丢最旧保最新（结果/事件只关心最新状态）。
func sendLatest[T any](ch chan T, v T) {
	for {
		select {
		case ch <- v:
			return
		default:
			select {
			case <-ch:
			default:
			}
		}
	}
}

// ---------------------------------------------------------------- 对外操作

// State 当前服务状态。
func (s *Scanner) State() *State {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.stateLocked()
}

func (s *Scanner) stateLocked() *State {
	boxDbg := map[string]*BoxDebug{}
	for _, id := range []BoxID{BoxA, BoxB} {
		if st := s.boxState[id]; st != nil {
			snip := []rune(st.OCRText)
			if len(snip) > 60 {
				snip = snip[:60]
			}
			boxDbg[string(id)] = &BoxDebug{Matched: st.Matched, Snippet: string(snip)}
		} else {
			boxDbg[string(id)] = nil
		}
	}
	banks, count := s.bankState()
	return &State{
		Scanning:      s.scanning,
		EngineReady:   s.engineReady,
		EngineError:   s.engineError,
		CaptureError:  s.tickErr,
		SessionType:   os.Getenv("XDG_SESSION_TYPE"), // wayland 截屏不可用（需 X11 会话）
		OcrMs:         s.lastOcrMs,
		BoxDebug:      boxDbg,
		Settings:      s.settings,
		Banks:         banks,
		QuestionCount: count,
	}
}

// SetBoxVisible 显示/隐藏扫描框（对齐 quizSearch:setBoxVisible）。
func (s *Scanner) SetBoxVisible(id BoxID, visible bool) *State {
	s.mu.Lock()
	defer s.mu.Unlock()
	if id != BoxA && id != BoxB {
		return s.stateLocked()
	}
	s.box(id).Visible = visible
	s.saveSettings()
	s.syncBoxWindowsLocked()
	s.pushResultLocked() // 单/双框布局即时切换
	return s.stateLocked()
}

// ResetBoxes 恢复双框默认布局（对齐 quizSearch:resetBoxes）。
func (s *Scanner) ResetBoxes() *State {
	s.mu.Lock()
	defer s.mu.Unlock()
	wa := workAreaOf(s.cfg.Host.Primary())
	s.settings.Boxes.A, s.settings.Boxes.B = ResetBoxes(wa)
	s.saveSettings()
	for _, id := range []BoxID{BoxA, BoxB} {
		if w := s.boxWins[id]; w != nil {
			g := s.box(id)
			w.SetBounds(overlay.Rect{X: g.X, Y: g.Y, W: g.W, H: g.H})
			s.paintBoxWindow(id, w)
		}
	}
	return s.stateLocked()
}

// Options 可热更选项（对齐 quizSearch:setOptions）。
type Options struct {
	Interval  *int
	Threshold *float64
	Opacity   *float64
}

// SetOptions 修改选项（钳制后落盘；间隔变更即时生效，不透明度即时作用于结果窗）。
func (s *Scanner) SetOptions(o Options) *State {
	s.mu.Lock()
	defer s.mu.Unlock()
	restartTimer := false
	if o.Interval != nil {
		v := ClampInterval(*o.Interval)
		restartTimer = v != s.settings.Interval
		s.settings.Interval = v
	}
	if o.Threshold != nil {
		s.settings.Threshold = ClampThreshold(*o.Threshold)
	}
	if o.Opacity != nil {
		s.settings.Opacity = ClampOpacity(*o.Opacity)
		if s.resultWin != nil {
			s.resultWin.SetOpacity(s.settings.Opacity)
		}
	}
	s.saveSettings()
	if restartTimer && s.scanning && s.stopTimer != nil {
		close(s.stopTimer)
		s.stopTimer = make(chan struct{})
		interval := s.settings.Interval
		stopCh := s.stopTimer
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			t := time.NewTicker(time.Duration(interval) * time.Millisecond)
			defer t.Stop()
			for {
				select {
				case <-stopCh:
					return
				case <-t.C:
					s.tick()
				}
			}
		}()
	}
	return s.stateLocked()
}

// BankImport 导入题库（rows 为二维表格行，首行表头）。
func (s *Scanner) BankImport(name, sourceFile string, rows [][]string) (ImportResult, *State, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, err := s.store.ImportRows(name, sourceFile, rows)
	if err != nil {
		return ImportResult{}, s.stateLocked(), err
	}
	s.indexDirty = true
	return r, s.stateLocked(), nil
}

// BankImportFile 从 xlsx 文件导入题库（excelize 解析全工作表）。
func (s *Scanner) BankImportFile(name, path string) (ImportResult, *State, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, err := s.store.ImportFile(name, path)
	if err != nil {
		return ImportResult{}, s.stateLocked(), err
	}
	s.indexDirty = true
	return r, s.stateLocked(), nil
}

// BankRemove 删除题库。
func (s *Scanner) BankRemove(id string) (*State, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.store.Remove(id); err != nil {
		return s.stateLocked(), err
	}
	s.indexDirty = true
	return s.stateLocked(), nil
}

// BankRename 重命名题库。
func (s *Scanner) BankRename(id, name string) (*State, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.store.Rename(id, name); err != nil {
		return s.stateLocked(), err
	}
	return s.stateLocked(), nil
}

// BankSetEnabled 启用/停用题库。
func (s *Scanner) BankSetEnabled(id string, enabled bool) (*State, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.store.SetEnabled(id, enabled); err != nil {
		return s.stateLocked(), err
	}
	s.indexDirty = true
	return s.stateLocked(), nil
}

// TestMatch 题干文本匹配自测（对齐 quizSearch:testMatch）。
func (s *Scanner) TestMatch(text string) MatchOutcome {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.indexDirty {
		s.rebuildIndex()
	}
	hit := FindBest(s.bankIndex, text, s.settings.Threshold)
	if hit == nil {
		return MatchOutcome{Matched: false}
	}
	return MatchOutcome{
		Matched:  hit.Question != nil,
		Score:    hit.Score,
		Question: hit.Question,
		Near:     hit.Near,
		Norm:     hit.Norm,
	}
}

// Selftest 引擎自检：模型文件齐备性 + 引擎可选扩展细节。
func (s *Scanner) Selftest() SelftestResult {
	missing := []string{}
	for _, f := range ModelFiles {
		if _, err := os.Stat(filepath.Join(s.cfg.ModelsDir, f)); err != nil {
			missing = append(missing, f)
		}
	}
	res := SelftestResult{
		OK:        len(missing) == 0,
		ModelsDir: s.cfg.ModelsDir,
		Missing:   missing,
	}
	s.mu.Lock()
	eng := s.engine
	s.mu.Unlock()
	if ts, ok := eng.(EngineSelftester); ok {
		res.Engine = ts.Selftest()
		if res.Engine != nil {
			if v, ok := res.Engine["ok"].(bool); ok && !v {
				res.OK = false
			}
		}
	}
	return res
}

// DebugCapture 把扫描框当前所见画面存成 PNG（排查「截屏黑/空」「OCR 失败」「匹配未中」用）。
func (s *Scanner) DebugCapture(dir string) (string, error) {
	s.mu.Lock()
	id := BoxID("")
	for _, cand := range []BoxID{BoxA, BoxB} {
		if s.boxWins[cand] != nil {
			id = cand
			break
		}
	}
	s.mu.Unlock()
	if id == "" {
		return "", errors.New("扫描框未开（先开始搜题）")
	}
	cap, err := s.captureBox(id, nil)
	if err != nil {
		return "", fmt.Errorf("截屏异常：%w", err)
	}
	if cap == nil {
		return "", errors.New("截屏失败（未取到屏幕画面）")
	}
	file := filepath.Join(dir, fmt.Sprintf("题图调试_框%s_%d.png", id, time.Now().UnixMilli()))
	img := image.NewNRGBA(image.Rect(0, 0, cap.Width, cap.Height))
	for i := 0; i < len(cap.Pix); i += 4 { // BGRA → NRGBA
		img.Pix[i] = cap.Pix[i+2]
		img.Pix[i+1] = cap.Pix[i+1]
		img.Pix[i+2] = cap.Pix[i]
		img.Pix[i+3] = cap.Pix[i+3]
	}
	f, err := os.Create(file)
	if err != nil {
		return "", err
	}
	defer f.Close()
	if err := png.Encode(f, img); err != nil {
		return "", err
	}
	return file, nil
}
