package render

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/chromedp"
)

// TestElectronCDPWorker 用安装包自带 Electron（desktop/node_modules/electron/dist）+
// desktop/electron/cdp-worker.cjs 走真实 CDP 链路的集成测试：验证 Options.BrowserArgs/
// NoHeadless 经 allocator 进入 Electron 命令行后，导航 / eval / 新 tab（Electron 不支持
// Target.createTarget，走 cdp-worker 桥接开窗）/ 截图 / printToPDF（Electron CDP 不实现
// Page.printToPDF，走 cdp-worker 桥接 webContents.printToPDF）全部可用。
// 只打开 data:text/html 本地测试页，不访问任何网络。
//
// 默认跳过；设 RENDER_ELECTRON_IT=1 后运行。可用 RENDER_ELECTRON_BIN /
// RENDER_CDP_WORKER 覆盖两个路径（默认按仓库布局推断）。
func TestElectronCDPWorker(t *testing.T) {
	if os.Getenv("RENDER_ELECTRON_IT") == "" {
		t.Skip("集成测试默认跳过：设 RENDER_ELECTRON_IT=1 后运行（可用 RENDER_ELECTRON_BIN/RENDER_CDP_WORKER 指定路径）")
	}
	electronBin, workerScript := electronITPaths(t)

	opts := DefaultOptions(electronBin, t.TempDir())
	opts.BrowserArgs = []string{workerScript}
	opts.NoHeadless = true
	opts.Log = func(line string) { t.Log(line) }

	br := newCdpBrowser(opts, &redactor{})
	ctx := context.Background()
	if err := br.start(ctx); err != nil {
		t.Fatalf("启动 Electron CDP worker 失败: %v", err)
	}
	defer br.close()

	// 1) 主 target（cdp-worker.cjs 的隐藏窗口）导航到本地 data 页并 eval
	mainPage := dataURL("main-page", "hello")
	if err := br.navigate(ctx, mainPage); err != nil {
		t.Fatalf("导航失败: %v", err)
	}
	// navigate 不等整页加载，轮询等新文档就绪
	if !br.waitExpr(ctx, "document.title === 'main-page'", 15*time.Second, 300*time.Millisecond) {
		t.Fatalf("等待主 target 导航就绪超时；console: %v", br.consoleSnapshot())
	}
	title, err := br.evalString(ctx, "document.title", evalTimeout)
	if err != nil || title != "main-page" {
		t.Fatalf("eval document.title = %q, %v；want main-page", title, err)
	}
	v, err := br.eval(ctx, "1+2", evalTimeout)
	if err != nil || fmt.Sprint(v) != "3" {
		t.Fatalf("eval 1+2 = %v, %v；want 3", v, err)
	}

	// 2) 截图（裁剪内容区落盘 PNG）
	pngPath := filepath.Join(opts.WorkDir, "shot.png")
	if err := br.screenshotClip(ctx, clipRect{X: 0, Y: 0, Width: 800, Height: 600}, pngPath); err != nil {
		t.Fatalf("截图失败: %v", err)
	}
	pngData, err := os.ReadFile(pngPath)
	if err != nil {
		t.Fatalf("读截图失败: %v", err)
	}
	if !strings.HasPrefix(string(pngData), "\x89PNG") {
		t.Fatalf("截图不是 PNG（%d 字节）", len(pngData))
	}

	// 3) 新 tab：独立 target 导航 + eval + printToPDF
	tabCtx, tabCancel, err := br.newTab(ctx)
	if err != nil {
		t.Fatalf("新 tab 初始化失败: %v", err)
	}
	defer tabCancel()
	if v, err := evalOn(ctx, tabCtx, "1+1", evalTimeout); err != nil || fmt.Sprint(v) != "2" {
		t.Fatalf("新 tab eval = %v, %v", v, err)
	}
	navigateOn(t, ctx, tabCtx, dataURL("tab-page", "pdf body"))
	if !br.waitExprOn(ctx, tabCtx, "document.title === 'tab-page'", 15*time.Second, 300*time.Millisecond) {
		t.Fatalf("等待新 tab 导航就绪超时；console: %v", br.consoleSnapshot())
	}
	tabTitle, err := evalOn(ctx, tabCtx, "document.title", evalTimeout)
	if err != nil || fmt.Sprint(tabTitle) != "tab-page" {
		t.Fatalf("新 tab eval document.title = %v, %v；want tab-page", tabTitle, err)
	}
	pdfData, err := br.tabPrintPDF(ctx, tabCtx)
	if err != nil {
		t.Fatalf("printToPDF 失败: %v", err)
	}
	if !strings.HasPrefix(string(pdfData), "%PDF") {
		t.Fatalf("printToPDF 产物不是 PDF（%d 字节）", len(pdfData))
	}
}

// electronITPaths 推断/校验 Electron 与 cdp-worker.cjs 路径（env 可覆盖）。
func electronITPaths(t *testing.T) (electronBin, workerScript string) {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller 失败")
	}
	toolkitRoot := filepath.Clean(filepath.Join(filepath.Dir(file), "..", "..", "..")) // uvmp-toolkit/

	electronBin = os.Getenv("RENDER_ELECTRON_BIN")
	if electronBin == "" {
		name := "electron"
		if runtime.GOOS == "windows" {
			name = "electron.exe"
		}
		electronBin = filepath.Join(toolkitRoot, "desktop", "node_modules", "electron", "dist", name)
	}
	workerScript = os.Getenv("RENDER_CDP_WORKER")
	if workerScript == "" {
		workerScript = filepath.Join(toolkitRoot, "desktop", "electron", "cdp-worker.cjs")
	}
	for _, p := range []string{electronBin, workerScript} {
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("路径不存在（可用 RENDER_ELECTRON_BIN/RENDER_CDP_WORKER 覆盖）: %v", err)
		}
	}
	return electronBin, workerScript
}

// dataURL 生成纯本地 data:text/html 测试页（base64 编码，避免转义问题）。
func dataURL(title, body string) string {
	html := "<html><head><title>" + title + "</title></head><body>" + body + "</body></html>"
	return "data:text/html;base64," + base64.StdEncoding.EncodeToString([]byte(html))
}

// navigateOn 在指定标签页上下文导航（与 b.navigate 同口径，目标为 tabCtx）。
func navigateOn(t *testing.T, jobCtx, tabCtx context.Context, url string) {
	t.Helper()
	err := runOn(jobCtx, tabCtx, evalTimeout, chromedp.Func(func(cctx context.Context, tgt *chromedp.Target) error {
		res, err := cdp.Call(cctx, tgt, page.Navigate, page.NavigateParams{URL: url})
		if err != nil {
			return err
		}
		if res.ErrorText != "" {
			return errors.New(res.ErrorText)
		}
		return nil
	}))
	if err != nil {
		t.Fatalf("tab 导航失败: %v", err)
	}
}
