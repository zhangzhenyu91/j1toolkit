// overlay：原生透明置顶悬浮窗（扫描框/结果窗）与屏幕采集抽象。
//
// 坐标口径对齐 Electron screen API：窗口/显示器几何为 DIP（逻辑像素），
// 截帧 Frame 为物理像素（DIP × Display.Scale）；持久化几何与 settings.json 同单位，可互换。
// 像素格式：窗口内容 Buffer 为 RGBA 预乘 alpha（与 image.RGBA 同布局）；
// 截帧/裁剪为稠密 BGRA8888（对齐 Electron nativeImage.getBitmap，OCR 输入同口径）。
//
// 平台实现按 build tag 拆分：Windows（Win32 分层窗）、Linux（X11 纯 Go 线协议）；
// Wayland 会话明确报错（截屏/置顶均不可用，需 X11 登录）。其他平台返回 ErrUnsupported。
package overlay

import "errors"

// Rect 矩形（DIP，屏幕坐标）。
type Rect struct {
	X int `json:"x"`
	Y int `json:"y"`
	W int `json:"w"`
	H int `json:"h"`
}

// Contains 点是否在矩形内。
func (r Rect) Contains(x, y int) bool {
	return x >= r.X && x < r.X+r.W && y >= r.Y && y < r.Y+r.H
}

// Point 点（DIP，屏幕坐标）。
type Point struct {
	X int
	Y int
}

// Display 显示器（Bounds/WorkArea 为 DIP；Scale = 物理像素每 DIP）。
type Display struct {
	ID       int
	Bounds   Rect
	WorkArea Rect
	Scale    float64
}

// Frame 整显示器截帧（稠密 BGRA8888，物理像素）。
type Frame struct {
	Pix       []byte // len = Width*Height*4
	Width     int
	Height    int
	DisplayID int
}

// Crop 框区域裁剪结果（稠密 BGRA8888，物理像素）。
type Crop struct {
	Pix    []byte // len = Width*Height*4
	Width  int
	Height int
}

// MaxThumb 缩略图长边封顶（超大屏/高倍率下缩略图请求封顶——部分平台超额直接回空，已踩坑）。
const MaxThumb = 4096

// WindowOpts 创建悬浮窗参数。
type WindowOpts struct {
	Bounds  Rect    // 初始几何（DIP）
	Title   string  // 窗口标题（诊断用）
	Opacity float64 // 全局不透明度 0.3~1
}

// WindowEventKind 悬浮窗输入事件类别。
type WindowEventKind int

const (
	// EventPointerPress 窗内按下（X/Y 为窗内 DIP 坐标，Button 1=左 2=中 3=右）。
	EventPointerPress WindowEventKind = iota
	// EventPointerRelease 窗内抬起。
	EventPointerRelease
)

// WindowEvent 悬浮窗输入事件。
type WindowEvent struct {
	Kind   WindowEventKind
	X, Y   int // 窗内 DIP 坐标
	Button int // 1=左 2=中 3=右
}

// Window 透明置顶悬浮窗。
type Window interface {
	// Bounds 当前几何（DIP）。
	Bounds() Rect
	// SetBounds 移动+缩放一步到位（Windows 透明窗缩小单独 setSize 只生效一次——已踩坑，实现须整体设界）。
	SetBounds(r Rect)
	// SetOpacity 设置全局不透明度（0.3~1）。
	SetOpacity(a float64)
	// SetVisible 显示/隐藏（显示不抢焦点）。
	SetVisible(v bool)
	// Paint 提交整窗内容（RGBA 预乘 alpha 稠密缓冲，尺寸须与窗口一致；实现负责转平台像素）。
	Paint(b *Buffer) error
	// Events 输入事件流（Close 后关闭）。
	Events() <-chan WindowEvent
	// Close 销毁窗口（幂等）。
	Close()
}

// Host 悬浮窗/屏幕采集宿主（平台实现；测试用 fake.go）。
type Host interface {
	// NewWindow 创建透明置顶悬浮窗。
	NewWindow(opts WindowOpts) (Window, error)
	// PointerState 查询光标位置（DIP）与左键按下态（拖拽轮询用；不依赖 pointer capture）。
	PointerState() (pos Point, leftDown bool, err error)
	// Displays 全部显示器。
	Displays() []Display
	// Primary 主显示器。
	Primary() Display
	// DisplayFor 与给定矩形相交面积最大的显示器（对齐 Electron getDisplayMatching；无交集取主显示器）。
	DisplayFor(r Rect) Display
	// CaptureDisplay 截取显示器整屏帧；maxThumb>0 时长边封顶。返回 nil 帧表示截屏为空。
	CaptureDisplay(d Display, maxThumb int) (*Frame, error)
	// Close 释放宿主资源（幂等）。
	Close()
}

var (
	// ErrWayland Wayland 会话不支持（截屏/置顶悬浮窗需 X11 会话）。
	ErrWayland = errors.New("Wayland 会话不支持悬浮窗与截屏，请切换 X11 会话登录")
	// ErrUnsupported 当前平台无原生悬浮窗实现。
	ErrUnsupported = errors.New("当前平台不支持原生悬浮窗")
)
