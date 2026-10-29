// CoreBackend 题库扫描控制测试：接口依赖面 fake（状态/事件/生命周期）+
// 真实 quizsearch.Scanner（overlay.FakeHost + fake 引擎）两条通路，
// 以及旧 Electron 数据目录兼容（旧 banks/settings 原地可读）。
package ui

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/zhangzhenyu91/j1toolkit/uvmp-toolkit/core-go/internal/overlay"
	"github.com/zhangzhenyu91/j1toolkit/uvmp-toolkit/core-go/internal/quizsearch"
	"github.com/zhangzhenyu91/j1toolkit/uvmp-toolkit/core-go/internal/state"
)

// testFrame 按显示器 1/4 分辨率构造稠密缩略帧（FakeHost.SetFrame 用）。
func testFrame(d overlay.Display, fill byte) *overlay.Frame {
	w, h := d.Bounds.W/4, d.Bounds.H/4
	pix := make([]byte, w*h*4)
	for i := 0; i < len(pix); i += 4 {
		pix[i] = fill
		pix[i+1] = fill
		pix[i+2] = fill
		pix[i+3] = 255
	}
	return &overlay.Frame{Pix: pix, Width: w, Height: h, DisplayID: d.ID}
}

var _ QuizScanner = (*fakeQuizScanner)(nil)
var _ QuizScanner = (*quizsearch.Scanner)(nil)

// fakeQuizScanner 扫描服务依赖面 fake：记录调用、状态可控、事件可注入。
type fakeQuizScanner struct {
	mu       sync.Mutex
	calls    []string
	scanning bool
	disposed bool
	boxVis   map[quizsearch.BoxID]bool
	interval int
	thresh   float64
	opacity  float64
	selfRes  quizsearch.SelftestResult
	events   chan quizsearch.PageEvent
}

func newFakeQuizScanner() *fakeQuizScanner {
	return &fakeQuizScanner{
		boxVis:   map[quizsearch.BoxID]bool{quizsearch.BoxA: true, quizsearch.BoxB: true},
		interval: 1000,
		thresh:   0.6,
		opacity:  0.9,
		selfRes:  quizsearch.SelftestResult{OK: true, ModelsDir: "mem://models", Missing: []string{}},
		events:   make(chan quizsearch.PageEvent, 16),
	}
}

func (f *fakeQuizScanner) record(name string) {
	f.mu.Lock()
	f.calls = append(f.calls, name)
	f.mu.Unlock()
}

func (f *fakeQuizScanner) stateLocked() *quizsearch.State {
	st := &quizsearch.State{
		Scanning:      f.scanning,
		EngineReady:   f.scanning,
		Settings:      quizsearch.DefaultSettings(),
		QuestionCount: 2,
	}
	st.Settings.Interval = f.interval
	st.Settings.Threshold = f.thresh
	st.Settings.Opacity = f.opacity
	st.Settings.Boxes.A.Visible = f.boxVis[quizsearch.BoxA]
	st.Settings.Boxes.B.Visible = f.boxVis[quizsearch.BoxB]
	return st
}

func (f *fakeQuizScanner) State() *quizsearch.State {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.stateLocked()
}

func (f *fakeQuizScanner) Start() (*quizsearch.State, error) {
	f.record("Start")
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.disposed {
		return nil, errors.New("扫描服务已释放")
	}
	f.scanning = true
	return f.stateLocked(), nil
}

func (f *fakeQuizScanner) Stop() *quizsearch.State {
	f.record("Stop")
	f.mu.Lock()
	defer f.mu.Unlock()
	f.scanning = false
	return f.stateLocked()
}

func (f *fakeQuizScanner) SetBoxVisible(id quizsearch.BoxID, visible bool) *quizsearch.State {
	f.record("SetBoxVisible")
	f.mu.Lock()
	defer f.mu.Unlock()
	f.boxVis[id] = visible
	return f.stateLocked()
}

func (f *fakeQuizScanner) ResetBoxes() *quizsearch.State {
	f.record("ResetBoxes")
	f.mu.Lock()
	defer f.mu.Unlock()
	f.boxVis = map[quizsearch.BoxID]bool{quizsearch.BoxA: true, quizsearch.BoxB: true}
	return f.stateLocked()
}

func (f *fakeQuizScanner) SetOptions(o quizsearch.Options) *quizsearch.State {
	f.record("SetOptions")
	f.mu.Lock()
	defer f.mu.Unlock()
	if o.Interval != nil {
		f.interval = quizsearch.ClampInterval(*o.Interval)
	}
	if o.Threshold != nil {
		f.thresh = quizsearch.ClampThreshold(*o.Threshold)
	}
	if o.Opacity != nil {
		f.opacity = quizsearch.ClampOpacity(*o.Opacity)
	}
	return f.stateLocked()
}

