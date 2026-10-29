package usb

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeRunner 按命令名前缀脚本化返回。
type fakeRunner struct {
	// handlers 键为 "name arg1 arg2..." 前缀匹配（空格拼接后 HasPrefix）。
	handlers map[string]func(args []string) (string, string, error)
	calls    []string
}

func (f *fakeRunner) Run(_ context.Context, name string, args ...string) (string, string, error) {
	key := strings.Join(append([]string{name}, args...), " ")
	f.calls = append(f.calls, key)
	for prefix, h := range f.handlers {
		if strings.HasPrefix(key, prefix) {
			return h(args)
		}
	}
	return "", "", errors.New("未脚本化命令: " + key)
}

func intPtr(v int) *int { return &v }

func linuxOpts(f *fakeRunner) *Options {
	return &Options{
		Runner:   f,
		GOOS:     "linux",
		Sleep:    func(time.Duration) {},
		Euid:     intPtr(1000), // 非 root
		LookPath: func(string) (string, error) { return "", errors.New("not found") },
	}
}

func TestFindUsbWindows(t *testing.T) {
	f := &fakeRunner{handlers: map[string]func([]string) (string, string, error){
		"powershell": func(args []string) (string, string, error) {
			// 校验卷标单引号转义
			cmd := strings.Join(args, " ")
			if !strings.Contains(cmd, "Get-Volume -FileSystemLabel 'GLKVM'") {
				t.Fatalf("PowerShell 命令 = %q", cmd)
			}
			return "E\r\n", "", nil
		},
	}}
	opts := &Options{Runner: f, GOOS: "windows", Stat: func(p string) bool { return p == "E:\\" }}
	got := FindUsbMount("GLKVM", nil, opts)
	if got != "E:\\" {
		t.Fatalf("FindUsbMount = %q", got)
	}
	// 不存在的盘符 → 空
	opts.Stat = func(string) bool { return false }
	if got := FindUsbMount("GLKVM", nil, opts); got != "" {
		t.Fatalf("盘符不存在应返回空, got %q", got)
	}
}

func TestFindUsbWindowsQuoteEscape(t *testing.T) {
	f := &fakeRunner{handlers: map[string]func([]string) (string, string, error){
		"powershell": func(args []string) (string, string, error) {
			cmd := strings.Join(args, " ")
			if !strings.Contains(cmd, "FileSystemLabel 'A''B'") {
				t.Fatalf("单引号未转义: %q", cmd)
			}
			return "F\r\n", "", nil
		},
	}}
	opts := &Options{Runner: f, GOOS: "windows", Stat: func(p string) bool { return p == "F:\\" }}
	if got := FindUsbMount("A'B", nil, opts); got != "F:\\" {
		t.Fatalf("FindUsbMount = %q", got)
	}
}

func TestFindUsbLinuxMounted(t *testing.T) {
	lsblk := `{"blockdevices":[{"path":"/dev/sda1","label":"GLKVM","mountpoint":"/media/usb","children":null}]}`
	f := &fakeRunner{handlers: map[string]func([]string) (string, string, error){
		"lsblk": func([]string) (string, string, error) { return lsblk, "", nil },
	}}
	if got := FindUsbMount("GLKVM", nil, linuxOpts(f)); got != "/media/usb" {
		t.Fatalf("已挂载 = %q", got)
	}
}

func TestFindUsbLinuxMountRetry(t *testing.T) {
	// 第一次扫描未挂载 → 挂载 → 第二次扫描已挂载
	lsblkNo := `{"blockdevices":[{"path":"/dev/sdb1","label":"glkvm","mountpoint":"","children":null}]}`
	lsblkYes := `{"blockdevices":[{"path":"/dev/sdb1","label":"GLKVM","mountpoint":"/media/gl","children":null}]}`
	state := 0
	f := &fakeRunner{handlers: map[string]func([]string) (string, string, error){
		"lsblk": func([]string) (string, string, error) {
			state++
			if state == 1 {
				return lsblkNo, "", nil
			}
			return lsblkYes, "", nil
		},
		"udisksctl": func(args []string) (string, string, error) {
			if strings.Join(args, " ") != "mount -b /dev/sdb1" {
				t.Fatalf("挂载命令 = %v", args)
			}
			return "Mounted /dev/sdb1 at /media/gl", "", nil
		},
	}}
	var logs []string
	got := FindUsbMount("GLKVM", func(s string) { logs = append(logs, s) }, linuxOpts(f))
	if got != "/media/gl" {
		t.Fatalf("挂载后 = %q (logs=%v)", got, logs)
	}
	if !containsStr(logs, "U盘挂载成功") {
		t.Fatalf("缺成功日志: %v", logs)
	}
}

func containsStr(logs []string, sub string) bool {
	for _, l := range logs {
		if strings.Contains(l, sub) {
			return true
		}
	}
	return false
}

