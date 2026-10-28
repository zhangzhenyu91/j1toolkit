package imagepdf

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
	"os"
	"path/filepath"
	"testing"
)

// ---------- 测试素材 ----------

// genImage 生成渐变测试图。
func genImage(w, h int) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{R: uint8(x * 255 / max(w, 1)), G: uint8(y * 255 / max(h, 1)), B: 128, A: 255})
		}
	}
	return img
}

// genJPEG 生成 JPEG 测试素材。
func genJPEG(t *testing.T, w, h int) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := EncodeJPEG(&buf, genImage(w, h), 80); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// findFontsDir 向上查找 server/assets/fonts（字体只读）。
func findFontsDir(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Skipf("取不到工作目录: %v", err)
	}
	for i := 0; i < 8; i++ {
		cand := filepath.Join(dir, "server", "assets", "fonts")
		if st, err := os.Stat(cand); err == nil && st.IsDir() {
			return cand
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	t.Skip("未找到 server/assets/fonts（不在仓库内运行），跳过字体用例")
	return ""
}

// buildJPEGWithOrientation 构造带 EXIF Orientation 的 JPEG（在 SOI 后插 APP1，小端 TIFF）。
func buildJPEGWithOrientation(t *testing.T, base []byte, orientation uint16) []byte {
	t.Helper()
	return buildJPEGWithOrientationOrder(t, base, orientation, binary.LittleEndian)
}

// buildJPEGWithOrientationOrder 同上，可指定 TIFF 字节序（覆盖 II/MM 两分支）。
func buildJPEGWithOrientationOrder(t *testing.T, base []byte, orientation uint16, order binary.ByteOrder) []byte {
	t.Helper()
	// TIFF：字节序头 + IFD0 偏移 8；IFD0：1 条目（0x0112 SHORT）+ 下一 IFD 0
	tiff := make([]byte, 8, 8+2+12+4)
	if order == binary.BigEndian {
		tiff[0], tiff[1], tiff[2], tiff[3] = 'M', 'M', 0, '*'
	} else {
		tiff[0], tiff[1], tiff[2], tiff[3] = 'I', 'I', '*', 0
	}
	order.PutUint32(tiff[4:8], 8)
	entry := make([]byte, 12)
	order.PutUint16(entry[0:2], 0x0112)
	order.PutUint16(entry[2:4], 3) // SHORT
	order.PutUint32(entry[4:8], 1)
	order.PutUint16(entry[8:10], orientation)
	tiff = append(tiff, 0x01, 0x00)
	tiff = append(tiff, entry...)
	tiff = append(tiff, 0, 0, 0, 0)

	payload := append([]byte("Exif\x00\x00"), tiff...)
	seg := []byte{0xFF, 0xE1, 0, 0}
	binary.BigEndian.PutUint16(seg[2:4], uint16(len(payload)+2))
	seg = append(seg, payload...)

	out := append([]byte{0xFF, 0xD8}, seg...)
	out = append(out, base[2:]...) // 跳过原 SOI
	return out
}

// ---------- 方向处理 ----------

func TestJPEGOrientation(t *testing.T) {
	base := genJPEG(t, 60, 40)
	if got := JPEGOrientation(base); got != 1 {
		t.Fatalf("无 EXIF 应为 1，实际 %d", got)
	}
	for _, o := range []uint16{1, 2, 3, 4, 5, 6, 7, 8} {
		data := buildJPEGWithOrientation(t, base, o)
		if got := JPEGOrientation(data); got != int(o) {
			t.Fatalf("orientation = %d，期望 %d", got, o)
		}
	}
	if got := JPEGOrientation([]byte("not a jpeg")); got != 1 {
		t.Fatalf("非 JPEG 应为 1，实际 %d", got)
	}
}

// TestJPEGOrientationEdge EXIF 解析边界：大端 TIFF、非法取值、截断 APP1、SOS 前无 EXIF。
func TestJPEGOrientationEdge(t *testing.T) {
	base := genJPEG(t, 32, 24)
	// 大端（MM）TIFF 同样可读
	for _, o := range []uint16{3, 7} {
		data := buildJPEGWithOrientationOrder(t, base, o, binary.BigEndian)
		if got := JPEGOrientation(data); got != int(o) {
			t.Fatalf("大端 orientation = %d，期望 %d", got, o)
		}
	}
	// 非法取值按无 EXIF 处理
	for _, bad := range []uint16{0, 9, 0xFFFF} {
		data := buildJPEGWithOrientation(t, base, bad)
		if got := JPEGOrientation(data); got != 1 {
			t.Fatalf("非法 orientation=%d 应为 1，实际 %d", bad, got)
		}
	}
	// 截断 APP1（段长越过数据末尾）→ 1，不得越界 panic
	if got := JPEGOrientation([]byte{0xFF, 0xD8, 0xFF, 0xE1, 0x00, 0x20, 0x45, 0x78}); got != 1 {
		t.Fatalf("截断 APP1 应为 1，实际 %d", got)
	}
	// SOI 后即 SOS → 1
	if got := JPEGOrientation([]byte{0xFF, 0xD8, 0xFF, 0xDA, 0x00, 0x02}); got != 1 {
		t.Fatalf("SOS 前无 EXIF 应为 1，实际 %d", got)
	}
}

// flipHImg 水平镜像（独立基元，与 AutoOrient 的逆向采样互为对照）。
func flipHImg(img image.Image) *image.RGBA {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	out := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			out.Set(w-1-x, y, img.At(b.Min.X+x, b.Min.Y+y))
		}
	}
	return out
}