func (f *fakeQuizScanner) DebugCapture(dir string) (string, error) {
	f.record("DebugCapture")
	if dir == "" {
		return "", errors.New("目录为空")
	}
	return filepath.Join(dir, "题图调试_框A_1.png"), nil
}

func (f *fakeQuizScanner) BankImportFile(name, path string) (quizsearch.ImportResult, *quizsearch.State, error) {
	f.record("BankImportFile")
	f.mu.Lock()
	defer f.mu.Unlock()
	return quizsearch.ImportResult{ID: "b1", Name: name, Count: 1, Enabled: true}, f.stateLocked(), nil
}

func (f *fakeQuizScanner) BankRemove(id string) (*quizsearch.State, error) {
	f.record("BankRemove")
	return f.State(), nil
}

func (f *fakeQuizScanner) BankRename(id, name string) (*quizsearch.State, error) {
	f.record("BankRename")
	return f.State(), nil
}

func (f *fakeQuizScanner) BankSetEnabled(id string, enabled bool) (*quizsearch.State, error) {
	f.record("BankSetEnabled")
	return f.State(), nil
}

func (f *fakeQuizScanner) Selftest() quizsearch.SelftestResult {
	f.record("Selftest")
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.selfRes
}

func (f *fakeQuizScanner) Events() <-chan quizsearch.PageEvent { return f.events }

func (f *fakeQuizScanner) Dispose() {
	f.record("Dispose")
	f.mu.Lock()
	defer f.mu.Unlock()
	f.disposed = true
}

func (f *fakeQuizScanner) called(name string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.calls {
		if c == name {
			return true
		}
	}
	return false
}

// stubEngine quizsearch.Engine fake（记录 Init/Submit/Close；结果由测试注入）。
type stubEngine struct {
	mu      sync.Mutex
	models  string
	inited  bool
	closed  bool
	submits int
	results chan quizsearch.OCRResult
}

func newStubEngine() *stubEngine {
	return &stubEngine{results: make(chan quizsearch.OCRResult, 8)}
}

func (e *stubEngine) Init(modelsDir string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.models = modelsDir
	e.inited = true
	return nil
}

func (e *stubEngine) Submit(id int64, bitmap []byte, width, height int) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.submits++
}

func (e *stubEngine) Results() <-chan quizsearch.OCRResult { return e.results }

func (e *stubEngine) Close() {
	e.mu.Lock()
	defer e.mu.Unlock()
	if !e.closed {
		e.closed = true
		close(e.results)
	}
}

