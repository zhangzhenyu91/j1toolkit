// 扫描编排测试：fake OCR 引擎 + fake 宿主，覆盖 A/B 帧哈希跳帧、忙门闩、epoch 作废、
// 匹配推送去重、隐藏/拖拽几何落盘与选项钳制（不依赖真实 OCR/窗口/截屏）。
package quizsearch

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/zhangzhenyu91/j1toolkit/uvmp-toolkit/core-go/internal/overlay"
)

type fakeSubmit struct {
	ID     int64
	Width  int
	Height int
	PixLen int
}

// fakeEngine 假 OCR 引擎：记录提交、结果由测试注入。
type fakeEngine struct {
	mu      sync.Mutex
	models  string
	inited  bool
	closed  bool
	submits []fakeSubmit
	results chan OCRResult
}

func newFakeEngine() *fakeEngine {
	return &fakeEngine{results: make(chan OCRResult, 8)}
}

func (e *fakeEngine) Init(modelsDir string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.models = modelsDir
	e.inited = true
	return nil
}

func (e *fakeEngine) Submit(id int64, bitmap []byte, width, height int) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.submits = append(e.submits, fakeSubmit{ID: id, Width: width, Height: height, PixLen: len(bitmap)})
}

func (e *fakeEngine) Results() <-chan OCRResult { return e.results }

func (e *fakeEngine) Close() {
	e.mu.Lock()
	defer e.mu.Unlock()
	if !e.closed {
		e.closed = true
		close(e.results)
	}
}

func (e *fakeEngine) submitList() []fakeSubmit {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]fakeSubmit, len(e.submits))
	copy(out, e.submits)
	return out
}

// fakeSelftestEngine 自检扩展引擎。
type fakeSelftestEngine struct {
	*fakeEngine
	extra map[string]any
}

func (e *fakeSelftestEngine) Selftest() map[string]any { return e.extra }

