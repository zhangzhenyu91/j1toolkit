package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/zhangzhenyu91/j1toolkit/uvmp-toolkit/core-go/internal/apps"
	"github.com/zhangzhenyu91/j1toolkit/uvmp-toolkit/core-go/internal/config"
	"github.com/zhangzhenyu91/j1toolkit/uvmp-toolkit/core-go/internal/job"
	"github.com/zhangzhenyu91/j1toolkit/uvmp-toolkit/core-go/internal/state"
)

// fakeBackend 测试应用后端。
type fakeBackend struct{ fail bool }

func (f fakeBackend) Run(j *job.Job, params map[string]any) (map[string]any, error) {
	j.Log("cli 测试日志")
	if f.fail {
		return nil, os.ErrInvalid
	}
	return map[string]any{"ok": true}, nil
}

// setup 隔离状态/配置环境 + 注册测试应用。
func setup(t *testing.T, enabled bool, tm, driver string) config.Dict {
	t.Helper()
	state.SetRoot(t.TempDir())
	t.Setenv("UVMP_TOOLKIT_CONFIG", filepath.Join(t.TempDir(), "config.json"))
	t.Setenv("UVMP_TOOLKIT_CONFIG_INI", filepath.Join(t.TempDir(), "no.ini"))
	apps.Reset()
	apps.Register(&apps.App{ID: "daily_sync", Name: "派车同步", Backend: fakeBackend{}})
	apps.Register(&apps.App{ID: "tree_sync", Name: "树障同步", Backend: fakeBackend{}})
	apps.Register(&apps.App{ID: "crossing_sync", Name: "交跨同步", Backend: fakeBackend{}})
	apps.Register(&apps.App{ID: "pcd_export", Name: "轨迹导出", Backend: fakeBackend{fail: true}})
	return config.Dict{
		"schedule": config.Dict{"enabled": enabled, "time": tm, "driver": driver},
	}
}

func TestCmdSelfcheck(t *testing.T) {
	setup(t, true, "09:15", "os")
	apps.Reset()
	if code := cmdSelfcheck(); code != 1 {
		t.Fatalf("空注册表应失败, code=%d", code)
	}
	apps.Register(&apps.App{ID: "x", Name: "X", Backend: fakeBackend{}})
	if code := cmdSelfcheck(); code != 0 {
		t.Fatalf("应通过, code=%d", code)
	}
}

func TestRunHeadless(t *testing.T) {
	setup(t, true, "09:15", "os")
	if code := runHeadless("daily_sync", "每日派车单同步", map[string]any{}); code != 0 {
		t.Fatalf("成功任务 exit=%d", code)
	}
	if code := runHeadless("pcd_export", "轨迹导出", map[string]any{}); code != 1 {
		t.Fatalf("失败任务 exit=%d", code)
	}
	if code := runHeadless("不存在", "x", nil); code != 1 {
		t.Fatalf("缺应用 exit=%d", code)
	}
}

func TestCmdDailyExportIfDueSkip(t *testing.T) {
	cfg := setup(t, true, "23:59", "os") // 未到点
	if code := cmdDailyExport(cfg, []string{"--if-due"}); code != 0 {
		t.Fatalf("未到点应 exit 0, code=%d", code)
	}
	// 停用
	cfg = setup(t, false, "00:00", "os")
	if code := cmdDailyExport(cfg, []string{"--if-due"}); code != 0 {
		t.Fatalf("停用应 exit 0, code=%d", code)
	}
}

func TestCmdDailyExportIfDueRun(t *testing.T) {
	cfg := setup(t, true, "00:00", "os") // 已过点
	if code := cmdDailyExport(cfg, []string{"--if-due", "--date", "2026-10-28"}); code != 0 {
		t.Fatalf("到点应执行, code=%d", code)
	}
	// 尝试戳已写（失败重试间隔闸）
	if state.GetStamp("daily_export_attempt") == nil {
		t.Fatal("应写尝试戳")
	}
}

func TestCmdDailyExportDriverGate(t *testing.T) {
	cfg := setup(t, true, "00:00", "app") // 客户端驻留调度：OS 定时器跳过
	if code := cmdDailyExport(cfg, []string{"--if-due"}); code != 0 {
		t.Fatalf("driver 门闸应 exit 0, code=%d", code)
	}
}

func TestCmdTableSyncNightlySkip(t *testing.T) {
	cfg := setup(t, true, "00:00", "os")
	// 未配置 local_path → 跳过
	if code := cmdTableSync(cfg, "tree_sync", []string{"--nightly", "--if-due"}); code != 0 {
		t.Fatalf("未配置应跳过, code=%d", code)
	}
	// 配置后到点执行
	cfg["tree_sync"] = config.Dict{"local_path": filepath.Join(t.TempDir(), "a.xlsx"), "nightly_time": "00:00"}
	if code := cmdTableSync(cfg, "tree_sync", []string{"--nightly", "--if-due"}); code != 0 {
		t.Fatalf("到点应执行, code=%d", code)
	}
	if state.GetStamp("tree_sync_nightly_attempt") == nil {
		t.Fatal("应写夜间尝试戳")
	}
}

func TestCmdTableSyncTick(t *testing.T) {
	cfg := setup(t, true, "00:00", "os")
	if code := cmdTableSync(cfg, "crossing_sync", []string{"--tick"}); code != 0 {
		t.Fatalf("tick 应执行, code=%d", code)
	}
}

func TestCmdOrderExportValidation(t *testing.T) {
	cfg := setup(t, true, "00:00", "os")
	// 缺 outdir
	if code := cmdOrderExport(cfg, []string{"--codes", "PC001"}); code != 2 {
		t.Fatalf("缺 outdir exit=%d", code)
	}
	// 缺单号来源
	if code := cmdOrderExport(cfg, []string{"--outdir", t.TempDir()}); code != 2 {
		t.Fatalf("缺来源 exit=%d", code)
	}
	// 非法 type
	if code := cmdOrderExport(cfg, []string{"--outdir", t.TempDir(), "--codes", "PC001", "--type", "bad"}); code != 2 {
		t.Fatalf("非法 type exit=%d", code)
	}
}

func TestCmdOrderExportRunsApp(t *testing.T) {
	cfg := setup(t, true, "00:00", "os")
	// 后端失败 → exit 1（走完参数解析与任务包装）
	if code := cmdOrderExport(cfg, []string{
		"--outdir", t.TempDir(), "--codes", "PC001", "--type", "pcd",
	}); code != 1 {
		t.Fatalf("失败任务 exit=%d", code)
	}
}
