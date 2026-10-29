// 设置与几何测试：默认值/深合并/钳制/结果窗几何回退（纯函数 + 临时目录）。
package quizsearch

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadSettingsDefaults(t *testing.T) {
	wa := WorkArea{X: 0, Y: 0, W: 1920, H: 1040}
	s := LoadSettings(filepath.Join(t.TempDir(), "settings.json"), wa)
	d := DefaultSettings()
	if s.Interval != 1000 || s.Threshold != 0.6 || s.Opacity != 0.9 {
		t.Errorf("scalars = %+v", s)
	}
	if s.Boxes.A != (BoxGeom{X: 80, Y: 100, W: 720, H: 180, Visible: true}) {
		t.Errorf("box A = %+v", s.Boxes.A)
	}
	// B.y=-1 → 贴工作区底部（wa.y+wa.height-h-120）
	if s.Boxes.B.Y != 0+1040-180-120 {
		t.Errorf("box B.y = %d, want %d", s.Boxes.B.Y, 0+1040-180-120)
	}
	if s.Result != (ResultGeom{X: -1, Y: -1, W: 380, H: 260}) {
		t.Errorf("result = %+v", s.Result)
	}
	if d.Boxes.B.Y != -1 {
		t.Errorf("default B.y should stay -1 in struct, got %d", d.Boxes.B.Y)
	}
}

func TestLoadSettingsMerge(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	if err := os.WriteFile(path, []byte(`{"threshold":0.8,"boxes":{"A":{"x":10,"visible":false}}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	s := LoadSettings(path, WorkArea{X: 0, Y: 0, W: 1920, H: 1080})
	if s.Threshold != 0.8 {
		t.Errorf("threshold = %v", s.Threshold)
	}
	if s.Boxes.A.X != 10 || s.Boxes.A.Visible || s.Boxes.A.W != 720 {
		t.Errorf("box A merged = %+v", s.Boxes.A)
	}
	if s.Interval != 1000 {
		t.Errorf("interval default lost: %d", s.Interval)
	}
}

func TestLoadSettingsCorruptFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	if err := os.WriteFile(path, []byte("{broken"), 0o644); err != nil {
		t.Fatal(err)
	}
	s := LoadSettings(path, WorkArea{X: 0, Y: 0, W: 800, H: 600})
	if s.Threshold != 0.6 {
		t.Errorf("corrupt file should fall back to defaults, got %+v", s)
	}
}

func TestSaveSettingsRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	s := DefaultSettings()
	s.Threshold = 0.75
	s.Boxes.A.X = 123
	SaveSettings(path, s)
	got := LoadSettings(path, WorkArea{X: 0, Y: 0, W: 1920, H: 1080})
	if got.Threshold != 0.75 || got.Boxes.A.X != 123 {
		t.Errorf("round trip = %+v", got)
	}
}

func TestClamps(t *testing.T) {
	if ClampInterval(100) != 500 || ClampInterval(9999) != 3000 || ClampInterval(1500) != 1500 {
		t.Error("ClampInterval")
	}
	if ClampThreshold(0.1) != 0.3 || ClampThreshold(0.95) != 0.9 || ClampThreshold(0.5) != 0.5 {
		t.Error("ClampThreshold")
	}
	if ClampOpacity(0.1) != 0.3 || ClampOpacity(2) != 1 || ClampOpacity(0.9) != 0.9 {
		t.Error("ClampOpacity")
	}
}

func TestResolveResultGeometry(t *testing.T) {
	wa := WorkArea{X: 0, Y: 0, W: 1920, H: 1040}
	// 默认右下角（边距 16）
	g := ResolveResultGeometry(ResultGeom{X: -1, Y: -1, W: 380, H: 260}, wa)
	if g.X != 1920-380-16 || g.Y != 1040-260-16 || g.W != 380 || g.H != 260 {
		t.Errorf("default = %+v", g)
	}
	// 用户拖过的持久化坐标
	g = ResolveResultGeometry(ResultGeom{X: 300, Y: 200, W: 400, H: 300}, wa)
	if g.X != 300 || g.Y != 200 || g.W != 400 || g.H != 300 {
		t.Errorf("saved = %+v", g)
	}
	// 越出工作区（留 40px 余量）回退默认
	g = ResolveResultGeometry(ResultGeom{X: 1900, Y: 200, W: 400, H: 300}, wa)
	if g.X != 1920-400-16 {
		t.Errorf("out-of-area fallback = %+v", g)
	}
	// 最小尺寸与工作区封顶
	g = ResolveResultGeometry(ResultGeom{X: -1, Y: -1, W: 10, H: 10}, wa)
	if g.W != 260 || g.H != 140 {
		t.Errorf("min size = %+v", g)
	}
	g = ResolveResultGeometry(ResultGeom{X: -1, Y: -1, W: 9999, H: 9999}, wa)
	if g.W != wa.W || g.H != wa.H {
		t.Errorf("cap to work area = %+v", g)
	}
}

func TestResetBoxes(t *testing.T) {
	wa := WorkArea{X: 0, Y: 100, W: 1280, H: 800}
	a, b := ResetBoxes(wa)
	if a != (BoxGeom{X: 80, Y: 100, W: 720, H: 180, Visible: true}) {
		t.Errorf("A = %+v", a)
	}
	if b.Y != 100+800-180-120 || b.X != 80 {
		t.Errorf("B = %+v", b)
	}
}
