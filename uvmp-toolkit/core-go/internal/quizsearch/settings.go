// 题库搜题：设置持久化（移植自 desktop/electron/quizsearch/index.cjs 的 settings 部分）。
// settings.json 与旧 Electron 实现双向兼容：同名字段、同默认值、同钳制口径。
package quizsearch

import (
	"encoding/json"
	"os"
)

// BoxGeom 扫描框几何与显隐（屏幕坐标，DIP 语义，与 .cjs settings.boxes 一致）。
type BoxGeom struct {
	X       int  `json:"x"`
	Y       int  `json:"y"`
	W       int  `json:"w"`
	H       int  `json:"h"`
	Visible bool `json:"visible"`
}

// ResultGeom 结果窗几何（x/y=-1 表示默认右下角，用户拖动/缩放后落盘）。
type ResultGeom struct {
	X int `json:"x"`
	Y int `json:"y"`
	W int `json:"w"`
	H int `json:"h"`
}

// Settings settings.json 全量设置。
type Settings struct {
	Interval  int        `json:"interval"`
	Threshold float64    `json:"threshold"`
	Opacity   float64    `json:"opacity"`
	Result    ResultGeom `json:"result"`
	Boxes     struct {
		A BoxGeom `json:"A"`
		B BoxGeom `json:"B"`
	} `json:"boxes"`
}

// WorkArea 工作区矩形（DIP；由显示层提供，几何回退用）。
type WorkArea struct {
	X, Y, W, H int
}

// DefaultSettings 默认设置（对齐 .cjs DEFAULT_SETTINGS）。
func DefaultSettings() Settings {
	var s Settings
	s.Interval = 1000 // 扫描间隔 ms（500~3000）
	s.Threshold = 0.6 // 匹配命中阈值（0.3~0.9）
	s.Opacity = 0.9   // 结果窗不透明度
	s.Result = ResultGeom{X: -1, Y: -1, W: 380, H: 260}
	s.Boxes.A = BoxGeom{X: 80, Y: 100, W: 720, H: 180, Visible: true}
	s.Boxes.B = BoxGeom{X: 80, Y: -1, W: 720, H: 180, Visible: true} // y=-1：初始化时贴屏幕底部
	return s
}

// LoadSettings 读取 settings.json 并与默认值深合并；文件缺失/损坏按默认处理。
// boxes.B.y<0 时贴主工作区底部（同 .cjs _loadSettings）。
func LoadSettings(path string, wa WorkArea) Settings {
	base := asAnyMap(DefaultSettings())
	if data, err := os.ReadFile(path); err == nil {
		var saved map[string]any
		if json.Unmarshal(data, &saved) == nil {
			deepMergeInto(base, saved)
		}
	}
	var s Settings
	if data, err := json.Marshal(base); err == nil {
		_ = json.Unmarshal(data, &s)
	}
	if s.Boxes.B.Y < 0 {
		s.Boxes.B.Y = wa.Y + wa.H - s.Boxes.B.H - 120
	}
	return s
}

// SaveSettings 写 settings.json（2 空格缩进，同 .cjs）。写失败静默忽略（同 .cjs）。
func SaveSettings(path string, s Settings) {
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return
	}
	_ = os.WriteFile(path, data, 0o644)
}

// asAnyMap 结构体转 map（json 往返，保留字段名口径）。
func asAnyMap(v any) map[string]any {
	data, err := json.Marshal(v)
	if err != nil {
		return map[string]any{}
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		return map[string]any{}
	}
	return m
}

// deepMergeInto 把 extra 的字段递归合并进 base（extra 优先；数组与标量整体替换，同 .cjs deepMerge）。
func deepMergeInto(base, extra map[string]any) {
	for k, v := range extra {
		if v == nil {
			continue
		}
		if vm, ok := v.(map[string]any); ok {
			if bm, ok := base[k].(map[string]any); ok {
				deepMergeInto(bm, vm)
				continue
			}
		}
		base[k] = v
	}
}

// ClampInterval 扫描间隔钳制（500~3000ms）。
func ClampInterval(v int) int {
	if v < 500 {
		return 500
	}
	if v > 3000 {
		return 3000
	}
	return v
}

// ClampThreshold 匹配阈值钳制（0.3~0.9）。
func ClampThreshold(v float64) float64 {
	if v < 0.3 {
		return 0.3
	}
	if v > 0.9 {
		return 0.9
	}
	return v
}

// ClampOpacity 结果窗不透明度钳制（0.3~1.0）。
func ClampOpacity(v float64) float64 {
	if v < 0.3 {
		return 0.3
	}
	if v > 1 {
		return 1
	}
	return v
}

// ResolveResultGeometry 结果窗初始几何（移植自 _createResultWin）：
// 尺寸取用户拖过的持久化宽高（最小 260×140，不超出当前工作区）；
// 位置取持久化坐标，越出工作区（留 40px 余量）则回退默认右下角（边距 16）。
func ResolveResultGeometry(saved ResultGeom, wa WorkArea) BoxGeom {
	w := saved.W
	if w == 0 {
		w = 380
	}
	if w < 260 {
		w = 260
	}
	if w > wa.W {
		w = wa.W
	}
	h := saved.H
	if h == 0 {
		h = 260
	}
	if h < 140 {
		h = 140
	}
	if h > wa.H {
		h = wa.H
	}
	x := wa.X + wa.W - w - 16
	y := wa.Y + wa.H - h - 16
	if saved.X >= wa.X && saved.X <= wa.X+wa.W-40 &&
		saved.Y >= wa.Y && saved.Y <= wa.Y+wa.H-40 {
		x = saved.X
		y = saved.Y
	}
	return BoxGeom{X: x, Y: y, W: w, H: h}
}

// ResetBoxes 恢复双框默认布局（对齐 quizSearch:resetBoxes IPC）：
// A 用默认位；B 默认位但贴工作区底部（wa.y+wa.height-h-120）。
func ResetBoxes(wa WorkArea) (a, b BoxGeom) {
	d := DefaultSettings()
	a = d.Boxes.A
	b = d.Boxes.B
	b.Y = wa.Y + wa.H - b.H - 120
	return a, b
}
