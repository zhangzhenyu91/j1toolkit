package main

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/zhangzhenyu91/j1toolkit/uvmp-toolkit/core-go/internal/platform"
)

// guiLauncherName 安装包随包的 GUI 隐藏启动器（packaging/fyne/run-uvmp-gui.vbs）：
// wscript 隐藏控制台子系统 exe 的黑窗，并把安装目录 native\ 注入 PATH。
const guiLauncherName = "run-uvmp-gui.vbs"

// prependNativeDir 把可执行文件同目录的 native\（ONNX Runtime 运行库）注入 PATH，
// 兜底不经启动器直启 exe 的场景；目录不存在时不动环境，幂等。
func prependNativeDir() {
	exe, err := os.Executable()
	if err != nil {
		return
	}
	native := filepath.Join(filepath.Dir(exe), "native")
	if st, err := os.Stat(native); err != nil || !st.IsDir() {
		return
	}
	path := os.Getenv("PATH")
	lower := strings.ToLower(path)
	if strings.Contains(lower, strings.ToLower(native)+string(os.PathListSeparator)) {
		return
	}
	_ = os.Setenv("PATH", native+string(os.PathListSeparator)+path)
}

// installedGUILauncher 返回安装态隐藏启动器的绝对路径；未安装（源码运行/go run）返回空串。
func installedGUILauncher() string {
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	p := filepath.Join(filepath.Dir(exe), guiLauncherName)
	if st, err := os.Stat(p); err != nil || st.IsDir() {
		return ""
	}
	return p
}

// applyAutoStartLauncher 把开机自启目标改指 wscript 隐藏启动器：
// 直接注册 uvmp-toolkit.exe（控制台子系统）会在登录时弹控制台窗口，且缺 native\ 运行库
// 搜索路径；非 Windows 或未安装时保持 DefaultAutoStart 的原始可执行文件路径。
func applyAutoStartLauncher(a *platform.AutoStart) {
	if a == nil || platform.GOOS() != "windows" {
		return
	}
	launcher := installedGUILauncher()
	if launcher == "" {
		return
	}
	a.ExecPath = `wscript.exe "` + launcher + `"`
}
