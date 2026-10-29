package browser

import (
	"strings"
)

// 浏览器环境变量（exporter.ParseRunConfig / render.ParseSelftestConfig 按现有口径读取）：
//   - UVMP_BROWSER_BIN：显式宿主机浏览器路径（CI/门禁/排障指定，优先于自动检测）；
//   - UVMP_BROWSER_WORKER / UVMP_BROWSER_APP：仅历史 Electron 回退（render.builtin=true）
//     使用，常规宿主浏览器不读。
const (
	EnvBrowserBin    = "UVMP_BROWSER_BIN"
	EnvBrowserWorker = "UVMP_BROWSER_WORKER"
	EnvBrowserApp    = "UVMP_BROWSER_APP"
)

// ExportWorkerFlag Electron 回退导出模式开关：browser-runtime/main.cjs 双模式开关的
// export 不可见渲染模式。作为 UVMP_BROWSER_WORKER 注入值（与 UVMP_BROWSER_APP 组
// [APP, worker] 位置参数）。
const ExportWorkerFlag = "--uvmp-browser-mode=export"

// ExplicitChrome 显式浏览器选择（config.json render.chrome，其次 config.ini [render]
// chrome）——与 exporter.ParseRunConfig / render.ParseSelftestConfig 的优先级同口径。
func ExplicitChrome(cfg map[string]any) string {
	if s := cfgStr(cfgMap(cfg, "render"), "chrome"); s != "" {
		return s
	}
	return cfgStr(cfgMap(cfgMap(cfg, "_ini"), "render"), "chrome")
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
