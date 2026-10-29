// uvmp-toolkit 是「Shade 壹匣 - 内网」内网工具箱桌面客户端（Fyne）入口：
// 单实例锁 + 二次启动唤起、开机自启动管理、关窗驻留托盘、60s 驻留调度、退出确认、系统标题栏。
//
// 业务数据一律经 ui.Backend 接口获取；随壳交付的是 ui.CoreBackend（生产实现，
// 直连 Go 核心 job/state/config/apps 等），ui.FakeBackend 仅测试/冒烟用。
package main

import (
	"fmt"
	"log"
	"os"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"

	"github.com/zhangzhenyu91/j1toolkit/uvmp-toolkit/core-go/internal/platform"
	"github.com/zhangzhenyu91/j1toolkit/uvmp-toolkit/core-go/internal/ui"
	"github.com/zhangzhenyu91/j1toolkit/uvmp-toolkit/core-go/internal/ui/appshell"
)

// newBackend 构造业务后端。
// 桌面壳只依赖 ui.Backend 接口；生产实现为 ui.CoreBackend（Go 核心直连，
// 与 toolkit RPC 同口径），ui.FakeBackend 仅测试/冒烟用。
func newBackend() ui.Backend {
	return ui.NewCoreBackend()
}

func main() {
	// 生产冒烟（packaging/fyne/gate_smoke.sh 门禁契约）：按 UVMP_FYNE_SMOKE 模式
	// 跑完自行退出，不进正常启动（见 smoke.go）
	if mode := strings.TrimSpace(os.Getenv("UVMP_FYNE_SMOKE")); mode != "" {
		os.Exit(runSmoke(mode))
	}

	stateDir := platform.ResolveStateDir()
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		log.Fatalf("创建状态目录失败: %v", err)
	}

	// 单实例：已有实例在运行时发送「显示主窗口」信号后直接退出
	lock, err := platform.Acquire(platform.LockFile(stateDir))
	if err == platform.ErrAlreadyRunning {
		fmt.Println("已有实例在运行，已请求唤起主窗口")
		return
	}
	if err != nil {
		log.Fatalf("单实例锁获取失败: %v", err)
	}
	defer lock.Close()

	a := app.NewWithID("com.j1net.uvmp-toolkit")
	a.Settings().SetTheme(appshell.NewTheme())

	backend := newBackend()
	autoStart, err := platform.DefaultAutoStart()
	if err != nil {
		log.Printf("开机自启管理初始化失败（不影响主功能）: %v", err)
		autoStart = nil
	}

	shell, err := appshell.NewShell(a, backend, appshell.Options{AutoStart: autoStart})
	if err != nil {
		log.Fatalf("启动桌面壳失败: %v", err)
	}

	// 二次启动唤起主窗口（托盘驻留时窗口可能处于隐藏态）
	go func() {
		for range lock.ShowRequests() {
			fyne.Do(func() {
				shell.Win.Show()
				shell.Win.RequestFocus()
			})
		}
	}()

	shell.Win.ShowAndRun()
}
