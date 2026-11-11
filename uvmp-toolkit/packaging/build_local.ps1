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
# 构建门禁（任一失败即停，不静默跳过）：
#   Python unittest（tests/ + packaging/）→ desktop Node 测试 → vite build →
#   PyInstaller 核心装配完整性（apps/parallel.py、trial 引擎脚本、playwright、websocket、
#   pypdf、Cryptodome）→
#   selfcheck → rpc ping → CDP 冒烟（纯 Python data: 自检；缺宿主浏览器退出码 2 明确报告）→
#   electron-builder → GUI 冒烟（UVMP_CI_SMOKE：真实渲染 + 核心 RPC，隔离配置/状态目录）。
# 纯 Electron+Python 轨：不依赖任何外部 Go 工作进程（Go 导出工作进程已删除）。
#
# Linux 核心本机（Windows）做不出来（PyInstaller 不跨平台、本机无 Docker/WSL）：
#   在麒麟机上跑 packaging/build_on_device.sh 得到核心目录（含 uvmp-core 可执行文件），
#   拷回本机后用上面的 -LinuxCore* 参数指过来即可。若核心 Python 代码相比上一版没变，
#   也可直接复用上一版 release 包里的 resources/core/ 目录。
#
# 本脚本内置两处"本机网络/权限"自救（首次跑通前必看）：
#   1) nsis/win-codesign/7zip 等二进制走 npmmirror 镜像（github.com 直连不通）；
#      npmmirror 重定向 URL 带签名 → electron-builder 缓存目录名每次随机 → 反复重试失败。
#      对策：预下载到 packaging/.eb-binaries/ 并起本地静态服务（127.0.0.1:8899），
#      URL 稳定则缓存命中。electron-builder 26 自带 toolsets 会做 SHA256 校验，
#      本地静态服务只换传输路径、不放宽完整性（旧版对 app-builder.exe 打 -snld 补丁的
#      做法已随 app-builder 一起废弃）。
param(
  [switch]$SkipCore,          # 跳过 Windows 核心构建（复用已有 dist/uvmp-core）
  [string]$LinuxCoreAmd64 = "",
  [string]$LinuxCoreArm64 = ""
)
$ErrorActionPreference = "Stop"
Set-Location (Join-Path $PSScriptRoot "..")

$env:ELECTRON_MIRROR = "https://npmmirror.com/mirrors/electron/"

$PY = $env:UVMP_PYTHON     # 可选：显式指定带 pyinstaller 的 Python（门禁/CI）
if ($PY) {
  if (-not (Test-Path $PY)) { throw "UVMP_PYTHON 不存在: $PY" }
} else {
  # 正式产线（CI）用 packaging/python-runtime.env 里的内置 CPython 3.15.2；
  # 本机出包允许用既有环境，但必须是 3.12+（requirements-core.txt 的最低要求），
  # 且版本与 3.15.2 不一致时会告警——核心包的解释器版本会随本机 Python 漂移。
  $PY = "E:\qtenv\python.exe"   # 3.12 + pyinstaller（本机既有测试环境）
  if (-not (Test-Path $PY)) { $PY = "python" }
}
# 解释器版本门禁：必须 >= 3.12（requirements-core.txt 的最低要求），与内置运行时不一致时告警
$wantVer = ((Get-Content (Join-Path $PSScriptRoot "python-runtime.env") |
    Where-Object { $_ -match "^PYVER=" } | Select-Object -First 1) -replace "^PYVER=", "").Trim()
$gotVer = (& $PY -c "import sys; print('%d.%d.%d' % sys.version_info[:3])" 2>$null | Select-Object -Last 1)
if ($LASTEXITCODE -ne 0 -or -not $gotVer) { throw "无法运行 Python: $PY" }
$gotVer = $gotVer.Trim()
$gotParts = $gotVer.Split(".")
if ([int]$gotParts[0] -lt 3 -or ([int]$gotParts[0] -eq 3 -and [int]$gotParts[1] -lt 12)) {
  throw "Python $gotVer 过低：构建依赖要求 3.12+（正式产线用内置 CPython $wantVer）"
}
if ($gotVer -ne $wantVer) {
  Write-Warning "本机 Python $gotVer 与内置运行时 $wantVer 不一致：本机产物核心包的解释器版本会与 CI 产物不同"
}

