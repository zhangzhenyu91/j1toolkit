//go:build !windows

package browser

// appPathCandidates 非 Windows 无 App Paths 注册表（Linux/麒麟走 PATH 与固定路径）。
func appPathCandidates(string) []string { return nil }
