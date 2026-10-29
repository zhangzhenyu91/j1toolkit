package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// uvmp-export run/selftest 独立工作测试（无 Python goexport 注入）：模拟安装布局
// 自动注入内置浏览器环境，请求/事件协议不变；不访问网络（真实路径止步于本地
// auth.ParseConfig 校验，自检止步于假 Electron 启动失败）。

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

// clearBrowserEnv 清空三件套（t.Setenv 统一还原）。
func clearBrowserEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{"UVMP_BROWSER_BIN", "UVMP_BROWSER_WORKER", "UVMP_BROWSER_APP"} {
		t.Setenv(k, "")
	}
}

// TestRunExportIOInjectsBrowserEnv 无 Python 注入时 run 也能过 ParseRunConfig：
// 注入生效则错误止步于本地 SSO 配置校验（「未配置 SSO」），而不是「未找到
// 内置浏览器」；结束后环境已 restore。
func TestRunExportIOInjectsBrowserEnv(t *testing.T) {
	root := fakeFyneLayout(t)
	t.Chdir(root)
	clearBrowserEnv(t)
	req := `{"config":{},"params":{"outdir":"` + filepath.ToSlash(filepath.Join(t.TempDir(), "out")) +
		`","batch_subdir":false},"codes":[{"seq":1,"code":"PC001"}]}`
	var stdout bytes.Buffer
	if code := runExportIO(strings.NewReader(req), &stdout); code != 1 {
		t.Fatalf("缺 SSO 配置应退出 1，got %d（%s）", code, stdout.String())
	}
	s := stdout.String()
	if strings.Contains(s, "未找到内置浏览器") || strings.Contains(s, "UVMP_BROWSER_BIN") {
		t.Fatalf("注入应已生效（不应缺内置浏览器）: %s", s)
	}
	if !strings.Contains(s, "未配置 SSO") {
		t.Fatalf("应过 ParseRunConfig、止步于 SSO 配置校验: %s", s)
	}
	if v := os.Getenv("UVMP_BROWSER_BIN"); v != "" {
		t.Fatalf("任务结束未 restore: BIN=%q", v)
	}
}

// TestRunExportIOMockOK mock 全流程离线出 done 事件（协议冒烟）。
func TestRunExportIOMockOK(t *testing.T) {
	clearBrowserEnv(t)
	req := `{"config":{"dev":{"mock":true}},"params":{"outdir":"` + filepath.ToSlash(filepath.Join(t.TempDir(), "out")) +
		`","batch_subdir":false},"codes":[{"seq":1,"code":"PC001"}]}`
	var stdout bytes.Buffer
	if code := runExportIO(strings.NewReader(req), &stdout); code != 0 {
		t.Fatalf("mock 应退出 0，got %d（%s）", code, stdout.String())
	}
	if !strings.Contains(stdout.String(), `"event":"done"`) {
		t.Fatalf("缺 done 事件: %s", stdout.String())
	}
}

// TestRunSelftestIOInjectsBrowserEnv selftest 自动注入：日志中的自检浏览器即
// 注入的假 Electron（证明 ParseSelftestConfig 拿到注入值），实链在启动阶段失败
// 走 error 事件；结束后环境已 restore。
func TestRunSelftestIOInjectsBrowserEnv(t *testing.T) {
	root := fakeFyneLayout(t)
	t.Chdir(root)
	clearBrowserEnv(t)
	t.Setenv("UVMP_SELFTEST_DIR", t.TempDir())
	req := `{"config":{}}`
	var stdout bytes.Buffer
	if code := runSelftestIO(strings.NewReader(req), &stdout); code != 1 {
		t.Fatalf("假 Electron 应退出 1，got %d（%s）", code, stdout.String())
	}
	wantBin := filepath.Join(root, "browser-runtime", fakeElectronName())
	// NDJSON 内路径为 JSON 转义形式（反斜杠翻倍），反转义后比对
	got := strings.ReplaceAll(stdout.String(), `\\`, `\`)
	if !strings.Contains(got, wantBin) {
		t.Fatalf("日志应含注入的浏览器路径 %q: %s", wantBin, stdout.String())
	}
	if !strings.Contains(stdout.String(), `"event":"error"`) {
		t.Fatalf("缺 error 事件: %s", stdout.String())
	}
	if v := os.Getenv("UVMP_BROWSER_BIN"); v != "" {
		t.Fatalf("任务结束未 restore: BIN=%q", v)
	}
}

// TestRunSelftestIODefaultOutDir 独立使用未设 UVMP_SELFTEST_DIR：产物目录缺省
// 落临时目录，不再报「产物目录未指定」。
func TestRunSelftestIODefaultOutDir(t *testing.T) {
	root := fakeFyneLayout(t)
	t.Chdir(root)
	clearBrowserEnv(t)
	t.Setenv("UVMP_SELFTEST_DIR", "")
	req := `{"config":{}}`
	var stdout bytes.Buffer
	runSelftestIO(strings.NewReader(req), &stdout)
	s := stdout.String()
	if strings.Contains(s, "产物目录未指定") {
		t.Fatalf("不应缺产物目录: %s", s)
	}
	if !strings.Contains(s, "uvmp-selftest-") {
		t.Fatalf("应缺省临时产物目录: %s", s)
	}
}