// flipVImg 垂直镜像。
func flipVImg(img image.Image) *image.RGBA {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	out := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			out.Set(x, h-1-y, img.At(b.Min.X+x, b.Min.Y+y))
		}
	}
	return out
}

// rotCWImg 顺时针 90°：源 (x,y) → 目标 (h-1-y, x)，宽高互换。
func rotCWImg(img image.Image) *image.RGBA {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	out := image.NewRGBA(image.Rect(0, 0, h, w))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			out.Set(h-1-y, x, img.At(b.Min.X+x, b.Min.Y+y))
		}
	}
	return out
}

// rotCCWImg 逆时针 90°：源 (x,y) → 目标 (y, w-1-x)，宽高互换。
func rotCCWImg(img image.Image) *image.RGBA {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	out := image.NewRGBA(image.Rect(0, 0, h, w))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			out.Set(y, w-1-x, img.At(b.Min.X+x, b.Min.Y+y))
		}
	}
	return out
}

// exifUpright 按 EXIF Orientation 语义组合基元变换得到「显示端正」期望图：
// 1 正向 / 2 水平镜像 / 3 旋转 180° / 4 垂直镜像 / 5 镜像水平+逆时针 90°（转置）/
// 6 顺时针 90° / 7 镜像水平+顺时针 90°（反对角线翻转）/ 8 逆时针 90°。
// 对齐 Node @napi-rs/canvas v1.0.3 loadImage 的 EXIF 摆正实测（含镜像 2/4/5/7）。
func exifUpright(img image.Image, o int) *image.RGBA {
	switch o {
	case 2:
		return flipHImg(img)
	case 3:
		return flipVImg(flipHImg(img))
	case 4:
		return flipVImg(img)
	case 5:
		return rotCCWImg(flipHImg(img))
	case 6:
		return rotCWImg(img)
	case 7:
		return rotCWImg(flipHImg(img))
	case 8:
		return rotCCWImg(img)
	default:
		return flipVImg(flipVImg(img)) // 恒等（经两次变换复制，输出归零原点）
	}
}

// uniqImage 每像素唯一不透明色；越界采样得到的透明黑无法混过比对。
func uniqImage(w, h int) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			i := y*w + x
			img.Set(x, y, color.RGBA{R: uint8(i + 1), G: uint8(2*i + 1), B: uint8(3*i + 1), A: 255})
		}
	}
	return img
}

// rgbaAt 归一到 color.RGBA（YCbCr 等经颜色模型取 8 位，与 Set 入图口径一致）。
func rgbaAt(img image.Image, x, y int) color.RGBA {
	return color.RGBAModel.Convert(img.At(x, y)).(color.RGBA)
}

// assertPixelsEqual 逐像素精确比对（含尺寸）；越界采样得到的透明黑单独提示。
func assertPixelsEqual(t *testing.T, got, want image.Image) {
	t.Helper()
	gb, wb := got.Bounds(), want.Bounds()
	if gb.Dx() != wb.Dx() || gb.Dy() != wb.Dy() {
		t.Fatalf("尺寸不符: got=%dx%d want=%dx%d", gb.Dx(), gb.Dy(), wb.Dx(), wb.Dy())
	}
	for y := 0; y < wb.Dy(); y++ {
		for x := 0; x < wb.Dx(); x++ {
			g := rgbaAt(got, gb.Min.X+x, gb.Min.Y+y)
			w := rgbaAt(want, wb.Min.X+x, wb.Min.Y+y)
			if g == w {
				continue
			}
			if g.A == 0 && w.A != 0 {
				t.Fatalf("(%d,%d) got 为透明黑，疑似采样越界", x, y)
			}
			t.Fatalf("(%d,%d) 像素不符: got=%v want=%v", x, y, g, w)
		}
	}
}

// pixelsEqual 逐像素判等（含尺寸）。
func pixelsEqual(a, b image.Image) bool {
	ab, bb := a.Bounds(), b.Bounds()
	if ab.Dx() != bb.Dx() || ab.Dy() != bb.Dy() {
		return false
	}
	for y := 0; y < bb.Dy(); y++ {
		for x := 0; x < bb.Dx(); x++ {
			if rgbaAt(a, ab.Min.X+x, ab.Min.Y+y) != rgbaAt(b, bb.Min.X+x, bb.Min.Y+y) {
				return false
			}
		}
	}
	return true
}

