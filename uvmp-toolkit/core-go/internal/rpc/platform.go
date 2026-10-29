package rpc

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/zhangzhenyu91/j1toolkit/uvmp-toolkit/core-go/internal/browser"
	"github.com/zhangzhenyu91/j1toolkit/uvmp-toolkit/core-go/internal/config"
)

// detectChrome 返回可用的宿主浏览器路径（getSystemInfo 用）；找不到报错。
// preferred 非空 = 用户在设置页/配置里指定的公司浏览器，优先且必须存在。
// 探测口径统一在 internal/browser：360ent（Windows 注册表 App Paths / ProgramData /
// PATH / 常见目录；Linux browser360ent-cn-stable 优先）→ qaxbrowser / Chrome / Edge。
func detectChrome(preferred string) (string, error) {
	h, err := browser.DetectHost(preferred)
	if err != nil {
		return "", err
	}
	return h.Path, nil
}

// exeDir 可执行文件所在目录（suggested_outdirs 的 output 基准）。
func exeDir() string {
	p, err := os.Executable()
	if err != nil {
		return "."
	}
	if resolved, rerr := filepath.EvalSymlinks(p); rerr == nil {
		p = resolved
	}
	return filepath.Dir(p)
}

// openFile 打开文件（存在性检查用）。
func openFile(p string) (*os.File, error) {
	return os.Open(p)
}

// readFileString 读文本文件（非法字节替换）。
func readFileString(p string) (string, error) {
	data, err := os.ReadFile(p)
	if err != nil {
		return "", err
	}
	return strings.ToValidUTF8(string(data), "\uFFFD"), nil
}

// dirStat 目录下文件数与总字节。
func dirStat(dir string) (int, int64) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0, 0
	}
	n, total := 0, int64(0)
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		n++
		if info, err := e.Info(); err == nil {
			total += info.Size()
		}
	}
	return n, total
}

// configHasMock dev.mock（自检与调度判定用）。
func configHasMock(cfg config.Dict) bool {
	return config.BoolField(config.MapField(cfg, "dev"), "mock", false)
}
