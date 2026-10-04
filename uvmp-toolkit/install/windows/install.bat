@echo off
rem 内网工具箱【源码模式】安装脚本（Windows 10+）
rem 仅安装每日派车单同步计划任务（CLI 无头）；GUI 客户端请用 Release 的 Setup.exe。
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
"%PYTHONW%" -c "import sys;sys.path.insert(0,r'%BASE_DIR%\pylib');import requests" >nul 2>&1
if errorlevel 1 (
  echo [2/3] 解压 offline_packages 到 pylib/ ...
  if not exist "%BASE_DIR%\pylib" mkdir "%BASE_DIR%\pylib"
  for %%F in ("%BASE_DIR%\offline_packages\*.whl") do "%PYTHONW%" -m zipfile -e "%%F" "%BASE_DIR%\pylib\"
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

schtasks /create /tn "UVMP-Toolkit-DailyExport" /f /sc minute /mo 10 ^
  /tr "\"%PYTHONW%\" \"%BASE_DIR%\toolkit\app.py\" daily-export --if-due" >nul
if errorlevel 1 (
  echo [错误] 计划任务创建失败
  exit /b 1
)
echo        计划任务 UVMP-Toolkit-DailyExport 已创建（每 10 分钟检查，到配置时间自动执行）

echo ============================================================
echo 安装完成！（源码 CLI 模式，仅每日定时导出）
echo   手动执行一次:  schtasks /run /tn "UVMP-Toolkit-DailyExport"
echo   GUI 客户端请使用 Release 的 uvmp-toolkit-Setup-*.exe
echo ============================================================
endlocal
