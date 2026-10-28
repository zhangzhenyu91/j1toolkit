package imagepdf

import (
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"math"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"golang.org/x/image/font"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/math/fixed"

	"shade/internal/assetpath"
)

// 字体素材为 server/assets/fonts（只读；容器内 /app/server/assets/fonts）三件套：
//   - HYQiHeiX2-65J.ttf  汉仪旗黑 65J（正文中文，本身即中黑）
//   - NotoSansSCBold.otf Noto Sans SC Bold（「防伪」等加粗前缀）
//   - PTMono-Bold.ttf    PT Mono Bold（防伪码等宽码值）
type FontFiles struct {
	Chinese     *opentype.Font // HYQiHeiX2-65J
	ChineseBold *opentype.Font // NotoSansSCBold
	Mono        *opentype.Font // PTMono-Bold
}

// LoadFontFiles 从字体目录加载三件套素材。
func LoadFontFiles(assetsFontsDir string) (*FontFiles, error) {
	load := func(name string) (*opentype.Font, error) {
		data, err := os.ReadFile(filepath.Join(assetsFontsDir, name))
		if err != nil {
			return nil, fmt.Errorf("imagepdf: 读字体 %s: %w", name, err)
		}
		f, err := opentype.Parse(data)
		if err != nil {
			return nil, fmt.Errorf("imagepdf: 解析字体 %s: %w", name, err)
		}
		return f, nil
	}
	var ff FontFiles
	var err error
	if ff.Chinese, err = load("HYQiHeiX2-65J.ttf"); err != nil {
		return nil, err
	}
	if ff.ChineseBold, err = load("NotoSansSCBold.otf"); err != nil {
		return nil, err
	}
	if ff.Mono, err = load("PTMono-Bold.ttf"); err != nil {
		return nil, err
	}
	return &ff, nil
}

// 默认字体目录缓存（字体三件套只读、进程内复用，避免每次渲染重复解析）。
var (
	defaultFontsMu   sync.Mutex
	defaultFontsDir  string
	defaultFonts     *FontFiles
	defaultFontsErr  error
	defaultFontsDone bool
)

// DefaultFontsDir 返回字体素材目录（assetpath 探测 assets/fonts，容器/仓根布局自适应）。
func DefaultFontsDir() string {
	if dir := assetpath.Resolve("fonts"); dir != "" {
		return dir
	}
	return filepath.Join("assets", "fonts")
}

// LoadDefaultFonts 加载默认字体三件套并缓存：目录由 DefaultFontsDir 解析，
// 进程内只读一次（失败也缓存，避免坏环境下反复读盘；测试可用 ResetDefaultFonts 清缓存）。
func LoadDefaultFonts() (*FontFiles, error) {
	defaultFontsMu.Lock()
	defer defaultFontsMu.Unlock()
	if defaultFontsDone {
		return defaultFonts, defaultFontsErr
	}
	defaultFontsDir = DefaultFontsDir()
	defaultFonts, defaultFontsErr = LoadFontFiles(defaultFontsDir)
	defaultFontsDone = true
	return defaultFonts, defaultFontsErr
}

// ResetDefaultFonts 清空默认字体缓存（测试用）。
func ResetDefaultFonts() {
	defaultFontsMu.Lock()
	defer defaultFontsMu.Unlock()
	defaultFontsDir, defaultFonts, defaultFontsErr, defaultFontsDone = "", nil, nil, false
}

// WatermarkLine 水印文本行。
type WatermarkLine struct {
	Text string
	Mono bool    // true → 等宽字体（PTMono）
	Bold bool    // true → 加粗前缀字体（NotoSansSCBold），Mono 优先
	Size float64 // 字号 pt（DPI=72 时即像素）
}

// WatermarkOptions 水印绘制参数（左下角半透明背板 + 文本行）。
type WatermarkOptions struct {
	Lines       []WatermarkLine
	DPI         float64     // 默认 72
	Margin      int         // 距图左/下边距 px，默认 16
	Padding     int         // 背板内边距 px，默认 12
	LineSpacing float64     // 行距系数，默认 1.35
	TextColor   color.Color // 默认白色
	PanelColor  color.Color // 默认黑 55% 不透明
}

