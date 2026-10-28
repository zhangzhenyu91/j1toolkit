# 内置浏览器资产：下载 / SHA256 校验 / 装配（构建机执行；目标机不联网下载）—— Windows 主机版
#
# 用法（在仓库根或任意位置，脚本自定位到 uvmp-toolkit/）：
#   powershell -ExecutionPolicy Bypass -File packaging/browser_assets.ps1 [<platform>] [<outdir>]
#   powershell -ExecutionPolicy Bypass -File packaging/browser_assets.ps1 -RecordSha <platform>
# platform ∈ windows-amd64 | linux-amd64 | linux-arm64（缺省 windows-amd64）
# outdir 缺省 dist-browser/browser（最终随 electron-builder 进 resources/browser/）
#
# 环境：
#   UVMP_BROWSER_ARCHIVE  本地归档包（zip/tar.gz）路径——跳过下载，仍按清单 SHA256 校验
#   UVMP_BROWSER_DIR      本地已解包目录——跳过下载与归档校验，直接装配（校验主可执行文件存在）
#   UVMP_BROWSER_CACHE    下载缓存目录（缺省 packaging/.browser-cache）
#
# 纪律与 packaging/browser_assets.sh 一致（以 packaging/browser-assets.json 为准）：
#   - 归档 SHA256 未记录（空）→ 一律失败；发版前先 -RecordSha 下载实测写回，或用 UVMP_BROWSER_DIR 通道
#   - 资产缺失（无 URL 且无本地通道）→ 失败，禁止编造地址（linux-arm64 即此状态）
#   - Windows 主机装配 linux 资产时无法保留 Unix 可执行位（NTFS 无该语义）——醒目警告，
#     正式发版的 linux 包请在 CI/linux 构建机装配；electron-builder 侧既有流程不受影响
param(
  [Parameter(Position = 0)][string]$Platform = "windows-amd64",
  [Parameter(Position = 1)][string]$OutDir = "dist-browser/browser",
  [switch]$RecordSha
)
$ErrorActionPreference = "Stop"
Set-Location (Join-Path $PSScriptRoot "..")
$Manifest = "packaging/browser-assets.json"
if ($env:UVMP_BROWSER_CACHE) { $CacheDir = $env:UVMP_BROWSER_CACHE } else { $CacheDir = "packaging/.browser-cache" }

function Fail([string]$msg) { Write-Host "错误: $msg" -ForegroundColor Red; exit 1 }

if ($Platform -notin @("windows-amd64", "linux-amd64", "linux-arm64")) {
  Fail "不支持的平台: $Platform（可选 windows-amd64|linux-amd64|linux-arm64）"
}
if (-not (Test-Path $Manifest)) { Fail "缺少清单: $Manifest" }

# ---------- 读清单 ----------
$m = Get-Content $Manifest -Raw -Encoding UTF8 | ConvertFrom-Json
$assetProp = $m.assets.PSObject.Properties[$Platform]
if ($null -eq $assetProp) { Fail "清单无平台条目: $Platform" }
$a = $assetProp.Value
$Url = [string]$a.url
$ArchiveType = [string]$a.archive_type
$Sha = [string]$a.archive_sha256
$Exe = [string]$a.executable
$Version = [string]$m.product.version
if ($Exe -eq "") { Fail "清单缺 executable 字段（$Platform）" }

function Get-Sha256([string]$file) { (Get-FileHash -Algorithm SHA256 -Path $file).Hash.ToLower() }

function Write-Metadata([string]$out, [string]$source, [string]$sha) {
  $meta = [ordered]@{
    platform       = $Platform
    version        = $Version
    archive_sha256 = $sha
    source         = $Source
    executable     = $Exe
  }
  ($meta | ConvertTo-Json) + "`n" | Set-Content (Join-Path $out "uvmp-browser.json") -Encoding UTF8
}

function Test-SameDir([string]$x, [string]$y) {
  return ([System.IO.Path]::GetFullPath($x) -eq [System.IO.Path]::GetFullPath($y))
}

function Assert-Exe([string]$dir) {
  if (-not (Test-Path (Join-Path $dir $Exe))) { Fail "缺少主可执行文件: $dir/$Exe" }
}

# ---------- -RecordSha：下载/取归档 → 实测 SHA256 写回清单（保留清单原格式） ----------
if ($RecordSha) {
  $archive = $env:UVMP_BROWSER_ARCHIVE
  if (-not $archive) {
    if ($Url -eq "") { Fail "$Platform 清单无 URL 且未设 UVMP_BROWSER_ARCHIVE，无法记录 SHA256" }
    New-Item -ItemType Directory -Force $CacheDir | Out-Null
    $archive = Join-Path $CacheDir ([System.IO.Path]::GetFileName(([Uri]$Url).LocalPath))
    if (-not (Test-Path $archive)) {
      Write-Host "下载 $Url ..."
      curl.exe -fSL --retry 3 -o $archive $Url
      if ($LASTEXITCODE -ne 0) { Fail "下载失败: $Url" }
    }
  }
  if (-not (Test-Path $archive)) { Fail "归档不存在: $archive" }
  $newSha = Get-Sha256 $archive
  $txt = [System.IO.File]::ReadAllText((Resolve-Path $Manifest))
  $re = New-Object System.Text.RegularExpressions.Regex ('("' + [regex]::Escape($Platform) + '"[\s\S]*?"archive_sha256":\s*")[^"]*(")')
  if (-not $re.IsMatch($txt)) { Fail "清单中找不到 $Platform 的 archive_sha256 字段" }
  $txt = $re.Replace($txt, ('${1}' + $newSha + '${2}'), 1)
  $utf8NoBom = New-Object System.Text.UTF8Encoding($false)
  [System.IO.File]::WriteAllText((Resolve-Path $Manifest), $txt, $utf8NoBom)
  Write-Host "已写回 ${Platform}: archive_sha256=$newSha"
  exit 0
}

