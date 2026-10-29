package browser

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/zhangzhenyu91/j1toolkit/uvmp-toolkit/core-go/internal/render"
)

// Electron 集成测试（BROWSER_RUNTIME_IT=1）：用本机随包 Electron + browser-runtime 走
// 真实双模式链路——export 不可见模式（DevToolsActivePort/回环桥/停止）、course 可见模式
// （start_url 自动访问 / 进程复用 / 优雅停止）、以及 render 兼容（render.Selftest 全链路
// 以 browser-runtime 为宿主）。只打开 data: 本地页，不访问任何业务网络。
//
// 默认跳过；设 BROWSER_RUNTIME_IT=1 后运行。可用 BROWSER_RUNTIME_ELECTRON /
// BROWSER_RUNTIME_DIR 覆盖两个路径（默认按仓库布局推断）。

// itPaths 推断/校验 Electron 与 browser-runtime 目录（env 可覆盖）。
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
	if os.Getenv("BROWSER_RUNTIME_IT") == "" {
		t.Skip("集成测试默认跳过：设 BROWSER_RUNTIME_IT=1 后运行（可用 BROWSER_RUNTIME_ELECTRON/BROWSER_RUNTIME_DIR 指定路径）")
	}
}

// dataPage 生成纯本地 data:text/html 测试页（base64，避免转义问题）。
func dataPage(title, body string) string {
	html := "<html><head><title>" + title + "</title></head><body>" + body + "</body></html>"
	return "data:text/html;base64," + base64.StdEncoding.EncodeToString([]byte(html))
}

// freePort 取一个当前空闲的本机回环端口（课程模式固定端口测试用）。
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

