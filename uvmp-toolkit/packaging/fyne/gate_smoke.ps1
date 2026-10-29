# Fyne 轨门禁（Windows 主机）：DLL 依赖检查 + GUI/CDP/OCR 真实冒烟
#
# 用法（在仓库根或任意位置，脚本自定位到 uvmp-toolkit/）：
#   powershell -ExecutionPolicy Bypass -File packaging/fyne/gate_smoke.ps1 `
#       [-StagingDir dist-fyne\windows-amd64]
#
# 门禁内容（glibc 符号检查仅 Linux 有意义，Windows 走 DLL 依赖与真实冒烟）：
#   1. 产物齐全：uvmp-toolkit.exe、uvmp-core.exe、uvmp-export.exe、browser-runtime\electron.exe、
#      browser-runtime\app\{package.json,main.cjs}、models\{det,rec}.onnx、native\onnxruntime.dll
#   2. DLL 依赖：主二进制与 onnxruntime.dll 的导入表依赖在 PATH/同目录可解析
#      （objdump -p 查 DLL Name；缺 vcruntime 等系统库即失败）
#   3. GUI 冒烟：UVMP_FYNE_SMOKE=gui（契约：首帧渲染成功自行退出 0，见 packaging/fyne/README.md）
#   4. CDP 冒烟：uvmp-export selftest（UVMP_BROWSER_BIN/APP 指向装配运行时、
#      UVMP_BROWSER_WORKER=--uvmp-browser-mode=export 双模式导出口径；
#      只开 data: 本地页零网络，导航/eval/截图/printToPDF 全链路）
#   5. OCR 冒烟：UVMP_FYNE_SMOKE=ocr（GUI 侧 ONNX 推理自检：加载 det/rec 模型对空白图跑通并退出 0；
#      需 GUI 以 -tags onnx 构建，见 build_fyne_core.* 的 UVMP_FYNE_TAGS）
param(
  [string]$StagingDir = ""
)
$ErrorActionPreference = "Stop"
Set-Location (Join-Path $PSScriptRoot "..\..")

function Fail([string]$msg) { Write-Host "门禁失败: $msg" -ForegroundColor Red; exit 1 }

if ($StagingDir -eq "") { $StagingDir = "dist-fyne/windows-amd64" }
$StagingFull = [System.IO.Path]::GetFullPath($StagingDir)
if (-not (Test-Path $StagingFull)) { Fail "缺暂存目录: $StagingFull" }

# ---------- 1. 产物齐全 ----------
foreach ($f in @("uvmp-toolkit.exe", "uvmp-core.exe", "uvmp-export.exe", "browser-runtime/electron.exe",
                 "browser-runtime/app/main.cjs", "browser-runtime/app/package.json",
                 "models/det.onnx", "models/rec.onnx",
                 "native/onnxruntime.dll")) {
  if (-not (Test-Path (Join-Path $StagingFull $f))) { Fail "产物缺失: $f" }
}
Write-Host "== 产物齐全检查通过 =="

# ---------- 2. DLL 依赖（objdump 导入表；缺系统依赖即失败） ----------
function Get-DllImports([string]$pe) {
  $out = & objdump -p $pe 2>$null | Select-String "^\s*DLL Name:\s*(.+)$"
  foreach ($line in $out) { ($line.Matches[0].Groups[1].Value.Trim()) }
}
$objdump = Get-Command objdump -ErrorAction SilentlyContinue
if ($objdump) {
  Write-Host "== DLL 依赖门禁（objdump 导入表）=="
  foreach ($pe in @("uvmp-toolkit.exe", "uvmp-core.exe", "uvmp-export.exe", "native/onnxruntime.dll")) {
    $path = Join-Path $StagingFull $pe
    $imports = @(Get-DllImports $path)
    if ($imports.Count -eq 0) { Write-Host "  ${pe}: 无导入表或静态，跳过" ; continue }
    foreach ($dll in $imports) {
      $lower = $dll.ToLower()
      # 系统 DLL 由 Windows 提供；其余需在 staging 内或 PATH 可解析
      $isSystem = $lower -match '^(kernel32|user32|gdi32|advapi32|shell32|ole32|oleaut32|comdlg32|comctl32|ws2_32|wsock32|ntdll|msvcrt|ucrtbase|userenv|shlwapi|version|iphlpapi|psapi|dbghelp|sechost|rpcrt4|setupapi|cfgmgr32|winmm|imm32|crypt32|bcrypt|ncrypt|dnsapi|wldap32|dhcpcsvc|powrprof|wtsapi32|winsta|dwmapi|uxtheme|d3d11|dxgi|opengl32|glu32|hid|propsys|wevtapi|normaliz|api-ms-win-).*\.dll$'
      if ($isSystem) { continue }
      $local = Join-Path $StagingFull $dll
      $native = Join-Path (Join-Path $StagingFull "native") $dll
      if (-not (Test-Path $local) -and -not (Test-Path $native) -and -not (Get-Command $dll -ErrorAction SilentlyContinue)) {
        Fail "DLL 依赖缺失: $pe -> $dll"
      }
    }
    Write-Host "  ${pe}: 导入依赖可解析"
  }
  Write-Host "== DLL 依赖门禁通过 =="
} else {
  Write-Host "提示: 未找到 objdump（MSYS2 binutils），跳过导入表检查（冒烟门禁仍执行）"
}

