package render

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	cdpbrowser "github.com/chromedp/cdproto/browser"
	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/log"
	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"
	"github.com/chromedp/chromedp/remote"
)

// eval 默认超时（与 Python eval(timeout=30) 口径一致）。
const evalTimeout = 30 * time.Second

// consoleRingCap 控制台/网络日志环形缓冲长度（超过后丢弃最早 100 条，与 Python 一致）。
const consoleRingCap = 300

// cdpBrowser 独立 Chromium 进程与 CDP 会话（对应 official_track.py 的 CdpBrowser）：
// 独立 profile、随机 CDP 调试端口、crypto.random 注入、console/网络事件捕获、
// 条件等待、网络静默检测、截图与失败诊断打包。
//
// 宿主浏览器（默认，NoHeadless=false）由本包直接拉起并管理进程：Chromium 系浏览器
// （Edge/360ent 等）不一定输出 "DevTools listening on" 行，故就绪以 profile 目录的
// DevToolsActivePort 通告为准，再用 remote 分配器连 WebSocket。Electron 回退
// （NoHeadless=true）仍由 chromedp ExecAllocator 拉起（其 stdout 通告可用）。
type cdpBrowser struct {
	opts Options
	red  *redactor
	net  *netTracker

	mu      sync.Mutex
	console []string // 页面 console/异常/网络错误环形缓冲（诊断用）

	allocCancel context.CancelFunc
	ctx         context.Context // 浏览器根上下文（首个标签页）
	cancel      context.CancelFunc
	cmd         *exec.Cmd
	hostDone    chan struct{} // 宿主浏览器进程退出（cmd.Wait 完成）

	logFile     *os.File
	logRed      *redactingWriter
	closeOnce   sync.Once
	startedPump bool
}

func newCdpBrowser(opts Options, red *redactor) *cdpBrowser {
	return &cdpBrowser{opts: opts, red: red, net: newNetTracker()}
}

// profileDir 独立浏览器 profile 目录。
func (b *cdpBrowser) profileDir() string {
	return filepath.Join(b.opts.WorkDir, ".chrome-official-tmp")
}

// logPath 浏览器进程日志（stderr/stdout 脱敏后落盘）。
func (b *cdpBrowser) logPath() string {
	return filepath.Join(b.opts.WorkDir, "official_chrome.log")
}

// start 启动独立 Chromium 进程并建立 CDP 会话。首次 Run 不套超时（超时上下文会连带
// 停掉整个浏览器）；启动上下文取消即关浏览器。
func (b *cdpBrowser) start(ctx context.Context) error {
	if err := os.MkdirAll(b.profileDir(), 0o755); err != nil {
		return fmt.Errorf("render: 创建浏览器 profile 目录失败: %w", err)
	}
	// 防上次残留的 DevToolsActivePort 干扰（移植自 CdpBrowser 启动清理）
	_ = os.Remove(filepath.Join(b.profileDir(), "DevToolsActivePort"))

	f, err := os.Create(b.logPath())
	if err != nil {
		return fmt.Errorf("render: 创建浏览器日志失败: %w", err)
	}
	b.logFile = f
	b.logRed = newRedactingWriter(b.red, f)

	if b.opts.NoHeadless {
		return b.startElectron(ctx)
	}
	return b.startHost(ctx)
}

// startElectron 历史 Electron 回退：由 chromedp ExecAllocator 拉起 browser-runtime
// 宿主（其 stdout 会通告 DevTools 地址，ModifyCmdFunc 负责位置参数与进程回收）。
func (b *cdpBrowser) startElectron(ctx context.Context) error {
	allocOpts := []chromedp.ExecAllocatorOption{
		remote.WebSocket, // 走调试端口 WebSocket（配合 --remote-debugging-port）
		chromedp.ExecPath(b.opts.BrowserPath),
		chromedp.CombinedOutput(b.logRed),
	}
	for _, s := range b.opts.switches(b.profileDir()) {
		allocOpts = append(allocOpts, chromedp.Flag(s.name, s.value))
	}
	// chromedp 只产出 --开关 + 末尾 about:blank，ModifyCmdFunc 可在其间插位置参数；
	// 它会替换默认进程设置，故先复刻默认设置，再按需改写 Args 并留存进程句柄供退出清理。
	extra := append([]string(nil), b.opts.BrowserArgs...)
	allocOpts = append(allocOpts, chromedp.ModifyCmdFunc(func(cmd *exec.Cmd) {
		defaultSysProcAttr(cmd)
		cmd.Args = insertBrowserArgs(cmd.Args, extra)
		b.cmd = cmd
	}))

	allocCtx, allocCancel := chromedp.NewExecAllocator(ctx, allocOpts...)
	b.allocCancel = allocCancel
	b.ctx, b.cancel = chromedp.NewContext(allocCtx)
	if err := b.initFirstTab(); err != nil {
		b.close()
		return fmt.Errorf("render: 启动浏览器失败: %w", err)
	}
	b.startEventPumps()
	return nil
}

