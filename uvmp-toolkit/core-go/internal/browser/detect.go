package browser

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// 宿主浏览器统一探测（导出 / 课程 / 系统信息共用一套口径）：
//
//  1. 显式 render.chrome（设置页手选，含 config.ini [render] chrome）优先且必须存在；
//  2. 环境变量 UVMP_BROWSER_BIN（CI/门禁/排障显式指定）次之且必须存在；
//  3. 公司指定浏览器自动检测：
//     - Windows：360 企业浏览器 360ent 注册表 App Paths（HKLM/HKCU）→
//     %ProgramData%\360ent\Application\360ent.exe（环境变量缺省 C:\ProgramData）→
//     PATH / 常见目录；
//     - Linux：browser360ent-cn-stable（PATH 与 /usr/bin）优先；
//  4. 兼容浏览器兜底：奇安信可信浏览器 qaxbrowser → Google Chrome → Microsoft Edge。
//
// 旧 browser-runtime（Electron）不再自动探测，只有调用方显式给出 RuntimeDir 时
// 才按 Electron 回退启动（见 Config.RuntimeDir），也不在目标机下载任何浏览器。

// Host 宿主浏览器探测结果。
type Host struct {
	Path   string `json:"path"`   // 浏览器可执行文件
	Name   string `json:"name"`   // 显示名（360 企业浏览器 360ent / Google Chrome …）
	Source string `json:"source"` // 命中来源（显式配置 / 环境变量 / 注册表 App Paths / PATH / 常见目录）
}

// Probe 宿主浏览器探测依赖（默认真实实现；测试注入候选/查询，不触碰真实注册表）。
type Probe struct {
	GOOS     string                            // 目标平台（缺省 runtime.GOOS）
	LookPath func(name string) (string, error) // PATH 查找（缺省 exec.LookPath）
	Exists   func(path string) bool            // 常规文件存在性（缺省 fileExists）
	AppPath  func(exe string) []string         // 注册表 App Paths 候选（Windows 专属实现）
	Getenv   func(key string) string           // 环境变量读取
}

// defaultProbe 真实探测依赖（注册表读取见 host_windows.go / host_other.go）。
func defaultProbe() Probe {
	return Probe{
		GOOS:     runtime.GOOS,
		LookPath: exec.LookPath,
		Exists:   fileExists,
		AppPath:  appPathCandidates,
		Getenv:   os.Getenv,
	}
}

// withDefaults 补零值默认，便于测试只注入关心的字段。
func (p Probe) withDefaults() Probe {
	d := defaultProbe()
	if p.GOOS == "" {
		p.GOOS = d.GOOS
	}
	if p.LookPath == nil {
		p.LookPath = d.LookPath
	}
	if p.Exists == nil {
		p.Exists = d.Exists
	}
	if p.AppPath == nil {
		p.AppPath = d.AppPath
	}
	if p.Getenv == nil {
		p.Getenv = d.Getenv
	}
	return p
}

// DetectHost 统一宿主浏览器探测（显式 render.chrome 优先，其次 UVMP_BROWSER_BIN，
// 再按平台自动检测 360ent / qaxbrowser / Chrome / Edge）。找不到返回可操作错误。
func DetectHost(preferred string) (Host, error) {
	return DetectHostIn(defaultProbe(), preferred)
}

// DetectHostWith 同 DetectHost，但环境变量读取可注入（exporter/render 的 getenv 口径）。
func DetectHostWith(preferred string, getenv func(string) string) (Host, error) {
	p := defaultProbe()
	if getenv != nil {
		p.Getenv = getenv
	}
	return DetectHostIn(p, preferred)
}