func TestAutoOrient(t *testing.T) {
	img := genImage(60, 40)
	rot := AutoOrient(img, 6) // 顺时针 90°：宽高互换
	if b := rot.Bounds(); b.Dx() != 40 || b.Dy() != 60 {
		t.Fatalf("旋转后尺寸 = %dx%d，期望 40x60", b.Dx(), b.Dy())
	}
	// 原 (0,0) 蓝 128 红 0 → 旋转后 (39, 0)
	r, _, _, _ := rot.At(39, 0).RGBA()
	if r>>8 != 0 {
		t.Fatalf("旋转映射错误: (39,0) 红=%d", r>>8)
	}
	same := AutoOrient(img, 1)
	if same.Bounds() != img.Bounds() {
		t.Fatal("orientation=1 应原样返回")
	}
}

// TestAutoOrientEXIFPixels EXIF 1-8 精确像素映射（重点 7 反对角线翻转）：
// 与 EXIF 语义组合实现（exifUpright）全图逐像素一致。必须含非方图——方图下
// 7 的错误采样公式（w/h 互换）可能侥幸通过；越界采样只会得到透明黑而非 panic。
func TestAutoOrientEXIFPixels(t *testing.T) {
	for _, sz := range []struct{ w, h int }{{4, 2}, {2, 3}, {5, 3}, {3, 5}, {1, 1}, {1, 4}, {4, 1}} {
		src := uniqImage(sz.w, sz.h)
		for o := 1; o <= 8; o++ {
			got := AutoOrient(src, o)
			if o >= 5 {
				if b := got.Bounds(); b.Dx() != sz.h || b.Dy() != sz.w {
					t.Fatalf("%dx%d orientation=%d 应 %dx%d，实际 %v", sz.w, sz.h, o, sz.h, sz.w, got.Bounds())
				}
			} else if b := got.Bounds(); b.Dx() != sz.w || b.Dy() != sz.h {
				t.Fatalf("%dx%d orientation=%d 应 %dx%d，实际 %v", sz.w, sz.h, o, sz.w, sz.h, got.Bounds())
			}
			if want := exifUpright(src, o); !pixelsEqual(got, want) {
				assertPixelsEqual(t, got, want) // 输出首个差异坐标
			}
		}
	}
}

// TestAutoOrientEXIFSubImage 非原点 Bounds（SubImage）：按局部坐标采样，输出归零原点。
func TestAutoOrientEXIFSubImage(t *testing.T) {
	full := uniqImage(5, 4)
	src := full.SubImage(image.Rect(1, 1, 4, 4)).(*image.RGBA) // 3x3，Min=(1,1)
	for o := 1; o <= 8; o++ {
		got := AutoOrient(src, o)
		if o == 1 {
			// 1 原样返回（含原 Bounds），不新建图
			if got.Bounds() != src.Bounds() {
				t.Fatalf("orientation=1 应原样返回，实际 %v", got.Bounds())
			}
		} else if b := got.Bounds(); b.Min != (image.Point{}) {
			t.Fatalf("orientation=%d 输出应归零原点，实际 %v", o, b)
		}
		if want := exifUpright(src, o); !pixelsEqual(got, want) {
			assertPixelsEqual(t, got, want)
		}
	}
}

