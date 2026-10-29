package course

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/zhangzhenyu91/j1toolkit/uvmp-toolkit/core-go/internal/browser"
)

// Electron 集成测试（COURSE_IT=1）：真实 Electron（browser-runtime 课程模式）+ 本机
// httptest 模拟站跑完整状态机——CDP 连接、页面识别、进入学习弹窗、响应监视、视频拖动
// 与 ended、上报确认、卡住课跳过与汇总。只访问 127.0.0.1 模拟站，不访问国网学堂。
//
// 默认跳过；设 COURSE_IT=1 后运行。可用 BROWSER_RUNTIME_ELECTRON / BROWSER_RUNTIME_DIR
// 覆盖两个路径（默认按仓库布局推断，口径同 internal/browser 的集成测试）。

// itPaths 推断/校验 Electron 与 browser-runtime 目录。
func itPaths(t *testing.T) (electronBin, runtimeDir string) {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller 失败")
	}
	toolkitRoot := filepath.Clean(filepath.Join(filepath.Dir(file), "..", "..", "..")) // uvmp-toolkit/

	electronBin = os.Getenv("BROWSER_RUNTIME_ELECTRON")
	if electronBin == "" {
		name := "electron"
		if runtime.GOOS == "windows" {
			name = "electron.exe"
		}
		electronBin = filepath.Join(toolkitRoot, "desktop", "node_modules", "electron", "dist", name)
	}
	runtimeDir = os.Getenv("BROWSER_RUNTIME_DIR")
	if runtimeDir == "" {
		runtimeDir = filepath.Join(toolkitRoot, "browser-runtime")
	}
	for _, p := range []string{electronBin, filepath.Join(runtimeDir, "package.json"), filepath.Join(runtimeDir, "main.cjs")} {
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("路径不存在（可用 BROWSER_RUNTIME_ELECTRON/BROWSER_RUNTIME_DIR 覆盖）: %v", err)
		}
	}
	return electronBin, runtimeDir
}

// itSkip 未开集成测试开关时跳过。
func itSkip(t *testing.T) {
	t.Helper()
	if os.Getenv("COURSE_IT") == "" {
		t.Skip("集成测试默认跳过：设 COURSE_IT=1 后运行（可用 BROWSER_RUNTIME_ELECTRON/BROWSER_RUNTIME_DIR 指定路径）")
	}
}

// freePort 取本机空闲端口（课程模式固定 CDP 端口用）。
func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	_ = l.Close()
	return port
}

// TestITElectronFullFlow 真实 Electron 全流程（只连本机模拟站）：
// 启动课程模式运行时→打开模拟课程列表→识别接管→清筛选→视频/文档学习→卡住跳过→汇总。
func TestITElectronFullFlow(t *testing.T) {
	itSkip(t)
	electronBin, runtimeDir := itPaths(t)

	station := newMockStation()
	t.Cleanup(station.Close)

	port := freePort(t)
	cdpURL := "http://127.0.0.1:" + itoa(port)
	profile := t.TempDir()
	cfg := browser.NewCourseConfig(electronBin, runtimeDir, profile, cdpURL, station.ListURL("train1"))
	cfg.Log = func(line string) { t.Log(line) }
	cfg.ReadyTimeout = 60 * time.Second

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	inst, err := browser.Start(ctx, cfg)
	if err != nil {
		t.Fatalf("课程模式启动失败: %v", err)
	}
	t.Cleanup(func() {
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer stopCancel()
		_ = inst.Stop(stopCtx)
	})

	connCtx, connCancel := context.WithTimeout(ctx, 30*time.Second)
	defer connCancel()
	cb, err := Connect(connCtx, ConnectOptions{CDPURL: inst.CDPURL(), Timeout: 30 * time.Second, Log: func(m string) { t.Log(m) }})
	if err != nil {
		t.Fatalf("CDP 连接失败: %v", err)
	}

	dir := t.TempDir()
	rec := &logRecorder{}
	opts := RunJobOptions{
		Options: Options{
			Timeout:          30 * time.Second,
			MaxAttempts:      1,
			OutDir:           dir,
			DiagnosticPrefix: "it_failure",
			Log:              rec.log,
		},
		Dir:   dir,
		Store: &CheckpointsStore{Path: filepath.Join(dir, "course-autoplay", "checkpoints.json")},
	}
	res, err := RunJob(ctx, cb, opts)
	if err != nil {
		t.Fatalf("真实浏览器全流程失败: %v（日志:\n%s）", err, rec.all())
	}
	for _, c := range []struct {
		key  string
		want float64
	}{
		{"total", 3}, {"completed", 2}, {"pending", 1}, {"skipped", 1},
	} {
		if got, ok := toFloat(res[c.key]); !ok || got != c.want {
			t.Fatalf("%s = %v，want %v（站点进度 v=%g d=%g s=%g；日志:\n%s）",
				c.key, res[c.key], c.want, station.rateOf("v"), station.rateOf("d"), station.rateOf("s"), rec.all())
		}
	}
	if station.rateOf("v") != 100 || station.rateOf("d") != 100 || station.rateOf("s") != 50 {
		t.Fatalf("站点进度 v=%g d=%g s=%g（日志:\n%s）", station.rateOf("v"), station.rateOf("d"), station.rateOf("s"), rec.all())
	}
	if !rec.contains("学习页已打开") || !rec.contains("已收到视频真实 ended 事件") {
		t.Fatalf("关键日志缺失:\n%s", rec.all())
	}
	// 弹窗已随处理关闭，列表页保留
	tabs, err := cb.Tabs(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, tab := range tabs {
		if strings.Contains(tab.URL, "courseplay") {
			t.Fatalf("学习弹窗未关闭: %v", tab.URL)
		}
	}
}

// itoa 整数转字符串。
func itoa(n int) string { return strconv.Itoa(n) }
