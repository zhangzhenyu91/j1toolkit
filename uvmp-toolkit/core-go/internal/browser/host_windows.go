//go:build windows

package browser

import (
	"strings"

	"golang.org/x/sys/windows/registry"
)

// appPathCandidates 读取 Windows App Paths 注册表默认值（360 企业浏览器 360ent 安装器
// 写在这里）：HKLM→HKCU、64/32 位视图依次尝试，只返回非空路径（存在性由调用方校验）。
func appPathCandidates(exe string) []string {
	subKey := `SOFTWARE\Microsoft\Windows\CurrentVersion\App Paths\` + exe
	out := []string{}
	for _, root := range []registry.Key{registry.LOCAL_MACHINE, registry.CURRENT_USER} {
		for _, view := range []uint32{registry.WOW64_64KEY, registry.WOW64_32KEY} {
			k, err := registry.OpenKey(root, subKey, registry.QUERY_VALUE|view)
			if err != nil {
				continue
			}
			val, _, err := k.GetStringValue("")
			_ = k.Close()
			if err != nil {
				continue
			}
			if p := strings.Trim(strings.TrimSpace(val), `"`); p != "" {
				out = append(out, p)
			}
		}
	}
	return out
}
