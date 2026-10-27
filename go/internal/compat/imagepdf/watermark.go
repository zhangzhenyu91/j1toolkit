package imagepdf

import (
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"os"
	"path/filepath"
	"sync"

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
