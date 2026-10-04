# 本机构建（GitHub Actions 额度耗尽时的发布通道）——Windows 主机
#
# 用法（在仓库根目录）：
#   powershell -ExecutionPolicy Bypass -File packaging/build_local.ps1
#       → Windows 全套：dist-electron/uvmp-toolkit-Setup-<ver>.exe + ...-win-portable.zip
#
#   powershell -ExecutionPolicy Bypass -File packaging/build_local.ps1 `
#       -LinuxCoreAmd64 D:\pkgs\core-linux-amd64 -LinuxCoreArm64 D:\pkgs\core-linux-arm64
#       → 追加麒麟 deb + tar.gz（amd64/arm64）
#
# Linux 核心本机（Windows）做不出来（PyInstaller 不跨平台、本机无 Docker/WSL）：
#   在麒麟机上跑 packaging/build_on_device.sh 得到核心目录（含 uvmp-core 可执行文件），
#   拷回本机后用上面的 -LinuxCore* 参数指过来即可。若核心 Python 代码相比上一版没变，
#   也可直接复用上一版 release 包里的 resources/core/ 目录。
#
# 本脚本内置两处"本机网络/权限"自救（首次跑通前必看）：
#   1) electron/winCodeSign/nsis 等二进制走 npmmirror 镜像（github.com 直连不通）；
#      npmmirror 重定向 URL 带签名 → electron-builder 缓存目录名每次随机 →
#      winCodeSign 里的 darwin 符号链接在非管理员 Windows 上解不开 → 反复重试失败。
#      对策：预下载到 packaging/.eb-binaries/ 并起本地静态服务（127.0.0.1:8899），
#      URL 稳定则缓存命中；且给 app-builder.exe 打二进制补丁（见下）。
#   2) app-builder 解包时给 7za 传 -snld——该参数须 7-Zip≥22 才有效，而 7zip-bin
#      打包的还是 21.07 → 参数被忽略仍尝试建符号链接 → 权限报错。
#      补丁：app-builder.exe 里 "-snld" → "-snl-"（等长 ASCII 替换；-snl- 把符号链接
#      落成普通文件，无需特权，7-Zip 21.07 也认）。本脚本每次构建前幂等打补丁。
param(
  [switch]$SkipCore,          # 跳过 Windows 核心构建（复用已有 dist/uvmp-core）
  [string]$LinuxCoreAmd64 = "",
  [string]$LinuxCoreArm64 = ""
)
$ErrorActionPreference = "Stop"
Set-Location (Join-Path $PSScriptRoot "..")

$env:ELECTRON_MIRROR = "https://npmmirror.com/mirrors/electron/"

$PY = "E:\qtenv\python.exe"     # 3.12 + pyinstaller（本机既有测试环境）
if (-not (Test-Path $PY)) { $PY = "python" }

# ---------- 0) 二进制工具链自救 ----------
# 0a. app-builder -snld → -snl- 二进制补丁（幂等）
$ab = "desktop\node_modules\app-builder-bin\win\x64\app-builder.exe"
if (Test-Path $ab) {
  & $PY -c "import sys; p=sys.argv[1]; d=open(p,'rb').read(); open(p,'wb').write(d.replace(b'-snld',b'-snl-')); print('app-builder 补丁:', ('已打' if d.count(b'-snld') else '此前已打'))" "$ab"
}

# 0b. 预置 electron-builder 二进制包到 packaging/.eb-binaries 并起本地静态服务
$binDir = "packaging\.eb-binaries"
$pkgs = @{
  "winCodeSign-2.6.0\winCodeSign-2.6.0.7z"     = "winCodeSign-2.6.0/winCodeSign-2.6.0.7z";
  "nsis-3.0.4.1\nsis-3.0.4.1.7z"               = "nsis-3.0.4.1/nsis-3.0.4.1.7z";
  "nsis-resources-3.4.1\nsis-resources-3.4.1.7z" = "nsis-resources-3.4.1/nsis-resources-3.4.1.7z";
}
foreach ($k in $pkgs.Keys) {
  $dst = Join-Path $binDir $k
  if (-not (Test-Path $dst)) {
    New-Item -ItemType Directory -Force (Split-Path $dst) | Out-Null
    Write-Host "下载 $k（npmmirror）..." -ForegroundColor Cyan
    curl.exe -sL -o $dst ("https://npmmirror.com/mirrors/electron-builder-binaries/" + $pkgs[$k])
    if ($LASTEXITCODE -ne 0) { throw "下载失败: $k" }
  }
}
# 本地静态服务已在跑则复用（URL 稳定 → 缓存目录名稳定 → 解包只做一次）
try { (Invoke-WebRequest -UseBasicParsing -TimeoutSec 2 "http://127.0.0.1:8899/winCodeSign-2.6.0/winCodeSign-2.6.0.7z" -Method Head) | Out-Null; $mirrorUp = $true } catch { $mirrorUp = $false }
if (-not $mirrorUp) {
  Start-Process -WindowStyle Hidden -FilePath $PY -ArgumentList '-m','http.server','8899','--bind','127.0.0.1' -WorkingDirectory $binDir
  Start-Sleep -Seconds 1
}
$env:ELECTRON_BUILDER_BINARIES_MIRROR = "http://127.0.0.1:8899/"

