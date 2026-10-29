# Fyne 轨 OCR 运行时装配（Windows 主机版）：按 packaging/fyne/assets/onnx-assets.json 校验
# SHA256 并装入 onnxruntime 库（native/）与 PP-OCRv4 模型（models/）（构建机执行，目标机不联网）
#
# 用法（在仓库根或任意位置，脚本自定位到 uvmp-toolkit/）：
#   powershell -ExecutionPolicy Bypass -File packaging/fyne/assemble_onnx.ps1 `
#       [-Platform windows-amd64] [-OutDir dist-fyne\windows-amd64]
#
# 环境（与 sh 版同名同义）：
#   UVMP_ORT_DIR          onnxruntime-node 的 napi-v3 目录
#                         （缺省 desktop/node_modules/onnxruntime-node/bin/napi-v3，npm ci 后就位）
#   UVMP_ORT_ARCHIVE      onnxruntime-node-<ver>.tgz——UVMP_ORT_DIR 缺失时从 tgz 解出 napi-v3
#   UVMP_ORT_MODELS_DIR   模型源目录（缺省 desktop/electron/quizsearch/models，随仓）
#
# 纪律（以 packaging/fyne/assets/onnx-assets.json 为准）：
#   - 每个文件 SHA256 必须与清单一致（清单为空视为未验资产），不匹配即失败，禁止编造
#   - Windows 只装 onnxruntime.dll（CPU EP，DirectML.dll 不随包）
param(
  [string]$Platform = "windows-amd64",
  [string]$OutDir = ""
)
$ErrorActionPreference = "Stop"
Set-Location (Join-Path $PSScriptRoot "..\..")
$Manifest = "packaging/fyne/assets/onnx-assets.json"

function Fail([string]$msg) { Write-Host "错误: $msg" -ForegroundColor Red; exit 1 }

if ($Platform -notin @("windows-amd64", "linux-amd64", "linux-arm64")) {
  Fail "不支持的平台: $Platform（可选 windows-amd64|linux-amd64|linux-arm64）"
}
if ($OutDir -eq "") { $OutDir = "dist-fyne/$Platform" }
$OutDirFull = [System.IO.Path]::GetFullPath($OutDir)

# ---------- 库来源定位 ----------
$OrtDir = if ($env:UVMP_ORT_DIR) { $env:UVMP_ORT_DIR } else { "desktop/node_modules/onnxruntime-node/bin/napi-v3" }
if (-not (Test-Path $OrtDir) -and $env:UVMP_ORT_ARCHIVE) {
  if (-not (Test-Path $env:UVMP_ORT_ARCHIVE)) { Fail "UVMP_ORT_ARCHIVE 不存在: $($env:UVMP_ORT_ARCHIVE)" }
  $tmp = Join-Path ([System.IO.Path]::GetTempPath()) ("uvmp-fyne-ort-" + [guid]::NewGuid().ToString("N"))
  New-Item -ItemType Directory -Force $tmp | Out-Null
  $extTmp = Join-Path $tmp "ext"
  Expand-Archive -Path $env:UVMP_ORT_ARCHIVE -DestinationPath $extTmp -Force
  $hit = Get-ChildItem -Recurse -Directory -Path $extTmp | Where-Object { $_.Name -eq "napi-v3" } | Select-Object -First 1
  if ($null -eq $hit) { Fail "tgz 内未找到 bin/napi-v3: $($env:UVMP_ORT_ARCHIVE)" }
  $OrtDir = $hit.FullName
  Write-Host "从 tgz 解出 napi-v3: $OrtDir"
}
if (-not (Test-Path $OrtDir)) { Fail "缺少 onnxruntime 库目录: $OrtDir（npm ci 后就位，或用 UVMP_ORT_DIR/UVMP_ORT_ARCHIVE 指入）" }
$ModelsDir = if ($env:UVMP_ORT_MODELS_DIR) { $env:UVMP_ORT_MODELS_DIR } else { "desktop/electron/quizsearch/models" }
if (-not (Test-Path $ModelsDir)) { Fail "缺少模型目录: $ModelsDir" }

function Get-Sha256([string]$path) { (Get-FileHash -Algorithm SHA256 -Path $path).Hash.ToLower() }

function Verify-And-Copy([string]$src, [string]$want, [string]$dst) {
  if (-not (Test-Path $src)) { Fail "装配源缺失: $src" }
  $actual = Get-Sha256 $src
  if ($actual -ne $want.ToLower()) {
    Fail "SHA256 不匹配: $src（实际 $actual，清单 $want）——禁止装配未验资产"
  }
  New-Item -ItemType Directory -Force (Split-Path $dst) | Out-Null
  Copy-Item -Force $src $dst
  Write-Host "  装入 $dst（$actual）"
}

# ---------- 按清单装配 ----------
if (-not (Test-Path $Manifest)) { Fail "缺少清单: $Manifest" }
$m = Get-Content $Manifest -Raw -Encoding UTF8 | ConvertFrom-Json
$count = 0
foreach ($e in @($m.libraries.$Platform | Where-Object { $_.required -ne $false })) {
  Verify-And-Copy (Join-Path $OrtDir $e.source) $e.sha256 (Join-Path $OutDirFull $e.dest)
  $count++
}
foreach ($e in @($m.models)) {
  Verify-And-Copy (Join-Path $ModelsDir $e.source) $e.sha256 (Join-Path $OutDirFull $e.dest)
  $count++
}
if ($count -eq 0) { Fail "清单无可装配条目（$Platform）" }
Write-Host "OCR 运行时装配完成：$count 个文件 -> $OutDirFull（native/ + models/）"
