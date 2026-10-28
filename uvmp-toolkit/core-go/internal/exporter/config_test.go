package exporter

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestParseRunConfigWorkersAndWaits 并发钳制（query 1-8 默认 4；render 1-4 默认 2）
// 与 30s 地图/6s 派车单等待配置映射。
func TestParseRunConfigWorkersAndWaits(t *testing.T) {
	browser := filepath.Join(t.TempDir(), "chrome-bin")
	_ = os.WriteFile(browser, []byte("x"), 0o755)
	req := Request{
		Config: map[string]any{
			"render":       map[string]any{"chrome": browser},
			"order_export": map[string]any{"query_workers": 99, "render_workers": 0},
			"_ini": map[string]any{
				"export": map[string]any{
					"map_min_wait":    "30000",
					"pcd_render_wait": "6000",
					"map_tiles_wait":  "12000",
					"map_idle_quiet":  "5000",
				},
			},
		},
		Params: map[string]any{"outdir": t.TempDir(), "batch_subdir": false},
		Codes:  []CodeItem{{Seq: 1, Code: "X"}},
	}
	cfg, err := ParseRunConfig(req, os.Getenv, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if cfg.QueryWorkers != 8 || cfg.RenderWorkers != 1 {
		t.Fatalf("并发钳制不符: %d %d", cfg.QueryWorkers, cfg.RenderWorkers)
	}
	if cfg.MinWait != 30*time.Second || cfg.PCDWait != 6*time.Second {
		t.Fatalf("30s/6s 等待映射不符: %v %v", cfg.MinWait, cfg.PCDWait)
	}
	if cfg.WaitTiles != 12*time.Second || cfg.IdleQuiet != 5*time.Second {
		t.Fatalf("瓦片/静默等待不符: %v %v", cfg.WaitTiles, cfg.IdleQuiet)
	}
	// 缺省值
	req2 := Request{Config: map[string]any{"render": map[string]any{"chrome": browser}}, Params: map[string]any{"outdir": t.TempDir(), "batch_subdir": false},
		Codes: []CodeItem{{Seq: 1, Code: "X"}}}
	cfg2, err := ParseRunConfig(req2, os.Getenv, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if cfg2.QueryWorkers != 4 || cfg2.RenderWorkers != 2 {
		t.Fatalf("默认并发不符: %d %d", cfg2.QueryWorkers, cfg2.RenderWorkers)
	}
	if cfg2.MinWait != 30*time.Second || cfg2.PCDWait != 6*time.Second {
		t.Fatalf("默认等待不符: %v %v", cfg2.MinWait, cfg2.PCDWait)
	}
	if cfg2.LookbackDays != 62 {
		t.Fatalf("默认回看天数不符: %d", cfg2.LookbackDays)
	}
}

// TestResolveBrowser 浏览器选择：显式 chrome 优先作兼容回退；builtin 必须
// UVMP_BROWSER_BIN（不探测宿主）；缺浏览器明确失败。
func TestResolveBrowser(t *testing.T) {
	browser := filepath.Join(t.TempDir(), "chrome-bin")
	_ = os.WriteFile(browser, []byte("x"), 0o755)
	reqFor := func(rc map[string]any) Request {
		return Request{
			Config: map[string]any{"render": rc},
			Params: map[string]any{"outdir": t.TempDir(), "batch_subdir": false},
			Codes:  []CodeItem{{Seq: 1, Code: "X"}},
		}
	}

	// 显式 render.chrome 优先
	cfg, err := ParseRunConfig(reqFor(map[string]any{"chrome": browser, "builtin": false}),
		os.Getenv, time.Now())
	if err != nil || cfg.BrowserPath != browser {
		t.Fatalf("显式 chrome 未生效: %v %v", cfg, err)
	}

	// chrome 指定但不存在 → 明确失败
	_, err = ParseRunConfig(reqFor(map[string]any{"chrome": filepath.Join(t.TempDir(), "missing")}), os.Getenv, time.Now())
	if err == nil || !strings.Contains(err.Error(), "指定的浏览器不存在") {
		t.Fatalf("缺浏览器应明确失败: %v", err)
	}

	// builtin=true：必须 UVMP_BROWSER_BIN，不自动探测宿主
	t.Setenv("UVMP_BROWSER_BIN", "")
	_, err = ParseRunConfig(reqFor(map[string]any{"builtin": true}), os.Getenv, time.Now())
	if err == nil || !strings.Contains(err.Error(), "UVMP_BROWSER_BIN") {
		t.Fatalf("builtin 无 UVMP_BROWSER_BIN 应失败: %v", err)
	}
	t.Setenv("UVMP_BROWSER_BIN", browser)
	cfg, err = ParseRunConfig(reqFor(map[string]any{"builtin": true}), os.Getenv, time.Now())
	if err != nil || cfg.BrowserPath != browser {
		t.Fatalf("UVMP_BROWSER_BIN 未生效: %v %v", cfg, err)
	}

	// builtin=false 且无 chrome → 明确失败
	t.Setenv("UVMP_BROWSER_BIN", "")
	_, err = ParseRunConfig(reqFor(map[string]any{"builtin": false}), os.Getenv, time.Now())
	if err == nil || !strings.Contains(err.Error(), "未配置可用浏览器") {
		t.Fatalf("无浏览器应明确失败: %v", err)
	}

	// mock 不需要浏览器
	cfg, err = ParseRunConfig(Request{
		Config: map[string]any{"dev": map[string]any{"mock": true}},
		Params: map[string]any{"outdir": t.TempDir(), "batch_subdir": false},
		Codes:  []CodeItem{{Seq: 1, Code: "X"}},
	}, os.Getenv, time.Now())
	if err != nil || cfg.BrowserPath != "" {
		t.Fatalf("mock 不应需要浏览器: %v %v", cfg, err)
	}
}

// TestParseRunConfigValidation 校验顺序：空清单先报错（config 空也一样）。
func TestParseRunConfigValidation(t *testing.T) {
	_, err := ParseRunConfig(Request{Config: map[string]any{}}, os.Getenv, time.Now())
	if err == nil || err.Error() != "未解析到任何派车单号" {
		t.Fatalf("空清单应先报错: %v", err)
	}
	_, err = ParseRunConfig(Request{
		Config: map[string]any{},
		Params: map[string]any{"export_pcd": false, "export_track": false},
		Codes:  []CodeItem{{Seq: 1, Code: "X"}},
	}, os.Getenv, time.Now())
	if err == nil || !strings.Contains(err.Error(), "至少勾选一项") {
		t.Fatalf("勾选校验不符: %v", err)
	}
}

// TestWorkerDir 渲染 worker 独立 WorkDir：状态目录 browser-workers/job/worker-N。
func TestWorkerDir(t *testing.T) {
	cfg := &RunConfig{StateDir: "/state"}
	got := cfg.WorkerDir(2)
	want := filepath.Join("/state", "browser-workers", "job", "worker-2")
	if got != want {
		t.Fatalf("WorkDir 不符: %s want %s", got, want)
	}
}
