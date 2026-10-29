package browser

import (
	"context"
	"os"
	"testing"

	"github.com/zhangzhenyu91/j1toolkit/uvmp-toolkit/core-go/internal/render"
)

// 统一自发现端到端集成测试（BROWSER_RUNTIME_IT=1）：EnsureExportEnv 注入 →
// render.ParseSelftestConfig 按 env 解析 → render.Selftest 真实 export 链路（data:
// 本地页，零网络）。默认跳过，与 electron_it_test 同开关/同本机运行时口径。

func TestITEnsureExportEnvSelftest(t *testing.T) {
	itSkip(t)
	itPaths(t) // 本机浏览器运行时就绪校验（缺失即失败）
	clearBrowserEnv(t)
	restore := EnsureExportEnv("")
	defer restore()
	bin := os.Getenv(EnvBrowserBin)
	if bin == "" {
		t.Fatal("EnsureExportEnv 未注入 UVMP_BROWSER_BIN（本机 browser-runtime 应可探测）")
	}
	if w := os.Getenv(EnvBrowserWorker); w != ExportWorkerFlag {
		t.Fatalf("WORKER = %q, want %q", w, ExportWorkerFlag)
	}
	if os.Getenv(EnvBrowserApp) == "" {
		t.Fatal("EnsureExportEnv 未注入 UVMP_BROWSER_APP")
	}
	cfg, err := render.ParseSelftestConfig(map[string]any{}, os.Getenv, t.TempDir())
	if err != nil {
		t.Fatalf("ParseSelftestConfig: %v", err)
	}
	if cfg.BrowserPath != bin {
		t.Fatalf("BrowserPath = %q, want %q", cfg.BrowserPath, bin)
	}
	if !cfg.NoHeadless || len(cfg.BrowserArgs) != 2 {
		t.Fatalf("内置 Electron 口径应组 [APP, worker] 且 NoHeadless: %+v", cfg)
	}
	res, err := render.Selftest(context.Background(), cfg)
	if err != nil {
		t.Fatalf("本机 browser-runtime export 自检失败: %v", err)
	}
	if !res.OK {
		t.Fatalf("自检结果: %+v", res)
	}
}