// startHost 宿主浏览器（Edge/360ent/Chrome 等）：本包直接拉起进程，就绪以
// DevToolsActivePort 通告为准，再用 remote 分配器连 WebSocket。Chromium 系宿主不一定
// 输出 "DevTools listening on"（Edge 实测不输出），故不能依赖 chromedp 的 stdout 解析。
func (b *cdpBrowser) startHost(ctx context.Context) error {
	args := formatSwitches(b.opts.switches(b.profileDir()))
	args = append(args, "about:blank")
	cmd := exec.Command(b.opts.BrowserPath, args...)
	defaultSysProcAttr(cmd)
	cmd.Stdout = b.logRed
	cmd.Stderr = b.logRed
	if err := cmd.Start(); err != nil {
		b.close()
		return fmt.Errorf("render: 启动浏览器失败: %w", err)
	}
	b.cmd = cmd
	b.hostDone = make(chan struct{})
	go func() {
		_ = cmd.Wait()
		close(b.hostDone)
	}()

	port, wsPath, err := b.waitDevToolsPort(ctx)
	if err != nil {
		b.close()
		// Windows 启动器可能已退出、浏览器主进程仍在：按专用 profile 兜底清理
		cleanupBrowserByProfile(b.profileDir())
		return fmt.Errorf("render: 启动浏览器失败: %w", err)
	}
	wsURL := fmt.Sprintf("ws://127.0.0.1:%d%s", port, wsPath)
	allocCtx, allocCancel := remote.NewAllocator(ctx, wsURL)
	b.allocCancel = allocCancel
	b.ctx, b.cancel = chromedp.NewContext(allocCtx)
	if err := b.initFirstTab(); err != nil {
		b.close()
		// CDP 初始化失败时 Browser.close 可能同样不可用：按专用 profile 兜底清理主进程
		cleanupBrowserByProfile(b.profileDir())
		return fmt.Errorf("render: 启动浏览器失败: %w", err)
	}
	b.startEventPumps()
	return nil
}

// waitDevToolsPort 等待浏览器写出 DevToolsActivePort（首行端口、次行 browser WS 路径），
// 进程提前退出或超时返回明确错误。Windows 上 Edge 的启动器可能先退出、由浏览器主进程
// 继续写通告，故进程退出后再宽限数秒继续等文件。
func (b *cdpBrowser) waitDevToolsPort(ctx context.Context) (int, string, error) {
	const (
		readyTimeout = 60 * time.Second
		exitGrace    = 5 * time.Second
	)
	deadline := time.Now().Add(readyTimeout)
	var exitedAt time.Time
	doneCh := b.hostDone
	for {
		if port, wsPath, err := readDevToolsActivePort(b.profileDir()); err == nil {
			return port, wsPath, nil
		}
		if !exitedAt.IsZero() && time.Since(exitedAt) > exitGrace {
			return 0, "", fmt.Errorf("浏览器进程提前退出（日志见 %s）", b.logPath())
		}
		if time.Now().After(deadline) {
			return 0, "", fmt.Errorf("等待 DevToolsActivePort 超时（%s，日志见 %s）", readyTimeout, b.logPath())
		}
		select {
		case <-ctx.Done():
			return 0, "", ctx.Err()
		case <-doneCh:
			exitedAt = time.Now()
			doneCh = nil
		case <-time.After(200 * time.Millisecond):
		}
	}
}

