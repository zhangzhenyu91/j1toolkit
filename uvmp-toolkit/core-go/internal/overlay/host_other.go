//go:build !windows && !linux

// overlay：其他平台占位（无原生悬浮窗实现，明确报错）。
package overlay

// NewHost 当前平台无原生悬浮窗实现（Windows 与 Linux/X11 之外）。
func NewHost() (Host, error) {
	return nil, ErrUnsupported
}
