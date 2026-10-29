package browser

import (
	"os"
	"path/filepath"
	"testing"
)

// 导出/自检浏览器环境变量注入测试：显式优先不覆盖、restore 精确还原、
// 模拟安装布局自动补齐；不访问网络。

// clearBrowserEnv 清空三件套（t.Setenv 统一还原）。
func clearBrowserEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{EnvBrowserBin, EnvBrowserWorker, EnvBrowserApp} {
		t.Setenv(k, "")
	}
}

func TestEnsureExportEnvInjects(t *testing.T) {
	root := fakeFyneLayout(t)
	clearBrowserEnv(t)
	restore := ensureExportEnv([]string{root}, "", os.LookupEnv, os.Setenv, os.Unsetenv)
	defer restore()
	wantBin := filepath.Join(root, "browser-runtime", fakeElectronName())
	wantApp := filepath.Join(root, "browser-runtime", "app")
	if got := os.Getenv(EnvBrowserBin); got != wantBin {
		t.Fatalf("BIN = %q, want %q", got, wantBin)
	}
	if got := os.Getenv(EnvBrowserWorker); got != ExportWorkerFlag {
		t.Fatalf("WORKER = %q, want %q", got, ExportWorkerFlag)
	}
	if got := os.Getenv(EnvBrowserApp); got != wantApp {
		t.Fatalf("APP = %q, want %q", got, wantApp)
	}
	restore()
	for _, k := range []string{EnvBrowserBin, EnvBrowserWorker, EnvBrowserApp} {
		if got := os.Getenv(k); got != "" {
			t.Fatalf("restore 后 %s = %q，应还原为空", k, got)
		}
	}
}

func TestEnsureExportEnvExplicitNotOverridden(t *testing.T) {
	root := fakeFyneLayout(t)
	clearBrowserEnv(t)
	t.Setenv(EnvBrowserBin, "custom-browser")
	restore := ensureExportEnv([]string{root}, "", os.LookupEnv, os.Setenv, os.Unsetenv)
	defer restore()
	// 显式 BIN 不覆盖，也不注入派生 WORKER/APP（goexport.derive_browser_env 同口径）
	if got := os.Getenv(EnvBrowserBin); got != "custom-browser" {
		t.Fatalf("BIN 被覆盖: %q", got)
	}
	if got := os.Getenv(EnvBrowserWorker); got != "" {
		t.Fatalf("显式 BIN 不应派生 WORKER: %q", got)
	}
	if got := os.Getenv(EnvBrowserApp); got != "" {
		t.Fatalf("显式 BIN 不应派生 APP: %q", got)
	}
}

func TestEnsureExportEnvExplicitWorkerKept(t *testing.T) {
	root := fakeFyneLayout(t)
	clearBrowserEnv(t)
	t.Setenv(EnvBrowserWorker, ExportWorkerFlag)
	restore := ensureExportEnv([]string{root}, "", os.LookupEnv, os.Setenv, os.Unsetenv)
	defer restore()
	// 显式 WORKER（=导出开关）不覆盖；BIN 派生、APP 按开关补齐
	wantBin := filepath.Join(root, "browser-runtime", fakeElectronName())
	if got := os.Getenv(EnvBrowserBin); got != wantBin {
		t.Fatalf("BIN = %q, want %q", got, wantBin)
	}
	if got := os.Getenv(EnvBrowserWorker); got != ExportWorkerFlag {
		t.Fatalf("WORKER 被覆盖: %q", got)
	}
	wantApp := filepath.Join(root, "browser-runtime", "app")
	if got := os.Getenv(EnvBrowserApp); got != wantApp {
		t.Fatalf("APP = %q, want %q", got, wantApp)
	}
}

func TestEnsureExportEnvExplicitChrome(t *testing.T) {
	root := fakeFyneLayout(t)
	clearBrowserEnv(t)
	restore := ensureExportEnv([]string{root}, filepath.Join(root, "chrome.exe"), os.LookupEnv, os.Setenv, os.Unsetenv)
	defer restore()
	// 显式 render.chrome 优先：完全不注入
	for _, k := range []string{EnvBrowserBin, EnvBrowserWorker, EnvBrowserApp} {
		if got := os.Getenv(k); got != "" {
			t.Fatalf("显式 render.chrome 不应注入 %s: %q", k, got)
		}
	}
}

func TestEnsureExportEnvNotFound(t *testing.T) {
	clearBrowserEnv(t)
	restore := ensureExportEnv([]string{t.TempDir()}, "", os.LookupEnv, os.Setenv, os.Unsetenv)
	defer restore()
	// 探测不到完整运行时：不注入，由下游按既有口径报错
	for _, k := range []string{EnvBrowserBin, EnvBrowserWorker, EnvBrowserApp} {
		if got := os.Getenv(k); got != "" {
			t.Fatalf("未探测到不应注入 %s: %q", k, got)
		}
	}
}

func TestEnsureExportEnvRestoreUnset(t *testing.T) {
	root := fakeFyneLayout(t)
	clearBrowserEnv(t)
	// BIN 原本「未设」：restore 应恢复为未设（而非设为空串）
	if err := os.Unsetenv(EnvBrowserBin); err != nil {
		t.Fatal(err)
	}
	restore := ensureExportEnv([]string{root}, "", os.LookupEnv, os.Setenv, os.Unsetenv)
	if os.Getenv(EnvBrowserBin) == "" {
		t.Fatal("应已注入 BIN")
	}
	restore()
	if v, ok := os.LookupEnv(EnvBrowserBin); ok {
		t.Fatalf("restore 后 BIN 应未设，实为 %q", v)
	}
}

func TestEnsureExportEnvPublicAPI(t *testing.T) {
	// 公开入口（默认搜索根=exe+cwd）：模拟安装布局经 cwd 命中
	root := fakeFyneLayout(t)
	t.Chdir(root)
	clearBrowserEnv(t)
	restore := EnsureExportEnv("")
	defer restore()
	wantBin := filepath.Join(root, "browser-runtime", fakeElectronName())
	if got := os.Getenv(EnvBrowserBin); got != wantBin {
		t.Fatalf("BIN = %q, want %q", got, wantBin)
	}
}

func TestExplicitChrome(t *testing.T) {
	cases := []struct {
		name string
		cfg  map[string]any
		want string
	}{
		{"空配置", map[string]any{}, ""},
		{"json 手选", map[string]any{"render": map[string]any{"chrome": "C:\\a.exe"}}, "C:\\a.exe"},
		{"ini 回退", map[string]any{"_ini": map[string]any{"render": map[string]any{"chrome": "/opt/b"}}}, "/opt/b"},
		{
			"json 优先于 ini",
			map[string]any{
				"render": map[string]any{"chrome": "json.exe"},
				"_ini":   map[string]any{"render": map[string]any{"chrome": "ini.exe"}},
			},
			"json.exe",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := ExplicitChrome(c.cfg); got != c.want {
				t.Fatalf("got %q, want %q", got, c.want)
			}
		})
	}
}

func TestEnsureExportEnvNoopRestore(t *testing.T) {
	// 不注入时 restore 为安全空操作
	restore := ensureExportEnv([]string{t.TempDir()}, "chrome", os.LookupEnv, os.Setenv, os.Unsetenv)
	if restore == nil {
		t.Fatal("restore 永不为 nil")
	}
	restore()
}
