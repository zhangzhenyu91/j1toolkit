package main

import (
	_ "embed"

	"fyne.io/fyne/v2"
)

// appIconPNG 是「Shade 壹匣」真实应用图标：desktop/src/assets/icon.png 重编码为 256×256，
// 丢弃 ICC/文本等元数据的干净 PNG（与 packaging/icon.png 同源；安装包 icon.ico 由同一源图生成，
// 见 packaging/fyne/assemble_nsis.ps1）。内嵌而非运行期读文件——托盘/任务栏图标必须随二进制
// 恒可用（构建目录缺资源已踩坑）。
//
//go:embed icon.png
var appIconPNG []byte

// applyBrandIcon 把品牌图标挂到应用（后续窗口的默认图标，也是托盘图标的来源）与主窗口。
// 托盘图标不在这里直接 SetSystemTrayIcon：systray 消息循环就绪前调用会报
// "tray not ready yet" 并丢图标；Fyne 在 systray onReady 时用 CurrentApp().Icon() 设托盘图标
// （见 internal/driver/glfw/driver_desktop.go runSystray），先设应用图标即可覆盖托盘。
// win 可为 nil（只设应用级图标）。
func applyBrandIcon(a fyne.App, win fyne.Window) {
	if a == nil {
		return
	}
	res := fyne.NewStaticResource("icon.png", appIconPNG)
	a.SetIcon(res)
	if win != nil {
		win.SetIcon(res)
	}
}
