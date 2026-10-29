package render

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/chromedp"
)

// 内置 CDP 浏览器自检（uvmp-export selftest 的生产实现）：复用与导出渲染同一套
// CDP 链路（含 Electron 缺失方法的 cdp-worker 桥），只打开 data:text/html 本地页，
// 依次验证导航 / eval / 新 tab / 截图 PNG / printToPDF，真实产物写 UVMP_SELFTEST_DIR
// （Python 侧传 state.logs_dir()），不访问任何网络。供设置页「内置 CDP 渲染自检」调用，
// 与 electron_cdp_test.go 的集成测试同链路、非测试形态。

// 自检产物与工作目录名（写入 OutDir）。
const (
	selftestPNGName     = "cdp-selftest.png"
	selftestPDFName     = "cdp-selftest.pdf"
	selftestWorkDirName = "cdp-selftest-work"
)

// SelftestConfig 内置 CDP 浏览器自检参数（uvmp-export selftest 的运行配置）。
type SelftestConfig struct {
	// BrowserPath 浏览器可执行文件（内置 Electron 或显式 render.chrome，由
	// ParseSelftestConfig 解析）。
	BrowserPath string
	// BrowserArgs 宿主位置参数：内置 Electron 的 [APP, worker] / [worker]；
	// 普通 Chromium（显式 render.chrome）留空。
	BrowserArgs []string
	// NoHeadless 不传 --headless=new（Electron 宿主不支持）。
	NoHeadless bool
	// OutDir 真实产物目录（UVMP_SELFTEST_DIR）。
	OutDir string

	// Log 每步日志回调（每行一条）；nil 则丢弃。
	Log func(line string)
	// Progress 步骤进度回调（step 从 1 起、total 固定 5）；nil 则丢弃。
	Progress func(step, total int, label string)
}

// SelftestResult 自检结果（done.result 的 JSON 形状：{ok,png,pdf,...}）。
type SelftestResult struct {
	OK       bool     `json:"ok"`
	PNG      string   `json:"png"`
	PDF      string   `json:"pdf"`
	PNGBytes int64    `json:"png_bytes"`
	PDFBytes int64    `json:"pdf_bytes"`
	Browser  string   `json:"browser"`
	WorkDir  string   `json:"work_dir"`
	Checks   []string `json:"checks"`
}

// ParseSelftestConfig 从 config.load 字典与环境变量组装自检参数（浏览器口径与
// exporter.ParseRunConfig 相同）：显式 render.chrome（config.json render 或
// config.ini [render]）优先作兼容回退，普通 Chromium 零位置参数；否则
// render.builtin=true（默认）必须用 UVMP_BROWSER_BIN 指定的内置浏览器，配
// UVMP_BROWSER_WORKER（--uvmp-cdp-worker 或 worker 脚本路径）与 UVMP_BROWSER_APP
// （源码 Electron 的 desktop 应用目录）组位置参数并去掉 --headless=new；
// 不自动探测宿主浏览器，缺浏览器明确报错。outDir 为 UVMP_SELFTEST_DIR（必填）。
func ParseSelftestConfig(cfg map[string]any, getenv func(string) string, outDir string) (SelftestConfig, error) {
	if getenv == nil {
		getenv = os.Getenv
	}
	outDir = strings.TrimSpace(outDir)
	if outDir == "" {
		return SelftestConfig{}, errors.New("自检：产物目录未指定（请设置 UVMP_SELFTEST_DIR）")
	}
	rIni := cfgMap(cfgMap(cfg, "_ini"), "render") // config.ini [render]
	rJSON := cfgMap(cfg, "render")                // config.json render
	chosen := cfgStr(rJSON, "chrome")
	if chosen == "" {
		chosen = cfgStr(rIni, "chrome")
	}
	out := SelftestConfig{OutDir: outDir}
	if chosen != "" {
		if _, err := os.Stat(chosen); err != nil {
			return SelftestConfig{}, fmt.Errorf("自检：指定的浏览器不存在: %s（请到设置页重新选择）", chosen)
		}
		out.BrowserPath = chosen
		return out, nil
	}
	if !cfgBool(rJSON, "builtin", cfgBool(rIni, "builtin", true)) {
		return SelftestConfig{}, errors.New("自检：未配置可用浏览器：请到「设置」页选择公司指定浏览器（render.chrome），" +
			"或启用内置浏览器（render.builtin + UVMP_BROWSER_BIN）后重试")
	}
	p := strings.TrimSpace(getenv("UVMP_BROWSER_BIN"))
	if p == "" {
		return SelftestConfig{}, errors.New("自检：未找到内置浏览器：render.builtin=true 时必须由 UVMP_BROWSER_BIN 指定内置浏览器，" +
			"不自动探测宿主浏览器；请安装完整版客户端或到「设置」页选择公司指定浏览器后重试")
	}
	if _, err := os.Stat(p); err != nil {
		return SelftestConfig{}, fmt.Errorf("自检：UVMP_BROWSER_BIN 指定的内置浏览器不存在: %s", p)
	}
	out.BrowserPath = p
	if w := strings.TrimSpace(getenv("UVMP_BROWSER_WORKER")); w != "" {
		if a := strings.TrimSpace(getenv("UVMP_BROWSER_APP")); a != "" {
			out.BrowserArgs = []string{a, w}
		} else {
			out.BrowserArgs = []string{w}
		}
		out.NoHeadless = true
	}
	return out, nil
}

