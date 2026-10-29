// 环境自检应用任务（设置页「环境自检」，移植自 toolkit/selftest.py）：
// PDF 渲染自检 / SSO 登录测试 / 内置 CDP 渲染自检。以普通任务形态运行
// （走 jobs 模型，日志/进度/结果与普通导出一致）。
//
// 与 rpc/selftest.go 同口径：桌面壳（ui.Backend）与 stdio RPC 共用本入口，
// 不复制两份自检逻辑。
package apps

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"

	"github.com/zhangzhenyu91/j1toolkit/uvmp-toolkit/core-go/internal/browser"
	"github.com/zhangzhenyu91/j1toolkit/uvmp-toolkit/core-go/internal/config"
	"github.com/zhangzhenyu91/j1toolkit/uvmp-toolkit/core-go/internal/job"
	"github.com/zhangzhenyu91/j1toolkit/uvmp-toolkit/core-go/internal/render"
	"github.com/zhangzhenyu91/j1toolkit/uvmp-toolkit/core-go/internal/state"
)

// RunSelftest 按 kind 执行环境自检任务（render / sso / cdp）。
func RunSelftest(kind string, j *job.Job, params map[string]any) (map[string]any, error) {
	switch kind {
	case "render":
		return runRenderSelftest(j, params)
	case "sso":
		return runSSOSelftest(j, params)
	case "cdp":
		return runCDPSelftest(j, params)
	default:
		return nil, fmt.Errorf("未知自检类型: %s", kind)
	}
}

// runRenderSelftest 双通道自检：①自带 Electron 包版打印 ②用户所选/自动探测浏览器。
// 官方弹窗截图（CDP）走的是通道②的真实浏览器——两条分开验。
// 两通道均经 render.Selftest 实链（导航/eval/截图/printToPDF），产物写
// logs/render-selftest-<lane>/，互不覆盖。
func runRenderSelftest(j *job.Job, _ map[string]any) (map[string]any, error) {
	log := func(s string) { j.Log(s) }
	lanes := []any{}

	// 通道①：自带 Electron（包版打印用）——UVMP_ELECTRON 显式优先（冻结壳注入），
	// 否则统一探测的内置浏览器运行时 Electron（开发树/fyne 安装布局同一口径）。
	eb := resolveElectronLane()
	if eb != "" {
		cfg := electronLaneConfig(eb)
		ok, err := runLaneSelftest(j, cfg)
		if ok {
			log(fmt.Sprintf("① 自带 Electron：OK（%s）", eb))
			lanes = append(lanes, map[string]any{"lane": "electron", "ok": true, "path": eb})
		} else {
			log(fmt.Sprintf("① 自带 Electron：失败 %s", err))
			lanes = append(lanes, map[string]any{"lane": "electron", "ok": false, "path": eb, "error": err.Error()})
		}
	} else {
		lanes = append(lanes, map[string]any{
			"lane": "electron", "ok": false, "path": "",
			"error": "源码模式无自带 Electron（打包版才有）",
		})
		log("① 自带 Electron：源码模式无，跳过")
	}

	// 通道②：真实浏览器（官方直出用；用户所选优先）
	rcfg := config.RenderCfg(config.Load())
	chosen := config.StrFieldTrim(rcfg, "chrome")
	chrome := chosen
	src := "用户指定"
	if chrome == "" {
		src = "自动探测"
		c, err := DetectBrowser("")
		if err != nil {
			log(fmt.Sprintf("② 浏览器：未找到（%s）", err))
			lanes = append(lanes, map[string]any{"lane": "chrome", "ok": false, "path": "", "error": err.Error()})
			return finishRenderSelftest(lanes)
		}
		chrome = c
	}
	log(fmt.Sprintf("② 浏览器（%s）：%s", src, chrome))
	chromeOut := filepath.Join(state.LogsDir(), "render-selftest-chrome")
	ccfg := render.SelftestConfig{BrowserPath: chrome, OutDir: chromeOut}
	ok, err := runLaneSelftest(j, ccfg)
	if ok {
		log(fmt.Sprintf("② 浏览器通道：OK（%s）", chrome))
		lanes = append(lanes, map[string]any{"lane": "chrome", "ok": true, "path": chrome})
	} else {
		log(fmt.Sprintf("② 浏览器通道：失败 %s", err))
		lanes = append(lanes, map[string]any{"lane": "chrome", "ok": false, "path": chrome, "error": err.Error()})
	}
	return finishRenderSelftest(lanes)
}

