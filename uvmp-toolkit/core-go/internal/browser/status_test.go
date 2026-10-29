package browser

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// fakeCDP 起一个只听 127.0.0.1 的假 CDP 端点（/json/list 返回指定 page 数），返回
// cdpURL 与关闭函数。
func fakeCDP(t *testing.T, pages int) (string, func()) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/json/list" {
			w.WriteHeader(404)
			return
		}
		targets := make([]map[string]any, 0, pages)
		for i := 0; i < pages; i++ {
			targets = append(targets, map[string]any{"type": "page", "url": "about:blank"})
		}
		targets = append(targets, map[string]any{"type": "browser"})
		_ = json.NewEncoder(w).Encode(targets)
	}))
	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	// probeCDP 用 cdpURL+"/json/list"，这里返回的地址已含端口、无路径
	return "http://127.0.0.1:" + u.Port(), srv.Close
}

// fakeBridge 起一个假回环桥（记录请求），返回 profile 目录（含 cdp-worker-bridge.json）。
func fakeBridge(t *testing.T, got *[]string) (profileDir string, closeFn func()) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-CDP-Worker-Token") != "test-token" {
			w.WriteHeader(403)
			return
		}
		*got = append(*got, r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(u.Port())
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	raw, _ := json.Marshal(bridgeInfo{Port: port, Token: "test-token", Mode: "course", PID: 4242})
	if err := os.WriteFile(filepath.Join(dir, bridgeFileName), raw, 0o644); err != nil {
		t.Fatal(err)
	}
	return dir, srv.Close
}

// TestProbeCDP 假 CDP 端点探活与 page 计数；未监听端口视为未存活。
func TestProbeCDP(t *testing.T) {
	cdpURL, closeFn := fakeCDP(t, 2)
	defer closeFn()

	alive, pages := probeCDP(context.Background(), cdpURL)
	if !alive || pages != 2 {
		t.Fatalf("probeCDP = %v, %d；want true, 2", alive, pages)
	}

	// 未监听端口（拿一个已关闭的端口探测）
	deadURL, closeDead := fakeCDP(t, 1)
	closeDead()
	if alive, _ := probeCDP(context.Background(), deadURL); alive {
		t.Fatal("已关闭端口不应探活")
	}
}

// TestBridgeOpenURLAndShutdown 回环桥契约：POST + X-CDP-Worker-Token 鉴权，
// /open-url 传 url、/shutdown 优雅退出；javascript: 等协议在 Go 侧先行拒绝。
func TestBridgeOpenURLAndShutdown(t *testing.T) {
	var calls []string
	profile, closeFn := fakeBridge(t, &calls)
	defer closeFn()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := bridgeOpenURL(ctx, profile, "data:text/html;base64,PGgxPmE8L2gxPg=="); err != nil {
		t.Fatalf("bridgeOpenURL 报错: %v", err)
	}
	if err := bridgeOpenURL(ctx, profile, "about:blank"); err != nil {
		t.Fatalf("bridgeOpenURL about:blank 报错: %v", err)
	}
	for _, bad := range []string{"javascript:alert(1)", "file:///etc/passwd", "ftp://x"} {
		if err := bridgeOpenURL(ctx, profile, bad); err == nil {
			t.Fatalf("bridgeOpenURL(%q) 应拒绝", bad)
		}
	}
	called, err := bridgeShutdown(ctx, profile)
	if err != nil || !called {
		t.Fatalf("bridgeShutdown = %v, %v；want true, nil", called, err)
	}
	if len(calls) != 3 || calls[0] != "/open-url" || calls[1] != "/open-url" || calls[2] != "/shutdown" {
		t.Fatalf("桥调用序列 = %v", calls)
	}

	// 无桥目录：open-url 报「非实例」，shutdown 返回未调用
	empty := t.TempDir()
	if err := bridgeOpenURL(ctx, empty, "about:blank"); err == nil || !strings.Contains(err.Error(), "非 browser-runtime") {
		t.Fatalf("无桥 open-url 应报非实例: %v", err)
	}
	called, err = bridgeShutdown(ctx, empty)
	if err != nil || called {
		t.Fatalf("无桥 shutdown = %v, %v；want false, nil", called, err)
	}
}

