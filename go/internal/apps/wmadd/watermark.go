// 图片水印渲染（对齐 server/src/worklog/render-photo.js + worklog/watermark.js 语义）：
// 字段清洗 sanitizeWm（内容/时间/天气/地点/经纬度按库列宽截断，防伪码服务端生成）、
// EXIF 方向矫正（与 @napi-rs/canvas loadImage 一致按 EXIF 1-8 摆正，再按 wx.getImageInfo
// 口径 up/down/left/right ± mirrored 转画布）、v2 卡片水印绘制（compat/imagepdf
// DrawWatermarkCard：蓝头黄点卡片 + 右下品牌块防伪双软影），JPEG quality 95 输出。
package wmadd

import (
	"bytes"
	"errors"
	"fmt"
	"image"
	"math/rand"
	"os"
	"strings"
	"sync"
	"time"

	"shade/internal/assetpath"
	"shade/internal/compat/imagepdf"
)

// WatermarkFields 清洗后的水印字段（渲染入参）。
type WatermarkFields struct {
	Content   string `json:"content"`   // 施工内容（≤500）
	Time      string `json:"time"`      // 拍摄时间（≤32，空则取当前 YYYY.MM.DD HH:mm）
	Weather   string `json:"weather"`   // 天气（≤64）
	Location  string `json:"location"`  // 地点（≤250）
	Longitude string `json:"longitude"` // 经度（≤32）
	Latitude  string `json:"latitude"`  // 纬度（≤32）
	AntiCode  string `json:"antiCode"`  // 防伪码（14 位字符集内，否则服务端重新生成）
}

// 字段清洗截断长度（对齐库列宽 work_content 500 / shot_time 32 / weather 64 / location 250）。
const (
	cutContent   = 500
	cutTime      = 32
	cutWeather   = 64
	cutLocation  = 250
	cutLongitude = 32
	cutLatitude  = 32
	antiCodeLen  = 14
)

// 防伪码字符集：去掉 0/O、1/I 等易混淆字符（与 worklog/watermark.js 一致）。
const antiCodeAlphabet = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789"

var antiRand = rand.New(rand.NewSource(time.Now().UnixNano()))
var antiRandMu sync.Mutex

// RandomAntiCode 生成 n 位防伪码（字符集 A-HJ-NP-Z2-9）。
func RandomAntiCode(n int) string {
	if n <= 0 {
		n = antiCodeLen
	}
	antiRandMu.Lock()
	defer antiRandMu.Unlock()
	b := make([]byte, n)
	for i := range b {
		b[i] = antiCodeAlphabet[antiRand.Intn(len(antiCodeAlphabet))]
	}
	return string(b)
}

// utf16Len UTF-16 码元数（JS 字符串长度口径）。
func utf16Len(s string) int {
	n := 0
	for _, r := range s {
		if r > 0xFFFF {
			n += 2
		} else {
			n++
		}
	}
	return n
}

// cut 字符串清洗：字符串、去首尾空格、按 UTF-16 码元限长（对齐 JS String.slice(0,n)）。
// JS 的 slice 会切开代理对，此处按完整字符截断，边缘行为更安全。
func cut(v string, n int) string {
	v = strings.TrimSpace(v)
	if utf16Len(v) <= n {
		return v
	}
	units := 0
	for i, r := range v {
		w := 1
		if r > 0xFFFF {
			w = 2
		}
		if units+w > n {
			return v[:i]
		}
		units += w
	}
	return v
}

// SanitizeWm 水印字段清洗（出工日志与水印添加同口径）：
// 字符串、去首尾空格、按库列宽截断；防伪码 14 位字符集内才采信前端值，否则服务端重新生成。
func SanitizeWm(wm map[string]string) WatermarkFields {
	get := func(k string) string { return strings.TrimSpace(wm[k]) }
	code := cut(get("antiCode"), antiCodeLen)
	ok := utf16Len(code) == antiCodeLen
	for _, r := range code {
		if !strings.ContainsRune(antiCodeAlphabet, r) {
			ok = false
			break
		}
	}
	if !ok {
		code = RandomAntiCode(antiCodeLen)
	}
	return WatermarkFields{
		Content:   cut(get("content"), cutContent),
		Time:      cut(get("time"), cutTime),
		Weather:   cut(get("weather"), cutWeather),
		Location:  cut(get("location"), cutLocation),
		Longitude: cut(get("longitude"), cutLongitude),
		Latitude:  cut(get("latitude"), cutLatitude),
		AntiCode:  code,
	}
}

