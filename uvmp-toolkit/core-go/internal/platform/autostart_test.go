package platform

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAutoStart_Linux(t *testing.T) {
	home := t.TempDir()
	a := &AutoStart{
		Name:     "Shade 壹匣 - 内网",
		ExecPath: "/opt/uvmp-toolkit/uvmp-toolkit",
		Comment:  "内网工具箱桌面客户端",
		Home:     home,
		GOOS:     "linux",
	}

	if on, err := a.Enabled(); err != nil || on {
		t.Fatalf("初始应未启用: on=%v err=%v", on, err)
	}
	if err := a.Enable(); err != nil {
		t.Fatalf("Enable: %v", err)
	}
	p, _ := a.DesktopFilePath()
	data, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("desktop 文件未生成: %v", err)
	}
	body := string(data)
	if !strings.Contains(body, "Exec=/opt/uvmp-toolkit/uvmp-toolkit") ||
		!strings.Contains(body, "Name=Shade 壹匣 - 内网") ||
		!strings.Contains(body, "X-GNOME-Autostart-enabled=true") {
		t.Fatalf("desktop 文件内容不符:\n%s", body)
	}
	if on, err := a.Enabled(); err != nil || !on {
		t.Fatalf("启用后应为已启用: on=%v err=%v", on, err)
	}
	if err := a.Disable(); err != nil {
		t.Fatalf("Disable: %v", err)
	}
	if on, _ := a.Enabled(); on {
		t.Fatal("关闭后应为未启用")
	}
	// 不存在时 Disable 幂等
	if err := a.Disable(); err != nil {
		t.Fatalf("重复 Disable 应幂等: %v", err)
	}
}

func TestAutoStart_Windows(t *testing.T) {
	var calls [][]string
	a := &AutoStart{
		ExecPath: `C:\Program Files\uvmp-toolkit\uvmp-toolkit.exe`,
		GOOS:     "windows",
		Home:     t.TempDir(),
		Run: func(name string, args ...string) error {
			calls = append(calls, append([]string{name}, args...))
			return nil
		},
	}
	if err := a.Enable(); err != nil {
		t.Fatalf("Enable: %v", err)
	}
	if len(calls) != 1 || calls[0][0] != "reg" || calls[0][1] != "add" {
		t.Fatalf("应调用 reg add: %v", calls)
	}
	joined := strings.Join(calls[0], " ")
	if !strings.Contains(joined, runKeyPath) || !strings.Contains(joined, runValueName) {
		t.Fatalf("reg add 参数不符: %s", joined)
	}

	calls = nil
	if on, err := a.Enabled(); err != nil || !on {
		t.Fatalf("Enabled 应查询 reg: on=%v err=%v", on, err)
	}
	if len(calls) != 1 || calls[0][1] != "query" {
		t.Fatalf("应调用 reg query: %v", calls)
	}

	calls = nil
	if err := a.Disable(); err != nil {
		t.Fatalf("Disable: %v", err)
	}
	if len(calls) != 1 || calls[0][1] != "delete" {
		t.Fatalf("应调用 reg delete: %v", calls)
	}
}

func TestAutoStart_Darwin(t *testing.T) {
	home := t.TempDir()
	a := &AutoStart{ExecPath: "/opt/uvmp-toolkit", GOOS: "darwin", Home: home}
	if err := a.Enable(); err != nil {
		t.Fatalf("Enable: %v", err)
	}
	p, _ := a.PlistFilePath()
	data, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("plist 未生成: %v", err)
	}
	if !strings.Contains(string(data), "/opt/uvmp-toolkit") {
		t.Fatalf("plist 内容不符:\n%s", data)
	}
	if on, _ := a.Enabled(); !on {
		t.Fatal("启用后应为已启用")
	}
	if err := a.Disable(); err != nil {
		t.Fatalf("Disable: %v", err)
	}
	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Fatalf("Disable 后 plist 应删除: %v", err)
	}
}

func TestDefaultAutoStart(t *testing.T) {
	a, err := DefaultAutoStart()
	if err != nil || a.ExecPath == "" {
		t.Fatalf("DefaultAutoStart: %+v %v", a, err)
	}
	if filepath.Base(a.ExecPath) == "" {
		t.Fatalf("ExecPath 不应为空: %q", a.ExecPath)
	}
}
