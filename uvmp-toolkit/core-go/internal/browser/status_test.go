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
// 再探；均带桥文件回填 BridgePort/PID。未在运行 Running=false。
func TestQueryStatus(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// 课程模式：在跑
	cdpURL, closeCDP := fakeCDP(t, 3)
	defer closeCDP()
	var calls []string
	profile, closeBridge := fakeBridge(t, &calls)
	defer closeBridge()
	cfg := NewCourseConfig(fixtureBrowser(t), fixtureRuntime(t), profile, cdpURL, "about:blank")
	st, err := QueryStatus(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !st.Running || st.Pages != 3 || st.CDPURL != cdpURL || st.BridgePort == 0 || st.PID != 4242 {
		t.Fatalf("课程状态 = %+v", st)
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
	expCfg := NewExportConfig(fixtureBrowser(t), fixtureRuntime(t), expProfile)
	st2, err := QueryStatus(ctx, expCfg)
	if err != nil {
		t.Fatal(err)
	}
	if !st2.Running || st2.CDPURL != expURL || st2.Pages != 1 {
		t.Fatalf("导出状态 = %+v", st2)
	}

	// 未在运行
	off := NewCourseConfig(fixtureBrowser(t), fixtureRuntime(t), t.TempDir(), "http://127.0.0.1:1", "")
	st3, err := QueryStatus(ctx, off)
	if err != nil {
		t.Fatal(err)
	}
	if st3.Running {
		t.Fatalf("未监听端口应 Running=false: %+v", st3)
	}
}

// TestStopWithoutInstance Stop 幂等：未在运行返回 nil；同端口有 CDP 但无回环桥
// （非 browser-runtime 实例）报错，不代杀。
func TestStopWithoutInstance(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	offline := NewCourseConfig(fixtureBrowser(t), fixtureRuntime(t), t.TempDir(), "http://127.0.0.1:1", "")
	if err := Stop(ctx, offline); err != nil {
		t.Fatalf("未在运行应幂等成功: %v", err)
	}

	cdpURL, closeCDP := fakeCDP(t, 1)
	defer closeCDP()
	foreign := NewCourseConfig(fixtureBrowser(t), fixtureRuntime(t), t.TempDir(), cdpURL, "")
	err := Stop(ctx, foreign)
	if err == nil || !strings.Contains(err.Error(), "非 browser-runtime") {
		t.Fatalf("无桥实例应报错: %v", err)
	}
}