// readDevToolsActivePort 读取 profile 目录的 Chromium 调试端口通告文件。
func readDevToolsActivePort(profileDir string) (int, string, error) {
	raw, err := os.ReadFile(filepath.Join(profileDir, "DevToolsActivePort"))
	if err != nil {
		return 0, "", err
	}
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if len(lines) < 2 {
		return 0, "", fmt.Errorf("DevToolsActivePort 内容不完整")
	}
	port, err := strconv.Atoi(strings.TrimSpace(lines[0]))
	if err != nil || port <= 0 {
		return 0, "", fmt.Errorf("DevToolsActivePort 端口无效: %q", lines[0])
	}
	wsPath := strings.TrimSpace(lines[1])
	if !strings.HasPrefix(wsPath, "/") {
		return 0, "", fmt.Errorf("DevToolsActivePort 路径无效: %q", wsPath)
	}
	return port, wsPath, nil
}

// initFirstTab 启动首个标签页并启用所需域，在任何页面脚本前注入 crypto.random 补齐。
func (b *cdpBrowser) initFirstTab() error {
	return chromedp.Do(b.ctx, chromedp.Func(func(cctx context.Context, t *chromedp.Target) error {
		if _, err := cdp.Call(cctx, t, page.Enable, page.EnableParams{}); err != nil {
			return err
		}
		if _, err := cdp.Call(cctx, t, runtime.Enable, cdp.Empty{}); err != nil {
			return err
		}
		if _, err := cdp.Call(cctx, t, log.Enable, cdp.Empty{}); err != nil {
			return err
		}
		if _, err := cdp.Call(cctx, t, network.Enable, network.EnableParams{}); err != nil {
			return err
		}
		_, err := cdp.Call(cctx, t, page.AddScriptToEvaluateOnNewDocument, page.AddScriptToEvaluateOnNewDocumentParams{
			Source: cryptoRandomPolyfillJS,
		})
		return err
	}))
}

// startEventPumps 泵入 console/异常/网络事件（对应 Python call() 等待期间的 _capture_event）。
func (b *cdpBrowser) startEventPumps() {
	if b.startedPump {
		return
	}
	b.startedPump = true

	go func() {
		for ev, err := range chromedp.Events(b.ctx, runtime.ConsoleAPICalled) {
			if err != nil {
				return
			}
			args := make([]string, 0, len(ev.Args))
			for _, a := range ev.Args {
				args = append(args, trimForDiag(consoleArg(a), 300))
			}
			b.addConsole("[console.%s] %s", ev.Type, strings.Join(args, " "))
		}
	}()
	go func() {
		for ev, err := range chromedp.Events(b.ctx, runtime.ExceptionThrown) {
			if err != nil {
				return
			}
			d := ev.ExceptionDetails
			if d == nil {
				continue
			}
			desc := ""
			if d.Exception != nil {
				desc = d.Exception.Description
			}
			b.addConsole("[异常] %s %s", d.Text, trimForDiag(desc, 400))
		}
	}()
	go func() {
		for ev, err := range chromedp.Events(b.ctx, log.EntryAdded) {
			if err != nil {
				return
			}
			if ev.Entry == nil {
				continue
			}
			b.addConsole("[log.%s] %s", ev.Entry.Level, trimForDiag(ev.Entry.Text, 300))
		}
	}()
	go func() {
		for ev, err := range chromedp.Events(b.ctx, network.RequestWillBeSent) {
			if err != nil {
				return
			}
			url := ""
			if ev.Request != nil {
				url = ev.Request.URL
			}
			b.net.request(string(ev.RequestID), url)
		}
	}()
	go func() {
		for ev, err := range chromedp.Events(b.ctx, network.ResponseReceived) {
			if err != nil {
				return
			}
			if ev.Response == nil {
				continue
			}
			status := int(ev.Response.Status)
			if status >= 400 {
				b.addConsole("[net.%d] %s", status, trimForDiag(ev.Response.URL, 220))
			}
		}
	}()
	go func() {
		for ev, err := range chromedp.Events(b.ctx, network.LoadingFailed) {
			if err != nil {
				return
			}
			url := b.net.finish(string(ev.RequestID))
			errText := ev.ErrorText
			if strings.Contains(errText, "ERR_ABORTED") {
				continue // 导航中断属常态噪音，不记
			}
			b.addConsole("[net.fail] %s %s", errText, trimForDiag(url, 220))
		}
	}()
	go func() {
		for ev, err := range chromedp.Events(b.ctx, network.LoadingFinished) {
			if err != nil {
				return
			}
			b.net.finish(string(ev.RequestID)) // 在途表出清（网络静默的信号源）
		}
	}()
}

