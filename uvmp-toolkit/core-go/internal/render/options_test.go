package render

import (
	"strings"
	"testing"
)

func TestOptionsNormalizeDefaults(t *testing.T) {
	o := Options{BrowserPath: "/usr/bin/chromium", WorkDir: "/tmp/w"}
	if err := o.normalize(); err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if o.Width != DefaultWidth || o.Height != DefaultHeight {
		t.Fatalf("窗口默认值不对: %dx%d", o.Width, o.Height)
	}
	if o.WaitDialog != DefaultWaitDialog || o.WaitTiles != DefaultWaitTiles ||
		o.WaitLogin != DefaultWaitLogin || o.IdleQuiet != DefaultIdleQuiet {
		t.Fatalf("等待时长默认值不对: %+v", o)
	}
	if o.MinWait != 0 || o.PCDWait != 0 {
		t.Fatalf("MinWait/PCDWait 零值应保持 0（表示不等待），得到 %v/%v", o.MinWait, o.PCDWait)
	}
}

func TestOptionsNormalizeRequired(t *testing.T) {
	if err := (&Options{WorkDir: "/tmp/w"}).normalize(); err == nil {
		t.Fatal("缺 BrowserPath 应报错")
	}
	if err := (&Options{BrowserPath: "/usr/bin/chromium"}).normalize(); err == nil {
		t.Fatal("缺 WorkDir 应报错")
	}
}

func TestDefaultOptions(t *testing.T) {
	o := DefaultOptions("/usr/bin/chromium", "/tmp/w")
	if err := o.normalize(); err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if o.PCDWait != DefaultPCDWait {
		t.Fatalf("DefaultOptions 应给出 PCDWait 默认值，得到 %v", o.PCDWait)
	}
}

func TestSwitchesSecurityDefaults(t *testing.T) {
	o := Options{BrowserPath: "/usr/bin/chromium", WorkDir: "/tmp/w", Width: 1500, Height: 950}
	args := strings.Join(formatSwitches(o.switches("/tmp/w/.chrome-official-tmp")), " ")
	for _, want := range []string{
		"--headless=new",
		"--remote-debugging-port=0",
		"--user-data-dir=/tmp/w/.chrome-official-tmp",
		"--window-size=1500,950",
	} {
		if !strings.Contains(args, want) {
			t.Fatalf("缺少开关 %s：%s", want, args)
		}
	}
	// 默认不得开启全局忽略证书 / 关同源策略
	for _, forbidden := range []string{"--ignore-certificate-errors", "--disable-web-security"} {
		if strings.Contains(args, forbidden) {
			t.Fatalf("默认不应带 %q：%s", forbidden, args)
		}
	}
}

func TestSwitchesSPKIPins(t *testing.T) {
	o := Options{
		BrowserPath: "/usr/bin/chromium", WorkDir: "/tmp/w",
		SPKIPins: []string{"pinA", "pinB"},
	}
	args := strings.Join(formatSwitches(o.switches("/tmp/w")), " ")
	if !strings.Contains(args, "--ignore-certificate-errors-spki-list=pinA;pinB") {
		t.Fatalf("SPKI pins 未映射到 spki-list：%s", args)
	}
}

func TestSwitchesCompatFlags(t *testing.T) {
	o := Options{
		BrowserPath: "/usr/bin/chromium", WorkDir: "/tmp/w",
		IgnoreCertificateErrors: true,
		DisableWebSecurity:      true,
	}
	args := strings.Join(formatSwitches(o.switches("/tmp/w")), " ")
	if !strings.Contains(args, "--ignore-certificate-errors") {
		t.Fatalf("兼容开关未生效：%s", args)
	}
	if !strings.Contains(args, "--disable-web-security") {
		t.Fatalf("兼容开关未生效：%s", args)
	}
}

func TestSwitchesNoHeadless(t *testing.T) {
	o := Options{BrowserPath: "/usr/bin/electron", WorkDir: "/tmp/w", NoHeadless: true}
	args := strings.Join(formatSwitches(o.switches("/tmp/w")), " ")
	if strings.Contains(args, "--headless") {
		t.Fatalf("NoHeadless 时不应带 headless 开关：%s", args)
	}
	for _, want := range []string{"--remote-debugging-port=0", "--user-data-dir=/tmp/w"} {
		if !strings.Contains(args, want) {
			t.Fatalf("NoHeadless 不应影响其它开关，缺 %s：%s", want, args)
		}
	}
}

func TestInsertBrowserArgs(t *testing.T) {
	// 插到末尾 about:blank 之前、所有 --开关 之后
	got := insertBrowserArgs([]string{"electron", "--a=1", "--b", "about:blank"}, []string{"/x/cdp-worker.cjs"})
	want := []string{"electron", "--a=1", "--b", "/x/cdp-worker.cjs", "about:blank"}
	if len(got) != len(want) {
		t.Fatalf("长度不对：%v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("第 %d 项不对：%v", i, got)
		}
	}
	// 末尾无 about:blank 时追加到末尾
	got = insertBrowserArgs([]string{"electron", "--a=1"}, []string{"s.js"})
	if got[2] != "s.js" || len(got) != 3 {
		t.Fatalf("无 about:blank 时应追加到末尾：%v", got)
	}
	// 空 extra 原样返回
	if out := insertBrowserArgs([]string{"electron"}, nil); len(out) != 1 {
		t.Fatalf("空 extra 应原样返回：%v", out)
	}
}