# 资产定位：PyInstaller 6（当前钉的 6.22.3）onedir 把 --add-data 资产放进
# dist/uvmp-core/_internal/（sys._MEIPASS 即该目录），PyInstaller 5 放 exe 同目录。
# 只认这两个精确位置；不用 -Recurse 兜底，否则「整包缺件」会被别处同名文件掩盖。
function Get-CorePayloadPath([string]$root, [string]$rel) {
  foreach ($base in @($root, (Join-Path $root "_internal"))) {
    $p = Join-Path $base $rel
    if (Test-Path -LiteralPath $p -PathType Leaf) { return $p }
  }
  return $null
}

# 核心包完整性：数据/二进制落盘 + PYZ 归档模块清单（纯 Python 包在归档里，不落同名目录）
function Assert-CoreBundle([string]$root, [string]$exeName, [int]$withPlaywright = 1) {
  if (-not (Test-Path $root)) { throw "核心目录不存在: $root" }
  $exe = Join-Path $root $exeName
  if (-not (Test-Path $exe)) { throw "核心缺少可执行文件 $exeName" }
  foreach ($rel in @(
      "apps\pcd_export\parallel.py",
      "apps\pcd_export\backend.py",
      "install\linux\uvmp-toolkit.service"
    )) {
    if (-not (Get-CorePayloadPath $root $rel)) {
      throw "核心缺少 $rel（根目录与 _internal 均无，PyInstaller 收编不完整）"
    }
  }
  # 两个 trial 引擎（课程自动学习 / 机巡审核）由 backend.py 按 _MEIPASS 路径加载，
  # 漏 --add-data 时 selfcheck 也能过、只有真跑应用才炸 —— 构建期必须逐文件断言
  foreach ($f in @("config.ini", "config.example.json", "config.initial.json", "run-usb-agent.vbs",
                   "course_autoplay_trial.py", "defect_review_trial.py")) {
    if (-not (Get-CorePayloadPath $root $f)) { throw "核心缺少 $f（根目录与 _internal 均无）" }
  }
  # 纯 Python 包（websocket/pypdf）进 PYZ 归档，不在磁盘生成同名目录——查归档清单
  $listing = & $PY -m PyInstaller.utils.cliutils.archive_viewer -r -b $exe 2>$null
  if ($LASTEXITCODE -ne 0 -or -not $listing) { throw "读取核心归档失败（pyi-archive_viewer）: $exe" }
  $names = @{}
  foreach ($line in $listing) { $names[$line.Trim()] = $true }
  $mods = @("apps", "selftest", "websocket", "pypdf", "Cryptodome")
  if ($withPlaywright) { $mods += @("playwright", "greenlet") }
  foreach ($mod in $mods) {
    if (-not $names.ContainsKey($mod)) { throw "核心归档缺少模块 $mod（PyInstaller 漏收）" }
  }
  if ($withPlaywright) {
    $hit = Get-ChildItem -Path $root -Recurse -Directory -Filter "playwright" -ErrorAction SilentlyContinue
    if (-not $hit) { throw "核心缺少 playwright 包数据目录（-collect-all 漏收）" }
  }
  Write-Host ("核心包完整性：OK（apps/parallel.py、归档模块 " + ($mods -join "/") + "、config/install/vbs）")
}

