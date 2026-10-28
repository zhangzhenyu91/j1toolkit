package exporter

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// RunConfig 一次批次导出的运行配置（config.load 字典 + pcd_export 参数的合并口径）。
type RunConfig struct {
	Load map[string]any // 原始 config.load 字典（auth.ParseConfig 用）
	Mock bool           // dev.mock=true：离线模拟数据，无浏览器写 stub PDF

	ExportPCD     bool // params.export_pcd（默认 true）
	ExportTrack   bool // params.export_track（默认 true）
	CalibrateNoon bool // params.calibrate_noon

	OutDir   string // 批次输出目录（含批次子目录）
	FilesDir string // OutDir/逐单

	LookbackDays  int // order_export.lookback_days，默认 62
	QueryWorkers  int // 查询/预取并发，默认 4，钳 1..8
	RenderWorkers int // 渲染并发，默认 2，钳 1..4

	BrowserPath string // 浏览器可执行文件（显式 render.chrome 或 UVMP_BROWSER_BIN）

	// 仅内置 Electron 时设置（UVMP_BROWSER_WORKER 指向 cdp-worker.cjs）：
	// BrowserArgs 传 worker 脚本路径、NoHeadless 不传 --headless=new；
	// 显式 render.chrome（普通 Chromium）保持零值。
	BrowserArgs []string
	NoHeadless  bool

	// 渲染等待（config.ini [export] 毫秒字符串映射，同 toolkit/uvmp.py start_renderer）：
	WaitTiles time.Duration // map_tiles_wait，默认 12s（下限 3s）
	IdleQuiet time.Duration // map_idle_quiet，默认 5s（下限 2s）
	MinWait   time.Duration // map_min_wait，默认 30s 地图固定等待下限
	PCDWait   time.Duration // pcd_render_wait，默认 6s 派车单打印区固定等待

	IgnoreCertificateErrors bool     // render.ignore_certificate_errors
	SPKIPins                []string // render.certificate_spki_pins

	StateDir string // 浏览器 worker WorkDir 根（StateDir/browser-workers/<批次>/worker-N）

	// BatchID 本批次 worker 目录唯一后缀（时间戳_进程号）：避免多进程共用
	// Chrome profile 的 Singleton 锁冲突；成功收尾整目录清理，失败保留诊断。
	BatchID string
}

// WorkerRootDir 本批次全部渲染 worker 的父目录（成功后整目录清理）。
func (c *RunConfig) WorkerRootDir() string {
	return filepath.Join(c.StateDir, "browser-workers", c.BatchID)
}

// WorkerDir 第 idx 个渲染 worker 的独立 WorkDir（状态目录 browser-workers/<批次>/worker-N）。
func (c *RunConfig) WorkerDir(idx int) string {
	return filepath.Join(c.WorkerRootDir(), fmt.Sprintf("worker-%d", idx))
}