// DrawWatermark 把水印文本块画到图上（返回新图，不改原图）。
func DrawWatermark(img image.Image, fonts *FontFiles, opts WatermarkOptions) (image.Image, error) {
	if img == nil {
		return nil, errors.New("imagepdf: 图像为空")
	}
	if fonts == nil || fonts.Chinese == nil || fonts.Mono == nil {
		return nil, errors.New("imagepdf: 字体未加载")
	}
	if len(opts.Lines) == 0 {
		return nil, errors.New("imagepdf: 水印无文本行")
	}
	if opts.DPI <= 0 {
		opts.DPI = 72
	}
	if opts.Margin <= 0 {
		opts.Margin = 16
	}
	if opts.Padding <= 0 {
		opts.Padding = 12
	}
	if opts.LineSpacing <= 0 {
		opts.LineSpacing = 1.35
	}
	if opts.TextColor == nil {
		opts.TextColor = color.White
	}
	if opts.PanelColor == nil {
		opts.PanelColor = color.RGBA{R: 0, G: 0, B: 0, A: 140}
	}

	dst := image.NewRGBA(img.Bounds())
	draw.Draw(dst, dst.Bounds(), img, img.Bounds().Min, draw.Src)

	// 建面并量宽
	faces := make([]font.Face, len(opts.Lines))
	var maxW int
	totalH := 0
	for i, ln := range opts.Lines {
		if ln.Size <= 0 {
			ln.Size = 16
			opts.Lines[i].Size = 16
		}
		ot := fonts.Chinese
		switch {
		case ln.Mono:
			ot = fonts.Mono
		case ln.Bold:
			ot = fonts.ChineseBold
		}
		face, err := opentype.NewFace(ot, &opentype.FaceOptions{
			Size:    ln.Size,
			DPI:     opts.DPI,
			Hinting: font.HintingFull,
		})
		if err != nil {
			return nil, fmt.Errorf("imagepdf: 建字体面: %w", err)
		}
		faces[i] = face
		m := face.Metrics()
		w := font.MeasureString(face, ln.Text).Ceil()
		if w > maxW {
			maxW = w
		}
		totalH += m.Ascent.Ceil() + m.Descent.Ceil()
	}
	if totalH == 0 {
		totalH = int(opts.Lines[0].Size)
	}
	spacing := int(float64(totalH/len(opts.Lines)) * (opts.LineSpacing - 1))
	if spacing < 0 {
		spacing = 0
	}
	panelW := maxW + 2*opts.Padding
	panelH := totalH + spacing*(len(opts.Lines)-1) + 2*opts.Padding

	b := dst.Bounds()
	px := b.Min.X + opts.Margin
	py := b.Max.Y - opts.Margin - panelH // 左下角
	if px+panelW > b.Max.X {
		panelW = b.Max.X - px
	}
	if py < b.Min.Y {
		py = b.Min.Y
	}

	// 半透明背板
	draw.Draw(dst, image.Rect(px, py, px+panelW, py+panelH),
		&image.Uniform{C: opts.PanelColor}, image.Point{}, draw.Over)

	// 文本行（自上而下）
	y := py + opts.Padding
	src := &image.Uniform{C: opts.TextColor}
	for i, ln := range opts.Lines {
		m := faces[i].Metrics()
		d := &font.Drawer{
			Dst:  dst,
			Src:  src,
			Face: faces[i],
			Dot:  fixed.P(px+opts.Padding, y+m.Ascent.Ceil()),
		}
		d.DrawString(ln.Text)
		y += m.Ascent.Ceil() + m.Descent.Ceil() + spacing
	}
	return dst, nil
}

// ================= v2 卡片水印（还原 worklog/watermark.js 实测版式） =================

// WatermarkCardParams v2 卡片水印参数（文本字段 + 品牌块素材），逐项对齐
// server/src/worklog/watermark.js 的 draw() 选项（defaults）与 render-photo.js 入参。
type WatermarkCardParams struct {
	Title     string // 卡片标题，空取「工程记录」
	Content   string // 施工内容
	Time      string // 拍摄时间 YYYY.MM.DD HH:mm，空取当前时间
	Weather   string // 天气，空时整行删除（卡片高度自适应）
	Location  string // 地点
	Longitude string // 经度，如 111.777658°E
	Latitude  string // 纬度，如 37.271637°N
	AntiCode  string // 防伪码，空随机生成 14 位
	MaxLines  int    // 每个值字段最多行数（超出截断加…），≤0 取 2
	HideBrand bool   // true 关闭右下品牌块（对齐 showBrand=false，缺省展示）
	// 品牌块素材（对齐 render-photo.js 自 server/assets 加载的三件；nil 时对应部分不画）：
	BrandImage           image.Image // brand-logo.png「今日水印/相机/真实可验」
	CodeShadowImage      image.Image // code-shadow.png 码值底衬软影条（官方 DA.9.png）
	CodeShadowLabelImage image.Image // code-shadow-label.png 「防伪」专用软影短条
}

