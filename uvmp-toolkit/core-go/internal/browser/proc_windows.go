//go:build windows

package browser

import (
	"os/exec"
	"strconv"
	"syscall"
)

// detachProcess 让浏览器进程脱离当前进程组独立存活（Python
// browser_automation.launch 的 CREATE_NEW_PROCESS_GROUP 口径）：课程受控浏览器
// 不随调用方进程退出而被杀，退出走回环桥 /shutdown 或窗口关闭。
func detachProcess(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = new(syscall.SysProcAttr)
	}
	cmd.SysProcAttr.CreationFlags = syscall.CREATE_NEW_PROCESS_GROUP
}

// killTree 强杀进程树（防 Electron 孤儿占调试端口）：taskkill /T /F，
// 与 toolkit/goexport.py 同口径；失败退化为单杀。
func killTree(pid int) {
	if pid <= 0 {
		return
	}
	err := exec.Command("taskkill", "/PID", strconv.Itoa(pid), "/T", "/F").Run()
	if err != nil {
		_ = exec.Command("taskkill", "/PID", strconv.Itoa(pid), "/F").Run()
	}
}

// isRoot Windows 无 root 概念（--no-sandbox 仅 Linux root 会话需要）。
func isRoot() bool { return false }
