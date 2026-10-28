package render

import (
	"encoding/base64"
	"strings"
)

// PNGWrapHTML 轨迹 PNG 包版成 PDF 的页面模板（$img_uri 为图片地址占位符）。
// 与 official_track.py PNG_WRAP_HTML 一致：A4 横向、左侧加宽页边距装订。
const PNGWrapHTML = `<!DOCTYPE html>
<html lang="zh-CN"><head><meta charset="utf-8"><style>
@page { size: A4 landscape; margin: 5mm 5mm 5mm 20mm; }
body { margin: 0; -webkit-print-color-adjust: exact; }
img { display: block; width: 272mm; height: 200mm; }
</style></head><body><img src="$img_uri"></body></html>`

// pngURIPlaceholder PNGWrapHTML 中的图片地址占位符。
const pngURIPlaceholder = "$img_uri"

// BuildPNGWrapHTML 把图片地址填入 PNGWrapHTML 模板。
func BuildPNGWrapHTML(imgURI string) string {
	return strings.Replace(PNGWrapHTML, pngURIPlaceholder, imgURI, 1)
}

// PNGDataURI 把 PNG 字节编成 data: 地址（同浏览器 document.write 包版用，
// 不依赖 file:// 与跨源策略）。
func PNGDataURI(png []byte) string {
	return "data:image/png;base64," + base64.StdEncoding.EncodeToString(png)
}
