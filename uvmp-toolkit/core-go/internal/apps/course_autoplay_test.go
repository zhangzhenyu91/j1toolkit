package apps

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zhangzhenyu91/j1toolkit/uvmp-toolkit/core-go/internal/config"
	"github.com/zhangzhenyu91/j1toolkit/uvmp-toolkit/core-go/internal/state"
)

// setupCourseTest 隔离状态目录与配置文件（不碰真实用户配置/状态）。
func setupCourseTest(t *testing.T, cfgJSON string) {
	t.Helper()
	state.SetRoot(t.TempDir())
	cfgPath := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(cfgPath, []byte(cfgJSON), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("UVMP_TOOLKIT_CONFIG", cfgPath)
	t.Setenv("UVMP_ELECTRON", "")
	t.Setenv("UVMP_BROWSER_RUNTIME_DIR", "")
}

func TestCourseSettingsValidation(t *testing.T) {
	setupCourseTest(t, `{"browser_automation":{"cdp_url":"http://127.0.0.1:9222","step_timeout":120}}`)
	cfg := config.Load()
	s, err := loadCourseSettings(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if s.CDPURL != "http://127.0.0.1:9222" || s.StepTimeout.Seconds() != 120 || s.MaxAttempts != 3 {
		t.Fatalf("设置 = %+v", s)
	}

	// 远程 CDP 地址拒绝
	_, err = loadCourseSettings(config.Dict{
		"browser_automation": config.Dict{"cdp_url": "http://10.0.0.1:9222"},
	})
	if err == nil || !strings.Contains(err.Error(), "仅允许本机") {
		t.Fatalf("远程地址错误 = %v", err)
	}
	// step_timeout 非法
	_, err = loadCourseSettings(config.Dict{
		"browser_automation": config.Dict{"cdp_url": "http://127.0.0.1:9222", "step_timeout": 0},
	})
	if err == nil || !strings.Contains(err.Error(), "step_timeout 必须大于 0") {
		t.Fatalf("超时错误 = %v", err)
	}
	// 启动网址必须完整 http/https
	_, err = loadCourseSettings(config.Dict{
		"browser_automation": config.Dict{"cdp_url": "http://127.0.0.1:9222", "start_url": "ftp://x"},
	})
	if err == nil || !strings.Contains(err.Error(), "启动网址") {
		t.Fatalf("启动网址错误 = %v", err)
	}
}

func TestCourseStatusProbe(t *testing.T) {
	setupCourseTest(t, `{"browser_automation":{"cdp_url":"http://127.0.0.1:9222"}}`)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/json/list" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`[{"type":"page","id":"1"},{"type":"page","id":"2"},{"type":"service_worker"}]`))
	}))
	defer srv.Close()

	cfg := config.Dict{
		"browser_automation": config.Dict{"cdp_url": srv.URL},
	}
	res, err := (&courseAutoplayBackend{}).Status(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if res["connected"] != true || res["pages"] != 2 {
		t.Fatalf("状态 = %v", res)
	}
	if res["active_lists"] != 0 {
		t.Fatalf("active_lists = %v", res["active_lists"])
	}
	if res["engine"] != "chromedp" {
		t.Fatalf("engine = %v", res["engine"])
	}
	if res["playwright"] != true {
		t.Fatalf("playwright 兼容字段（值=引擎可用）= %v", res["playwright"])
	}

	// 端口无响应 → connected=false 但状态卡照常返回
	cfgDown := config.Dict{
		"browser_automation": config.Dict{"cdp_url": "http://127.0.0.1:1"},
	}
	res2, err := (&courseAutoplayBackend{}).Status(cfgDown)
	if err != nil {
		t.Fatal(err)
	}
	if res2["connected"] != false || res2["pages"] != 0 {
		t.Fatalf("离线状态 = %v", res2)
	}
	if res2["playwright"] != true {
		t.Fatalf("离线状态 playwright 兼容字段 = %v", res2["playwright"])
	}
}

func TestCourseRunParamValidation(t *testing.T) {
	setupCourseTest(t, `{"browser_automation":{"cdp_url":"http://127.0.0.1:9222","step_timeout":5}}`)
	b := &courseAutoplayBackend{}
	m := newTestManager(t)
	j := m.Create("course_autoplay", "测试")

	// timeout 非法
	if _, err := b.Run(j, map[string]any{"timeout": 0}); err == nil || !strings.Contains(err.Error(), "必须大于 0") {
		t.Fatalf("timeout 错误 = %v", err)
	}
	// max_attempts 非法
	if _, err := b.Run(j, map[string]any{"max_attempts": -1}); err == nil || !strings.Contains(err.Error(), "必须大于 0") {
		t.Fatalf("max_attempts 错误 = %v", err)
	}
}

func TestCourseRunNoBrowser(t *testing.T) {
	setupCourseTest(t, `{"browser_automation":{"cdp_url":"http://127.0.0.1:1","step_timeout":5}}`)
	b := &courseAutoplayBackend{}
	m := newTestManager(t)
	j := m.Create("course_autoplay", "测试")
	_, err := b.Run(j, nil)
	if err == nil || !strings.Contains(err.Error(), "无法连接") {
		t.Fatalf("未开浏览器错误 = %v", err)
	}
}

func TestCourseCapabilities(t *testing.T) {
	Reset()
	Builtin()
	a := Get("course_autoplay")
	if _, ok := a.Backend.(StatusBackend); !ok {
		t.Fatal("course_autoplay 应支持 Status")
	}
	if _, ok := a.Backend.(BrowserBackend); !ok {
		t.Fatal("course_autoplay 应支持 LaunchBrowser")
	}
}

func TestResolveBrowserHostErrors(t *testing.T) {
	setupCourseTest(t, `{}`)
	// 脱离仓库布局（自动探测不命中开发树的 browser-runtime）
	t.Chdir(t.TempDir())
	_, _, err := resolveBrowserHost(courseSettings{})
	if err == nil || !strings.Contains(err.Error(), "browser-runtime") {
		t.Fatalf("缺 runtime 错误 = %v", err)
	}
	// 指定不存在的宿主
	_, _, err = resolveBrowserHost(courseSettings{
		ElectronPath: filepath.Join(t.TempDir(), "nope"),
		RuntimeDir:   t.TempDir(),
	})
	if err == nil {
		t.Fatal("不存在的宿主应报错")
	}
}
