package browser_test

import (
	"context"
	"os"
	"testing"

	"github.com/zhangzhenyu91/j1toolkit/uvmp-toolkit/core-go/internal/browser"
	"github.com/zhangzhenyu91/j1toolkit/uvmp-toolkit/core-go/internal/render"
)

// 宿主浏览器端到端集成测试（HOST_BROWSER_IT=1）：browser.DetectHost →
// render.ParseSelftestConfig → render.Selftest 真实 CDP 链路（data: 本地页，零网络）。
// 默认跳过；本机无宿主浏览器时明确 skip（不失败）。与 electron_it_test.go 的
// Electron 回退 IT 分开。外部测试包，避免 browser ↔ render 测试导入环。

func TestITHostBrowserSelftest(t *testing.T) {
	if os.Getenv("HOST_BROWSER_IT") == "" {
		t.Skip("集成测试默认跳过：设 HOST_BROWSER_IT=1 后运行（可用 UVMP_BROWSER_BIN 指定浏览器）")
	}
	host, err := browser.DetectHost("")
	if err != nil {
		t.Skipf("本机无宿主浏览器，跳过: %v", err)
	}
	if host.Path == "" {
		t.Skip("本机无宿主浏览器，跳过")
	}
	t.Setenv(browser.EnvBrowserBin, host.Path)
	cfg, err := render.ParseSelftestConfig(map[string]any{}, os.Getenv, t.TempDir())
	if err != nil {
		t.Fatalf("ParseSelftestConfig: %v", err)
	}
	if cfg.BrowserPath != host.Path {
		t.Fatalf("BrowserPath = %q, want %q", cfg.BrowserPath, host.Path)
	}
	if cfg.NoHeadless || len(cfg.BrowserArgs) != 0 {
		t.Fatalf("宿主浏览器应零位置参数/headless：%+v", cfg)
	}
	res, err := render.Selftest(context.Background(), cfg)
	if err != nil {
		t.Fatalf("宿主浏览器 CDP 自检失败: %v", err)
	}
	if !res.OK {
		t.Fatalf("自检结果: %+v", res)
	}
}