// cdpPageTitles 拉 /json/list 返回 page target 的标题（就绪/导航断言用）。
func cdpPageTitles(t *testing.T, cdpURL string) []string {
	t.Helper()
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get(cdpURL + "/json/list")
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	var targets []struct {
		Type  string `json:"type"`
		Title string `json:"title"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&targets); err != nil {
		return nil
	}
	var out []string
	for _, tg := range targets {
		if tg.Type == "page" {
			out = append(out, tg.Title)
		}
	}
	return out
}

// waitForTitle 轮询等待某个 page target 标题出现。
func waitForTitle(t *testing.T, cdpURL, title string) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for {
		for _, got := range cdpPageTitles(t, cdpURL) {
			if got == title {
				return
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("等待页面标题 %q 超时；当前 %v", title, cdpPageTitles(t, cdpURL))
		}
		time.Sleep(300 * time.Millisecond)
	}
}

// TestITCourseMode 课程可见模式真实链路：启动即访问 start_url（data: 本地页）→ 状态/
// CDPURL/回环桥就绪 → 同端口复用（不重复启动）且复用时按 start_url 导航 → Stop 优雅退出。
func TestITCourseMode(t *testing.T) {
	itSkip(t)
	electronBin, runtimeDir := itPaths(t)

	port := freePort(t)
	cdpURL := fmt.Sprintf("http://127.0.0.1:%d", port)
	profile := t.TempDir()
	cfg := NewCourseConfig(electronBin, runtimeDir, profile, cdpURL, dataPage("course-home", "hello"))
	cfg.Log = func(line string) { t.Log(line) }
	cfg.ReadyTimeout = 45 * time.Second

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	inst, err := Start(ctx, cfg)
	if err != nil {
		t.Fatalf("课程模式启动失败: %v", err)
	}
	// 无论断言在哪一步失败，都保证回收运行时（Windows 上日志句柄随进程释放，
	// 否则 t.TempDir 清理会因文件被占用而失败）
	t.Cleanup(func() {
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer stopCancel()
		_ = inst.Stop(stopCtx)
	})
	if inst.CDPURL() != cdpURL {
		t.Fatalf("CDPURL = %q；want %q", inst.CDPURL(), cdpURL)
	}
	if inst.ProfileDir() != profile {
		t.Fatalf("ProfileDir = %q；want %q", inst.ProfileDir(), profile)
	}
	st1 := inst.Status()
	if !st1.Running || st1.Reused || st1.PID == 0 || st1.BridgePort == 0 {
		t.Fatalf("课程实例状态 = %+v", st1)
	}
	// start_url 已由运行时首窗自动访问
	waitForTitle(t, cdpURL, "course-home")

	// 复用：同端口再 Start 不得新起进程（Reused=true、同一 PID），并按新 start_url 导航首屏
	cfg2 := cfg
	cfg2.StartURL = dataPage("course-reuse", "again")
	inst2, err := Start(ctx, cfg2)
	if err != nil {
		t.Fatalf("课程复用启动失败: %v", err)
	}
	st2 := inst2.Status()
	if !st2.Reused || !st2.Running || st2.PID != st1.PID {
		t.Fatalf("复用实例状态 = %+v；want Reused 且 PID 与首启一致 %d", st2, st1.PID)
	}
	waitForTitle(t, cdpURL, "course-reuse")

	// 优雅停止：/shutdown 后 CDP 不再响应（回环桥文件随退出删除）
	if err := inst2.Stop(ctx); err != nil {
		t.Fatalf("停止失败: %v", err)
	}
	deadline := time.Now().Add(15 * time.Second)
	for {
		if alive, _ := probeCDP(ctx, cdpURL); !alive {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("Stop 后 CDP 仍在响应")
		}
		time.Sleep(300 * time.Millisecond)
	}
	// 幂等：再停一次无害
	if err := inst2.Stop(ctx); err != nil {
		t.Fatalf("重复 Stop 应幂等: %v", err)
	}
}

// TestITExportMode 导出不可见模式真实链路：随机调试端口经 DevToolsActivePort 通告、
// 回环桥落盘、首屏 about:blank、Stop 回收进程。配套地图域名证书豁免/SPKI 开关由
// browser-runtime 按开关选择（此处不配 pin，运行时应装 map.sgcc.com.cn 豁免回调，
// 行为与 desktop/electron/cdp-worker.cjs 一致）。
func TestITExportMode(t *testing.T) {
	itSkip(t)
	electronBin, runtimeDir := itPaths(t)

	profile := t.TempDir()
	cfg := NewExportConfig(electronBin, runtimeDir, profile)
	cfg.Log = func(line string) { t.Log(line) }
	cfg.ReadyTimeout = 45 * time.Second

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	inst, err := Start(ctx, cfg)
	if err != nil {
		t.Fatalf("导出模式启动失败: %v", err)
	}
	// 无论断言在哪一步失败，都保证回收运行时（日志句柄随进程释放）
	t.Cleanup(func() {
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer stopCancel()
		_ = inst.Stop(stopCtx)
	})
	if st := inst.Status(); !st.Running || st.Pages != 1 || st.BridgePort == 0 || st.PID == 0 {
		t.Fatalf("导出实例状态 = %+v", st)
	}
	// DevToolsActivePort 通告 + 回环桥文件均落在独立 profile（render 契约位置）
	if _, err := os.Stat(filepath.Join(profile, "DevToolsActivePort")); err != nil {
		t.Fatalf("DevToolsActivePort 缺失: %v", err)
	}
	bi, err := loadBridge(profile)
	if err != nil {
		t.Fatalf("回环桥信息缺失: %v", err)
	}
	if bi.Mode != "export" || bi.PID <= 0 {
		t.Fatalf("回环桥附加字段 = %+v", bi)
	}
	// 首屏为 about:blank（不可见 worker 窗口）
	titles := cdpPageTitles(t, inst.CDPURL())
	if len(titles) != 1 {
		t.Fatalf("page target 数 = %d（%v）；want 1", len(titles), titles)
	}

	if err := inst.Stop(ctx); err != nil {
		t.Fatalf("停止失败: %v", err)
	}
	deadline := time.Now().Add(15 * time.Second)
	for {
		if alive, _ := probeCDP(ctx, inst.CDPURL()); !alive {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("Stop 后 CDP 仍在响应")
		}
		time.Sleep(300 * time.Millisecond)
	}
}

// TestITRenderCompat render 兼容：render.Selftest 以 browser-runtime 为宿主跑完整
// 导出链路（启动 / 导航 / eval / 新 tab 桥 / 截图 PNG / printToPDF 桥），只开 data:
// 本地页。全绿即证明 browser-runtime 的 export 模式与 internal/render 契约兼容
// （cdp-worker-bridge.json、/print-to-pdf、/new-window、DevTools 通告、不可见窗口）。
func TestITRenderCompat(t *testing.T) {
	itSkip(t)
	electronBin, runtimeDir := itPaths(t)

	outDir := t.TempDir()
	cfg := render.SelftestConfig{
		BrowserPath: electronBin,
		BrowserArgs: []string{runtimeDir}, // 以 browser-runtime 应用目录为宿主
		NoHeadless:  true,
		OutDir:      outDir,
		Log:         func(line string) { t.Log(line) },
		Progress:    func(step, total int, label string) { t.Logf("进度 %d/%d %s", step, total, label) },
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	res, err := render.Selftest(ctx, cfg)
	if err != nil {
		t.Fatalf("render 自检（browser-runtime 宿主）失败: %v", err)
	}
	if !res.OK || res.PNGBytes <= 0 || res.PDFBytes <= 0 {
		t.Fatalf("自检结果异常: %+v", res)
	}
	if png, err := os.ReadFile(res.PNG); err != nil || len(png) < 8 || string(png[:8]) != "\x89PNG\r\n\x1a\n" {
		t.Fatalf("截图不是 PNG: %v", err)
	}
	if pdf, err := os.ReadFile(res.PDF); err != nil || len(pdf) < 4 || string(pdf[:4]) != "%PDF" {
		t.Fatalf("产物不是 PDF: %v", err)
	}
}