# ---------- 0) electron-builder 二进制工具链预置 ----------
# 只做「github.com 直连不通 → 走 npmmirror 预下载 + 本地静态服务」这一件事。
# electron-builder 26 已把 app-builder 换成自带 toolsets（7zip/nsis/win-codesign 各自下载并内建
# SHA256 校验），旧版那些「app-builder.exe 二进制打 -snld 补丁」「winCodeSign-2.6.0 .7z」全部
# 不再适用——补丁目标文件已不存在，留着只会静默跳过、给人「已自救」的错觉。
# 包名/版本必须与 electron-builder 26 的 toolsets 期望一致：
#   toolsets/windows.js：win-codesign@1.1.0/win-codesign-windows-x64.zip、nsis-3.0.4.1、
#   nsis-resources-3.4.1；toolsets/7zip.js：7zip@1.0.0/7zip-win-x64.tar.gz
$binDir = "packaging\.eb-binaries"
$pkgs = @{
  "win-codesign@1.1.0\win-codesign-windows-x64.zip" = "win-codesign@1.1.0/win-codesign-windows-x64.zip";
  "nsis-3.0.4.1\nsis-3.0.4.1.7z"                     = "nsis-3.0.4.1/nsis-3.0.4.1.7z";
  "nsis-resources-3.4.1\nsis-resources-3.4.1.7z"     = "nsis-resources-3.4.1/nsis-resources-3.4.1.7z";
  "7zip@1.0.0\7zip-win-x64.tar.gz"                   = "7zip@1.0.0/7zip-win-x64.tar.gz";
}
New-Item -ItemType Directory -Force $binDir | Out-Null
foreach ($k in $pkgs.Keys) {
  $dst = Join-Path $binDir $k
  if (-not (Test-Path $dst)) {
    New-Item -ItemType Directory -Force (Split-Path $dst) | Out-Null
    Write-Host "下载 $k（npmmirror）..." -ForegroundColor Cyan
    curl.exe -sL --retry 3 -o $dst ("https://npmmirror.com/mirrors/electron-builder-binaries/" + $pkgs[$k])
    if ($LASTEXITCODE -ne 0 -or -not (Test-Path $dst) -or (Get-Item $dst).Length -eq 0) {
      Remove-Item $dst -ErrorAction SilentlyContinue
      throw "下载失败: $k"
    }
  }
}
# 本地静态服务已在跑则复用（URL 稳定 → 缓存目录名稳定 → 解包只做一次）
try { (Invoke-WebRequest -UseBasicParsing -TimeoutSec 2 "http://127.0.0.1:8899/nsis-3.0.4.1/nsis-3.0.4.1.7z" -Method Head) | Out-Null; $mirrorUp = $true } catch { $mirrorUp = $false }
if (-not $mirrorUp) {
  Start-Process -WindowStyle Hidden -FilePath $PY -ArgumentList '-m','http.server','8899','--bind','127.0.0.1' -WorkingDirectory $binDir
  Start-Sleep -Seconds 1
}
$env:ELECTRON_BUILDER_BINARIES_MIRROR = "http://127.0.0.1:8899/"

# ---------- 1) 源码门禁（Python / Node / 前端）----------
Write-Host "== 门禁：Python unittest ==" -ForegroundColor Cyan
& $PY -m unittest discover -s tests -v
if ($LASTEXITCODE -ne 0) { throw "Python unittest 失败" }
& $PY -m unittest discover -s packaging -p "test_*.py" -v
if ($LASTEXITCODE -ne 0) { throw "packaging 门禁自测失败" }

Write-Host "== 门禁：desktop Node 测试 ==" -ForegroundColor Cyan
if (-not (Test-Path "desktop\node_modules")) {
  npm ci --prefix desktop
  if ($LASTEXITCODE -ne 0) { throw "npm ci 失败" }
}
Push-Location desktop
try {
  node --test "test/*.test.mjs" "test/*.test.cjs"
  if ($LASTEXITCODE -ne 0) { throw "desktop Node 测试失败" }
} finally { Pop-Location }

Write-Host "== 门禁：vite 前端构建 ==" -ForegroundColor Cyan
npm run build --prefix desktop
if ($LASTEXITCODE -ne 0) { throw "vite build 失败" }