// consoleArg 取 console 参数的可读文本（值优先，退化用描述）。
func consoleArg(a *runtime.RemoteObject) string {
	if a == nil {
		return ""
	}
	if len(a.Value) > 0 {
		var v any
		if err := json.Unmarshal(a.Value, &v); err == nil {
			return fmt.Sprintf("%v", v)
		}
		return string(a.Value)
	}
	return a.Description
}

// addConsole 写入诊断环形缓冲。
func (b *cdpBrowser) addConsole(format string, args ...any) {
	line := b.red.apply(fmt.Sprintf(format, args...))
	b.mu.Lock()
	defer b.mu.Unlock()
	b.console = append(b.console, line)
	if len(b.console) > consoleRingCap {
		b.console = b.console[100:]
	}
}

// consoleSnapshot 诊断用日志快照。
func (b *cdpBrowser) consoleSnapshot() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]string, len(b.console))
	copy(out, b.console)
	return out
}

// evalOn 在标签页上下文 tabCtx 上求值；timeout 或 jobCtx 取消则中止本次调用
// （tabCtx 本身已随浏览器生命周期，中止调用不停浏览器）。
func evalOn(jobCtx, tabCtx context.Context, expr string, timeout time.Duration) (any, error) {
	if timeout <= 0 {
		timeout = evalTimeout
	}
	tctx, cancel := context.WithTimeout(tabCtx, timeout)
	defer cancel()
	stop := context.AfterFunc(jobCtx, cancel)
	defer stop()
	return chromedp.Run(tctx, chromedp.Evaluate[any](expr))
}

// runOn 在标签页上下文上执行动作（超时/取消口径同 evalOn）。
func runOn(jobCtx, tabCtx context.Context, timeout time.Duration, a chromedp.Action[chromedp.Void]) error {
	if timeout <= 0 {
		timeout = evalTimeout
	}
	tctx, cancel := context.WithTimeout(tabCtx, timeout)
	defer cancel()
	stop := context.AfterFunc(jobCtx, cancel)
	defer stop()
	return chromedp.Do(tctx, a)
}

// eval 在主标签页执行表达式并返回 JS 值（null/异常按错误返回，对应 Python eval 的
// 「失败给 None」：调用方按需忽略错误）。
func (b *cdpBrowser) eval(ctx context.Context, expr string, timeout time.Duration) (any, error) {
	return evalOn(ctx, b.ctx, expr, timeout)
}

// evalString 执行表达式并按字符串取值（非字符串返回空串）。
func (b *cdpBrowser) evalString(ctx context.Context, expr string, timeout time.Duration) (string, error) {
	v, err := b.eval(ctx, expr, timeout)
	if err != nil {
		return "", err
	}
	s, _ := v.(string)
	return s, nil
}

// waitExpr 在主标签页轮询直到表达式严格为 true（对应 Python wait_expr）。
func (b *cdpBrowser) waitExpr(ctx context.Context, expr string, timeout, interval time.Duration) bool {
	return b.waitExprOn(ctx, b.ctx, expr, timeout, interval)
}

// waitExprOn 在指定标签页上下文轮询条件（替代固定 sleep）。
func (b *cdpBrowser) waitExprOn(jobCtx, tabCtx context.Context, expr string, timeout, interval time.Duration) bool {
	if interval <= 0 {
		interval = 300 * time.Millisecond
	}
	deadline := time.Now().Add(timeout)
	for {
		v, err := evalOn(jobCtx, tabCtx, expr, evalTimeout)
		if err == nil {
			if ok, isBool := v.(bool); isBool && ok {
				return true
			}
		}
		if time.Now().After(deadline) {
			return false
		}
		select {
		case <-jobCtx.Done():
			return false
		case <-time.After(interval):
		}
	}
}

