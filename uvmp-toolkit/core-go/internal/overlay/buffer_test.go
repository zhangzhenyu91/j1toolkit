// 缓冲断言测试：缩略图封顶、裁剪稠密性/行拷贝/坐标换算（对齐 .cjs _captureBox 已踩坑口径）。
package overlay

import (
	"image/color"
	"testing"
)

// mkFrame 造稠密 BGRA 帧：像素 (x,y) 编码为 (x,y,x+y,255)，便于核对裁剪内容。
func mkFrame(w, h int) *Frame {
	pix := make([]byte, w*h*4)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			i := (y*w + x) * 4
			pix[i] = byte(x)
			pix[i+1] = byte(y)
			pix[i+2] = byte(x + y)
			pix[i+3] = 255
		}
	}
	return &Frame{Pix: pix, Width: w, Height: h, DisplayID: 1}
}

func TestThumbSizeCap(t *testing.T) {
	if w, h := ThumbSize(1000, 800, MaxThumb); w != 1000 || h != 800 {
		t.Errorf("no cap = %d×%d", w, h)
	}
	w, h := ThumbSize(8192, 4096, MaxThumb) // 长边 8192 → 缩到 4096×2048
	if w != 4096 || h != 2048 {
		t.Errorf("capped = %d×%d, want 4096×2048", w, h)
	}
	if w, h := ThumbSize(500, 400, 0); w != 500 || h != 400 {
		t.Errorf("maxThumb=0 should disable cap, got %d×%d", w, h)
	}
}

func TestCropFrameDense(t *testing.T) {
	d := Display{ID: 1, Bounds: Rect{X: 0, Y: 0, W: 1920, H: 1080}, Scale: 1}
	f := mkFrame(1920, 1080)
	box := Rect{X: 80, Y: 100, W: 720, H: 180}
	crop, err := CropFrame(f, d, box)
	if err != nil {
		t.Fatal(err)
	}
	if crop.Width != 720 || crop.Height != 180 {
		t.Fatalf("crop size = %dx%d, want 720x180", crop.Width, crop.Height)
	}
	// 缓冲断言：len == w*h*4 且稠密行拷贝（无行距空洞）
	if len(crop.Pix) != crop.Width*crop.Height*4 {
		t.Fatalf("buffer len = %d, want %d", len(crop.Pix), crop.Width*crop.Height*4)
	}
	for y := 0; y < crop.Height; y += 37 {
		for x := 0; x < crop.Width; x += 41 {
			i := (y*crop.Width + x) * 4
			if crop.Pix[i] != byte(80+x) || crop.Pix[i+1] != byte(100+y) {
				t.Fatalf("pixel (%d,%d) = (%d,%d), want (%d,%d)",
					x, y, crop.Pix[i], crop.Pix[i+1], 80+x, 100+y)
			}
		}
	}
}

func TestCropFrameScaleAndThumb(t *testing.T) {
	// 1.5 倍 DPI + 缩略图 1/2：kx/ky 自适应（物理=DIP×Scale）
	d := Display{ID: 1, Bounds: Rect{X: 100, Y: 50, W: 1280, H: 800}, Scale: 1.5}
	f := mkFrame(960, 600) // 物理 1920×1200 的一半缩略
	box := Rect{X: 100 + 100, Y: 50 + 60, W: 300, H: 120}
	crop, err := CropFrame(f, d, box)
	if err != nil {
		t.Fatal(err)
	}
	// rx=round((200-100)*1.5*0.5)=75（kx=960/(1280*1.5)=0.5）；rw=round(300*1.5*0.5)=225
	if crop.Width != 225 || crop.Height != 90 {
		t.Fatalf("crop = %dx%d, want 225x90", crop.Width, crop.Height)
	}
	if crop.Pix[0] != 75 || crop.Pix[1] != 45 {
		t.Fatalf("origin = (%d,%d), want (75,45)", crop.Pix[0], crop.Pix[1])
	}
}

func TestCropFrameTooSmall(t *testing.T) {
	d := Display{ID: 1, Bounds: Rect{X: 0, Y: 0, W: 1920, H: 1080}, Scale: 1}
	f := mkFrame(1920, 1080)
	if _, err := CropFrame(f, d, Rect{X: 10, Y: 10, W: 8, H: 8}); err != ErrCropTooSmall {
		t.Fatalf("want ErrCropTooSmall, got %v", err)
	}
	if _, err := CropFrame(nil, d, Rect{X: 0, Y: 0, W: 100, H: 100}); err != ErrCropTooSmall {
		t.Fatalf("nil frame want ErrCropTooSmall, got %v", err)
	}
	// 帧尺寸声明超实际缓冲 → 拒绝
	bad := &Frame{Pix: make([]byte, 10), Width: 100, Height: 100}
	if _, err := CropFrame(bad, d, Rect{X: 0, Y: 0, W: 100, H: 100}); err != ErrCropTooSmall {
		t.Fatalf("short buffer want ErrCropTooSmall, got %v", err)
	}
}

func TestBufferBGRAAndBlend(t *testing.T) {
	b := NewBuffer(4, 2)
	if len(b.Pix) != 4*2*4 {
		t.Fatalf("buffer len = %d", len(b.Pix))
	}
	b.FillRect(Rect{X: 0, Y: 0, W: 4, H: 2}, color.NRGBA{R: 255, A: 255})
	bgra := b.BGRA()
	if bgra[0] != 0 || bgra[1] != 0 || bgra[2] != 255 || bgra[3] != 255 {
		t.Fatalf("bgra pixel = %v, want B=0 G=0 R=255 A=255", bgra[:4])
	}
}