// PhotoRenderer 照片水印渲染适配层（默认实现 WatermarkRenderer；测试可注入桩）。
type PhotoRenderer interface {
	// Render 原图（jpeg/png）+ 水印字段 + EXIF 方向 → JPEG 字节。
	Render(photo []byte, wm WatermarkFields, orientation string) ([]byte, error)
}

// WatermarkRenderer 基于 compat/imagepdf 的默认渲染实现（v2 卡片版式）。
// Fonts/品牌素材为 nil 时 Render 懒加载默认素材（字体 imagepdf.LoadDefaultFonts、
// 品牌三件 assetpath 探测 assets/…），装配方可零值注入（main 的 &wmadd.WatermarkRenderer{}）。
type WatermarkRenderer struct {
	Fonts           *imagepdf.FontFiles // 字体三件套（DefaultFontsDir 加载；nil 懒加载）
	Brand           image.Image         // brand-logo.png 品牌 logo（nil 懒加载）
	CodeShadow      image.Image         // code-shadow.png 码值底衬软影条（nil 懒加载）
	CodeShadowLabel image.Image         // code-shadow-label.png 「防伪」软影短条（nil 懒加载）

	assetsMu   sync.Mutex
	assetsDone bool
}

// NewWatermarkRenderer 构造渲染器并自动加载字体三件套与品牌素材；
// 加载失败不报错，降级为懒加载由 Render 时给出明确错误（避免装配期误杀启动）。
func NewWatermarkRenderer() *WatermarkRenderer {
	r := &WatermarkRenderer{}
	if fonts, err := imagepdf.LoadDefaultFonts(); err == nil {
		r.Fonts = fonts
	}
	_ = r.ensureAssets()
	return r
}

// fonts 字体三件套：显式注入优先，否则走默认字体（imagepdf 内缓存，天然并发安全）。
func (r *WatermarkRenderer) fonts() (*imagepdf.FontFiles, error) {
	if r.Fonts != nil {
		return r.Fonts, nil
	}
	return imagepdf.LoadDefaultFonts()
}

// ensureAssets 懒加载品牌素材三件（显式注入优先；assetpath 探测 assets/ 与 server/assets/）。
func (r *WatermarkRenderer) ensureAssets() error {
	r.assetsMu.Lock()
	defer r.assetsMu.Unlock()
	if r.assetsDone {
		return nil
	}
	load := func(sub string, dst *image.Image) error {
		if *dst != nil {
			return nil
		}
		p := assetpath.ResolveFile(sub)
		if p == "" {
			return fmt.Errorf("wmadd: 素材缺失 %s", sub)
		}
		buf, err := os.ReadFile(p)
		if err != nil {
			return fmt.Errorf("wmadd: 读素材 %s: %w", sub, err)
		}
		img, err := imagepdf.DecodeImage(buf)
		if err != nil {
			return fmt.Errorf("wmadd: 解码素材 %s: %w", sub, err)
		}
		*dst = img
		return nil
	}
	if err := load("brand-logo.png", &r.Brand); err != nil {
		return err
	}
	if err := load("code-shadow.png", &r.CodeShadow); err != nil {
		return err
	}
	if err := load("code-shadow-label.png", &r.CodeShadowLabel); err != nil {
		return err
	}
	r.assetsDone = true
	return nil
}

// 编译期确认 WatermarkRenderer 满足 PhotoRenderer（可装配）。
var _ PhotoRenderer = (*WatermarkRenderer)(nil)