// 版式常量：全部为图片短边 B=min(W,H) 的比例（正文内部 x 为卡片宽 cardW 的比例），
// 与 server/src/worklog/watermark.js 的 M / SHADOW9 实测值逐项一致，改动须两端同步。
const (
	wmCardL           = 0.02   // 卡片左边距
	wmCardW           = 0.518  // 卡片宽
	wmCardB           = 0.0213 // 底边距（卡片底到图片底，卡片向上生长）
	wmRadius          = 0.016  // 卡片圆角
	wmHeaderH         = 0.06   // 头部高
	wmDotDia          = 0.0146 // 黄点直径
	wmDotCx           = 0.028  // 黄点中心 x（相对卡片左缘）
	wmTitleFont       = 0.0355 // 标题字号
	wmTitleCy         = 0.0335 // 标题中心 y（相对卡片顶）
	wmTitleDx         = 0.018  // 标题中心 x 相对卡片中心右移（避开左侧黄点）
	wmLabelFont       = 0.033  // 正文字号
	wmLabelX          = 0.0302 // 标签首字 x（相对 cardW）
	wmLabelPitch      = 0.076  // 标签字槽间距（相对 cardW，两字标签第 2 字落第 3 槽）
	wmColonX          = 0.3233 // 冒号 x（相对 cardW）
	wmValueX          = 0.3746 // 值起始 x（相对 cardW）
	wmValuePadR       = 0.033  // 值换行右边界到卡片右缘距离（相对 cardW）
	wmValueScaleX     = 0.95   // 值文字横向缩放
	wmRow0Cy          = 0.0258 // 首行文字中心 y（相对头/身分界线）
	wmRowPitch        = 0.0475 // 行距
	wmBodyTopPad      = 0.0092 // 卡身首行上到分界线距离
	wmGlyphH          = 0.03   // 字形高（卡片高度计算用）
	wmBodyBottomPad   = 0.01   // 末行下到卡片底距离
	wmLogoW           = 0.14   // 品牌 logo 图宽
	wmLogoRight       = 0.01   // logo 右边距
	wmLogoBottom      = 0.025  // logo 底边距
	wmLogoXiangL      = 0.01   // 「相」字左缘在 logo 图内的位置（相对图宽）
	wmCodeBeyondXiang = 0.001  // 「防」左缘超出「相」左缘的量
	wmCodeFont        = 0.0145 // 防伪行基准字号（仅量宽，实际按「相」字对齐等比缩放）
	wmCodeFontScale   = 1.1    // 码值字号相对前缀放大倍数
	wmCodeXScale      = 1.0    // 码值横向压缩
	wmCodeRight       = 0.016  // 防伪行右缘到图片右缘
	wmCodeBaseline    = 0.008  // 防伪码行基线（alphabetic）到图片底
	// 「防伪」与码值可见字形盒的 em 比例（对齐 watermark.js 按 @napi-rs/canvas 实测的兜底常量）。
	wmPreAscEm  = 0.88 // NotoSansSC CJK 上探
	wmPreDescEm = 0.12 // NotoSansSC CJK 下探
	wmCodeAscEm = 0.70 // PT Mono Bold 大写上探；下探恒 0（大写+数字无下伸笔画）
	// 防伪行文字本体阴影（官方 setShadowLayer 同模式：纯黑、零偏移）。
	wmCodeTextShadowAlpha = 0.3
	wmCodeTextShadowBlur  = 3.0 // 文字本体阴影模糊半径（px，基准短边 1200，随 B 缩放）
	// SHADOW9 软影条 9-patch 语义（code-shadow.png 155x26，去 1px 标记边后内容区 153x24；
	// 文字可见字形盒落于 padding box 135x12，左/上/右/下外扩 7/6/10/6 缩放单位）。
	wmShadowPadL     = 7.0
	wmShadowPadT     = 6.0
	wmShadowBoxW     = 135.0
	wmShadowBoxH     = 12.0
	wmShadowContentW = 153.0
	wmShadowContentH = 24.0
	// 「防伪」二字软影短条外放边距（倍字形高）。
	wmLabelShadowPad = 0.15
)

// 卡片/文字颜色（透明度在填充时以浮点 alpha 给出，与 JS rgba() 一致）。
var (
	wmHeaderColor = color.RGBA{R: 69, G: 136, B: 249, A: 255}  // rgba(69,136,249,0.7)
	wmBodyColor   = color.RGBA{R: 255, G: 255, B: 255, A: 255} // rgba(255,255,255,0.7)
	wmDotColor    = color.RGBA{R: 0xFA, G: 0xC4, B: 0x41, A: 255}
	wmTextColor   = color.RGBA{R: 0x1D, G: 0x21, B: 0x29, A: 255}
	wmCodeColor   = color.RGBA{R: 255, G: 255, B: 255, A: 255}
)

// 防伪码字符集：去掉 0/O、1/I 等易混淆字符（与 worklog/watermark.js CODE_CHARS 一致）。
const wmCodeAlphabet = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789"

// wmRandomCode 生成 n 位防伪码（对齐 watermark.js randomCode）。
func wmRandomCode(n int) string {
	if n <= 0 {
		n = 14
	}
	b := make([]byte, n)
	for i := range b {
		b[i] = wmCodeAlphabet[rand.Intn(len(wmCodeAlphabet))]
	}
	return string(b)
}

// wmFontKey 字体面缓存键（kind: 0 正文 HYQiHei / 1 加粗前缀 NotoSansSC / 2 等宽 PTMono）。
type wmFontKey struct {
	kind uint8
	size float64
}

// wmLoadFace 取（或建）字体面；字号即像素（DPI=72，不取整的浮点字号也支持）。
func wmLoadFace(fonts *FontFiles, cache map[wmFontKey]font.Face, kind uint8, px float64) (font.Face, error) {
	key := wmFontKey{kind: kind, size: px}
	if f, ok := cache[key]; ok {
		return f, nil
	}
	var ot *opentype.Font
	switch kind {
	case 1:
		ot = fonts.ChineseBold
	case 2:
		ot = fonts.Mono
	default:
		ot = fonts.Chinese
	}
	if ot == nil {
		return nil, errors.New("imagepdf: 字体未加载")
	}
	f, err := opentype.NewFace(ot, &opentype.FaceOptions{Size: px, DPI: 72, Hinting: font.HintingNone})
	if err != nil {
		return nil, fmt.Errorf("imagepdf: 建字体面: %w", err)
	}
	cache[key] = f
	return f, nil
}

// wmMeasure 文本前进宽度（px，浮点；对齐 canvas measureText.width）。
func wmMeasure(face font.Face, s string) float64 {
	return float64(font.MeasureString(face, s)) / 64
}

// wmMiddleBaseline textBaseline='middle' 的 alphabetic 基线 y（em 方框中线对齐 cy）。
func wmMiddleBaseline(face font.Face, cy float64) float64 {
	m := face.Metrics()
	return cy + (float64(m.Ascent)/64-float64(m.Descent)/64)/2
}