func newTestFrame(d overlay.Display, fill byte) *overlay.Frame {
	// 物理像素 = DIP×Scale；这里用 1/4 分辨率缩略帧，裁剪数学按 kx/ky 自适应
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

// newTestScanner 装配 fake 宿主+fake 引擎的扫描服务并预置题库。
func newTestScanner(t *testing.T) (*Scanner, *overlay.FakeHost, *fakeEngine) {
	t.Helper()
	host := overlay.NewFakeHost()
	eng := newFakeEngine()
	s, err := New(Config{
		DataDir:   t.TempDir(),
		ModelsDir: t.TempDir(),
		NewEngine: func() Engine { return eng },
		Host:      host,
	})
	if err != nil {
		t.Fatal(err)
	}
	s.settings.Interval = 3000 // 拉长自动轮询间隔，测试内手动 tick 可控
	s.settings.Boxes.A = BoxGeom{X: 80, Y: 100, W: 720, H: 180, Visible: true}
	s.settings.Boxes.B = BoxGeom{X: 80, Y: 500, W: 720, H: 180, Visible: true}
	host.SetFrame(host.Primary().ID, newTestFrame(host.Primary(), 0x40))
	if _, _, err := s.BankImport("测试库", "test.xlsx", [][]string{
		{"题干", "答案"},
		{"我国安全生产工作的方针是安全第一预防为主综合治理", "A"},
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Dispose)
	return s, host, eng
}

func eventually(t *testing.T, desc string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timeout waiting for %s", desc)
}

func TestTaskIDCodec(t *testing.T) {
	id := EncodeTaskID(7, 123456, BoxB)
	epoch, box := DecodeTaskID(id)
	if epoch != 7 || box != BoxB {
		t.Fatalf("decode = %d/%s, want 7/B", epoch, box)
	}
	id = EncodeTaskID(2, 3, BoxA)
	epoch, box = DecodeTaskID(id)
	if epoch != 2 || box != BoxA {
		t.Fatalf("decode = %d/%s, want 2/A", epoch, box)
	}
	// seq 模 500000 且框别占奇偶位（同 .cjs）
	if EncodeTaskID(1, 500000, BoxA) != EncodeTaskID(1, 0, BoxA) {
		t.Error("seq should wrap at 500000")
	}
}

func TestStartRequiresBanks(t *testing.T) {
	host := overlay.NewFakeHost()
	s, err := New(Config{DataDir: t.TempDir(), NewEngine: func() Engine { return newFakeEngine() }, Host: host})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Dispose()
	if _, err := s.Start(); err == nil || err.Error() != "请先导入题库" {
		t.Fatalf("want 请先导入题库, got %v", err)
	}
}

func TestStartCreatesWindowsAndEngine(t *testing.T) {
	s, host, eng := newTestScanner(t)
	st, err := s.Start()
	if err != nil {
		t.Fatal(err)
	}
	if !st.Scanning {
		t.Fatal("state should report scanning")
	}
	wins := host.Windows()
	if len(wins) != 3 { // A/B 扫描框 + 结果窗
		t.Fatalf("windows = %d, want 3", len(wins))
	}
	eventually(t, "engine ready event", func() bool {
		select {
		case ev := <-s.Events():
			return ev.Type == "engine" && ev.Ready
		default:
			return false
		}
	})
	eng.mu.Lock()
	defer eng.mu.Unlock()
	if !eng.inited || eng.models == "" {
		t.Errorf("engine not inited: %+v", eng)
	}
}

func TestTickFrameHashSkipAndResubmit(t *testing.T) {
	s, host, eng := newTestScanner(t)
	if _, err := s.Start(); err != nil {
		t.Fatal(err)
	}
	eventually(t, "engine ready", func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		return s.engineReady
	})
	s.tick()
	s.tick()                                // 画面未变 → 跳过
	if n := len(eng.submitList()); n != 2 { // A/B 各一次
		t.Fatalf("submits = %d, want 2（A/B 各一帧）", n)
	}
	s.tick()
	if n := len(eng.submitList()); n != 2 {
		t.Fatalf("unchanged frame should skip, submits = %d", n)
	}
	// 画面变化 → 重新提交（先回清在途态，模拟引擎已回结果；否则每框限一会挡住重提）
	s.mu.Lock()
	s.inFlight = map[BoxID]bool{}
	s.mu.Unlock()
	host.SetFrame(host.Primary().ID, newTestFrame(host.Primary(), 0x80))
	s.tick()
	if n := len(eng.submitList()); n != 4 {
		t.Fatalf("changed frame should resubmit, submits = %d", n)
	}
	// 同帧双框同显示器只截一次（shotCache）；每拍一截共 4 拍
	if c := host.CaptureCount(host.Primary().ID); c != 4 {
		t.Errorf("captures = %d, want 4（每拍一截）", c)
	}
}

func TestTickBusyLatch(t *testing.T) {
	s, host, eng := newTestScanner(t)
	if _, err := s.Start(); err != nil {
		t.Fatal(err)
	}
	eventually(t, "engine ready", func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		return s.engineReady
	})
	block := make(chan struct{})
	host.SetCaptureBlock(block)
	done := make(chan struct{})
	go func() { s.tick(); close(done) }()
	eventually(t, "first capture entered", func() bool {
		return host.CaptureCount(host.Primary().ID) >= 1
	})
	s.tick() // 门闩：未完的拍不重入
	if c := host.CaptureCount(host.Primary().ID); c != 1 {
		t.Errorf("busy tick should not capture again, captures = %d", c)
	}
	close(block)
	<-done
	if n := len(eng.submitList()); n != 2 {
		t.Errorf("submits = %d, want 2", n)
	}
}

func TestEpochDropsStaleResults(t *testing.T) {
	s, _, eng := newTestScanner(t)
	st, err := s.Start()
	if err != nil {
		t.Fatal(err)
	}
	_ = st
	s.mu.Lock()
	oldEpoch := s.ocrEpoch
	s.mu.Unlock()
	staleID := EncodeTaskID(oldEpoch, 0, BoxA)
	s.Stop()
	s.mu.Lock()
	s.scanning = true // 直接进入新一轮（epoch 已递增）
	s.ocrEpoch++
	s.mu.Unlock()
	s.onOCRResult(OCRResult{ID: staleID, OK: true, Text: "我国安全生产工作的方针是安全第一预防为主综合治理"})
	s.mu.Lock()
	got := s.boxState[BoxA]
	s.mu.Unlock()
	if got != nil {
		t.Fatalf("stale epoch result should be dropped, got %+v", got)
	}
	// 当前 epoch 结果正常进入
	s.mu.Lock()
	curEpoch := s.ocrEpoch
	s.mu.Unlock()
	s.onOCRResult(OCRResult{ID: EncodeTaskID(curEpoch, 0, BoxA), OK: true, Text: "我国安全生产工作的方针是安全第一预防为主综合治理"})
	s.mu.Lock()
	got = s.boxState[BoxA]
	s.mu.Unlock()
	if got == nil || !got.Matched {
		t.Fatalf("current epoch result should apply, got %+v", got)
	}
	_ = eng
}

