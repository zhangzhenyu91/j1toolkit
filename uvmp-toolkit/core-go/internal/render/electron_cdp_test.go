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
	"github.com/chromedp/cdproto/target"
	"github.com/chromedp/chromedp"
)

// TestElectronCDPWorker 用安装包自带 Electron（desktop/node_modules/electron/dist）+
// desktop/electron/cdp-worker.cjs 走真实 CDP 链路的集成测试（保留的直接脚本方式）：
// 验证 Options.BrowserArgs/NoHeadless 经 allocator 进入 Electron 命令行后，导航 /
// eval / 新 tab（Electron 不支持 Target.createTarget，走 cdp-worker 桥接开窗）/
// 截图 / printToPDF（Electron CDP 不实现 Page.printToPDF，走 cdp-worker 桥接
// webContents.printToPDF）全部可用。只打开 data:text/html 本地测试页，不访问任何网络。
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

// TestElectronMainWorkerFlag 用源码 Electron + desktop APP + --uvmp-cdp-worker 真实走
// main.cjs 的 worker 分支（打包版/源码版统一入口；打包版 Electron 忽略直接 JS 路径，
// 只能靠该专用参数）：BrowserArgs 组参口径与 exporter.RunConfig 相同（APP 非空
// [APP,worker]）。验证 data 页导航 / eval / 截图 / printToPDF 可用，并确认：
// userData 独立（--user-data-dir 生效、默认 userData 位置零写入）；worker 模式没有
// 主窗口/托盘（createWindow/createTray 同在 GUI whenReady 回调，无 GUI 窗口目标即证明
// 该回调未跑——单实例/核心进程/IPC 同在 worker 分支之后）。只开本地 data: 页。
//
// 默认跳过；设 RENDER_ELECTRON_IT=1 后运行（路径覆盖同 TestElectronCDPWorker）。
func TestElectronMainWorkerFlag(t *testing.T) {
	if os.Getenv("RENDER_ELECTRON_IT") == "" {
		t.Skip("集成测试默认跳过：设 RENDER_ELECTRON_IT=1 后运行（可用 RENDER_ELECTRON_BIN/RENDER_CDP_WORKER 指定路径）")
	}
	electronBin, _ := electronITPaths(t)
	desktopDir := electronITDesktop(t)

	// 默认 userData 沙箱：把 APPDATA/USERPROFILE/HOME/XDG_CONFIG_HOME 指到临时目录，
	// worker 模式若误跑 GUI 分支（Linux userData 重写/单实例配置等）会往默认位置写
	// userData 目录，事后扫描可判零写入。
	sandbox := t.TempDir()
	t.Setenv("APPDATA", sandbox)
	t.Setenv("USERPROFILE", sandbox)
	t.Setenv("HOME", sandbox)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(sandbox, ".config"))

	opts := DefaultOptions(electronBin, t.TempDir())
	opts.BrowserArgs = []string{desktopDir, "--uvmp-cdp-worker"} // 同 RunConfig [APP,worker] 口径
	opts.NoHeadless = true
	opts.Log = func(line string) { t.Log(line) }

	br := newCdpBrowser(opts, &redactor{})
	ctx := context.Background()
	if err := br.start(ctx); err != nil {
		t.Fatalf("启动 Electron（main.cjs --uvmp-cdp-worker）失败: %v", err)
	}
	defer br.close()

	// 0) worker 分支专项：userData 独立 + 没有主窗口/托盘
	// cdp-worker.cjs 只把桥接文件写进 --user-data-dir 钉住的 userData，落盘即证明
	// userData 是独立 profile（顺带证明走的是 worker 分支而非 GUI）
	bridgeFile := filepath.Join(opts.WorkDir, ".chrome-official-tmp", "cdp-worker-bridge.json")
	if _, err := os.Stat(bridgeFile); err != nil {
		t.Fatalf("userData 不独立/未走 worker 分支（%s 缺失）: %v", bridgeFile, err)
	}
	if _, err := os.Stat(filepath.Join(opts.WorkDir, ".chrome-official-tmp", "DevToolsActivePort")); err != nil {
		t.Fatalf("调试端口通告文件未落在独立 userData: %v", err)
	}
	for _, name := range findDefaultUserData(sandbox) {
		t.Fatalf("默认 userData 位置被写入（worker 模式不应执行 GUI/userData 重写逻辑）: %s", name)
	}
	assertNoGuiWindow(t, ctx, br)

	// 1) 主 target（main.cjs 转交的 cdp-worker.cjs 隐藏窗口）导航到本地 data 页并 eval
	mainPage := dataURL("main-page", "hello")
	if err := br.navigate(ctx, mainPage); err != nil {
		t.Fatalf("导航失败: %v", err)
	}
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

	// 3) 新 tab：独立 target 导航 + eval + printToPDF（走 cdp-worker 桥接）
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