// writeModelFiles 写齐自检四文件（fake 会话不读内容）。
func writeModelFiles(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, f := range quizsearch.ModelFiles {
		if err := os.WriteFile(filepath.Join(dir, f), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// waitQuizCond 轮询等待条件（异步回填）。
func waitQuizCond(t *testing.T, desc string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timeout waiting for %s", desc)
}

func TestCoreBackendQuizScanControlFakeScanner(t *testing.T) {
	b, _ := setupCore(t)
	ctx := context.Background()
	fs := newFakeQuizScanner()
	b.AttachQuizScanner(fs)

	sub, err := b.QuizSubscribe(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()

	// 开始 → 状态映射 → 事件泵把 scan 引擎事件扇出到订阅者
	st, err := b.QuizScanStart(ctx)
	if err != nil || !st.Scanning || !st.EngineReady {
		t.Fatalf("QuizScanStart = %+v err=%v", st, err)
	}
	fs.events <- quizsearch.PageEvent{Type: "scan", Scanning: true, Ready: true}
	fs.events <- quizsearch.PageEvent{Type: "engine", Ready: false, Error: "引擎初始化失败：样例"}
	deadline := time.After(3 * time.Second)
	var gotScan, gotEngine bool
	for !gotScan || !gotEngine {
		select {
		case ev := <-sub.Events():
			switch ev.Type {
			case QuizEventScan:
				if !ev.Scanning {
					t.Fatalf("scan 事件 = %+v", ev)
				}
				gotScan = true
			case QuizEventEngine:
				if ev.Ready || ev.Error == "" {
					t.Fatalf("engine 事件 = %+v", ev)
				}
				gotEngine = true
			}
		case <-deadline:
			t.Fatal("等待 scan/engine 事件超时")
		}
	}

	// A/B 显隐 + 编号校验
	st, err = b.QuizSetBoxVisible(ctx, "a", false) // 小写容错
	if err != nil || st.BoxVisible["A"] {
		t.Fatalf("SetBoxVisible = %+v err=%v", st, err)
	}
	fs.events <- quizsearch.PageEvent{Type: "boxHidden", Box: "A"}
	select {
	case ev := <-sub.Events():
		if ev.Type != QuizEventBoxHidden || ev.Box != "A" {
			t.Fatalf("boxHidden 事件 = %+v", ev)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("等待 boxHidden 事件超时")
	}
	if _, err := b.QuizSetBoxVisible(ctx, "C", true); err == nil {
		t.Fatal("非法框编号应报错")
	}

	// 选项（nil 不改）+ 重置
	threshold := 0.8
	st, err = b.QuizSetOptions(ctx, QuizScanOptions{Threshold: &threshold})
	if err != nil || st.Threshold != 0.8 || st.Interval != 1000 {
		t.Fatalf("SetOptions = %+v err=%v", st, err)
	}
	if st, err = b.QuizResetBoxes(ctx); err != nil || !st.BoxVisible["A"] || !st.BoxVisible["B"] {
		t.Fatalf("ResetBoxes = %+v err=%v", st, err)
	}

	// 诊断 + 自检
	if p, err := b.QuizDebugCapture(ctx, t.TempDir()); err != nil || p == "" {
		t.Fatalf("DebugCapture = %q err=%v", p, err)
	}
	fs.selfRes = quizsearch.SelftestResult{OK: false, ModelsDir: "m", Missing: []string{"cls.onnx"}, Engine: map[string]any{"ok": false}}
	r, err := b.QuizSelftest(ctx)
	if err != nil || r.OK || r.Missing[0] != "cls.onnx" || r.Engine["ok"] != false {
		t.Fatalf("QuizSelftest = %+v err=%v", r, err)
	}

	// 停止 + 生命周期
	if st, err = b.QuizScanStop(ctx); err != nil || st.Scanning {
		t.Fatalf("QuizScanStop = %+v err=%v", st, err)
	}
	b.Dispose()
	if !fs.called("Dispose") {
		t.Fatal("Dispose 应释放扫描服务")
	}
	if _, err := b.QuizScanState(ctx); err == nil {
		t.Fatal("Dispose 后扫描方法应报错")
	}
	if _, err := b.QuizScanStart(ctx); err == nil {
		t.Fatal("Dispose 后开始搜题应报错")
	}
}

func TestCoreBackendQuizScanRealScannerFakeHost(t *testing.T) {
	b, _ := setupCore(t)
	ctx := context.Background()

	host := overlay.NewFakeHost()
	host.SetFrame(host.Primary().ID, testFrame(host.Primary(), 0x40))
	var mu sync.Mutex
	var last *stubEngine
	s, err := quizsearch.New(quizsearch.Config{
		DataDir:   b.quizDir,
		ModelsDir: writeModelFiles(t),
		NewEngine: func() quizsearch.Engine {
			e := newStubEngine()
			mu.Lock()
			last = e
			mu.Unlock()
			return e
		},
		Host: host,
	})
	if err != nil {
		t.Fatal(err)
	}
	s.SetOptions(quizsearch.Options{Interval: intPtr(3000)}) // 拉长自动轮询，避免测试内频繁抓帧
	if _, _, err := s.BankImport("测试库", "test.xlsx", [][]string{
		{"题干", "答案"},
		{"我国安全生产工作的方针是安全第一预防为主综合治理", "A"},
	}); err != nil {
		t.Fatal(err)
	}
	b.AttachQuizScanner(s)

	sub, err := b.QuizSubscribe(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()

	// 开始：真实编排创建扫描框/结果窗 + 引擎 Init
	st, err := b.QuizScanStart(ctx)
	if err != nil || !st.Scanning {
		t.Fatalf("QuizScanStart = %+v err=%v", st, err)
	}
	if wins := host.Windows(); len(wins) < 3 { // A/B 框 + 结果窗
		t.Fatalf("扫描窗未创建：%d", len(wins))
	}
	waitQuizCond(t, "引擎就绪", func() bool {
		mu.Lock()
		e := last
		mu.Unlock()
		if e == nil {
			return false
		}
		e.mu.Lock()
		defer e.mu.Unlock()
		return e.inited && e.models != ""
	})
	// 事件流：scan + engine（引擎就绪）
	var gotScan, gotEngine bool
	waitQuizCond(t, "scan/engine 事件", func() bool {
		for {
			select {
			case ev := <-sub.Events():
				if ev.Type == QuizEventScan && ev.Scanning {
					gotScan = true
				}
				if ev.Type == QuizEventEngine && ev.Ready {
					gotEngine = true
				}
			default:
				return gotScan && gotEngine
			}
		}
	})

	// 选项/显隐/重置走真实 Scanner 并回读
	opacity := 0.75
	if st, err = b.QuizSetOptions(ctx, QuizScanOptions{Opacity: &opacity}); err != nil || st.Opacity != 0.75 {
		t.Fatalf("SetOptions = %+v err=%v", st, err)
	}
	if st, err = b.QuizSetBoxVisible(ctx, "B", false); err != nil || st.BoxVisible["B"] {
		t.Fatalf("SetBoxVisible = %+v err=%v", st, err)
	}
	if st, err = b.QuizResetBoxes(ctx); err != nil || !st.BoxVisible["A"] || !st.BoxVisible["B"] {
		t.Fatalf("ResetBoxes = %+v err=%v", st, err)
	}

	// 诊断：真实截帧（FakeHost 预置帧）→ PNG 落盘
	outDir := t.TempDir()
	file, err := b.QuizDebugCapture(ctx, outDir)
	if err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(file); err != nil || fi.Size() == 0 {
		t.Fatalf("题图未写出: %v", err)
	}

	// 自检：四文件齐备 → OK
	r, err := b.QuizSelftest(ctx)
	if err != nil || !r.OK || len(r.Missing) != 0 {
		t.Fatalf("QuizSelftest = %+v err=%v", r, err)
	}

	// 停止 + 释放
	if st, err = b.QuizScanStop(ctx); err != nil || st.Scanning {
		t.Fatalf("QuizScanStop = %+v err=%v", st, err)
	}
	b.Dispose()
	if _, err := b.QuizScanState(ctx); err == nil {
		t.Fatal("Dispose 后扫描方法应报错")
	}
}

func TestCoreBackendQuizDataDirLegacyCompat(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("UVMP_TOOLKIT_CONFIG", filepath.Join(dir, "config.json"))
	t.Setenv("UVMP_TOOLKIT_CONFIG_INI", filepath.Join(dir, "none.ini"))
	t.Setenv("APPDATA", dir)
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("HOME", dir)
	state.SetRoot(filepath.Join(dir, "state"))
	t.Cleanup(func() { state.SetRoot("") })

	// 旧 Electron 数据原地放置：%APPDATA%/Shade 壹匣 - 内网/quiz-search
	legacy := filepath.Join(dir, "Shade 壹匣 - 内网", "quiz-search")
	if err := os.MkdirAll(filepath.Join(legacy, "banks"), 0o755); err != nil {
		t.Fatal(err)
	}
	bankJSON := `{"id":"old1","name":"旧安规库","sourceFile":"旧.xlsx","importedAt":"2025-01-02T03:04:05.000Z","questions":[{"type":"judge","content":"旧题干：安全生产方针","options":["正确","错误"],"answer":"A","analysis":"旧解析","sort":0}]}`
	if err := os.WriteFile(filepath.Join(legacy, "banks", "old1.json"), []byte(bankJSON), 0o644); err != nil {
		t.Fatal(err)
	}
	idxJSON := `{"banks":[{"id":"old1","name":"旧安规库","sourceFile":"旧.xlsx","importedAt":"2025-01-02T03:04:05.000Z","count":1,"enabled":true}]}`
	if err := os.WriteFile(filepath.Join(legacy, "banks-index.json"), []byte(idxJSON), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(legacy, "settings.json"), []byte(`{"interval":1234,"threshold":0.55,"opacity":0.8}`), 0o644); err != nil {
		t.Fatal(err)
	}

	b := NewCoreBackend()
	t.Cleanup(b.Dispose)
	if b.quizDir != legacy {
		t.Fatalf("quizDir = %s，want 旧 Electron 目录 %s", b.quizDir, legacy)
	}

	// 旧 banks 原地可读
	banks, err := b.QuizBanks(context.Background())
	if err != nil || len(banks) != 1 || banks[0].ID != "old1" || banks[0].Name != "旧安规库" || !banks[0].Enabled {
		t.Fatalf("旧 banks 读取 = %+v err=%v", banks, err)
	}

	// 旧 settings 原地可读（真实 Scanner 读同一目录）
	s, err := quizsearch.New(quizsearch.Config{
		DataDir:   b.quizDir,
		ModelsDir: writeModelFiles(t),
		NewEngine: func() quizsearch.Engine { return newStubEngine() },
		Host:      overlay.NewFakeHost(),
	})
	if err != nil {
		t.Fatal(err)
	}
	b.AttachQuizScanner(s)
	st, err := b.QuizScanState(context.Background())
	if err != nil || st.Interval != 1234 || st.Threshold != 0.55 || st.Opacity != 0.8 {
		t.Fatalf("旧 settings 读取 = %+v err=%v", st, err)
	}
}

func intPtr(v int) *int { return &v }
