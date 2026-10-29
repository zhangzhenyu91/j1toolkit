//go:build windows

package browser

import (
	"os/exec"
	"strconv"
	"syscall"
)

// createNoWindow 隐藏 taskkill 控制台窗口（CREATE_NO_WINDOW）。
const createNoWindow = 0x08000000

// detachProcess 让浏览器进程脱离当前进程组独立存活（Python
// browser_automation.launch 的 CREATE_NEW_PROCESS_GROUP 口径）：课程受控浏览器
// 不随调用方进程退出而被杀，退出走回环桥 /shutdown 或窗口关闭。
func detachProcess(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = new(syscall.SysProcAttr)
	}
	cmd.SysProcAttr.CreationFlags = syscall.CREATE_NEW_PROCESS_GROUP
}

// runTaskkill 执行 taskkill（隐藏窗口，不闪控制台）；tree=true 连带子进程。
func runTaskkill(pid int, tree bool) error {
	args := []string{"/PID", strconv.Itoa(pid)}
	if tree {
		args = append(args, "/T")
	}
	args = append(args, "/F")
	cmd := exec.Command("taskkill", args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: createNoWindow}
	return cmd.Run()
}

// killTree 强杀进程树（防浏览器孤儿占调试端口/锁 profile）：taskkill /T /F，
// 与 toolkit/goexport.py 同口径；失败退化为单杀。
func killTree(pid int) {
	if pid <= 0 {
		return
	}
	if err := runTaskkill(pid, true); err != nil {
		_ = runTaskkill(pid, false)
	}
}

// isRoot Windows 无 root 概念（--no-sandbox 仅 Linux root 会话需要）。
func isRoot() bool { return false }
