package render

import "strings"

// buildProfileCleanupScript 生成按专用 profile 清理浏览器进程的 PowerShell 脚本。
// 纯函数（不执行、跨平台可测）：
//   - 明确排除当前 PowerShell 自身（$_.ProcessId -ne $PID）：脚本命令行本身包含 profile
//     路径字面量，不过滤会把 PowerShell 自己匹配进去并自杀；
//   - 只匹配命令行含该专用 profile 路径的进程（--user-data-dir），不触碰用户日常浏览器；
//   - profile 按 PowerShell 单引号字面量转义（单引号翻倍）；
//   - 空白 profile 返回空串（调用方跳过，避免匹配到一切进程）。
func buildProfileCleanupScript(profileDir string) string {
	profileDir = strings.TrimSpace(profileDir)
	if profileDir == "" {
		return ""
	}
	literal := strings.ReplaceAll(profileDir, "'", "''")
	return "Get-CimInstance Win32_Process | Where-Object { $_.ProcessId -ne $PID -and " +
		"$_.CommandLine -and $_.CommandLine.Contains('" + literal + "') } | " +
		"ForEach-Object { Stop-Process -Id $_.ProcessId -Force -ErrorAction SilentlyContinue }"
}
