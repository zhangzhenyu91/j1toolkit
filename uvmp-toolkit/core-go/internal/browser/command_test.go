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

// TestBuildArgsExport 导出模式命令行：随机调试端口、独立 profile、模式标记，末尾为
// 运行时应用目录；不带课程专属开关。
func TestBuildArgsExport(t *testing.T) {
	cfg := NewExportConfig(fixtureBrowser(t), fixtureRuntime(t), "/tmp/profile-export")
	cfg.SPKIPins = []string{"pinA", "pinB"}
	c, err := cfg.Validate()
	if err != nil {
		t.Fatal(err)
	}
	args := c.buildArgs()
	if args[len(args)-1] != c.RuntimeDir {
		t.Fatalf("末尾应为 RuntimeDir：%v", args)
	}
	for _, want := range []string{
		"--user-data-dir=/tmp/profile-export",
		"--window-size=1500,950",
		"--remote-allow-origins=*",
		"--disable-dev-shm-usage",
		"--uvmp-browser-mode=export",
		"--remote-debugging-port=0",
		"--disable-gpu",
		"--no-sandbox",
		"--ignore-certificate-errors-spki-list=pinA;pinB",
	} {
		if !hasArg(args, want) {
			t.Fatalf("缺少参数 %q：%v", want, args)
		}
	}
	for _, banned := range []string{"--uvmp-browser-mode=course", "--uvmp-start-url="} {
		if hasPrefixArg(args, banned) {
			t.Fatalf("导出模式不应带 %q：%v", banned, args)
		}
	}
}

// TestBuildArgsCourse 课程模式命令行：调试端口取 CDPURL（默认 9222）、持久 profile、
// start_url 传递、与 Python browser_automation.launch 同款反首次运行开关。
func TestBuildArgsCourse(t *testing.T) {
	cfg := NewCourseConfig(fixtureBrowser(t), fixtureRuntime(t), "/tmp/profile-course",
		"", "https://edu.example.com/course-list")
	cfg.CDPURL = "" // 走默认 9222
	c, err := cfg.Validate()
	if err != nil {
		t.Fatal(err)
	}
	args := c.buildArgs()
	for _, want := range []string{
		"--user-data-dir=/tmp/profile-course",
		"--uvmp-browser-mode=course",
		"--remote-debugging-port=9222",
		"--no-first-run",
		"--no-default-browser-check",
		"--uvmp-start-url=https://edu.example.com/course-list",
	} {
		if !hasArg(args, want) {
			t.Fatalf("缺少参数 %q：%v", want, args)
		}
	}
	if hasArg(args, "--disable-gpu") {
		t.Fatalf("课程模式不强制 disable-gpu：%v", args)
	}
	if hasPrefixArg(args, "--ignore-certificate-errors-spki-list") {
		t.Fatalf("未配 pin 不应带 SPKI 开关：%v", args)
	}

	// 自定义端口随 CDPURL
	cfg2 := NewCourseConfig(fixtureBrowser(t), fixtureRuntime(t), "/tmp/p", "http://127.0.0.1:19222", "")
	c2, err := cfg2.Validate()
	if err != nil {
		t.Fatal(err)
	}
	if !hasArg(c2.buildArgs(), "--remote-debugging-port=19222") {
		t.Fatalf("自定义端口未生效：%v", c2.buildArgs())
	}
}