// Render 解码原图 → EXIF/wx 方向摆正 → 画 v2 卡片水印 → JPEG 输出（quality 95，与 Node 一致）。
func (r *WatermarkRenderer) Render(photo []byte, wm WatermarkFields, orientation string) ([]byte, error) {
	if r == nil {
		return nil, errors.New("wmadd: 渲染器未装配")
	}
	fonts, err := r.fonts()
	if err != nil {
		return nil, fmt.Errorf("wmadd: 字体未加载: %w", err)
	}
	if err := r.ensureAssets(); err != nil {
		return nil, err
	}
	img, err := decodeOriented(photo)
	if err != nil {
		return nil, fmt.Errorf("wmadd: 图片解码失败: %w", err)
	}
	img = orientByWxTag(img, orientation)

	// 空时间取当前（与 watermark.js 默认一致）
	if wm.Time == "" {
		wm.Time = time.Now().Format("2006.01.02 15:04")
	}
	out, err := imagepdf.DrawWatermarkCard(img, fonts, imagepdf.WatermarkCardParams{
		Content:              wm.Content,
		Time:                 wm.Time,
		Weather:              wm.Weather,
		Location:             wm.Location,
		Longitude:            wm.Longitude,
		Latitude:             wm.Latitude,
		AntiCode:             wm.AntiCode,
		BrandImage:           r.Brand,
		CodeShadowImage:      r.CodeShadow,
		CodeShadowLabelImage: r.CodeShadowLabel,
	})
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	if err := imagepdf.EncodeJPEG(&buf, out, 95); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// decodeOriented 解码 jpeg/png/webp 并按 EXIF orientation(1-8) 摆正
// （对齐 @napi-rs/canvas loadImage 的 EXIF 行为；非 JPEG 恒无 EXIF）。
func decodeOriented(data []byte) (image.Image, error) {
	img, err := imagepdf.DecodeImage(data)
	if err != nil {
		return nil, err
	}
	o := 1
	if imagepdf.ImageFormat(data) == "jpeg" {
		o = imagepdf.JPEGOrientation(data)
	}
	return autoOrientEXIF(img, o), nil
}

// autoOrientEXIF 按 EXIF orientation(1-8) 摆正像素（含镜像），对齐 @napi-rs/canvas loadImage。
// 注：与 compat/imagepdf.AutoOrient 同表，独立保留本包回归参照。
func autoOrientEXIF(img image.Image, o int) image.Image {
	if img == nil {
		return nil
	}
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	switch o {
	case 2, 3, 4, 5, 6, 7, 8:
	default:
		return img
	}
	outW, outH := w, h
	if o >= 5 {
		outW, outH = h, w
	}
	dst := image.NewRGBA(image.Rect(0, 0, outW, outH))
	for oy := 0; oy < outH; oy++ {
		for ox := 0; ox < outW; ox++ {
			var sx, sy int
			switch o {
			case 2: // 水平镜像
				sx, sy = w-1-ox, oy
			case 3: // 旋转 180°
				sx, sy = w-1-ox, h-1-oy
			case 4: // 垂直镜像
				sx, sy = ox, h-1-oy
			case 5: // 转置（主对角线翻转）
				sx, sy = oy, ox
			case 6: // 顺时针 90°
				sx, sy = oy, h-1-ox
			case 7: // 反对角线翻转
				sx, sy = w-1-oy, h-1-ox
			case 8: // 逆时针 90°
				sx, sy = w-1-oy, ox
			}
			dst.Set(ox, oy, img.At(b.Min.X+sx, b.Min.Y+sy))
		}
	}
	return dst
}

// orientByWxTag 按微信 wx.getImageInfo 的 orientation 标签把像素摆正
// （对齐 render-photo.js：right 顺时针 90°、left 逆时针 90°、down 旋转 180°；* 镜像标签按正向处理）。
func orientByWxTag(img image.Image, tag string) image.Image {
	o := strings.ToLower(strings.TrimSpace(tag))
	switch {
	case strings.HasPrefix(o, "right"):
		return rotate90(img, true)
	case strings.HasPrefix(o, "left"):
		return rotate90(img, false)
	case strings.HasPrefix(o, "down"):
		return rotate180(img)
	default:
		return img
	}
}

// rotate90 顺时针（cw=true）/逆时针 旋转 90°。
func rotate90(img image.Image, cw bool) image.Image {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	dst := image.NewRGBA(image.Rect(0, 0, h, w))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			c := img.At(b.Min.X+x, b.Min.Y+y)
			if cw {
				dst.Set(h-1-y, x, c)
			} else {
				dst.Set(y, w-1-x, c)
			}
		}
	}
	return dst
}

// rotate180 旋转 180°。
func rotate180(img image.Image) image.Image {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			dst.Set(w-1-x, h-1-y, img.At(b.Min.X+x, b.Min.Y+y))
		}
	}
	return dst
}
