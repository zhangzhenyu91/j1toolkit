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
#   - 装配布局缺件即失败（uvmp-toolkit.exe / uvmp-core.exe / uvmp-export.exe / models\ / native\）；
#     **browser-runtime 不是 Windows 包的必备项也不再入包**（CDP 走显式宿主，见 gate_smoke.ps1），
#     本脚本显式剔除，避免重跑时把构建机上残留的 Electron 运行时打进安装包
#   - 图标只认真正 Shade 图为源（packaging/icon.png，由 desktop/src/assets/icon.png 重编码而来）：
#     装配时再清一次元数据（iCCP 等）并生成多尺寸 icon.ico；任何一步失败即中止装配，
#     绝不静默回退 NSIS 默认图标（安装包/快捷方式/卸载项都必须显示品牌图标）
#   - NSIS 用 Unicode 3 + UTF-8 BOM 脚本（fyne-installer.nsi；ANSI 中文乱码为 v1.x 踩坑史），
#     本脚本先校验 BOM 再调 makensis
param(
  [string]$StagingDir = "",
  [string]$Version = ""
)
$ErrorActionPreference = "Stop"
Set-Location (Join-Path $PSScriptRoot "..\..")
$NSI = "packaging/fyne/fyne-installer.nsi"
$IconSrc = "packaging/icon.png"

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

