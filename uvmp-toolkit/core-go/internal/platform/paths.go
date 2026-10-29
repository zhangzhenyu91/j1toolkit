// Package platform 提供桌面壳的平台能力（纯逻辑，无 GUI 依赖）：
// 单实例锁与二次启动唤起、开机自启动、关窗驻留/退出确认策略、60s 驻留调度、
// 状态与配置目录解析。托盘与窗口的 Fyne 接线见 internal/ui/appshell。
package platform

import (
	"os"
	"path/filepath"
)

// StateDir 状态目录（与 Python 核心 state.py 口径一致）：
// linux `$XDG_DATA_HOME/uvmp-toolkit`（缺省 `~/.local/share/uvmp-toolkit`）；
// windows `%LOCALAPPDATA%\uvmp-toolkit`；其他（darwin）`~/.local/share/uvmp-toolkit`。
// goos/home/xdg/localAppData 由调用方注入（测试免环境变量）。
func StateDir(goos, home, xdgDataHome, localAppData string) string {
	switch goos {
	case "windows":
		base := localAppData
		if base == "" {
			base = filepath.Join(home, "AppData", "Local")
		}
		return filepath.Join(base, "uvmp-toolkit")
	default:
		base := xdgDataHome
		if base == "" {
			base = filepath.Join(home, ".local", "share")
		}
		return filepath.Join(base, "uvmp-toolkit")
	}
}

// UserConfigDir 用户配置目录：windows `%APPDATA%\uvmp-toolkit`；
// 其他 `~/.config/uvmp-toolkit`（与核心冻结版用户配置目录一致）。
func UserConfigDir(goos, home, appData string) string {
	if goos == "windows" {
		base := appData
		if base == "" {
			base = filepath.Join(home, "AppData", "Roaming")
		}
		return filepath.Join(base, "uvmp-toolkit")
	}
	return filepath.Join(home, ".config", "uvmp-toolkit")
}

// ResolveStateDir 用真实环境变量解析状态目录。
func ResolveStateDir() string {
	return StateDir(GOOS(), os.Getenv("HOME"), os.Getenv("XDG_DATA_HOME"), os.Getenv("LOCALAPPDATA"))
}

// ResolveUserConfigDir 用真实环境变量解析用户配置目录。
// 注意：Windows 的 HOME/%APPDATA% 解析依赖环境变量，必要时由集成者用 os.UserHomeDir 补齐。
func ResolveUserConfigDir() string {
	return UserConfigDir(GOOS(), os.Getenv("HOME"), os.Getenv("APPDATA"))
}

// LockFile 单实例锁文件路径（状态目录下）。
func LockFile(stateDir string) string {
	return filepath.Join(stateDir, "uvmp-toolkit.lock")
}
