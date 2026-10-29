# Fyne 轨浏览器运行时装配（Windows 主机版）：按 packaging/fyne/assets/electron33-runtime.json
# 下载 / SHA256 校验 / 装配 Electron 33 运行时 + 独立浏览器运行时应用（构建机执行，目标机不联网）
#
# 用法（在仓库根或任意位置，脚本自定位到 uvmp-toolkit/）：
#   powershell -ExecutionPolicy Bypass -File packaging/fyne/fetch_browser_runtime.ps1 `
#       [-Platform windows-amd64] [-OutDir dist-fyne\windows-amd64\browser-runtime]
#
# 环境（与 sh 版同名同义）：
#   UVMP_BROWSER_ARCHIVE  本地 Electron 发行 zip——跳过下载，仍按清单 SHA256 校验
#   UVMP_BROWSER_DIR      本地已解包目录——跳过下载与归档校验，直接装配（校验主可执行文件存在）
#   UVMP_BROWSER_CACHE    下载缓存目录（缺省 packaging/.fyne-cache/electron）
#
# 纪律（以 packaging/fyne/assets/electron33-runtime.json 为准）：
#   - 归档 SHA256 与清单不匹配（或清单为空）→ 一律失败，禁止编造校验值
#   - 装配产物：主可执行 electron.exe 直落 outdir 根；独立浏览器运行时应用落 outdir/app/
#     （拷 uvmp-toolkit/browser-runtime/{package.json,main.cjs}，main=main.cjs 双模式：
#     --uvmp-browser-mode=export|course，见 browser-runtime/main.cjs 头注释）；
#     另写 outdir/uvmp-browser.json 元数据（路径全 ASCII）
param(
  [string]$Platform = "",
  [string]$OutDir = ""
)
$ErrorActionPreference = "Stop"
Set-Location (Join-Path $PSScriptRoot "..\..")
$Manifest = "packaging/fyne/assets/electron33-runtime.json"
$CacheDir = if ($env:UVMP_BROWSER_CACHE) { $env:UVMP_BROWSER_CACHE } else { "packaging/.fyne-cache/electron" }

function Fail([string]$msg) { Write-Host "错误: $msg" -ForegroundColor Red; exit 1 }

# ---------- 参数 ----------
if ($Platform -eq "") { $Platform = "windows-amd64"; Write-Host "平台缺省: $Platform" }
if ($Platform -notin @("windows-amd64", "linux-amd64", "linux-arm64")) {
  Fail "不支持的平台: $Platform（可选 windows-amd64|linux-amd64|linux-arm64）"
}
if ($OutDir -eq "") { $OutDir = Join-Path "dist-fyne/$Platform" "browser-runtime" }
$OutDirFull = [System.IO.Path]::GetFullPath($OutDir)

# ---------- 读清单 ----------
if (-not (Test-Path $Manifest)) { Fail "缺少清单: $Manifest" }
$m = Get-Content $Manifest -Raw -Encoding UTF8 | ConvertFrom-Json
$a = $m.assets.$Platform
if ($null -eq $a) { Fail "清单无平台条目: $Platform" }
$Url = $a.url; $ArchiveType = $a.archive_type; $Sha = $a.archive_sha256
$MirrorUrl = $a.mirror_url
$Exe = $a.executable; $Version = $m.product.version
if (-not $Exe) { Fail "清单缺 executable 字段（$Platform）" }
if (-not $Sha) { Fail "清单缺 archive_sha256（$Platform）：未验资产禁止装配，先按官方 SHASUMS256 实取写回清单" }

function Get-Sha256([string]$path) {
  (Get-FileHash -Algorithm SHA256 -Path $path).Hash.ToLower()
}

function Write-Metadata([string]$out, [string]$source, [string]$sha) {
  $meta = [ordered]@{ platform = $Platform; version = $Version; archive_sha256 = $sha
                      source = $source; executable = $Exe }
  ($meta | ConvertTo-Json) + "`n" | Set-Content -Path (Join-Path $out "uvmp-browser.json") -Encoding UTF8
}

