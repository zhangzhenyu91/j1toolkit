package appshell

// nativeWindowOps 只封装窗口管理，不承载业务逻辑。
// 实现使用 Fyne 的 driver.NativeWindow 官方接口获取原生句柄。
type nativeWindowOps interface {
	Prepare()
	Minimize()
	ToggleMaximize()
	BeginMove()
	BeginResize(direction int)
	EndResize()
	Maximized() bool
	SupportsEdgeResize() bool
}

// 与 EWMH _NET_WM_MOVERESIZE 的方向值保持一致。
const (
	resizeTopLeft = iota
	resizeTop
	resizeTopRight
	resizeRight
	resizeBottomRight
	resizeBottom
	resizeBottomLeft
	resizeLeft
)

type unsupportedWindowOps struct{}

func (unsupportedWindowOps) Prepare()                 {}
func (unsupportedWindowOps) Minimize()                {}
func (unsupportedWindowOps) ToggleMaximize()          {}
func (unsupportedWindowOps) BeginMove()               {}
func (unsupportedWindowOps) BeginResize(int)          {}
func (unsupportedWindowOps) EndResize()               {}
func (unsupportedWindowOps) Maximized() bool          { return false }
func (unsupportedWindowOps) SupportsEdgeResize() bool { return false }
