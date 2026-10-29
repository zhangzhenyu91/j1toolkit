//go:build !windows

package browser

import (
	"os"
	"os/exec"
	"syscall"
)

// detachProcess 让浏览器进程脱离当前进程组独立存活（Python
// browser_automation.launch 的 start_new_session 口径）：课程受控浏览器不随调用方
// 进程退出而被杀，退出走回环桥 /shutdown 或窗口关闭。
func detachProcess(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = new(syscall.SysProcAttr)
	}
	cmd.SysProcAttr.Setsid = true
}

// killTree 强杀进程树（防 Electron 孤儿占调试端口）：进程已 Setsid，pgid == pid，
// killpg 整组杀掉（与 toolkit/goexport.py 的 killpg 口径一致）；失败退化为单杀。
func killTree(pid int) {
	if pid <= 0 {
		return
	}
	if err := syscall.Kill(-pid, syscall.SIGKILL); err != nil {
		_ = syscall.Kill(pid, syscall.SIGKILL)
	}
}

// isRoot 是否 root 会话（Linux root 下 Chromium 需要 --no-sandbox，
// 与 Python browser_automation.launch 同口径）。
func isRoot() bool { return os.Geteuid() == 0 }