# ---------- 通道：本地已解包目录 ----------
if ($env:UVMP_BROWSER_DIR) {
  $SrcDir = $env:UVMP_BROWSER_DIR
  if (-not (Test-Path $SrcDir)) { Fail "UVMP_BROWSER_DIR 不存在: $SrcDir" }
  if (-not (Test-Path (Join-Path $SrcDir $Exe))) { Fail "UVMP_BROWSER_DIR 缺主可执行 ${Exe}: $SrcDir" }
  New-Item -ItemType Directory -Force $OutDirFull | Out-Null
  Copy-Item -Recurse -Force (Join-Path $SrcDir "*") $OutDirFull
  Write-Metadata $OutDirFull "UVMP_BROWSER_DIR=$SrcDir" "uvmp-browser-dir（未走归档校验）"
  Write-Host "采用本地已解包浏览器运行时: $SrcDir -> $OutDirFull"
} else {
  # ---------- 取归档（本地包或下载） ----------
  New-Item -ItemType Directory -Force $CacheDir | Out-Null
  if ($env:UVMP_BROWSER_ARCHIVE) {
    $Archive = $env:UVMP_BROWSER_ARCHIVE
    if (-not (Test-Path $Archive)) { Fail "UVMP_BROWSER_ARCHIVE 不存在: $Archive" }
  } else {
    if (-not $Url) { Fail "清单缺 url（$Platform）：未实测地址禁止装配" }
    $Ext = if ($ArchiveType) { $ArchiveType } else { "zip" }
    $Archive = Join-Path $CacheDir "electron-$Version-$Platform.$Ext"
    if (-not (Test-Path $Archive)) {
      Write-Host "下载 Electron $Version（$Platform）: $Url"
      $downloaded = $false
      try {
        Invoke-WebRequest -Uri $Url -OutFile "$Archive.part" -UseBasicParsing
        $downloaded = $true
      } catch {
        Write-Host "官方直链失败: $($_.Exception.Message)"
        # 官方直链失败回退镜像（SHA256 仍按官方校验和强制，镜像只解决连通性）
        if ($MirrorUrl) {
          Write-Host "回退镜像: $MirrorUrl"
          try {
            Invoke-WebRequest -Uri $MirrorUrl -OutFile "$Archive.part" -UseBasicParsing
            $downloaded = $true
          } catch {
            Fail "下载失败: $Url 与镜像 $MirrorUrl（可改用 UVMP_BROWSER_ARCHIVE/UVMP_BROWSER_DIR 本地通道）——$($_.Exception.Message)"
          }
        } else {
          Fail "下载失败: $Url（可改用 UVMP_BROWSER_ARCHIVE/UVMP_BROWSER_DIR 本地通道）——$($_.Exception.Message)"
        }
      }
      if ($downloaded) { Move-Item -Force "$Archive.part" $Archive }
    }
  }

  # ---------- SHA256 校验（与清单必须一致） ----------
  $Actual = Get-Sha256 $Archive
  if ($Actual -ne $Sha.ToLower()) {
    Fail "SHA256 不匹配: $Archive（实际 $Actual，清单 $Sha）——禁止装配未验资产"
  }
  Write-Host "SHA256 校验通过: $Actual"

  # ---------- 装配（zip 剥掉单一顶层目录后的内容直落 outdir） ----------
  if (Test-Path $OutDirFull) { Remove-Item -Recurse -Force $OutDirFull }
  New-Item -ItemType Directory -Force $OutDirFull | Out-Null
  $tmp = Join-Path ([System.IO.Path]::GetTempPath()) ("uvmp-fyne-electron-" + [guid]::NewGuid().ToString("N"))
  New-Item -ItemType Directory -Force $tmp | Out-Null
  try {
    Expand-Archive -Path $Archive -DestinationPath $tmp -Force
    $tops = Get-ChildItem $tmp
    $root = $tmp
    if ($tops.Count -eq 1 -and $tops[0].PSIsContainer) {
      # 唯一顶层目录才剥壳（Electron 发行 zip 为根铺开，不会命中）
      $inner = Get-ChildItem $tops[0].FullName
      if ($inner.Count -gt 0) { $root = $tops[0].FullName }
    }
    Copy-Item -Recurse -Force (Join-Path $root "*") $OutDirFull
  } finally {
    Remove-Item -Recurse -Force $tmp -ErrorAction SilentlyContinue
  }
  if (-not (Test-Path (Join-Path $OutDirFull $Exe))) { Fail "装配后缺主可执行 $Exe（归档结构与清单 executable 不符）" }
  Write-Metadata $OutDirFull $Url $Actual
  Write-Host "浏览器运行时装配完成: $(Join-Path $OutDirFull $Exe)"
}

# ---------- 独立浏览器运行时应用（app/：main=main.cjs，export/course 双模式） ----------
$RTSrc = "browser-runtime"
if (-not (Test-Path (Join-Path $RTSrc "main.cjs"))) { Fail "缺少独立浏览器运行时脚本: $RTSrc/main.cjs" }
if (-not (Test-Path (Join-Path $RTSrc "package.json"))) { Fail "缺少独立浏览器运行时清单: $RTSrc/package.json" }
$AppDir = Join-Path $OutDirFull "app"
New-Item -ItemType Directory -Force $AppDir | Out-Null
Copy-Item -Force (Join-Path $RTSrc "main.cjs") (Join-Path $AppDir "main.cjs")
Copy-Item -Force (Join-Path $RTSrc "package.json") (Join-Path $AppDir "package.json")
# app 入口契约：package.json main 须指向双模式主脚本 main.cjs（Electron 以应用目录方式加载）
$main = (Get-Content (Join-Path $AppDir "package.json") -Raw -Encoding UTF8 | ConvertFrom-Json).main
if ($main -ne "main.cjs") {
  Fail "app/package.json main 应为 main.cjs（当前 $main）：app 入口须与 export/course 双模式主脚本匹配"
}
Write-Host "独立浏览器运行时应用就位: $AppDir（main=main.cjs 双模式）"
Write-Host "  导出口径: UVMP_BROWSER_APP=$AppDir + UVMP_BROWSER_WORKER=--uvmp-browser-mode=export（uvmp-export 渲染/自检）"
Write-Host "  课程口径: internal/browser 以 --uvmp-browser-mode=course [--uvmp-start-url=…] 驱动同一 app"
