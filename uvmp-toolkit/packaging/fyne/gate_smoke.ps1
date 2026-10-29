# Fyne 轨门禁（Windows 主机）：DLL 依赖检查 + GUI/CDP/OCR 真实冒烟
#
# 用法（在仓库根或任意位置，脚本自定位到 uvmp-toolkit/）：
#   powershell -ExecutionPolicy Bypass -File packaging/fyne/gate_smoke.ps1 `
#       [-StagingDir dist-fyne\windows-amd64]
#
# 门禁内容（glibc 符号检查仅 Linux 有意义，Windows 走 DLL 依赖与真实冒烟）：
#   1. 产物齐全：uvmp-toolkit.exe、uvmp-core.exe、uvmp-export.exe、
#      models\{det,rec}.onnx、native\onnxruntime.dll（browser-runtime 不再是 Windows 必备项）
#   2. DLL 依赖：主二进制与 onnxruntime.dll 的导入表依赖在 PATH/同目录可解析
#      （objdump -p 查 DLL Name；缺 vcruntime 等系统库即失败）
#   3. GUI 冒烟：UVMP_FYNE_SMOKE=gui（契约：首帧渲染成功自行退出 0，见 packaging/fyne/README.md）
#   4. CDP 冒烟：仅在**显式提供浏览器宿主**时执行（UVMP_BROWSER_BIN/APP，或装配布局里存在
#      browser-runtime 的本地联调通道）；Windows 包不再随带 Electron 运行时，未提供即明确跳过
#      （不是静默略过，日志点名开启方式）；执行时即 uvmp-export selftest（data: 本地页零网络，
#      导航/eval/截图/printToPDF 全链路）
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

