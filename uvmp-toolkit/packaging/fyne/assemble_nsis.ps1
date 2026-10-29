# Fyne 轨 NSIS 装配（Windows 主机）：dist-fyne/windows-amd64/ 装配布局 →
# uvmp-toolkit-fyne-Setup-<version>.exe（NSIS）+ uvmp-toolkit-fyne-<version>-win-portable.zip
#
# 用法（在仓库根或任意位置，脚本自定位到 uvmp-toolkit/）：
#   powershell -ExecutionPolicy Bypass -File packaging/fyne/assemble_nsis.ps1 `
#       [-StagingDir dist-fyne\windows-amd64] [-Version <ver>]
#
# 环境：
#   UVMP_FYNE_VERSION  版本号（缺省 uvmp-toolkit/version.json 的 version；与发版 tag 对齐由 CI 传入）
#   UVMP_NSIS_MAKENSIS makensis 路径（缺省自动探测 PATH / Program Files (x86)\NSIS）
#
# 产物命名（-fyne- 中缀避免覆盖 Electron 轨产物；保留 .exe 后缀与 CNB 取件口径一致）：
#   uvmp-toolkit-fyne-Setup-<version>.exe   （NSIS 安装程序；/S 静默、/D=<dir> 指定目录）
#   uvmp-toolkit-fyne-<version>-win-portable.zip（绿色包，解压即用）
#
# 纪律：
#   - 装配布局缺件即失败（uvmp-toolkit.exe / uvmp-core.exe / uvmp-export.exe / browser-runtime\ / models\ / native\）
#   - NSIS 用 Unicode 3 + UTF-8 BOM 脚本（fyne-installer.nsi；ANSI 中文乱码为 v1.x 踩坑史）
#   - icon.ico 由 packaging/icon.png 经 System.Drawing 生成（失败降级用默认图标，不阻断）
param(
  [string]$StagingDir = "",
  [string]$Version = ""
)
$ErrorActionPreference = "Stop"
Set-Location (Join-Path $PSScriptRoot "..\..")
$NSI = "packaging/fyne/fyne-installer.nsi"

function Fail([string]$msg) { Write-Host "错误: $msg" -ForegroundColor Red; exit 1 }

if ($StagingDir -eq "") { $StagingDir = "dist-fyne/windows-amd64" }
$StagingFull = [System.IO.Path]::GetFullPath($StagingDir)
if ($Version -eq "") { $Version = $env:UVMP_FYNE_VERSION }
if (-not $Version) {
  # 版本与 desktop/package.json 解耦：读 uvmp-toolkit/version.json（装配版本单一来源）
  $verJson = Get-Content "version.json" -Raw -Encoding UTF8 | ConvertFrom-Json
  $Version = $verJson.version
}
if (-not $Version) { Fail "无法确定版本号：设 UVMP_FYNE_VERSION 或确保 uvmp-toolkit/version.json 可读" }
Write-Host "装配版本: $Version（windows-amd64）"

# ---------- 装配布局齐全性（缺失即失败，不出残缺包） ----------
foreach ($f in @("uvmp-toolkit.exe", "uvmp-core.exe", "uvmp-export.exe")) {
  if (-not (Test-Path (Join-Path $StagingFull $f))) { Fail "装配布局缺 $f（先跑 packaging/fyne/build_fyne_core.ps1）" }
}
if (-not (Test-Path (Join-Path $StagingFull "browser-runtime/electron.exe"))) {
  Fail "装配布局缺 browser-runtime\electron.exe（先跑 packaging/fyne/fetch_browser_runtime.ps1）"
}
if (-not (Test-Path (Join-Path $StagingFull "browser-runtime/app/main.cjs")) -or
    -not (Test-Path (Join-Path $StagingFull "browser-runtime/app/package.json"))) {
  Fail "装配布局缺 browser-runtime\app\{package.json,main.cjs}（独立浏览器运行时应用）"
}
if (-not (Test-Path (Join-Path $StagingFull "models/det.onnx")) -or -not (Test-Path (Join-Path $StagingFull "models/rec.onnx"))) {
  Fail "装配布局缺 models（先跑 packaging/fyne/assemble_onnx.ps1）"
}
if (-not (Get-ChildItem (Join-Path $StagingFull "native") -Filter "onnxruntime.dll" -ErrorAction SilentlyContinue)) {
  Fail "装配布局缺 native\onnxruntime.dll（先跑 packaging/fyne/assemble_onnx.ps1）"
}

