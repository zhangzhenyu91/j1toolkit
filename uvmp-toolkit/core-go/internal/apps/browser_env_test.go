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

// 浏览器运行时统一自发现接入测试：模拟安装布局（假 Electron/运行时应用目录），
// 验证导出/自检自动注入内置浏览器环境（显式优先不覆盖、任务结束 restore）与
// 课程 resolveBrowserHost 共用同一探测；不访问网络。

// fakeElectronName 测试平台的宿主可执行名。
func fakeElectronName() string {
	if os.PathSeparator == '\\' {
		return "electron.exe"
	}
	return "electron"
}

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

// fakeFyneLayout 模拟 fyne 安装布局：browser-runtime/electron(.exe) +
// browser-runtime/app/{package.json,main.cjs}，返回布局根。
func fakeFyneLayout(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	for _, n := range []string{"package.json", "main.cjs"} {
		writeFakeFile(t, filepath.Join(root, "browser-runtime", "app", n))
	}
	writeFakeFile(t, filepath.Join(root, "browser-runtime", fakeElectronName()))
	return root
}

// fakeSourceLayout 模拟开发树：browser-runtime/{package.json,main.cjs} +
// desktop/node_modules/electron/dist/electron(.exe)，返回布局根。
func fakeSourceLayout(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	for _, n := range []string{"package.json", "main.cjs"} {
		writeFakeFile(t, filepath.Join(root, "browser-runtime", n))
	}
	writeFakeFile(t, filepath.Join(root, "desktop", "node_modules", "electron", "dist", fakeElectronName()))
	return root
}

// clearBrowserEnv 清空三件套（t.Setenv 统一还原）。
func clearBrowserEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{"UVMP_BROWSER_BIN", "UVMP_BROWSER_WORKER", "UVMP_BROWSER_APP"} {
		t.Setenv(k, "")
	}
}

// TestPcdRunInjectsBrowserEnv 进程内导出自动注入内置浏览器环境，且注入先于
// exporter.ParseRunConfig（BuildDeps 仅在解析成功后调用，其 cfg 即注入结果）；
// 任务结束环境已 restore。
func TestPcdRunInjectsBrowserEnv(t *testing.T) {
	state.SetRoot(t.TempDir())
	t.Cleanup(func() { state.SetRoot("") })
	root := fakeFyneLayout(t)
	t.Chdir(root)
	clearBrowserEnv(t)
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
	wantBin := filepath.Join(root, "browser-runtime", fakeElectronName())
	if gotCfg.BrowserPath != wantBin {
		t.Fatalf("BrowserPath = %q, want %q", gotCfg.BrowserPath, wantBin)
	}
	if !gotCfg.NoHeadless {
		t.Fatal("内置 Electron 应 NoHeadless")
	}
	wantApp := filepath.Join(root, "browser-runtime", "app")
	if len(gotCfg.BrowserArgs) != 2 || gotCfg.BrowserArgs[0] != wantApp ||
		gotCfg.BrowserArgs[1] != "--uvmp-browser-mode=export" {
		t.Fatalf("BrowserArgs = %v, want [%s %s]", gotCfg.BrowserArgs, wantApp, "--uvmp-browser-mode=export")
	}
	if v := os.Getenv("UVMP_BROWSER_BIN"); v != "" {
		t.Fatalf("任务结束未 restore: BIN=%q", v)
	}
}

// TestPcdRunExplicitChromeNoInject 显式 render.chrome 优先：不注入环境、
// BrowserArgs 保持零值（旧行为不变）。
func TestPcdRunExplicitChromeNoInject(t *testing.T) {
	state.SetRoot(t.TempDir())
	t.Cleanup(func() { state.SetRoot("") })
	root := fakeFyneLayout(t)
	t.Chdir(root)
	clearBrowserEnv(t)
	chrome := filepath.Join(root, "mychrome.exe")
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
	for _, k := range []string{"UVMP_BROWSER_BIN", "UVMP_BROWSER_WORKER", "UVMP_BROWSER_APP"} {
		if v := os.Getenv(k); v != "" {
			t.Fatalf("显式 render.chrome 不应注入 %s=%q", k, v)
		}
	}
}