// blendPixel src-over 混合（r/g/b 直通色 0-255，a 0-1；目标按预乘处理，越界忽略）。
func blendPixel(dst *image.RGBA, x, y int, r, g, b, a float64) {
	if a <= 0 || !image.Pt(x, y).In(dst.Bounds()) {
		return
	}
	if a > 1 {
		a = 1
	}
	i := dst.PixOffset(x, y)
	da := float64(dst.Pix[i+3]) / 255
	na := a + da*(1-a)
	if na <= 0 {
		return
	}
	pr, pg, pb := r/255*a, g/255*a, b/255*a
	dr, dg, db := float64(dst.Pix[i])/255, float64(dst.Pix[i+1])/255, float64(dst.Pix[i+2])/255
	dst.Pix[i] = uint8((pr+dr*(1-a))/na*255 + 0.5)
	dst.Pix[i+1] = uint8((pg+dg*(1-a))/na*255 + 0.5)
	dst.Pix[i+2] = uint8((pb+db*(1-a))/na*255 + 0.5)
	dst.Pix[i+3] = uint8(na*255 + 0.5)
}

// roundedRectInside 圆角矩形成员测试（corners 顺序 [左上,右上,右下,左下]，同 JS roundedPath）。
func roundedRectInside(x, y, x0, y0, x1, y1 float64, r [4]float64) bool {
	if x < x0 || x > x1 || y < y0 || y > y1 {
		return false
	}
	if r[0] > 0 && x < x0+r[0] && y < y0+r[0] {
		dx, dy := x-(x0+r[0]), y-(y0+r[0])
		return dx*dx+dy*dy <= r[0]*r[0]
	}
	if r[1] > 0 && x > x1-r[1] && y < y0+r[1] {
		dx, dy := x-(x1-r[1]), y-(y0+r[1])
		return dx*dx+dy*dy <= r[1]*r[1]
	}
	if r[2] > 0 && x > x1-r[2] && y > y1-r[2] {
		dx, dy := x-(x1-r[2]), y-(y1-r[2])
		return dx*dx+dy*dy <= r[2]*r[2]
	}
	if r[3] > 0 && x < x0+r[3] && y > y1-r[3] {
		dx, dy := x-(x0+r[3]), y-(y1-r[3])
		return dx*dx+dy*dy <= r[3]*r[3]
	}
	return true
}

// fillRoundedRect 填充圆角矩形（每角独立半径；4x4 超采样抗锯齿；alpha 0-1）。
func fillRoundedRect(dst *image.RGBA, x0, y0, x1, y1 float64, r [4]float64, alpha float64, col color.RGBA) {
	px0, py0 := int(math.Floor(x0)), int(math.Floor(y0))
	px1, py1 := int(math.Ceil(x1)), int(math.Ceil(y1))
	for y := py0; y < py1; y++ {
		for x := px0; x < px1; x++ {
			inside := 0
			for sy := 0; sy < 4; sy++ {
				for sx := 0; sx < 4; sx++ {
					fx := float64(x) + (float64(sx)+0.5)/4
					fy := float64(y) + (float64(sy)+0.5)/4
					if roundedRectInside(fx, fy, x0, y0, x1, y1, r) {
						inside++
					}
				}
			}
			if inside > 0 {
				blendPixel(dst, x, y, float64(col.R), float64(col.G), float64(col.B), alpha*float64(inside)/16)
			}
		}
	}
}

// fillCircle 实心圆（4x4 超采样抗锯齿；alpha 0-1）。
func fillCircle(dst *image.RGBA, cx, cy, rad, alpha float64, col color.RGBA) {
	px0, py0 := int(math.Floor(cx-rad)), int(math.Floor(cy-rad))
	px1, py1 := int(math.Ceil(cx+rad)), int(math.Ceil(cy+rad))
	for y := py0; y < py1; y++ {
		for x := px0; x < px1; x++ {
			inside := 0
			for sy := 0; sy < 4; sy++ {
				for sx := 0; sx < 4; sx++ {
					dx := float64(x) + (float64(sx)+0.5)/4 - cx
					dy := float64(y) + (float64(sy)+0.5)/4 - cy
					if dx*dx+dy*dy <= rad*rad {
						inside++
					}
				}
			}
			if inside > 0 {
				blendPixel(dst, x, y, float64(col.R), float64(col.G), float64(col.B), alpha*float64(inside)/16)
			}
		}
	}
}

// sampleMaskAlpha 双线性采样字形遮罩 alpha（0-1）；(fx,fy) 为目标坐标系连续坐标。
func sampleMaskAlpha(mask image.Image, maskp image.Point, dr image.Rectangle, fx, fy float64) float64 {
	mx := fx - float64(dr.Min.X) + float64(maskp.X)
	my := fy - float64(dr.Min.Y) + float64(maskp.Y)
	x0 := int(math.Floor(mx))
	y0 := int(math.Floor(my))
	tx := mx - float64(x0)
	ty := my - float64(y0)
	at := func(x, y int) float64 {
		_, _, _, a := mask.At(x, y).RGBA()
		return float64(a) / 65535
	}
	return at(x0, y0)*(1-tx)*(1-ty) + at(x0+1, y0)*tx*(1-ty) +
		at(x0, y0+1)*(1-tx)*ty + at(x0+1, y0+1)*tx*ty
}

