package appshell

import (
	_ "embed"

	"fyne.io/fyne/v2"
)

// 字体随应用嵌入，离线目标机不依赖系统 sans-serif 或 fontconfig 回退。
// Noto Sans SC 负责比例正文；JetBrains Maple Mono 负责日志等需要对齐的内容。
var (
	//go:embed assets/fonts/NotoSansSC-Regular.ttf
	notoSansSCRegularTTF []byte
	//go:embed assets/fonts/NotoSansSC-Bold.ttf
	notoSansSCBoldTTF []byte
	//go:embed assets/fonts/JetBrainsMapleMono-Regular.ttf
	jetBrainsMapleMonoRegularTTF []byte

	notoSansSCRegular  = fyne.NewStaticResource("NotoSansSC-Regular.ttf", notoSansSCRegularTTF)
	notoSansSCBold     = fyne.NewStaticResource("NotoSansSC-Bold.ttf", notoSansSCBoldTTF)
	jetBrainsMapleMono = fyne.NewStaticResource(
		"JetBrainsMapleMono-Regular.ttf", jetBrainsMapleMonoRegularTTF)
)

// embeddedFont 按文字语义选择字体。正文未引入斜体字重，Italic 回退同字重正体，
// 避免退回 Fyne 默认字体后丢失中文；Symbol 仍由基础主题提供。
func embeddedFont(style fyne.TextStyle, base fyne.Theme) fyne.Resource {
	if style.Symbol {
		return base.Font(style)
	}
	if style.Monospace {
		return jetBrainsMapleMono
	}
	if style.Bold {
		return notoSansSCBold
	}
	return notoSansSCRegular
}
