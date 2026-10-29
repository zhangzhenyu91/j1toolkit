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
#      register-tasks.cmd / run-uvmp-gui.vbs / elevate-tasks.ps1 / icon.ico / models\ /
#      native\onnxruntime.dll / 启动器 cmd / 卸载器；并断言不含 browser-runtime（Windows 包不随带
#      Electron 运行时，CDP 走显式宿主）
#   5. 快捷方式与卸载项：快捷方式目标 wscript.exe + run-uvmp-gui.vbs 参数（隐藏控制台）、
#      图标引用安装目录 icon.ico；卸载项 DisplayIcon/UninstallString/QuietUninstallString 正确引用
#   6. OS 计划任务接管校验：5 个 UVMP-Toolkit-* 任务全部注册且指向 <安装目录>\run-uvmp.vbs
#      隐藏窗口调 uvmp-core.exe 同名参数（Daily/TreeTick/TreeNightly/CrossingTick/CrossingNightly），
#      预置的假旧任务被替换（升级替换不双跑）
#   7. 静默卸载后：快捷方式删除、目录清理干净、5 个计划任务全部删除（不恢复旧任务定义）、
#      开机自启残值（HKCU Run 值 uvmp-toolkit）删除，备份任务随后恢复
# 注意：冒烟会真实安装/卸载一次（含临时覆盖同名快捷方式与 HKCU Run 值），开发机跑前先关掉客户端。
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
# 开机自启值（HKCU Run，值名与 autostart.go 一致）冒烟会覆盖成演练值，先备份、结束恢复
$runKey = "HKCU:\Software\Microsoft\Windows\CurrentVersion\Run"
$runBackup = (Get-ItemProperty -Path $runKey -Name "uvmp-toolkit" -ErrorAction SilentlyContinue)."uvmp-toolkit"
Write-Host "== 计划任务备份 + 假旧任务预置通过 =="

