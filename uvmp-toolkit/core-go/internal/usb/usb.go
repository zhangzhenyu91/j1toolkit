// Package usb U 盘（按卷标）发现/挂载与平台工具（移植自 toolkit/platform_utils.py）。
//
// U 盘逻辑移植自 export_dispatch_orders.py（卷标 GLKVM 的 KVM 虚拟 U 盘）：
//   - linux：lsblk 按卷标发现设备；未挂载则用 udisksctl 挂载（重试 3 次）
//   - windows：PowerShell Get-Volume 按卷标扫盘符（尽力而为；找不到回退 fallback_dir）
//
// 命令执行/文件存在性可注入（Runner/Stat），单测全走 fake，不碰真实系统。
package usb

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// Runner 命令执行器（测试注入 fake）。
type Runner interface {
	// Run 执行命令，返回 stdout/stderr；超时/失败以 err 体现（stdout/stderr 尽量带回）。
	Run(ctx context.Context, name string, args ...string) (stdout, stderr string, err error)
}

// ExecRunner 真实子进程执行器（30 秒超时）。
type ExecRunner struct{}

// Run 实现 Runner。
func (ExecRunner) Run(ctx context.Context, name string, args ...string) (string, string, error) {
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(cctx, name, args...)
	var out, errb bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errb
	err := cmd.Run()
	return out.String(), errb.String(), err
}

// Options 发现/挂载的可注入依赖；零值可用（真实系统行为）。
type Options struct {
	Runner   Runner // 命令执行器；nil=ExecRunner
	GOOS     string // "windows"/"linux"；空=runtime.GOOS
	Sleep    func(time.Duration)
	Stat     func(string) bool // 路径存在性；nil=os.Stat（须为路径存在）
	Environ  func(string) string
	Euid     *int // 进程 euid（linux root 挂载逻辑用）；nil=os.Geteuid
	LookPath func(string) (string, error)
}

func (o *Options) lookPath(name string) (string, error) {
	if o != nil && o.LookPath != nil {
		return o.LookPath(name)
	}
	return exec.LookPath(name)
}

func (o *Options) runner() Runner {
	if o != nil && o.Runner != nil {
		return o.Runner
	}
	return ExecRunner{}
}

func (o *Options) goos() string {
	if o != nil && o.GOOS != "" {
		return o.GOOS
	}
	return runtime.GOOS
}

func (o *Options) sleep(d time.Duration) {
	if o != nil && o.Sleep != nil {
		o.Sleep(d)
		return
	}
	time.Sleep(d)
}

func (o *Options) stat(p string) bool {
	if o != nil && o.Stat != nil {
		return o.Stat(p)
	}
	st, err := os.Stat(p)
	return err == nil && st != nil
}

func (o *Options) environ(k string) string {
	if o != nil && o.Environ != nil {
		return o.Environ(k)
	}
	return os.Getenv(k)
}

func (o *Options) euid() int {
	if o != nil && o.Euid != nil {
		return *o.Euid
	}
	return os.Geteuid()
}

func logf(log func(string), format string, args ...any) {
	if log != nil {
		log(fmt.Sprintf(format, args...))
	}
}

// ---------------------------------------------------------------- U 盘（按卷标）

// FindUsbMount 按卷标找 U 盘挂载点/盘符；找不到返回空串（Python find_usb_mount 同口径）。
func FindUsbMount(label string, log func(string), opts *Options) string {
	if label == "" {
		return ""
	}
	if opts.goos() == "windows" {
		return findUSBWindows(label, log, opts)
	}
	return findUSBLinux(label, log, opts)
}

// desktopUser 返回本机活动桌面用户名；无活动会话时回退镜像状态目录属主
// （Python platform_utils._desktop_user 同口径；仅 linux）。
func desktopUser(opts *Options) string {
	uid := -1
	if stdout, stderr, err := opts.runner().Run(context.Background(), "loginctl", "list-sessions", "--no-legend"); err == nil {
		_ = stderr
		for _, line := range strings.Split(stdout, "\n") {
			parts := strings.Fields(line)
			if len(parts) < 3 {
				continue
			}
			sessionID, rawUID := parts[0], parts[1]
			u, err := strconv.Atoi(rawUID)
			if err != nil || u == 0 {
				continue
			}
			infoOut, _, ierr := opts.runner().Run(context.Background(),
				"loginctl", "show-session", sessionID, "-p", "Active", "-p", "State", "-p", "Remote")
			if ierr != nil {
				continue
			}
			props := map[string]string{}
			for _, item := range strings.Split(infoOut, "\n") {
				if k, v, ok := strings.Cut(item, "="); ok {
					props[strings.TrimSpace(k)] = strings.TrimSpace(v)
				}
			}
			if props["Active"] == "yes" && props["State"] == "active" && props["Remote"] == "no" {
				uid = u
				break
			}
		}
	} else {
		_ = stderr
	}
	if uid == -1 {
		// 回退：镜像状态目录属主
		if mirror := strings.TrimSpace(opts.environ("UVMP_TOOLKIT_MIRROR_STATE")); mirror != "" {
			if st, err := os.Stat(mirror); err == nil {
				uid = fileUID(st)
			}
		}
	}
	if uid <= 0 {
		return ""
	}
	if u, err := user.LookupId(strconv.Itoa(uid)); err == nil {
		return u.Username
	}
	return ""
}

