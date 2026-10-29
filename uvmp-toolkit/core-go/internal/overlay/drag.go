// overlay：拖拽轮询的纯数学（移植自 index.cjs _pollBoxDrag / result 拖动）。
// 位移一律由 OS 光标与起点差值推出：渲染层合成输入 movementX/Y 在悬浮窗上不可靠（已踩坑）。
// 缩放收缩留 3px 余量让光标不越窗界（窗口跟手后光标始终留在窗内，抬起不依赖 pointer capture）。
package overlay

import "time"

// DragMode 拖拽模式。
type DragMode int

const (
	// DragMove 整窗移动。
	DragMove DragMode = iota
	// DragResize 右下缩放（位置不动，仅改尺寸）。
	DragResize
)

// DragPollInterval 拖拽光标轮询间隔（16ms，同 .cjs setInterval）。
const DragPollInterval = 16 * time.Millisecond

// DragTimeout 拖拽兜底超时（5 分钟防卡死，同 .cjs）。
const DragTimeout = 5 * time.Minute

// Dragger 拖拽状态机：由起点光标/几何 + 当前光标推出目标几何（纯函数式，便于测试）。
type Dragger struct {
	Mode    DragMode
	cursor0 Point
	bounds0 Rect
	MinW    int
	MinH    int
}

// NewDragger 创建拖拽状态机（minW/minH 为窗口最小尺寸）。
func NewDragger(mode DragMode, cursor0 Point, bounds0 Rect, minW, minH int) *Dragger {
	return &Dragger{Mode: mode, cursor0: cursor0, bounds0: bounds0, MinW: minW, MinH: minH}
}

// Step 按当前光标位移计算目标几何。
// 移动：起点几何 + 光标差值；缩放：宽高 + 光标差值（钳最小尺寸，收缩再 +3px 余量），
// 位置保持起点（缩放过程窗口不动）。
func (d *Dragger) Step(cursor Point) Rect {
	dx := cursor.X - d.cursor0.X
	dy := cursor.Y - d.cursor0.Y
	if d.Mode == DragMove {
		return Rect{X: d.bounds0.X + dx, Y: d.bounds0.Y + dy, W: d.bounds0.W, H: d.bounds0.H}
	}
	w := d.bounds0.W + dx
	h := d.bounds0.H + dy
	if w < d.MinW {
		w = d.MinW
	}
	if h < d.MinH {
		h = d.MinH
	}
	if w < d.bounds0.W {
		w += 3
	}
	if h < d.bounds0.H {
		h += 3
	}
	return Rect{X: d.bounds0.X, Y: d.bounds0.Y, W: w, H: h}
}