// blitGlyphScaled 按 scaleX（围绕 x0）横向压缩绘制单个字形遮罩（双线性重采样）。
func blitGlyphScaled(dst *image.RGBA, dr image.Rectangle, mask image.Image, maskp image.Point, x0, scaleX float64, col color.RGBA) {
	if mask == nil {
		return
	}
	fx0 := x0 + (float64(dr.Min.X)-x0)*scaleX
	fx1 := x0 + (float64(dr.Max.X)-x0)*scaleX
	dx0 := int(math.Floor(fx0))
	dx1 := int(math.Ceil(fx1))
	cr, cg, cb := float64(col.R), float64(col.G), float64(col.B)
	ca := float64(col.A) / 255
	for dy := dr.Min.Y; dy < dr.Max.Y; dy++ {
		for dx := dx0; dx < dx1; dx++ {
			sx := x0 + (float64(dx)+0.5-x0)/scaleX - 0.5
			a := sampleMaskAlpha(mask, maskp, dr, sx, float64(dy)+0.5)
			if a > 0 {
				blendPixel(dst, dx, dy, cr, cg, cb, ca*a)
			}
		}
	}
}

// drawRun 绘制一行文本（x 起点、y 为 alphabetic 基线；scaleX 横向缩放，围绕 x 起点）。
func drawRun(dst *image.RGBA, face font.Face, s string, x, y, scaleX float64, col color.RGBA) {
	if scaleX == 1 {
		d := &font.Drawer{
			Dst:  dst,
			Src:  image.NewUniform(col),
			Face: face,
			Dot:  fixed.Point26_6{X: fixed.Int26_6(x * 64), Y: fixed.Int26_6(y * 64)},
		}
		d.DrawString(s)
		return
	}
	dotX := x
	for _, r := range s {
		dr, mask, maskp, adv, _ := face.Glyph(fixed.Point26_6{X: fixed.Int26_6(dotX * 64), Y: fixed.Int26_6(y * 64)}, r)
		blitGlyphScaled(dst, dr, mask, maskp, x, scaleX, col)
		dotX += float64(adv) / 64
	}
}

// alphaOf 提取图像 alpha 通道。
func alphaOf(src *image.RGBA) *image.Alpha {
	b := src.Bounds()
	out := image.NewAlpha(image.Rect(0, 0, b.Dx(), b.Dy()))
	for y := 0; y < b.Dy(); y++ {
		for x := 0; x < b.Dx(); x++ {
			out.Pix[y*out.Stride+x] = src.Pix[(y+b.Min.Y)*src.Stride+(x+b.Min.X)*4+3]
		}
	}
	return out
}

// gaussBlurAlpha 高斯模糊 alpha 遮罩（可分离核、2σ 截断；sigma=blur/2 对齐 skia 阴影口径）。
func gaussBlurAlpha(m *image.Alpha, sigma float64) {
	if sigma <= 0.25 {
		return
	}
	r := int(math.Ceil(2 * sigma))
	if r < 1 {
		r = 1
	}
	k := make([]float64, 2*r+1)
	var sum float64
	for i := -r; i <= r; i++ {
		w := math.Exp(-float64(i*i) / (2 * sigma * sigma))
		k[i+r] = w
		sum += w
	}
	for i := range k {
		k[i] /= sum
	}
	w, h := m.Rect.Dx(), m.Rect.Dy()
	tmp := make([]float64, w*h)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			var acc float64
			for i := -r; i <= r; i++ {
				xx := x + i
				switch {
				case xx < 0:
					xx = 0
				case xx >= w:
					xx = w - 1
				}
				acc += float64(m.Pix[y*m.Stride+xx]) * k[i+r]
			}
			tmp[y*w+x] = acc
		}
	}
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			var acc float64
			for i := -r; i <= r; i++ {
				yy := y + i
				switch {
				case yy < 0:
					yy = 0
				case yy >= h:
					yy = h - 1
				}
				acc += tmp[yy*w+x] * k[i+r]
			}
			v := int(acc + 0.5)
			if v > 255 {
				v = 255
			}
			m.Pix[y*m.Stride+x] = uint8(v)
		}
	}
}

// compositeAlpha 把 alpha 遮罩按 alphaScale 缩放后以 (r,g,b) 合成到 dst（(x,y) 为遮罩左上角）。
func compositeAlpha(dst *image.RGBA, m *image.Alpha, x, y, r, g, b, alphaScale float64) {
	ix, iy := int(math.Floor(x)), int(math.Floor(y))
	for j := 0; j < m.Rect.Dy(); j++ {
		for i := 0; i < m.Rect.Dx(); i++ {
			a := float64(m.Pix[j*m.Stride+i]) / 255
			if a > 0 {
				blendPixel(dst, ix+i, iy+j, r, g, b, alphaScale*a)
			}
		}
	}
}