// ParseRunConfig 解析并校验请求。校验顺序与 Python backend.run 一致：
// 先清单（空清单报错，config 为空也一样——CI 冒烟依赖此 error 事件），
// 再勾选，最后浏览器（仅真实路径）。now 为批次目录时间戳来源。
func ParseRunConfig(req Request, getenv func(string) string, now time.Time) (*RunConfig, error) {
	if getenv == nil {
		getenv = os.Getenv
	}
	if len(req.Codes) == 0 {
		return nil, errors.New("未解析到任何派车单号")
	}
	cfg := &RunConfig{Load: req.Config}
	params := req.Params
	if params == nil {
		params = map[string]any{}
	}

	cfg.ExportPCD = boolField(params, "export_pcd", true)
	cfg.ExportTrack = boolField(params, "export_track", true)
	if !(cfg.ExportPCD || cfg.ExportTrack) {
		return nil, errors.New("派车单 / 轨迹至少勾选一项")
	}
	cfg.CalibrateNoon = boolField(params, "calibrate_noon", false)
	cfg.Mock = boolField(mapField(cfg.Load, "dev"), "mock", false)

	oe := mapField(cfg.Load, "order_export")
	cfg.LookbackDays = intField(oe, "lookback_days", 62)
	if cfg.LookbackDays < 1 {
		cfg.LookbackDays = 1
	}
	cfg.QueryWorkers = clampInt(intField(oe, "query_workers", 4), 1, 8)
	cfg.RenderWorkers = clampInt(intField(oe, "render_workers", 2), 1, 4)

	// ---- 输出目录（backend._run_python 同口径）----
	outdir := strField(params, "outdir")
	if outdir == "" {
		outdir = strField(oe, "default_outdir")
	}
	if outdir == "" {
		outdir = "./output"
	}
	outdir = expandUser(outdir)
	if boolField(params, "batch_subdir", boolField(oe, "batch_subdir", true)) {
		base := strField(params, "batch_name")
		if base == "" {
			base = "派车单"
		}
		outdir = filepath.Join(outdir, safeName(base)+"_导出_"+now.Format("20060102_150405"))
	}
	cfg.OutDir = outdir
	cfg.FilesDir = filepath.Join(outdir, "逐单")
	cfg.StateDir = stateDir()
	cfg.BatchID = fmt.Sprintf("%s_%d", now.Format("20060102_150405"), os.Getpid())

	// ---- 渲染等待映射（_ini.export，毫秒；同 uvmp.py start_renderer 口径）----
	ini := mapField(cfg.Load, "_ini")
	exp := mapField(ini, "export")
	cfg.WaitTiles = maxDur(3*time.Second, msField(exp, "map_tiles_wait", 12000))
	cfg.IdleQuiet = maxDur(2*time.Second, msField(exp, "map_idle_quiet", 5000))
	cfg.MinWait = msField(exp, "map_min_wait", 30000)   // 30s 地图固定等待
	cfg.PCDWait = msField(exp, "pcd_render_wait", 6000) // 6s 派车单固定等待

	// ---- render 配置：config.ini [render] 为底，config.json render 覆盖 ----
	rc := mapField(ini, "render")
	rcCopy := map[string]any{}
	for k, v := range rc {
		rcCopy[k] = v
	}
	rjson := mapField(cfg.Load, "render")
	if c := strField(rjson, "chrome"); c != "" {
		rcCopy["chrome"] = c
	}
	if _, ok := rjson["ignore_certificate_errors"]; ok {
		rcCopy["ignore_certificate_errors"] = rjson["ignore_certificate_errors"]
	}
	if pins, ok := rjson["certificate_spki_pins"].([]any); ok {
		rcCopy["certificate_spki_pins"] = rjson["certificate_spki_pins"]
		_ = pins
	}
	cfg.IgnoreCertificateErrors = boolField(rcCopy, "ignore_certificate_errors", false)
	if pins, ok := rcCopy["certificate_spki_pins"].([]any); ok {
		for _, p := range pins {
			if s := strAny(p); s != "" {
				cfg.SPKIPins = append(cfg.SPKIPins, s)
			}
		}
	}

	// ---- 浏览器选择：显式 render.chrome 优先作兼容回退；否则 builtin=true 必须走
	// UVMP_BROWSER_BIN，不自动探测宿主；缺浏览器明确失败。mock 无浏览器也全流程。----
	if !cfg.Mock {
		p, err := resolveBrowser(rcCopy, rjson, getenv)
		if err != nil {
			return nil, err
		}
		cfg.BrowserPath = p
	}
	return cfg, nil
}

// resolveBrowser 浏览器选择（不做宿主自动探测）：
//  1. 显式 render.chrome（config.json 用户手选，兼容回退）：必须存在；
//  2. 否则 render.builtin=true（默认）：必须用 UVMP_BROWSER_BIN 指向的内置浏览器；
//  3. 其余情况明确失败。
func resolveBrowser(rc map[string]any, rjson map[string]any, getenv func(string) string) (string, error) {
	chosen := strField(rjson, "chrome")
	if chosen == "" {
		chosen = strField(rc, "chrome") // config.ini [render] chrome 同样算显式
	}
	if chosen != "" {
		if _, err := os.Stat(chosen); err != nil {
			return "", fmt.Errorf("指定的浏览器不存在: %s（请到设置页重新选择）", chosen)
		}
		return chosen, nil
	}
	builtin := boolField(rjson, "builtin", boolField(rc, "builtin", true))
	if builtin {
		p := strField(map[string]any{"v": getenv("UVMP_BROWSER_BIN")}, "v")
		if p == "" {
			return "", errors.New("未找到内置浏览器：render.builtin=true 时必须由 UVMP_BROWSER_BIN 指定内置浏览器，" +
				"不自动探测宿主浏览器；请安装完整版客户端或到「设置」页选择公司指定浏览器后重试")
		}
		if _, err := os.Stat(p); err != nil {
			return "", fmt.Errorf("UVMP_BROWSER_BIN 指定的内置浏览器不存在: %s", p)
		}
		return p, nil
	}
	return "", errors.New("未配置可用浏览器：请到「设置」页选择公司指定浏览器（render.chrome），" +
		"或启用内置浏览器（render.builtin + UVMP_BROWSER_BIN）后重试")
}

func maxDur(a, b time.Duration) time.Duration {
	if a > b {
		return a
	}
	return b
}
