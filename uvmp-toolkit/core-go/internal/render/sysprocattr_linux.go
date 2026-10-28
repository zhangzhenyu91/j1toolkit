//go:build linux

package render

import (
	"os"
	"os/exec"
	"syscall"
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
}