// bilinearRGBA 双线性采样（预乘插值后还原直通色）；返回 r/g/b 0-255、a 0-255。
func bilinearRGBA(src image.Image, u, v float64) (r, g, b, a float64) {
	sb := src.Bounds()
	x0 := int(math.Floor(u))
	y0 := int(math.Floor(v))
	tx := u - float64(x0)
	ty := v - float64(y0)
	clamp := func(v, lo, hi int) int {
		if v < lo {
			return lo
		}
		if v > hi {
			return hi
		}
		return v
	}
	var pr, pg, pb, pa float64
	for j := 0; j <= 1; j++ {
		for i := 0; i <= 1; i++ {
			w := (1 - math.Abs(float64(i)-tx)) * (1 - math.Abs(float64(j)-ty))
			if w <= 0 {
				continue
			}
			cr, cg, cb, ca := src.At(clamp(x0+i, sb.Min.X, sb.Max.X-1), clamp(y0+j, sb.Min.Y, sb.Max.Y-1)).RGBA()
			pr += float64(cr) / 65535 * w
			pg += float64(cg) / 65535 * w
			pb += float64(cb) / 65535 * w
			pa += float64(ca) / 65535 * w
		}
	}
	a = pa * 255
	if pa > 0 {
		r, g, b = pr/pa*255, pg/pa*255, pb/pa*255
	}
	return r, g, b, a
}

// drawImageScaledTo 把 src 的 (sx0,sy0,sw,sh) 子矩形双线性缩放到 (x,y,w,h) 并 src-over 合成
// （对齐 canvas drawImage 的拉伸语义）。
func drawImageScaledTo(dst *image.RGBA, src image.Image, sx0, sy0, sw, sh, x, y, w, h float64) {
	if src == nil || sw <= 0 || sh <= 0 || w <= 0 || h <= 0 {
		return
	}
	dx0, dy0 := int(math.Floor(x)), int(math.Floor(y))
	dx1, dy1 := int(math.Ceil(x+w)), int(math.Ceil(y+h))
	for dy := dy0; dy < dy1; dy++ {
		for dx := dx0; dx < dx1; dx++ {
			u := sx0 + (float64(dx)+0.5-x)/w*sw - 0.5
			v := sy0 + (float64(dy)+0.5-y)/h*sh - 0.5
			r, g, b, a := bilinearRGBA(src, u, v)
			if a > 0 {
				blendPixel(dst, dx, dy, r, g, b, a/255)
			}
		}
	}
}

// drawImageShadowed 绘图并带 setShadowLayer 同款阴影（黑色、模糊、纵向偏移，偏移随缩放单位）。
func drawImageShadowed(dst *image.RGBA, src image.Image, sx0, sy0, sw, sh, x, y, w, h, shadowAlpha, blurPx, shadowOffY float64) {
	if shadowAlpha > 0 {
		iw := int(math.Ceil(w)) + 2
		ih := int(math.Ceil(h)) + 2
		if iw > 0 && ih > 0 {
			temp := image.NewRGBA(image.Rect(0, 0, iw, ih))
			drawImageScaledTo(temp, src, sx0, sy0, sw, sh, 0, 0, w, h)
			mask := alphaOf(temp)
			gaussBlurAlpha(mask, blurPx/2)
			compositeAlpha(dst, mask, x, y+shadowOffY, 0, 0, 0, shadowAlpha)
		}
	}
	drawImageScaledTo(dst, src, sx0, sy0, sw, sh, x, y, w, h)
}

// drawTextShadowed 绘制带阴影的文本：先按字形遮罩模糊黑影（setShadowLayer 同模式：
// 纯黑、零偏移），再绘文字本体。asc/desc 为可见字形盒上下探（px）。
func drawTextShadowed(dst *image.RGBA, face font.Face, s string, x, y, scaleX, asc, desc, shadowAlpha, blurPx float64, col color.RGBA) {
	if shadowAlpha > 0 {
		pad := int(math.Ceil(2.5*blurPx)) + 3
		w := int(math.Ceil(wmMeasure(face, s)*scaleX)) + 2*pad
		h := int(math.Ceil(asc+desc)) + 2*pad
		if w > 0 && h > 0 {
			temp := image.NewRGBA(image.Rect(0, 0, w, h))
			boxX := x - float64(pad)
			boxY := y - asc - float64(pad)
			drawRun(temp, face, s, x-boxX, y-boxY, scaleX, color.RGBA{R: 255, G: 255, B: 255, A: 255})
			mask := alphaOf(temp)
			gaussBlurAlpha(mask, blurPx/2)
			compositeAlpha(dst, mask, boxX, boxY, 0, 0, 0, shadowAlpha)
		}
	}
	drawRun(dst, face, s, x, y, scaleX, col)
}

// wmWrapValue 值换行：\n 分段 + 段内贪心换行，超 maxLines 截断加省略号
// （对齐 watermark.js wrapValue：宽度按 valueScaleX 缩放后与 maxValueW 比较）。
func wmWrapValue(face font.Face, text string, maxValueW float64, maxLines int) []string {
	var lines []string
	for _, seg := range strings.Split(text, "\n") {
		cur := ""
		for _, ch := range seg {
			if cur != "" && wmMeasure(face, cur+string(ch))*wmValueScaleX > maxValueW {
				lines = append(lines, cur)
				cur = string(ch)
			} else {
				cur += string(ch)
			}
		}
		lines = append(lines, cur) // 空串也占位（显式空行）
	}
	if len(lines) > maxLines {
		lines = lines[:maxLines]
		last := lines[len(lines)-1]
		for last != "" && wmMeasure(face, last+"…")*wmValueScaleX > maxValueW {
			rs := []rune(last)
			last = string(rs[:len(rs)-1])
		}
		lines[len(lines)-1] = last + "…"
	}
	return lines
}

