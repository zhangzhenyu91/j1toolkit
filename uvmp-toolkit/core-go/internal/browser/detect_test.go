package browser

import (
	"errors"
	"os"
	"strings"
	"testing"
)

// 宿主浏览器统一探测测试：注入候选/查询/文件系统，不触碰真实注册表、不访问网络。

// fakeProbe 造一个可控探测环境：files 中的路径视为存在；lookups 中的名字视为
// PATH 命中；appPaths 为注册表 App Paths 返回值。
func fakeProbe(goos string, files, lookups, appPaths []string) Probe {
	fileSet := map[string]bool{}
	for _, f := range files {
		fileSet[f] = true
	}
	lookSet := map[string]bool{}
	for _, l := range lookups {
		lookSet[l] = true
	}
	return Probe{
		GOOS: goos,
		LookPath: func(name string) (string, error) {
			if lookSet[name] {
				return name, nil
			}
			return "", errors.New("not found")
		},
		Exists: func(p string) bool {
			return fileSet[p] || lookSet[p]
		},
		AppPath: func(string) []string { return appPaths },
		Getenv:  func(string) string { return "" },
	}
}

// TestDetectHostExplicitChrome 显式 render.chrome 优先且必须存在。
func TestDetectHostExplicitChrome(t *testing.T) {
	p := fakeProbe("windows", []string{`C:\chrome.exe`}, nil, nil)
	h, err := DetectHostIn(p, `C:\chrome.exe`)
	if err != nil {
		t.Fatal(err)
	}
	if h.Path != `C:\chrome.exe` || !strings.Contains(h.Source, "render.chrome") {
		t.Fatalf("显式选择结果 = %+v", h)
	}
	if _, err := DetectHostIn(p, `C:\missing.exe`); err == nil || !strings.Contains(err.Error(), "指定的浏览器不存在") {
		t.Fatalf("显式浏览器不存在应报错: %v", err)
	}
}

// TestDetectHostEnvOverride UVMP_BROWSER_BIN 是显式指定（CI/门禁），优先于自动检测，
// 但让位于显式 render.chrome；路径不存在时明确报错而不是静默回落。
func TestDetectHostEnvOverride(t *testing.T) {
	p := fakeProbe("linux", []string{"/usr/bin/browser360ent-cn-stable", "/ci/chromium"}, nil, nil)
	p.Getenv = func(k string) string {
		if k == EnvBrowserBin {
			return "/ci/chromium"
		}
		return ""
	}
	h, err := DetectHostIn(p, "")
	if err != nil {
		t.Fatal(err)
	}
	if h.Path != "/ci/chromium" || !strings.Contains(h.Source, EnvBrowserBin) {
		t.Fatalf("环境变量应优先于自动检测: %+v", h)
	}
	h, err = DetectHostIn(p, "/usr/bin/browser360ent-cn-stable")
	if err != nil || h.Path != "/usr/bin/browser360ent-cn-stable" {
		t.Fatalf("显式 render.chrome 应优先于环境变量: %+v %v", h, err)
	}

	p.Getenv = func(string) string { return "/ci/missing" }
	if _, err := DetectHostIn(p, ""); err == nil || !strings.Contains(err.Error(), EnvBrowserBin) {
		t.Fatalf("环境变量路径不存在应明确报错: %v", err)
	}
}