# ---------- 1. 产物齐全（browser-runtime 不再要求：Windows 包不随带 Electron 运行时） ----------
foreach ($f in @("uvmp-toolkit.exe", "uvmp-core.exe", "uvmp-export.exe",
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
# 第三方输入法（本机搜狗）会在窗口取得前台焦点时注入被测进程，其自带 libpng 会把自己
# 皮肤 PNG 的 "iCCP: known incorrect sRGB profile" 警告写到本进程 stderr（已核实：
# 该字符串只存在于 C:\Windows\System32\sogoupy.ime 等 IME DLL，我方资源/二进制均无 iCCP/libpng）。
# 冒烟进程自身在启动时调 ImmDisableIME（cmd/uvmp-toolkit/imm_windows.go，仅冒烟路径），
# 第三方 IME 不再注入；这里再用 -NoNewWindow 继承当前控制台，不额外新建抢焦点窗口。
# 全程既不屏蔽也不过滤 stderr。
Write-Host "== GUI 冒烟（UVMP_FYNE_SMOKE=gui）=="
$env:UVMP_FYNE_SMOKE = "gui"
$env:UVMP_FYNE_SMOKE_MAXIMIZE = "1" # Windows 原生最大化 → 还原，覆盖尺寸同步与圆角区域
$env:PATH = "$StagingFull\native;$env:PATH"
$p = Start-Process -FilePath (Join-Path $StagingFull "uvmp-toolkit.exe") -PassThru -Wait -NoNewWindow `
  -RedirectStandardOutput "fyne-smoke-gui.log" -RedirectStandardError "fyne-smoke-gui.err.log"
Get-Content "fyne-smoke-gui.log" -ErrorAction SilentlyContinue
Get-Content "fyne-smoke-gui.err.log" -ErrorAction SilentlyContinue
if ($p.ExitCode -ne 0) {
  Fail "GUI 冒烟失败（exit=$($p.ExitCode)）：契约 UVMP_FYNE_SMOKE=gui 须首帧渲染成功后退出 0（见 packaging/fyne/README.md）"
}
Remove-Item Env:UVMP_FYNE_SMOKE -ErrorAction SilentlyContinue
Remove-Item Env:UVMP_FYNE_SMOKE_MAXIMIZE -ErrorAction SilentlyContinue
Write-Host "== GUI 冒烟通过 =="

# ---------- 4. CDP 冒烟（宿主浏览器；不需要 Electron 运行时） ----------
# 口径与核心一致（internal/render.ParseSelftestConfig + internal/browser.DetectHost）：
#   - 默认：uvmp-export selftest 走核心自动检测的宿主浏览器（360ent 优先，兼容 Chrome/Edge；
#     Windows CI 自带 Edge），只打开 data: 本地页、零业务网络 → 默认必跑，不再默认跳过；
#   - 普通宿主只需 UVMP_BROWSER_BIN（显式指定可执行文件），**不需要** UVMP_BROWSER_APP，
#     也不注入 Electron worker（worker 位置参数只对 Electron 回退有意义）；
#   - 显式 UVMP_BROWSER_WORKER=… → 历史 Electron 回退通道，此时 BIN+APP 必须齐全且存在；
#   - 显式 UVMP_GATE_ELECTRON=1 → 才允许用装配目录里的 browser-runtime 当回退宿主；
#   - 显式 UVMP_GATE_SKIP_CDP=1 → 明确跳过（仅排障用）。
# 门禁结束（成功/失败/跳过）一律恢复本进程原有环境变量，不污染后续步骤。
$cdpKeys = @("UVMP_BROWSER_BIN", "UVMP_BROWSER_APP", "UVMP_BROWSER_WORKER", "UVMP_SELFTEST_DIR")
$cdpEnvBackup = @{}
foreach ($k in $cdpKeys) { $cdpEnvBackup[$k] = [Environment]::GetEnvironmentVariable($k, "Process") }
$cdpMode = "跳过"
try {
  if ($env:UVMP_GATE_SKIP_CDP -eq "1") {
    Write-Host "== CDP 冒烟跳过（UVMP_GATE_SKIP_CDP=1 显式跳过；默认门禁不跳过）=="
  } else {
    $bin = $env:UVMP_BROWSER_BIN
    $app = $env:UVMP_BROWSER_APP
    if ($env:UVMP_BROWSER_WORKER) {
      # 历史 Electron 回退：核心只在此分支使用 worker 位置参数，BIN+APP 缺一即失败（同口径）
      if (-not $bin) { Fail "CDP 冒烟：已设 UVMP_BROWSER_WORKER（Electron 回退）但缺 UVMP_BROWSER_BIN" }
      if (-not $app) { Fail "CDP 冒烟：Electron 回退通道缺 UVMP_BROWSER_APP（browser-runtime/app 目录）" }
      foreach ($p in @($bin, $app)) {
        if (-not (Test-Path $p)) { Fail "CDP 冒烟：Electron 回退路径不存在: $p" }
      }
      $cdpMode = "Electron 回退（UVMP_BROWSER_WORKER）"
    } elseif ($env:UVMP_GATE_ELECTRON -eq "1") {
      # 仅显式要求才回退到装配目录里的 Electron 运行时（本地联调通道）
      $rtBin = Join-Path $StagingFull "browser-runtime/electron.exe"
      $rtApp = Join-Path $StagingFull "browser-runtime/app"
      if (-not (Test-Path $rtBin) -or -not (Test-Path (Join-Path $rtApp "main.cjs"))) {
        Fail "CDP 冒烟：UVMP_GATE_ELECTRON=1 但装配目录缺 browser-runtime（先跑 fetch_browser_runtime.ps1 或改用宿主浏览器）"
      }
      $env:UVMP_BROWSER_BIN = $rtBin
      $env:UVMP_BROWSER_APP = $rtApp
      $env:UVMP_BROWSER_WORKER = "--uvmp-browser-mode=export"
      $cdpMode = "Electron 回退（装配目录 browser-runtime）"
    } elseif ($bin) {
      # 普通宿主浏览器显式指定：只需 BIN；不设 APP/WORKER，核心按宿主浏览器链路跑
      $cdpMode = "宿主浏览器（UVMP_BROWSER_BIN=$bin）"
    } else {
      $cdpMode = "宿主浏览器（核心自动检测：360ent / Chrome / Edge）"
    }

    Write-Host "== CDP 冒烟（uvmp-export selftest，$cdpMode）=="
    $selftestDir = Join-Path $env:TEMP ("uvmp-fyne-selftest-" + [guid]::NewGuid().ToString("N"))
    New-Item -ItemType Directory -Force $selftestDir | Out-Null
    $env:UVMP_SELFTEST_DIR = $selftestDir
    '{"config":{}}' | Set-Content -Encoding ascii fyne-smoke-selftest-req.json
    $p = Start-Process -FilePath (Join-Path $StagingFull "uvmp-export.exe") -ArgumentList "selftest" `
      -RedirectStandardInput fyne-smoke-selftest-req.json -RedirectStandardOutput fyne-smoke-selftest.log `
      -RedirectStandardError fyne-smoke-selftest.err.log -PassThru -NoNewWindow -Wait
    Get-Content fyne-smoke-selftest.log -ErrorAction SilentlyContinue
    Get-Content fyne-smoke-selftest.err.log -ErrorAction SilentlyContinue
    if ($p.ExitCode -ne 0) {
      Fail "CDP 冒烟失败（exit=$($p.ExitCode)，$cdpMode）：uvmp-export selftest 未通过（可设 UVMP_BROWSER_BIN 显式指定宿主浏览器）"
    }
    if (-not ((Get-Content fyne-smoke-selftest.log -Raw -ErrorAction SilentlyContinue) -match '"event"')) {
      Fail "CDP 冒烟失败：selftest stdout 无 NDJSON 事件"
    }
    # 自检真实产物（导航/eval/新 tab/截图/printToPDF 全链路落盘）
    foreach ($f in @("cdp-selftest.png", "cdp-selftest.pdf")) {
      if (-not (Test-Path (Join-Path $selftestDir $f))) { Fail "CDP 冒烟失败：缺自检产物 $f（$selftestDir）" }
    }
    Write-Host "== CDP 冒烟通过（$cdpMode）=="
  }
} finally {
  # 恢复门禁前的环境变量（原值为空则删除临时注入），后续步骤不受影响
  foreach ($k in $cdpKeys) {
    if ($null -ne $cdpEnvBackup[$k]) {
      [Environment]::SetEnvironmentVariable($k, $cdpEnvBackup[$k], "Process")
    } else {
      Remove-Item "Env:$k" -ErrorAction SilentlyContinue
    }
  }
}

# ---------- 5. OCR 冒烟（UVMP_FYNE_SMOKE=ocr 契约） ----------
Write-Host "== OCR 冒烟（UVMP_FYNE_SMOKE=ocr：ONNX 模型加载 + 空白图推理自检）=="
$env:UVMP_FYNE_SMOKE = "ocr"
$p = Start-Process -FilePath (Join-Path $StagingFull "uvmp-toolkit.exe") -PassThru -Wait -NoNewWindow `
  -RedirectStandardOutput "fyne-smoke-ocr.log" -RedirectStandardError "fyne-smoke-ocr.err.log"
Get-Content "fyne-smoke-ocr.log" -ErrorAction SilentlyContinue
Get-Content "fyne-smoke-ocr.err.log" -ErrorAction SilentlyContinue
if ($p.ExitCode -ne 0) {
  Fail "OCR 冒烟失败（exit=$($p.ExitCode)）：契约 UVMP_FYNE_SMOKE=ocr 须加载 models/{det,rec}.onnx 跑通推理后退出 0（见 packaging/fyne/README.md）"
}
Remove-Item Env:UVMP_FYNE_SMOKE -ErrorAction SilentlyContinue
Write-Host "== OCR 冒烟通过 =="

if ($env:UVMP_GATE_SKIP_CDP -eq "1") {
  $cdpLabel = "，CDP 按 UVMP_GATE_SKIP_CDP=1 明确跳过"
} else {
  $cdpLabel = " + CDP（$cdpMode）"
}
Write-Host "Fyne 轨门禁全部通过（DLL 依赖 + GUI/OCR$cdpLabel）: $StagingFull"
