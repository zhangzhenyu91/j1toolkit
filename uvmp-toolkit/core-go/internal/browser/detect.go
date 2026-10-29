package browser

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// 浏览器运行时统一自发现（开发树与 fyne 安装布局同一口径），供导出/自检环境变量
// 注入（env.go EnsureExportEnv）与课程 resolveBrowserHost 共用，不再各写一套
// 不兼容的路径推导：
//
//   - fyne 安装布局（packaging/fyne fetch_browser_runtime 装配产物）：
//     <根>/browser-runtime/electron(.exe) + <根>/browser-runtime/app/
//     （app/{package.json,main.cjs} 双模式运行时）；
//   - 开发树：uvmp-toolkit/browser-runtime/（源码运行时应用目录，package.json +
//     main.cjs 直落本目录）+ desktop/node_modules/electron/dist/electron(.exe)。

// Runtime 统一探测到的浏览器运行时。
type Runtime struct {
	Electron string // 宿主 Electron 可执行（electron(.exe)）
	AppDir   string // browser-runtime 运行时应用目录（内含 package.json + main.cjs）
}

// electronNames 宿主可执行候选名（fyne 装配 Windows 为 electron.exe）。
var electronNames = []string{"electron.exe", "electron"}

// runtimeAppFiles 运行时应用目录必备文件（Electron 按 package.json main 加载 main.cjs）。
var runtimeAppFiles = []string{"package.json", "main.cjs"}

// hasRuntimeApp 目录是否为 browser-runtime 运行时应用目录。
func hasRuntimeApp(dir string) bool {
	for _, name := range runtimeAppFiles {
		if !fileExists(filepath.Join(dir, name)) {
			return false
		}
	}
	return true
}

// DetectRuntime 按默认搜索根统一探测完整浏览器运行时（应用目录与 Electron 宿主
// 都须找到），找不到返回明确错误。
func DetectRuntime() (Runtime, error) {
	return DetectRuntimeIn(scanRoots())
}

// DetectRuntimeIn 在给定搜索根内探测完整浏览器运行时（测试可模拟安装布局）。
func DetectRuntimeIn(roots []string) (Runtime, error) {
	rt := ScanRuntimeIn(roots)
	if rt.AppDir == "" {
		return Runtime{}, errors.New("browser: 未找到 browser-runtime 运行时应用目录" +
			"（开发树 uvmp-toolkit/browser-runtime 或安装布局 browser-runtime/app）")
	}
	if rt.Electron == "" {
		return Runtime{}, fmt.Errorf("browser: 未找到 Electron 宿主（%s 旁或 desktop/node_modules/electron/dist）",
			rt.AppDir)
	}
	return rt, nil
}

// ScanRuntime 按默认搜索根统一探测（应用目录/Electron 各自独立、找不到留空），
// 供课程 resolveBrowserHost 分别报「缺 runtime / 缺 Electron」的既有口径。
func ScanRuntime() Runtime {
	return ScanRuntimeIn(scanRoots())
}

// ScanRuntimeIn 在给定搜索根内统一探测（测试可模拟安装布局）。每个根（及其
// uvmp-toolkit 开发子目录）内按序命中：
//  1. fyne 安装布局：browser-runtime/app/（应用目录）+ browser-runtime/electron(.exe)；
//  2. 开发树/源码：browser-runtime/（应用目录即本目录）+ browser-runtime/electron(.exe)
//     或 desktop/node_modules/electron/dist、node_modules/electron/dist。
func ScanRuntimeIn(roots []string) Runtime {
	var rt Runtime
	for _, base := range scanBases(roots) {
		if rt.AppDir == "" {
			rt.AppDir = findRuntimeApp(base)
		}
		if rt.Electron == "" {
			rt.Electron = findElectron(base)
		}
		if rt.AppDir != "" && rt.Electron != "" {
			return rt
		}
	}
	return rt
}

// scanBases 展开搜索根：根自身 + 根/uvmp-toolkit 开发子目录（从仓库根启动开发时命中）。
func scanBases(roots []string) []string {
	out := []string{}
	for _, r := range roots {
		if r == "" {
			continue
		}
		out = append(out, r, filepath.Join(r, "uvmp-toolkit"))
	}
	return out
}

// findRuntimeApp <根>/browser-runtime/app（fyne fetch 装配：app/{package.json,main.cjs}）
// 优先，其次 <根>/browser-runtime（源码，package.json + main.cjs 直落本目录）；
// 兼容历史装配（package.json 在 browser-runtime/、入口在 app/）仍取 browser-runtime/。
func findRuntimeApp(base string) string {
	rtdir := filepath.Join(base, "browser-runtime")
	if hasRuntimeApp(filepath.Join(rtdir, "app")) {
		return filepath.Join(rtdir, "app")
	}
	if hasRuntimeApp(rtdir) {
		return rtdir
	}
	if fileExists(filepath.Join(rtdir, "package.json")) && fileExists(filepath.Join(rtdir, "app", "main.cjs")) {
		return rtdir
	}
	return ""
}

// findElectron 宿主可执行：<根>/browser-runtime/electron(.exe)（fyne 装配直落）与
// 开发树 node_modules Electron dist 位置。
func findElectron(base string) string {
	rtdir := filepath.Join(base, "browser-runtime")
	for _, name := range electronNames {
		for _, c := range []string{
			filepath.Join(rtdir, name),
			filepath.Join(base, "desktop", "node_modules", "electron", "dist", name),
			filepath.Join(base, "node_modules", "electron", "dist", name),
		} {
			if fileExists(c) {
				return c
			}
		}
	}
	return ""
}

// scanRoots 可执行文件/工作目录向上数级的探测根（开发树与安装树同一口径：
// exe 目录与工作目录各向上 5 级，含中间各级）。
func scanRoots() []string {
	roots := []string{}
	if exe, err := os.Executable(); err == nil {
		if resolved, err := filepath.EvalSymlinks(exe); err == nil {
			exe = resolved
		}
		roots = append(roots, filepath.Dir(exe))
	}
	if wd, err := os.Getwd(); err == nil {
		roots = append(roots, wd)
	}
	out := []string{}
	for _, r := range roots {
		dir := r
		for i := 0; i < 5; i++ {
			out = append(out, dir)
			parent := filepath.Dir(dir)
			if parent == dir {
				break
			}
			dir = parent
		}
	}
	return out
}

// fileExists 常规文件存在性（目录不算）。
func fileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.Mode().IsRegular()
}