// waitNetworkIdle 等网络请求静默（地图瓦片/接口「加载完成」的信号）：匹配的在途请求归零
// 且持续 quiet，且先累计见到 minSeen 个匹配请求（防「还没开始加载就误判静默」）。
// timeout 为兜底上限，返回是否静默完成。
func (b *cdpBrowser) waitNetworkIdle(ctx context.Context, quiet, timeout time.Duration, urlSubstr string, minSeen int) bool {
	baseDone, _ := b.net.counts(urlSubstr)
	tracker := newIdleTracker(quiet, minSeen, baseDone)
	deadline := time.Now().Add(timeout)
	for {
		doneN, busy := b.net.counts(urlSubstr)
		if tracker.update(time.Now(), doneN, len(busy)) {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		select {
		case <-ctx.Done():
			return false
		case <-time.After(400 * time.Millisecond):
		}
	}
}

// navigate 打开 URL（不等整页加载，与 Python Page.navigate 一致）。
func (b *cdpBrowser) navigate(ctx context.Context, url string) error {
	return runOn(ctx, b.ctx, evalTimeout, chromedp.Func(func(cctx context.Context, t *chromedp.Target) error {
		res, err := cdp.Call(cctx, t, page.Navigate, page.NavigateParams{URL: url})
		if err != nil {
			return err
		}
		if res.ErrorText != "" {
			return fmt.Errorf("导航失败: %s", res.ErrorText)
		}
		return nil
	}))
}

// clipRect 截图裁剪框（对应 Python _render 里的 clip 字典）。
type clipRect struct {
	X      float64
	Y      float64
	Width  float64
	Height float64
}

// screenshotClip 按内容区裁剪截图并落盘 PNG。
func (b *cdpBrowser) screenshotClip(ctx context.Context, clip clipRect, outPNG string) error {
	var data []byte
	err := runOn(ctx, b.ctx, evalTimeout, chromedp.Func(func(cctx context.Context, t *chromedp.Target) error {
		res, err := cdp.Call(cctx, t, page.CaptureScreenshot, page.CaptureScreenshotParams{
			Format: page.CaptureScreenshotFormatPng,
			Clip: &page.Viewport{
				X:      clip.X,
				Y:      clip.Y,
				Width:  clip.Width,
				Height: clip.Height,
				Scale:  1,
			},
		})
		if err != nil {
			return err
		}
		if len(res.Data) == 0 {
			return fmt.Errorf("截图失败: 无数据")
		}
		data = res.Data
		return nil
	}))
	if err != nil {
		return err
	}
	return os.WriteFile(outPNG, data, 0o644)
}

// capturePNG 整页截图（诊断用），返回 PNG 字节。
func (b *cdpBrowser) capturePNG(ctx context.Context) ([]byte, error) {
	var data []byte
	err := runOn(ctx, b.ctx, evalTimeout, chromedp.Func(func(cctx context.Context, t *chromedp.Target) error {
		res, err := cdp.Call(cctx, t, page.CaptureScreenshot, page.CaptureScreenshotParams{
			Format: page.CaptureScreenshotFormatPng,
		})
		if err != nil {
			return err
		}
		data = res.Data
		return nil
	}))
	return data, err
}

// newTab 打开新标签页（对应 Python new_tab），返回标签页上下文与关闭函数
// （取消即关标签页）。普通 Chromium 走 chromedp Target.createTarget 开 tab；
// Electron 等不支持 createTarget 的宿主（-32000 Not supported）改经 cdp-worker.cjs
// 开不可见新窗口后附加（见 bridge.go）。
func (b *cdpBrowser) newTab(jobCtx context.Context) (context.Context, context.CancelFunc, error) {
	if b.hasBridge() {
		return b.newTabViaBridge(jobCtx)
	}
	tabCtx, tabCancel := chromedp.NewContext(b.ctx)
	if err := b.initTab(jobCtx, tabCtx); err != nil {
		tabCancel()
		if isCreateTargetMissing(err) {
			if tabCtx2, cancel2, err2 := b.newTabViaBridge(jobCtx); err2 == nil {
				return tabCtx2, cancel2, nil
			}
		}
		return nil, nil, err
	}
	return tabCtx, tabCancel, nil
}

// initTab 启用新标签页的 Page 域（与 Python new_tab 一致）。
// 注意：首次 Do 会触发目标 attach，chromedp attachTarget 用传入 ctx 启动目标事件循环
// （Target.run）——必须用与标签页同生命周期的 tabCtx 直接跑，不能套临时超时 ctx，
// 否则 Do 返回后事件循环随超时 ctx 退出，后续命令全部挂死（实测）。超时/取消只约束
// 本次等待，不带走事件循环。
func (b *cdpBrowser) initTab(jobCtx, tabCtx context.Context) error {
	type result struct{ err error }
	done := make(chan result, 1)
	go func() {
		done <- result{chromedp.Do(tabCtx, chromedp.Func(func(cctx context.Context, t *chromedp.Target) error {
			_, err := cdp.Call(cctx, t, page.Enable, page.EnableParams{})
			return err
		}))}
	}()
	select {
	case r := <-done:
		return r.err
	case <-jobCtx.Done():
		return jobCtx.Err()
	case <-time.After(evalTimeout):
		return fmt.Errorf("render: 初始化标签页超时")
	}
}

// tabPrintPDF 在标签页上 printToPDF（preferCSSPageSize + printBackground，与 Python 一致）。
// 宿主未实现 Page.printToPDF（Electron 等，-32601 wasn't found）时自动改走
// cdp-worker.cjs 的本地兜底（webContents.printToPDF，见 bridge.go）；普通 Chromium
// 仍是纯 CDP 调用，行为不变。
func (b *cdpBrowser) tabPrintPDF(jobCtx, tabCtx context.Context) ([]byte, error) {
	var data []byte
	err := runOn(jobCtx, tabCtx, 2*evalTimeout, chromedp.Func(func(cctx context.Context, t *chromedp.Target) error {
		res, err := cdp.Call(cctx, t, page.PrintToPDF, page.PrintToPDFParams{
			PreferCSSPageSize: boolPtr(true),
			PrintBackground:   boolPtr(true),
		})
		if err != nil {
			return err
		}
		if len(res.Data) == 0 {
			return fmt.Errorf("printToPDF 无数据")
		}
		data = res.Data
		return nil
	}))
	if err == nil {
		return data, nil
	}
	if !isPrintToPDFMissing(err) {
		return nil, err
	}
	return b.printToPDFViaWorker(jobCtx, tabCtx)
}

// dumpDiag 失败现场打包：console 日志 + 整页截图，文件名带 tag（对应 Python dump_diag）。
func (b *cdpBrowser) dumpDiag(ctx context.Context, outDir, tag string) {
	defer func() { _ = recover() }() // 诊断失败不影响主流程（与 Python 宽容一致）
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return
	}
	if lines := b.consoleSnapshot(); len(lines) > 0 {
		_ = os.WriteFile(filepath.Join(outDir, "diag_console_"+tag+".log"),
			[]byte(b.red.apply(strings.Join(lines, "\n"))), 0o644)
	}
	data, err := b.capturePNG(ctx)
	if err == nil && len(data) > 0 {
		_ = os.WriteFile(filepath.Join(outDir, "diag_page_"+tag+".png"), data, 0o644)
	}
	if b.opts.Log != nil {
		b.opts.Log(fmt.Sprintf("  [诊断] console 日志与截图已写入 %s（diag_*_%s.*）", outDir, tag))
	}
}