// mountLinuxDevice root 触发时优先以桌面用户挂载，失败后回退 root 保证自动任务可用。
func mountLinuxDevice(device string, log func(string), opts *Options) (string, string, error) {
	cmd := []string{"mount", "-b", device}
	if opts.euid() == 0 {
		name := desktopUser(opts)
		if _, err := opts.lookPath("runuser"); err == nil && name != "" {
			logf(log, "以桌面用户 %s 挂载U盘", name)
			args := append([]string{"-u", name, "--", "udisksctl"}, cmd...)
			stdout, stderr, err := opts.runner().Run(context.Background(), "runuser", args...)
			if err == nil {
				return stdout, stderr, nil
			}
			logf(log, "桌面用户挂载失败，改用 root 挂载（桌面用户可能无权打开）: %s", strings.TrimSpace(stderr))
		} else {
			logf(log, "未识别桌面用户或 runuser，按 root 挂载（桌面用户可能无权打开）")
		}
	}
	return opts.runner().Run(context.Background(), "udisksctl", cmd...)
}

// scanLinux lsblk 按卷标找设备：返回 (device, mountpoint, ok)；ok=false 表示扫描失败。
func scanLinux(labelUpper string, log func(string), opts *Options) (string, string, bool) {
	stdout, stderr, err := opts.runner().Run(context.Background(),
		"lsblk", "-J", "-o", "PATH,LABEL,MOUNTPOINT")
	if err != nil {
		logf(log, "lsblk 执行失败: %s", strings.TrimSpace(stderr))
		return "", "", false
	}
	var data struct {
		BlockDevices []struct {
			Path       string `json:"path"`
			Label      string `json:"label"`
			Mountpoint string `json:"mountpoint"`
			Children   []struct {
				Path       string `json:"path"`
				Label      string `json:"label"`
				Mountpoint string `json:"mountpoint"`
			} `json:"children"`
		} `json:"blockdevices"`
	}
	if jerr := json.Unmarshal([]byte(stdout), &data); jerr != nil {
		logf(log, "查找U盘设备异常: %s", jerr)
		return "", "", false
	}
	for _, dev := range data.BlockDevices {
		nodes := []struct {
			Path, Label, Mountpoint string
		}{
			{dev.Path, dev.Label, dev.Mountpoint},
		}
		for _, c := range dev.Children {
			nodes = append(nodes, struct{ Path, Label, Mountpoint string }{c.Path, c.Label, c.Mountpoint})
		}
		for _, n := range nodes {
			if strings.EqualFold(n.Label, labelUpper) {
				return n.Path, n.Mountpoint, true
			}
		}
	}
	return "", "", true
}

// findUSBLinux 确保卷标为 label 的设备已挂载，返回挂载点；失败返回空串。
func findUSBLinux(label string, log func(string), opts *Options) string {
	labelUpper := strings.ToUpper(label)
	for attempt := 1; attempt <= 3; attempt++ {
		device, mountpoint, ok := scanLinux(labelUpper, log, opts)
		if !ok {
			return ""
		}
		if mountpoint != "" {
			logf(log, "U盘已挂载: %s", mountpoint)
			return mountpoint
		}
		if device == "" {
			logf(log, "未找到卷标 %s 的U盘设备 (第 %d/3 次)", label, attempt)
		} else {
			_, stderr, err := mountLinuxDevice(device, log, opts)
			if err != nil {
				logf(log, "挂载失败 (%s): %s", device, strings.TrimSpace(stderr))
			}
			opts.sleep(1 * time.Second)
			_, actual, ok2 := scanLinux(labelUpper, log, opts)
			if !ok2 {
				return ""
			}
			if actual != "" {
				logf(log, "U盘挂载成功: %s", actual)
				return actual
			}
			if err == nil {
				logf(log, "挂载后未检测到挂载点")
			}
		}
		if attempt < 3 {
			opts.sleep(5 * time.Second)
		}
	}
	return ""
}

