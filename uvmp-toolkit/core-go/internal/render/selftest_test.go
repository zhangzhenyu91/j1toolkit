package render

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestParseSelftestConfig 浏览器口径与 exporter.ParseRunConfig 对齐：
// 显式 render.chrome 优先（零位置参数），render.builtin=true 为历史 Electron 回退
// （UVMP_BROWSER_BIN + WORKER/APP），默认宿主浏览器自动检测（UVMP_BROWSER_BIN 可显式
// 指定）。纯逻辑，文件系统只用临时目录。

func TestParseSelftestConfigExplicitChrome(t *testing.T) {
	chrome := filepath.Join(t.TempDir(), "chrome.exe")
	if err := os.WriteFile(chrome, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(t.TempDir(), "electron.exe")
	if err := os.WriteFile(bin, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	env := map[string]string{"UVMP_BROWSER_BIN": bin, "UVMP_BROWSER_WORKER": "--uvmp-cdp-worker"}
	cfg, err := ParseSelftestConfig(map[string]any{
		"render": map[string]any{"chrome": chrome},
	}, func(k string) string { return env[k] }, t.TempDir())
	if err != nil {
		t.Fatalf("显式 render.chrome 应可用: %v", err)
	}
	if cfg.BrowserPath != chrome {
		t.Fatalf("BrowserPath = %q；want 显式 render.chrome %q", cfg.BrowserPath, chrome)
	}
	if len(cfg.BrowserArgs) != 0 || cfg.NoHeadless {
		t.Fatalf("显式 render.chrome 应零位置参数/headless：%+v", cfg)
	}
}

func TestParseSelftestConfigIniChrome(t *testing.T) {
	chrome := filepath.Join(t.TempDir(), "chrome")
	if err := os.WriteFile(chrome, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg, err := ParseSelftestConfig(map[string]any{
		"_ini": map[string]any{"render": map[string]any{"chrome": chrome}},
	}, func(string) string { return "" }, t.TempDir())
	if err != nil {
		t.Fatalf("config.ini [render] chrome 应可用: %v", err)
	}
	if cfg.BrowserPath != chrome {
		t.Fatalf("BrowserPath = %q；want %q", cfg.BrowserPath, chrome)
	}
}

// TestParseSelftestConfigHostDetect 默认（builtin=false）宿主浏览器自动检测：
// UVMP_BROWSER_BIN 显式指定 → 直接采用，零位置参数/headless。
func TestParseSelftestConfigHostDetect(t *testing.T) {
	host := filepath.Join(t.TempDir(), "360ent.exe")
	if err := os.WriteFile(host, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	env := map[string]string{"UVMP_BROWSER_BIN": host}
	cfg, err := ParseSelftestConfig(map[string]any{"render": map[string]any{}},
		func(k string) string { return env[k] }, t.TempDir())
	if err != nil {
		t.Fatalf("宿主浏览器应可用: %v", err)
	}
	if cfg.BrowserPath != host {
		t.Fatalf("BrowserPath = %q；want %q", cfg.BrowserPath, host)
	}
	if cfg.NoHeadless || len(cfg.BrowserArgs) != 0 {
		t.Fatalf("宿主浏览器应零位置参数：%+v", cfg)
	}
}

// TestParseSelftestConfigWorkerEnvLegacy 显式 UVMP_BROWSER_WORKER（CI/门禁脚本按旧口径
// 注入）即使未开 builtin 也按 Electron 回退处理：组 worker 位置参数并去掉 headless。
func TestParseSelftestConfigWorkerEnvLegacy(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "electron.exe")
	if err := os.WriteFile(bin, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	env := map[string]string{
		"UVMP_BROWSER_BIN":    bin,
		"UVMP_BROWSER_WORKER": "--uvmp-browser-mode=export",
		"UVMP_BROWSER_APP":    filepath.Join(dir, "browser-runtime", "app"),
	}
	cfg, err := ParseSelftestConfig(map[string]any{}, func(k string) string { return env[k] }, dir)
	if err != nil {
		t.Fatalf("worker 显式注入应可用: %v", err)
	}
	if cfg.BrowserPath != bin || !cfg.NoHeadless ||
		len(cfg.BrowserArgs) != 2 || cfg.BrowserArgs[0] != env["UVMP_BROWSER_APP"] ||
		cfg.BrowserArgs[1] != "--uvmp-browser-mode=export" {
		t.Fatalf("Electron 回退参数不符: %+v", cfg)
	}
}

func TestParseSelftestConfigBuiltinWorker(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "electron.exe")
	if err := os.WriteFile(bin, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	env := map[string]string{
		"UVMP_BROWSER_BIN":    bin,
		"UVMP_BROWSER_WORKER": "--uvmp-cdp-worker",
		"UVMP_BROWSER_APP":    filepath.Join(dir, "desktop"),
	}
	cfg, err := ParseSelftestConfig(map[string]any{"render": map[string]any{"builtin": true}},
		func(k string) string { return env[k] }, dir)
	if err != nil {
		t.Fatalf("Electron 回退 UVMP_BROWSER_BIN 应可用: %v", err)
	}
	if cfg.BrowserPath != bin {
		t.Fatalf("BrowserPath = %q；want %q", cfg.BrowserPath, bin)
	}
	wantArgs := []string{env["UVMP_BROWSER_APP"], "--uvmp-cdp-worker"}
	if len(cfg.BrowserArgs) != 2 || cfg.BrowserArgs[0] != wantArgs[0] || cfg.BrowserArgs[1] != wantArgs[1] {
		t.Fatalf("BrowserArgs = %v；want %v", cfg.BrowserArgs, wantArgs)
	}
	if !cfg.NoHeadless {
		t.Fatal("Electron 回退应 NoHeadless")
	}
}

func TestParseSelftestConfigBuiltinNoApp(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "electron")
	if err := os.WriteFile(bin, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	env := map[string]string{
		"UVMP_BROWSER_BIN":    bin,
		"UVMP_BROWSER_WORKER": "--uvmp-cdp-worker",
	}
	cfg, err := ParseSelftestConfig(map[string]any{"render": map[string]any{"builtin": true}},
		func(k string) string { return env[k] }, dir)
	if err != nil {
		t.Fatalf("打包版（无 APP）应可用: %v", err)
	}
	if len(cfg.BrowserArgs) != 1 || cfg.BrowserArgs[0] != "--uvmp-cdp-worker" {
		t.Fatalf("BrowserArgs = %v；want [--uvmp-cdp-worker]", cfg.BrowserArgs)
	}
}

func TestParseSelftestConfigErrors(t *testing.T) {
	dir := t.TempDir()
	env := map[string]string{}
	// 无产物目录
	if _, err := ParseSelftestConfig(nil, func(string) string { return "" }, ""); err == nil ||
		!strings.Contains(err.Error(), "UVMP_SELFTEST_DIR") {
		t.Fatalf("缺 UVMP_SELFTEST_DIR 应明确报错: %v", err)
	}
	// Electron 回退（builtin=true）: UVMP_BROWSER_BIN 指向不存在文件
	env["UVMP_BROWSER_BIN"] = filepath.Join(dir, "missing-electron")
	_, err := ParseSelftestConfig(map[string]any{"render": map[string]any{"builtin": true}},
		func(k string) string { return env[k] }, dir)
	if err == nil || !strings.Contains(err.Error(), "UVMP_BROWSER_BIN") {
		t.Fatalf("builtin + 不存在的 BIN 应明确报错: %v", err)
	}
	env["UVMP_BROWSER_BIN"] = ""
	// 显式 chrome 不存在
	_, err = ParseSelftestConfig(map[string]any{
		"render": map[string]any{"chrome": filepath.Join(dir, "missing.exe")},
	}, func(k string) string { return env[k] }, dir)
	if err == nil || !strings.Contains(err.Error(), "指定的浏览器不存在") {
		t.Fatalf("chrome 不存在应明确报错: %v", err)
	}
	// 宿主检测：UVMP_BROWSER_BIN 不存在
	env["UVMP_BROWSER_BIN"] = filepath.Join(dir, "missing-browser")
	_, err = ParseSelftestConfig(nil, func(k string) string { return env[k] }, dir)
	if err == nil || !strings.Contains(err.Error(), "UVMP_BROWSER_BIN") {
		t.Fatalf("UVMP_BROWSER_BIN 不存在应明确报错: %v", err)
	}
}

func TestLocalPageURL(t *testing.T) {
	u := localPageURL("main-page", "hello")
	if !strings.HasPrefix(u, "data:text/html;base64,") {
		t.Fatalf("localPageURL = %q；want data: 本地页", u)
	}
	if strings.Contains(u, "http") {
		t.Fatalf("自检页不得出现网络地址: %q", u)
	}
}
