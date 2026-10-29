// overlay：窗口内容缓冲（RGBA 预乘 alpha）与屏幕帧裁剪（对齐 index.cjs _captureBox 缓冲口径）。
package overlay

import (
	"errors"
	"image"
	"image/color"
	"image/draw"
	"math"
)

// Buffer 窗口内容缓冲：RGBA 预乘 alpha、稠密 W*H*4（与 image.RGBA 同布局，可直接作 font.Drawer 目标）。
type Buffer struct {
	Pix []byte
	W   int
	H   int
}

// NewBuffer 创建全透明缓冲。
func NewBuffer(w, h int) *Buffer {
	if w < 1 {
		w = 1
	}
	if h < 1 {
		h = 1
	}
	return &Buffer{Pix: make([]byte, w*h*4), W: w, H: h}
}

// Image 包装为 image.RGBA（同底层数组，零拷贝）。
func (b *Buffer) Image() *image.RGBA {
	return &image.RGBA{Pix: b.Pix, Stride: b.W * 4, Rect: image.Rect(0, 0, b.W, b.H)}
}

// BGRA 转换为 BGRA 预乘拷贝（平台 blit 用）。
func (b *Buffer) BGRA() []byte {
	out := make([]byte, len(b.Pix))
	for i := 0; i < len(b.Pix); i += 4 {
		out[i] = b.Pix[i+2]
		out[i+1] = b.Pix[i+1]
		out[i+2] = b.Pix[i]
		out[i+3] = b.Pix[i+3]
	}
	return out
}

// FillRect 矩形填充（source-over 混合，c 为直通色 NRGBA）。
func (b *Buffer) FillRect(r Rect, c color.NRGBA) {
	x0, y0 := maxInt(r.X, 0), maxInt(r.Y, 0)
	x1, y1 := minInt(r.X+r.W, b.W), minInt(r.Y+r.H, b.H)
	if x0 >= x1 || y0 >= y1 {
		return
	}
	draw.Draw(b.Image(), image.Rect(x0, y0, x1, y1), image.NewUniform(c), image.Point{}, draw.Over)
}

// StrokeRect 矩形描边（width 为线宽像素）。
func (b *Buffer) StrokeRect(r Rect, width int, c color.NRGBA) {
	if width < 1 {
		width = 1
	}
	b.FillRect(Rect{X: r.X, Y: r.Y, W: r.W, H: width}, c)                                 // 上
	b.FillRect(Rect{X: r.X, Y: r.Y + r.H - width, W: r.W, H: width}, c)                   // 下
	b.FillRect(Rect{X: r.X, Y: r.Y + width, W: width, H: r.H - 2*width}, c)               // 左
	b.FillRect(Rect{X: r.X + r.W - width, Y: r.Y + width, W: width, H: r.H - 2*width}, c) // 右
}

// FillRoundedRect 圆角矩形填充（角部逐像素判定，直角边不做抗锯齿）。
func (b *Buffer) FillRoundedRect(r Rect, radius int, c color.NRGBA) {
	x0, y0 := maxInt(r.X, 0), maxInt(r.Y, 0)
	x1, y1 := minInt(r.X+r.W, b.W), minInt(r.Y+r.H, b.H)
	if x0 >= x1 || y0 >= y1 {
		return
	}
	if radius < 0 {
		radius = 0
	}
	rad := float64(minInt(radius, minInt(r.W, r.H)/2))
	img := b.Image()
	for y := y0; y < y1; y++ {
		for x := x0; x < x1; x++ {
			if !roundedContains(r, rad, x, y) {
				continue
			}
			off := img.PixOffset(x, y)
			blendNRGBA(img.Pix[off:off+4], c)
		}
	}
}

// roundedContains 圆角矩形命中（像素中心判定）。
func roundedContains(r Rect, rad float64, x, y int) bool {
	fx, fy := float64(x)+0.5, float64(y)+0.5
	l, t := float64(r.X), float64(r.Y)
	rt, btm := float64(r.X+r.W), float64(r.Y+r.H)
	if fx >= l+rad && fx <= rt-rad {
		return fy >= t && fy <= btm
	}
	if fy >= t+rad && fy <= btm-rad {
		return fx >= l && fx <= rt
	}
	cx, cy := l+rad, t+rad
	if fx < l+rad && fy < t+rad {
		return (fx-cx)*(fx-cx)+(fy-cy)*(fy-cy) <= rad*rad
	}
	cx = rt - rad
	if fx > rt-rad && fy < t+rad {
		return (fx-cx)*(fx-cx)+(fy-cy)*(fy-cy) <= rad*rad
	}
	cx, cy = l+rad, btm-rad
	if fx < l+rad && fy > btm-rad {
		return (fx-cx)*(fx-cx)+(fy-cy)*(fy-cy) <= rad*rad
	}
	cx = rt - rad
	if fx > rt-rad && fy > btm-rad {
		return (fx-cx)*(fx-cx)+(fy-cy)*(fy-cy) <= rad*rad
	}
	return false
}