// drawBrandBlock 右下品牌块：防伪行（「防伪」前缀 + 码值，各自垫软影条 + 文字投影）
// 与品牌 logo（对齐 watermark.js showBrand 段落的量宽/对齐/阴影语义）。
func drawBrandBlock(dst *image.RGBA, fonts *FontFiles, cache map[wmFontKey]font.Face, W, H, B float64, antiCode string, card WatermarkCardParams) error {
	rightX := W - wmCodeRight*B
	baselineY := H - wmCodeBaseline*B
	codePrefix := "防伪 "
	codeFs := wmCodeFont * B

	// 横向：行右缘固定 codeRight 不动；先按基准字号量行宽，再等比缩小至「防」左缘
	// 刚好超过上方 logo 中「相」字左缘（logoXiangL / codeBeyondXiang）。
	prefixFace, err := wmLoadFace(fonts, cache, 1, codeFs)
	if err != nil {
		return err
	}
	codeFace, err := wmLoadFace(fonts, cache, 2, codeFs*wmCodeFontScale)
	if err != nil {
		return err
	}
	prefixW0 := wmMeasure(prefixFace, codePrefix)
	codeW0 := wmMeasure(codeFace, antiCode) * wmCodeXScale
	xiangX := W - (wmLogoRight+wmLogoW-wmLogoXiangL*wmLogoW)*B
	codeFs *= (rightX - xiangX + wmCodeBeyondXiang*B) / (prefixW0 + codeW0)
	// 浮点字号（不取整）：取整会让缩放后行宽偏离目标 1-2px，左缘对不上「相」字
	prefixFace, err = wmLoadFace(fonts, cache, 1, codeFs)
	if err != nil {
		return err
	}
	codeFace, err = wmLoadFace(fonts, cache, 2, codeFs*wmCodeFontScale)
	if err != nil {
		return err
	}

	// 可见字形盒（em 常量对齐 watermark.js 实测兜底值；码值下探恒 0）
	preAsc := wmPreAscEm * codeFs
	preDesc := wmPreDescEm * codeFs
	codeAsc := wmCodeAscEm * codeFs * wmCodeFontScale
	codeDesc := 0.0

	codeW := wmMeasure(codeFace, antiCode) * wmCodeXScale
	prefixW := wmMeasure(prefixFace, codePrefix)
	prefixX := rightX - prefixW - codeW
	// 中线对齐：两段可见字形垂直中心同线（前缀基线按中心残差校正）
	prefixBaselineY := baselineY + (codeDesc-codeAsc-(preDesc-preAsc))/2

	// 底衬软影条：「防伪」与码值各自独立垫一条，互不相连。
	// 码值用整条 DA.9.png 内容区（153x24，去 1px 标记边），按 9-patch 内容留白语义拉伸。
	if img := card.CodeShadowImage; img != nil {
		ssx := codeW / wmShadowBoxW
		ssy := codeAsc / wmShadowBoxH
		drawImageScaledTo(dst, img, 1, 1, wmShadowContentW, wmShadowContentH,
			rightX-codeW-wmShadowPadL*ssx, baselineY-codeAsc-wmShadowPadT*ssy,
			wmShadowContentW*ssx, wmShadowContentH*ssy)
	}
	// 「防伪」二字用裁短拼接的专用短条，字形盒仅外放一圈小边距
	if img := card.CodeShadowLabelImage; img != nil {
		ib := img.Bounds()
		preGlyphW := wmMeasure(prefixFace, "防伪") // 前缀二字宽（不含尾部空格）
		preH := preAsc + preDesc
		prePad := wmLabelShadowPad * preH
		drawImageScaledTo(dst, img, float64(ib.Min.X), float64(ib.Min.Y), float64(ib.Dx()), float64(ib.Dy()),
			prefixX-prePad, prefixBaselineY-preAsc-prePad, preGlyphW+2*prePad, preH+2*prePad)
	}

	// 文字本体阴影（官方 setShadowLayer 同模式）
	blur := wmCodeTextShadowBlur * B / 1200
	drawTextShadowed(dst, prefixFace, codePrefix, prefixX, prefixBaselineY, 1, preAsc, preDesc, wmCodeTextShadowAlpha, blur, wmCodeColor)
	drawTextShadowed(dst, codeFace, antiCode, rightX-codeW, baselineY, wmCodeXScale, codeAsc, codeDesc, wmCodeTextShadowAlpha, blur, wmCodeColor)

	// 品牌 logo 图（阴影沿用原参数，不继承上方防伪行文字的阴影）
	if img := card.BrandImage; img != nil {
		ib := img.Bounds()
		lw := wmLogoW * B
		lh := lw * float64(ib.Dy()) / float64(ib.Dx())
		drawImageShadowed(dst, img, float64(ib.Min.X), float64(ib.Min.Y), float64(ib.Dx()), float64(ib.Dy()),
			W-wmLogoRight*B-lw, H-wmLogoBottom*B-lh, lw, lh, 0.35, 6*B/1200, 2*B/1200)
	}
	return nil
}

