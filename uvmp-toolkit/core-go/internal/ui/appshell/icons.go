package appshell

import (
	"fmt"
	"image/color"
	"sync"

	"fyne.io/fyne/v2"
)

// 壳层线性图标：24×24 viewBox、1.7px 描边、圆角端点，与网页端
// server/public/assets/icons.js 同一套形态语言（本包内嵌 SVG，不引用外部资源）。
// 图标按「名称 + 颜色」生成静态资源：Fyne 的 ThemedResource 只改 fill，
// 线条图标必须把颜色写进 stroke，故每种颜色单独缓存一份。

const lineIconTemplate = `<svg xmlns="http://www.w3.org/2000/svg" width="24" height="24" viewBox="0 0 24 24" fill="none" stroke="%s" stroke-width="1.7" stroke-linecap="round" stroke-linejoin="round">%s</svg>`

// lineIconPaths 图标路径（仅描边，不含 fill，保证 1.7px 线宽一致）。
var lineIconPaths = map[string]string{
	// 首页：屋檐 + 门洞
	"home": `<path d="M3.8 10.4 12 3.6l8.2 6.8V20a1 1 0 0 1-1 1h-4.7v-6.1H9.5V21H4.8a1 1 0 0 1-1-1z"/>`,
	// 派车同步：日历（定时）
	"calendar": `<rect x="3.5" y="5" width="17" height="15.5" rx="2"/><path d="M16 3v4M8 3v4M3.5 10h17"/><path d="M8.2 14.6h2.2M13.6 14.6h2.2M8.2 17.8h2.2M13.6 17.8h2.2"/>`,
	// 树障同步：林带
	"forest": `<path d="M12 3.5 6.8 10h2.7L4.8 16.5h4.7v4h5v-4h4.7L14.5 10h2.7z"/>`,
	// 交跨同步：两条路径交汇
	"fork": `<circle cx="6" cy="5" r="2.3"/><circle cx="18" cy="5" r="2.3"/><circle cx="12" cy="19" r="2.3"/><path d="M6 7.3v1.5a4 4 0 0 0 4 4h4a4 4 0 0 0 4-4V7.3M12 12.8v3.9"/>`,
	// 轨迹导出：起终点 + 行驶路线
	"route": `<circle cx="5.4" cy="18.6" r="2"/><circle cx="18.6" cy="5.4" r="2"/><path d="M7.4 18.6h4.1a3.4 3.4 0 0 0 0-6.8H9.9a3.4 3.4 0 0 1 0-6.8h6.7"/>`,
	// 题库搜题：放大镜
	"search": `<circle cx="10.8" cy="10.8" r="6.6"/><path d="M20.4 20.4 15.6 15.6"/>`,
	// 学堂刷课：显示器 + 播放
	"course": `<rect x="3" y="4.5" width="18" height="12.5" rx="2"/><path d="M9.9 8.3l4.6 2.4-4.6 2.4z"/><path d="M8.5 21h7M12 17v4"/>`,
	// 设置：齿轮
	"settings": `<circle cx="12" cy="12" r="3"/><path d="M19.4 15a1.7 1.7 0 0 0 .34 1.87l.06.06a2 2 0 1 1-2.83 2.83l-.06-.06a1.7 1.7 0 0 0-1.87-.34 1.7 1.7 0 0 0-1 1.55V21a2 2 0 1 1-4 0v-.09a1.7 1.7 0 0 0-1-1.55 1.7 1.7 0 0 0-1.87.34l-.06.06a2 2 0 1 1-2.83-2.83l.06-.06a1.7 1.7 0 0 0 .34-1.87 1.7 1.7 0 0 0-1.55-1H3a2 2 0 1 1 0-4h.09a1.7 1.7 0 0 0 1.55-1 1.7 1.7 0 0 0-.34-1.87l-.06-.06a2 2 0 1 1 2.83-2.83l.06.06a1.7 1.7 0 0 0 1.87.34h.01a1.7 1.7 0 0 0 1-1.55V3a2 2 0 1 1 4 0v.09a1.7 1.7 0 0 0 1 1.55 1.7 1.7 0 0 0 1.87-.34l.06-.06a2 2 0 1 1 2.83 2.83l-.06.06a1.7 1.7 0 0 0-.34 1.87v.01a1.7 1.7 0 0 0 1.55 1H21a2 2 0 1 1 0 4h-.09a1.7 1.7 0 0 0-1.55 1z"/>`,
	// 方向
	"chevron-right": `<path d="M9.5 6.5 15 12l-5.5 5.5"/>`,
	"arrow-right":   `<path d="M5 12h14M13 6l6 6-6 6"/>`,
	"external":      `<path d="M13.5 5H19v5.5"/><path d="M19 5 11 13"/><path d="M19 14.5V19a1 1 0 0 1-1 1H5a1 1 0 0 1-1-1V6a1 1 0 0 1 1-1h4.5"/>`,
	// 状态辅助
	"clock":    `<circle cx="12" cy="12" r="8.5"/><path d="M12 7.5V12l3.2 1.9"/>`,
	"history":  `<path d="M3.5 12a8.5 8.5 0 1 0 2.5-6"/><path d="M3.5 4.5v5h5"/><path d="M12 7.5V12l3.2 1.9"/>`,
	"star":     `<path d="m12 3 2.8 5.7 6.2.9-4.5 4.4 1.1 6.2-5.6-3-5.6 3 1.1-6.2L3 9.6l6.2-.9z"/>`,
	"copy":     `<rect x="8" y="8" width="12" height="12" rx="2"/><path d="M16 8V5a2 2 0 0 0-2-2H5a2 2 0 0 0-2 2v9a2 2 0 0 0 2 2h3"/>`,
	"download": `<path d="M12 3v12m0 0 4-4m-4 4-4-4"/><path d="M4 15v4a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2v-4"/>`,
}

var (
	lineIconMu    sync.Mutex
	lineIconCache = map[string]fyne.Resource{}
)

// lineIcon 取指定颜色的线性图标资源（同色同图复用，避免重复解析 SVG）。
func lineIcon(name string, c color.Color) fyne.Resource {
	path, ok := lineIconPaths[name]
	if !ok {
		panic("未登记线性图标: " + name)
	}
	key := name + "|" + hexColor(c)
	lineIconMu.Lock()
	defer lineIconMu.Unlock()
	if res, ok := lineIconCache[key]; ok {
		return res
	}
	body := fmt.Sprintf(lineIconTemplate, hexColor(c), path)
	res := fyne.NewStaticResource(name+".svg", []byte(body))
	lineIconCache[key] = res
	return res
}

// hexColor 颜色转 #RRGGBB（SVG stroke 用）。
func hexColor(c color.Color) string {
	n := color.NRGBAModel.Convert(c).(color.NRGBA)
	return fmt.Sprintf("#%02X%02X%02X", n.R, n.G, n.B)
}