// TestAutoOrientEXIFCorners 四象限角点表对齐 Node @napi-rs/canvas v1.0.3 loadImage
// 本地实测（4x2 四象限 JPEG 的角点排布逐项一致，含 2/4/5/7 镜像；与 wmadd.TestAutoOrientEXIF 同源）。
func TestAutoOrientEXIFCorners(t *testing.T) {
	// 4x2 四象限：左上红 / 右上绿 / 左下蓝 / 右下白
	red, green, blue, white := color.RGBA{R: 255, A: 255}, color.RGBA{G: 255, A: 255}, color.RGBA{B: 255, A: 255}, color.RGBA{R: 255, G: 255, B: 255, A: 255}
	src := image.NewRGBA(image.Rect(0, 0, 4, 2))
	for x := 0; x < 4; x++ {
		for y := 0; y < 2; y++ {
			switch {
			case y == 0 && x < 2:
				src.Set(x, y, red)
			case y == 0:
				src.Set(x, y, green)
			case x < 2:
				src.Set(x, y, blue)
			default:
				src.Set(x, y, white)
			}
		}
	}
	corner := func(img image.Image, x, y int) color.RGBA {
		r, g, b, a := img.At(x, y).RGBA()
		return color.RGBA{uint8(r >> 8), uint8(g >> 8), uint8(b >> 8), uint8(a >> 8)}
	}
	type quad struct{ tl, tr, bl, br color.RGBA }
	cases := map[int]quad{
		1: {red, green, blue, white},
		2: {green, red, white, blue}, // 水平镜像
		3: {white, blue, green, red}, // 旋转 180°
		4: {blue, white, red, green}, // 垂直镜像
		5: {red, blue, green, white}, // 转置（宽高互换 2x4）
		6: {blue, red, white, green}, // 顺时针 90°
		7: {white, green, blue, red}, // 反对角线翻转（镜像水平 + 顺时针 90°）
		8: {green, white, red, blue}, // 逆时针 90°
	}
	for o, want := range cases {
		got := AutoOrient(src, o)
		b := got.Bounds()
		if o >= 5 {
			if b.Dx() != 2 || b.Dy() != 4 {
				t.Fatalf("orientation=%d 应 2x4，实际 %v", o, b)
			}
		} else if b.Dx() != 4 || b.Dy() != 2 {
			t.Fatalf("orientation=%d 应 4x2，实际 %v", o, b)
		}
		tl := corner(got, b.Min.X, b.Min.Y)
		tr := corner(got, b.Max.X-1, b.Min.Y)
		bl := corner(got, b.Min.X, b.Max.Y-1)
		br := corner(got, b.Max.X-1, b.Max.Y-1)
		if tl != want.tl || tr != want.tr || bl != want.bl || br != want.br {
			t.Fatalf("orientation=%d 四角不符: got=%v,%v,%v,%v want=%v,%v,%v,%v", o, tl, tr, bl, br, want.tl, want.tr, want.bl, want.br)
		}
	}
}

func TestDecodeWithOrientation(t *testing.T) {
	base := genJPEG(t, 60, 40)
	data := buildJPEGWithOrientation(t, base, 6)
	img, o, err := DecodeWithOrientation(data)
	if err != nil {
		t.Fatal(err)
	}
	if o != 6 {
		t.Fatalf("orientation = %d", o)
	}
	if b := img.Bounds(); b.Dx() != 40 || b.Dy() != 60 {
		t.Fatalf("摆正后尺寸 = %dx%d", b.Dx(), b.Dy())
	}
}

// TestDecodeWithOrientationSinglePass 解码摆正恰好一次（1-8 全表，重点 7）：
// 与「同一 JPEG 原样解码后再按 EXIF 语义摆正」逐像素一致（两侧同源解码，有损误差抵消，
// 可精确比对），且 2-8 时明确不等于二次摆正结果。对齐 @napi-rs/canvas loadImage——
// 其解码已按 EXIF 摆正一次，返回图即端正结果，不得再摆正。
func TestDecodeWithOrientationSinglePass(t *testing.T) {
	base := genJPEG(t, 60, 40)
	for o := 1; o <= 8; o++ {
		data := buildJPEGWithOrientation(t, base, uint16(o))
		got, ro, err := DecodeWithOrientation(data)
		if err != nil {
			t.Fatal(err)
		}
		if ro != o {
			t.Fatalf("orientation = %d，期望 %d", ro, o)
		}
		raw, err := DecodeImage(data) // 未摆正
		if err != nil {
			t.Fatal(err)
		}
		want := exifUpright(raw, o)
		assertPixelsEqual(t, got, want)
		if o >= 5 {
			if b := got.Bounds(); b.Dx() != 40 || b.Dy() != 60 {
				t.Fatalf("orientation=%d 摆正后尺寸 = %dx%d", o, b.Dx(), b.Dy())
			}
		} else if b := got.Bounds(); b.Dx() != 60 || b.Dy() != 40 {
			t.Fatalf("orientation=%d 摆正后尺寸 = %dx%d", o, b.Dx(), b.Dy())
		}
		if o != 1 {
			if pixelsEqual(got, exifUpright(want, o)) {
				t.Fatalf("orientation=%d 疑似二次摆正", o)
			}
		}
	}
}

// ---------- 缩放 ----------

func TestResize(t *testing.T) {
	img := genImage(100, 50)
	got := Resize(img, 40, 40)
	if b := got.Bounds(); b.Dx() != 40 || b.Dy() != 20 {
		t.Fatalf("缩放 = %dx%d，期望 40x20", b.Dx(), b.Dy())
	}
	same := Resize(img, 200, 200)
	if same.Bounds() != img.Bounds() {
		t.Fatal("已在范围内应原样返回")
	}
	stretched := ScaleTo(img, 30, 30)
	if b := stretched.Bounds(); b.Dx() != 30 || b.Dy() != 30 {
		t.Fatalf("拉伸 = %dx%d", b.Dx(), b.Dy())
	}
}

// ---------- JPEG / WebP ----------

