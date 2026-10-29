// overlay：Linux 平台入口——Wayland 会话明确报错，X11 会话走纯 Go 线协议实现。
package overlay

import (
	"errors"
	"os"
)

// NewHost 创建 Linux 悬浮窗宿主。
// Wayland 会话不支持截屏/置顶悬浮窗（同开发指南：Wayland 会话截屏不可用须换 X11 登录），
// 明确报 ErrWayland，不静默降级。
func NewHost() (Host, error) {
	session := os.Getenv("XDG_SESSION_TYPE")
	if session == "wayland" {
		return nil, ErrWayland
	}
	if os.Getenv("DISPLAY") == "" {
		if os.Getenv("WAYLAND_DISPLAY") != "" {
			return nil, ErrWayland
		}
		return nil, errors.New("未设置 DISPLAY（需要 X11 会话）")
	}
	return newX11Host()
}