# ---------- 3. GUI 冒烟 ----------
Write-Host "== GUI 冒烟（UVMP_FYNE_SMOKE=gui）=="
$env:UVMP_FYNE_SMOKE = "gui"
$env:PATH = "$StagingFull\native;$env:PATH"
$p = Start-Process -FilePath (Join-Path $StagingFull "uvmp-toolkit.exe") -PassThru -Wait `
  -RedirectStandardOutput "fyne-smoke-gui.log" -RedirectStandardError "fyne-smoke-gui.err.log"
Get-Content "fyne-smoke-gui.log" -ErrorAction SilentlyContinue
Get-Content "fyne-smoke-gui.err.log" -ErrorAction SilentlyContinue
if ($p.ExitCode -ne 0) {
  Fail "GUI 冒烟失败（exit=$($p.ExitCode)）：契约 UVMP_FYNE_SMOKE=gui 须首帧渲染成功后退出 0（见 packaging/fyne/README.md）"
}
Remove-Item Env:UVMP_FYNE_SMOKE -ErrorAction SilentlyContinue
Write-Host "== GUI 冒烟通过 =="

# ---------- 4. CDP 冒烟（uvmp-export selftest，data: 页零网络） ----------
Write-Host "== CDP 冒烟（uvmp-export selftest，装配浏览器运行时）=="
$selftestDir = Join-Path $env:TEMP ("uvmp-fyne-selftest-" + [guid]::NewGuid().ToString("N"))
New-Item -ItemType Directory -Force $selftestDir | Out-Null
$env:UVMP_BROWSER_BIN = Join-Path $StagingFull "browser-runtime/electron.exe"
$env:UVMP_BROWSER_APP = Join-Path $StagingFull "browser-runtime/app"
$env:UVMP_BROWSER_WORKER = "--uvmp-browser-mode=export"
$env:UVMP_SELFTEST_DIR = $selftestDir
'{"config":{}}' | Set-Content -Encoding ascii fyne-smoke-selftest-req.json
$p = Start-Process -FilePath (Join-Path $StagingFull "uvmp-export.exe") -ArgumentList "selftest" `
  -RedirectStandardInput fyne-smoke-selftest-req.json -RedirectStandardOutput fyne-smoke-selftest.log `
  -RedirectStandardError fyne-smoke-selftest.err.log -PassThru -NoNewWindow -Wait
Get-Content fyne-smoke-selftest.log -ErrorAction SilentlyContinue
Get-Content fyne-smoke-selftest.err.log -ErrorAction SilentlyContinue
if ($p.ExitCode -ne 0) { Fail "CDP 冒烟失败（exit=$($p.ExitCode)）：uvmp-export selftest 未通过" }
if (-not ((Get-Content fyne-smoke-selftest.log -Raw -ErrorAction SilentlyContinue) -match '"event"')) {
  Fail "CDP 冒烟失败：selftest stdout 无 NDJSON 事件"
}
foreach ($k in @("UVMP_BROWSER_BIN", "UVMP_BROWSER_APP", "UVMP_BROWSER_WORKER", "UVMP_SELFTEST_DIR")) {
  Remove-Item "Env:$k" -ErrorAction SilentlyContinue
}
Write-Host "== CDP 冒烟通过 =="

# ---------- 5. OCR 冒烟（UVMP_FYNE_SMOKE=ocr 契约） ----------
Write-Host "== OCR 冒烟（UVMP_FYNE_SMOKE=ocr：ONNX 模型加载 + 空白图推理自检）=="
$env:UVMP_FYNE_SMOKE = "ocr"
$p = Start-Process -FilePath (Join-Path $StagingFull "uvmp-toolkit.exe") -PassThru -Wait `
  -RedirectStandardOutput "fyne-smoke-ocr.log" -RedirectStandardError "fyne-smoke-ocr.err.log"
Get-Content "fyne-smoke-ocr.log" -ErrorAction SilentlyContinue
Get-Content "fyne-smoke-ocr.err.log" -ErrorAction SilentlyContinue
if ($p.ExitCode -ne 0) {
  Fail "OCR 冒烟失败（exit=$($p.ExitCode)）：契约 UVMP_FYNE_SMOKE=ocr 须加载 models/{det,rec}.onnx 跑通推理后退出 0（见 packaging/fyne/README.md）"
}
Remove-Item Env:UVMP_FYNE_SMOKE -ErrorAction SilentlyContinue
Write-Host "== OCR 冒烟通过 =="

Write-Host "Fyne 轨门禁全部通过（DLL 依赖 + GUI/CDP/OCR）: $StagingFull"
