//go:build !linux

package render

import "os/exec"

// defaultSysProcAttr 复刻 chromedp ExecAllocator 的默认进程设置（allocateCmdOptions）：
// 非 Linux 平台 chromedp 不做任何设置（Windows/macOS 默认行为即空操作）。
func defaultSysProcAttr(_ *exec.Cmd) {}

// terminateBrowserProcess 非 Linux 沿用 chromedp 取消上下文后的默认进程清理。
func terminateBrowserProcess(_ *exec.Cmd) {}