// Selftest 跑内置 CDP 浏览器自检：只打开 data: 本地页（零网络），依次验证
// 主 target 导航与 eval、新 tab（Electron 走 cdp-worker 桥）导航与 eval、
// 裁剪截图 PNG 落盘、printToPDF 落盘；产物写 cfg.OutDir
// （cdp-selftest.png / cdp-selftest.pdf），浏览器 profile 与日志在
// OutDir/cdp-selftest-work（失败保留诊断）。任一步失败返回明确 error。
func Selftest(ctx context.Context, cfg SelftestConfig) (*SelftestResult, error) {
	if strings.TrimSpace(cfg.BrowserPath) == "" {
		return nil, errors.New("自检：未配置浏览器（需 UVMP_BROWSER_BIN 内置浏览器或显式 render.chrome）")
	}
	if strings.TrimSpace(cfg.OutDir) == "" {
		return nil, errors.New("自检：产物目录未指定（请设置 UVMP_SELFTEST_DIR）")
	}
	if err := os.MkdirAll(cfg.OutDir, 0o755); err != nil {
		return nil, fmt.Errorf("自检：创建产物目录失败: %w", err)
	}
	workDir := filepath.Join(cfg.OutDir, selftestWorkDirName)

	opts := DefaultOptions(cfg.BrowserPath, workDir)
	opts.BrowserArgs = cfg.BrowserArgs
	opts.NoHeadless = cfg.NoHeadless
	opts.Log = cfg.Log
	br := newCdpBrowser(opts, &redactor{})
	defer br.close()

	logf := func(format string, args ...any) {
		if cfg.Log != nil {
			cfg.Log(fmt.Sprintf(format, args...))
		}
	}
	res := &SelftestResult{Browser: cfg.BrowserPath, WorkDir: workDir}
	const total = 5
	passed := 0
	stepDone := func(name string) {
		passed++
		res.Checks = append(res.Checks, name)
		logf("  [自检] %s：OK", name)
		if cfg.Progress != nil {
			cfg.Progress(passed, total, name)
		}
	}
	fail := func(name string, err error) (*SelftestResult, error) {
		br.dumpDiag(ctx, workDir, "selftest")
		return nil, fmt.Errorf("自检：%s失败: %w（诊断见 %s）", name, err, workDir)
	}

	logf("内置 CDP 浏览器自检：启动（%s）", cfg.BrowserPath)
	if err := br.start(ctx); err != nil {
		return fail("启动浏览器", err)
	}
	stepDone("启动浏览器")

	// 1) 主 target（cdp-worker.cjs 隐藏窗口）导航到本地 data 页并 eval
	mainPage := localPageURL("main-page", "hello")
	if err := br.navigate(ctx, mainPage); err != nil {
		return fail("导航", err)
	}
	if !br.waitExpr(ctx, "document.title === 'main-page'", evalTimeout, 300*time.Millisecond) {
		return fail("导航", fmt.Errorf("等待主 target 导航就绪超时"))
	}
	title, err := br.evalString(ctx, "document.title", evalTimeout)
	if err != nil || title != "main-page" {
		return fail("eval", fmt.Errorf("eval document.title = %q, %v；want main-page", title, err))
	}
	v, err := br.eval(ctx, "1+2", evalTimeout)
	if err != nil || fmt.Sprint(v) != "3" {
		return fail("eval", fmt.Errorf("eval 1+2 = %v, %v；want 3", v, err))
	}
	stepDone("导航与 eval")

	// 2) 新 tab：独立 target（Electron 走 cdp-worker 桥）导航 + eval
	tabCtx, tabCancel, err := br.newTab(ctx)
	if err != nil {
		return fail("新 tab", err)
	}
	defer tabCancel()
	if v, err := evalOn(ctx, tabCtx, "1+1", evalTimeout); err != nil || fmt.Sprint(v) != "2" {
		return fail("新 tab", fmt.Errorf("新 tab eval = %v, %v", v, err))
	}
	if err := tabNavigate(ctx, tabCtx, localPageURL("tab-page", "pdf body")); err != nil {
		return fail("新 tab", err)
	}
	if !br.waitExprOn(ctx, tabCtx, "document.title === 'tab-page'", evalTimeout, 300*time.Millisecond) {
		return fail("新 tab", fmt.Errorf("等待新 tab 导航就绪超时"))
	}
	tabTitle, err := evalOn(ctx, tabCtx, "document.title", evalTimeout)
	if err != nil || fmt.Sprint(tabTitle) != "tab-page" {
		return fail("新 tab", fmt.Errorf("新 tab eval document.title = %v, %v；want tab-page", tabTitle, err))
	}
	stepDone("新 tab")

	// 3) 截图（裁剪内容区落盘 PNG，校验 PNG 魔数）
	pngPath := filepath.Join(cfg.OutDir, selftestPNGName)
	if err := br.screenshotClip(ctx, clipRect{X: 0, Y: 0, Width: 800, Height: 600}, pngPath); err != nil {
		return fail("截图 PNG", err)
	}
	pngData, err := os.ReadFile(pngPath)
	if err != nil {
		return fail("截图 PNG", fmt.Errorf("读截图失败: %w", err))
	}
	if !strings.HasPrefix(string(pngData), "\x89PNG") {
		return fail("截图 PNG", fmt.Errorf("截图不是 PNG（%d 字节）", len(pngData)))
	}
	res.PNG = pngPath
	res.PNGBytes = int64(len(pngData))
	stepDone("截图 PNG")

	// 4) printToPDF（Electron 走 cdp-worker 桥 webContents.printToPDF，校验 PDF 魔数）
	pdfData, err := br.tabPrintPDF(ctx, tabCtx)
	if err != nil {
		return fail("printToPDF", err)
	}
	if !strings.HasPrefix(string(pdfData), "%PDF") {
		return fail("printToPDF", fmt.Errorf("printToPDF 产物不是 PDF（%d 字节）", len(pdfData)))
	}
	pdfPath := filepath.Join(cfg.OutDir, selftestPDFName)
	if err := os.WriteFile(pdfPath, pdfData, 0o644); err != nil {
		return fail("printToPDF", fmt.Errorf("写 PDF 失败: %w", err))
	}
	res.PDF = pdfPath
	res.PDFBytes = int64(len(pdfData))
	stepDone("printToPDF")

	res.OK = true
	return res, nil
}

