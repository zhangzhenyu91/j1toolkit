package ocr

import (
	"fmt"
	"image"
	_ "image/png" // PNG 解码器
	"os"
)

// LoadPNGToBGRA 读取 PNG 并转为密集 BGRA 位图（同 Electron getBitmap 密集布局：
// 每像素 4 字节 B,G,R,A、行连续）。golden PNG 识别与端到端测试用。
func LoadPNGToBGRA(path string) (bgra []byte, width, height int, err error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, 0, 0, err
	}
	defer f.Close()
	img, _, err := image.Decode(f)
	if err != nil {
		return nil, 0, 0, fmt.Errorf("ocr: PNG 解码失败: %w", err)
	}
	return ImageToBGRA(img)
}

// ImageToBGRA image.Image → 密集 BGRA 位图。
func ImageToBGRA(img image.Image) (bgra []byte, width, height int, err error) {
	b := img.Bounds()
	width, height = b.Dx(), b.Dy()
	if width <= 0 || height <= 0 {
		return nil, 0, 0, fmt.Errorf("ocr: 图像尺寸非法 %dx%d", width, height)
	}
	bgra = make([]byte, width*height*4)
	i := 0
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			r, g, bl, _ := img.At(x, y).RGBA() // 16 位预乘，取高字节
			bgra[i] = byte(bl >> 8)
			bgra[i+1] = byte(g >> 8)
			bgra[i+2] = byte(r >> 8)
			bgra[i+3] = 255
			i += 4
		}
	}
	return bgra, width, height, nil
}