# ---------- 3. 静默安装（/S /D=<dir>；/D 必须是最后一个参数且不带引号） ----------
try {
  $instDir = Join-Path $env:TEMP ("uvmp-fyne-smoke-" + [guid]::NewGuid().ToString("N"))
  Write-Host "== 静默安装 -> $instDir =="
  $p = Start-Process -FilePath $InstallerFull -ArgumentList "/S", "/D=$instDir" -PassThru -Wait
  if ($p.ExitCode -ne 0) { Fail "静默安装失败（exit=$($p.ExitCode)）" }

  # ---------- 4. 安装布局校验（不含 browser-runtime） ----------
  foreach ($f in @("uvmp-toolkit.exe", "uvmp-core.exe", "uvmp-export.exe", "run-uvmp.vbs",
                   "register-tasks.cmd", "run-uvmp-gui.vbs", "elevate-tasks.ps1", "icon.ico",
                   "models/det.onnx", "models/rec.onnx",
                   "native/onnxruntime.dll", "uvmp-toolkit-fyne.cmd",
                   "Uninstall-uvmp-toolkit-fyne.exe")) {
    if (-not (Test-Path (Join-Path $instDir $f))) { Fail "安装布局缺: $f" }
  }
  if (Test-Path (Join-Path $instDir "browser-runtime")) {
    Fail "安装布局含 browser-runtime（Windows 包不再随带 Electron 运行时，CDP 走显式宿主）"
  }
  # 品牌图标：多尺寸 ico（16/24/32/48/64/128/256 共 7 条），快捷方式与卸载项都引用它
  $icoPath = Join-Path $instDir "icon.ico"
  $icoBytes = [System.IO.File]::ReadAllBytes($icoPath)
  $icoCount = [int]$icoBytes[4] + ([int]$icoBytes[5] -shl 8)
  if ($icoCount -lt 7) { Fail "icon.ico 尺寸条目不足（$icoCount < 7：16/24/32/48/64/128/256）" }
  Write-Host "== 安装布局校验通过（含品牌 icon.ico：$icoCount 个尺寸） =="

  # ---------- 5. 快捷方式与卸载项（wscript 隐藏启动器 + 品牌图标 + 引用正确） ----------
  $unKey = "HKCU:\Software\Microsoft\Windows\CurrentVersion\Uninstall\UvmpToolkitFyne"
  $un = Get-ItemProperty -Path $unKey -ErrorAction SilentlyContinue
  if ($null -eq $un) { Fail "缺卸载项注册表键: $unKey" }
  if ($un.DisplayIcon -notmatch 'icon\.ico') { Fail "卸载项 DisplayIcon 未引用 icon.ico: $($un.DisplayIcon)" }
  $unExe = Join-Path $instDir "Uninstall-uvmp-toolkit-fyne.exe"
  if ($un.UninstallString -notmatch [regex]::Escape($unExe)) { Fail "卸载项 UninstallString 未引用安装目录卸载器: $($un.UninstallString)" }
  if ($un.QuietUninstallString -notmatch '/S') { Fail "卸载项 QuietUninstallString 缺 /S: $($un.QuietUninstallString)" }

  $wsh = New-Object -ComObject WScript.Shell
  $smLnk = Join-Path ([Environment]::GetFolderPath("Programs")) "$($un.DisplayName).lnk"
  $deskLnk = Join-Path ([Environment]::GetFolderPath("Desktop")) "$($un.DisplayName).lnk"
  foreach ($lnk in @($smLnk, $deskLnk)) {
    if (-not (Test-Path $lnk)) { Fail "快捷方式缺失: $lnk" }
    $sc = $wsh.CreateShortcut($lnk)
    if ($sc.TargetPath -notmatch 'wscript\.exe$') { Fail "快捷方式未指向 wscript.exe（控制台子系统 exe 直启会闪黑窗）: $($sc.TargetPath)" }
    if ($sc.Arguments -notmatch 'run-uvmp-gui\.vbs') { Fail "快捷方式未指向隐藏启动器 run-uvmp-gui.vbs: $($sc.Arguments)" }
    if ($sc.IconLocation -notmatch [regex]::Escape("$instDir\icon.ico")) { Fail "快捷方式图标未引用安装目录 icon.ico: $($sc.IconLocation)" }
  }
  Write-Host "== 快捷方式与卸载项校验通过（wscript 隐藏启动器 + 品牌图标） =="

  # 自启残值清理演练：模拟应用启用过开机自启（HKCU Run，值名与 autostart.go 一致），卸载必须删除
  # （$runKey 与备份在上方计划任务备份段取得，finally 里恢复）
  New-ItemProperty -Path $runKey -Name "uvmp-toolkit" `
    -Value "wscript.exe `"$instDir\run-uvmp-gui.vbs`"" -PropertyType String -Force | Out-Null

  # ---------- 6. OS 计划任务接管校验（5 任务注册 + 同名替换 + 同名参数） ----------
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

  # ---------- 7. 静默卸载 ----------
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
  # 卸载删除快捷方式、开机自启残值与卸载项注册表键
  foreach ($lnk in @($smLnk, $deskLnk)) {
    if (Test-Path $lnk) { Fail "卸载后残留快捷方式: $lnk" }
  }
  if ($null -ne (Get-ItemProperty -Path $runKey -Name "uvmp-toolkit" -ErrorAction SilentlyContinue)) {
    Fail "卸载后残留开机自启项（HKCU Run: uvmp-toolkit）"
  }
  if (Test-Path $unKey) { Fail "卸载后残留卸载项注册表键: $unKey" }
  Write-Host "== 卸载清理校验通过（目录 + 5 计划任务 + 快捷方式 + 自启残值 + 卸载项） =="
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
  # 恢复冒烟前的开机自启值（原本没有则删除演练残留；早期失败时 runKey 可能还没赋值）
  if ($runKey -and (Test-Path $runKey)) {
    if ($null -ne $runBackup) {
      New-ItemProperty -Path $runKey -Name "uvmp-toolkit" -Value $runBackup -PropertyType String -Force | Out-Null
    } else {
      Remove-ItemProperty -Path $runKey -Name "uvmp-toolkit" -ErrorAction SilentlyContinue
    }
  }
}

Write-Host "Fyne 轨安装冒烟全部通过: $InstallerFull"