func TestFindUsbLinuxNotFound(t *testing.T) {
	lsblk := `{"blockdevices":[{"path":"/dev/sda1","label":"OTHER","mountpoint":"","children":null}]}`
	f := &fakeRunner{handlers: map[string]func([]string) (string, string, error){
		"lsblk": func([]string) (string, string, error) { return lsblk, "", nil },
	}}
	var logs []string
	got := FindUsbMount("GLKVM", func(s string) { logs = append(logs, s) }, linuxOpts(f))
	if got != "" {
		t.Fatalf("未找到应返回空, got %q", got)
	}
	// 重试 3 次：lsblk 调 3 次
	n := 0
	for _, c := range f.calls {
		if strings.HasPrefix(c, "lsblk") {
			n++
		}
	}
	if n != 3 {
		t.Fatalf("lsblk 调用 = %d, want 3", n)
	}
	if !containsStr(logs, "未找到卷标 GLKVM 的U盘设备 (第 3/3 次)") {
		t.Fatalf("日志 = %v", logs)
	}
}

func TestFindUsbLinuxChildDevice(t *testing.T) {
	lsblk := `{"blockdevices":[{"path":"/dev/sda","label":"","mountpoint":"","children":[{"path":"/dev/sda1","label":"GLKVM","mountpoint":"/mnt/u","children":null}]}]}`
	f := &fakeRunner{handlers: map[string]func([]string) (string, string, error){
		"lsblk": func([]string) (string, string, error) { return lsblk, "", nil },
	}}
	if got := FindUsbMount("GLKVM", nil, linuxOpts(f)); got != "/mnt/u" {
		t.Fatalf("子设备 = %q", got)
	}
}

func TestUsbWritableDirFallback(t *testing.T) {
	f := &fakeRunner{handlers: map[string]func([]string) (string, string, error){
		"lsblk": func([]string) (string, string, error) {
			return `{"blockdevices":[]}`, "", nil
		},
	}}
	fb := t.TempDir() + "/fallback"
	dir, source := UsbWritableDir("GLKVM", fb, nil, linuxOpts(f))
	if source != SourceFallback || dir != fb {
		t.Fatalf("fallback = %q/%q", dir, source)
	}
	// 无后备 → none
	dir, source = UsbWritableDir("GLKVM", "", nil, linuxOpts(f))
	if source != SourceNone || dir != "" {
		t.Fatalf("none = %q/%q", dir, source)
	}
}

func TestUsbWritableDirUSB(t *testing.T) {
	f := &fakeRunner{handlers: map[string]func([]string) (string, string, error){
		"powershell": func([]string) (string, string, error) { return "G\r\n", "", nil },
	}}
	opts := &Options{Runner: f, GOOS: "windows", Stat: func(p string) bool { return p == "G:\\" }}
	dir, source := UsbWritableDir("GLKVM", "X:/fb", nil, opts)
	if source != SourceUSB || dir != "G:\\" {
		t.Fatalf("usb = %q/%q", dir, source)
	}
}

func TestSyncFs(t *testing.T) {
	f := &fakeRunner{handlers: map[string]func([]string) (string, string, error){
		"sync": func([]string) (string, string, error) { return "", "", nil },
	}}
	SyncFs(&Options{Runner: f, GOOS: "linux"})
	if len(f.calls) != 1 || f.calls[0] != "sync" {
		t.Fatalf("calls = %v", f.calls)
	}
	// windows 无操作
	SyncFs(&Options{Runner: f, GOOS: "windows"})
	if len(f.calls) != 1 {
		t.Fatal("windows 不应调 sync")
	}
}

func TestSuggestedOutdirsDedupe(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	for _, name := range []string{"Desktop", "Documents"} {
		if err := os.MkdirAll(filepath.Join(home, name), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	f := &fakeRunner{handlers: map[string]func([]string) (string, string, error){}}
	opts := &Options{Runner: f, GOOS: "windows", Stat: func(string) bool { return false }}
	base := filepath.Join(home, "base")
	outs := SuggestedOutdirs("", "", base, opts)
	seen := map[string]bool{}
	for _, d := range outs {
		if seen[d] {
			t.Fatalf("重复目录 %q", d)
		}
		seen[d] = true
	}
	if len(outs) != 3 {
		t.Fatalf("临时 HOME 下应有桌面、文档和项目输出三个建议目录，实际: %v", outs)
	}
	if outs[len(outs)-1] != filepath.Join(base, "output") {
		t.Fatalf("末项应为 output: %v", outs)
	}
}

func TestHomeSubdir(t *testing.T) {
	// 不存在的名字回退 home
	if got := HomeSubdir("__不存在的目录__"); got == "" {
		t.Fatal("应回退 home")
	}
}