# ---------- 装配布局齐全性（缺失即失败，不出残缺包；browser-runtime 不再要求） ----------
foreach ($f in @("uvmp-toolkit.exe", "uvmp-core.exe", "uvmp-export.exe")) {
  if (-not (Test-Path (Join-Path $StagingFull $f))) { Fail "装配布局缺 $f（先跑 packaging/fyne/build_fyne_core.ps1）" }
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

# ---------- 图标工具（PNG 元数据检查 / 重编码 / 多尺寸 ICO；失败一律 Fail，不降级） ----------
Add-Type -AssemblyName System.Drawing

# Get-PngChunks 逐块解析 PNG，返回块类型序列（用于断言无 iCCP 等色彩管理元数据）
function Get-PngChunks([string]$Path) {
  $b = [System.IO.File]::ReadAllBytes($Path)
  $sig = @(0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A)
  if ($b.Length -lt 8) { throw "PNG 过短: $Path" }
  for ($i = 0; $i -lt 8; $i++) { if ($b[$i] -ne $sig[$i]) { throw "不是 PNG 文件: $Path" } }
  $chunks = @()
  $pos = 8
  while ($pos + 8 -le $b.Length) {
    $len = ([int]$b[$pos] -shl 24) -bor ([int]$b[$pos + 1] -shl 16) -bor ([int]$b[$pos + 2] -shl 8) -bor [int]$b[$pos + 3]
    if ($len -lt 0) { throw "PNG 块长度非法: $Path" }
    $type = [System.Text.Encoding]::ASCII.GetString($b, $pos + 4, 4)
    $chunks += $type
    if ($type -eq "IEND") { break }
    $pos += 12 + $len
  }
  return $chunks
}

# Save-CleanPng 用 GDI+ 重编码为 32bpp ARGB PNG：丢弃 iCCP/文本等辅助块（Windows Shell 与
# NSIS 图标对带色彩管理块的 PNG/ICO 兼容性差，源图来源不可控，一律净化后再用）
function Save-CleanPng([string]$Source, [string]$Out) {
  $img = [System.Drawing.Image]::FromFile($Source)
  try {
    if ($img.Width -lt 256 -or $img.Height -lt 256) { throw "源图过小（需 ≥256x256）: $Source" }
    $bmp = [System.Drawing.Bitmap]::new($img.Width, $img.Height, [System.Drawing.Imaging.PixelFormat]::Format32bppArgb)
    try {
      $g = [System.Drawing.Graphics]::FromImage($bmp)
      try {
        $g.Clear([System.Drawing.Color]::Transparent)
        $g.CompositingMode = [System.Drawing.Drawing2D.CompositingMode]::SourceCopy
        $g.CompositingQuality = [System.Drawing.Drawing2D.CompositingQuality]::HighQuality
        $g.InterpolationMode = [System.Drawing.Drawing2D.InterpolationMode]::HighQualityBicubic
        $g.PixelOffsetMode = [System.Drawing.Drawing2D.PixelOffsetMode]::HighQuality
        $g.SmoothingMode = [System.Drawing.Drawing2D.SmoothingMode]::HighQuality
        $g.DrawImage($img, [System.Drawing.Rectangle]::new(0, 0, $img.Width, $img.Height))
      } finally { $g.Dispose() }
      $bmp.Save($Out, [System.Drawing.Imaging.ImageFormat]::Png)
    } finally { $bmp.Dispose() }
  } finally { $img.Dispose() }
}

# ConvertTo-IconDibEntry 把位图编码为 ICO 的 32bpp BMP 条目（DIB 头 + 自下而上 BGRA + 全 0 AND 掩码）
function ConvertTo-IconDibEntry([System.Drawing.Bitmap]$Bitmap) {
  $w = $Bitmap.Width; $h = $Bitmap.Height
  $rect = [System.Drawing.Rectangle]::new(0, 0, $w, $h)
  $data = $Bitmap.LockBits($rect, [System.Drawing.Imaging.ImageLockMode]::ReadOnly, [System.Drawing.Imaging.PixelFormat]::Format32bppArgb)
  try {
    $stride = $data.Stride
    $raw = New-Object byte[] ($stride * $h)
    [System.Runtime.InteropServices.Marshal]::Copy($data.Scan0, $raw, 0, $raw.Length)
  } finally { $Bitmap.UnlockBits($data) }

  $ms = [System.IO.MemoryStream]::new()
  $bw = [System.IO.BinaryWriter]::new($ms)
  try {
    $bw.Write([uint32]40); $bw.Write([int32]$w); $bw.Write([int32]($h * 2))
    $bw.Write([uint16]1); $bw.Write([uint16]32); $bw.Write([uint32]0)
    $bw.Write([uint32]($w * $h * 4)); $bw.Write([int32]0); $bw.Write([int32]0)
    $bw.Write([uint32]0); $bw.Write([uint32]0)
    for ($y = $h - 1; $y -ge 0; $y--) { $bw.Write($raw, $y * $stride, $w * 4) }
    $maskStride = [int]([math]::Ceiling($w / 32.0) * 4)
    $mask = New-Object byte[] ($maskStride * $h)
    $bw.Write($mask, 0, $mask.Length)
    $bw.Flush()
    return $ms.ToArray()
  } finally { $bw.Dispose(); $ms.Dispose() }
}

# New-MultiSizeIco 生成多尺寸 ICO：16/24/32/48/64 走 BMP 条目（老 Shell/资源管理器兼容），
# 128/256 走 PNG 条目（Vista+ 支持，体积小）；大小屏、任务栏、Alt+Tab 都不再糊成单一 32px
function New-MultiSizeIco([string]$SourcePng, [string]$OutIco, [int[]]$Sizes = @(16, 24, 32, 48, 64, 128, 256)) {
  $src = [System.Drawing.Image]::FromFile($SourcePng)
  try {
    $entries = New-Object System.Collections.ArrayList
    foreach ($size in $Sizes) {
      $bmp = [System.Drawing.Bitmap]::new($size, $size, [System.Drawing.Imaging.PixelFormat]::Format32bppArgb)
      try {
        $g = [System.Drawing.Graphics]::FromImage($bmp)
        try {
          $g.Clear([System.Drawing.Color]::Transparent)
          $g.CompositingQuality = [System.Drawing.Drawing2D.CompositingQuality]::HighQuality
          $g.InterpolationMode = [System.Drawing.Drawing2D.InterpolationMode]::HighQualityBicubic
          $g.PixelOffsetMode = [System.Drawing.Drawing2D.PixelOffsetMode]::HighQuality
          $g.SmoothingMode = [System.Drawing.Drawing2D.SmoothingMode]::HighQuality
          $g.DrawImage($src, [System.Drawing.Rectangle]::new(0, 0, $size, $size))
        } finally { $g.Dispose() }
        if ($size -ge 128) {
          $pngStream = [System.IO.MemoryStream]::new()
          try { $bmp.Save($pngStream, [System.Drawing.Imaging.ImageFormat]::Png); $bytes = $pngStream.ToArray() }
          finally { $pngStream.Dispose() }
        } else {
          $bytes = ConvertTo-IconDibEntry $bmp
        }
        [void]$entries.Add([pscustomobject]@{ Size = $size; Bytes = $bytes })
      } finally { $bmp.Dispose() }
    }
    $ms = [System.IO.MemoryStream]::new()
    $bw = [System.IO.BinaryWriter]::new($ms)
    try {
      $bw.Write([uint16]0); $bw.Write([uint16]1); $bw.Write([uint16]$entries.Count)
      $offset = 6 + 16 * $entries.Count
      foreach ($e in $entries) {
        $dim = if ($e.Size -ge 256) { 0 } else { $e.Size }
        $bw.Write([byte]$dim); $bw.Write([byte]$dim); $bw.Write([byte]0); $bw.Write([byte]0)
        $bw.Write([uint16]1); $bw.Write([uint16]32)
        $bw.Write([uint32]$e.Bytes.Length); $bw.Write([uint32]$offset)
        $offset += $e.Bytes.Length
      }
      foreach ($e in $entries) { $bw.Write($e.Bytes, 0, $e.Bytes.Length) }
      $bw.Flush()
      [System.IO.File]::WriteAllBytes($OutIco, $ms.ToArray())
    } finally { $bw.Dispose(); $ms.Dispose() }
  } finally { $src.Dispose() }
}

# Get-IcoEntryCount 读 ICO 头校验是否为多条目图标（结构非法即失败）
function Get-IcoEntryCount([string]$Path) {
  $b = [System.IO.File]::ReadAllBytes($Path)
  if ($b.Length -lt 6 + 16) { throw "ICO 过短: $Path" }
  $reserved = [int]$b[0] + ([int]$b[1] -shl 8)
  $type = [int]$b[2] + ([int]$b[3] -shl 8)
  $count = [int]$b[4] + ([int]$b[5] -shl 8)
  if ($reserved -ne 0 -or $type -ne 1 -or $count -lt 1) { throw "ICO 头非法（reserved=$reserved type=$type count=$count）: $Path" }
  return $count
}

# ---------- payload 目录（排除装配产物本身，防止重跑把安装包打进去） ----------
$payload = Join-Path ([System.IO.Path]::GetTempPath()) ("uvmp-fyne-payload-" + [guid]::NewGuid().ToString("N"))
New-Item -ItemType Directory -Force $payload | Out-Null
try {
  # ---------- 图标：真正 Shade 图 → 清元数据重编码 → 多尺寸 icon.ico（失败即中止） ----------
  if (-not (Test-Path $IconSrc)) { Fail "缺少品牌图标源 $IconSrc（真正 Shade 图标，由 desktop/src/assets/icon.png 重编码）" }
  $srcChunks = Get-PngChunks $IconSrc
  if ($srcChunks -contains "iCCP") { Write-Host "提示: 源图含 iCCP 色彩配置文件，重编码后丢弃（旧 desktop 图需检测）" }
  $CleanPng = Join-Path $payload "icon.png"
  $IcoFile = Join-Path $payload "icon.ico"
  try {
    Save-CleanPng $IconSrc $CleanPng
  } catch {
    Fail "品牌图标重编码失败（$($_.Exception.Message)）：不允许回退默认图标，先修 packaging/icon.png"
  }
  $cleanChunks = Get-PngChunks $CleanPng
  if ($cleanChunks -contains "iCCP") { Fail "重编码后 PNG 仍含 iCCP（$CleanPng）" }
  try {
    New-MultiSizeIco $CleanPng $IcoFile
  } catch {
    Fail "多尺寸 icon.ico 生成失败（$($_.Exception.Message)）：不允许回退默认图标"
  }
  $icoCount = Get-IcoEntryCount $IcoFile
  try {
    $icoCheck = [System.Drawing.Icon]::new($IcoFile)
    $icoCheck.Dispose()
  } catch {
    Fail "icon.ico 无法被 Windows 图标引擎解析（$($_.Exception.Message)）"
  }
  if ($icoCount -lt 7) { Fail "icon.ico 尺寸条目不足（$icoCount < 7）：需 16/24/32/48/64/128/256" }
  Write-Host "品牌图标就绪: icon.png（净化块: $($cleanChunks -join ',')）+ icon.ico（$icoCount 个尺寸）"

  # ---------- payload：三件套 + models + native + 计划任务/启动脚本（显式剔除 browser-runtime） ----------
  foreach ($item in @("uvmp-toolkit.exe", "uvmp-core.exe", "uvmp-export.exe", "models", "native")) {
    $src = Join-Path $StagingFull $item
    if (-not (Test-Path $src)) { Fail "装配布局缺 $item" }
    Copy-Item -Recurse -Force $src (Join-Path $payload $item)
  }
  foreach ($script in @("run-uvmp.vbs", "register-tasks.cmd", "run-uvmp-gui.vbs", "elevate-tasks.ps1")) {
    $src = Join-Path "packaging/fyne" $script
    if (-not (Test-Path $src)) { Fail "缺打包脚本: $src" }
    Copy-Item -Force $src (Join-Path $payload $script)
  }
  # 嵌入字体随安装包和绿色包保留完整 OFL。
  $fontLicenseDir = "core-go/internal/ui/appshell/assets/fonts"
  New-Item -ItemType Directory -Force (Join-Path $payload "licenses") | Out-Null
  foreach ($f in @("NotoSansSC-OFL.txt", "JetBrainsMapleMono-OFL.txt")) {
    $src = Join-Path $fontLicenseDir $f
    if (-not (Test-Path $src)) { Fail "缺少字体许可: $src" }
    Copy-Item -Force $src (Join-Path $payload "licenses/$f")
  }
  if (Test-Path (Join-Path $payload "browser-runtime")) {
    Fail "payload 混入 browser-runtime（Windows 包不再随带 Electron 运行时，CDP 走显式宿主）"
  }
  foreach ($f in @("uvmp-core.exe", "run-uvmp.vbs", "register-tasks.cmd", "run-uvmp-gui.vbs", "elevate-tasks.ps1", "icon.ico", "icon.png")) {
    if (-not (Test-Path (Join-Path $payload $f))) { Fail "payload 缺文件: $f" }
  }

  # ---------- NSI 静态断言（UTF-8 BOM、5 任务检测/注册/卸载终验、无窗调用、品牌图标、隐藏启动器） ----------
  $nsiBytes = [System.IO.File]::ReadAllBytes([System.IO.Path]::GetFullPath($NSI))
  if ($nsiBytes.Length -lt 3 -or $nsiBytes[0] -ne 0xEF -or $nsiBytes[1] -ne 0xBB -or $nsiBytes[2] -ne 0xBF) {
    Fail "NSI 缺 UTF-8 BOM（中文乱码根因）: $NSI"
  }
  $nsiText = Get-Content $NSI -Raw -Encoding UTF8
  if ($nsiText -notmatch '!ifndef ICONFILE[\s\S]{0,200}!error') { Fail "NSI 未强制要求 ICONFILE（不得回退默认图标）" }
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
  if ($nsiText -notmatch "run-uvmp-gui\.vbs") { Fail "NSI 未引用 run-uvmp-gui.vbs（GUI 隐藏启动器）" }
  if ($nsiText -notmatch "elevate-tasks\.ps1") { Fail "NSI 未引用 elevate-tasks.ps1（UAC 提权隐藏控制台）" }
  if ($nsiText -notmatch "nsExec::ExecToStack") { Fail "NSI 未用 nsExec（无窗调用）" }
  if ($nsiText -match "(?i)ExecWait\s+'cmd\s+/c\s+(schtasks|powershell)") { Fail "NSI 仍有 ExecWait 直调 cmd（会弹控制台窗口）" }
  if ($nsiText -notmatch 'CreateShortCut[^\r\n]*wscript\.exe[^\r\n]*run-uvmp-gui\.vbs') { Fail "NSI 快捷方式未指向 wscript 隐藏启动器" }
  if ($nsiText -notmatch 'DisplayIcon"\s+"\$INSTDIR\\icon\.ico') { Fail "NSI 卸载项 DisplayIcon 未引用安装目录 icon.ico" }
  if ($nsiText -match '(?i)browser-runtime\\') { Fail "NSI 仍引用 browser-runtime 路径（Windows 包不再随带 Electron 运行时）" }
  Write-Host "== NSIS 静态校验通过（BOM + 5 任务检测/注册/卸载 + nsExec 无窗 + 品牌图标 + 隐藏启动器）=="

  # ---------- makensis ----------
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

  $nsiArgs = @("/DSTAGINGDIR=$payload", "/DOUTFILE=$OutFile", "/DVERSION=$Version", "/DICONFILE=$IcoFile")
  # 安装程序 exe 的文件属性版本号（VIProductVersion 要求 4 段纯数字；开发版本号非数字则跳过）
  if ($Version -match '^\d+(\.\d+){2,3}$') {
    $parts = @($Version.Split('.'))
    while ($parts.Count -lt 4) { $parts += "0" }
    if (-not ($parts | Where-Object { [int]$_ -gt 65535 })) { $nsiArgs += "/DVI4=$($parts -join '.')" }
  }
  $nsiArgs += [System.IO.Path]::GetFullPath($NSI)
  & $Makensis @nsiArgs
  if ($LASTEXITCODE -ne 0) { Fail "makensis 失败（exit=$LASTEXITCODE）" }
  if (-not (Test-Path $OutFile)) { Fail "NSIS 产物缺失: $OutFile" }

  # ---------- 绿色包（payload + 控制台启动器，解压即用；不含 browser-runtime） ----------
  $stageZip = Join-Path ([System.IO.Path]::GetTempPath()) ("uvmp-fyne-zip-" + [guid]::NewGuid().ToString("N"))
  try {
    $inner = Join-Path $stageZip "uvmp-toolkit-fyne"
    New-Item -ItemType Directory -Force $inner | Out-Null
    Copy-Item -Recurse -Force (Join-Path $payload "*") $inner
    $launcher = "@echo off`r`nsetlocal`r`nset `"PATH=%~dp0native;%PATH%`"`r`n`"%~dp0uvmp-toolkit.exe`" %*`r`n"
    Set-Content -Path (Join-Path $inner "uvmp-toolkit-fyne.cmd") -Value $launcher -Encoding ASCII
    if (Test-Path $ZipFile) { Remove-Item -Force $ZipFile }
    Compress-Archive -Path $inner -DestinationPath $ZipFile
  } finally {
    Remove-Item -Recurse -Force $stageZip -ErrorAction SilentlyContinue
  }
} finally {
  Remove-Item -Recurse -Force $payload -ErrorAction SilentlyContinue
}

Write-Host "NSIS 装配完成:"
Write-Host "  $OutFile"
Write-Host "  $ZipFile"
