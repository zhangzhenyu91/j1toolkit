package ocr

import (
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"testing"
)

// syntheticBGRA jsGoldenFull 的合成输入图（400x200 BGRA 渐变，同 gen.mjs 全管线用例）。
func syntheticBGRA() []byte {
	const w, h = 400, 200
	buf := make([]byte, w*h*4)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			i := (y*w + x) * 4
			buf[i] = byte(x & 0xff)         // B
			buf[i+1] = byte(y & 0xff)       // G
			buf[i+2] = byte((x + y) & 0xff) // R
			buf[i+3] = 255
		}
	}
	return buf
}

// TestPipelineGolden JS 金标对拍：打桩全管线（比例/后处理/裁剪/CTC/坐标映射逐值一致）。
func TestPipelineGolden(t *testing.T) {
	det := &fakeDet{}
	rec := &fakeRec{}
	// gen.mjs 打桩管线的 p.chars = ['blank','A','B','C',' ','D']（rec 输出 C=6 同构）。
	chars := []string{"blank", "A", "B", "C", " ", "D"}
	p, err := NewPipeline(det, rec, chars)
	if err != nil {
		t.Fatalf("NewPipeline：%v", err)
	}
	res, err := p.Run(syntheticBGRA(), jsGoldenFull.W, jsGoldenFull.H)
	if err != nil {
		t.Fatalf("Run：%v", err)
	}
	if res.Text != jsGoldenFull.Text {
		t.Fatalf("text = %q，want %q", res.Text, jsGoldenFull.Text)
	}
	if len(res.Lines) != len(jsGoldenFull.Lines) {
		t.Fatalf("行数 = %d，want %d（%+v）", len(res.Lines), len(jsGoldenFull.Lines), res.Lines)
	}
	for i := range res.Lines {
		got, want := res.Lines[i], jsGoldenFull.Lines[i]
		if got.Text != want.Text || got.Box != want.Box {
			t.Fatalf("lines[%d] 不符：got %+v want %+v", i, got, want)
		}
		if diff := got.Score - want.Score; diff > 1e-12 || diff < -1e-12 {
			t.Fatalf("lines[%d].score = %.17g，want %.17g", i, got.Score, want.Score)
		}
	}
}

// TestPipelineGoldenPNG golden PNG 识别：合成图 → PNG 编码 → 解码 BGRA → 全管线。
func TestPipelineGoldenPNG(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "golden.png")
	writeSyntheticPNG(t, path)
	bgra, w, h, err := LoadPNGToBGRA(path)
	if err != nil {
		t.Fatalf("LoadPNGToBGRA：%v", err)
	}
	if w != jsGoldenFull.W || h != jsGoldenFull.H {
		t.Fatalf("PNG 尺寸 = %dx%d，want %dx%d", w, h, jsGoldenFull.W, jsGoldenFull.H)
	}
	// 解码 BGRA 必须与合成缓冲逐字节一致。
	want := syntheticBGRA()
	for i := range want {
		if bgra[i] != want[i] {
			t.Fatalf("BGRA[%d] 不符：got %d want %d", i, bgra[i], want[i])
		}
	}
	chars := []string{"blank", "A", "B", "C", " ", "D"}
	p, err := NewPipeline(&fakeDet{}, &fakeRec{}, chars)
	if err != nil {
		t.Fatalf("NewPipeline：%v", err)
	}
	res, err := p.Run(bgra, w, h)
	if err != nil {
		t.Fatalf("Run：%v", err)
	}
	if res.Text != jsGoldenFull.Text {
		t.Fatalf("golden PNG text = %q，want %q", res.Text, jsGoldenFull.Text)
	}
}

// TestPipelineErrors 会话错误与非法输入的错误路径。
func TestPipelineErrors(t *testing.T) {
	chars := []string{"blank", "A"}
	if _, err := NewPipeline(nil, &fakeRec{}, chars); err == nil {
		t.Fatal("nil det 应报错")
	}
	if _, err := NewPipeline(&fakeDet{}, &fakeRec{}, nil); err == nil {
		t.Fatal("空词表应报错")
	}
	p, err := NewPipeline(&fakeErrDet{err: errFakeSession}, &fakeRec{}, chars)
	if err != nil {
		t.Fatalf("NewPipeline：%v", err)
	}
	if _, err := p.Run(syntheticBGRA(), 400, 200); err == nil {
		t.Fatal("det 错误应向上返回")
	}
	p2, _ := NewPipeline(&fakeDet{}, &fakeRec{}, chars)
	if _, err := p2.Run(nil, 400, 200); err == nil {
		t.Fatal("缓冲不足应报错")
	}
}

// writeSyntheticPNG 将合成图（同 syntheticBGRA 的像素）写为 PNG。
func writeSyntheticPNG(t *testing.T, path string) {
	t.Helper()
	const w, h = 400, 200
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.SetNRGBA(x, y, color.NRGBA{
				R: byte((x + y) & 0xff),
				G: byte(y & 0xff),
				B: byte(x & 0xff),
				A: 255,
			})
		}
	}
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("创建 PNG：%v", err)
	}
	defer f.Close()
	if err := png.Encode(f, img); err != nil {
		t.Fatalf("PNG 编码：%v", err)
	}
}
