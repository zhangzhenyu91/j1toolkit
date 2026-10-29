# Fyne 轨安装冒烟（Windows 主机）：NSIS 静默实装 → 布局校验 → 静默卸载清理校验
#
# 用法（在仓库根或任意位置，脚本自定位到 uvmp-toolkit/）：
#   powershell -ExecutionPolicy Bypass -File packaging/fyne/install_smoke.ps1 `
#       [-Installer dist-fyne\windows-amd64\uvmp-toolkit-fyne-Setup-<ver>.exe]
#
# 门禁内容：
#   1. 安装程序存在且为 PE（NSIS 出包校验）
#   2. 计划任务备份（机器上已有同名任务先导出 XML，冒烟结束恢复）+ 预置假旧任务验证同名替换接管
#   3. /S /D=<临时目录> 静默安装成功（退出码 0）
#   4. 安装布局校验：uvmp-toolkit.exe / uvmp-core.exe / uvmp-export.exe / run-uvmp.vbs /
#      register-tasks.cmd / browser-runtime\electron.exe / browser-runtime\app\main.cjs / models\ /
#      native\onnxruntime.dll / 启动器 cmd / 卸载器
#   5. OS 计划任务接管校验：5 个 UVMP-Toolkit-* 任务全部注册且指向 <安装目录>\run-uvmp.vbs
#      隐藏窗口调 uvmp-core.exe 同名参数（Daily/TreeTick/TreeNightly/CrossingTick/CrossingNightly），
#      预置的假旧任务被替换（升级替换不双跑）
#   6. 卸载器 /S 静默卸载后目录清理干净、5 个计划任务全部删除（不恢复旧任务定义），备份任务随后恢复
param(
  [string]$Installer = ""
)
$ErrorActionPreference = "Stop"
Set-Location (Join-Path $PSScriptRoot "..\..")

function Fail([string]$msg) { Write-Host "安装冒烟失败: $msg" -ForegroundColor Red; exit 1 }

if ($Installer -eq "") {
  $hit = Get-ChildItem "dist-fyne/windows-amd64" -Filter "uvmp-toolkit-fyne-Setup-*.exe" -ErrorAction SilentlyContinue |
    Select-Object -First 1
  if ($null -eq $hit) { Fail "未找到安装程序（先跑 packaging/fyne/assemble_nsis.ps1）" }
  $Installer = $hit.FullName
}
$InstallerFull = [System.IO.Path]::GetFullPath($Installer)
if (-not (Test-Path $InstallerFull)) { Fail "安装程序不存在: $InstallerFull" }

# ---------- 1. PE 头校验 ----------
$fs = [System.IO.File]::OpenRead($InstallerFull)
try {
  $b0 = $fs.ReadByte(); $b1 = $fs.ReadByte()
} finally { $fs.Dispose() }
if ($b0 -ne 0x4D -or $b1 -ne 0x5A) { Fail "安装程序非 PE（MZ 头缺失）: $InstallerFull" }
Write-Host "== 安装程序 PE 校验通过 =="

