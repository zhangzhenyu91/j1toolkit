@echo off
rem 内网工具箱【源码模式】安装脚本（Windows 10+）
rem 仅安装 U 盘单监听 usb-agent 登录计划任务（CLI 无头）；GUI 客户端请用 Release 的 Setup.exe。
rem 用法：双击运行（无需管理员；计划任务以当前用户身份、登录后运行）
setlocal EnableExtensions
chcp 65001 >nul

set "SCRIPT_DIR=%~dp0"
for %%I in ("%SCRIPT_DIR%..\..") do set "BASE_DIR=%%~fI"
set "CONFIG_PATH=%BASE_DIR%\toolkit\config.json"

echo ============================================================
echo 内网工具箱 安装（Windows 源码 CLI 模式，无 GUI）
echo   项目目录: %BASE_DIR%
echo ============================================================

rem ---- 1. Python 检测 ----
set "PYTHONW="
for %%P in (pythonw.exe) do set "PYTHONW=%%~$PATH:P"
if not defined PYTHONW (
  where py >nul 2>&1 && for /f "delims=" %%P in ('py -3 -c "import sys;print(sys.executable.replace('python.exe','pythonw.exe'))" 2^>nul') do set "PYTHONW=%%P"
)
if not defined PYTHONW (
  for %%P in (python.exe) do set "PYTHONW=%%~$PATH:P"
)
if not defined PYTHONW (
  echo [错误] 未找到 Python，请先安装 Python 3.9+ 并加入 PATH
  exit /b 1
)
echo [1/3] Python: %PYTHONW%

rem ---- 2. 依赖检查（免 pip：wheel 解压到 pylib）----
rem 探测到 gmssl.sm2：gmssl 的 sm2.py 顶层 `from Cryptodome.Util.asn1 import ...`，
rem 只 import gmssl 会通过、真用 SM2 才 ImportError（自动登录必挂）→ 需随包 pycryptodomex。
rem 解压时只跳过 linux/macOS 的二进制轮（同名 .pyd/.so 会互相覆盖）：跳过
rem *manylinux* / *musllinux* / *linux_* / *macosx*；解压 *none-any* 纯 Python 轮与 *win32* / *win_amd64* / *win_arm64*。
rem 反向的「win 白名单」会把纯 Python 轮整批漏掉（曾导致装完仍缺 requests）。
"%PYTHONW%" -c "import sys;sys.path.insert(0,r'%BASE_DIR%\pylib');import gmssl.sm2;from Cryptodome.Util.asn1 import DerSequence" >nul 2>&1
if errorlevel 1 (
  echo [2/3] 解压 offline_packages 到 pylib/ ...
  if not exist "%BASE_DIR%\pylib" mkdir "%BASE_DIR%\pylib"
  for %%F in ("%BASE_DIR%\offline_packages\*.whl") do (
    echo %%~nxF | findstr /i /c:"manylinux" /c:"musllinux" /c:"linux_" /c:"macosx" >nul
    if errorlevel 1 "%PYTHONW%" -m zipfile -e "%%F" "%BASE_DIR%\pylib\"
  )
  "%PYTHONW%" -c "import sys;sys.path.insert(0,r'%BASE_DIR%\pylib');import gmssl.sm2;from Cryptodome.Util.asn1 import DerSequence" >nul 2>&1
  if errorlevel 1 (
    echo [错误] 依赖仍不完整，请检查 offline_packages\（需含 pycryptodomex 的 win 版 wheel）
    exit /b 1
  )
) else (
  echo [2/3] 依赖已就绪
)

rem ---- 3. 配置文件 + 计划任务 ----
if not exist "%CONFIG_PATH%" (
  copy /y "%BASE_DIR%\toolkit\config.example.json" "%CONFIG_PATH%" >nul
  echo [3/3] 已生成 %CONFIG_PATH%（请编辑填写 SSO 账号密码与导出时间）
) else (
  echo [3/3] 配置文件已存在，保留
)

rem 旧版每分钟任务：升级时删除，避免与新常驻 agent 双跑。
for %%T in (UVMP-Toolkit-DailyExport UVMP-Toolkit-TreeSyncTick UVMP-Toolkit-TreeSyncNightly UVMP-Toolkit-CrossingSyncTick UVMP-Toolkit-CrossingSyncNightly) do (
  schtasks /delete /tn "%%T" /f >nul 2>&1
)

rem 单一 U 盘监听 agent：登录时隐藏启动，常驻消费服务端指令（自身 flock 单实例）。
schtasks /create /tn "UVMP-Toolkit-UsbAgent" /f /sc onlogon ^
  /tr "\"wscript.exe\" \"%BASE_DIR%\packaging\run-usb-agent.vbs\"" >nul
if errorlevel 1 (
  echo [错误] 计划任务 UVMP-Toolkit-UsbAgent 创建失败
  exit /b 1
)
echo        计划任务 UVMP-Toolkit-UsbAgent 已创建（登录后常驻监听 U 盘指令）

echo ============================================================
echo 安装完成！（源码 CLI 模式：U 盘单监听 agent）
echo   手动检查一次: "%PYTHONW%" "%BASE_DIR%\toolkit\app.py" usb-agent --once
echo   立即启动:     schtasks /run /tn "UVMP-Toolkit-UsbAgent"
echo   GUI 客户端请使用 Release 的 uvmp-toolkit-Setup-*.exe
echo ============================================================
endlocal
