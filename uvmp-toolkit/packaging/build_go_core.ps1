# Go 导出工作进程（uvmp-export）构建：go test / go vet / go build —— Windows 主机版
#
# 用法（在仓库根或任意位置，脚本自定位到 uvmp-toolkit/）：
#   powershell -ExecutionPolicy Bypass -File packaging/build_go_core.ps1
#   powershell -ExecutionPolicy Bypass -File packaging/build_go_core.ps1 `
#       -Target linux-amd64 -GoOut dist-core\core\uvmp-export
#
# 参数：
#   -Target      windows-amd64（缺省）| linux-amd64 | linux-arm64（Go 支持交叉编译，CGO 关闭）
#   -GoOut       最终产物路径（缺省 dist/uvmp-go/<target>/uvmp-export(.exe)）
#   -GoPrebuilt  已构建二进制路径（或环境变量 UVMP_GO_PREBUILT）——直接采用，不要求 go 工具链
#
# 约定与 packaging/build_go_core.sh 一致：core-go/ 模块（go.mod + cmd/uvmp-export）、
# CGO_ENABLED=0 静态构建（麒麟 glibc 2.28 零依赖）、go test/vet 按宿主（Windows）执行
# （跨平台不执行目标架构测试——明确说明不冒充，目标架构由 CI 麒麟门禁实测）；
# 工具链缺失且无预构建产物 → 明确失败，不静默跳过。
param(
  [string]$Target = "windows-amd64",
  [string]$GoOut = "",
  [string]$GoPrebuilt = ""
)
$ErrorActionPreference = "Stop"
Set-Location (Join-Path $PSScriptRoot "..")

function Fail([string]$msg) { Write-Host "错误: $msg" -ForegroundColor Red; exit 1 }

# ---------- 目标平台 ----------
switch ($Target) {
  "windows-amd64" { $goos = "windows"; $goarch = "amd64"; $bin = "uvmp-export.exe" }
  "linux-amd64"   { $goos = "linux";   $goarch = "amd64"; $bin = "uvmp-export" }
  "linux-arm64"   { $goos = "linux";   $goarch = "arm64"; $bin = "uvmp-export" }
  default { Fail "不支持的目标平台: $Target（可选 windows-amd64|linux-amd64|linux-arm64）" }
}
if ($GoOut -eq "") { $GoOut = Join-Path "dist/uvmp-go/$Target" $bin }
$GoOutFull = [System.IO.Path]::GetFullPath($GoOut)

# ---------- 通道：复用已构建二进制 ----------
if ($GoPrebuilt -eq "") { $GoPrebuilt = $env:UVMP_GO_PREBUILT }
if ($GoPrebuilt -ne "") {
  if (-not (Test-Path $GoPrebuilt)) { Fail "UVMP_GO_PREBUILT 不存在: $GoPrebuilt" }
  $srcFull = [System.IO.Path]::GetFullPath($GoPrebuilt)
  if ($srcFull -eq $GoOutFull) {
    Write-Host "UVMP_GO_PREBUILT 即输出文件，仅校验: $GoOutFull"
  } else {
    New-Item -ItemType Directory -Force (Split-Path $GoOutFull) | Out-Null
    Copy-Item $GoPrebuilt $GoOutFull -Force
    Write-Host "采用预构建 uvmp-export: $GoPrebuilt -> $GoOutFull"
  }
  if (-not (Test-Path $GoOutFull)) { Fail "预构建产物缺失: $GoOutFull" }
  exit 0
}

# ---------- 源码与工具链检查（缺失即失败）----------
if (-not (Test-Path "core-go/go.mod")) { Fail "缺少 core-go/go.mod（Go 导出工作进程源码模块未就位）" }
if (-not (Test-Path "core-go/cmd/uvmp-export")) { Fail "缺少 core-go/cmd/uvmp-export（导出工作进程入口）" }
if (-not (Get-Command go -ErrorAction SilentlyContinue)) {
  Fail "未找到 go 工具链：请安装 core-go/go.mod 声明版本的 Go，或以 -GoPrebuilt/UVMP_GO_PREBUILT 指入已构建的 $bin"
}
go version
if ($LASTEXITCODE -ne 0) { Fail "go 工具链不可用" }

# ---------- 构建 ----------
if ($Target -eq "windows-amd64") {
  Write-Host "== go vet / go test（宿主=windows-amd64）=="
} else {
  Write-Host "== go vet / go test（按宿主 Windows 架构执行；跨平台不执行 $Target 目标架构测试，由 CI 麒麟门禁实测）=="
}
New-Item -ItemType Directory -Force (Split-Path $GoOutFull) | Out-Null
Push-Location core-go
try {
  $env:CGO_ENABLED = "0"
  go vet ./...
  if ($LASTEXITCODE -ne 0) { Fail "go vet 失败" }
  go test ./...
  if ($LASTEXITCODE -ne 0) { Fail "go test 失败" }
  Write-Host "== go build（$Target，CGO_ENABLED=0 静态）=="
  # GOOS/GOARCH 只在 build 生效（test/vet 必须按宿主执行，跨平台编译的目标架构测试跑不起来）
  $env:GOOS = $goos; $env:GOARCH = $goarch
  go build -o $GoOutFull ./cmd/uvmp-export
  if ($LASTEXITCODE -ne 0) { Fail "go build 失败" }
} finally {
  Remove-Item Env:GOOS -ErrorAction SilentlyContinue
  Remove-Item Env:GOARCH -ErrorAction SilentlyContinue
  Pop-Location
}
if (-not (Test-Path $GoOutFull)) { Fail "构建产物缺失: $GoOutFull" }
Write-Host "构建完成: $GoOutFull"