# ---------- 1) Windows Python 核心 ----------
if (-not $SkipCore) {
  Write-Host "== PyInstaller 构建 Windows 核心 ==" -ForegroundColor Cyan
  & $PY -m pip install -q pyinstaller requests gmssl pypdf websocket-client
  & $PY -m PyInstaller --name uvmp-core --onedir --clean --noconfirm `
    --add-data "toolkit/apps;apps" `
    --add-data "toolkit/config.example.json;." `
    --hidden-import uvmp --hidden-import vehicle_export --hidden-import official_track `
    --hidden-import xlsx_util --hidden-import devmock --hidden-import rpc `
    --hidden-import pypdf `
    toolkit/app.py
  if ($LASTEXITCODE -ne 0) { throw "PyInstaller 失败" }

  Copy-Item config.ini dist\uvmp-core\config.ini -Force
  Copy-Item toolkit\config.example.json dist\uvmp-core\config.example.json -Force
  Copy-Item packaging\config.initial.json dist\uvmp-core\config.initial.json -Force
  Copy-Item packaging\run-daily-export.vbs dist\uvmp-core\run-daily-export.vbs -Force
  Copy-Item README.md dist\uvmp-core\README.md -Force
  Copy-Item -Recurse install dist\uvmp-core\install -Force

  Write-Host "== 冒烟：selfcheck + rpc ping ==" -ForegroundColor Cyan
  & "dist\uvmp-core\uvmp-core.exe" selfcheck
  if ($LASTEXITCODE -ne 0) { throw "selfcheck 失败" }
  $ping = '{"id":1,"method":"ping"}' | & "dist\uvmp-core\uvmp-core.exe" rpc
  if (-not ($ping -match '"ok": true')) { throw "rpc ping 失败" }
}

# ---------- 2) 前端构建 ----------
Write-Host "== vite 前端构建 ==" -ForegroundColor Cyan
npm run build --prefix desktop
if ($LASTEXITCODE -ne 0) { throw "vite build 失败" }

# ---------- 3) Windows 打包 ----------
# electron-builder 须在 desktop/ 内运行：afterPack 钩子路径按工作目录解析（已踩坑）
Write-Host "== electron-builder：Windows（NSIS + 绿色 zip）==" -ForegroundColor Cyan
if (Test-Path dist-core) { Remove-Item -Recurse -Force dist-core }
New-Item -ItemType Directory -Force dist-core | Out-Null
Copy-Item -Recurse dist\uvmp-core dist-core\core
Push-Location desktop
try { npx electron-builder --win; if ($LASTEXITCODE -ne 0) { throw "electron-builder(win) 失败" } } finally { Pop-Location }

# ---------- 4) Linux 打包（核心由麒麟机/CI 提供，本机只组装 deb/tar.gz）----------
function Pack-Linux($corePath, $archFlag, $label) {
  Write-Host "== electron-builder：Linux $label（deb + tar.gz）==" -ForegroundColor Cyan
  if (Test-Path dist-core) { Remove-Item -Recurse -Force dist-core }
  New-Item -ItemType Directory -Force dist-core | Out-Null
  Copy-Item -Recurse $corePath dist-core\core
  Push-Location desktop
  try { npx electron-builder --linux $archFlag --c.productName=uvmp-toolkit; if ($LASTEXITCODE -ne 0) { throw "electron-builder(linux $label) 失败" } } finally { Pop-Location }
}
if ($LinuxCoreAmd64) { Pack-Linux $LinuxCoreAmd64 "--x64" "amd64" }
if ($LinuxCoreArm64) { Pack-Linux $LinuxCoreArm64 "--arm64" "arm64" }

Write-Host "`n构建完成，产物在 dist-electron/：" -ForegroundColor Green
Get-ChildItem dist-electron -File | Select-Object Name, @{N="MB";E={[math]::Round($_.Length/1MB,1)}}