// DetectHostIn 在给定探测依赖下执行统一探测（测试模拟注册表/PATH/文件系统）。
func DetectHostIn(p Probe, preferred string) (Host, error) {
	p = p.withDefaults()
	if chosen := strings.TrimSpace(preferred); chosen != "" {
		if p.Exists(chosen) {
			return Host{Path: chosen, Name: browserDisplayName(chosen), Source: "显式指定（render.chrome）"}, nil
		}
		return Host{}, fmt.Errorf("指定的浏览器不存在: %s（请到设置页重新选择）", chosen)
	}
	if envBin := strings.TrimSpace(p.Getenv(EnvBrowserBin)); envBin != "" {
		if p.Exists(envBin) {
			return Host{Path: envBin, Name: browserDisplayName(envBin), Source: "环境变量 " + EnvBrowserBin}, nil
		}
		return Host{}, fmt.Errorf("%s 指定的浏览器不存在: %s（请修正环境变量或改用 render.chrome）", EnvBrowserBin, envBin)
	}
	for _, it := range hostCandidates(p.GOOS, p) {
		if it.path != "" {
			if p.Exists(it.path) {
				return Host{Path: it.path, Name: it.name, Source: it.source}, nil
			}
			continue
		}
		if it.lookup == "" {
			continue
		}
		found, err := p.LookPath(it.lookup)
		if err != nil || !p.Exists(found) {
			continue
		}
		return Host{Path: found, Name: it.name, Source: "PATH"}, nil
	}
	return Host{}, errors.New("未找到可用浏览器：请安装公司指定浏览器（360 企业浏览器 360ent / 奇安信可信浏览器），" +
		"或到「设置」页手动选择浏览器（render.chrome）；CI/排障可用环境变量 UVMP_BROWSER_BIN 显式指定")
}

// hostItem 一项浏览器候选：path 为绝对路径，lookup 为 PATH 查找名（二选一）。
type hostItem struct {
	name   string // 显示名
	source string // 命中来源说明
	path   string // 绝对路径候选
	lookup string // PATH 查找名
}

// hostCandidates 按平台给出有序候选（360ent 优先，兼容 qaxbrowser / Chrome / Edge）。
func hostCandidates(goos string, p Probe) []hostItem {
	if goos == "windows" {
		return windowsHostCandidates(p)
	}
	return linuxHostCandidates(p)
}

// windowsHostCandidates Windows：360ent 注册表 App Paths（HKLM/HKCU）→
// %ProgramData%\360ent\Application\360ent.exe（ProgramData 环境变量优先，缺省
// C:\ProgramData）→ PATH / 常见目录，再兼容其他浏览器。
func windowsHostCandidates(p Probe) []hostItem {
	items := []hostItem{}
	for _, appPath := range p.AppPath("360ent.exe") {
		items = append(items, hostItem{name: "360 企业浏览器 360ent", source: "注册表 App Paths", path: appPath})
	}
	programFiles := firstNonEmpty(p.Getenv("ProgramFiles"), `C:\Program Files`)
	programFilesX86 := firstNonEmpty(p.Getenv("ProgramFiles(x86)"), `C:\Program Files (x86)`)
	programData := firstNonEmpty(p.Getenv("ProgramData"), `C:\ProgramData`)
	localAppData := p.Getenv("LOCALAPPDATA")
	items = append(items,
		hostItem{name: "360 企业浏览器 360ent", source: "常见目录", path: joinUnder(programData, "360ent", "Application", "360ent.exe")},
		hostItem{name: "360 企业浏览器 360ent", source: "PATH", lookup: "360ent.exe"},
		hostItem{name: "360 企业浏览器 360ent", source: "常见目录", path: filepath.Join(programFilesX86, "360", "360ent", "Application", "360ent.exe")},
		hostItem{name: "360 企业浏览器 360ent", source: "常见目录", path: filepath.Join(programFiles, "360", "360ent", "Application", "360ent.exe")},
		hostItem{name: "360 企业浏览器 360ent", source: "常见目录", path: joinUnder(localAppData, "360ent", "Application", "360ent.exe")},
		hostItem{name: "360 安全浏览器", source: "PATH", lookup: "360chrome.exe"},
		hostItem{name: "360 安全浏览器", source: "PATH", lookup: "360se.exe"},
		hostItem{name: "奇安信可信浏览器", source: "PATH", lookup: "qaxbrowser.exe"},
		hostItem{name: "奇安信可信浏览器", source: "常见目录", path: filepath.Join(programFilesX86, "QAXBrowser", "QAXBrowser.exe")},
		hostItem{name: "奇安信可信浏览器", source: "常见目录", path: filepath.Join(programFiles, "QAXBrowser", "QAXBrowser.exe")},
		hostItem{name: "Google Chrome", source: "PATH", lookup: "chrome.exe"},
		hostItem{name: "Google Chrome", source: "常见目录", path: `C:\Program Files\Google\Chrome\Application\chrome.exe`},
		hostItem{name: "Google Chrome", source: "常见目录", path: `C:\Program Files (x86)\Google\Chrome\Application\chrome.exe`},
		hostItem{name: "Google Chrome", source: "常见目录", path: joinUnder(localAppData, "Google", "Chrome", "Application", "chrome.exe")},
		hostItem{name: "Microsoft Edge", source: "PATH", lookup: "msedge.exe"},
		hostItem{name: "Microsoft Edge", source: "常见目录", path: `C:\Program Files (x86)\Microsoft\Edge\Application\msedge.exe`},
		hostItem{name: "Microsoft Edge", source: "常见目录", path: `C:\Program Files\Microsoft\Edge\Application\msedge.exe`},
	)
	return items
}

