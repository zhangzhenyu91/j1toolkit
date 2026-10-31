package netdisk

// 图片缩略图生成（/thumb 网格格子专用）：x/image 解码（jpeg/png/gif/bmp/webp）→ EXIF 方向转正 →
// 等比缩至 ≤384px（fit inside、不放大）→ nativewebp 编码 webp 落出。
// 口径对照 Node sharp：.rotate() + .resize(384,384,{fit:'inside',withoutEnlargement:true}) + .webp()；
// nativewebp 仅支持无损 VP8L（无 quality 参数），输出体积略大于 sharp q78 有损，属纯 Go 依赖取舍。

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"github.com/HugoSmits86/nativewebp"
	_ "golang.org/x/image/bmp" // 注册解码器
	"golang.org/x/image/draw"
	_ "golang.org/x/image/webp" // 注册解码器
	"image"
	_ "image/gif"  // 注册解码器（缩略图输入格式）
	_ "image/jpeg" // 注册解码器
	_ "image/png"  // 注册解码器
	"net/http"
	"os"
	"path/filepath"
)

// WebpThumbnailer 基于 x/image + nativewebp 的缩略图生成器（实现 Thumbnailer）。
type WebpThumbnailer struct {
	// MaxSide 输出长边上限（≤0 取默认 384）。
	MaxSide int
	// MaxPixels 输入像素上限（≤0 取默认 268402689，对齐 sharp limitInputPixels）；
	// 在 DecodeConfig 阶段拒绝解码炸弹，不进入完整解码。
	MaxPixels int
}

// ErrThumbTooLarge 图片像素超出缩略图预算（显式错误，路由映射 413）。
var ErrThumbTooLarge = errors.New("图片尺寸过大，无法生成缩略图")

// defaultThumbMaxPixels 默认像素上限（sharp 默认 limitInputPixels = 268402689）。
const defaultThumbMaxPixels = 268402689

// NewWebpThumbnailer 构造缩略图生成器（main 装配：m.Thumb = netdisk.NewWebpThumbnailer()）。
func NewWebpThumbnailer() *WebpThumbnailer {
	return &WebpThumbnailer{MaxSide: 384}
}

// MakeThumb 解码图片 → EXIF 转正 → 等比缩至 ≤MaxSide → 无损 webp 输出。
// 不支持/损坏的图片返回错误（路由层 relayFail，前端回退类型图标）。
func (t *WebpThumbnailer) MakeThumb(src []byte) ([]byte, error) {
	if len(src) == 0 {
		return nil, errors.New("图片内容为空")
	}
	maxPixels := t.MaxPixels
	if maxPixels <= 0 {
		maxPixels = defaultThumbMaxPixels
	}
	// 先 DecodeConfig 校验像素预算：解码炸弹在完整解码前拒绝（不分配像素内存）。
	cfg, _, err := image.DecodeConfig(bytes.NewReader(src))
	if err != nil {
		return nil, fmt.Errorf("图片解析失败: %w", err)
	}
	if cfg.Width <= 0 || cfg.Height <= 0 || int64(cfg.Width)*int64(cfg.Height) > int64(maxPixels) {
		return nil, fmt.Errorf("%w（%dx%d > %d 像素）", ErrThumbTooLarge, cfg.Width, cfg.Height, maxPixels)
	}
	img, format, err := image.Decode(bytes.NewReader(src))
	if err != nil {
		return nil, fmt.Errorf("图片解析失败: %w", err)
	}
	if format == "jpeg" {
		img = applyOrientation(img, jpegOrientation(src))
	}
	max := t.MaxSide
	if max <= 0 {
		max = 384
	}
	img = fitInside(img, max)
	var buf bytes.Buffer
	if err := nativewebp.Encode(&buf, img, &nativewebp.Options{CompressionLevel: nativewebp.DefaultCompression}); err != nil {
		return nil, fmt.Errorf("webp 编码失败: %w", err)
	}
	return buf.Bytes(), nil
}

// fitInside 等比缩至长边 ≤max（不放大）；无需缩放时原图返回。
func fitInside(img image.Image, max int) image.Image {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	if w <= 0 || h <= 0 {
		return img
	}
	if w <= max && h <= max {
		return img
	}
	scale := float64(max) / float64(w)
	if s := float64(max) / float64(h); s < scale {
		scale = s
	}
	dw, dh := int(float64(w)*scale+0.5), int(float64(h)*scale+0.5)
	if dw < 1 {
		dw = 1
	}
	if dh < 1 {
		dh = 1
	}
	dst := image.NewNRGBA(image.Rect(0, 0, dw, dh))
	draw.CatmullRom.Scale(dst, dst.Bounds(), img, b, draw.Src, nil)
	return dst
}