# ---------- 2. 计划任务备份 + 预置假旧任务（验证同名替换接管） ----------
# 5 个计划任务与旧版/源码模式/Electron 轨同名（升级替换语义，见 fyne-installer.nsi）
$SchedTasks = @(
  @{ Name = "UVMP-Toolkit-DailyExport";         Args = "daily-export --if-due" },
  @{ Name = "UVMP-Toolkit-TreeSyncTick";        Args = "tree-sync --tick" },
  @{ Name = "UVMP-Toolkit-TreeSyncNightly";     Args = "tree-sync --nightly --if-due" },
  @{ Name = "UVMP-Toolkit-CrossingSyncTick";    Args = "crossing-sync --tick" },
  @{ Name = "UVMP-Toolkit-CrossingSyncNightly"; Args = "crossing-sync --nightly --if-due" }
)
# schtasks 经 cmd 调用并吞掉 stderr：$ErrorActionPreference=Stop 下原生命令 stderr 会触发终止异常（PS5.1 坑）
function Invoke-Schtasks([string]$argLine) {
  cmd /c "schtasks $argLine >nul 2>&1"
  return $LASTEXITCODE
}
function Get-TaskXml([string]$Name) {
  $out = cmd /c "schtasks /query /tn `"$Name`" /xml 2>nul"
  if ($LASTEXITCODE -ne 0) { return $null }
  return ($out -join "`n")
}
$taskBackupDir = Join-Path $env:TEMP ("uvmp-fyne-tasks-" + [guid]::NewGuid().ToString("N"))
New-Item -ItemType Directory -Force $taskBackupDir | Out-Null
foreach ($t in $SchedTasks) {
  # 机器上已有同名任务（用户正式任务）先导出 XML 备份，冒烟结束恢复
  $xml = Get-TaskXml $t.Name
  if ($null -ne $xml) { Set-Content -Path (Join-Path $taskBackupDir "$($t.Name).xml") -Value $xml -Encoding utf8 }
  # 预置假「旧安装」任务（指向 cmd 占位）：安装器应检测并同名替换，不双跑
  if ((Invoke-Schtasks "/create /tn `"$($t.Name)`" /f /sc minute /mo 1 /tr `"cmd.exe /c exit 0`"") -ne 0) {
    Fail "预置假旧任务失败: $($t.Name)"
  }
}
Write-Host "== 计划任务备份 + 假旧任务预置通过 =="

# ---------- 3. 静默安装（/S /D=<dir>；/D 必须是最后一个参数且不带引号） ----------
try {
  $instDir = Join-Path $env:TEMP ("uvmp-fyne-smoke-" + [guid]::NewGuid().ToString("N"))
  Write-Host "== 静默安装 -> $instDir =="
  $p = Start-Process -FilePath $InstallerFull -ArgumentList "/S", "/D=$instDir" -PassThru -Wait
  if ($p.ExitCode -ne 0) { Fail "静默安装失败（exit=$($p.ExitCode)）" }

  # ---------- 4. 安装布局校验 ----------
  foreach ($f in @("uvmp-toolkit.exe", "uvmp-core.exe", "uvmp-export.exe", "run-uvmp.vbs",
                   "register-tasks.cmd", "browser-runtime/electron.exe",
                   "browser-runtime/app/main.cjs",
                   "browser-runtime/app/package.json",
                   "models/det.onnx", "models/rec.onnx",
                   "native/onnxruntime.dll", "uvmp-toolkit-fyne.cmd",
                   "Uninstall-uvmp-toolkit-fyne.exe")) {
    if (-not (Test-Path (Join-Path $instDir $f))) { Fail "安装布局缺: $f" }
  }
  Write-Host "== 安装布局校验通过 =="

  # ---------- 5. OS 计划任务接管校验（5 任务注册 + 同名替换 + 同名参数） ----------
  foreach ($t in $SchedTasks) {
    $xml = Get-TaskXml $t.Name
    if ($null -eq $xml) { Fail "计划任务缺失: $($t.Name)" }
    if ($xml -notmatch "wscript") { Fail "$($t.Name) 未经 wscript 隐藏窗口启动" }
    if ($xml -notmatch [regex]::Escape("run-uvmp.vbs")) { Fail "$($t.Name) 未指向 run-uvmp.vbs（未被本安装接管）" }
    if ($xml -notmatch [regex]::Escape($instDir)) { Fail "$($t.Name) 未指向安装目录: $instDir" }
    if ($xml -notmatch [regex]::Escape($t.Args)) { Fail "$($t.Name) 参数不符（应含: $($t.Args)）" }
    if ($xml -match [regex]::Escape("exit 0")) { Fail "$($t.Name) 假旧任务残留（未被同名替换）" }
  }
  Write-Host "== OS 计划任务接管校验通过（5 任务注册/同名替换/同名参数） =="

  # ---------- 6. 静默卸载 ----------
  $uninst = Join-Path $instDir "Uninstall-uvmp-toolkit-fyne.exe"
  Write-Host "== 静默卸载 =="
  $p = Start-Process -FilePath $uninst -ArgumentList "/S" -PassThru -Wait -WorkingDirectory $instDir
  if ($p.ExitCode -ne 0) { Fail "静默卸载失败（exit=$($p.ExitCode)）" }
  # 卸载器自删留下的残骸最多一层目录；文件树必须清空
  $left = Get-ChildItem $instDir -Recurse -ErrorAction SilentlyContinue |
    Where-Object { -not $_.PSIsContainer -and $_.Name -ne "Uninstall-uvmp-toolkit-fyne.exe" }
  if ($left) {
    $left | ForEach-Object { Write-Host "  残留: $($_.FullName)" }
    Fail "卸载后残留 $($left.Count) 个文件"
  }
  Remove-Item -Recurse -Force $instDir -ErrorAction SilentlyContinue
  # 卸载删除全部 5 个计划任务（不恢复旧任务定义）
  foreach ($t in $SchedTasks) {
    if ($null -ne (Get-TaskXml $t.Name)) { Fail "卸载后残留计划任务: $($t.Name)" }
  }
  Write-Host "== 卸载清理校验通过（目录 + 5 计划任务） =="
} finally {
  # 恢复冒烟前已有同名任务（用户正式任务不因冒烟丢失）；原本不存在的任务已被卸载删除，无需恢复
  foreach ($t in $SchedTasks) {
    $xmlPath = Join-Path $taskBackupDir "$($t.Name).xml"
    if (Test-Path $xmlPath) {
      if ((Invoke-Schtasks "/create /tn `"$($t.Name)`" /xml `"$xmlPath`" /f") -ne 0) {
        Write-Host "  警告: 恢复计划任务失败: $($t.Name)" -ForegroundColor Yellow
      }
    }
  }
  Remove-Item -Recurse -Force $taskBackupDir -ErrorAction SilentlyContinue
}

Write-Host "Fyne 轨安装冒烟全部通过: $InstallerFull"
