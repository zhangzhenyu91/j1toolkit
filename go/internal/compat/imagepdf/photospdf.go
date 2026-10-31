package imagepdf

import (
	"bytes"
	"errors"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
)

// photospdf.go —— 水印照片 PDF 组合（对齐 worklog/photospdf.js）：
// A4 横向；每张卡片的照片两两一页——两张竖版并排（横版旋转 90° 竖放），
// 同一页只放同一卡片的照片；卡片余最后一张（或仅一张）时独占一页（居中），
// 下一张卡片从新页开始。页边距与内网客户端「派车单·轨迹导出」一致：
// 左 20mm 装订侧，其余 5mm。

const (
	ptPerMM = 2.834645669

	photoPageW = 841.89 // A4 横向（pt）
	photoPageH = 595.28

	photoMarginL  = 20 * ptPerMM // 左 20mm（装订侧）
	photoMarginR  = 5 * ptPerMM
	photoMarginTB = 5 * ptPerMM
	photoGap      = 20.0

	photoContentW = photoPageW - photoMarginL - photoMarginR
	photoCellW    = (photoContentW - photoGap) / 2
	photoCellH    = photoPageH - photoMarginTB*2
)

// ErrNoPhotos 全部照片损坏/为空时无页可生成。
var ErrNoPhotos = errors.New("imagepdf: 无可用照片，未生成 PDF")

// PhotoCard 一张出车卡片的照片组（卡内照片按上传序）。
type PhotoCard struct {
	Photos [][]byte // JPEG（水印渲染产物）或 PNG（按魔数兜底）
}

// loadPhoto 登记照片到文档，返回图像索引与像素尺寸；坏图返回 false（跳过不留空框）。
// JPEG 须整图解码验证后才登记（DCTDecode 直嵌原字节，但上传流水线与商旅图片并不保证
// 整图校验，头合法体损坏的图不得嵌入；仅凭尾部 FFD9 或头解析不算验证），尺寸取解码
// 结果；PNG 需出 RGB 位图，解码后走 flattenRGB。
// JPEG 色彩：灰度直嵌声明 DeviceGray；CMYK（4 分量）解码转 RGB 后 Flate 嵌入（DeviceRGB），
// 不得按 DeviceRGB 直嵌 DCT 造成偏色；其余 3 分量 JPEG 按 DeviceRGB 直嵌。
func loadPhoto(d *pdfDoc, buf []byte) (idx int, w, h int, ok bool) {
	if len(buf) == 0 {
		return 0, 0, 0, false
	}
	switch ImageFormat(buf) {
	case "jpeg":
		img, err := jpeg.Decode(bytes.NewReader(buf))
		if err != nil {
			return 0, 0, 0, false
		}
		b := img.Bounds()
		w, h := b.Dx(), b.Dy()
		switch img.ColorModel() {
		case color.GrayModel:
			return d.addImage(&pdfImage{width: w, height: h, jpegData: buf, colorspace: "DeviceGray"}), w, h, true
		case color.CMYKModel:
			return d.addImage(&pdfImage{width: w, height: h, rgbData: flattenRGB(img)}), w, h, true
		default:
			return d.addImage(&pdfImage{width: w, height: h, jpegData: buf}), w, h, true
		}
	case "png":
		img, err := png.Decode(bytes.NewReader(buf))
		if err != nil {
			return 0, 0, 0, false
		}
		b := img.Bounds()
		w, h := b.Dx(), b.Dy()
		return d.addImage(&pdfImage{width: w, height: h, rgbData: flattenRGB(img)}), w, h, true
	default:
		return 0, 0, 0, false
	}
}

