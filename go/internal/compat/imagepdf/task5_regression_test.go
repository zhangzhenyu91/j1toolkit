// Task 5 回归：Gray/CMYK JPEG 必须以正确 PDF 色彩嵌入（灰度声明 DeviceGray、CMYK 转 RGB），
// 不得一律按 DeviceRGB 直嵌 DCT 导致偏色/渲染错误。
package imagepdf

import (
	"bytes"
	"compress/zlib"
	"encoding/base64"
	"image"
	"image/color"
	"image/jpeg"
	"io"
	"strings"
	"testing"
)

// imageDict 取第一个图像 XObject 的字典部分（不含流数据）。
func imageDict(t *testing.T, pdf []byte) string {
	t.Helper()
	idx := bytes.Index(pdf, []byte("/Subtype /Image"))
	if idx < 0 {
		t.Fatal("PDF 中未找到图像对象")
	}
	rest := pdf[idx:]
	end := bytes.Index(rest, []byte("stream"))
	if end < 0 {
		t.Fatal("图像对象缺少 stream")
	}
	return string(rest[:end])
}

// TestGrayJPEGUsesDeviceGray 灰度 JPEG 直嵌 DCT 时必须声明 DeviceGray（1 分量）。
func TestGrayJPEGUsesDeviceGray(t *testing.T) {
	img := image.NewGray(image.Rect(0, 0, 6, 4))
	for y := 0; y < 4; y++ {
		for x := 0; x < 6; x++ {
			img.SetGray(x, y, color.Gray{Y: uint8(x*30 + y*10)})
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 90}); err != nil {
		t.Fatal(err)
	}
	pdf, err := BuildPhotosPDF([]PhotoCard{{Photos: [][]byte{buf.Bytes()}}})
	if err != nil {
		t.Fatalf("生成 PDF: %v", err)
	}
	dict := imageDict(t, pdf)
	if !strings.Contains(dict, "/ColorSpace /DeviceGray") {
		t.Fatalf("灰度 JPEG 应声明 DeviceGray，实际字典: %s", dict)
	}
	if !strings.Contains(dict, "/Filter /DCTDecode") {
		t.Fatalf("灰度 JPEG 可直接 DCT 嵌入，实际字典: %s", dict)
	}
}

// cmykJpegBase64 8x8 CMYK JPEG（左半洋红、右半青；Pillow 生成，带 Adobe APP14 反相标记）。
const cmykJpegBase64 = "/9j/7gAOQWRvYmUAZAAAAAAA/9sAQwABAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEB/8AAFAgACAAIBEMRAE0RAFkRAEsRAP/EAB8AAAEFAQEBAQEBAAAAAAAAAAABAgMEBQYHCAkKC//EALUQAAIBAwMCBAMFBQQEAAABfQECAwAEEQUSITFBBhNRYQcicRQygZGhCCNCscEVUtHwJDNicoIJChYXGBkaJSYnKCkqNDU2Nzg5OkNERUZHSElKU1RVVldYWVpjZGVmZ2hpanN0dXZ3eHl6g4SFhoeIiYqSk5SVlpeYmZqio6Slpqeoqaqys7S1tre4ubrCw8TFxsfIycrS09TV1tfY2drh4uPk5ebn6Onq8fLz9PX29/j5+v/aAA4EQwBNAFkASwAAPwCP/g+c/wCcXX/d7P8A76PUf/BjH/zlF/7sm/8AfuK/v4r+/iv/2Q=="

// TestCMYKJPEGConvertedToRGB CMYK JPEG 不得按 DeviceRGB 直嵌 DCT；应解码转 RGB（Flate）保色。
func TestCMYKJPEGConvertedToRGB(t *testing.T) {
	raw, err := base64.StdEncoding.DecodeString(cmykJpegBase64)
	if err != nil {
		t.Fatal(err)
	}
	img, err := jpeg.Decode(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("CMYK 测试图解码失败: %v", err)
	}
	if _, ok := img.(*image.CMYK); !ok {
		t.Fatalf("测试素材应为 CMYK JPEG，实际 %T", img)
	}
	want := flattenRGB(img)
	pdf, err := BuildPhotosPDF([]PhotoCard{{Photos: [][]byte{raw}}})
	if err != nil {
		t.Fatalf("生成 PDF: %v", err)
	}
	dict := imageDict(t, pdf)
	if strings.Contains(dict, "/DeviceCMYK") || strings.Contains(dict, "/Filter /DCTDecode") {
		t.Fatalf("CMYK JPEG 应转 RGB Flate 嵌入，实际字典: %s", dict)
	}
	if !strings.Contains(dict, "/ColorSpace /DeviceRGB") || !strings.Contains(dict, "/Filter /FlateDecode") {
		t.Fatalf("CMYK 转换后应为 DeviceRGB + FlateDecode，实际字典: %s", dict)
	}
	// 取 Flate 流解压，逐字节比对 Go 解码后的 RGB（Go 已处理 Adobe 反相）。
	start := bytes.Index(pdf, []byte("stream\n"))
	if start < 0 {
		t.Fatal("缺少 stream")
	}
	start += len("stream\n")
	end := bytes.Index(pdf[start:], []byte("\nendstream"))
	if end < 0 {
		t.Fatal("缺少 endstream")
	}
	zr, err := zlib.NewReader(bytes.NewReader(pdf[start : start+end]))
	if err != nil {
		t.Fatalf("Flate 流解析失败: %v", err)
	}
	got, err := io.ReadAll(zr)
	if err != nil {
		t.Fatalf("Flate 解压失败: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("CMYK 转 RGB 色彩不符：got %d 字节 want %d 字节", len(got), len(want))
	}
}
