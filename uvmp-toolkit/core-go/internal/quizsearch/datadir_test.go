// 数据目录解析测试：旧 Electron 目录兼容（原地可读）与回落口径。
package quizsearch

import (
	"os"
	"path/filepath"
	"testing"
)

// setUserConfigDir 把 os.UserConfigDir 指向临时目录（Windows=%APPDATA%，Linux=$XDG_CONFIG_HOME 或 ~/.config）。
func setUserConfigDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("APPDATA", dir)
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("HOME", dir)
	return dir
}

func TestResolveDataDirPrefersLegacyWithData(t *testing.T) {
	ucd := setUserConfigDir(t)
	legacy := filepath.Join(ucd, legacyAppDir, "quiz-search")
	if err := os.MkdirAll(filepath.Join(legacy, "banks"), 0o755); err != nil {
		t.Fatal(err)
	}
	// 旧 banks 原地可读：只有 banks/<id>.json 也算有数据
	if err := os.WriteFile(filepath.Join(legacy, "banks", "b1.json"), []byte(`{"id":"b1","questions":[]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	got := ResolveDataDir(t.TempDir())
	if got != legacy {
		t.Fatalf("ResolveDataDir = %s，want 旧 Electron 目录 %s", got, legacy)
	}
	if !HasData(legacy) {
		t.Fatal("HasData 应识别 banks/*.json")
	}
}

func TestResolveDataDirLegacySettingsInPlace(t *testing.T) {
	ucd := setUserConfigDir(t)
	legacy := filepath.Join(ucd, legacyAppDir, "quiz-search")
	if err := os.MkdirAll(legacy, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(legacy, "settings.json"), []byte(`{"interval":1200}`), 0o644); err != nil {
		t.Fatal(err)
	}
	// 设置文件原地读：LoadSettings 应取到旧值
	s := LoadSettings(filepath.Join(legacy, "settings.json"), WorkArea{W: 1920, H: 1080})
	if s.Interval != 1200 {
		t.Fatalf("旧 settings.json 未原地生效：%+v", s)
	}
	if got := ResolveDataDir(t.TempDir()); got != legacy {
		t.Fatalf("ResolveDataDir = %s，want %s", got, legacy)
	}
}

func TestResolveDataDirKeepsExistingFallbackData(t *testing.T) {
	ucd := setUserConfigDir(t)
	fallbackRoot := t.TempDir()
	fallback := filepath.Join(fallbackRoot, "quiz-search")
	if err := os.MkdirAll(fallback, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fallback, "banks-index.json"), []byte(`{"banks":[]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := ResolveDataDir(fallbackRoot); got != fallback {
		t.Fatalf("ResolveDataDir = %s，want 已有数据的回落目录 %s", got, fallback)
	}
	if HasData(filepath.Join(ucd, legacyAppDir, "quiz-search")) {
		t.Fatal("不存在的旧目录不应判为有数据")
	}
}

func TestResolveDataDirFreshFallsToLegacy(t *testing.T) {
	ucd := setUserConfigDir(t)
	got := ResolveDataDir(t.TempDir())
	want := filepath.Join(ucd, legacyAppDir, "quiz-search")
	if got != want {
		t.Fatalf("全新安装 ResolveDataDir = %s，want 旧 Electron 同址 %s", got, want)
	}
	if LegacyDataDir() != want {
		t.Fatalf("LegacyDataDir = %s，want %s", LegacyDataDir(), want)
	}
}
