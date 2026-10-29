package rpc

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/zhangzhenyu91/j1toolkit/uvmp-toolkit/core-go/internal/config"
)

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

// detectChrome 返回可用的浏览器路径（getSystemInfo 用）；找不到报错。
// preferred 非空 = 用户在设置页/配置里指定的公司浏览器，优先且必须存在。
// （移植自 platform_utils.detect_chrome；仅 getSystemInfo 用途）
func detectChrome(preferred string) (string, error) {
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

// exeDir 可执行文件所在目录（suggested_outdirs 的 output 基准）。
func exeDir() string {
	p, err := os.Executable()
	if err != nil {
		return "."
	}
	if resolved, rerr := filepath.EvalSymlinks(p); rerr == nil {
		p = resolved
	}
	return filepath.Dir(p)
}

// openFile 打开文件（存在性检查用）。
func openFile(p string) (*os.File, error) {
	return os.Open(p)
}

// readFileString 读文本文件（非法字节替换）。
func readFileString(p string) (string, error) {
	data, err := os.ReadFile(p)
	if err != nil {
		return "", err
	}
	return strings.ToValidUTF8(string(data), "\uFFFD"), nil
}

// dirStat 目录下文件数与总字节。
func dirStat(dir string) (int, int64) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0, 0
	}
	n, total := 0, int64(0)
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		n++
		if info, err := e.Info(); err == nil {
			total += info.Size()
		}
	}
	return n, total
}

// configHasMock dev.mock（自检与调度判定用）。
func configHasMock(cfg config.Dict) bool {
	return config.BoolField(config.MapField(cfg, "dev"), "mock", false)
}