// flattenRGB 图像转 RGB 字节（alpha 展平，与逐像素 At().RGBA() 口径逐字节一致）；
// png.Decode 常见产物（NRGBA/RGBA/Gray）走直读 Pix 快路径，其余回退逐像素。
func flattenRGB(img image.Image) []byte {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	rgb := make([]byte, w*h*3)
	switch im := img.(type) {
	case *image.NRGBA:
		// 复刻 color.NRGBA.RGBA() 预乘公式后取高字节，仅省接口调用开销
		for y := 0; y < h; y++ {
			src := im.Pix[im.PixOffset(b.Min.X, b.Min.Y+y):]
			o := y * w * 3
			for x := 0; x < w; x++ {
				a := uint32(src[x*4+3])
				for c := 0; c < 3; c++ {
					v := uint32(src[x*4+c]) * 0x101
					rgb[o+c] = byte(v * a / 0xff >> 8)
				}
				o += 3
			}
		}
	case *image.RGBA:
		for y := 0; y < h; y++ {
			src := im.Pix[im.PixOffset(b.Min.X, b.Min.Y+y):]
			o := y * w * 3
			for x := 0; x < w; x++ {
				rgb[o] = src[x*4]
				rgb[o+1] = src[x*4+1]
				rgb[o+2] = src[x*4+2]
				o += 3
			}
		}
	case *image.Gray:
		for y := 0; y < h; y++ {
			src := im.Pix[im.PixOffset(b.Min.X, b.Min.Y+y):]
			o := y * w * 3
			for x := 0; x < w; x++ {
				v := src[x]
				rgb[o] = v
				rgb[o+1] = v
				rgb[o+2] = v
				o += 3
			}
		}
	default:
		for y := 0; y < h; y++ {
			for x := 0; x < w; x++ {
				r, g, bl, _ := img.At(b.Min.X+x, b.Min.Y+y).RGBA()
				o := (y*w + x) * 3
				rgb[o] = byte(r >> 8)
				rgb[o+1] = byte(g >> 8)
				rgb[o+2] = byte(bl >> 8) // alpha 展平（白底照片口径）
			}
		}
	}
	return rgb
}

// drawPhoto 把照片放进左/右半页框（cellX 为框左缘），框内居中；
// 横版（宽>高）逆时针 90° 竖放（与 photospdf.js drawPhoto 同几何）。
func drawPhoto(page *pdfPage, imgIdx, w, h int, cellX float64) {
	landscape := w > h
	dw, dh := w, h
	if landscape {
		dw, dh = h, w
	}
	scale := photoCellW / float64(dw)
	if s := photoCellH / float64(dh); s < scale {
		scale = s
	}
	dw2 := float64(dw) * scale
	dh2 := float64(dh) * scale
	x := cellX + (photoCellW-dw2)/2
	y := photoMarginTB + (photoCellH-dh2)/2
	if landscape {
		// 逆时针 90°：像单位方 (u,v) → (x+dw2 − v·dw2, y + u·dh2)
		page.placements = append(page.placements, pdfPlacement{
			imgIdx:  imgIdx,
			matrix6: [6]float64{0, dh2, -dw2, 0, x + dw2, y},
		})
		return
	}
	page.placements = append(page.placements, pdfPlacement{
		imgIdx:  imgIdx,
		matrix6: [6]float64{dw2, 0, 0, dh2, x, y},
	})
}

// BuildPhotosPDF 按卡片序组合水印照片 PDF。全部照片损坏返回 ErrNoPhotos。
func BuildPhotosPDF(cards []PhotoCard) ([]byte, error) {
	d := &pdfDoc{}
	for _, card := range cards {
		photos := card.Photos
		for i := 0; i < len(photos); i += 2 {
			pair := photos[i:]
			if len(pair) > 2 {
				pair = pair[:2]
			}
			type loaded struct {
				idx  int
				w, h int
			}
			var imgs []loaded
			for _, buf := range pair {
				idx, w, h, ok := loadPhoto(d, buf) // 逐张嵌入：坏图跳过
				if ok {
					imgs = append(imgs, loaded{idx, w, h})
				}
			}
			if len(imgs) == 0 {
				continue // 整对嵌入全失败时不新增空白页
			}
			page := d.addPage(photoPageW, photoPageH)
			if len(imgs) == 1 {
				// 独占一页：同尺寸框在内容区内居中
				drawPhoto(page, imgs[0].idx, imgs[0].w, imgs[0].h, photoMarginL+(photoContentW-photoCellW)/2)
			} else {
				drawPhoto(page, imgs[0].idx, imgs[0].w, imgs[0].h, photoMarginL)
				drawPhoto(page, imgs[1].idx, imgs[1].w, imgs[1].h, photoMarginL+photoCellW+photoGap)
			}
		}
	}
	if len(d.pages) == 0 {
		return nil, ErrNoPhotos
	}
	return d.bytes()
}