// TestRunCDPSelftestInjectsEnv 内置 CDP 渲染自检自动注入（ParseSelftestConfig 之前）：
// 假 Electron 使实链自检在启动阶段失败，但错误不应是「未找到内置浏览器」；
// 结束后环境已 restore。
func TestRunCDPSelftestInjectsEnv(t *testing.T) {
	t.Setenv("UVMP_TOOLKIT_CONFIG", filepath.Join(t.TempDir(), "config.json"))
	t.Setenv("UVMP_TOOLKIT_CONFIG_INI", filepath.Join(t.TempDir(), "none.ini"))
	state.SetRoot(t.TempDir())
	t.Cleanup(func() { state.SetRoot("") })
	root := fakeFyneLayout(t)
	t.Chdir(root)
	clearBrowserEnv(t)
	j := &job.Job{}
	_, err := RunSelftest("cdp", j, nil)
	if err == nil {
		t.Fatal("假 Electron 应使自检失败（本测试只验证注入先于 ParseSelftestConfig）")
	}
	if strings.Contains(err.Error(), "未找到内置浏览器") || strings.Contains(err.Error(), "UVMP_BROWSER_BIN") {
		t.Fatalf("应已注入内置浏览器环境，错误不应是缺浏览器: %v", err)
	}
	if v := os.Getenv("UVMP_BROWSER_BIN"); v != "" {
		t.Fatalf("任务结束未 restore: BIN=%q", v)
	}
}

// TestResolveElectronLane 自带 Electron 通道宿主：显式 UVMP_ELECTRON 优先，
// 否则统一探测（模拟安装布局），都没有返回空（调用方跳过）。
func TestResolveElectronLane(t *testing.T) {
	p := filepath.Join(t.TempDir(), "electron-bin")
	writeFakeFile(t, p)
	t.Setenv("UVMP_ELECTRON", p)
	if got := resolveElectronLane(); got != p {
		t.Fatalf("显式 UVMP_ELECTRON 未优先: %q", got)
	}
	root := fakeFyneLayout(t)
	t.Chdir(root)
	t.Setenv("UVMP_ELECTRON", "")
	want := filepath.Join(root, "browser-runtime", fakeElectronName())
	if got := resolveElectronLane(); got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
	t.Chdir(t.TempDir())
	if got := resolveElectronLane(); got != "" {
		t.Fatalf("无宿主应返回空: %q", got)
	}
}

// TestElectronLaneConfigFallback 环境未注入时按统一探测组导出口径 [APP, 开关]；
// 显式 UVMP_BROWSER_WORKER/APP 优先不覆盖。
func TestElectronLaneConfigFallback(t *testing.T) {
	root := fakeFyneLayout(t)
	t.Chdir(root)
	t.Setenv("UVMP_ELECTRON", "")
	t.Setenv("UVMP_BROWSER_WORKER", "")
	t.Setenv("UVMP_BROWSER_APP", "")
	cfg := electronLaneConfig(resolveElectronLane())
	wantApp := filepath.Join(root, "browser-runtime", "app")
	if len(cfg.BrowserArgs) != 2 || cfg.BrowserArgs[0] != wantApp ||
		cfg.BrowserArgs[1] != "--uvmp-browser-mode=export" {
		t.Fatalf("BrowserArgs = %v", cfg.BrowserArgs)
	}
	if !cfg.NoHeadless {
		t.Fatal("应 NoHeadless")
	}
	t.Setenv("UVMP_BROWSER_WORKER", "--uvmp-cdp-worker")
	t.Setenv("UVMP_BROWSER_APP", "/explicit/app")
	cfg = electronLaneConfig(resolveElectronLane())
	if len(cfg.BrowserArgs) != 2 || cfg.BrowserArgs[0] != "/explicit/app" ||
		cfg.BrowserArgs[1] != "--uvmp-cdp-worker" {
		t.Fatalf("显式 env 未优先: %v", cfg.BrowserArgs)
	}
}

// TestResolveBrowserHostDetectsLayouts 课程宿主定位共用统一探测：fyne 安装布局
// 返回 browser-runtime/app 应用目录（修复旧口径指到 browser-runtime 的不兼容），
// 开发树返回源码 browser-runtime。
func TestResolveBrowserHostDetectsLayouts(t *testing.T) {
	setupCourseTest(t, `{}`)
	root := fakeFyneLayout(t)
	t.Chdir(root)
	electron, runtimeDir, err := resolveBrowserHost(courseSettings{})
	if err != nil {
		t.Fatal(err)
	}
	if electron != filepath.Join(root, "browser-runtime", fakeElectronName()) {
		t.Fatalf("electron = %q", electron)
	}
	if want := filepath.Join(root, "browser-runtime", "app"); runtimeDir != want {
		t.Fatalf("runtimeDir = %q, want %q", runtimeDir, want)
	}

	setupCourseTest(t, `{}`)
	root2 := fakeSourceLayout(t)
	t.Chdir(root2)
	electron, runtimeDir, err = resolveBrowserHost(courseSettings{})
	if err != nil {
		t.Fatal(err)
	}
	if electron != filepath.Join(root2, "desktop", "node_modules", "electron", "dist", fakeElectronName()) {
		t.Fatalf("electron = %q", electron)
	}
	if want := filepath.Join(root2, "browser-runtime"); runtimeDir != want {
		t.Fatalf("runtimeDir = %q, want %q", runtimeDir, want)
	}
}
