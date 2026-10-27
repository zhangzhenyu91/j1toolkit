package imagepdf

import (
	"image"

	xdraw "golang.org/x/image/draw"
)

// Resize 等比缩放到不超过 maxW×maxH（已在范围内或目标非法则原样返回）。
func Resize(img image.Image, maxW, maxH int) image.Image {
	if img == nil || maxW <= 0 || maxH <= 0 {
		return img
	}
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	if w <= maxW && h <= maxH {
		return img
	}
	sw, sh := w, h
	if sw*maxH > sh*maxW {
		sw, sh = maxW, h*maxW/w
	} else {
		sw, sh = w*maxH/h, maxH
	}
	return ScaleTo(img, sw, sh)
}

// ScaleTo 缩放到指定像素尺寸（必要时拉伸）。
func ScaleTo(img image.Image, w, h int) image.Image {
	if img == nil || w <= 0 || h <= 0 {
		return img
	}
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	xdraw.CatmullRom.Scale(dst, dst.Bounds(), img, img.Bounds(), xdraw.Over, nil)
	return dst
}