// TestQueryStatus 按配置查询状态：课程模式探 CDPURL；导出模式读 DevToolsActivePort
// 再探；Electron 回退配置另有桥文件回填 BridgePort/PID。未在运行 Running=false。
func TestQueryStatus(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// 课程模式（宿主浏览器）：在跑，无回环桥
	cdpURL, closeCDP := fakeCDP(t, 3)
	defer closeCDP()
	cfg := NewCourseConfig(fixtureBrowser(t), "", t.TempDir(), cdpURL, "about:blank")
	st, err := QueryStatus(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !st.Running || st.Pages != 3 || st.CDPURL != cdpURL || st.BridgePort != 0 {
		t.Fatalf("课程状态 = %+v", st)
	}

	// Electron 回退：桥文件回填 BridgePort/PID
	var calls []string
	profile, closeBridge := fakeBridge(t, &calls)
	defer closeBridge()
	ecfg := NewCourseConfig(fixtureBrowser(t), fixtureRuntime(t), profile, cdpURL, "about:blank")
	est, err := QueryStatus(ctx, ecfg)
	if err != nil {
		t.Fatal(err)
	}
	if !est.Running || est.BridgePort == 0 || est.PID != 4242 {
		t.Fatalf("Electron 回退状态 = %+v", est)
	}

	// 导出模式：DevToolsActivePort 通告端口
	expProfile := t.TempDir()
	expURL, closeExp := fakeCDP(t, 1)
	defer closeExp()
	expPort, err := strconv.Atoi(strings.TrimPrefix(expURL, "http://127.0.0.1:"))
	if err != nil {
		t.Fatal(err)
	}
	portFile := fmt.Sprintf("%d\n/devtools/browser/x\n", expPort)
	if err := os.WriteFile(filepath.Join(expProfile, "DevToolsActivePort"), []byte(portFile), 0o644); err != nil {
		t.Fatal(err)
	}
	expCfg := NewExportConfig(fixtureBrowser(t), "", expProfile)
	st2, err := QueryStatus(ctx, expCfg)
	if err != nil {
		t.Fatal(err)
	}
	if !st2.Running || st2.CDPURL != expURL || st2.Pages != 1 {
		t.Fatalf("导出状态 = %+v", st2)
	}

	// 未在运行
	off := NewCourseConfig(fixtureBrowser(t), "", t.TempDir(), "http://127.0.0.1:1", "")
	st3, err := QueryStatus(ctx, off)
	if err != nil {
		t.Fatal(err)
	}
	if st3.Running {
		t.Fatalf("未监听端口应 Running=false: %+v", st3)
	}
}

// TestStartCourseReuseHost 宿主浏览器课程模式复用：同端口已有 CDP 时不再拉起新进程，
// 持久 profile 原样使用，start_url 经 CDP /json/new 打开（PUT 优先）。
func TestStartCourseReuseHost(t *testing.T) {
	var opened []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/json/list":
			_ = json.NewEncoder(w).Encode([]map[string]any{
				{"type": "page", "url": "about:blank"},
				{"type": "browser"},
			})
		case strings.HasPrefix(r.URL.Path, "/json/new"):
			opened = append(opened, r.Method+" "+r.URL.RawQuery)
			w.WriteHeader(http.StatusOK)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	cdpURL := "http://127.0.0.1:" + u.Port()
	profile := t.TempDir()
	cfg := NewCourseConfig(fixtureBrowser(t), "", profile, cdpURL, "https://edu.example.com/course-list")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	inst, err := Start(ctx, cfg)
	if err != nil {
		t.Fatalf("复用已有课程浏览器失败: %v", err)
	}
	if !inst.Status().Reused {
		t.Fatal("应标记为复用实例")
	}
	if inst.CDPURL() != cdpURL || inst.ProfileDir() != profile {
		t.Fatalf("复用实例地址/profile 不符: %q %q", inst.CDPURL(), inst.ProfileDir())
	}
	if got := inst.Status().StartURL; got != "https://edu.example.com/course-list" {
		t.Fatalf("复用实例 start_url = %q", got)
	}
	if len(opened) != 1 || opened[0] != "PUT https://edu.example.com/course-list" {
		t.Fatalf("start_url 打开调用 = %v（应在复用实例经 PUT /json/new 打开）", opened)
	}
}

// 关闭时明确报错（不代杀他人进程）。
func TestStopWithoutInstance(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	offline := NewCourseConfig(fixtureBrowser(t), "", t.TempDir(), "http://127.0.0.1:1", "")
	if err := Stop(ctx, offline); err != nil {
		t.Fatalf("未在运行应幂等成功: %v", err)
	}

	cdpURL, closeCDP := fakeCDP(t, 1)
	defer closeCDP()
	foreign := NewCourseConfig(fixtureBrowser(t), "", t.TempDir(), cdpURL, "")
	foreign.StopGrace = 300 * time.Millisecond
	err := Stop(ctx, foreign)
	if err == nil || !strings.Contains(err.Error(), "仍在响应") {
		t.Fatalf("无法关闭的宿主浏览器应报错: %v", err)
	}
}

// TestOpenURLViaCDP 复用实例打开 start_url：宿主浏览器走 CDP /json/new（优先 PUT，
// 旧版 GET 回退），非 http/https/data/about 协议在 Go 侧先行拒绝。
func TestOpenURLViaCDP(t *testing.T) {
	var methods []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/json/new") {
			methods = append(methods, r.Method)
			if r.Method == http.MethodPut {
				w.WriteHeader(http.StatusBadRequest) // 模拟旧版：PUT 不支持
				return
			}
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(404)
	}))
	defer srv.Close()
	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	cdpURL := "http://127.0.0.1:" + u.Port()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := openURLViaCDP(ctx, cdpURL, "https://edu.example.com/course-list"); err != nil {
		t.Fatalf("openURLViaCDP 报错: %v", err)
	}
	if len(methods) != 2 || methods[0] != http.MethodPut || methods[1] != http.MethodGet {
		t.Fatalf("调用序列 = %v（应先 PUT 再回退 GET）", methods)
	}
	for _, bad := range []string{"javascript:alert(1)", "file:///etc/passwd", "ftp://x"} {
		if err := openURLViaCDP(ctx, cdpURL, bad); err == nil {
			t.Fatalf("openURLViaCDP(%q) 应拒绝", bad)
		}
	}
}