// findUSBWindows PowerShell Get-Volume 按卷标找盘符，返回 'X:\'；找不到返回空串。
// （不用 ctypes 调 Win32 API：PowerShell 子进程零原生依赖，Win10+ 自带）
func findUSBWindows(label string, log func(string), opts *Options) string {
	label = strings.TrimSpace(label)
	if label == "" {
		return ""
	}
	safe := strings.ReplaceAll(label, "'", "''")
	ps := fmt.Sprintf("(Get-Volume -FileSystemLabel '%s' -ErrorAction SilentlyContinue).DriveLetter", safe)
	stdout, _, err := opts.runner().Run(context.Background(),
		"powershell", "-NoProfile", "-NonInteractive", "-Command", ps)
	if err == nil {
		lines := strings.Split(strings.TrimSpace(stdout), "\n")
		if len(lines) > 0 {
			letter := strings.TrimSpace(lines[0])
			if letter != "" {
				root := letter + ":\\"
				if opts.stat(root) {
					logf(log, "U盘已就绪: %s（卷标 %s）", root, label)
					return root
				}
			}
		}
	} else {
		logf(log, "U盘检测异常: %s", err)
	}
	logf(log, "未找到卷标 %s 的U盘", label)
	return ""
}

// Source 标记常量（UsbWritableDir 返回值）。
const (
	SourceUSB      = "usb"
	SourceFallback = "fallback"
	SourceNone     = "none"
)

// UsbWritableDir 确定交付目录：优先 U 盘挂载点，其次 fallbackDir（自动创建）。
// 返回 (目录或空, 来源标记 usb/fallback/none)（Python usb_writable_dir 同口径）。
func UsbWritableDir(label, fallbackDir string, log func(string), opts *Options) (string, string) {
	if mount := FindUsbMount(label, log, opts); mount != "" {
		return mount, SourceUSB
	}
	fb := strings.TrimSpace(fallbackDir)
	if fb != "" {
		_ = os.MkdirAll(fb, 0o755)
		logf(log, "U盘不可用，改用后备目录: %s", fb)
		return fb, SourceFallback
	}
	return "", SourceNone
}

// SyncFs linux 落盘；windows 无对应操作。
func SyncFs(opts *Options) {
	if opts.goos() == "linux" {
		_, _, _ = opts.runner().Run(context.Background(), "sync")
	}
}

// ---------------------------------------------------------------- 其他平台工具

// OpenPath 用系统文件管理器打开目录/文件（GUI「打开目录」按钮）。
func OpenPath(path string) bool {
	switch runtime.GOOS {
	case "windows":
		cmd := exec.Command("cmd", "/c", "start", "", path)
		return cmd.Start() == nil
	case "darwin":
		return exec.Command("open", path).Start() == nil
	default:
		cmd := exec.Command("xdg-open", path)
		cmd.Stdout = nil
		cmd.Stderr = nil
		return cmd.Start() == nil
	}
}

// HomeSubdir 在家目录下找第一个存在的候选子目录（中文/英文桌面目录差异）。
func HomeSubdir(names ...string) string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	for _, n := range names {
		p := filepath.Join(home, n)
		if st, err := os.Stat(p); err == nil && st.IsDir() {
			return p
		}
	}
	return home
}

// SuggestedOutdirs GUI 输出目录建议列表：U盘 > 桌面 > 文档 > 项目 output（去重保序）。
func SuggestedOutdirs(label, defaultOutdir, baseDir string, opts *Options) []string {
	out := []string{}
	if mount := FindUsbMount(label, nil, opts); mount != "" {
		out = append(out, mount)
	}
	out = append(out, HomeSubdir("桌面", "Desktop"))
	out = append(out, HomeSubdir("文档", "Documents"))
	def := strings.TrimSpace(defaultOutdir)
	if def == "" {
		def = filepath.Join(baseDir, "output")
	}
	out = append(out, def)
	seen := map[string]bool{}
	ret := []string{}
	for _, d := range out {
		if d != "" && !seen[d] {
			seen[d] = true
			ret = append(ret, d)
		}
	}
	return ret
}

// PlatformSummary 系统概要（getSystemInfo 用）：平台/架构/核心版本。
func PlatformSummary() map[string]any {
	return map[string]any{
		"platform": runtime.GOOS,
		"machine":  runtime.GOARCH,
		"core":     "go " + runtime.Version(),
	}
}