func TestOCRResultMatchAndPushDedupe(t *testing.T) {
	s, _, eng := newTestScanner(t)
	if _, err := s.Start(); err != nil {
		t.Fatal(err)
	}
	eventually(t, "engine ready", func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		return s.engineReady
	})
	s.tick()
	subs := eng.submitList()
	if len(subs) == 0 {
		t.Fatal("no submits")
	}
	// 经引擎结果通道回送匹配文本（模拟真实 roundtrip）
	eng.results <- OCRResult{ID: subs[0].ID, OK: true, Text: "我国安全生产工作的方针是？安全第一、预防为主、综合治理", ElapsedMs: 42}
	eventually(t, "result pushed", func() bool {
		select {
		case rs := <-s.Results():
			st := rs.Boxes[string(BoxA)]
			if st == nil || !st.Matched || st.Q == nil {
				t.Fatalf("payload = %+v", rs)
			}
			if rs.OcrMs != 42 {
				t.Errorf("ocrMs = %d, want 42", rs.OcrMs)
			}
			return true
		default:
			return false
		}
	})
	// 相同结果不再重复推送
	prevLen := len(s.Results())
	s.onOCRResult(OCRResult{ID: subs[0].ID, OK: true, Text: "我国安全生产工作的方针是？安全第一、预防为主、综合治理"})
	if len(s.Results()) != prevLen {
		t.Error("identical result should not push again")
	}
}

func TestCloseZoneHidesBox(t *testing.T) {
	s, host, _ := newTestScanner(t)
	if _, err := s.Start(); err != nil {
		t.Fatal(err)
	}
	wins := host.Windows()
	var boxA *overlay.FakeWindow
	for _, w := range wins {
		if w.Bounds().W == 720 && w.Bounds().Y == 100 {
			boxA = w
		}
	}
	if boxA == nil {
		t.Fatal("box A window not found")
	}
	b := boxA.Bounds()
	boxA.InjectEvent(overlay.WindowEvent{Kind: overlay.EventPointerPress, X: b.W - 1, Y: 1, Button: 1}) // 右上 ×
	eventually(t, "box hidden event", func() bool {
		select {
		case ev := <-s.Events():
			return ev.Type == "boxHidden" && ev.Box == "A"
		default:
			return false
		}
	})
	s.mu.Lock()
	visible := s.settings.Boxes.A.Visible
	s.mu.Unlock()
	if visible {
		t.Error("box A should be hidden")
	}
	// 显隐已落盘
	saved := LoadSettings(s.settingsPath, WorkArea{X: 0, Y: 0, W: 1920, H: 1080})
	if saved.Boxes.A.Visible {
		t.Error("visibility should persist to settings.json")
	}
}

func TestDragMovePersistsGeometry(t *testing.T) {
	s, host, _ := newTestScanner(t)
	if _, err := s.Start(); err != nil {
		t.Fatal(err)
	}
	var boxA *overlay.FakeWindow
	for _, w := range host.Windows() {
		if w.Bounds().X == 80 && w.Bounds().Y == 100 {
			boxA = w
		}
	}
	if boxA == nil {
		t.Fatal("box A window not found")
	}
	host.SetPointer(overlay.Point{X: 200, Y: 200}, true)
	boxA.InjectEvent(overlay.WindowEvent{Kind: overlay.EventPointerPress, X: 30, Y: 30, Button: 1}) // 拖动区
	// 等拖拽轮询已启动（起点光标已采样），再动光标——防 runDrag 迟启动把新位置当起点
	eventually(t, "drag poller started", func() bool {
		return boxA.SetBoundsCount() > 0
	})
	host.SetPointer(overlay.Point{X: 250, Y: 230}, true)
	eventually(t, "window moved", func() bool {
		b := boxA.Bounds()
		return b.X == 130 && b.Y == 130
	})
	host.SetPointer(overlay.Point{X: 250, Y: 230}, false) // 抬起 → 落盘
	eventually(t, "geometry persisted", func() bool {
		saved := LoadSettings(s.settingsPath, WorkArea{X: 0, Y: 0, W: 1920, H: 1080})
		return saved.Boxes.A.X == 130 && saved.Boxes.A.Y == 130
	})
}

