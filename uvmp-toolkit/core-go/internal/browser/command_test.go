package browser

import (
	"strings"
	"testing"
)

// hasArg 断言参数集合里存在完全匹配项。
func hasArg(args []string, want string) bool {
	for _, a := range args {
		if a == want {
			return true
		}
	}
	return false
}

// hasPrefixArg 断言参数集合里存在指定前缀项。
func hasPrefixArg(args []string, prefix string) bool {
	for _, a := range args {
		if strings.HasPrefix(a, prefix) {
			return true
		}
	}
	return false
}

// TestBuildArgsExportHost 宿主浏览器导出：随机调试端口、独立 profile、无头不可见，
// 不带 Electron 专属开关/应用目录位置参数。
func TestBuildArgsExportHost(t *testing.T) {
	cfg := NewExportConfig(fixtureBrowser(t), "", "/tmp/profile-export")
	cfg.SPKIPins = []string{"pinA", "pinB"}
	c, err := cfg.Validate()
	if err != nil {
		t.Fatal(err)
	}
	args := c.buildArgs()
	for _, want := range []string{
		"--user-data-dir=/tmp/profile-export",
		"--window-size=1500,950",
		"--remote-allow-origins=*",
		"--disable-dev-shm-usage",
		"--headless=new",
		"--remote-debugging-port=0",
		"--disable-gpu",
		"--no-sandbox",
		"--ignore-certificate-errors-spki-list=pinA;pinB",
	} {
		if !hasArg(args, want) {
			t.Fatalf("缺少参数 %q：%v", want, args)
		}
	}
	for _, banned := range []string{"--uvmp-browser-mode=export", "--uvmp-browser-mode=course", "--uvmp-start-url="} {
		if hasPrefixArg(args, banned) {
			t.Fatalf("宿主浏览器导出不应带 %q：%v", banned, args)
		}
	}
	if args[len(args)-1] != "--ignore-certificate-errors-spki-list=pinA;pinB" {
		t.Fatalf("宿主浏览器导出末尾不应有应用目录位置参数：%v", args)
	}
}

// TestBuildArgsExportElectron Electron 回退导出：双模式 export 开关 + 末尾运行时目录，
// 不变（历史兼容）。
func TestBuildArgsExportElectron(t *testing.T) {
	c, err := NewExportConfig(fixtureBrowser(t), fixtureRuntime(t), "/tmp/profile-export").Validate()
	if err != nil {
		t.Fatal(err)
	}
	args := c.buildArgs()
	if args[len(args)-1] != c.RuntimeDir {
		t.Fatalf("末尾应为 RuntimeDir：%v", args)
	}
	for _, want := range []string{"--uvmp-browser-mode=export", "--remote-debugging-port=0", "--disable-gpu"} {
		if !hasArg(args, want) {
			t.Fatalf("缺少参数 %q：%v", want, args)
		}
	}
	if hasArg(args, "--headless=new") {
		t.Fatalf("Electron 回退不应带 --headless=new：%v", args)
	}
}

// TestBuildArgsCourseHost 宿主浏览器课程模式：调试端口取 CDPURL（默认 9222）、持久
// profile、可见窗口、start_url 直接作为首窗位置参数；不带任何 Electron 专属参数。
func TestBuildArgsCourseHost(t *testing.T) {
	cfg := NewCourseConfig(fixtureBrowser(t), "", "/tmp/profile-course",
		"", "https://edu.example.com/course-list")
	cfg.CDPURL = "" // 走默认 9222
	c, err := cfg.Validate()
	if err != nil {
		t.Fatal(err)
	}
	args := c.buildArgs()
	for _, want := range []string{
		"--user-data-dir=/tmp/profile-course",
		"--remote-debugging-port=9222",
		"--no-first-run",
		"--no-default-browser-check",
	} {
		if !hasArg(args, want) {
			t.Fatalf("缺少参数 %q：%v", want, args)
		}
	}
	if args[len(args)-1] != "https://edu.example.com/course-list" {
		t.Fatalf("start_url 应直接作为首窗位置参数：%v", args)
	}
	for _, banned := range []string{"--uvmp-browser-mode", "--uvmp-start-url"} {
		if hasPrefixArg(args, banned) {
			t.Fatalf("宿主浏览器不应带 Electron 专属参数 %q：%v", banned, args)
		}
	}
	if hasArg(args, "--headless=new") {
		t.Fatalf("课程模式必须可见窗口：%v", args)
	}
	if hasArg(args, "--disable-gpu") {
		t.Fatalf("课程模式不强制 disable-gpu：%v", args)
	}
	if hasPrefixArg(args, "--ignore-certificate-errors-spki-list") {
		t.Fatalf("未配 pin 不应带 SPKI 开关：%v", args)
	}

	// 自定义端口随 CDPURL；空 start_url 不追加位置参数
	cfg2 := NewCourseConfig(fixtureBrowser(t), "", "/tmp/p", "http://127.0.0.1:19222", "")
	c2, err := cfg2.Validate()
	if err != nil {
		t.Fatal(err)
	}
	args2 := c2.buildArgs()
	if !hasArg(args2, "--remote-debugging-port=19222") {
		t.Fatalf("自定义端口未生效：%v", args2)
	}
	if last := args2[len(args2)-1]; strings.HasPrefix(last, "http") || strings.HasPrefix(last, "data:") {
		t.Fatalf("空 start_url 不应追加位置参数：%v", args2)
	}
}

// TestBuildArgsCourseElectron Electron 回退课程模式：--uvmp-browser-mode=course +
// --uvmp-start-url + 末尾应用目录（历史兼容）。
func TestBuildArgsCourseElectron(t *testing.T) {
	c, err := NewCourseConfig(fixtureBrowser(t), fixtureRuntime(t), "/tmp/profile-course",
		"", "https://edu.example.com/course-list").Validate()
	if err != nil {
		t.Fatal(err)
	}
	args := c.buildArgs()
	for _, want := range []string{
		"--uvmp-browser-mode=course",
		"--remote-debugging-port=9222",
		"--uvmp-start-url=https://edu.example.com/course-list",
	} {
		if !hasArg(args, want) {
			t.Fatalf("缺少参数 %q：%v", want, args)
		}
	}
	if args[len(args)-1] != c.RuntimeDir {
		t.Fatalf("末尾应为 RuntimeDir：%v", args)
	}
}