func finishRenderSelftest(lanes []any) (map[string]any, error) {
	okAll := true
	anyOK := false
	for _, l := range lanes {
		lm := l.(map[string]any)
		ok := lm["ok"] == true
		path, _ := lm["path"].(string)
		if ok {
			anyOK = true
		}
		// ok_all 只考核「有产物的通道」与浏览器通道（与 Python 一致）
		if (lm["lane"] == "chrome" || path != "") && !ok {
			okAll = false
		}
	}
	if !anyOK {
		return nil, errors.New("两条渲染通道均不可用，请检查设置页浏览器选择")
	}
	return map[string]any{"lanes": lanes, "ok_all": okAll}, nil
}

// resolveElectronLane 自带 Electron 通道宿主：UVMP_ELECTRON 显式优先（Electron 壳
// spawn 核心时注入），否则统一探测的内置浏览器运行时 Electron（开发树/fyne 安装布局，
// 与导出/自检 env 注入同一探测）。都没有返回空（调用方按「源码模式无自带 Electron」跳过）。
func resolveElectronLane() string {
	if eb := os.Getenv("UVMP_ELECTRON"); eb != "" && fileExists(eb) {
		return eb
	}
	if rt, err := browser.DetectRuntime(); err == nil {
		return rt.Electron
	}
	return ""
}

// electronLaneConfig 自带 Electron 通道（UVMP_BROWSER_WORKER/APP 组位置参数，
// 口径同 render.ParseSelftestConfig 的内置浏览器分支；环境未注入时按统一探测的
// 导出口径组 [APP, --uvmp-browser-mode=export]）。
func electronLaneConfig(eb string) render.SelftestConfig {
	c := render.SelftestConfig{
		BrowserPath: eb,
		OutDir:      filepath.Join(state.LogsDir(), "render-selftest-electron"),
	}
	if w := os.Getenv("UVMP_BROWSER_WORKER"); w != "" {
		if a := os.Getenv("UVMP_BROWSER_APP"); a != "" {
			c.BrowserArgs = []string{a, w}
		} else {
			c.BrowserArgs = []string{w}
		}
		c.NoHeadless = true
	} else if rt, err := browser.DetectRuntime(); err == nil && rt.AppDir != "" {
		c.BrowserArgs = []string{rt.AppDir, browser.ExportWorkerFlag}
		c.NoHeadless = true
	}
	return c
}

// runLaneSelftest 跑一条渲染通道的实链自检（导航/eval/新 tab/截图 PNG/printToPDF）。
func runLaneSelftest(j *job.Job, cfg render.SelftestConfig) (bool, error) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	j.AddCancelHook(cancel)
	cfg.Log = func(line string) { j.Log(line) }
	cfg.Progress = func(step, total int, label string) { j.SetProgress(step, total, label) }
	if err := os.MkdirAll(cfg.OutDir, 0o755); err != nil {
		return false, err
	}
	res, err := render.Selftest(ctx, cfg)
	if err != nil {
		if j.Cancelled() {
			return false, job.ErrCancelled
		}
		return false, err
	}
	j.Log(fmt.Sprintf("自检产物：%s（%.1f KB）/%s（%.1f KB）",
		res.PNG, float64(res.PNGBytes)/1024, res.PDF, float64(res.PDFBytes)/1024))
	return true, nil
}

