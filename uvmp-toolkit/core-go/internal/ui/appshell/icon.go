package appshell

import (
	_ "embed"

	"fyne.io/fyne/v2"
)

// shellIconPNG 随包内嵌的产品图标（源：uvmp-toolkit/desktop/src/assets/icon.png，
// 1024×1024 RGBA；安装包 icon.ico 与 packaging/icon.png 由同一源图生成）。
// go:embed 不能跨包目录引用，故在包内保留一份，供品牌区与托盘在运行时直接使用。
//
//go:embed assets/icon.png
var shellIconPNG []byte

// shellIconRes 品牌图标资源（StaticResource 可被 Fyne 图像与托盘直接使用）。
var shellIconRes = fyne.NewStaticResource("shade-icon.png", shellIconPNG)

// ShellIcon 返回「Shade 壹匣」品牌图标资源（品牌区、托盘共用，运行时内嵌保证随包可得）。
func ShellIcon() fyne.Resource { return shellIconRes }
