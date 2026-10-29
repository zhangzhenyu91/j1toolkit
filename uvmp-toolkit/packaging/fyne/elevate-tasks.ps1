# 提权重试包装（由 fyne-installer.nsi 经 nsExec 无窗调用，安装/卸载共用）：
# 旧计划任务若由管理员权限创建（高完整性标签），中完整性的替换/删除会被 Windows 拒绝；
# 安装器此时经本脚本走 UAC 提升后再跑同目录 register-tasks.cmd。
#
# 无窗要点：
#   - 本脚本自身由 nsExec 调 powershell.exe -WindowStyle Hidden（无控制台窗口）；
#   - 提升后的 cmd 控制台以 Start-Process -WindowStyle Hidden 隐藏；
#   - UAC 确认框是唯一可见界面（系统安全边界，不能也不应绕过），用户取消即失败。
param(
  [ValidateSet("install", "remove")]
  [string]$Mode = "install"
)
$ErrorActionPreference = "Stop"

$cmd = Join-Path $PSScriptRoot "register-tasks.cmd"
if (-not (Test-Path $cmd)) {
  Write-Host "提权重试失败: 缺少 register-tasks.cmd（$cmd）"
  exit 2
}

try {
  $p = Start-Process -FilePath $env:ComSpec `
    -ArgumentList @("/c", "call `"$cmd`" $Mode") `
    -Verb RunAs -Wait -PassThru -WindowStyle Hidden
} catch {
  # 用户取消 UAC / 提权被策略拒绝：返回非 0，安装器终验会明确中止
  Write-Host "提权重试未完成（UAC 取消或策略拒绝）: $($_.Exception.Message)"
  exit 3
}
if ($null -eq $p) { exit 4 }
exit $p.ExitCode
