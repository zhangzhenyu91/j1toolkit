//go:build linux

package render

import (
	"os"
	"os/exec"
	"syscall"
	"time"
)

// defaultSysProcAttr 复刻 chromedp ExecAllocator 的默认进程设置（allocateCmdOptions）：
// ModifyCmdFunc 会替换它，故用 BrowserArgs 时须先补回。Linux 上父进程（Go）死亡时
// 连带杀掉浏览器子进程，避免遗留孤儿 Electron/Chromium 占着调试端口。
func defaultSysProcAttr(cmd *exec.Cmd) {
	if _, ok := os.LookupEnv("LAMBDA_TASK_ROOT"); ok {
		return // AWS Lambda 环境不设 Pdeathsig（与 chromedp 一致）
	}
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = new(syscall.SysProcAttr)
	}
	cmd.SysProcAttr.Pdeathsig = syscall.SIGKILL
	cmd.SysProcAttr.Setpgid = true
}

// terminateBrowserProcess 终止本次浏览器的独立进程组并等待回收，防止 Electron 子进程
// 在关闭根进程后继续写 profile，导致工作目录删除竞态。只作用于本进程创建的进程组。
func terminateBrowserProcess(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	if cmd.SysProcAttr != nil && cmd.SysProcAttr.Setpgid {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	} else {
		_ = cmd.Process.Kill()
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if err := syscall.Kill(cmd.Process.Pid, 0); err == syscall.ESRCH {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
}
