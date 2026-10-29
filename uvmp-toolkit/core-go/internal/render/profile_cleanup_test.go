package render

import (
	"strings"
	"testing"
)

// TestBuildProfileCleanupScript 兜底清理脚本：排除 PowerShell 自身（$PID）避免自杀，
// profile 作为单引号字面量转义，空 profile 返回空串。
func TestBuildProfileCleanupScript(t *testing.T) {
	profile := `C:\Users\tester\AppData\Local\Temp\uvmp\worker-0\.chrome-official-tmp`
	script := buildProfileCleanupScript(profile)
	for _, want := range []string{
		"Get-CimInstance Win32_Process",
		"$_.ProcessId -ne $PID",
		"$_.CommandLine.Contains('" + profile + "')",
		"Stop-Process -Id $_.ProcessId -Force",
	} {
		if !strings.Contains(script, want) {
			t.Fatalf("脚本缺少 %q:\n%s", want, script)
		}
	}

	if got := buildProfileCleanupScript("   "); got != "" {
		t.Fatalf("空 profile 应返回空脚本，got %q", got)
	}

	quoted := buildProfileCleanupScript(`C:\Users\O'Brien\profile`)
	if !strings.Contains(quoted, `O''Brien`) {
		t.Fatalf("单引号未按 PowerShell 字面量转义:\n%s", quoted)
	}
}
