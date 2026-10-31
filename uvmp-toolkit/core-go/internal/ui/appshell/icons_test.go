package appshell

import (
	"image/color"
	"strings"
	"testing"
)

func TestLineIconSharedActionSet(t *testing.T) {
	names := []string{
		"folder", "file", "save", "upload", "stop", "pause", "cancel", "edit",
		"reset", "globe", "flask", "info", "warning", "close", "restore",
		"download", "copy", "trash", "play", "refresh", "check",
	}
	for _, name := range names {
		t.Run(name, func(t *testing.T) {
			if path := lineIconPaths[name]; path == "" {
				t.Fatalf("共享动作图标 %q 未登记", name)
			}
			resource := lineIcon(name, color.White)
			if resource == nil {
				t.Fatal("线性图标资源不得为 nil")
			}
			svg := string(resource.Content())
			for _, want := range []string{`viewBox="0 0 24 24"`, `stroke-width="1.7"`, `stroke="#FFFFFF"`} {
				if !strings.Contains(svg, want) {
					t.Fatalf("图标 %q 缺少 %s：%s", name, want, svg)
				}
			}
		})
	}
}