# ---------- 装配 ----------
# 通道 1：本地已解包目录（信任构建者；来源即输出目录时仅校验）
if ($env:UVMP_BROWSER_DIR) {
  $srcDir = $env:UVMP_BROWSER_DIR
  if (-not (Test-Path $srcDir)) { Fail "UVMP_BROWSER_DIR 不是目录: $srcDir" }
  Assert-Exe $srcDir
  if (Test-SameDir $srcDir $OutDir) {
    Write-Host "UVMP_BROWSER_DIR 即输出目录，仅校验: $OutDir/$Exe"
  } else {
    Write-Host "按本地目录装配: $srcDir -> $OutDir"
    if (Test-Path $OutDir) { Remove-Item -Recurse -Force $OutDir }
    New-Item -ItemType Directory -Force $OutDir | Out-Null
    Copy-Item -Recurse (Join-Path $srcDir "*") $OutDir
    Assert-Exe $OutDir
  }
  Write-Host "浏览器资产装配完成（目录通道）: $OutDir/$Exe"
  exit 0
}

# 通道 2/3：本地归档或按 URL 下载（均须清单 SHA256 已记录；未验资产在下载前即失败）
if ($Sha -eq "") {
  Fail "$Platform 的 archive_sha256 未记录（未验资产不得装配）：发版前先执行 packaging/browser_assets.ps1 -RecordSha $Platform 实测写回，或用 UVMP_BROWSER_DIR 通道"
}
$archive = $env:UVMP_BROWSER_ARCHIVE
if ($archive) {
  if (-not (Test-Path $archive)) { Fail "UVMP_BROWSER_ARCHIVE 不存在: $archive" }
} else {
  if ($Url -eq "") {
    Fail "$Platform 清单无 URL 且未设 UVMP_BROWSER_ARCHIVE/UVMP_BROWSER_DIR（资产缺失即构建失败，见 $Manifest status 说明）"
  }
  New-Item -ItemType Directory -Force $CacheDir | Out-Null
  $archive = Join-Path $CacheDir ([System.IO.Path]::GetFileName(([Uri]$Url).LocalPath))
  if (Test-Path $archive) {
    Write-Host "命中下载缓存: $archive"
  } else {
    Write-Host "下载 $Url ..."
    curl.exe -fSL --retry 3 -o $archive $Url
    if ($LASTEXITCODE -ne 0) { Fail "下载失败: $Url" }
  }
}

$actual = Get-Sha256 $archive
if ($actual -ne $Sha) { Fail "SHA256 不符: 清单=$Sha 实测=$actual（$archive）" }

$tmp = Join-Path ([System.IO.Path]::GetTempPath()) ("uvmp-browser-" + [System.IO.Path]::GetRandomFileName())
New-Item -ItemType Directory -Force $tmp | Out-Null
try {
  switch ($ArchiveType) {
    "zip" {
      Expand-Archive -Path $archive -DestinationPath $tmp -Force
    }
    "tar.gz" {
      tar -xzf $archive -C $tmp
      if ($LASTEXITCODE -ne 0) { Fail "解包失败: $archive" }
    }
    default { Fail "不支持的 archive_type: $ArchiveType" }
  }
  # 单一顶层目录剥壳（chrome-win64/、chrome-linux64/ 等），内容直落装配根
  $entries = @(Get-ChildItem $tmp)
  $src = $tmp
  if ($entries.Count -eq 1 -and $entries[0].PSIsContainer) { $src = $entries[0].FullName }
  Assert-Exe $src
  if (Test-Path $OutDir) { Remove-Item -Recurse -Force $OutDir }
  New-Item -ItemType Directory -Force $OutDir | Out-Null
  Copy-Item -Recurse (Join-Path $src "*") $OutDir
  Assert-Exe $OutDir
} finally {
  Remove-Item -Recurse -Force $tmp -ErrorAction SilentlyContinue
}
Write-Metadata $OutDir $archive $actual
if ($Platform -like "linux-*") {
  Write-Warning "在 Windows 主机装配 linux 浏览器资产：NTFS 无法保留 Unix 可执行位，正式发版的 linux 包请在 CI/linux 构建机装配（既有 -LinuxCore* 应急流程同此限制）"
}
Write-Host "浏览器资产装配完成（$Platform $Version sha256=$actual）: $OutDir/$Exe"
