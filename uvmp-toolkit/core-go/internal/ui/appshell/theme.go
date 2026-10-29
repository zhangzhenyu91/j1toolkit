// Package appshell 是内网工具箱的 Fyne 桌面壳：AppShell、8 个常驻页面、
// 政企蓝白主题与通用控件（Toast/LogView/HistoryList/弹层/文件目录选择/托盘）。
// 只做展示与交互，业务一律经 ui.Backend 接口（含 FakeBackend 冒烟实现）。
package appshell

import (
	"image/color"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/theme"
)

// 政企蓝白（Enterprise Blue）定稿 token（AGENTS.md 第四节 / design/设计规范.md）。
var (
	BrandBlue       = color.NRGBA{R: 0x0E, G: 0x3D, B: 0xA8, A: 0xFF} // 品牌蓝
	DeepBlue        = color.NRGBA{R: 0x0A, G: 0x35, B: 0x92, A: 0xFF} // 深蓝（页首）
	LightBlueBG     = color.NRGBA{R: 0xE8, G: 0xF0, B: 0xFF, A: 0xFF} // 浅蓝底
	SuccessGreen    = color.NRGBA{R: 0x00, G: 0xB4, B: 0x2A, A: 0xFF} // 成功绿
	SuccessGreenTxt = color.NRGBA{R: 0x0B, G: 0x8A, B: 0x37, A: 0xFF}
	WarningOrange   = color.NRGBA{R: 0xFF, G: 0x7D, B: 0x00, A: 0xFF} // 警告橙
	WarningOrangeTx = color.NRGBA{R: 0xD2, G: 0x5F, B: 0x00, A: 0xFF}
	DangerRed       = color.NRGBA{R: 0xF5, G: 0x3F, B: 0x3F, A: 0xFF} // 危险红
	DangerRedBG     = color.NRGBA{R: 0xFF, G: 0xEC, B: 0xE8, A: 0xFF}
	WarningOrangeBG = color.NRGBA{R: 0xFF, G: 0xF7, B: 0xE8, A: 0xFF}
	SuccessGreenBG  = color.NRGBA{R: 0xE8, G: 0xFF, B: 0xEA, A: 0xFF}
	PageBG          = color.NRGBA{R: 0xF5, G: 0xF7, B: 0xFA, A: 0xFF} // 页面背景
	CardBG          = color.NRGBA{R: 0xFF, G: 0xFF, B: 0xFF, A: 0xFF} // 卡片
	BlockBG         = color.NRGBA{R: 0xF7, G: 0xF8, B: 0xFA, A: 0xFF} // 嵌套浅灰块
	TextPrimary     = color.NRGBA{R: 0x1D, G: 0x21, B: 0x29, A: 0xFF} // 正文
	TextSecondary   = color.NRGBA{R: 0x4E, G: 0x59, B: 0x69, A: 0xFF} // 次要文字
	TextTertiary    = color.NRGBA{R: 0x86, G: 0x90, B: 0x9C, A: 0xFF} // 辅助文字
	Border          = color.NRGBA{R: 0xE5, G: 0xE6, B: 0xEB, A: 0xFF} // 边框/分隔线
	Placeholder     = color.NRGBA{R: 0xC9, G: 0xCD, B: 0xD4, A: 0xFF} // 弱化占位
)

// enterpriseTheme 政企蓝白主题：仅覆盖语义色，字号/图标/其余尺寸继承默认主题。
// 浅色定稿，深色变体同值（深色阶后续按设计规范统一覆盖，不在壳内各自造色板）。
type enterpriseTheme struct {
	base fyne.Theme
}

// NewTheme 构造政企蓝白主题。
func NewTheme() fyne.Theme {
	return &enterpriseTheme{base: theme.DefaultTheme()}
}

var _ fyne.Theme = (*enterpriseTheme)(nil)

func (t *enterpriseTheme) Color(name fyne.ThemeColorName, _ fyne.ThemeVariant) color.Color {
	switch name {
	case theme.ColorNameBackground:
		return PageBG
	case theme.ColorNameButton:
		return CardBG
	case theme.ColorNameDisabledButton:
		return BlockBG
	case theme.ColorNameDisabled:
		return Placeholder
	case theme.ColorNameError:
		return DangerRed
	case theme.ColorNameFocus:
		return BrandBlue
	case theme.ColorNameForeground:
		return TextPrimary
	case theme.ColorNameForegroundOnError:
		return CardBG
	case theme.ColorNameForegroundOnPrimary:
		return CardBG
	case theme.ColorNameForegroundOnSuccess:
		return CardBG
	case theme.ColorNameForegroundOnWarning:
		return CardBG
	case theme.ColorNameHeaderBackground:
		return CardBG
	case theme.ColorNameHover:
		return LightBlueBG
	case theme.ColorNameHyperlink:
		return BrandBlue
	case theme.ColorNameInputBackground:
		return CardBG
	case theme.ColorNameInputBorder:
		return Border
	case theme.ColorNameMenuBackground:
		return CardBG
	case theme.ColorNameOverlayBackground:
		return CardBG
	case theme.ColorNamePlaceHolder:
		return TextTertiary
	case theme.ColorNamePressed:
		return LightBlueBG
	case theme.ColorNamePrimary:
		return BrandBlue
	case theme.ColorNameScrollBar:
		return Placeholder
	case theme.ColorNameScrollBarBackground:
		return BlockBG
	case theme.ColorNameSelection:
		return LightBlueBG
	case theme.ColorNameSeparator:
		return Border
	case theme.ColorNameShadow:
		return color.NRGBA{R: 0, G: 0, B: 0, A: 0x14}
	case theme.ColorNameSuccess:
		return SuccessGreen
	case theme.ColorNameWarning:
		return WarningOrange
	default:
		return t.base.Color(name, theme.VariantLight)
	}
}

func (t *enterpriseTheme) Font(style fyne.TextStyle) fyne.Resource {
	return t.base.Font(style)
}

func (t *enterpriseTheme) Icon(name fyne.ThemeIconName) fyne.Resource {
	return t.base.Icon(name)
}

func (t *enterpriseTheme) Size(name fyne.ThemeSizeName) float32 {
	return t.base.Size(name)
}
