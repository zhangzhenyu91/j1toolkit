# 安装器计划任务终验：独立脚本避免 NSIS、cmd 与 PowerShell 三层引号转义。
param(
  [Parameter(Mandatory = $true)][string]$Name,
  [Parameter(Mandatory = $true)][string]$Launcher
)

$xml = & schtasks.exe /query /tn $Name /xml
$queryExit = $LASTEXITCODE
if ($queryExit -ne 0) {
  Write-Output "任务查询失败: $Name (exit=$queryExit)"
  exit 1
}
$text = $xml -join "`n"
if ($text.IndexOf("run-uvmp.vbs", [System.StringComparison]::OrdinalIgnoreCase) -lt 0 -or
    $text.IndexOf($Launcher, [System.StringComparison]::OrdinalIgnoreCase) -lt 0) {
  Write-Output "任务未指向本安装: $Name；期望启动器: $Launcher"
  exit 1
}
exit 0