// localPageURL 生成纯本地 data:text/html 测试页（base64 编码，避免转义问题）。
func localPageURL(title, body string) string {
	html := "<html><head><title>" + title + "</title></head><body>" + body + "</body></html>"
	return "data:text/html;base64," + base64.StdEncoding.EncodeToString([]byte(html))
}

// tabNavigate 在指定标签页上下文导航（与 (*cdpBrowser).navigate 同口径）。
func tabNavigate(jobCtx, tabCtx context.Context, url string) error {
	return runOn(jobCtx, tabCtx, evalTimeout, chromedp.Func(func(cctx context.Context, t *chromedp.Target) error {
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

// ---------------------------------------------------------------- config 取值（宽松，同 exporter）

func cfgMap(m map[string]any, key string) map[string]any {
	if m == nil {
		return nil
	}
	v, _ := m[key].(map[string]any)
	return v
}

func cfgStr(m map[string]any, key string) string {
	if m == nil {
		return ""
	}
	s, _ := m[key].(string)
	return strings.TrimSpace(s)
}

// cfgBool 宽松布尔：bool 直取；字符串/数值解析；缺省取 def（同 exporter.boolField）。
func cfgBool(m map[string]any, key string, def bool) bool {
	if m == nil {
		return def
	}
	v, ok := m[key]
	if !ok || v == nil {
		return def
	}
	switch t := v.(type) {
	case bool:
		return t
	case string:
		switch strings.TrimSpace(strings.ToLower(t)) {
		case "true", "1", "yes", "on":
			return true
		case "false", "0", "no", "off", "":
			return false
		}
		return def
	case float64:
		return t != 0
	default:
		return def
	}
}
