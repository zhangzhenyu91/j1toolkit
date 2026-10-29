package platform

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// AutoStart 开机自启动管理（登录后驻留托盘）：
//   - linux：`~/.config/autostart/uvmp-toolkit.desktop`（与 electron/main.cjs 口径一致）；
//   - windows：`HKCU\Software\Microsoft\Windows\CurrentVersion\Run` 注册表（经 reg.exe，避免引入注册表依赖）；
//   - darwin：`~/Library/LaunchAgents/com.j1net.uvmp-toolkit.plist`。
//
// GOOS/Home/Run 可注入，纯逻辑可测；默认走 runtime 与 exec.Command。
type AutoStart struct {
	Name     string                                  // 显示名（linux Name= / windows 注册表值名）
	ExecPath string                                  // 可执行文件路径（含必要参数，原样写入）
	Comment  string                                  // 备注（linux Comment=）
	Home     string                                  // 家目录（空则取 os.UserHomeDir）
	GOOS     string                                  // 空则取 platform.GOOS()
	Run      func(name string, args ...string) error // 外部命令执行器（windows reg；测试注入）
}

// DefaultAutoStart 用当前可执行文件构造。
func DefaultAutoStart() (*AutoStart, error) {
	exe, err := os.Executable()
	if err != nil {
		return nil, err
	}
	return &AutoStart{
		Name:     "Shade 壹匣 - 内网",
		ExecPath: exe,
		Comment:  "内网工具箱桌面客户端",
	}, nil
}

func (a *AutoStart) goos() string {
	if a.GOOS != "" {
		return a.GOOS
	}
	return GOOS()
}

func (a *AutoStart) home() (string, error) {
	if a.Home != "" {
		return a.Home, nil
	}
	return os.UserHomeDir()
}

func (a *AutoStart) run(name string, args ...string) error {
	if a.Run != nil {
		return a.Run(name, args...)
	}
	cmd := exec.Command(name, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s %v 失败: %w（%s）", name, args, err, strings.TrimSpace(string(out)))
	}
	return nil
}

const runKeyPath = `HKCU\Software\Microsoft\Windows\CurrentVersion\Run`
const runValueName = "uvmp-toolkit"

// DesktopFilePath linux 自启动 .desktop 文件路径。
func (a *AutoStart) DesktopFilePath() (string, error) {
	home, err := a.home()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "autostart", "uvmp-toolkit.desktop"), nil
}

// PlistFilePath darwin LaunchAgent 路径。
func (a *AutoStart) PlistFilePath() (string, error) {
	home, err := a.home()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "Library", "LaunchAgents", "com.j1net.uvmp-toolkit.plist"), nil
}

// Enable 启用开机自启动。
func (a *AutoStart) Enable() error {
	switch a.goos() {
	case "windows":
		return a.run("reg", "add", runKeyPath, "/v", runValueName, "/d", a.ExecPath, "/f")
	case "darwin":
		p, err := a.PlistFilePath()
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return err
		}
		body := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
<key>Label</key><string>com.j1net.uvmp-toolkit</string>
<key>ProgramArguments</key><array><string>%s</string></array>
<key>RunAtLoad</key><true/>
</dict></plist>
`, a.ExecPath)
		return os.WriteFile(p, []byte(body), 0o644)
	default: // linux
		p, err := a.DesktopFilePath()
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return err
		}
		body := "[Desktop Entry]\nType=Application\nName=" + a.Name + "\n" +
			"Exec=" + a.ExecPath + "\n" +
			"X-GNOME-Autostart-enabled=true\n"
		if a.Comment != "" {
			body += "Comment=" + a.Comment + "\n"
		}
		return os.WriteFile(p, []byte(body), 0o600)
	}
}

// Disable 关闭开机自启动（不存在时静默成功）。
func (a *AutoStart) Disable() error {
	switch a.goos() {
	case "windows":
		// 先查后删：值不存在时直接成功，避免依赖 reg.exe 的本地化/编码错误文案。
		enabled, err := a.Enabled()
		if err != nil {
			return err
		}
		if !enabled {
			return nil
		}
		return a.run("reg", "delete", runKeyPath, "/v", runValueName, "/f")
	case "darwin":
		p, err := a.PlistFilePath()
		if err != nil {
			return err
		}
		return removeIfExists(p)
	default:
		p, err := a.DesktopFilePath()
		if err != nil {
			return err
		}
		return removeIfExists(p)
	}
}

// Enabled 查询当前是否已启用开机自启动。
func (a *AutoStart) Enabled() (bool, error) {
	switch a.goos() {
	case "windows":
		err := a.run("reg", "query", runKeyPath, "/v", runValueName)
		return err == nil, nil
	case "darwin":
		p, err := a.PlistFilePath()
		if err != nil {
			return false, err
		}
		_, serr := os.Stat(p)
		return serr == nil, nil
	default:
		p, err := a.DesktopFilePath()
		if err != nil {
			return false, err
		}
		_, serr := os.Stat(p)
		return serr == nil, nil
	}
}

func removeIfExists(p string) error {
	err := os.Remove(p)
	if err != nil && os.IsNotExist(err) {
		return nil
	}
	return err
}