func TestSetOptionsClampsAndOpacity(t *testing.T) {
	s, host, _ := newTestScanner(t)
	if _, err := s.Start(); err != nil {
		t.Fatal(err)
	}
	interval, threshold, opacity := 100, 5.0, 0.1
	s.SetOptions(Options{Interval: &interval, Threshold: &threshold, Opacity: &opacity})
	s.mu.Lock()
	got := s.settings
	s.mu.Unlock()
	if got.Interval != 500 || got.Threshold != 0.9 || got.Opacity != 0.3 {
		t.Errorf("clamped = %+v", got)
	}
	var resultWin *overlay.FakeWindow
	for _, w := range host.Windows() {
		if w.Bounds().W == 380 {
			resultWin = w
		}
	}
	if resultWin == nil {
		t.Fatal("result window not found")
	}
	if resultWin.Opacity() != 0.3 {
		t.Errorf("result opacity = %v, want 0.3", resultWin.Opacity())
	}
}

func TestTestMatchAndSelftest(t *testing.T) {
	s, _, _ := newTestScanner(t)
	out := s.TestMatch("我国安全生产工作的方针是？安全第一、预防为主、综合治理")
	if !out.Matched || out.Question == nil {
		t.Fatalf("want match, got %+v", out)
	}
	if out.Score <= 0 || out.Norm == "" {
		t.Errorf("outcome = %+v", out)
	}
	out = s.TestMatch("完全无关的文本一二三")
	if out.Matched {
		t.Fatalf("want miss, got %+v", out)
	}

	dir := t.TempDir()
	s.cfg.ModelsDir = dir
	res := s.Selftest()
	if res.OK || len(res.Missing) != 4 {
		t.Fatalf("empty models dir selftest = %+v", res)
	}
	for _, f := range ModelFiles {
		if err := os.WriteFile(filepath.Join(dir, f), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	res = s.Selftest()
	if !res.OK || len(res.Missing) != 0 {
		t.Fatalf("full models dir selftest = %+v", res)
	}
}

func TestDebugCaptureWritesPNG(t *testing.T) {
	s, _, _ := newTestScanner(t)
	if _, err := s.DebugCapture(t.TempDir()); err == nil {
		t.Fatal("want 扫描框未开 error before start")
	}
	if _, err := s.Start(); err != nil {
		t.Fatal(err)
	}
	outDir := t.TempDir()
	file, err := s.DebugCapture(outDir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(file); err != nil {
		t.Fatalf("png missing: %v", err)
	}
}

func TestEngineInitErrorReported(t *testing.T) {
	host := overlay.NewFakeHost()
	eng := newFakeEngine()
	s, err := New(Config{
		DataDir:   t.TempDir(),
		NewEngine: func() Engine { return &initFailEngine{fakeEngine: eng} },
		Host:      host,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Dispose()
	if _, _, err := s.BankImport("k", "k.xlsx", [][]string{{"题干", "答案"}, {"甲", "A"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Start(); err != nil {
		t.Fatal(err)
	}
	eventually(t, "engine error event", func() bool {
		select {
		case ev := <-s.Events():
			return ev.Type == "engine" && !ev.Ready && ev.Error != ""
		default:
			return false
		}
	})
	if st := s.State(); st.EngineError == "" {
		t.Error("engineError should be set")
	}
}

type initFailEngine struct {
	*fakeEngine
}

func (e *initFailEngine) Init(string) error { return errors.New("模型加载失败") }
