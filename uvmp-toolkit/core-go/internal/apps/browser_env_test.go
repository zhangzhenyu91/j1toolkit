package apps

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zhangzhenyu91/j1toolkit/uvmp-toolkit/core-go/internal/config"
	"github.com/zhangzhenyu91/j1toolkit/uvmp-toolkit/core-go/internal/exporter"
	"github.com/zhangzhenyu91/j1toolkit/uvmp-toolkit/core-go/internal/job"
	"github.com/zhangzhenyu91/j1toolkit/uvmp-toolkit/core-go/internal/state"
)

// 宿主浏览器接入测试：导出/自检默认使用宿主浏览器（显式 render.chrome 优先，
// UVMP_BROWSER_BIN 可显式指定），课程 resolveBrowserHost 走同一探测；历史 Electron
// 回退只在显式配置时启用。不访问网络。

// writeFakeFile 造空文件（探测只看存在性）。
func writeFakeFile(t *testing.T, p string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("fake"), 0o755); err != nil {
		t.Fatal(err)
	}
}

// TestPcdRunUsesHostBrowser 进程内导出默认用宿主浏览器（UVMP_BROWSER_BIN 显式指定）：
// ParseRunConfig 直接采用，零 Electron 位置参数；不再注入/还原环境变量。
func TestPcdRunUsesHostBrowser(t *testing.T) {
	state.SetRoot(t.TempDir())
	t.Cleanup(func() { state.SetRoot("") })
	host := filepath.Join(t.TempDir(), "360ent.exe")
	writeFakeFile(t, host)
	t.Setenv("UVMP_BROWSER_BIN", host)
	var gotCfg *exporter.RunConfig
	b := &pcdExportBackend{
		StateSt:  &state.Store{Root: t.TempDir()},
		LoadConf: func() config.Dict { return config.Dict{} },
		BuildDeps: func(ctx context.Context, req exporter.Request, cfg *exporter.RunConfig) (exporter.Deps, error) {
			gotCfg = cfg
			return exporter.Deps{}, errors.New("stop-after-parse")
		},
	}
	j, _, _ := runJobSync(t, b, map[string]any{
		"outdir":       filepath.Join(t.TempDir(), "out"),
		"batch_subdir": false,
		"codes_text":   "PC001",
	})
	if gotCfg == nil {
		t.Fatalf("BuildDeps 未被调用（ParseRunConfig 应已通过）：status=%s err=%s", j.Status, j.Error)
	}
	if gotCfg.BrowserPath != host {
		t.Fatalf("BrowserPath = %q, want %q", gotCfg.BrowserPath, host)
	}
	if gotCfg.NoHeadless || len(gotCfg.BrowserArgs) != 0 {
		t.Fatalf("宿主浏览器应零 Electron 位置参数: %+v", gotCfg)
	}
	if v := os.Getenv("UVMP_BROWSER_BIN"); v != host {
		t.Fatalf("不应改写 UVMP_BROWSER_BIN: %q", v)
	}
}

// TestPcdRunExplicitChrome 显式 render.chrome 优先：BrowserPath 即所选浏览器，
// BrowserArgs 保持零值。
func TestPcdRunExplicitChrome(t *testing.T) {
	state.SetRoot(t.TempDir())
	t.Cleanup(func() { state.SetRoot("") })
	chrome := filepath.Join(t.TempDir(), "mychrome.exe")
	writeFakeFile(t, chrome)
	var gotCfg *exporter.RunConfig
	b := &pcdExportBackend{
		StateSt: &state.Store{Root: t.TempDir()},
		LoadConf: func() config.Dict {
			return config.Dict{"render": config.Dict{"chrome": chrome}}
		},
		BuildDeps: func(ctx context.Context, req exporter.Request, cfg *exporter.RunConfig) (exporter.Deps, error) {
			gotCfg = cfg
			return exporter.Deps{}, errors.New("stop-after-parse")
		},
	}
	runJobSync(t, b, map[string]any{
		"outdir":       filepath.Join(t.TempDir(), "out"),
		"batch_subdir": false,
		"codes_text":   "PC001",
	})
	if gotCfg == nil {
		t.Fatal("BuildDeps 未被调用")
	}
	if gotCfg.BrowserPath != chrome {
		t.Fatalf("BrowserPath = %q, want %q", gotCfg.BrowserPath, chrome)
	}
	if gotCfg.NoHeadless || len(gotCfg.BrowserArgs) != 0 {
		t.Fatalf("显式 render.chrome 应保持零位置参数: %+v", gotCfg)
	}
}

