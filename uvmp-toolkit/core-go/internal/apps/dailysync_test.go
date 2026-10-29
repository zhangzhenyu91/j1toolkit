package apps

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zhangzhenyu91/j1toolkit/uvmp-toolkit/core-go/internal/config"
	"github.com/zhangzhenyu91/j1toolkit/uvmp-toolkit/core-go/internal/state"
	"github.com/zhangzhenyu91/j1toolkit/uvmp-toolkit/core-go/internal/usb"
)

// fakeQuerier 脚本化派车单查询。
type fakeQuerier struct {
	items []map[string]any
	err   error
}

func (f fakeQuerier) QueryPcdAllFilter(_ context.Context, _ string, _ int) ([]map[string]any, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.items, nil
}

// fakeUsbOpts windows 路径 + fake PowerShell（返回 drive 指定盘符）。
func fakeUsbOpts(t *testing.T, drive string, exists bool) *usb.Options {
	t.Helper()
	return &usb.Options{
		GOOS: "windows",
		Runner: usbRunner(func(args []string) (string, string, error) {
			if drive == "" {
				return "", "", fmt.Errorf("无U盘")
			}
			return drive + "\r\n", "", nil
		}),
		Stat: func(p string) bool { return exists && p == drive+":\\" },
	}
}

// usbRunner 简单函数式 Runner 适配。
type usbRunner func(args []string) (string, string, error)

func (f usbRunner) Run(_ context.Context, name string, args ...string) (string, string, error) {
	return f(append([]string{name}, args...))
}

// dailyCfg 组一份测试配置。
func dailyCfg(fallback, tempDir string) config.Dict {
	return config.Dict{
		"usb":          config.Dict{"label": "GLKVM", "fallback_dir": fallback},
		"daily_export": config.Dict{"temp_dir": tempDir},
		"schedule":     config.Dict{"enabled": true, "time": "09:15", "driver": "os"},
	}
}

func TestDailySyncRun(t *testing.T) {
	state.SetRoot(t.TempDir())
	fallback := filepath.Join(t.TempDir(), "fb")
	tempDir := filepath.Join(t.TempDir(), "tmp")
	day := "2026-10-28"
	items := []map[string]any{
		{"planSendTime": day + " 08:00:00", "runCode": "PC001", "vehicleNumber": "晋A1", "runState": "2"},
		{"planSendTime": day + " 09:00:00", "runCode": "PC002", "vehicleNumber": "晋A2", "runState": "0"},
		{"dispatchTime": day + " 07:00:00.0", "runCode": "PC003", "vehicleNumber": "晋A3", "runState": "1"},
		{"planSendTime": "2026-10-27 08:00:00", "runCode": "PC901", "vehicleNumber": "晋B1", "runState": "2"}, // 昨日 → 过滤
	}
	b := &dailySyncBackend{
		Querier:  fakeQuerier{items: items},
		UsbOpts:  fakeUsbOpts(t, "", false), // U盘不在 → 走 fallback
		Now:      func() time.Time { return time.Date(2026, 10, 28, 9, 20, 0, 0, time.Local) },
		LoadConf: func() config.Dict { return dailyCfg(fallback, tempDir) },
	}
	j, res, _ := runJobSync(t, b, map[string]any{"date": day})
	if j.Status != "success" {
		t.Fatalf("status = %s error=%s", j.Status, j.Error)
	}
	if res["count"] != 3 {
		t.Fatalf("count = %v（应过滤昨日单）", res["count"])
	}
	if res["dest"] != "后备目录" {
		t.Fatalf("dest = %v", res["dest"])
	}
	file, _ := res["file"].(string)
	if !strings.HasSuffix(file, day+"-派车单.xlsx") {
		t.Fatalf("file = %q", file)
	}
	if _, err := os.Stat(file); err != nil {
		t.Fatalf("交付文件缺失: %v", err)
	}
	// 成功戳
	stamp := state.GetStamp("daily_export")
	if stamp == nil || stamp["date"] != day {
		t.Fatalf("成功戳 = %v", stamp)
	}
	if stamp["count"] != float64(3) {
		t.Fatalf("戳 count = %v", stamp["count"])
	}
}