func TestEncodeDecodeJPEG(t *testing.T) {
	var buf bytes.Buffer
	if err := EncodeJPEG(&buf, genImage(80, 50), 75); err != nil {
		t.Fatal(err)
	}
	if buf.Len() == 0 || ImageFormat(buf.Bytes()) != "jpeg" {
		t.Fatal("JPEG 产物非法")
	}
	img, err := DecodeImage(buf.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	if b := img.Bounds(); b.Dx() != 80 || b.Dy() != 50 {
		t.Fatalf("JPEG 回读尺寸 = %dx%d", b.Dx(), b.Dy())
	}
}

func TestEncodeDecodeWebP(t *testing.T) {
	var buf bytes.Buffer
	if err := EncodeWebP(&buf, genImage(64, 48)); err != nil {
		t.Fatalf("WebP 编码: %v", err)
	}
	out := buf.Bytes()
	if len(out) == 0 || ImageFormat(out) != "webp" {
		t.Fatal("WebP 产物非法")
	}
	img, err := DecodeImage(out)
	if err != nil {
		t.Fatalf("WebP 回读: %v", err)
	}
	if b := img.Bounds(); b.Dx() != 64 || b.Dy() != 48 {
		t.Fatalf("WebP 回读尺寸 = %dx%d", b.Dx(), b.Dy())
	}
}

func TestDecodeImageBad(t *testing.T) {
	if _, err := DecodeImage([]byte("zzz")); err == nil {
		t.Fatal("未知格式应报错")
	}
	if ImageFormat([]byte("zzz")) != "" {
		t.Fatal("未知格式探测应为空")
	}
}

// ---------- 水印 ----------

func TestDrawWatermark(t *testing.T) {
	fonts, err := LoadFontFiles(findFontsDir(t))
	if err != nil {
		t.Fatal(err)
	}
	// 黑底图 + 白字：统计亮像素验证文本落笔
	base := image.NewRGBA(image.Rect(0, 0, 400, 300))
	out, err := DrawWatermark(base, fonts, WatermarkOptions{
		Lines: []WatermarkLine{
			{Text: "工程记录 2026-10-27", Size: 18},
			{Text: "防伪 ", Bold: true, Size: 12},
			{Text: "ABCDEFG 23456789", Mono: true, Size: 12},
		},
		TextColor:  color.White,
		PanelColor: color.RGBA{A: 0}, // 透明背板便于像素断言
	})
	if err != nil {
		t.Fatal(err)
	}
	if out.Bounds() != base.Bounds() {
		t.Fatal("水印不应改变画布尺寸")
	}
	bright := 0
	b := out.Bounds()
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			r, g, bl, _ := out.At(x, y).RGBA()
			if r>>8 > 200 && g>>8 > 200 && bl>>8 > 200 {
				bright++
			}
		}
	}
	if bright < 200 {
		t.Fatalf("文本亮像素 = %d，期望 ≥200（字体/中文/等宽均落笔）", bright)
	}

	// 不透明红背板：验证背板绘制
	out2, err := DrawWatermark(base, fonts, WatermarkOptions{
		Lines:      []WatermarkLine{{Text: "水印", Size: 16}},
		PanelColor: color.RGBA{R: 255, A: 255},
		TextColor:  color.White,
	})
	if err != nil {
		t.Fatal(err)
	}
	red := 0
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			r, g, bl, _ := out2.At(x, y).RGBA()
			if r>>8 > 240 && g>>8 < 20 && bl>>8 < 20 {
				red++
			}
		}
	}
	if red < 100 {
		t.Fatalf("背板红像素 = %d，期望 ≥100", red)
	}

	if _, err := DrawWatermark(base, fonts, WatermarkOptions{}); err == nil {
		t.Fatal("空水印行应报错")
	}
	if _, err := DrawWatermark(base, nil, WatermarkOptions{Lines: []WatermarkLine{{Text: "x"}}}); err == nil {
		t.Fatal("缺字体应报错")
	}
}

// ---------- v2 卡片水印 ----------

// findAssetsDir 向上查找 server/assets（品牌图/字体只读）。
func findAssetsDir(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Skipf("取不到工作目录: %v", err)
	}
	for i := 0; i < 8; i++ {
		cand := filepath.Join(dir, "server", "assets")
		if st, err := os.Stat(cand); err == nil && st.IsDir() {
			return cand
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	t.Skip("未找到 server/assets（不在仓库内运行），跳过品牌素材用例")
	return ""
}

// loadAssetImage 解码品牌素材（png）。
func loadAssetImage(t *testing.T, path string) image.Image {
	t.Helper()
	buf, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读素材 %s: %v", path, err)
	}
	img, err := DecodeImage(buf)
	if err != nil {
		t.Fatalf("解码素材 %s: %v", path, err)
	}
	return img
}

// solidImage 纯色测试图。
func solidImage(w, h int, c color.RGBA) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, c)
		}
	}
	return img
}

// nearRGB 色距容差匹配（分量 0-255）。
func nearRGB(r, g, b uint32, tr, tg, tb, tol int) bool {
	d := func(a uint32, v int) bool {
		diff := int(a) - v
		if diff < 0 {
			diff = -diff
		}
		return diff <= tol
	}
	return d(r, tr) && d(g, tg) && d(b, tb)
}

