package apps

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zhangzhenyu91/j1toolkit/uvmp-toolkit/core-go/internal/config"
	"github.com/zhangzhenyu91/j1toolkit/uvmp-toolkit/core-go/internal/state"
	"github.com/zhangzhenyu91/j1toolkit/uvmp-toolkit/core-go/internal/usb"
)

// intPtr 测试用 *int。
func intPtr(v int) *int { return &v }

// newTableSyncTest 构造带 fake U 盘的表同步后端（linux lsblk 直达挂载点）。
func newTableSyncTest(t *testing.T, spec tableSyncSpec, usbDir, localPath string) *tableSyncBackend {
	t.Helper()
	opts := &usb.Options{
		GOOS:     "linux",
		Runner:   linuxLsblkRunner(usbDir),
		Sleep:    func(time.Duration) {},
		Euid:     intPtr(1000),
		LookPath: func(string) (string, error) { return "", os.ErrNotExist },
	}
	return &tableSyncBackend{
		spec:    spec,
		UsbOpts: opts,
		Now:     func() time.Time { return time.Date(2026, 10, 28, 23, 56, 0, 0, time.Local) },
		LoadConf: func() config.Dict {
			return config.Dict{
				"usb": config.Dict{"label": "GLKVM"},
				spec.CfgKey: config.Dict{
					"local_path":   localPath,
					"file_name":    spec.DefaultFileName,
					"nightly_time": "23:55",
				},
				"schedule": config.Dict{"enabled": true, "time": "09:15", "driver": "os"},
			}
		},
	}
}