// TestRunCDPSelftestUsesHostBrowser CDP 自检默认走宿主浏览器：假浏览器使实链在启动
// 阶段失败，但错误不应是「未找到可用浏览器」（证明检测已通过）。
func TestRunCDPSelftestUsesHostBrowser(t *testing.T) {
	t.Setenv("UVMP_TOOLKIT_CONFIG", filepath.Join(t.TempDir(), "config.json"))
	t.Setenv("UVMP_TOOLKIT_CONFIG_INI", filepath.Join(t.TempDir(), "none.ini"))
	state.SetRoot(t.TempDir())
	t.Cleanup(func() { state.SetRoot("") })
	host := filepath.Join(t.TempDir(), "360ent.exe")
	writeFakeFile(t, host)
	t.Setenv("UVMP_BROWSER_BIN", host)
	j := &job.Job{}
	_, err := RunSelftest("cdp", j, nil)
	if err == nil {
		t.Fatal("假浏览器应使自检失败（本测试只验证浏览器检测已通过）")
	}
	if strings.Contains(err.Error(), "未找到可用浏览器") {
		t.Fatalf("应已检测到宿主浏览器，错误不应是缺浏览器: %v", err)
	}
	if v := os.Getenv("UVMP_BROWSER_BIN"); v != host {
		t.Fatalf("不应改写 UVMP_BROWSER_BIN: %q", v)
	}
}

// TestResolveElectronLane 历史 Electron 回退宿主：显式 UVMP_ELECTRON 或配置
// browser_automation.electron_path；都没有返回空（不再自动探测 browser-runtime）。
func TestResolveElectronLane(t *testing.T) {
	p := filepath.Join(t.TempDir(), "electron-bin")
	writeFakeFile(t, p)
	t.Setenv("UVMP_ELECTRON", p)
	if got := resolveElectronLane(config.Dict{}); got != p {
		t.Fatalf("显式 UVMP_ELECTRON 未优先: %q", got)
	}
	t.Setenv("UVMP_ELECTRON", "")
	if got := resolveElectronLane(config.Dict{}); got != "" {
		t.Fatalf("未配置应返回空（默认宿主浏览器）: %q", got)
	}
	cfg := config.Dict{"browser_automation": config.Dict{"electron_path": p}}
	if got := resolveElectronLane(cfg); got != p {
		t.Fatalf("配置 electron_path 未生效: %q", got)
	}
}

// TestElectronLaneConfigFallback 环境未注入时按配置的 runtime_dir 组导出口径
// [APP, 开关]；显式 UVMP_BROWSER_WORKER/APP 优先不覆盖。
func TestElectronLaneConfigFallback(t *testing.T) {
	t.Setenv("UVMP_BROWSER_WORKER", "")
	t.Setenv("UVMP_BROWSER_APP", "")
	rt := t.TempDir()
	writeFakeFile(t, filepath.Join(rt, "package.json"))
	cfg := electronLaneConfig("electron-bin", config.Dict{
		"browser_automation": config.Dict{"runtime_dir": rt},
	})
	if len(cfg.BrowserArgs) != 2 || cfg.BrowserArgs[0] != rt ||
		cfg.BrowserArgs[1] != "--uvmp-browser-mode=export" {
		t.Fatalf("BrowserArgs = %v", cfg.BrowserArgs)
	}
	if !cfg.NoHeadless {
		t.Fatal("应 NoHeadless")
	}
	t.Setenv("UVMP_BROWSER_WORKER", "--uvmp-cdp-worker")
	t.Setenv("UVMP_BROWSER_APP", "/explicit/app")
	cfg = electronLaneConfig("electron-bin", config.Dict{})
	if len(cfg.BrowserArgs) != 2 || cfg.BrowserArgs[0] != "/explicit/app" ||
		cfg.BrowserArgs[1] != "--uvmp-cdp-worker" {
		t.Fatalf("显式 env 未优先: %v", cfg.BrowserArgs)
	}
}

// TestResolveBrowserHostHostMode 课程宿主定位：默认返回宿主浏览器路径（runtime 为空），
// 显式 render.chrome 优先；Electron 回退需 electron_path + runtime_dir 成对。
func TestResolveBrowserHostHostMode(t *testing.T) {
	host := filepath.Join(t.TempDir(), "360ent.exe")
	writeFakeFile(t, host)
	t.Setenv("UVMP_BROWSER_BIN", host)
	got, rt, err := resolveBrowserHost(courseSettings{})
	if err != nil {
		t.Fatal(err)
	}
	if got != host || rt != "" {
		t.Fatalf("宿主浏览器 = %q, runtime=%q；want %q, 空", got, rt, host)
	}

	chrome := filepath.Join(t.TempDir(), "chrome.exe")
	writeFakeFile(t, chrome)
	got, rt, err = resolveBrowserHost(courseSettings{Chrome: chrome})
	if err != nil || got != chrome || rt != "" {
		t.Fatalf("显式 render.chrome 优先失败: %q %q %v", got, rt, err)
	}

	electron := filepath.Join(t.TempDir(), "electron")
	writeFakeFile(t, electron)
	runtimeDir := t.TempDir()
	writeFakeFile(t, filepath.Join(runtimeDir, "package.json"))
	writeFakeFile(t, filepath.Join(runtimeDir, "main.cjs"))
	got, rt, err = resolveBrowserHost(courseSettings{ElectronPath: electron, RuntimeDir: runtimeDir})
	if err != nil || got != electron || rt != runtimeDir {
		t.Fatalf("Electron 回退失败: %q %q %v", got, rt, err)
	}
}