// close 关闭 CDP 会话并终止浏览器进程（对应 Python CdpBrowser.close）。宿主浏览器由
// 本包直接拉起：先经 CDP Browser.close 让浏览器自行退出（Windows 上启动器可能已退出、
// 浏览器主进程仍在），再兜底终止进程树并等待退出；Electron 回退沿 chromedp 默认清理。
func (b *cdpBrowser) close() {
	b.closeOnce.Do(func() {
		if b.hostDone != nil {
			b.closeHostBrowser()
		}
		if b.cancel != nil {
			b.cancel()
		}
		if b.allocCancel != nil {
			b.allocCancel()
		}
		if b.hostDone == nil {
			terminateBrowserProcess(b.cmd)
		}
		if b.logRed != nil {
			_ = b.logRed.Flush()
		}
		if b.logFile != nil {
			_ = b.logFile.Close()
		}
	})
}

// closeHostBrowser 宿主浏览器优雅退出：CDP Browser.close（超时兜底）→ 等进程退出 →
// 仍存活则终止进程树。Chromium 的 Browser.close 会关闭整个浏览器进程（含 Windows
// 启动器派生出的主进程），避免只杀掉外层启动器留下占 profile 的子进程。
func (b *cdpBrowser) closeHostBrowser() {
	if b.ctx != nil {
		done := make(chan struct{})
		go func() {
			_, _ = chromedp.Run(b.ctx, chromedp.Func(func(cctx context.Context, t *chromedp.Target) error {
				_, err := cdp.Call(cctx, t, cdpbrowser.Close, cdp.Empty{})
				return err
			}))
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
		}
	}
	select {
	case <-b.hostDone:
	case <-time.After(2 * time.Second):
		terminateBrowserProcess(b.cmd)
		select {
		case <-b.hostDone:
		case <-time.After(2 * time.Second):
		}
	}
}

// boolPtr 布尔指针字面量（cdproto 可选参数用）。
func boolPtr(v bool) *bool { return &v }