// TestDetectHostWindows360entOrder Windows：360ent 注册表 App Paths（HKLM/HKCU）→
// %ProgramData%\360ent\Application\360ent.exe（环境变量优先，缺省 C:\ProgramData）→
// PATH / 常见目录。
func TestDetectHostWindows360entOrder(t *testing.T) {
	registryPath := `C:\Registry\360ent.exe`
	programData := `C:\ProgramData\360ent\Application\360ent.exe`

	// 注册表命中优先于 ProgramData 与 PATH
	p := fakeProbe("windows", []string{registryPath, programData}, []string{"360ent.exe"}, []string{registryPath})
	h, err := DetectHostIn(p, "")
	if err != nil || h.Path != registryPath || h.Source != "注册表 App Paths" {
		t.Fatalf("注册表优先结果 = %+v, %v", h, err)
	}

	// 注册表未写：ProgramData 固定路径优先于 PATH
	p = fakeProbe("windows", []string{programData}, []string{"360ent.exe"}, nil)
	h, err = DetectHostIn(p, "")
	if err != nil || h.Path != programData {
		t.Fatalf("ProgramData 应优先于 PATH: %+v, %v", h, err)
	}

	// ProgramData 环境变量优先于缺省 C:\ProgramData
	envProgramData := `D:\CorpData`
	envProgramDataPath := `D:\CorpData\360ent\Application\360ent.exe`
	p = fakeProbe("windows", []string{envProgramDataPath}, nil, nil)
	p.Getenv = func(k string) string {
		if k == "ProgramData" {
			return envProgramData
		}
		return ""
	}
	h, err = DetectHostIn(p, "")
	if err != nil || h.Path != envProgramDataPath {
		t.Fatalf("ProgramData 环境变量未生效: %+v, %v", h, err)
	}

	// 都没有：PATH 命中 360ent.exe
	p = fakeProbe("windows", nil, []string{"360ent.exe"}, nil)
	h, err = DetectHostIn(p, "")
	if err != nil || !strings.HasSuffix(h.Path, "360ent.exe") || h.Source != "PATH" {
		t.Fatalf("PATH 命中结果 = %+v, %v", h, err)
	}

	// 360ent 缺失时兼容 Chrome / Edge（360ent 全链路优先）
	p = fakeProbe("windows", []string{`C:\Program Files\Microsoft\Edge\Application\msedge.exe`},
		[]string{"msedge.exe"}, nil)
	h, err = DetectHostIn(p, "")
	if err != nil || h.Name != "Microsoft Edge" {
		t.Fatalf("Edge 兜底结果 = %+v, %v", h, err)
	}
}

// TestDetectHostLinux360entFirst Linux：browser360ent-cn-stable（PATH 与 /usr/bin）优先，
// 兼容 qaxbrowser / Chrome / Edge。
func TestDetectHostLinux360entFirst(t *testing.T) {
	p := fakeProbe("linux", []string{"/usr/bin/browser360ent-cn-stable"}, []string{"browser360ent-cn-stable"}, nil)
	h, err := DetectHostIn(p, "")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(h.Path, "browser360ent-cn-stable") {
		t.Fatalf("应优先 browser360ent-cn-stable: %+v", h)
	}

	// PATH 未命中时用 /usr/bin 固定路径
	p = fakeProbe("linux", []string{"/usr/bin/browser360ent-cn-stable"}, nil, nil)
	h, err = DetectHostIn(p, "")
	if err != nil || h.Path != "/usr/bin/browser360ent-cn-stable" || h.Source != "常见目录" {
		t.Fatalf("/usr/bin 固定路径结果 = %+v, %v", h, err)
	}

	// 兼容奇安信可信浏览器
	p = fakeProbe("linux", []string{"/usr/bin/qaxbrowser"}, nil, nil)
	h, err = DetectHostIn(p, "")
	if err != nil || h.Name != "奇安信可信浏览器" {
		t.Fatalf("qaxbrowser 兜底结果 = %+v, %v", h, err)
	}

	// 再兜底 Chrome / Chromium / Edge
	p = fakeProbe("linux", nil, []string{"google-chrome-stable"}, nil)
	h, err = DetectHostIn(p, "")
	if err != nil || h.Name != "Google Chrome" {
		t.Fatalf("Chrome 兜底结果 = %+v, %v", h, err)
	}
	p = fakeProbe("linux", nil, []string{"microsoft-edge-stable"}, nil)
	h, err = DetectHostIn(p, "")
	if err != nil || h.Name != "Microsoft Edge" {
		t.Fatalf("Edge 兜底结果 = %+v, %v", h, err)
	}
}

// TestDetectHostNotFound 全部候选缺失：可操作错误（安装 360ent 或设置页手选）。
func TestDetectHostNotFound(t *testing.T) {
	p := fakeProbe("linux", nil, nil, nil)
	_, err := DetectHostIn(p, "")
	if err == nil || !strings.Contains(err.Error(), "未找到可用浏览器") || !strings.Contains(err.Error(), "360ent") {
		t.Fatalf("未找到浏览器错误 = %v", err)
	}
}

// TestAppPathCandidatesNonWindows 非 Windows 无注册表候选。
func TestAppPathCandidatesNonWindows(t *testing.T) {
	if os.PathSeparator != '\\' { // 仅在非 Windows 执行
		if got := appPathCandidates("360ent.exe"); len(got) != 0 {
			t.Fatalf("非 Windows 不应有 App Paths 候选: %v", got)
		}
	}
}