// applyOrientation 按 EXIF Orientation（1-8）转正；1/未知原样返回。
// 映射同 JPEG EXIF 规范：2 水平镜像 / 3 旋转180 / 4 垂直镜像 / 5 转置 /
// 6 顺时针90 / 7 转置+旋转180 / 7 对角翻转 / 8 逆时针90。
func applyOrientation(img image.Image, o int) image.Image {
	if o <= 1 || o > 8 {
		return img
	}
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	var out *image.NRGBA
	if o >= 5 {
		out = image.NewNRGBA(image.Rect(0, 0, h, w))
	} else {
		out = image.NewNRGBA(image.Rect(0, 0, w, h))
	}
	for y := 0; y < out.Bounds().Dy(); y++ {
		for x := 0; x < out.Bounds().Dx(); x++ {
			var sx, sy int
			switch o {
			case 2: // 水平镜像
				sx, sy = w-1-x, y
			case 3: // 旋转 180
				sx, sy = w-1-x, h-1-y
			case 4: // 垂直镜像
				sx, sy = x, h-1-y
			case 5: // 转置（水平镜像 + 逆时针90）
				sx, sy = y, x
			case 6: // 顺时针 90
				sx, sy = y, h-1-x
			case 7: // 对角翻转（水平镜像 + 顺时针90）
				sx, sy = h-1-y, w-1-x
			case 8: // 逆时针 90
				sx, sy = h-1-y, x
			}
			out.Set(x, y, img.At(b.Min.X+sx, b.Min.Y+sy))
		}
	}
	return out
}

// jpegOrientation 解析 JPEG EXIF Orientation（1-8）；无 EXIF/解析失败返回 1。
// 仅读 IFD0 的 0x0112 标签（手机照片转正够用；其余 EXIF 一律忽略）。
func jpegOrientation(src []byte) int {
	if len(src) < 4 || src[0] != 0xFF || src[1] != 0xD8 {
		return 1
	}
	for i := 2; i+4 <= len(src); {
		if src[i] != 0xFF {
			return 1
		}
		marker := src[i+1]
		switch marker {
		case 0xD8, 0x01: // 无长度段
			i += 2
			continue
		case 0xDA, 0xD9: // SOS / EOI：其后为熵编码数据
			return 1
		}
		if marker == 0x00 || marker == 0xFF {
			i++
			continue
		}
		segLen := int(src[i+2])<<8 | int(src[i+3])
		if segLen < 2 || i+2+segLen > len(src) {
			return 1
		}
		if marker == 0xE1 { // APP1
			seg := src[i+4 : i+2+segLen]
			if o := exifOrientation(seg); o > 1 {
				return o
			}
		}
		i += 2 + segLen
	}
	return 1
}

// exifOrientation 解析 APP1 载荷（"Exif\0\0" + TIFF）中的 Orientation。
func exifOrientation(seg []byte) int {
	if len(seg) < 14 || string(seg[:6]) != "Exif\x00\x00" {
		return 1
	}
	tiff := seg[6:]
	var order binary.ByteOrder
	switch string(tiff[:2]) {
	case "II":
		order = binary.LittleEndian
	case "MM":
		order = binary.BigEndian
	default:
		return 1
	}
	if order.Uint16(tiff[2:4]) != 42 {
		return 1
	}
	ifdOff := int(order.Uint32(tiff[4:8]))
	if ifdOff+2 > len(tiff) {
		return 1
	}
	n := int(order.Uint16(tiff[ifdOff : ifdOff+2]))
	for i := 0; i < n; i++ {
		e := ifdOff + 2 + i*12
		if e+12 > len(tiff) {
			return 1
		}
		if order.Uint16(tiff[e:e+2]) != 0x0112 { // Orientation
			continue
		}
		if order.Uint16(tiff[e+2:e+4]) != 3 { // SHORT
			return 1
		}
		v := int(order.Uint16(tiff[e+8 : e+10]))
		if v >= 1 && v <= 8 {
			return v
		}
		return 1
	}
	return 1
}

// thumbCacheFile 缩略图缓存路径（ThumbDir 未配置或未命中返回 ok=false）。
func (m *Module) thumbCacheFile(key string) (string, bool) {
	if m.ThumbDir == "" {
		return "", false
	}
	p := filepath.Join(m.ThumbDir, key+".webp")
	if st, err := os.Stat(p); err == nil && st.Mode().IsRegular() {
		return p, true
	}
	return "", false
}

// sendThumbCache 直出缓存文件（缩略图小，全量读入简单可靠）。
func sendThumbCache(w http.ResponseWriter, file string) {
	data, err := os.ReadFile(file)
	if err != nil {
		return
	}
	w.Header().Set("Content-Type", "image/webp")
	w.Header().Set("Cache-Control", "private, max-age=86400")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}

// storeThumbCache 落盘缓存（tmp + rename 原子写，防并发读到半成品）；失败忽略。
func (m *Module) storeThumbCache(key string, webp []byte) {
	if m.ThumbDir == "" {
		return
	}
	if err := os.MkdirAll(m.ThumbDir, 0o755); err != nil {
		return
	}
	final := filepath.Join(m.ThumbDir, key+".webp")
	tmp := fmt.Sprintf("%s.%d.tmp", final, os.Getpid())
	if os.WriteFile(tmp, webp, 0o644) != nil {
		return
	}
	_ = os.Rename(tmp, final)
}
