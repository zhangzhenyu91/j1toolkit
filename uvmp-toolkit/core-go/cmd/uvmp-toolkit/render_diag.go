package main

import (
	"fmt"
	"log"
	"os"
	"runtime"
	"strings"
)

// renderDiagnostics 记录编译期 GL API 与进程原始会话环境。
// DISPLAY 在 Wayland+XWayland 下也可能存在，不能单凭它判断实际会话。
func renderDiagnostics(getenv func(string) string) string {
	value := func(key string) string {
		if raw := strings.TrimSpace(getenv(key)); raw != "" {
			return raw
		}
		return "<unset>"
	}
	return fmt.Sprintf(
		"goos=%s goarch=%s fyne_gl_build=%s xdg_session_type=%s display=%s wayland_display=%s",
		runtime.GOOS, runtime.GOARCH, fyneGLBuild,
		value("XDG_SESSION_TYPE"), value("DISPLAY"), value("WAYLAND_DISPLAY"))
}

func logRenderDiagnostics() {
	// 仅 stderr/冒烟日志；桌面图标启动不保证持久保存，也不冒充实际 GL renderer。
	log.Printf("Fyne 运行环境（编译期 GL / 进程会话）: %s", renderDiagnostics(os.Getenv))
}