// blendNRGBA source-over 把直通色混入预乘 RGBA 像素。
func blendNRGBA(dst []byte, c color.NRGBA) {
	a := uint32(c.A)
	if a == 0 {
		return
	}
	inv := 255 - a
	sr := uint32(c.R) * a / 255
	sg := uint32(c.G) * a / 255
	sb := uint32(c.B) * a / 255
	dst[0] = byte(sr + uint32(dst[0])*inv/255)
	dst[1] = byte(sg + uint32(dst[1])*inv/255)
	dst[2] = byte(sb + uint32(dst[2])*inv/255)
	dst[3] = byte(a + uint32(dst[3])*inv/255)
}

// ThumbSize 缩略图请求尺寸封顶（长边不超过 maxThumb，同比例缩放；对齐 _captureBox 的 MAX_THUMB 处理）。
func ThumbSize(physW, physH, maxThumb int) (int, int) {
	if maxThumb <= 0 || (physW <= maxThumb && physH <= maxThumb) {
		return physW, physH
	}
	k := float64(maxThumb) / float64(maxInt(physW, physH))
	return int(math.Round(float64(physW) * k)), int(math.Round(float64(physH) * k))
}

// ErrCropTooSmall 裁剪区域过小（<16px，无 OCR 价值）。
var ErrCropTooSmall = errors.New("裁剪区域过小")

// CropFrame 从整显示器帧裁出框区域（对齐 _captureBox 裁剪数学，单位换算同口径）：
// 缩略图实际尺寸可能与请求略有出入，换算「缩略图像素 / 物理像素」比例（物理像素 = DIP × Scale），
// 按行手工裁出稠密 BGRA8888（不用平台 crop API：裁剪图缓冲不一定是稠密 w×h×4——
// 行距继承原图，实测约为 3 倍，已踩坑）。
func CropFrame(f *Frame, d Display, box Rect) (*Crop, error) {
	if f == nil || len(f.Pix) < f.Width*f.Height*4 {
		return nil, ErrCropTooSmall
	}
	scale := d.Scale
	if scale <= 0 {
		scale = 1
	}
	physW := float64(d.Bounds.W) * scale
	physH := float64(d.Bounds.H) * scale
	if physW <= 0 || physH <= 0 {
		return nil, ErrCropTooSmall
	}
	kx := float64(f.Width) / physW
	ky := float64(f.Height) / physH
	rx := maxInt(0, int(math.Round(float64(box.X-d.Bounds.X)*scale*kx)))
	ry := maxInt(0, int(math.Round(float64(box.Y-d.Bounds.Y)*scale*ky)))
	rw := minInt(f.Width-rx, int(math.Round(float64(box.W)*scale*kx)))
	rh := minInt(f.Height-ry, int(math.Round(float64(box.H)*scale*ky)))
	if rw < 16 || rh < 16 {
		return nil, ErrCropTooSmall
	}
	out := make([]byte, rw*rh*4)
	stride := f.Width * 4
	for y := 0; y < rh; y++ {
		srcStart := (ry+y)*stride + rx*4
		copy(out[y*rw*4:(y+1)*rw*4], f.Pix[srcStart:srcStart+rw*4])
	}
	return &Crop{Pix: out, Width: rw, Height: rh}, nil
}

// scaleFrameBGRA 最近邻缩放稠密 BGRA 帧（fake/截屏/绘制共用）。
func scaleFrameBGRA(src []byte, sw, sh, dw, dh, displayID int) *Frame {
	out := make([]byte, dw*dh*4)
	for y := 0; y < dh; y++ {
		sy := y * sh / dh
		for x := 0; x < dw; x++ {
			sx := x * sw / dw
			si := (sy*sw + sx) * 4
			di := (y*dw + x) * 4
			copy(out[di:di+4], src[si:si+4])
		}
	}
	return &Frame{Pix: out, Width: dw, Height: dh, DisplayID: displayID}
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
