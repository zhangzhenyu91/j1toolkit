package main

import (
	"runtime"
	"strings"
	"testing"
)

func TestRenderDiagnostics(t *testing.T) {
	env := map[string]string{
		"XDG_SESSION_TYPE": "x11",
		"DISPLAY":          ":1",
		"WAYLAND_DISPLAY":  "",
	}
	got := renderDiagnostics(func(key string) string { return env[key] })
	wantGL := "gl2.1"
	if (runtime.GOARCH == "arm" || runtime.GOARCH == "arm64") && runtime.GOOS != "darwin" {
		wantGL = "gles2.0"
	}
	for _, want := range []string{
		"goos=" + runtime.GOOS,
		"goarch=" + runtime.GOARCH,
		"fyne_gl_build=" + wantGL,
		"xdg_session_type=x11",
		"display=:1",
		"wayland_display=<unset>",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("诊断 %q 缺少 %q", got, want)
		}
	}
}

func TestRenderDiagnosticsEmptyEnvironment(t *testing.T) {
	got := renderDiagnostics(func(string) string { return "" })
	for _, key := range []string{"xdg_session_type", "display", "wayland_display"} {
		if !strings.Contains(got, key+"=<unset>") {
			t.Errorf("空环境诊断 %q 缺少 %s=<unset>", got, key)
		}
	}
}