// writeLocal 造内网表。
func writeLocal(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// writeCmd 造 U 盘指令文件。
func writeCmd(t *testing.T, usbDir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(usbDir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestTableSyncUSBAbsent(t *testing.T) {
	state.SetRoot(t.TempDir())
	b := newTableSyncTest(t, treeSyncSpec, "", "")
	b.UsbOpts = fakeUsbOpts(t, "", false) // 找不到 U 盘
	j, res, _ := runJobSync(t, b, map[string]any{"tick": true})
	if j.Status != "success" || res["skipped"] != "usb_absent" {
		t.Fatalf("status=%s res=%v err=%s", j.Status, res, j.Error)
	}
}

func TestTableSyncNoCommand(t *testing.T) {
	state.SetRoot(t.TempDir())
	usbDir := t.TempDir()
	b := newTableSyncTest(t, treeSyncSpec, usbDir, filepath.Join(t.TempDir(), "内网.xlsx"))
	j, res, _ := runJobSync(t, b, map[string]any{"tick": true})
	if j.Status != "success" || res["skipped"] != "no_command" {
		t.Fatalf("status=%s res=%v", j.Status, res)
	}
}

func TestTableSyncSnapshot(t *testing.T) {
	state.SetRoot(t.TempDir())
	usbDir := t.TempDir()
	local := filepath.Join(t.TempDir(), "内网.xlsx")
	writeLocal(t, local, "local-table")
	writeCmd(t, usbDir, treeSyncSpec.CmdName,
		`{"version":1,"cycle_id":"c-1","kind":"snapshot","file":"上盘表.xlsx","issued_at":"x"}`)
	b := newTableSyncTest(t, treeSyncSpec, usbDir, local)
	j, res, _ := runJobSync(t, b, map[string]any{"tick": true})
	if j.Status != "success" {
		t.Fatalf("status=%s error=%s", j.Status, j.Error)
	}
	if res["action"] != "snapshot" || res["cycle_id"] != "c-1" || res["kind"] != "snapshot" {
		t.Fatalf("res = %v", res)
	}
	data, err := os.ReadFile(filepath.Join(usbDir, "上盘表.xlsx"))
	if err != nil || string(data) != "local-table" {
		t.Fatalf("上盘内容 = %q err=%v", data, err)
	}
	// 指令文件已删除
	if _, err := os.Stat(filepath.Join(usbDir, treeSyncSpec.CmdName)); !os.IsNotExist(err) {
		t.Fatal("指令文件应已删除")
	}
}

func TestTableSyncReplace(t *testing.T) {
	state.SetRoot(t.TempDir())
	usbDir := t.TempDir()
	local := filepath.Join(t.TempDir(), "内网.xlsx")
	writeLocal(t, local, "old-table")
	writeCmd(t, usbDir, crossingSyncSpec.CmdName,
		`{"version":1,"cycle_id":"c-2","kind":"replace","file":"新表.xlsx"}`)
	writeLocal(t, filepath.Join(usbDir, "新表.xlsx"), "new-table")
	b := newTableSyncTest(t, crossingSyncSpec, usbDir, local)
	j, res, _ := runJobSync(t, b, map[string]any{"tick": true})
	if j.Status != "success" {
		t.Fatalf("status=%s error=%s", j.Status, j.Error)
	}
	if res["action"] != "replace" || res["src_deleted"] != "新表.xlsx" {
		t.Fatalf("res = %v", res)
	}
	data, _ := os.ReadFile(local)
	if string(data) != "new-table" {
		t.Fatalf("内网表 = %q", data)
	}
	// 备份
	bak, _ := os.ReadFile(local + ".bak")
	if string(bak) != "old-table" {
		t.Fatalf("备份 = %q", bak)
	}
	// 消费完的新表 + 指令文件删除
	if _, err := os.Stat(filepath.Join(usbDir, "新表.xlsx")); !os.IsNotExist(err) {
		t.Fatal("U盘新表应已删除")
	}
	if _, err := os.Stat(filepath.Join(usbDir, crossingSyncSpec.CmdName)); !os.IsNotExist(err) {
		t.Fatal("指令文件应已删除")
	}
}

func TestTableSyncUnknownKindKeepsCmd(t *testing.T) {
	state.SetRoot(t.TempDir())
	usbDir := t.TempDir()
	writeCmd(t, usbDir, treeSyncSpec.CmdName,
		`{"kind":"teleport","cycle_id":"c-3","file":"x.xlsx"}`)
	b := newTableSyncTest(t, treeSyncSpec, usbDir, filepath.Join(t.TempDir(), "内网.xlsx"))
	j, res, _ := runJobSync(t, b, map[string]any{"tick": true})
	if j.Status != "success" || res["skipped"] != "unknown_kind" {
		t.Fatalf("status=%s res=%v", j.Status, res)
	}
	if _, err := os.Stat(filepath.Join(usbDir, treeSyncSpec.CmdName)); err != nil {
		t.Fatal("未知 kind 指令应保留")
	}
}

func TestTableSyncBadFileKeepsCmd(t *testing.T) {
	state.SetRoot(t.TempDir())
	usbDir := t.TempDir()
	writeCmd(t, usbDir, treeSyncSpec.CmdName,
		`{"kind":"snapshot","cycle_id":"c-4","file":"../逃逸.xlsx"}`)
	b := newTableSyncTest(t, treeSyncSpec, usbDir, filepath.Join(t.TempDir(), "内网.xlsx"))
	j, res, _ := runJobSync(t, b, map[string]any{"tick": true})
	if j.Status != "success" || res["skipped"] != "bad_file" {
		t.Fatalf("status=%s res=%v", j.Status, res)
	}
	if _, err := os.Stat(filepath.Join(usbDir, treeSyncSpec.CmdName)); err != nil {
		t.Fatal("非法 file 指令应保留")
	}
}

func TestTableSyncCorruptCmd(t *testing.T) {
	state.SetRoot(t.TempDir())
	usbDir := t.TempDir()
	writeCmd(t, usbDir, treeSyncSpec.CmdName, `{corrupt`)
	b := newTableSyncTest(t, treeSyncSpec, usbDir, filepath.Join(t.TempDir(), "内网.xlsx"))
	j, res, _ := runJobSync(t, b, map[string]any{"tick": true})
	if j.Status != "success" || res["skipped"] != "no_command" {
		t.Fatalf("status=%s res=%v", j.Status, res)
	}
	if _, err := os.Stat(filepath.Join(usbDir, treeSyncSpec.CmdName)); err != nil {
		t.Fatal("损坏指令应保留待人工处理")
	}
}

func TestTableSyncNightlyFallbackSnapshot(t *testing.T) {
	state.SetRoot(t.TempDir())
	usbDir := t.TempDir()
	local := filepath.Join(t.TempDir(), "内网.xlsx")
	writeLocal(t, local, "nightly-table")
	b := newTableSyncTest(t, treeSyncSpec, usbDir, local)
	j, res, _ := runJobSync(t, b, map[string]any{"nightly": true})
	if j.Status != "success" {
		t.Fatalf("status=%s error=%s", j.Status, j.Error)
	}
	if res["action"] != "snapshot" || res["nightly"] != true {
		t.Fatalf("res = %v", res)
	}
	if _, err := os.Stat(filepath.Join(usbDir, treeSyncSpec.DefaultFileName)); err != nil {
		t.Fatalf("夜间兜底应按配置文件名上盘: %v", err)
	}
	// 写成功戳 tree_sync_nightly（不共用其他戳）
	if stamp := state.GetStamp("tree_sync_nightly"); stamp == nil || stamp["date"] != "2026-10-28" {
		t.Fatalf("夜间戳 = %v", stamp)
	}
	if state.GetStamp("crossing_sync_nightly") != nil {
		t.Fatal("不应写交跨戳")
	}
}

func TestTableSyncNightlyMissingLocalPath(t *testing.T) {
	state.SetRoot(t.TempDir())
	usbDir := t.TempDir()
	b := newTableSyncTest(t, treeSyncSpec, usbDir, "")
	j, _, _ := runJobSync(t, b, map[string]any{"nightly": true})
	if j.Status != "failed" || !strings.Contains(j.Error, "tree_sync.local_path") {
		t.Fatalf("status=%s error=%s", j.Status, j.Error)
	}
	// 失败不写成功戳
	if state.GetStamp("tree_sync_nightly") != nil {
		t.Fatal("失败不应写戳")
	}
}

func TestTableSyncCmdMissingLocalPathKeepsCmd(t *testing.T) {
	state.SetRoot(t.TempDir())
	usbDir := t.TempDir()
	writeCmd(t, usbDir, crossingSyncSpec.CmdName,
		`{"kind":"snapshot","cycle_id":"c-5","file":"x.xlsx"}`)
	b := newTableSyncTest(t, crossingSyncSpec, usbDir, "")
	j, _, _ := runJobSync(t, b, map[string]any{"tick": true})
	if j.Status != "failed" || !strings.Contains(j.Error, "crossing_sync.local_path") {
		t.Fatalf("status=%s error=%s", j.Status, j.Error)
	}
	if _, err := os.Stat(filepath.Join(usbDir, crossingSyncSpec.CmdName)); err != nil {
		t.Fatal("配置缺失时指令应保留（补齐后自动补执行）")
	}
}

func TestTableSyncStatus(t *testing.T) {
	state.SetRoot(t.TempDir())
	usbDir := t.TempDir()
	local := filepath.Join(t.TempDir(), "内网.xlsx")
	writeLocal(t, local, "x")
	b := newTableSyncTest(t, treeSyncSpec, usbDir, local)
	st, err := b.Status(b.LoadConf())
	if err != nil {
		t.Fatal(err)
	}
	ts := st["tree_sync"].(map[string]any)
	if ts["local_exists"] != true || ts["file_name"] != treeSyncSpec.DefaultFileName {
		t.Fatalf("tree_sync = %v", ts)
	}
	if st["usb"].(map[string]any)["mount"] != usbDir {
		t.Fatalf("usb = %v", st["usb"])
	}
}
