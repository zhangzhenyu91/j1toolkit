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
	"image/png"
	"log"
	"os"
	"strings"
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

	// 冒烟进程禁用输入法（Windows）：避免本机第三方 IME 在窗口取前台焦点时注入本进程，
	// 把 IME 自己的 libpng 警告写到门禁 stderr（来源见 imm_windows.go；仅冒烟路径）
	disableIMEForSmoke()

	a := app.NewWithID("com.j1net.uvmp-toolkit")
	a.Settings().SetTheme(appshell.NewTheme())
	// 与正常启动同口径：先设应用图标（托盘图标由 Fyne 在 systray 就绪时取应用图标）
	applyBrandIcon(a, nil)
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
	// 主窗口图标同样用品牌图标：冒烟顺带验证图标资源可加载
	applyBrandIcon(a, shell.Win)
	// 本地视觉回归可显式指定窗口尺寸，例如 UVMP_FYNE_SMOKE_SIZE=900x600。
	// 默认门禁不传此变量，仍使用生产尺寸；不允许低于壳层约定的 900×600。
	if raw := os.Getenv("UVMP_FYNE_SMOKE_SIZE"); raw != "" {
		var width, height float32
		if _, scanErr := fmt.Sscanf(raw, "%fx%f", &width, &height); scanErr != nil || width < 900 || height < 600 {
			fmt.Fprintf(os.Stderr, "GUI 冒烟失败：无效 UVMP_FYNE_SMOKE_SIZE %q（应为宽x高且不小于 900x600）\n", raw)
			return 2
		}
		shell.Win.Resize(fyne.NewSize(width, height))
	}
	// 本地视觉回归可显式指定常驻页面；缺省仍为首页，不改变正式门禁路径。
	if page := strings.TrimSpace(os.Getenv("UVMP_FYNE_SMOKE_PAGE")); page != "" {
		shell.Navigate(page)
	}

	frame := make(chan string, 1)
	go func() {
		info, ok := waitRenderedFrame(shell.Win, waitFirstFrame)
		if !ok {
			fmt.Fprintf(os.Stderr, "GUI 冒烟失败：%v 内未等到真实首帧渲染\n", waitFirstFrame)
			os.Exit(1) // 事件循环仍阻塞在 ShowAndRun，直接以非 0 结束
		}
		fmt.Println("GUI 冒烟：真实首帧渲染完成（", info, "）")
		// 可选截图仅用于视觉回归，不影响默认门禁；文件路径由本地调用方显式给出。
		if path := os.Getenv("UVMP_FYNE_SMOKE_CAPTURE"); path != "" {
			var img image.Image
			fyne.DoAndWait(func() { img = captureFrame(shell.Win.Canvas()) })
			if img == nil {
				fmt.Fprintln(os.Stderr, "GUI 冒烟截图失败：没有取得真实帧")
				os.Exit(1)
			}
			file, createErr := os.Create(path)
			if createErr == nil {
				createErr = png.Encode(file, img)
				if closeErr := file.Close(); createErr == nil {
					createErr = closeErr
				}
			}
			if createErr != nil {
				fmt.Fprintln(os.Stderr, "GUI 冒烟截图失败：", createErr)
				os.Exit(1)
			}
			fmt.Println("GUI 冒烟截图：", path)
		}
		if err := exerciseWindowMaximize(shell); err != nil {
			fmt.Fprintln(os.Stderr, "窗口最大化冒烟失败：", err)
			os.Exit(1)
		}
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