# ---------- 产物名（-fyne- 中缀 + CNB 取件后缀 .exe/.zip） ----------
$OutName = "uvmp-toolkit-fyne-Setup-$Version.exe"
$OutFile = [System.IO.Path]::GetFullPath((Join-Path "dist-fyne/windows-amd64" $OutName))
$ZipName = "uvmp-toolkit-fyne-$Version-win-portable.zip"
$ZipFile = [System.IO.Path]::GetFullPath((Join-Path "dist-fyne/windows-amd64" $ZipName))

# ---------- icon.ico（icon.png → ico；失败降级默认图标，不阻断） ----------
$IcoFile = Join-Path $StagingFull "icon.ico"
$IcoParam = ""
try {
  Add-Type -AssemblyName System.Drawing
  $png = [System.Drawing.Image]::FromFile([System.IO.Path]::GetFullPath("packaging/icon.png"))
  try {
    $bmp = New-Object System.Drawing.Bitmap $png, 32, 32
    $hIcon = $bmp.GetHicon()
    $icon = [System.Drawing.Icon]::FromHandle($hIcon)
    $fs = [System.IO.File]::Create($IcoFile)
    try { $icon.Save($fs) } finally { $fs.Dispose() }
    $bmp.Dispose()
    $IcoParam = "/DICONFILE=$IcoFile"
    Write-Host "icon.ico 生成: $IcoFile"
  } finally { $png.Dispose() }
} catch {
  Write-Host "提示: icon.ico 生成失败（$($_.Exception.Message)），使用 NSIS 默认图标"
  $IcoParam = ""
}

# ---------- makensis（先组 payload 目录：排除装配产物本身，防止重跑把安装包打进去） ----------
$payload = Join-Path ([System.IO.Path]::GetTempPath()) ("uvmp-fyne-payload-" + [guid]::NewGuid().ToString("N"))
New-Item -ItemType Directory -Force $payload | Out-Null
foreach ($item in @("uvmp-toolkit.exe", "uvmp-core.exe", "uvmp-export.exe", "browser-runtime", "models", "native")) {
  $src = Join-Path $StagingFull $item
  if (-not (Test-Path $src)) { Fail "装配布局缺 $item" }
  Copy-Item -Recurse -Force $src (Join-Path $payload $item)
}
if (Test-Path $IcoFile) { Copy-Item -Force $IcoFile (Join-Path $payload "icon.ico") }

