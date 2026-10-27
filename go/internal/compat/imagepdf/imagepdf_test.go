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

// buildJPEGWithOrientation 构造带 EXIF Orientation 的 JPEG（在 SOI 后插 APP1）。
func buildJPEGWithOrientation(t *testing.T, base []byte, orientation uint16) []byte {
	t.Helper()
	// TIFF(II)：II*\0 + IFD0 偏移 8；IFD0：1 条目（0x0112 SHORT）+ 下一 IFD 0
	tiff := []byte{'I', 'I', '*', 0, 8, 0, 0, 0}
	entry := make([]byte, 12)
	binary.LittleEndian.PutUint16(entry[0:2], 0x0112)
	binary.LittleEndian.PutUint16(entry[2:4], 3) // SHORT
	binary.LittleEndian.PutUint32(entry[4:8], 1)
	binary.LittleEndian.PutUint16(entry[8:10], orientation)
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
	for _, o := range []uint16{1, 3, 6, 8} {
		data := buildJPEGWithOrientation(t, base, o)
		if got := JPEGOrientation(data); got != int(o) {
			t.Fatalf("orientation = %d，期望 %d", got, o)
		}
	}
	if got := JPEGOrientation([]byte("not a jpeg")); got != 1 {
		t.Fatalf("非 JPEG 应为 1，实际 %d", got)
	}
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