# ---------- 2) Windows Python 核心 ----------
if (-not $SkipCore) {
  Write-Host "== PyInstaller 构建 Windows 核心 ==" -ForegroundColor Cyan
  # 依赖全钉版本（packaging/requirements-core.txt），装完 pip check 保证组合自洽
  & $PY -m pip install -q -r packaging/requirements-core.txt
  if ($LASTEXITCODE -ne 0) { throw "Python 构建依赖安装失败" }
  & $PY -m pip check
  if ($LASTEXITCODE -ne 0) { throw "Python 依赖冲突（pip check 失败）" }
  & $PY -m PyInstaller --name uvmp-core --onedir --clean --noconfirm `
    --add-data "toolkit/apps;apps" `
    --add-data "toolkit/config.example.json;." `
    --add-data "course_autoplay_trial.py;." `
    --add-data "defect_review_trial.py;." `
    --collect-all playwright --collect-all greenlet `
    --hidden-import pyee --hidden-import typing_extensions `
    --hidden-import Cryptodome --hidden-import Cryptodome.Util.asn1 `
    --hidden-import browser_automation `
    --hidden-import uvmp --hidden-import vehicle_export --hidden-import official_track `
    --hidden-import xlsx_util --hidden-import devmock --hidden-import rpc `
    --hidden-import usb_agent --hidden-import usb_protocol --hidden-import dev_script_runner `
    --hidden-import cluster --hidden-import cluster.service `
    --hidden-import cluster.identity --hidden-import cluster.registry `
    --hidden-import cluster.protocol --hidden-import cluster.export --hidden-import version `
    --hidden-import scheduler `
    --hidden-import selftest --hidden-import websocket --hidden-import pypdf `
    toolkit/app.py
  if ($LASTEXITCODE -ne 0) { throw "PyInstaller 失败" }

  Copy-Item config.ini dist\uvmp-core\config.ini -Force
  Copy-Item toolkit\config.example.json dist\uvmp-core\config.example.json -Force
  Copy-Item packaging\config.initial.json dist\uvmp-core\config.initial.json -Force
  Copy-Item packaging\run-usb-agent.vbs dist\uvmp-core\run-usb-agent.vbs -Force
  Copy-Item README.md dist\uvmp-core\README.md -Force
  Copy-Item -Recurse install dist\uvmp-core\install -Force
}

# 复用已有核心（-SkipCore）也必跑冒烟，避免「跳过构建」顺带跳过门禁
if (-not (Test-Path "dist\uvmp-core\uvmp-core.exe")) {
  throw "核心不存在：dist\uvmp-core\uvmp-core.exe（-SkipCore 需已有构建产物）"
}
Write-Host "== 冒烟：selfcheck + rpc ping ==" -ForegroundColor Cyan
& "dist\uvmp-core\uvmp-core.exe" selfcheck
if ($LASTEXITCODE -ne 0) { throw "selfcheck 失败" }
$ping = '{"id":1,"method":"ping"}' | & "dist\uvmp-core\uvmp-core.exe" rpc
if (-not ($ping -match '"ok": true')) { throw "rpc ping 失败" }

Assert-CoreBundle "dist\uvmp-core" "uvmp-core.exe"

Write-Host "== 门禁：CDP 冒烟（纯 Python data: 自检）==" -ForegroundColor Cyan
& $PY packaging\cdp_smoke.py --core dist\uvmp-core\uvmp-core.exe --timeout 240
$cdpCode = $LASTEXITCODE
if ($cdpCode -eq 2) {
  Write-Warning "CDP 冒烟已明确跳过：宿主无可用浏览器；本次构建未验证 CDP 链路（装公司指定浏览器后重跑）"
} elseif ($cdpCode -ne 0) {
  throw "CDP 冒烟失败（exit=$cdpCode）"
}

# ---------- 3) Windows 打包 ----------
# electron-builder 须在 desktop/ 内运行：afterPack 钩子路径按工作目录解析（已踩坑）
Write-Host "== electron-builder：Windows（NSIS + 绿色 zip）==" -ForegroundColor Cyan
if (Test-Path dist-core) { Remove-Item -Recurse -Force dist-core }
New-Item -ItemType Directory -Force dist-core | Out-Null
Copy-Item -Recurse dist\uvmp-core dist-core\core
Push-Location desktop
try { npx electron-builder --win; if ($LASTEXITCODE -ne 0) { throw "electron-builder(win) 失败" } } finally { Pop-Location }

# ---------- 3b) GUI 冒烟（UVMP_CI_SMOKE：真实渲染 + 核心 RPC，隔离配置/状态）----------
Write-Host "== 门禁：GUI 冒烟（UVMP_CI_SMOKE）==" -ForegroundColor Cyan
$exe = (Get-ChildItem "dist-electron\win-unpacked\*.exe" -ErrorAction SilentlyContinue | Select-Object -First 1)
if (-not $exe) { throw "未找到 dist-electron\win-unpacked\*.exe" }
$smokeDir = Join-Path $env:TEMP ("uvmp-ci-smoke-" + [guid]::NewGuid().ToString("N"))
New-Item -ItemType Directory -Force $smokeDir | Out-Null
Copy-Item toolkit\config.example.json (Join-Path $smokeDir "config.json") -Force
$smokeKeys = @("UVMP_CI_SMOKE", "ELECTRON_ENABLE_LOGGING", "UVMP_TOOLKIT_CONFIG", "LOCALAPPDATA", "APPDATA")
$savedEnv = @{}
foreach ($k in $smokeKeys) { $savedEnv[$k] = [Environment]::GetEnvironmentVariable($k, "Process") }
try {
  $env:UVMP_CI_SMOKE = "1"
  $env:ELECTRON_ENABLE_LOGGING = "1"
  $env:UVMP_TOOLKIT_CONFIG = Join-Path $smokeDir "config.json"
  $env:LOCALAPPDATA = $smokeDir
  $env:APPDATA = $smokeDir
  $outLog = Join-Path $smokeDir "smoke-out.log"
  $errLog = Join-Path $smokeDir "smoke-err.log"
  # Start-Process -PassThru 对 Electron 进程取不到 ExitCode（空值会误判失败），改走 .NET Process
  $psi = New-Object System.Diagnostics.ProcessStartInfo
  $psi.FileName = $exe.FullName
  $psi.UseShellExecute = $false
  $psi.RedirectStandardOutput = $true
  $psi.RedirectStandardError = $true
  $proc = New-Object System.Diagnostics.Process
  $proc.StartInfo = $psi
  $null = $proc.Start()
  $outTask = $proc.StandardOutput.ReadToEndAsync()
  $errTask = $proc.StandardError.ReadToEndAsync()
  if (-not $proc.WaitForExit(180000)) { $proc.Kill(); throw "GUI 冒烟超时（180s）" }
  $proc.WaitForExit()
  ($outTask.Result -split "`r?`n") | Select-Object -Last 20 | Set-Content $outLog
  ($errTask.Result -split "`r?`n") | Select-Object -Last 30 | Set-Content $errLog
  Write-Host "---- smoke stdout ----"; Get-Content $outLog -ErrorAction SilentlyContinue | Select-Object -Last 20
  Write-Host "---- smoke stderr ----"; Get-Content $errLog -ErrorAction SilentlyContinue | Select-Object -Last 30
  if ($proc.ExitCode -ne 0) { throw "GUI 冒烟失败 exit=$($proc.ExitCode)" }
} finally {
  foreach ($k in $smokeKeys) {
    if ($null -eq $savedEnv[$k]) { Remove-Item "Env:$k" -ErrorAction SilentlyContinue }
    else { Set-Item "Env:$k" $savedEnv[$k] }
  }
  Remove-Item -Recurse -Force $smokeDir -ErrorAction SilentlyContinue
}

# ---------- 4) Linux 打包（核心由麒麟机/CI 提供，本机只组装 deb/tar.gz）----------
function Pack-Linux($corePath, $archFlag, $label) {
  Write-Host "== electron-builder：Linux $label（deb + tar.gz）==" -ForegroundColor Cyan
  if (Test-Path dist-core) { Remove-Item -Recurse -Force dist-core }
  New-Item -ItemType Directory -Force dist-core | Out-Null
  Copy-Item -Recurse $corePath dist-core\core
  Assert-CoreBundle "dist-core\core" "uvmp-core"
  Push-Location desktop
  try { npx electron-builder --linux $archFlag --c.productName=uvmp-toolkit; if ($LASTEXITCODE -ne 0) { throw "electron-builder(linux $label) 失败" } } finally { Pop-Location }
}
if ($LinuxCoreAmd64) { Pack-Linux $LinuxCoreAmd64 "--x64" "amd64" }
if ($LinuxCoreArm64) { Pack-Linux $LinuxCoreArm64 "--arm64" "arm64" }

Write-Host "`n构建完成，产物在 dist-electron/：" -ForegroundColor Green
Get-ChildItem dist-electron -File | Select-Object Name, @{N="MB";E={[math]::Round($_.Length/1MB,1)}}
