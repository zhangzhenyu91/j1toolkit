package browser

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// 浏览器运行时统一自发现测试：模拟 fyne 安装布局 / 开发树源码布局，不访问网络。

// fakeElectronName 测试平台的宿主可执行名。
func fakeElectronName() string {
	if runtime.GOOS == "windows" {
		return "electron.exe"
	}
	return "electron"
}

// writeFakeFile 造空文件（探测只看存在性）。
func writeFakeFile(t *testing.T, p string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("fake"), 0o755); err != nil {
		t.Fatal(err)
	}
}

// writeFakeRuntimeApp 造 browser-runtime 运行时应用目录（package.json + main.cjs）。
func writeFakeRuntimeApp(t *testing.T, dir string) {
	t.Helper()
	writeFakeFile(t, filepath.Join(dir, "package.json"))
	writeFakeFile(t, filepath.Join(dir, "main.cjs"))
}

// fakeFyneLayout 模拟 fyne 安装布局：browser-runtime/electron(.exe) +
// browser-runtime/app/{package.json,main.cjs}，返回布局根。
func fakeFyneLayout(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	writeFakeRuntimeApp(t, filepath.Join(root, "browser-runtime", "app"))
	writeFakeFile(t, filepath.Join(root, "browser-runtime", fakeElectronName()))
	return root
}

// fakeSourceLayout 模拟开发树：browser-runtime/{package.json,main.cjs} +
// desktop/node_modules/electron/dist/electron(.exe)，返回布局根。
func fakeSourceLayout(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	writeFakeRuntimeApp(t, filepath.Join(root, "browser-runtime"))
	writeFakeFile(t, filepath.Join(root, "desktop", "node_modules", "electron", "dist", fakeElectronName()))
	return root
}

func TestScanRuntimeInFyneLayout(t *testing.T) {
	root := fakeFyneLayout(t)
	rt := ScanRuntimeIn([]string{root})
	wantElectron := filepath.Join(root, "browser-runtime", fakeElectronName())
	wantApp := filepath.Join(root, "browser-runtime", "app")
	if rt.Electron != wantElectron {
		t.Fatalf("Electron = %q, want %q", rt.Electron, wantElectron)
	}
	if rt.AppDir != wantApp {
		t.Fatalf("AppDir = %q, want %q", rt.AppDir, wantApp)
	}
	if _, err := DetectRuntimeIn([]string{root}); err != nil {
		t.Fatalf("DetectRuntimeIn: %v", err)
	}
}

func TestScanRuntimeInSourceLayout(t *testing.T) {
	root := fakeSourceLayout(t)
	rt := ScanRuntimeIn([]string{root})
	wantApp := filepath.Join(root, "browser-runtime")
	wantElectron := filepath.Join(root, "desktop", "node_modules", "electron", "dist", fakeElectronName())
	if rt.AppDir != wantApp {
		t.Fatalf("AppDir = %q, want %q", rt.AppDir, wantApp)
	}
	if rt.Electron != wantElectron {
		t.Fatalf("Electron = %q, want %q", rt.Electron, wantElectron)
	}
}

func TestScanRuntimeInPrefersAppDir(t *testing.T) {
	// 同时存在 app/（fyne）与源码直落布局时 app/ 优先
	root := fakeFyneLayout(t)
	writeFakeRuntimeApp(t, filepath.Join(root, "browser-runtime"))
	rt := ScanRuntimeIn([]string{root})
	wantApp := filepath.Join(root, "browser-runtime", "app")
	if rt.AppDir != wantApp {
		t.Fatalf("AppDir = %q, want %q（app/ 优先）", rt.AppDir, wantApp)
	}
}

func TestScanRuntimeInHistoricalLayout(t *testing.T) {
	// 历史装配：package.json 在 browser-runtime/、入口 main.cjs 在 app/
	root := t.TempDir()
	writeFakeFile(t, filepath.Join(root, "browser-runtime", "package.json"))
	writeFakeFile(t, filepath.Join(root, "browser-runtime", "app", "main.cjs"))
	writeFakeFile(t, filepath.Join(root, "browser-runtime", fakeElectronName()))
	rt := ScanRuntimeIn([]string{root})
	wantApp := filepath.Join(root, "browser-runtime")
	if rt.AppDir != wantApp {
		t.Fatalf("AppDir = %q, want %q（历史装配）", rt.AppDir, wantApp)
	}
	if rt.Electron == "" {
		t.Fatal("Electron 不应为空")
	}
}

func TestScanRuntimeInPartial(t *testing.T) {
	// 只有应用目录无 Electron：AppDir 有值、Electron 留空
	root := t.TempDir()
	writeFakeRuntimeApp(t, filepath.Join(root, "browser-runtime", "app"))
	rt := ScanRuntimeIn([]string{root})
	if rt.AppDir == "" || rt.Electron != "" {
		t.Fatalf("rt = %+v", rt)
	}
	if _, err := DetectRuntimeIn([]string{root}); err == nil || !strings.Contains(err.Error(), "Electron") {
		t.Fatalf("缺 Electron 应报错: %v", err)
	}
}

func TestScanRuntimeInUvmpToolkitSubdir(t *testing.T) {
	// 从仓库根启动：根/uvmp-toolkit/ 开发子目录同样命中
	root := fakeSourceLayout(t)
	sub := t.TempDir()
	if err := os.Rename(root, filepath.Join(sub, "uvmp-toolkit")); err != nil {
		t.Fatal(err)
	}
	rt := ScanRuntimeIn([]string{sub})
	if !strings.HasSuffix(rt.AppDir, filepath.Join("uvmp-toolkit", "browser-runtime")) {
		t.Fatalf("AppDir = %q", rt.AppDir)
	}
	if rt.Electron == "" {
		t.Fatal("Electron 不应为空")
	}
}

func TestDetectRuntimeInNotFound(t *testing.T) {
	_, err := DetectRuntimeIn([]string{t.TempDir()})
	if err == nil || !strings.Contains(err.Error(), "browser-runtime") {
		t.Fatalf("err = %v", err)
	}
}