// linuxHostCandidates Linux/麒麟：browser360ent-cn-stable（PATH 与 /usr/bin）优先，
// 兼容奇安信可信浏览器 / Chrome / Edge。
func linuxHostCandidates(_ Probe) []hostItem {
	return []hostItem{
		{name: "360 企业浏览器 360ent", source: "PATH", lookup: "browser360ent-cn-stable"},
		{name: "360 企业浏览器 360ent", source: "常见目录", path: "/usr/bin/browser360ent-cn-stable"},
		{name: "360 企业浏览器 360ent", source: "常见目录", path: "/opt/apps/browser360ent-cn-stable/files/browser360ent-cn-stable"},
		{name: "奇安信可信浏览器", source: "PATH", lookup: "qaxbrowser"},
		{name: "奇安信可信浏览器", source: "常见目录", path: "/usr/bin/qaxbrowser"},
		{name: "奇安信可信浏览器", source: "常见目录", path: "/opt/qaxbrowser/qaxbrowser"},
		{name: "奇安信可信浏览器", source: "常见目录", path: "/opt/apps/qaxbrowser/qaxbrowser"},
		{name: "360 安全浏览器", source: "PATH", lookup: "browser360"},
		{name: "360 安全浏览器", source: "PATH", lookup: "browser360-cn"},
		{name: "360 安全浏览器", source: "常见目录", path: "/usr/bin/browser360"},
		{name: "360 安全浏览器", source: "常见目录", path: "/opt/apps/com.360.browser-stable/files/browser360"},
		{name: "Google Chrome", source: "PATH", lookup: "google-chrome-stable"},
		{name: "Google Chrome", source: "PATH", lookup: "google-chrome"},
		{name: "Google Chrome", source: "常见目录", path: "/usr/bin/google-chrome-stable"},
		{name: "Google Chrome", source: "常见目录", path: "/usr/bin/google-chrome"},
		{name: "Google Chrome", source: "常见目录", path: "/opt/google/chrome/chrome"},
		{name: "Chromium", source: "PATH", lookup: "chromium"},
		{name: "Chromium", source: "PATH", lookup: "chromium-browser"},
		{name: "Chromium", source: "常见目录", path: "/usr/bin/chromium"},
		{name: "Chromium", source: "常见目录", path: "/usr/bin/chromium-browser"},
		{name: "Microsoft Edge", source: "PATH", lookup: "microsoft-edge-stable"},
		{name: "Microsoft Edge", source: "PATH", lookup: "microsoft-edge"},
		{name: "Microsoft Edge", source: "常见目录", path: "/usr/bin/microsoft-edge-stable"},
		{name: "Microsoft Edge", source: "常见目录", path: "/usr/bin/microsoft-edge"},
	}
}

// browserDisplayName 显式路径的显示名（候选表未命中时用文件名）。
func browserDisplayName(path string) string {
	base := filepath.Base(path)
	if base == "" || base == "." {
		return "浏览器"
	}
	return base
}

// firstNonEmpty 返回第一个非空值。
func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

// joinUnder 在 base 非空时拼接路径（环境变量缺失返回空，避免相对路径误命中）。
func joinUnder(base string, elems ...string) string {
	if strings.TrimSpace(base) == "" {
		return ""
	}
	return filepath.Join(append([]string{base}, elems...)...)
}

// fileExists 常规文件存在性（目录不算）。
func fileExists(p string) bool {
	if p == "" {
		return false
	}
	st, err := os.Stat(p)
	return err == nil && st.Mode().IsRegular()
}
