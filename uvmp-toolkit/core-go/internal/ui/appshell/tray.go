package appshell

import (
	"bytes"
	"image"
	"image/color"
	"image/png"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/driver/desktop"
	"golang.org/x/image/draw"
)

// TrayMenu 系统托盘句柄：菜单随搜题扫描态刷新（对齐 electron/main.cjs refreshTrayMenu：
// 扫描中追加「停止搜题」——原嗖嗖搜题的托盘交互）。
type TrayMenu struct {
	da       desktop.App
	scanning bool
	onShow   func()
	onStop   func()
	onExit   func()
}

// SetupTray 建立系统托盘（图标 + 菜单：显示主窗口 / [停止搜题] / 退出）。
// onStopQuiz 为托盘「停止搜题」动作（仅扫描中显示）；返回句柄，
// App 不支持 desktop 扩展时返回 nil（关窗即退出不驻留）。
func SetupTray(a fyne.App, win fyne.Window, onExit, onStopQuiz func()) *TrayMenu {
	da, ok := a.(desktop.App)
	if !ok {
		return nil
	}
	// 托盘图标不在这里直接设：fyne.io/systray 的消息循环未就绪前调用 SetSystemTrayIcon
	// 会打 "systray error: unable to set icon: tray not ready yet" 并丢弃图标
	// （systray_windows.go setIcon → ErrTrayNotReadyYet）。Fyne 驱动在 systray onReady 时
	// 用 fyne.CurrentApp().Icon() 设托盘图标，所以这里只保证应用图标可用（调用方已设
	// 品牌图标则保持不变；未设时兜底托盘占位图）。
	if a.Icon() == nil {
		a.SetIcon(TrayIcon())
	}
	// 左键点托盘显示主窗口（v2.7+ 支持；不支持时仅菜单可用）。
	// Fyne Windows 驱动在此处断言自己的具体窗口类型，必须先解包壳层窗口。
	if sw, ok := a.(interface{ SetSystemTrayWindow(fyne.Window) }); ok {
		sw.SetSystemTrayWindow(windowBase(win))
	}
	t := &TrayMenu{
		da: da,
		onShow: func() {
			runUI(func() {
				win.Show()
				win.RequestFocus()
			})
		},
		onStop: onStopQuiz,
		onExit: onExit,
	}
	t.apply()
	return t
}

// SetQuizScanning 刷新扫描态（扫描中追加「停止搜题」；可在任意线程调用）。
func (t *TrayMenu) SetQuizScanning(scanning bool) {
	if t == nil || t.scanning == scanning {
		return
	}
	t.scanning = scanning
	t.apply()
}

func (t *TrayMenu) apply() {
	t.da.SetSystemTrayMenu(buildTrayMenu(t.scanning, t.onShow, t.onStop, t.onExit))
}

// buildTrayMenu 组装托盘菜单（纯函数，便于测试）。
func buildTrayMenu(scanning bool, onShow, onStopQuiz, onExit func()) *fyne.Menu {
	items := []*fyne.MenuItem{
		fyne.NewMenuItem("显示主窗口", onShow),
	}
	if scanning {
		items = append(items, fyne.NewMenuItem("停止搜题", onStopQuiz))
	}
	items = append(items, fyne.NewMenuItemSeparator(), fyne.NewMenuItem("退出", onExit))
	return fyne.NewMenu(AppTitle, items...)
}

// TrayIcon 返回托盘图标：真实产品图标缩放为 32×32（托盘图标必须在运行时可得，
// 随包内嵌避免 build 目录资源缺失——已踩坑）；解码失败时回退品牌蓝方块。
func TrayIcon() fyne.Resource {
	if icon, err := scaleIcon(shellIconPNG, 32); err == nil {
		return fyne.NewStaticResource("shade-tray.png", icon)
	}
	return fallbackTrayIcon()
}

// scaleIcon 把 PNG 字节等比缩放到指定边长（保持透明通道）。
func scaleIcon(src []byte, size int) ([]byte, error) {
	img, err := png.Decode(bytes.NewReader(src))
	if err != nil {
		return nil, err
	}
	dst := image.NewNRGBA(image.Rect(0, 0, size, size))
	draw.CatmullRom.Scale(dst, dst.Bounds(), img, img.Bounds(), draw.Src, nil)
	var buf bytes.Buffer
	if err := png.Encode(&buf, dst); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// fallbackTrayIcon 生成品牌蓝托盘图标（内嵌图标不可解码时的兜底）。
func fallbackTrayIcon() fyne.Resource {
	const size = 32
	img := image.NewNRGBA(image.Rect(0, 0, size, size))
	// 品牌蓝圆角方块 + 白色内框线（政企蓝白形态）
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			img.SetNRGBA(x, y, color.NRGBA{R: 0x0E, G: 0x3D, B: 0xA8, A: 0xFF})
		}
	}
	white := color.NRGBA{R: 0xFF, G: 0xFF, B: 0xFF, A: 0xFF}
	for i := 8; i < size-8; i++ {
		img.SetNRGBA(i, 8, white)
		img.SetNRGBA(i, size-9, white)
		img.SetNRGBA(8, i, white)
		img.SetNRGBA(size-9, i, white)
	}
	var buf bytes.Buffer
	_ = png.Encode(&buf, img)
	return fyne.NewStaticResource("uvmp-tray.png", buf.Bytes())
}
