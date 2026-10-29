// 题库搜题：数据目录解析（兼容旧 Electron userData/quiz-search 原地可读）。
//
// 旧 Electron 实现把 settings.json 与 banks-* 落在 app.getPath('userData')/quiz-search：
// Windows 为 %APPDATA%/Shade 壹匣 - 内网/quiz-search，Linux 为 ~/.config/Shade 壹匣 - 内网/quiz-search
// （两者都是 os.UserConfigDir()/Shade 壹匣 - 内网/quiz-search）。升级到 Go 实现后
// DataDir 解析到同一位置时旧 banks/settings 原地可读（store/settings JSON 双向兼容），
// 不迁移、不复制。
package quizsearch

import (
	"os"
	"path/filepath"
)

// legacyAppDir 旧 Electron userData 目录名（electron-builder productName）。
const legacyAppDir = "Shade 壹匣 - 内网"

// LegacyDataDir 旧 Electron 题库数据目录（os.UserConfigDir()/Shade 壹匣 - 内网/quiz-search）。
// UserConfigDir 不可用时返回空串。
func LegacyDataDir() string {
	base, err := os.UserConfigDir()
	if err != nil || base == "" {
		return ""
	}
	return filepath.Join(base, legacyAppDir, "quiz-search")
}

// HasData 目录内是否已有题库/设置数据（banks-index.json / settings.json / banks/*.json）。
func HasData(dir string) bool {
	if dir == "" {
		return false
	}
	if _, err := os.Stat(filepath.Join(dir, "banks-index.json")); err == nil {
		return true
	}
	if _, err := os.Stat(filepath.Join(dir, "settings.json")); err == nil {
		return true
	}
	m, _ := filepath.Glob(filepath.Join(dir, "banks", "*.json"))
	return len(m) > 0
}

// ResolveDataDir 解析题库数据目录（fallbackRoot 一般传状态目录根）：
//  1. 旧 Electron 目录有数据 → 原地续用（旧 banks/settings 可读，升级不丢库）；
//  2. 状态目录/quiz-search 已有数据（更早 Go 版本写入）→ 不丢，继续用；
//  3. 全新安装 → 与旧 Electron 同址（os.UserConfigDir()/Shade 壹匣 - 内网/quiz-search），
//     之后新旧实现双向兼容同一目录；
//  4. UserConfigDir 不可用 → 状态目录/quiz-search。
func ResolveDataDir(fallbackRoot string) string {
	legacy := LegacyDataDir()
	fallback := filepath.Join(fallbackRoot, "quiz-search")
	switch {
	case HasData(legacy):
		return legacy
	case HasData(fallback):
		return fallback
	case legacy != "":
		return legacy
	default:
		return fallback
	}
}