// DrawWatermarkCard 在图上绘制 v2 卡片水印（左下卡片 + 右下品牌块），返回新图、不改原图。
// 版式、文本字段、比例缩放、字号、logo 与防伪双软影均还原 worklog/watermark.js v2。
func DrawWatermarkCard(img image.Image, fonts *FontFiles, card WatermarkCardParams) (image.Image, error) {
	if img == nil {
		return nil, errors.New("imagepdf: 图像为空")
	}
	if fonts == nil || fonts.Chinese == nil || fonts.Mono == nil || fonts.ChineseBold == nil {
		return nil, errors.New("imagepdf: 字体未加载")
	}
	title := card.Title
	if title == "" {
		title = "工程记录"
	}
	maxLines := card.MaxLines
	if maxLines <= 0 {
		maxLines = 2
	}
	antiCode := card.AntiCode
	if antiCode == "" {
		antiCode = wmRandomCode(14)
	}
	tm := card.Time
	if tm == "" {
		tm = time.Now().Format("2006.01.02 15:04")
	}

	ib := img.Bounds()
	W, H := float64(ib.Dx()), float64(ib.Dy())
	dst := image.NewRGBA(image.Rect(0, 0, ib.Dx(), ib.Dy()))
	draw.Draw(dst, dst.Bounds(), img, ib.Min, draw.Src)
	cache := map[wmFontKey]font.Face{}

	// 缩放基准：短边（横版照片水印不随宽度放大）
	B := math.Min(W, H)

	/* ================= 布局 ================= */
	cardX := wmCardL * B
	cardW := wmCardW * B
	headerH := wmHeaderH * B
	radius := wmRadius * B
	pitch := wmRowPitch * B
	labelX := cardX + wmLabelX*cardW
	colonX := cardX + wmColonX*cardW
	valueX := cardX + wmValueX*cardW
	slotPitch := wmLabelPitch * cardW
	fs := math.Round(wmLabelFont * B) // font(px) 取整，与 JS 一致
	maxValueW := cardW*(1-wmValuePadR) - wmValueX*cardW

	labelFace, err := wmLoadFace(fonts, cache, 0, fs)
	if err != nil {
		return nil, err
	}

	/* ---- 1) 值换行（\n 分段 + 段内贪心换行）；天气为空时整行删除 ---- */
	type wmRow struct {
		label string
		lines []string
	}
	values := [...]struct{ label, value string }{
		{"施工内容", card.Content},
		{"拍摄时间", tm},
		{"天气", card.Weather},
		{"地点", card.Location},
		{"经度", card.Longitude},
		{"纬度", card.Latitude},
	}
	rows := make([]wmRow, 0, len(values))
	for _, f := range values {
		if f.label == "天气" && strings.TrimSpace(f.value) == "" {
			continue
		}
		rows = append(rows, wmRow{label: f.label, lines: wmWrapValue(labelFace, f.value, maxValueW, maxLines)})
	}
	totalLines := 0
	for _, r := range rows {
		totalLines += len(r.lines)
	}
	if totalLines == 0 {
		totalLines = 1
	}

	/* ---- 2) 卡片高度按行数动态计算，底边固定、向上生长 ---- */
	topPad := wmBodyTopPad * B
	glyphH := wmGlyphH * B
	bottomPad := wmBodyBottomPad * B
	cardH := headerH + topPad + pitch*float64(totalLines-1) + glyphH + bottomPad
	cardY := H - wmCardB*B - cardH
	if cardY < wmCardL*B {
		cardY = wmCardL * B // 极端长文本防顶出画面
	}

	/* ================= 左下角卡片 ================= */
	// 卡身：白色半透明，仅头部分界线以下（下圆角；与头部蓝块互不重叠）
	fillRoundedRect(dst, cardX, cardY+headerH, cardX+cardW, cardY+cardH,
		[4]float64{0, 0, radius, radius}, 0.7, wmBodyColor)
	// 头部：纯色半透明蓝，上圆角
	fillRoundedRect(dst, cardX, cardY, cardX+cardW, cardY+headerH,
		[4]float64{radius, radius, 0, 0}, 0.7, wmHeaderColor)
	// 黄色圆点
	fillCircle(dst, cardX+wmDotCx*B, cardY+headerH/2, wmDotDia*B/2, 1, wmDotColor)

	// 标题（视觉居中，避开左侧黄点）
	titleFace, err := wmLoadFace(fonts, cache, 0, math.Round(wmTitleFont*B))
	if err != nil {
		return nil, err
	}
	tw := wmMeasure(titleFace, title)
	drawRun(dst, titleFace, title, cardX+cardW/2+wmTitleDx*B-tw/2, wmMiddleBaseline(titleFace, cardY+wmTitleCy*B), 1, wmCodeColor)

	/* ================= 正文 =================
	 * 标签逐字落槽分散对齐（4 字标签占 0-3 槽，2 字标签占 0、3 槽），
	 * 冒号固定 colonX 列；值从 valueX 列起（横向 0.95 缩放），续行与值列对齐 */
	splitY := cardY + headerH
	cy := splitY + wmRow0Cy*B
	for _, r := range rows {
		rs := []rune(r.label)
		for c, ch := range rs {
			slot := c
			if len(rs) == 2 {
				slot = c * 3
			}
			drawRun(dst, labelFace, string(ch), labelX+float64(slot)*slotPitch, wmMiddleBaseline(labelFace, cy), 1, wmTextColor)
		}
		drawRun(dst, labelFace, "：", colonX, wmMiddleBaseline(labelFace, cy), 1, wmTextColor)
		for _, line := range r.lines {
			drawRun(dst, labelFace, line, valueX, wmMiddleBaseline(labelFace, cy), wmValueScaleX, wmTextColor)
			cy += pitch
		}
	}

	/* ================= 右下角品牌块 ================= */
	if !card.HideBrand {
		if err := drawBrandBlock(dst, fonts, cache, W, H, B, antiCode, card); err != nil {
			return nil, err
		}
	}
	return dst, nil
}
