package render

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestParseSelftestConfig 浏览器口径与 exporter.ParseRunConfig 对齐：
// 显式 render.chrome 优先（普通 Chromium 零位置参数），否则内置 UVMP_BROWSER_BIN
// 配 UVMP_BROWSER_WORKER/APP；缺浏览器明确报错。纯逻辑，文件系统只用临时目录。

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
	cfg, err := ParseSelftestConfig(nil, func(k string) string { return env[k] }, dir)
	if err != nil {
		t.Fatalf("内置 UVMP_BROWSER_BIN 应可用: %v", err)
	}
	if cfg.BrowserPath != bin {
		t.Fatalf("BrowserPath = %q；want %q", cfg.BrowserPath, bin)
	}
	wantArgs := []string{env["UVMP_BROWSER_APP"], "--uvmp-cdp-worker"}
	if len(cfg.BrowserArgs) != 2 || cfg.BrowserArgs[0] != wantArgs[0] || cfg.BrowserArgs[1] != wantArgs[1] {
		t.Fatalf("BrowserArgs = %v；want %v", cfg.BrowserArgs, wantArgs)
	}
	if !cfg.NoHeadless {
		t.Fatal("内置 worker 应 NoHeadless")
	}
}

func TestParseSelftestConfigBuiltinNoApp(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "electron")
	if err := os.WriteFile(bin, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	env := map[string]string{"UVMP_BROWSER_BIN": bin, "UVMP_BROWSER_WORKER": "--uvmp-cdp-worker"}
	cfg, err := ParseSelftestConfig(map[string]any{}, func(k string) string { return env[k] }, dir)
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
	// 无浏览器
	_, err := ParseSelftestConfig(nil, func(k string) string { return env[k] }, dir)
	if err == nil || !strings.Contains(err.Error(), "内置浏览器") {
		t.Fatalf("缺浏览器应明确报错: %v", err)
	}
	// builtin=false 且无显式 chrome
	_, err = ParseSelftestConfig(map[string]any{
		"render": map[string]any{"builtin": false},
	}, func(k string) string { return env[k] }, dir)
	if err == nil || !strings.Contains(err.Error(), "未配置可用浏览器") {
		t.Fatalf("builtin=false 无 chrome 应明确报错: %v", err)
	}
	// 显式 chrome 不存在
	_, err = ParseSelftestConfig(map[string]any{
		"render": map[string]any{"chrome": filepath.Join(dir, "missing.exe")},
	}, func(k string) string { return env[k] }, dir)
	if err == nil || !strings.Contains(err.Error(), "指定的浏览器不存在") {
		t.Fatalf("chrome 不存在应明确报错: %v", err)
	}
	// UVMP_BROWSER_BIN 不存在
	env["UVMP_BROWSER_BIN"] = filepath.Join(dir, "missing-electron")
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