// runCDPSelftest 内置 CDP 渲染自检：render.Selftest 走真实内置浏览器 CDP 链路
// （导航 / eval / 新 tab / 截图 PNG / printToPDF），只打开 data: 本地页、零网络访问；
// 真实产物写日志目录（cdp-selftest.png / cdp-selftest.pdf）。无浏览器或链路失败明确报错。
func runCDPSelftest(j *job.Job, _ map[string]any) (map[string]any, error) {
	cfg := config.Load()
	out := state.LogsDir()
	// 统一浏览器运行时自发现：ParseSelftestConfig 之前自动注入内置浏览器环境
	// （显式 render.chrome/显式 env 优先不覆盖），自检结束 restore 恢复。
	restore := browser.EnsureExportEnv(browser.ExplicitChrome(cfg))
	defer restore()
	j.Log(fmt.Sprintf("内置 CDP 渲染自检：产物目录 %s", out))
	stCfg, err := render.ParseSelftestConfig(cfg, os.Getenv, out)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	j.AddCancelHook(cancel)
	stCfg.Log = func(line string) { j.Log(line) }
	stCfg.Progress = func(step, total int, label string) { j.SetProgress(step, total, label) }
	res, err := render.Selftest(ctx, stCfg)
	if err != nil {
		if j.Cancelled() {
			return nil, job.ErrCancelled
		}
		return nil, err
	}
	outMap := map[string]any{
		"ok":        res.OK,
		"png":       res.PNG,
		"pdf":       res.PDF,
		"png_bytes": res.PNGBytes,
		"pdf_bytes": res.PDFBytes,
		"browser":   res.Browser,
		"work_dir":  res.WorkDir,
		"checks":    res.Checks,
	}
	return outMap, nil
}

// runSSOSelftest 只验证 SSO 登录与凭证换取，不导出（mock 模式下走模拟客户端）。
func runSSOSelftest(j *job.Job, _ map[string]any) (map[string]any, error) {
	cfg := config.Load()
	_, tokenLen, err := MakeClient(cfg, LogFunc(func(format string, args ...any) {
		j.Log(fmt.Sprintf(format, args...))
	}))
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"ok":        true,
		"token_len": tokenLen,
		"mock":      config.BoolField(config.MapField(cfg, "dev"), "mock", false),
	}, nil
}

// ---------------------------------------------------------------- 浏览器探测

// chromeNames Chrome/Chromium 及麒麟常见专用浏览器（按序探测；公司指定浏览器找不到时让用户在设置页手选）。
var chromeNames = []string{
	"google-chrome",
	"google-chrome-stable",
	"chromium",
	"chromium-browser",
	"qaxbrowser", // 奇安信可信浏览器（麒麟软件商店常见）
	"browser360",
	"browser360-cn",
	"360chrome", // 360 安全浏览器
	"microsoft-edge",
	"microsoft-edge-stable",
	"chrome",
}

// linuxBrowserPaths 不在 PATH 时的已知绝对路径（麒麟软件商店/厂商默认安装位置）。
var linuxBrowserPaths = []string{
	"/usr/bin/qaxbrowser",
	"/opt/qaxbrowser/qaxbrowser",
	"/opt/apps/qaxbrowser/qaxbrowser",
	"/usr/bin/browser360",
	"/opt/apps/com.360.browser-stable/files/browser360",
}

// windowsChromePaths Windows 已知安装位置。
var windowsChromePaths = []string{
	`C:\Program Files\Google\Chrome\Application\chrome.exe`,
	`C:\Program Files (x86)\Google\Chrome\Application\chrome.exe`,
	`C:\Program Files (x86)\Microsoft\Edge\Application\msedge.exe`,
	`C:\Program Files\Microsoft\Edge\Application\msedge.exe`,
}

// DetectBrowser 返回可用的浏览器路径；找不到报错。
// preferred 非空 = 用户在设置页/配置里指定的公司浏览器，优先且必须存在
// （移植自 platform_utils.detect_chrome）。
func DetectBrowser(preferred string) (string, error) {
	if preferred != "" {
		if fileExists(preferred) {
			return preferred, nil
		}
		return "", fmt.Errorf("指定的浏览器不存在: %s（请在设置页重新选择）", preferred)
	}
	for _, name := range chromeNames {
		if found, err := exec.LookPath(name); err == nil {
			return found, nil
		}
	}
	if runtime.GOOS == "windows" {
		for _, p := range windowsChromePaths {
			if fileExists(p) {
				return p, nil
			}
		}
		if local := os.Getenv("LOCALAPPDATA"); local != "" {
			p := filepath.Join(local, "Google", "Chrome", "Application", "chrome.exe")
			if fileExists(p) {
				return p, nil
			}
		}
	} else {
		for _, p := range linuxBrowserPaths {
			if fileExists(p) {
				return p, nil
			}
		}
	}
	return "", errors.New("未找到浏览器：请在设置页手动选择（公司指定浏览器）")
}

// fileExists 常规文件存在性。
func fileExists(p string) bool {
	if p == "" {
		return false
	}
	st, err := os.Stat(p)
	return err == nil && st.Mode().IsRegular()
}
