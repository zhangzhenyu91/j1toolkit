package browser

import (
	"os"
	"strings"
)

// 导出/自检浏览器环境变量（exporter.ParseRunConfig / render.ParseSelftestConfig
// 按现有口径读取；Python goexport.derive_browser_env 亦注入同名变量）。
const (
	EnvBrowserBin    = "UVMP_BROWSER_BIN"
	EnvBrowserWorker = "UVMP_BROWSER_WORKER"
	EnvBrowserApp    = "UVMP_BROWSER_APP"
)

// ExportWorkerFlag 导出/自检 worker 入口开关：browser-runtime/main.cjs 双模式开关的
// export 不可见渲染模式。作为 UVMP_BROWSER_WORKER 注入值（与 UVMP_BROWSER_APP 组
// [APP, worker] 位置参数），与 packaging/fyne gate_smoke、uvmp-export 渲染/自检口径一致。
const ExportWorkerFlag = "--uvmp-browser-mode=export"

// ExplicitChrome 显式浏览器选择（config.json render.chrome，其次 config.ini [render]
// chrome）——与 exporter.ParseRunConfig / render.ParseSelftestConfig 的优先级同口径。
func ExplicitChrome(cfg map[string]any) string {
	if s := cfgStr(cfgMap(cfg, "render"), "chrome"); s != "" {
		return s
	}
	return cfgStr(cfgMap(cfgMap(cfg, "_ini"), "render"), "chrome")
}

// EnsureExportEnv 为导出/自检自动注入内置浏览器环境变量（UVMP_BROWSER_BIN=Electron
// 宿主、UVMP_BROWSER_WORKER=--uvmp-browser-mode=export、UVMP_BROWSER_APP=运行时应用
// 目录），供 exporter.ParseRunConfig / render.ParseSelftestConfig 按现有口径读取——
// Go 进程内导出/自检与 uvmp-export 直跑不再依赖 Python goexport 推导（无 Python 也能
// 独立工作）。布局探测与课程 resolveBrowserHost 同一套（见 detect.go）。
//
// 优先级（显式优先且不覆盖）：
//   - 显式 render.chrome（设置页手选浏览器）：完全不注入，交显式浏览器；
//   - 显式 UVMP_BROWSER_BIN/WORKER/APP：已设值不覆盖；显式 BIN 不注入派生
//     WORKER/APP（与 goexport.derive_browser_env 同口径）；
//   - 其余按统一探测自动补齐；探测不到完整运行时（应用目录 + Electron）则不注入，
//     由下游按既有口径报「未找到内置浏览器」。
//
// 返回 restore 将本次改动的环境变量恢复原状：长期进程（GUI 核心）每任务用完即调，
// 避免污染同进程后续任务；返回值永不为 nil。
func EnsureExportEnv(renderChrome string) (restore func()) {
	return ensureExportEnv(scanRoots(), renderChrome, os.LookupEnv, os.Setenv, os.Unsetenv)
}

// envPrev 注入前的环境变量原值（set=false 表示原本未设）。
type envPrev struct {
	val string
	set bool
}

// ensureExportEnv EnsureExportEnv 的可注入核心（测试模拟安装布局与环境）。只写
// 「原本为空或未设」的变量，restore 按 envPrev 精确还原。
func ensureExportEnv(roots []string, renderChrome string,
	lookup func(string) (string, bool),
	setenv func(string, string) error,
	unsetenv func(string) error,
) func() {
	noop := func() {}
	if strings.TrimSpace(renderChrome) != "" {
		return noop // 显式 render.chrome 优先：不注入
	}
	prev := map[string]envPrev{}
	applied := []string{}
	// take 取当前值并记原值；返回裁剪后值与「是否可写」（原本为空或未设才可写）。
	take := func(k string) (val string, writable bool) {
		v, ok := lookup(k)
		prev[k] = envPrev{val: v, set: ok}
		v = strings.TrimSpace(v)
		return v, v == ""
	}
	set := func(k, v string) {
		if err := setenv(k, v); err == nil {
			applied = append(applied, k)
		}
	}

	_, binWritable := take(EnvBrowserBin)
	worker, workerWritable := take(EnvBrowserWorker)
	_, appWritable := take(EnvBrowserApp)

	var rt Runtime
	if binWritable || (appWritable && (worker == ExportWorkerFlag || workerWritable)) {
		rt = ScanRuntimeIn(roots)
	}
	if binWritable && rt.Electron != "" && rt.AppDir != "" {
		set(EnvBrowserBin, rt.Electron)
		if workerWritable {
			set(EnvBrowserWorker, ExportWorkerFlag)
			worker = ExportWorkerFlag
		}
	}
	if appWritable && worker == ExportWorkerFlag && rt.AppDir != "" {
		set(EnvBrowserApp, rt.AppDir)
	}

	if len(applied) == 0 {
		return noop
	}
	return func() {
		for _, k := range applied {
			p := prev[k]
			if p.set {
				_ = setenv(k, p.val)
			} else {
				_ = unsetenv(k)
			}
		}
	}
}

// cfgMap 取嵌套 dict（缺失或类型不符返回 nil）。
func cfgMap(m map[string]any, key string) map[string]any {
	if m == nil {
		return nil
	}
	sub, _ := m[key].(map[string]any)
	return sub
}

// cfgStr 取 dict 字符串值并裁剪空白。
func cfgStr(m map[string]any, key string) string {
	if m == nil {
		return ""
	}
	s, _ := m[key].(string)
	return strings.TrimSpace(s)
}
