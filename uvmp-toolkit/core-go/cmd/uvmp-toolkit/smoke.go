// 生产冒烟命令（packaging/fyne/gate_smoke.sh 门禁按此契约调用，见 packaging/fyne/README.md）：
//
//	UVMP_FYNE_SMOKE=gui —— 真实壳装配并完成首帧真实渲染后自行退出 0；失败非 0
//	UVMP_FYNE_SMOKE=ocr —— 按安装相对 native/models 布局（兼容开发路径）真实加载
//	  ONNX Runtime 与 det/rec 模型，零张量推理跑通后退出 0；失败非 0；
//	  不开主窗（无显示环境可跑），需以 -tags onnx 构建
package main

import (
	"fmt"
	"image"
	"log"
	"os"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"

	"github.com/zhangzhenyu91/j1toolkit/uvmp-toolkit/core-go/internal/platform"
	"github.com/zhangzhenyu91/j1toolkit/uvmp-toolkit/core-go/internal/ui/appshell"
)

// runSmoke 执行 UVMP_FYNE_SMOKE 冒烟模式并返回退出码（未知模式直接失败，不进正常启动）。
func runSmoke(mode string) int {
	switch mode {
	case "gui":
		return smokeGUI()
	case "ocr":
		return smokeOCR()
	default:
		fmt.Fprintf(os.Stderr, "冒烟失败：未知 UVMP_FYNE_SMOKE 模式 %q（支持 gui|ocr）\n", mode)
		return 2
	}
}

// smokeGUI 真实壳首帧冒烟：与生产同路径装配（应用/主题/业务后端/壳体），不取单实例锁
// （冒烟须可重复执行，且不得因已有真实实例而假通过）；首帧真实渲染完成即自行退出 0。
func smokeGUI() int {
	const waitFirstFrame = 60 * time.Second

	a := app.NewWithID("com.j1net.uvmp-toolkit")
	a.Settings().SetTheme(appshell.NewTheme())
	backend := newBackend()
	autoStart, err := platform.DefaultAutoStart()
	if err != nil {
		log.Printf("开机自启管理初始化失败（不影响冒烟）: %v", err)
		autoStart = nil
	}
	shell, err := appshell.NewShell(a, backend, appshell.Options{AutoStart: autoStart})
	if err != nil {
		fmt.Fprintf(os.Stderr, "GUI 冒烟失败：启动桌面壳失败: %v\n", err)
		return 1
	}

	frame := make(chan string, 1)
	go func() {
		info, ok := waitRenderedFrame(shell.Win, waitFirstFrame)
		if !ok {
			fmt.Fprintf(os.Stderr, "GUI 冒烟失败：%v 内未等到真实首帧渲染\n", waitFirstFrame)
			os.Exit(1) // 事件循环仍阻塞在 ShowAndRun，直接以非 0 结束
		}
		fmt.Println("GUI 冒烟：真实首帧渲染完成（", info, "）")
		frame <- info
		fyne.DoAndWait(func() { a.Quit() })
	}()

	shell.Win.ShowAndRun() // 事件循环由冒烟协程在首帧后 Quit
	// 冒烟自行退出不走 Shell.Quit：此处补释放底层资源（扫描服务等），与正常退出同口径
	if d, ok := backend.(interface{ Dispose() }); ok {
		d.Dispose()
	}
	select {
	case <-frame:
		return 0
	default:
		fmt.Fprintln(os.Stderr, "GUI 冒烟失败：窗口在首帧渲染前被关闭")
		return 1
	}
}

// waitRenderedFrame 轮询等待真实首帧：画布/内容已布局且 Canvas.Capture 读到已绘出
// 内容（非同色空帧）的真实帧，连续两次确认（跨轮询节拍，排除布局与绘制未落定）后
// 返回帧诊断串；超时返回 false。
func waitRenderedFrame(win fyne.Window, timeout time.Duration) (string, bool) {
	deadline := time.Now().Add(timeout)
	streak := 0
	info := ""
	for time.Now().Before(deadline) {
		ok := false
		fyne.DoAndWait(func() {
			defer func() { _ = recover() }() // 首帧前/未实现的 Capture 异常按未就绪处理，继续等
			cv := win.Canvas()
			if cv == nil {
				return
			}
			cs := cv.Size()
			if cs.Width < 1 || cs.Height < 1 {
				return
			}
			obj := win.Content()
			if obj == nil || obj.Size().Width < 1 || obj.Size().Height < 1 {
				return // 内容尚未布局
			}
			img := captureFrame(cv)
			if img == nil || img.Bounds().Dx() < 1 || img.Bounds().Dy() < 1 {
				return
			}
			if captureUniform(img) {
				return // 帧仍是同色空面，未绘出真实内容
			}
			info = fmt.Sprintf("canvas=%.0fx%.0f capture=%dx%d",
				cs.Width, cs.Height, img.Bounds().Dx(), img.Bounds().Dy())
			ok = true
		})
		if ok {
			streak++
			if streak >= 2 {
				return info, true
			}
		} else {
			streak = 0
		}
		time.Sleep(50 * time.Millisecond)
	}
	return "", false
}

// captureUniform 帧是否为同色空面（步进采样比较全部像素；壳界面必有文字/图标层次，
// 同色即认为尚未绘出真实内容）。
func captureUniform(img image.Image) bool {
	b := img.Bounds()
	step := 1
	if b.Dx()*b.Dy() > 2_000_000 {
		step = 7
	}
	var firstR, firstG, firstB, firstA uint32
	first := false
	for y := b.Min.Y; y < b.Max.Y; y += step {
		for x := b.Min.X; x < b.Max.X; x += step {
			r, g, bt, a := img.At(x, y).RGBA()
			if !first {
				firstR, firstG, firstB, firstA = r, g, bt, a
				first = true
				continue
			}
			if r != firstR || g != firstG || bt != firstB || a != firstA {
				return false
			}
		}
	}
	return true
}
