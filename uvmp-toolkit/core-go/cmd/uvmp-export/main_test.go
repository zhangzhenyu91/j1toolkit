package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// uvmp-export run/selftest 独立工作测试：默认宿主浏览器（UVMP_BROWSER_BIN 显式指定），
// 请求/事件协议不变；不访问网络（真实路径止步于本地 auth.ParseConfig 校验，自检止步于
// 假浏览器启动失败）。

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

// TestRunExportIOUsesHostBrowser 宿主浏览器就绪时 run 能过 ParseRunConfig：错误止步于
// 本地 SSO 配置校验（「未配置 SSO」），而不是「未找到可用浏览器」。
func TestRunExportIOUsesHostBrowser(t *testing.T) {
	host := filepath.Join(t.TempDir(), "360ent.exe")
	writeFakeFile(t, host)
	t.Setenv("UVMP_BROWSER_BIN", host)
	req := `{"config":{},"params":{"outdir":"` + filepath.ToSlash(filepath.Join(t.TempDir(), "out")) +
		`","batch_subdir":false},"codes":[{"seq":1,"code":"PC001"}]}`
	var stdout bytes.Buffer
	if code := runExportIO(strings.NewReader(req), &stdout); code != 1 {
		t.Fatalf("缺 SSO 配置应退出 1，got %d（%s）", code, stdout.String())
	}
	s := stdout.String()
	if strings.Contains(s, "未找到可用浏览器") {
		t.Fatalf("宿主浏览器应已就绪（不应缺浏览器）: %s", s)
	}
	if !strings.Contains(s, "未配置 SSO") {
		t.Fatalf("应过 ParseRunConfig、止步于 SSO 配置校验: %s", s)
	}
	if v := os.Getenv("UVMP_BROWSER_BIN"); v != host {
		t.Fatalf("不应改写 UVMP_BROWSER_BIN: %q", v)
	}
}

// TestRunExportIOHostMissing 宿主浏览器缺失：明确 error 事件（不静默下载/探测旧运行时）。
func TestRunExportIOHostMissing(t *testing.T) {
	t.Setenv("UVMP_BROWSER_BIN", filepath.Join(t.TempDir(), "missing-browser"))
	req := `{"config":{},"params":{"outdir":"` + filepath.ToSlash(filepath.Join(t.TempDir(), "out")) +
		`","batch_subdir":false},"codes":[{"seq":1,"code":"PC001"}]}`
	var stdout bytes.Buffer
	if code := runExportIO(strings.NewReader(req), &stdout); code != 1 {
		t.Fatalf("缺浏览器应退出 1，got %d（%s）", code, stdout.String())
	}
	if !strings.Contains(stdout.String(), `"event":"error"`) ||
		!strings.Contains(stdout.String(), "UVMP_BROWSER_BIN") {
		t.Fatalf("应给出明确缺浏览器错误: %s", stdout.String())
	}
}

// TestRunExportIOMockOK mock 全流程离线出 done 事件（协议冒烟）。
func TestRunExportIOMockOK(t *testing.T) {
	t.Setenv("UVMP_BROWSER_BIN", "")
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

// TestRunSelftestIOUsesHostBrowser selftest 默认宿主浏览器：日志中的自检浏览器即
// UVMP_BROWSER_BIN 指定的路径（证明 ParseSelftestConfig 拿到宿主浏览器），实链在启动
// 阶段失败走 error 事件。
func TestRunSelftestIOUsesHostBrowser(t *testing.T) {
	host := filepath.Join(t.TempDir(), "360ent.exe")
	writeFakeFile(t, host)
	t.Setenv("UVMP_BROWSER_BIN", host)
	t.Setenv("UVMP_SELFTEST_DIR", t.TempDir())
	req := `{"config":{}}`
	var stdout bytes.Buffer
	if code := runSelftestIO(strings.NewReader(req), &stdout); code != 1 {
		t.Fatalf("假浏览器应退出 1，got %d（%s）", code, stdout.String())
	}
	// NDJSON 内路径为 JSON 转义形式（反斜杠翻倍），反转义后比对
	got := strings.ReplaceAll(stdout.String(), `\\`, `\`)
	if !strings.Contains(got, host) {
		t.Fatalf("日志应含宿主浏览器路径 %q: %s", host, stdout.String())
	}
	if !strings.Contains(stdout.String(), `"event":"error"`) {
		t.Fatalf("缺 error 事件: %s", stdout.String())
	}
	if v := os.Getenv("UVMP_BROWSER_BIN"); v != host {
		t.Fatalf("不应改写 UVMP_BROWSER_BIN: %q", v)
	}
}

// TestRunSelftestIODefaultOutDir 独立使用未设 UVMP_SELFTEST_DIR：产物目录缺省
// 落临时目录，不再报「产物目录未指定」。
func TestRunSelftestIODefaultOutDir(t *testing.T) {
	host := filepath.Join(t.TempDir(), "360ent.exe")
	writeFakeFile(t, host)
	t.Setenv("UVMP_BROWSER_BIN", host)
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
