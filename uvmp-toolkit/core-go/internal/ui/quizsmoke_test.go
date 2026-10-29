// 真实 Scanner 冒烟（UVMP_SCANNER_SMOKE=1 启用；本机手动验证用，不进默认测试）。
// 覆盖：原生悬浮窗创建/绘制/缩放/关闭 + OCR 模型自检（四文件口径）+
// 真实引擎装载探针（det/rec 会话 + 词表）。不 Start 截屏轮询（不启动无限扫描）。
package ui

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/zhangzhenyu91/j1toolkit/uvmp-toolkit/core-go/internal/ocr"
	"github.com/zhangzhenyu91/j1toolkit/uvmp-toolkit/core-go/internal/overlay"
	"github.com/zhangzhenyu91/j1toolkit/uvmp-toolkit/core-go/internal/quizsearch"
)

func TestRealScannerSmoke(t *testing.T) {
	if os.Getenv("UVMP_SCANNER_SMOKE") != "1" {
		t.Skip("真实 Scanner 冒烟：设 UVMP_SCANNER_SMOKE=1 启用")
	}

	// 模型目录：默认探测之外回落开发仓 desktop/electron/quizsearch/models
	modelsDir, err := QuizModelsDir()
	if err != nil {
		alt := filepath.Clean(filepath.Join("..", "..", "..", "desktop", "electron", "quizsearch", "models"))
		if hasModelFiles(alt) {
			modelsDir = alt
		} else {
			t.Skipf("未找到 OCR 模型：%v", err)
		}
	}
	t.Logf("模型目录：%s", modelsDir)

	// ORT 共享库：默认探测之外回落开发仓 desktop/node_modules/onnxruntime-node 布局
	if os.Getenv("UVMP_ORT_LIB") == "" {
		if lib := devORTLib(); lib != "" {
			t.Setenv("UVMP_ORT_LIB", lib)
		}
	}

	// 原生窗口：扫描框 + 结果窗创建/绘制/缩放/关闭
	host, err := overlay.NewHost()
	if err != nil {
		t.Skipf("无原生悬浮窗宿主：%v", err)
	}
	defer host.Close()
	p := host.Primary()
	t.Logf("主显示器=%+v", p)

	box, err := host.NewWindow(overlay.WindowOpts{
		Bounds:  overlay.Rect{X: p.WorkArea.X + 80, Y: p.WorkArea.Y + 80, W: 720, H: 180},
		Title:   "冒烟扫描框A",
		Opacity: 1,
	})
	if err != nil {
		t.Fatalf("创建扫描框失败：%v", err)
	}
	b := box.Bounds()
	buf := overlay.NewBuffer(b.W, b.H)
	overlay.DrawScanBox(buf, "A")
	if err := box.Paint(buf); err != nil {
		t.Fatalf("绘制扫描框失败：%v", err)
	}
	box.SetBounds(overlay.Rect{X: b.X + 20, Y: b.Y + 20, W: b.W + 40, H: b.H + 20})
	box.Close()

	resWin, err := host.NewWindow(overlay.WindowOpts{
		Bounds:  overlay.Rect{X: p.WorkArea.X + 160, Y: p.WorkArea.Y + 320, W: 380, H: 260},
		Title:   "冒烟结果窗",
		Opacity: 0.9,
	})
	if err != nil {
		t.Fatalf("创建结果窗失败：%v", err)
	}
	rb := resWin.Bounds()
	rbuf := overlay.NewBuffer(rb.W, rb.H)
	overlay.DrawResultCard(rbuf, overlay.ResultView{Title: "题库搜题", OcrMs: 42})
	if err := resWin.Paint(rbuf); err != nil {
		t.Fatalf("绘制结果窗失败：%v", err)
	}
	resWin.SetOpacity(0.8)
	resWin.Close()
	t.Log("原生窗口冒烟通过")

	// 真实 Scanner 装配（原生宿主）+ 模型自检（不 Start）
	s, err := NewQuizScanner(QuizScannerOptions{ModelsDir: modelsDir, DataDir: t.TempDir(), Host: host})
	if err != nil {
		t.Fatalf("NewQuizScanner：%v", err)
	}
	defer s.Dispose()
	res := s.Selftest()
	if !res.OK || len(res.Missing) != 0 {
		t.Fatalf("模型自检未通过（四文件口径）：%+v", res)
	}
	t.Logf("Selftest=%+v", res)

	// 真实引擎装载探针：det/rec 会话 + 词表（完整 Init/Close，不投帧）
	factory, err := newSessionFactory(modelsDir)
	if err != nil {
		t.Fatalf("OCR 引擎未编入（需 -tags onnx）：%v", err)
	}
	eng := quizsearch.NewOCREngineFactory(factory)()
	if err := eng.Init(modelsDir); err != nil {
		t.Fatalf("引擎装载失败：%v", err)
	}
	if v, ok := factory.(ocr.RuntimeVersioner); ok {
		t.Logf("ORT 版本：%s", v.RuntimeVersion())
	}
	if ts, ok := eng.(quizsearch.EngineSelftester); ok {
		t.Logf("引擎自检：%+v", ts.Selftest())
	}
	eng.Close()
	t.Log("引擎装载探针通过")

	// 未开始搜题时诊断应明确报错（不伪造成像）
	if _, err := s.DebugCapture(t.TempDir()); err == nil {
		t.Fatal("未开始搜题时保存题图应报错")
	}
}

// devORTLib 开发仓 desktop/node_modules 的 ORT 共享库（按平台取；目录用 Node 架构名）。
func devORTLib() string {
	arch := runtime.GOARCH
	if arch == "amd64" {
		arch = "x64" // node_modules 布局用 Node 架构名
	}
	var name string
	switch runtime.GOOS {
	case "windows":
		name = filepath.Join("win32", arch, "onnxruntime.dll")
	case "linux":
		name = filepath.Join("linux", arch, "libonnxruntime.so.1")
	case "darwin":
		name = filepath.Join("darwin", arch, "libonnxruntime.1.20.1.dylib")
	default:
		return ""
	}
	p := filepath.Clean(filepath.Join("..", "..", "..", "desktop", "node_modules",
		"onnxruntime-node", "bin", "napi-v3", name))
	if st, err := os.Stat(p); err == nil && !st.IsDir() {
		return p
	}
	return ""
}