func TestDailySyncNoUSBNoFallback(t *testing.T) {
	state.SetRoot(t.TempDir())
	b := &dailySyncBackend{
		Querier:  fakeQuerier{},
		UsbOpts:  fakeUsbOpts(t, "", false),
		LoadConf: func() config.Dict { return dailyCfg("", t.TempDir()) },
	}
	j, _, _ := runJobSync(t, b, map[string]any{})
	if j.Status != "failed" || !strings.Contains(j.Error, "U盘不可用且未配置后备目录") {
		t.Fatalf("status=%s error=%s", j.Status, j.Error)
	}
}

func TestDailySyncEmptyItems(t *testing.T) {
	state.SetRoot(t.TempDir())
	fallback := filepath.Join(t.TempDir(), "fb")
	b := &dailySyncBackend{
		Querier:  fakeQuerier{items: nil},
		UsbOpts:  fakeUsbOpts(t, "", false),
		Now:      func() time.Time { return time.Date(2026, 10, 28, 9, 20, 0, 0, time.Local) },
		LoadConf: func() config.Dict { return dailyCfg(fallback, t.TempDir()) },
	}
	j, res, _ := runJobSync(t, b, map[string]any{})
	if j.Status != "success" {
		t.Fatalf("status = %s error=%s", j.Status, j.Error)
	}
	if res["count"] != 0 || res["file"] != "" {
		t.Fatalf("空结果 = %v", res)
	}
	stamp := state.GetStamp("daily_export")
	if stamp == nil || stamp["count"] != float64(0) {
		t.Fatalf("空结果也应写成功戳: %v", stamp)
	}
}

func TestDailySyncUsbReady(t *testing.T) {
	state.SetRoot(t.TempDir())
	// 造一个假 U盘：windows 盘符 X:\ 映射到真实临时目录不可行（盘符硬编码），
	// 这里改走 linux 路径：lsblk 返回挂载点 = 临时目录。
	usbDir := t.TempDir()
	opts := &usb.Options{
		GOOS:     "linux",
		Runner:   linuxLsblkRunner(usbDir),
		Sleep:    func(time.Duration) {},
		Euid:     intPtr(1000),
		LookPath: func(string) (string, error) { return "", fmt.Errorf("nf") },
	}
	b := &dailySyncBackend{
		Querier: fakeQuerier{items: []map[string]any{
			{"planSendTime": "2026-10-28 08:00:00", "runCode": "PC001", "runState": "2"},
		}},
		UsbOpts:  opts,
		Now:      func() time.Time { return time.Date(2026, 10, 28, 9, 20, 0, 0, time.Local) },
		LoadConf: func() config.Dict { return dailyCfg("", t.TempDir()) },
	}
	j, res, _ := runJobSync(t, b, map[string]any{"date": "2026-10-28"})
	if j.Status != "success" {
		t.Fatalf("status = %s error=%s", j.Status, j.Error)
	}
	if res["dest"] != "U盘" || res["dir"] != usbDir {
		t.Fatalf("res = %v", res)
	}
	if _, err := os.Stat(filepath.Join(usbDir, "2026-10-28-派车单.xlsx")); err != nil {
		t.Fatalf("U盘文件缺失: %v", err)
	}
}

// linuxLsblkRunner lsblk 返回固定挂载点、其余命令空转。
func linuxLsblkRunner(mountpoint string) usbRunner {
	return func(args []string) (string, string, error) {
		if len(args) > 0 && args[0] == "lsblk" {
			return fmt.Sprintf(`{"blockdevices":[{"path":"/dev/sda1","label":"GLKVM","mountpoint":%q,"children":null}]}`, mountpoint), "", nil
		}
		return "", "", nil
	}
}

func TestDailySyncStatus(t *testing.T) {
	state.SetRoot(t.TempDir())
	state.SetStamp("daily_export", map[string]any{"date": "2026-10-28", "time": "2026-10-28 09:15:01"})
	b := &dailySyncBackend{UsbOpts: fakeUsbOpts(t, "", false)}
	st, err := b.Status(dailyCfg("", ""))
	if err != nil {
		t.Fatal(err)
	}
	sch := st["schedule"].(map[string]any)
	if sch["time"] != "09:15" || sch["driver"] != "os" {
		t.Fatalf("schedule = %v", sch)
	}
	if st["stamp"] == nil {
		t.Fatal("stamp 应存在")
	}
	due := st["due"].(map[string]any)
	if _, ok := due["reason"]; !ok {
		t.Fatalf("due = %v", due)
	}
}
