//go:build windows

package render

import (
	"os/exec"
	"strconv"
	"syscall"
)

// createNoWindow 子进程不弹控制台窗口（taskkill/PowerShell 兜底用）。
const createNoWindow = 0x08000000

// defaultSysProcAttr Windows 沿用默认进程设置（chromedp 在 Windows 不做特殊设置）。
func defaultSysProcAttr(_ *exec.Cmd) {}

// terminateBrowserProcess 终止本包拉起的宿主浏览器进程树：Windows 先 taskkill /T
// 连带子进程（渲染/GPU/crashpad），再 Process.Kill 兜底，避免残留进程占用 profile。
func terminateBrowserProcess(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	kill := exec.Command("taskkill", "/T", "/F", "/PID", strconv.Itoa(cmd.Process.Pid))
	kill.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: createNoWindow}
	_ = kill.Run()
	_ = cmd.Process.Kill()
}

// cleanupBrowserByProfile 兜底清理：Windows 上 Edge 启动器会先退出、由浏览器主进程继续
// 运行（--edge-skip-compat-layer-relaunch），若启动/CDP 初始化失败拿不到进程句柄，就按
// --user-data-dir 精确匹配并结束该专用 profile 的进程（不触碰用户日常浏览器）。
// 只在启动失败/优雅关闭超时时调用，正常关闭走 CDP Browser.close。
func cleanupBrowserByProfile(profileDir string) {
	script := buildProfileCleanupScript(profileDir)
	if script == "" {
		return
	}
	cmd := exec.Command("powershell", "-NoProfile", "-NonInteractive", "-Command", script)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: createNoWindow}
	_ = cmd.Run()
}
