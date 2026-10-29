package platform

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestStateDir(t *testing.T) {
	got := StateDir("linux", "/home/u", "", "")
	if got != filepath.Join("/home/u", ".local", "share", "uvmp-toolkit") {
		t.Fatalf("linux 默认状态目录不符: %s", got)
	}
	got = StateDir("linux", "/home/u", "/data/xdg", "")
	if got != filepath.Join("/data/xdg", "uvmp-toolkit") {
		t.Fatalf("linux XDG 优先级不符: %s", got)
	}
	got = StateDir("windows", `C:\Users\u`, "", `C:\Users\u\AppData\Local`)
	if !strings.Contains(got, "uvmp-toolkit") || !strings.Contains(got, "Local") {
		t.Fatalf("windows 状态目录不符: %s", got)
	}
	got = StateDir("windows", `C:\Users\u`, "", "")
	if !strings.Contains(got, filepath.Join("AppData", "Local")) {
		t.Fatalf("windows 缺 LOCALAPPDATA 回退不符: %s", got)
	}
}

func TestUserConfigDir(t *testing.T) {
	got := UserConfigDir("linux", "/home/u", "")
	if got != filepath.Join("/home/u", ".config", "uvmp-toolkit") {
		t.Fatalf("linux 用户配置目录不符: %s", got)
	}
	got = UserConfigDir("windows", `C:\Users\u`, `C:\Users\u\AppData\Roaming`)
	if !strings.Contains(got, "Roaming") {
		t.Fatalf("windows 用户配置目录不符: %s", got)
	}
}

func TestLockFilePath(t *testing.T) {
	p := LockFile("/tmp/state")
	if filepath.Base(p) != "uvmp-toolkit.lock" {
		t.Fatalf("锁文件名不符: %s", p)
	}
}