// findDefaultUserData 扫描被重定向的默认 userData 根（sandbox 与其 .config）里
// 是否出现了 Electron/本应用的 userData 目录（uvmp*/Shade*/Electron*），返回命中清单。
func findDefaultUserData(sandbox string) []string {
	var hits []string
	roots := []string{sandbox, filepath.Join(sandbox, ".config")}
	for _, root := range roots {
		entries, err := os.ReadDir(root)
		if err != nil {
			continue
		}
		for _, e := range entries {
			n := e.Name()
			low := strings.ToLower(n)
			if strings.Contains(low, "uvmp") || strings.Contains(low, "shade") || strings.Contains(low, "electron") {
				hits = append(hits, filepath.Join(root, n))
			}
		}
	}
	return hits
}

// assertNoGuiWindow 枚举 CDP target 断言没有 GUI 主窗口：主窗口加载
// file://…/desktop/dist/index.html、标题「Shade 壹匣 - 内网」，与 worker 的
// about:blank/data: 页可区分。托盘与主窗口同在 GUI whenReady 回调创建（见 main.cjs），
// 无 GUI 窗口即证明该回调未执行、托盘同样未创建。
func assertNoGuiWindow(t *testing.T, ctx context.Context, br *cdpBrowser) {
	t.Helper()
	var res target.GetTargetsResult
	err := runOn(ctx, br.ctx, evalTimeout, chromedp.Func(func(cctx context.Context, tgt *chromedp.Target) error {
		var callErr error
		res, callErr = cdp.Call(cctx, tgt, target.GetTargets, target.GetTargetsParams{})
		return callErr
	}))
	if err != nil {
		t.Fatalf("枚举 CDP target 失败: %v", err)
	}
	pages := 0
	for _, ti := range res.TargetInfos {
		if ti == nil {
			continue
		}
		if ti.Type == "page" {
			pages++
		}
		if strings.HasPrefix(ti.URL, "file://") {
			t.Fatalf("发现 file:// 窗口（GUI 主窗口）: %s", ti.URL)
		}
		if strings.Contains(ti.Title, "Shade 壹匣") {
			t.Fatalf("发现 GUI 主窗口标题: %q", ti.Title)
		}
	}
	if pages != 1 {
		t.Fatalf("worker 模式应只有 1 个 page target，实际 %d：%+v", pages, res.TargetInfos)
	}
}

// electronITDesktop 推断/校验源码 Electron 应用目录 uvmp-toolkit/desktop
// （main.cjs 所在应用；RENDER_ELECTRON_BIN/RENDER_CDP_WORKER 覆盖不影响它）。
func electronITDesktop(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller 失败")
	}
	toolkitRoot := filepath.Clean(filepath.Join(filepath.Dir(file), "..", "..", "..")) // uvmp-toolkit/
	desktopDir := filepath.Join(toolkitRoot, "desktop")
	for _, p := range []string{filepath.Join(desktopDir, "package.json"), filepath.Join(desktopDir, "electron", "main.cjs")} {
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("desktop 应用目录不完整: %v", err)
		}
	}
	return desktopDir
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