// closeTo 浮点近似比较。
func closeTo(got, want, tol float64) bool {
	d := got - want
	if d < 0 {
		d = -d
	}
	return d <= tol
}

// bboxOf 统计区域内满足条件像素的包围盒（无命中 ok=false；x1/y1 为开区间）。
func bboxOf(img image.Image, x0, y0, x1, y1 int, match func(r, g, b uint32) bool) (int, int, int, int, bool) {
	b := img.Bounds()
	if x0 < b.Min.X {
		x0 = b.Min.X
	}
	if y0 < b.Min.Y {
		y0 = b.Min.Y
	}
	if x1 > b.Max.X {
		x1 = b.Max.X
	}
	if y1 > b.Max.Y {
		y1 = b.Max.Y
	}
	minX, minY, maxX, maxY := 1<<30, 1<<30, -1, -1
	for y := y0; y < y1; y++ {
		for x := x0; x < x1; x++ {
			r, g, bl, _ := img.At(x, y).RGBA()
			if match(r>>8, g>>8, bl>>8) {
				if x < minX {
					minX = x
				}
				if y < minY {
					minY = y
				}
				if x > maxX {
					maxX = x
				}
				if y > maxY {
					maxY = y
				}
			}
		}
	}
	if maxX < 0 {
		return 0, 0, 0, 0, false
	}
	return minX, minY, maxX + 1, maxY + 1, true
}

// cardColors 卡片合成色（rgba 叠 168 灰底后的目标色）。
var (
	colorHeaderBlue = func(r, g, b uint32) bool { return nearRGB(r, g, b, 99, 146, 225, 18) }
	colorBodyWhite  = func(r, g, b uint32) bool { return nearRGB(r, g, b, 229, 229, 229, 10) }
	colorDotYellow  = func(r, g, b uint32) bool { return r > 200 && g > 140 && g < 235 && b < 140 }
)

