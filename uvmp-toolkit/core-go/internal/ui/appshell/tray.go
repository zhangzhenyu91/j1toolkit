package appshell

import (
	"bytes"
	"image"
	"image/color"
	"image/png"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/driver/desktop"
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
	da.SetSystemTrayIcon(TrayIcon())
	// 左键点托盘显示主窗口（v2.7+ 支持；不支持时仅菜单可用）
	if sw, ok := a.(interface{ SetSystemTrayWindow(fyne.Window) }); ok {
		sw.SetSystemTrayWindow(win)
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

// TrayIcon 生成品牌蓝托盘图标（运行时以 PNG 资源内嵌，避免随包资源缺失——
// 托盘图标必须在运行时可得，build 目录资源缺失已踩坑）。
func TrayIcon() fyne.Resource {
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
