// Package imagepdf 是 Go 全量迁移阶段 0 验证切片：图像与 PDF 兼容能力。
// 对齐 Node 侧口径（utils/photo.js、worklog/watermark.js、photospdf.js、safeday/merge.js、wmadd）：
//   - 图片 EXIF 方向处理与缩放；
//   - 中文/等宽字体文本水印（字体只用 server/assets/fonts 素材）；
//   - JPEG / WebP 输出（WebP 走纯 Go 编码器，无 CGO）；
//   - PDF 合并（pdfcpu）与水印照片排版 PDF（A4 横向双图/页，自建最小 PDF 写出器）。
//
// 优先纯 Go：x/image、nativewebp、pdfcpu 均无 CGO；若换 libwebp/cgo 方案需在部署镜像加 C 工具链。
package imagepdf

import (
	"bytes"
	"encoding/binary"
	"errors"
	"image"
)

// JPEGOrientation 解析 JPEG 的 EXIF Orientation（标签 0x0112，取值 1-8）。
// 无 EXIF / 非 JPEG / 解析失败一律返回 1（正向）。
func JPEGOrientation(data []byte) int {
	if len(data) < 4 || data[0] != 0xFF || data[1] != 0xD8 {
		return 1
	}
	// 顺序扫描段：FFxx [len(2 含自身)] ...
	for i := 2; i+4 <= len(data); {
		if data[i] != 0xFF {
			return 1
		}
		marker := data[i+1]
		if marker == 0xD8 || marker == 0x01 || (marker >= 0xD0 && marker <= 0xD7) {
			i += 2 // 无长度域的标记
			continue
		}
		if marker == 0xDA || marker == 0xD9 {
			return 1 // SOS/EOI 之前未找到即无
		}
		segLen := int(binary.BigEndian.Uint16(data[i+2 : i+4]))
		if segLen < 2 || i+2+segLen > len(data) {
			return 1
		}
		if marker == 0xE1 {
			seg := data[i+4 : i+2+segLen]
			if o := exifOrientation(seg); o != 0 {
				return o
			}
		}
		i += 2 + segLen
	}
	return 1
}

// exifOrientation 解析 APP1 段（"Exif\0\0"+TIFF）中的 IFD0 Orientation。
func exifOrientation(app1 []byte) int {
	if len(app1) < 14 || !bytes.HasPrefix(app1, []byte("Exif\x00\x00")) {
		return 0
	}
	tiff := app1[6:]
	var order binary.ByteOrder
	switch {
	case bytes.HasPrefix(tiff, []byte("II*\x00")):
		order = binary.LittleEndian
	case bytes.HasPrefix(tiff, []byte("MM\x00*")):
		order = binary.BigEndian
	default:
		return 0
	}
	ifdOff := int(order.Uint32(tiff[4:8]))
	if ifdOff+2 > len(tiff) {
		return 0
	}
	n := int(order.Uint16(tiff[ifdOff : ifdOff+2]))
	for i := 0; i < n; i++ {
		e := ifdOff + 2 + i*12
		if e+12 > len(tiff) {
			return 0
		}
		if order.Uint16(tiff[e:e+2]) == 0x0112 { // Orientation
			v := order.Uint16(tiff[e+8 : e+10])
			if v >= 1 && v <= 8 {
				return int(v)
			}
			return 0
		}
	}
	return 0
}

// AutoOrient 按 EXIF orientation（1-8）把像素摆正，返回新图。
func AutoOrient(img image.Image, orientation int) image.Image {
	if img == nil {
		return nil
	}
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	switch orientation {
	case 2, 3, 4, 5, 6, 7, 8:
	default:
		return img
	}
	// 5-8 交换宽高
	outW, outH := w, h
	if orientation >= 5 {
		outW, outH = h, w
	}
	dst := image.NewRGBA(image.Rect(0, 0, outW, outH))
	for oy := 0; oy < outH; oy++ {
		for ox := 0; ox < outW; ox++ {
			var sx, sy int
			switch orientation {
			case 2: // 水平镜像
				sx, sy = w-1-ox, oy
			case 3: // 旋转 180°
				sx, sy = w-1-ox, h-1-oy
			case 4: // 垂直镜像
				sx, sy = ox, h-1-oy
			case 5: // 转置（对角线翻转）
				sx, sy = oy, ox
			case 6: // 顺时针 90°
				sx, sy = oy, h-1-ox
			case 7: // 反对角线翻转
				sx, sy = h-1-oy, w-1-ox
			case 8: // 逆时针 90°
				sx, sy = w-1-oy, ox
			}
			dst.Set(ox, oy, img.At(b.Min.X+sx, b.Min.Y+sy))
		}
	}
	return dst
}

// DecodeWithOrientation 解码图片（jpeg/png/webp）并做 EXIF 方向摆正。
// 返回摆正后的图与原始 orientation（png/webp 恒为 1）。
func DecodeWithOrientation(data []byte) (image.Image, int, error) {
	img, err := DecodeImage(data)
	if err != nil {
		return nil, 0, err
	}
	o := 1
	if ImageFormat(data) == "jpeg" {
		o = JPEGOrientation(data)
	}
	return AutoOrient(img, o), o, nil
}

// errUnsupportedImage 不支持的图片格式。
var errUnsupportedImage = errors.New("imagepdf: 不支持的图片格式")