// TestDrawWatermarkCard v2 卡片版式断言：蓝头/黄点/卡身/防伪行/品牌 logo 几何
// 对齐 watermark.js 实测常量（图 1200x1600，B=min(W,H)=1200）。
func TestDrawWatermarkCard(t *testing.T) {
	assets := findAssetsDir(t)
	fonts, err := LoadFontFiles(filepath.Join(assets, "fonts"))
	if err != nil {
		t.Fatal(err)
	}
	brand := loadAssetImage(t, filepath.Join(assets, "brand-logo.png"))
	shadow := loadAssetImage(t, filepath.Join(assets, "code-shadow.png"))
	shadowLabel := loadAssetImage(t, filepath.Join(assets, "code-shadow-label.png"))

	const W, H = 1200, 1600
	const B = float64(W) // 短边
	base := solidImage(W, H, color.RGBA{R: 168, G: 168, B: 168, A: 255})
	out, err := DrawWatermarkCard(base, fonts, WatermarkCardParams{
		Content:              "绝缘子清扫",
		Time:                 "2026.10.27 09:00",
		Weather:              "多云 18°C",
		Location:             "尧都区",
		Longitude:            "111.500000°E",
		Latitude:             "36.200000°N",
		AntiCode:             "ABCDEFGHJKLMNP",
		BrandImage:           brand,
		CodeShadowImage:      shadow,
		CodeShadowLabelImage: shadowLabel,
	})
	if err != nil {
		t.Fatal(err)
	}
	if out.Bounds() != base.Bounds() {
		t.Fatal("卡片水印不应改变画布尺寸")
	}

	// 头部蓝块：左缘 cardL·B、宽 cardW·B、高 headerH·B
	hx0, hy0, hx1, hy1, ok := bboxOf(out, 0, 0, W, H, colorHeaderBlue)
	if !ok {
		t.Fatal("未检出头部蓝块")
	}
	if !closeTo(float64(hx0), wmCardL*B, 2.5) || !closeTo(float64(hx1), (wmCardL+wmCardW)*B, 2.5) {
		t.Fatalf("头部蓝块横坐标 = [%d,%d)，期望 ≈ [%.1f,%.1f)", hx0, hx1, wmCardL*B, (wmCardL+wmCardW)*B)
	}
	if !closeTo(float64(hy1-hy0), wmHeaderH*B, 2.5) {
		t.Fatalf("头部蓝块高 = %d，期望 ≈ %.1f", hy1-hy0, wmHeaderH*B)
	}

	// 黄点中心：卡片左缘 + dotCx·B、头部垂直中线
	sx, sy, n := 0.0, 0.0, 0.0
	for y := hy0; y < hy1; y++ {
		for x := hx0; x < hx1; x++ {
			r, g, b, _ := out.At(x, y).RGBA()
			if colorDotYellow(r>>8, g>>8, b>>8) {
				sx += float64(x)
				sy += float64(y)
				n++
			}
		}
	}
	if n == 0 {
		t.Fatal("未检出黄点")
	}
	if !closeTo(sx/n, wmCardL*B+wmDotCx*B, 2) {
		t.Fatalf("黄点中心 x = %.1f，期望 ≈ %.1f", sx/n, wmCardL*B+wmDotCx*B)
	}
	if !closeTo(sy/n, float64(hy0)+wmHeaderH*B/2, 2) {
		t.Fatalf("黄点中心 y = %.1f，期望 ≈ %.1f", sy/n, float64(hy0)+wmHeaderH*B/2)
	}

	// 卡身白块：底边 = H - cardB·B（底边固定、向上生长；仅卡片区，避开右下品牌块）
	_, by0, bx1, by1, ok := bboxOf(out, 0, 0, 760, H, colorBodyWhite)
	if !ok {
		t.Fatal("未检出卡身白块")
	}
	if !closeTo(float64(by1), H-wmCardB*B, 2.5) {
		t.Fatalf("卡身底边 = %d，期望 ≈ %.1f", by1, H-wmCardB*B)
	}
	if !closeTo(float64(by0), float64(hy1), 3) {
		t.Fatalf("卡身顶边 = %d，应紧接头部分界 %d", by0, hy1)
	}
	if !closeTo(float64(bx1), (wmCardL+wmCardW)*B, 2.5) {
		t.Fatalf("卡身右缘 = %d，期望 ≈ %.1f", bx1, (wmCardL+wmCardW)*B)
	}

	// 防伪行白字：右缘 = W - codeRight·B；「防」左缘对齐 logo 中「相」左缘
	cx0, _, cx1, _, ok := bboxOf(out, 1000, 1574, W, H, func(r, g, b uint32) bool {
		return r >= 246 && g >= 246 && b >= 246
	})
	if !ok {
		t.Fatal("未检出防伪行文字")
	}
	if !closeTo(float64(cx1), W-wmCodeRight*B, 4) {
		t.Fatalf("防伪行右缘 = %d，期望 ≈ %.1f", cx1, W-wmCodeRight*B)
	}
	xiangX := W - (wmLogoRight+wmLogoW-wmLogoXiangL*wmLogoW)*B
	if float64(cx0) < xiangX-14 || float64(cx0) > xiangX+10 {
		t.Fatalf("「防」左缘 = %d，期望贴「相」左缘 %.1f（codeBeyondXiang）", cx0, xiangX)
	}

	// 品牌 logo：右缘 = W - logoRight·B（软影可略溢出，容差放宽）
	lx0, _, lx1, _, ok := bboxOf(out, 1000, 1450, W, 1573, func(r, g, b uint32) bool {
		return !nearRGB(r, g, b, 168, 168, 168, 20)
	})
	if !ok {
		t.Fatal("未检出品牌 logo")
	}
	if !closeTo(float64(lx1), W-wmLogoRight*B, 8) {
		t.Fatalf("品牌 logo 右缘 = %d，期望 ≈ %.1f", lx1, W-wmLogoRight*B)
	}
	if !closeTo(float64(lx0), W-(wmLogoRight+wmLogoW)*B, 10) {
		t.Fatalf("品牌 logo 左缘 = %d，期望 ≈ %.1f", lx0, W-(wmLogoRight+wmLogoW)*B)
	}
}

