//go:build linux

package main

import (
	"encoding/binary"
	"image"
	"os/exec"

	"fyne.io/fyne/v2"
)

// captureFrame 直接读取 X11 根窗口，避开部分 Mesa 环境 Canvas.Capture 返回空帧的问题。
func captureFrame(_ fyne.Canvas) image.Image {
	out, err := exec.Command("xwd", "-root", "-silent").Output()
	if err != nil {
		return nil
	}
	return decodeXWD(out)
}

// decodeXWD 解码 Xvfb 常见的 32 位 ZPixmap（头部固定大端，像素字节序由头部位域指定）。
func decodeXWD(raw []byte) image.Image {
	if len(raw) < 100 {
		return nil
	}
	field := func(i int) uint32 { return binary.BigEndian.Uint32(raw[i : i+4]) }
	if field(0) < 100 || field(4) != 7 || field(8) != 2 || field(44) != 32 {
		return nil
	}
	w, h, stride, offset := int(field(16)), int(field(20)), int(field(48)), int(field(0))
	if w <= 0 || h <= 0 || stride < w*4 || offset < 100 || len(raw) < offset+stride*h {
		return nil
	}
	order := field(28)
	if order > 1 {
		return nil
	}
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			i := offset + y*stride + x*4
			p := img.PixOffset(x, y)
			if order == 1 {
				img.Pix[p], img.Pix[p+1], img.Pix[p+2], img.Pix[p+3] = raw[i+2], raw[i+1], raw[i], 0xff
			} else {
				img.Pix[p], img.Pix[p+1], img.Pix[p+2], img.Pix[p+3] = raw[i], raw[i+1], raw[i+2], 0xff
			}
		}
	}
	return img
}
