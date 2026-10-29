// 环境自检应用任务（设置页「环境自检」，移植自 toolkit/selftest.py）：
// PDF 渲染自检 / SSO 登录测试 / CDP 渲染自检。以普通任务形态运行
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
	"path/filepath"

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

// runRenderSelftest 双通道自检：①显式 Electron 回退（仅配置时）②宿主浏览器（官方
// 直出与 printToPDF 的真实链路）。两条分开验，互不覆盖产物。
func runRenderSelftest(j *job.Job, _ map[string]any) (map[string]any, error) {
	log := func(s string) { j.Log(s) }
	lanes := []any{}
	cfg := config.Load()

	// 通道①：历史 Electron 回退（默认不启用）——UVMP_ELECTRON 或
	// browser_automation.electron_path 显式指定时才跑；普通宿主浏览器不需要它。
	eb := resolveElectronLane(cfg)
	if eb != "" {
		ecfg := electronLaneConfig(eb, cfg)
		ok, err := runLaneSelftest(j, ecfg)
		if ok {
			log(fmt.Sprintf("① Electron 回退：OK（%s）", eb))
			lanes = append(lanes, map[string]any{"lane": "electron", "ok": true, "path": eb})
		} else {
			log(fmt.Sprintf("① Electron 回退：失败 %s", err))
			lanes = append(lanes, map[string]any{"lane": "electron", "ok": false, "path": eb, "error": err.Error()})
		}
	} else {
		lanes = append(lanes, map[string]any{
			"lane": "electron", "ok": false, "path": "",
			"error": "未配置 Electron 回退（默认使用宿主浏览器，可忽略本通道）",
		})
		log("① Electron 回退：未配置，跳过（默认使用宿主浏览器）")
	}

	// 通道②：宿主浏览器（官方直出用；显式 render.chrome 优先，否则自动检测 360ent 等）
	rcfg := config.RenderCfg(cfg)
	chosen := config.StrFieldTrim(rcfg, "chrome")
	src := "用户指定"
	if chosen == "" {
		src = "自动检测"
	}
	host, err := browser.DetectHost(chosen)
	if err != nil {
		log(fmt.Sprintf("② 浏览器：未找到（%s）", err))
		lanes = append(lanes, map[string]any{"lane": "chrome", "ok": false, "path": "", "error": err.Error()})
		return finishRenderSelftest(lanes)
	}
	log(fmt.Sprintf("② 浏览器（%s，%s）：%s", src, host.Source, host.Path))
	chromeOut := filepath.Join(state.LogsDir(), "render-selftest-chrome")
	ccfg := render.SelftestConfig{BrowserPath: host.Path, OutDir: chromeOut}
	ok, err := runLaneSelftest(j, ccfg)
	if ok {
		log(fmt.Sprintf("② 浏览器通道：OK（%s）", host.Path))
		lanes = append(lanes, map[string]any{"lane": "chrome", "ok": true, "path": host.Path})
	} else {
		log(fmt.Sprintf("② 浏览器通道：失败 %s", err))
		lanes = append(lanes, map[string]any{"lane": "chrome", "ok": false, "path": host.Path, "error": err.Error()})
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

// resolveElectronLane 历史 Electron 回退宿主：UVMP_ELECTRON（Electron 壳显式注入）优先，
// 其次配置 browser_automation.electron_path；都没有返回空（默认宿主浏览器，通道跳过）。
// 不再自动探测 browser-runtime / node_modules，也不下载。
func resolveElectronLane(cfg config.Dict) string {
	if eb := os.Getenv("UVMP_ELECTRON"); fileExists(eb) {
		return eb
	}
	ba := config.MapField(cfg, "browser_automation")
	if p := config.StrFieldTrim(ba, "electron_path"); fileExists(p) {
		return p
	}
	return ""
}

// electronLaneConfig Electron 回退通道（UVMP_BROWSER_WORKER/APP 组位置参数，口径同
// render.ParseSelftestConfig 的 Electron 分支）；环境未注入时按配置的 runtime_dir
// 组 [APP, --uvmp-browser-mode=export]。
func electronLaneConfig(eb string, cfg config.Dict) render.SelftestConfig {
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
		return c
	}
	ba := config.MapField(cfg, "browser_automation")
	if rt := config.StrFieldTrim(ba, "runtime_dir"); rt != "" && fileExists(filepath.Join(rt, "package.json")) {
		c.BrowserArgs = []string{rt, browser.ExportWorkerFlag}
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

// runCDPSelftest CDP 渲染自检：render.Selftest 走真实浏览器 CDP 链路（导航 / eval /
// 新 tab / 截图 PNG / printToPDF），只打开 data: 本地页、零网络访问；真实产物写日志目录
// （cdp-selftest.png / cdp-selftest.pdf）。无浏览器或链路失败明确报错。
func runCDPSelftest(j *job.Job, _ map[string]any) (map[string]any, error) {
	cfg := config.Load()
	out := state.LogsDir()
	j.Log(fmt.Sprintf("CDP 渲染自检：产物目录 %s", out))
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

// DetectBrowser 返回可用的宿主浏览器路径；找不到报错。preferred 非空 = 用户在设置页/
// 配置里指定的公司浏览器，优先且必须存在。探测口径统一在 internal/browser（360ent
// 注册表 App Paths / ProgramData / PATH / 常见目录，Linux browser360ent-cn-stable 优先）。
func DetectBrowser(preferred string) (string, error) {
	h, err := browser.DetectHost(preferred)
	if err != nil {
		return "", err
	}
	return h.Path, nil
}

// fileExists 常规文件存在性。
func fileExists(p string) bool {
	if p == "" {
		return false
	}
	st, err := os.Stat(p)
	return err == nil && st.Mode().IsRegular()
}
