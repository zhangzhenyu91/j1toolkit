# Fyne 轨核心构建（Windows 主机，MSYS2 mingw-w64 CGO）：Go+Fyne CGO 构建 uvmp-toolkit
# + Go 核心 CLI uvmp-core + Go 导出工作进程 uvmp-export，产出到 dist-fyne/windows-amd64/
#
# 前置（CI 用 msys2/setup-msys2：msystem=MINGW64、path-type=inherit，装
# mingw-w64-x86_64-gcc / make / pkg-config；本机装 MSYS2 MINGW64 并加入 PATH）：
#   gcc、pkg-config 可用（Fyne CGO 必需；go build 以 CC=gcc 走 mingw-w64）
#
# 用法（在仓库根或任意位置，脚本自定位到 uvmp-toolkit/）：
#   powershell -ExecutionPolicy Bypass -File packaging/fyne/build_fyne_core.ps1 `
#       [-Platform windows-amd64] [-FyneMod core-go] [-FynePkg ./cmd/uvmp-toolkit]
#
# 环境（与 sh 版同名同义，参数优先）：
#   UVMP_FYNE_MOD / UVMP_FYNE_PKG / UVMP_FYNE_BIN / UVMP_FYNE_SKIP_EXPORT / UVMP_GO_PREBUILT
#   UVMP_FYNE_TAGS        go build -tags 值（缺省 onnx：OCR 冒烟需要 ONNX 实现——
#                         internal/ocr/ort 置于 //go:build onnx 之后、yalue/onnxruntime_go 为
#                         CGO 包，见 core-go/internal/ocr/ort/doc.go；显式设空则不带标签构建）
#   UVMP_FYNE_WINDOWED=1  链接 -H=windowsgui（无控制台窗口；CI 冒烟保持控制台便于取退出码与日志）
#
# 纪律：
#   - GUI 入口缺失即明确失败（点名 UVMP_FYNE_PKG 契约），不静默跳过、不出残缺产物
#   - CGO_ENABLED=1 且只做本机 windows-amd64（CGO 不交叉；arm64 无 Windows 产物线）
#   - go vet / go test 按宿主执行；uvmp-export 保持 CGO_ENABLED=0（复用 build_go_core.ps1）
param(
  [string]$Platform = "windows-amd64",
  [string]$FyneMod = "",
  [string]$FynePkg = "",
  [string]$FyneBin = ""
)
$ErrorActionPreference = "Stop"
Set-Location (Join-Path $PSScriptRoot "..\..")

function Fail([string]$msg) { Write-Host "错误: $msg" -ForegroundColor Red; exit 1 }

if ($Platform -ne "windows-amd64") {
  Fail "不支持的平台: $Platform（Windows 构建仅 windows-amd64；linux 两平台用 build_fyne_core.sh）"
}
$Mod = if ($FyneMod) { $FyneMod } elseif ($env:UVMP_FYNE_MOD) { $env:UVMP_FYNE_MOD } else { "core-go" }
$Pkg = if ($FynePkg) { $FynePkg } elseif ($env:UVMP_FYNE_PKG) { $env:UVMP_FYNE_PKG } else { "./cmd/uvmp-toolkit" }
$BinName = if ($FyneBin) { $FyneBin } elseif ($env:UVMP_FYNE_BIN) { $env:UVMP_FYNE_BIN } else { "uvmp-toolkit.exe" }
$Tags = if ($null -ne $env:UVMP_FYNE_TAGS) { $env:UVMP_FYNE_TAGS } else { "onnx" }
$OutDir = [System.IO.Path]::GetFullPath("dist-fyne/windows-amd64")
New-Item -ItemType Directory -Force $OutDir | Out-Null

# ---------- Fyne GUI 源码契约（缺失即失败） ----------
if (-not (Test-Path (Join-Path $Mod "go.mod"))) { Fail "缺少 $Mod/go.mod（Fyne GUI 所在 Go 模块未就位；UVMP_FYNE_MOD 可改指）" }
$PkgDir = Join-Path $Mod ($Pkg -replace '^\./', '')
if (-not (Test-Path $PkgDir)) {
  Fail "缺少 Fyne GUI 入口 $PkgDir（契约：UVMP_FYNE_PKG 缺省 ./cmd/uvmp-toolkit，可改指；GUI 源码与 go.mod fyne 依赖由 GUI 侧落位后本脚本才可构建）"
}
# onnx 构建标签工具链预检：ORT 实现（internal/ocr/ort）在 //go:build onnx 之后、依赖
# yalue/onnxruntime_go（CGO 包），缺失时 go build 报错难读，这里点名补依赖的出处
if (",$Tags," -like "*,onnx,*") {
  if (-not (Select-String -Path (Join-Path $Mod "go.mod") -Pattern "github.com/yalue/onnxruntime_go" -Quiet)) {
    Fail "构建标签含 onnx 但 $Mod/go.mod 未含 github.com/yalue/onnxruntime_go（启用步骤见 core-go/internal/ocr/ort/doc.go；或设 UVMP_FYNE_TAGS 关闭 onnx）"
  }
}