# ---------- OS 计划任务脚本齐全性（run-uvmp.vbs / register-tasks.cmd 入 payload，NSIS 全量 File /r 打进安装包） ----------
Copy-Item -Force "packaging/fyne/run-uvmp.vbs" (Join-Path $payload "run-uvmp.vbs")
Copy-Item -Force "packaging/fyne/register-tasks.cmd" (Join-Path $payload "register-tasks.cmd")
foreach ($f in @("run-uvmp.vbs", "register-tasks.cmd", "uvmp-core.exe")) {
  if (-not (Test-Path (Join-Path $payload $f))) { Fail "payload 缺计划任务脚本: $f" }
}
# 静态断言：NSI 对 5 个任务全部检测/注册终验/卸载终验，且经 run-uvmp.vbs + register-tasks.cmd
$nsiText = Get-Content $NSI -Raw -Encoding UTF8
foreach ($t in @("UVMP-Toolkit-DailyExport", "UVMP-Toolkit-TreeSyncTick", "UVMP-Toolkit-TreeSyncNightly",
                 "UVMP-Toolkit-CrossingSyncTick", "UVMP-Toolkit-CrossingSyncNightly")) {
  if ($nsiText -notmatch [regex]::Escape("DetectSchedTask `"$t`"")) { Fail "NSI 缺计划任务检测: $t" }
  if ($nsiText -notmatch [regex]::Escape("VerifySchedTask `"$t`"")) { Fail "NSI 缺计划任务注册终验: $t" }
  if ($nsiText -notmatch [regex]::Escape("VerifySchedTaskGone `"$t`"")) { Fail "NSI 缺计划任务卸载终验: $t" }
}
if ($nsiText -notmatch "RunSchedTasks install") { Fail "NSI 缺计划任务注册调用（RunSchedTasks install）" }
if ($nsiText -notmatch "RunSchedTasks remove") { Fail "NSI 缺计划任务卸载调用（RunSchedTasks remove）" }
if ($nsiText -notmatch "run-uvmp\.vbs") { Fail "NSI 未引用 run-uvmp.vbs（隐藏窗口调用脚本）" }
if ($nsiText -notmatch "register-tasks\.cmd") { Fail "NSI 未引用 register-tasks.cmd（任务注册/清理脚本）" }
Write-Host "== NSIS 计划任务脚本齐全性校验通过（5 任务检测/注册/卸载 + run-uvmp.vbs + register-tasks.cmd）=="

$Makensis = $env:UVMP_NSIS_MAKENSIS
if (-not $Makensis) {
  $cmd = Get-Command makensis -ErrorAction SilentlyContinue
  if ($cmd) { $Makensis = $cmd.Source }
}
if (-not $Makensis) {
  foreach ($cand in @("${env:ProgramFiles(x86)}\NSIS\makensis.exe", "$env:ProgramFiles\NSIS\makensis.exe")) {
    if ($cand -and (Test-Path $cand)) { $Makensis = $cand; break }
  }
}
if (-not $Makensis) { Fail "未找到 makensis（NSIS 3 Unicode）：安装 NSIS 或设 UVMP_NSIS_MAKENSIS" }
Write-Host "makensis: $Makensis"

$nsiArgs = @("/DSTAGINGDIR=$payload", "/DOUTFILE=$OutFile", "/DVERSION=$Version")
if ($IcoParam) { $nsiArgs += $IcoParam }
$nsiArgs += [System.IO.Path]::GetFullPath($NSI)
try {
  & $Makensis @nsiArgs
  if ($LASTEXITCODE -ne 0) { Fail "makensis 失败（exit=$LASTEXITCODE）" }
  if (-not (Test-Path $OutFile)) { Fail "NSIS 产物缺失: $OutFile" }

  # ---------- 绿色包（payload + 启动器 cmd，解压即用） ----------
  $stageZip = Join-Path ([System.IO.Path]::GetTempPath()) ("uvmp-fyne-zip-" + [guid]::NewGuid().ToString("N"))
  $inner = Join-Path $stageZip "uvmp-toolkit-fyne"
  New-Item -ItemType Directory -Force $inner | Out-Null
  Copy-Item -Recurse -Force (Join-Path $payload "*") $inner
  $launcher = "@echo off`r`nsetlocal`r`nset `"PATH=%~dp0native;%PATH%`"`r`n`"%~dp0uvmp-toolkit.exe`" %*`r`n"
  Set-Content -Path (Join-Path $inner "uvmp-toolkit-fyne.cmd") -Value $launcher -Encoding ASCII
  if (Test-Path $ZipFile) { Remove-Item -Force $ZipFile }
  Compress-Archive -Path $inner -DestinationPath $ZipFile
  Remove-Item -Recurse -Force $stageZip -ErrorAction SilentlyContinue
} finally {
  Remove-Item -Recurse -Force $payload -ErrorAction SilentlyContinue
}

Write-Host "NSIS 装配完成:"
Write-Host "  $OutFile"
Write-Host "  $ZipFile"