// TestDrawWatermarkCardRows 卡片高度随行数自适应（对齐 watermark.js 行数公式）：
// 天气空整行删除；显式 \n 超 maxLines 截断；长值自动换行各占一行距。
func TestDrawWatermarkCardRows(t *testing.T) {
	assets := findAssetsDir(t)
	fonts, err := LoadFontFiles(filepath.Join(assets, "fonts"))
	if err != nil {
		t.Fatal(err)
	}
	const W, H = 1200, 1600
	const B = float64(W)
	base := solidImage(W, H, color.RGBA{R: 168, G: 168, B: 168, A: 255})

	short := WatermarkCardParams{
		Content: "绝缘子清扫", Time: "2026.10.27 09:00", Weather: "多云 18°C",
		Location: "尧都区", Longitude: "111.500000°E", Latitude: "36.200000°N",
		AntiCode: "ABCDEFGHJKLMNP", HideBrand: true,
	}
	noWeather := short
	noWeather.Weather = ""
	longContent := short
	longContent.Content = "甲\n乙\n丙" // 3 段 → 截断 2 行（maxLines=2）
	longLocation := short
	longLocation.Location = "尧都区·临汾电力高级技工学校" // 超宽 → 自动换 2 行

	// 行数 → 头部顶边（cardH 公式 + 底边固定）
	wantTop := func(lines int) float64 {
		cardH := wmHeaderH*B + wmBodyTopPad*B + wmRowPitch*B*float64(lines-1) + wmGlyphH*B + wmBodyBottomPad*B
		return H - wmCardB*B - cardH
	}
	topOf := func(p WatermarkCardParams) float64 {
		out, err := DrawWatermarkCard(base, fonts, p)
		if err != nil {
			t.Fatal(err)
		}
		_, hy0, _, _, ok := bboxOf(out, 0, 0, W, H, colorHeaderBlue)
		if !ok {
			t.Fatal("未检出头部蓝块")
		}
		return float64(hy0)
	}
	// 6 行（每字段 1 行）
	if top := topOf(short); !closeTo(top, wantTop(6), 3) {
		t.Fatalf("6 行卡片头部顶边 = %.1f，期望 ≈ %.1f", top, wantTop(6))
	}
	// 天气空 → 5 行
	if top := topOf(noWeather); !closeTo(top, wantTop(5), 3) {
		t.Fatalf("天气空行应 5 行卡片：头部顶边 = %.1f，期望 ≈ %.1f", top, wantTop(5))
	}
	// 内容 3 段截 2 行 → 7 行
	if top := topOf(longContent); !closeTo(top, wantTop(7), 3) {
		t.Fatalf("内容截断应 7 行卡片：头部顶边 = %.1f，期望 ≈ %.1f", top, wantTop(7))
	}
	// 长地点自动换行 +1 → 7 行
	if top := topOf(longLocation); !closeTo(top, wantTop(7), 3) {
		t.Fatalf("长值换行应 7 行卡片：头部顶边 = %.1f，期望 ≈ %.1f", top, wantTop(7))
	}
}

// ---------- PDF ----------

// nearlyEqual pt 尺寸近似比较。
func nearlyEqual(a, b float64) bool {
	d := a - b
	if d < 0 {
		d = -d
	}
	return d < 0.5
}

func TestBuildPhotosPDF(t *testing.T) {
	portrait := genJPEG(t, 300, 400)  // 3:4 竖版
	landscape := genJPEG(t, 400, 300) // 4:3 横版（旋转竖放）
	single := genJPEG(t, 320, 240)

	pdf, err := BuildPhotosPDF([]PhotoCard{
		{Photos: [][]byte{portrait, landscape}}, // 1 页（两图并排）
		{Photos: [][]byte{single}},              // 1 页（独占居中）
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(pdf) == 0 || !bytes.HasPrefix(pdf, []byte("%PDF")) {
		t.Fatal("PDF 产物非法")
	}
	n, err := PDFPageCount(pdf)
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("页数 = %d，期望 2", n)
	}
	dims, err := PDFPageDims(pdf)
	if err != nil {
		t.Fatal(err)
	}
	for i, d := range dims {
		if !nearlyEqual(d.Width, 841.89) || !nearlyEqual(d.Height, 595.28) {
			t.Fatalf("第 %d 页尺寸 = %.2f×%.2f，期望 A4 横向", i+1, d.Width, d.Height)
		}
	}
}

func TestBuildPhotosPDFSkipsBroken(t *testing.T) {
	good := genJPEG(t, 300, 400)
	// 一张坏图 + 一张好图 → 坏图跳过、好图独占一页
	pdf, err := BuildPhotosPDF([]PhotoCard{{Photos: [][]byte{[]byte("bad"), good}}})
	if err != nil {
		t.Fatal(err)
	}
	n, err := PDFPageCount(pdf)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("页数 = %d，期望 1（坏图跳过）", n)
	}
	// 全坏 / 空 → 无页可生成
	if _, err := BuildPhotosPDF(nil); err != ErrNoPhotos {
		t.Fatalf("空输入应返回 ErrNoPhotos，实际 %v", err)
	}
	if _, err := BuildPhotosPDF([]PhotoCard{{Photos: [][]byte{[]byte("bad")}}}); err != ErrNoPhotos {
		t.Fatalf("全坏应返回 ErrNoPhotos，实际 %v", err)
	}
}

func TestMergePDFs(t *testing.T) {
	p1, err := BuildPhotosPDF([]PhotoCard{{Photos: [][]byte{genJPEG(t, 300, 400)}}})
	if err != nil {
		t.Fatal(err)
	}
	p2, err := BuildPhotosPDF([]PhotoCard{{Photos: [][]byte{genJPEG(t, 320, 240), genJPEG(t, 300, 400)}}})
	if err != nil {
		t.Fatal(err)
	}
	merged, err := MergePDFs([][]byte{p1, p2})
	if err != nil {
		t.Fatal(err)
	}
	n, err := PDFPageCount(merged)
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("合并页数 = %d，期望 2", n)
	}
	if _, err := MergePDFs(nil); err == nil {
		t.Fatal("空输入应报错")
	}
	if _, err := MergePDFs([][]byte{p1, []byte("bad")}); err == nil {
		t.Fatal("非法 PDF 应报错")
	}
}
