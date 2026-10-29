//go:build !linux && !windows

package render

import "os/exec"

// defaultSysProcAttr macOS 沿用 chromedp 默认进程设置（无特殊设置）。
func defaultSysProcAttr(_ *exec.Cmd) {}

// terminateBrowserProcess 非 Linux/Windows 沿用 chromedp 取消上下文后的默认进程清理。
func terminateBrowserProcess(_ *exec.Cmd) {}

// cleanupBrowserByProfile 非 Windows 无启动器派生进程问题，无需按 profile 兜底清理。
func cleanupBrowserByProfile(_ string) {}
