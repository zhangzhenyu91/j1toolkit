package browser

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fixtureRuntime 造一个最小 browser-runtime 目录（package.json + main.cjs），返回目录。
func fixtureRuntime(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for name, content := range map[string]string{
		"package.json": `{"name":"uvmp-browser-runtime","main":"main.cjs"}`,
		"main.cjs":     "// 测试桩\n",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// fixtureBrowser 造一个假的宿主可执行文件路径（仅存在性校验用）。
func fixtureBrowser(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "electron-stub.exe")
	if err := os.WriteFile(p, []byte(""), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

// TestValidateLoopbackURL 仅回环校验：接受本机回环 http 地址，拒绝远程/https/无端口/
// 带路径查询用户信息的地址（与 Python browser_automation.validate_cdp_url 同口径）。
func TestValidateLoopbackURL(t *testing.T) {
	ok := []struct {
		in   string
		want string // 规范化地址（空=与 in 相同）
		port int
	}{
		{"http://127.0.0.1:9222", "", 9222},
		{"http://localhost:9222", "", 9222},
		{"http://[::1]:9222", "", 9222},
		{"http://127.0.0.1:19222", "", 19222},
		{"http://127.0.0.1:9222/", "http://127.0.0.1:9222", 9222},
		{"  http://127.0.0.1:9222  ", "http://127.0.0.1:9222", 9222},
	}
	for _, tc := range ok {
		got, port, err := ValidateLoopbackURL(tc.in)
		if err != nil {
			t.Fatalf("ValidateLoopbackURL(%q) 报错: %v", tc.in, err)
		}
		want := tc.want
		if want == "" {
			want = strings.TrimSpace(tc.in)
		}
		if got != want || port != tc.port {
			t.Fatalf("ValidateLoopbackURL(%q) = %q, %d；want %q, %d", tc.in, got, port, want, tc.port)
		}
	}

	bad := []string{
		"",
		"https://127.0.0.1:9222",          // 仅 http
		"http://192.168.1.10:9222",        // 远程主机
		"http://example.com:9222",         // 远程主机
		"http://127.0.0.1",                // 无端口
		"http://127.0.0.1:0",              // 端口 0
		"http://127.0.0.1:99999",          // 端口越界
		"http://user:pass@127.0.0.1:9222", // 用户信息
		"http://127.0.0.1:9222/json",      // 带路径
		"http://127.0.0.1:9222?a=1",       // 带查询
		"http://127.0.0.1:9222#frag",      // 带片段
		"ws://127.0.0.1:9222",             // 非 http
		"not a url",                       // 解析失败/协议空
	}
	for _, in := range bad {
		if got, port, err := ValidateLoopbackURL(in); err == nil {
			t.Fatalf("ValidateLoopbackURL(%q) 应报错，却通过: %q, %d", in, got, port)
		}
	}
}

// TestConfigDefaults 零值默认：课程模式 CDPURL 默认 9222 且回环校验、窗口尺寸/超时补默认；
// 宿主浏览器不需要 RuntimeDir，历史 Electron 回退才校验应用目录。
func TestConfigDefaults(t *testing.T) {
	cfg := NewCourseConfig(fixtureBrowser(t), "", t.TempDir(), "", "")
	cfg.CDPURL = "" // 强制走默认
	c, err := cfg.Validate()
	if err != nil {
		t.Fatalf("Validate 报错: %v", err)
	}
	if c.CDPURL != DefaultCourseCDPURL {
		t.Fatalf("CDPURL 默认 = %q；want %q", c.CDPURL, DefaultCourseCDPURL)
	}
	if c.Width != DefaultWidth || c.Height != DefaultHeight {
		t.Fatalf("窗口尺寸默认 = %dx%d；want %dx%d", c.Width, c.Height, DefaultWidth, DefaultHeight)
	}
	if c.ReadyTimeout != DefaultReadyTimeout || c.StopGrace != DefaultStopGrace {
		t.Fatalf("超时默认 = %v/%v；want %v/%v", c.ReadyTimeout, c.StopGrace, DefaultReadyTimeout, DefaultStopGrace)
	}
	if c.ElectronFallback() {
		t.Fatal("空 RuntimeDir 应视为宿主浏览器，不是 Electron 回退")
	}

	// Electron 回退：RuntimeDir 非空时才要求应用目录完整
	exp := NewExportConfig(fixtureBrowser(t), fixtureRuntime(t), t.TempDir())
	ce, err := exp.Validate()
	if err != nil {
		t.Fatalf("导出配置 Validate 报错: %v", err)
	}
	if ce.Mode != ModeExport || ce.CDPURL != "" || ce.StartURL != "" {
		t.Fatalf("导出配置被误填: %+v", ce)
	}
	if !ce.ElectronFallback() {
		t.Fatal("指定 RuntimeDir 应识别为 Electron 回退")
	}
}

// TestConfigValidateErrors 必填项与模式专属字段的错误路径。
func TestConfigValidateErrors(t *testing.T) {
	rt := fixtureRuntime(t)
	bin := fixtureBrowser(t)
	base := func() Config {
		return NewCourseConfig(bin, rt, t.TempDir(), "http://127.0.0.1:9222", "https://example.com/course")
	}

	cases := []struct {
		name   string
		mut    func(*Config)
		errSub string
	}{
		{"缺 Mode", func(c *Config) { c.Mode = "" }, "Mode"},
		{"缺 BrowserPath", func(c *Config) { c.BrowserPath = "" }, "BrowserPath"},
		{"BrowserPath 不存在", func(c *Config) { c.BrowserPath = filepath.Join(t.TempDir(), "nope") }, "不存在"},
		{"Electron 回退 RuntimeDir 非运行时", func(c *Config) { c.RuntimeDir = t.TempDir() }, "RuntimeDir 缺少"},
		{"Electron 回退缺 main.cjs", func(c *Config) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte("{}"), 0o644); err != nil {
				t.Fatal(err)
			}
			c.RuntimeDir = dir
		}, "RuntimeDir 缺少"},
		{"缺 ProfileDir", func(c *Config) { c.ProfileDir = "" }, "ProfileDir"},
		{"CDP 地址非回环", func(c *Config) { c.CDPURL = "http://192.168.0.1:9222" }, "回环"},
		{"StartURL 协议不支持", func(c *Config) { c.StartURL = "javascript:alert(1)" }, "协议"},
		{"StartURL 带入导出模式", func(c *Config) {
			c.Mode = ModeExport
			c.CDPURL = ""
		}, "仅课程模式"},
		{"CDPURL 带入导出模式", func(c *Config) {
			c.Mode = ModeExport
			c.StartURL = ""
			c.CDPURL = "http://127.0.0.1:9222"
		}, "仅课程模式"},
	}
	for _, tc := range cases {
		cfg := base()
		tc.mut(&cfg)
		_, err := cfg.Validate()
		if err == nil || !strings.Contains(err.Error(), tc.errSub) {
			t.Fatalf("%s：错误 = %v；want 含 %q", tc.name, err, tc.errSub)
		}
	}

	// data:/about: 启动网址合法（本地测试页/空白页）
	for _, u := range []string{"data:text/html;base64,PGgxPmE8L2gxPg==", "about:blank", ""} {
		cfg := base()
		cfg.StartURL = u
		if _, err := cfg.Validate(); err != nil {
			t.Fatalf("StartURL %q 应合法: %v", u, err)
		}
	}
}

// TestConfigSPKIPins SPKI pin 空白项剔除。
func TestConfigSPKIPins(t *testing.T) {
	cfg := NewExportConfig(fixtureBrowser(t), fixtureRuntime(t), t.TempDir())
	cfg.SPKIPins = []string{"pinA", " ", "pinB"}
	c, err := cfg.Validate()
	if err != nil {
		t.Fatal(err)
	}
	if len(c.SPKIPins) != 2 || c.SPKIPins[0] != "pinA" || c.SPKIPins[1] != "pinB" {
		t.Fatalf("SPKIPins = %v", c.SPKIPins)
	}
}