# ---------- 工具链检查 ----------
if (-not (Get-Command go -ErrorAction SilentlyContinue)) { Fail "未找到 go 工具链（actions/setup-go 或本机安装）" }
if (-not (Get-Command gcc -ErrorAction SilentlyContinue)) {
  Fail "未找到 gcc（Fyne CGO 必需）：MSYS2 装 mingw-w64-x86_64-gcc，CI 用 msys2/setup-msys2(msystem=MINGW64, path-type=inherit)"
}
go version
gcc --version | Select-Object -First 1

# ---------- 构建 Fyne GUI（CGO，本机 windows-amd64） ----------
Write-Host "== go vet / go test（宿主 windows-amd64）=="
Push-Location $Mod
try {
  $env:CGO_ENABLED = "1"
  $env:CC = "gcc"
  go vet ./...
  if ($LASTEXITCODE -ne 0) { Fail "go vet 失败" }
  go test ./...
  if ($LASTEXITCODE -ne 0) { Fail "go test 失败" }
  Write-Host "== go build（windows-amd64，CGO_ENABLED=1，tags=$(if ($Tags) { $Tags } else { '无' })）$Pkg =="
  $ldflags = ""
  if ($env:UVMP_FYNE_WINDOWED -eq "1") { $ldflags = "-H=windowsgui" }
  $buildArgs = @("build", "-trimpath")
  if ($Tags) { $buildArgs += @("-tags", $Tags) }
  if ($ldflags) { $buildArgs += @("-ldflags", $ldflags) }
  $buildArgs += @("-o", (Join-Path $OutDir $BinName), $Pkg)
  go @buildArgs
  if ($LASTEXITCODE -ne 0) { Fail "go build 失败" }
} finally {
  Remove-Item Env:CC -ErrorAction SilentlyContinue
  Remove-Item Env:CGO_ENABLED -ErrorAction SilentlyContinue
  Pop-Location
}
if (-not (Test-Path (Join-Path $OutDir $BinName))) { Fail "构建产物缺失: $(Join-Path $OutDir $BinName)" }
Write-Host "Fyne GUI 构建完成: $(Join-Path $OutDir $BinName)"

# ---------- 构建 uvmp-core（Go 核心 CLI/RPC，CGO_ENABLED=0，随包装入供命令行/排障） ----------
if (-not (Test-Path (Join-Path $Mod "cmd/uvmp-core"))) { Fail "缺少 $Mod/cmd/uvmp-core（Go 核心 CLI 入口）" }
Push-Location $Mod
try {
  $env:CGO_ENABLED = "0"
  go build -trimpath -o (Join-Path $OutDir "uvmp-core.exe") ./cmd/uvmp-core
  if ($LASTEXITCODE -ne 0) { Fail "uvmp-core 构建失败" }
} finally {
  Remove-Item Env:CGO_ENABLED -ErrorAction SilentlyContinue
  Pop-Location
}
if (-not (Test-Path (Join-Path $OutDir "uvmp-core.exe"))) { Fail "uvmp-core 构建产物缺失: $(Join-Path $OutDir 'uvmp-core.exe')" }
Write-Host "uvmp-core 构建完成: $(Join-Path $OutDir 'uvmp-core.exe')"

# ---------- 构建 uvmp-export（复用既有产线，CGO_ENABLED=0） ----------
if ($env:UVMP_FYNE_SKIP_EXPORT -eq "1") {
  Write-Host "跳过 uvmp-export 构建（UVMP_FYNE_SKIP_EXPORT=1）"
} else {
  powershell -NoProfile -File packaging/build_go_core.ps1 -Target windows-amd64 `
    -GoOut (Join-Path $OutDir "uvmp-export.exe")
  if ($LASTEXITCODE -ne 0) { Fail "uvmp-export 构建失败" }
  if (-not (Test-Path (Join-Path $OutDir "uvmp-export.exe"))) { Fail "uvmp-export 构建产物缺失" }
  Write-Host "uvmp-export 构建完成: $(Join-Path $OutDir 'uvmp-export.exe')"
}
Write-Host "Fyne 轨核心构建完成: $OutDir"
