// 拖拽数学测试：移动/缩放位移、最小尺寸、3px 收缩余量（对齐 .cjs _pollBoxDrag 口径）。
package overlay

import "testing"

func TestDraggerMove(t *testing.T) {
	d := NewDragger(DragMove, Point{X: 200, Y: 200}, Rect{X: 80, Y: 100, W: 720, H: 180}, 160, 60)
	got := d.Step(Point{X: 250, Y: 230})
	want := Rect{X: 130, Y: 130, W: 720, H: 180}
	if got != want {
		t.Errorf("move = %+v, want %+v", got, want)
	}
	got = d.Step(Point{X: 150, Y: 180}) // 负位移
	want = Rect{X: 30, Y: 80, W: 720, H: 180}
	if got != want {
		t.Errorf("move back = %+v, want %+v", got, want)
	}
}

func TestDraggerResizeGrow(t *testing.T) {
	d := NewDragger(DragResize, Point{X: 200, Y: 200}, Rect{X: 80, Y: 100, W: 720, H: 180}, 160, 60)
	got := d.Step(Point{X: 260, Y: 230})
	want := Rect{X: 80, Y: 100, W: 780, H: 210} // 位置不动，仅放大
	if got != want {
		t.Errorf("grow = %+v, want %+v", got, want)
	}
}

func TestDraggerResizeShrinkSlack(t *testing.T) {
	d := NewDragger(DragResize, Point{X: 200, Y: 200}, Rect{X: 80, Y: 100, W: 720, H: 180}, 160, 60)
	got := d.Step(Point{X: 180, Y: 190})
	// 收缩：w=700、h=170，均小于起点 → 各 +3px 余量（光标不越窗界）
	want := Rect{X: 80, Y: 100, W: 703, H: 173}
	if got != want {
		t.Errorf("shrink = %+v, want %+v", got, want)
	}
}

func TestDraggerResizeMinSize(t *testing.T) {
	d := NewDragger(DragResize, Point{X: 200, Y: 200}, Rect{X: 80, Y: 100, W: 720, H: 180}, 160, 60)
	got := d.Step(Point{X: -1000, Y: -1000}) // 大幅负位移 → 宽高均钳到最小尺寸（仍 +3 余量）
	want := Rect{X: 80, Y: 100, W: 163, H: 63}
	if got != want {
		t.Errorf("min size = %+v, want %+v", got, want)
	}
	// 小幅放大：目标大于起点尺寸时不补余量
	d2 := NewDragger(DragResize, Point{X: 200, Y: 200}, Rect{X: 80, Y: 100, W: 720, H: 180}, 160, 60)
	got = d2.Step(Point{X: 210, Y: 210})
	want = Rect{X: 80, Y: 100, W: 730, H: 190}
	if got != want {
		t.Errorf("grow small = %+v, want %+v", got, want)
	}
}
