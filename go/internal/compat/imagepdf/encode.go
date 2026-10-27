package imagepdf

import (
	"bytes"
	"image"
	"image/jpeg"
	"image/png"
	"io"

	"github.com/HugoSmits86/nativewebp"
)

// ImageFormat 魔数探测：'jpeg' | 'png' | 'webp' | ”（未知）。
func ImageFormat(data []byte) string {
	switch {
	case len(data) >= 3 && data[0] == 0xFF && data[1] == 0xD8 && data[2] == 0xFF:
		return "jpeg"
	case len(data) >= 8 && bytes.Equal(data[:8], []byte{0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A}):
		return "png"
	case len(data) >= 12 && bytes.Equal(data[0:4], []byte("RIFF")) && bytes.Equal(data[8:12], []byte("WEBP")):
		return "webp"
	}
	return ""
}

// DecodeImage 解码 JPEG / PNG / WebP（WebP 经纯 Go 解码器，兼容 VP8L alpha 标志）。
func DecodeImage(data []byte) (image.Image, error) {
	switch ImageFormat(data) {
	case "jpeg":
		img, err := jpeg.Decode(bytes.NewReader(data))
		if err != nil {
			return nil, err
		}
		return img, nil
	case "png":
		img, err := png.Decode(bytes.NewReader(data))
		if err != nil {
			return nil, err
		}
		return img, nil
	case "webp":
		img, err := nativewebp.DecodeIgnoreAlphaFlag(bytes.NewReader(data))
		if err != nil {
			return nil, err
		}
		return img, nil
	default:
		return nil, errUnsupportedImage
	}
}

// EncodeJPEG 输出 JPEG（quality 1-95，越界取默认 85）。
func EncodeJPEG(w io.Writer, img image.Image, quality int) error {
	if quality <= 0 || quality > 95 {
		quality = 85
	}
	return jpeg.Encode(w, img, &jpeg.Options{Quality: quality})
}

// EncodeWebP 输出 WebP（纯 Go VP8L 无损编码，无 CGO）。
func EncodeWebP(w io.Writer, img image.Image) error {
	return nativewebp.Encode(w, img, &nativewebp.Options{
		CompressionLevel: nativewebp.DefaultCompression,
	})
}
