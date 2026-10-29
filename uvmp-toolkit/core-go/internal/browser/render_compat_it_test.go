package browser_test

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/zhangzhenyu91/j1toolkit/uvmp-toolkit/core-go/internal/render"
)

// Electron 回退兼容集成测试（BROWSER_RUNTIME_IT=1）：render.Selftest 以 browser-runtime
// 应用目录为宿主跑完整导出链路（启动 / 导航 / eval / 新 tab 桥 / 截图 PNG /
// printToPDF 桥），只开 data: 本地页。全绿即证明 browser-runtime 的 export 模式与
// internal/render 契约兼容（cdp-worker-bridge.json、/print-to-pdf、/new-window、
// DevTools 通告、不可见窗口）。外部测试包，避免 browser ↔ render 测试导入环。

// electronPaths 推断/校验 Electron 与 browser-runtime 目录（env 可覆盖）。
func electronPaths(t *testing.T) (electronBin, runtimeDir string) {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller 失败")
	}
	toolkitRoot := filepath.Clean(filepath.Join(filepath.Dir(file), "..", "..", "..")) // uvmp-toolkit/

	electronBin = os.Getenv("BROWSER_RUNTIME_ELECTRON")
	if electronBin == "" {
		name := "electron"
		if runtime.GOOS == "windows" {
			name = "electron.exe"
		}
		electronBin = filepath.Join(toolkitRoot, "desktop", "node_modules", "electron", "dist", name)
	}
	runtimeDir = os.Getenv("BROWSER_RUNTIME_DIR")
	if runtimeDir == "" {
		runtimeDir = filepath.Join(toolkitRoot, "browser-runtime")
	}
	for _, p := range []string{electronBin, filepath.Join(runtimeDir, "package.json"), filepath.Join(runtimeDir, "main.cjs")} {
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("路径不存在（可用 BROWSER_RUNTIME_ELECTRON/BROWSER_RUNTIME_DIR 覆盖）: %v", err)
		}
	}
	return electronBin, runtimeDir
}

// TestITRenderCompat 见文件头说明；未开集成测试开关时跳过。
func TestITRenderCompat(t *testing.T) {
	if os.Getenv("BROWSER_RUNTIME_IT") == "" {
		t.Skip("集成测试默认跳过：设 BROWSER_RUNTIME_IT=1 后运行（可用 BROWSER_RUNTIME_ELECTRON/BROWSER_RUNTIME_DIR 指定路径）")
	}
	electronBin, runtimeDir := electronPaths(t)

	cfg := render.SelftestConfig{
		BrowserPath: electronBin,
		BrowserArgs: []string{runtimeDir}, // 以 browser-runtime 应用目录为宿主
		NoHeadless:  true,
		OutDir:      t.TempDir(),
		Log:         func(line string) { t.Log(line) },
		Progress:    func(step, total int, label string) { t.Logf("进度 %d/%d %s", step, total, label) },
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	res, err := render.Selftest(ctx, cfg)
	if err != nil {
		t.Fatalf("render 自检（browser-runtime 宿主）失败: %v", err)
	}
	if !res.OK || res.PNGBytes <= 0 || res.PDFBytes <= 0 {
		t.Fatalf("自检结果异常: %+v", res)
	}
	if png, err := os.ReadFile(res.PNG); err != nil || len(png) < 8 || string(png[:8]) != "\x89PNG\r\n\x1a\n" {
		t.Fatalf("截图不是 PNG: %v", err)
	}
	if pdf, err := os.ReadFile(res.PDF); err != nil || len(pdf) < 4 || string(pdf[:4]) != "%PDF" {
		t.Fatalf("产物不是 PDF: %v", err)
	}
}
