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
rem
rem 还须按解释器 ABI 过滤：Pillow 13 只有 cp311+ 的二进制轮（随包种子是 cp315）。
rem 把 cp315 的 .pyd 解进 3.7 的 pylib 只会得到 ImportError（magic tag 不匹配），
rem 而 abi3 轮（pycryptodomex cp37-abi3）与 none-any 轮任何 3.x 都能装。
rem
rem 依赖分两级：DEP_CORE 缺失必须中止；DEP_IMAGING（Pillow）缺失只警告——
rem 3.11–3.14 的机器装不上 cp315 的 Pillow 轮，但核心功能都还能用。
rem 版本读取不用 for /f：cmd 会剥掉命令的外层引号，括号还须额外转义，
rem 含空格的解释器路径更是直接解析失败（实测三种引号写法都恒为空）。
rem 改为「让 Python 自己写临时文件 + set /p 读回」，引号与路径空格都不参与解析。
rem ::UVMP_PYVER_PROBE::
set "PY_VER_TAG="
set "VER_FILE=%TEMP%\uvmp-pyver-%RANDOM%%RANDOM%.txt"
if exist "%VER_FILE%" del "%VER_FILE%" >nul 2>&1
"%PYTHONW%" -c "import sys;open(r'%VER_FILE%','w',encoding='utf-8').write('cp%%d%%d' %% sys.version_info[:2])" 2>nul
if exist "%VER_FILE%" set /p PY_VER_TAG=<"%VER_FILE%"
if exist "%VER_FILE%" del "%VER_FILE%" >nul 2>&1
rem ::/UVMP_PYVER_PROBE::
if not defined PY_VER_TAG (
  echo [错误] 无法读取 Python 版本（需要 3.7+）
  exit /b 1
)
set "DEP_CORE=requests, pypdf, websocket, gmssl.sm2, Cryptodome.Util.asn1"
set "DEP_IMAGING="
rem Pillow 13 要求 3.11+：更低版本不探测 PIL，也不解 Pillow 轮
"%PYTHONW%" -c "import sys;raise SystemExit(0 if sys.version_info>=(3,11) else 1)" >nul 2>&1
if not errorlevel 1 set "DEP_IMAGING=PIL.Image, PIL.ImageOps"
echo [2/3] 解释器标签: %PY_VER_TAG%
"%PYTHONW%" -c "import sys;sys.path.insert(0,r'%BASE_DIR%\pylib');import gmssl.sm2;from Cryptodome.Util.asn1 import DerSequence" >nul 2>&1
if errorlevel 1 (
  echo [2/3] 解压 offline_packages 到 pylib/ ...
  if not exist "%BASE_DIR%\pylib" mkdir "%BASE_DIR%\pylib"
  for %%F in ("%BASE_DIR%\offline_packages\*.whl") do (
    rem 平台过滤：只跳过 linux/macOS 的二进制轮
    echo %%~nxF | findstr /i /c:"manylinux" /c:"musllinux" /c:"linux_" /c:"macosx" >nul
    if errorlevel 1 (
      rem ABI 过滤：纯 Python 轮与 abi3 轮直接解；其余二进制轮要求文件名含当前解释器标签
      echo %%~nxF | findstr /i /c:"none-any" /c:"abi3" >nul
      if not errorlevel 1 (
        "%PYTHONW%" -m zipfile -e "%%F" "%BASE_DIR%\pylib\"
      ) else (
        echo %%~nxF | findstr /i /c:"-cp3" /c:"-cp4" >nul
        if errorlevel 1 (
          "%PYTHONW%" -m zipfile -e "%%F" "%BASE_DIR%\pylib\"
        ) else (
          echo %%~nxF | findstr /i /c:"%PY_VER_TAG%" >nul
          if not errorlevel 1 "%PYTHONW%" -m zipfile -e "%%F" "%BASE_DIR%\pylib\"
        )
      )
    )
  )
) else (
  echo [2/3] 依赖已就绪
)

rem 核心依赖缺失 = 真的装不起来，必须中止
"%PYTHONW%" -c "import sys;sys.path.insert(0,r'%BASE_DIR%\pylib');[__import__(m.strip()) for m in r'%DEP_CORE%'.split(',') if m.strip()]" >nul 2>&1
if errorlevel 1 (
  echo [错误] 核心依赖不完整，请检查 offline_packages\（需含 pycryptodomex 的 win 版 wheel）
  exit /b 1
)

rem 图片依赖是可选能力：缺了只警告「图片功能不可用」，绝不阻断安装
if defined DEP_IMAGING (
  "%PYTHONW%" -c "import sys;sys.path.insert(0,r'%BASE_DIR%\pylib');[__import__(m.strip()) for m in r'%DEP_IMAGING%'.split(',') if m.strip()]" >nul 2>&1
  if errorlevel 1 (
    echo       警告：图片工具不可用（当前 Python %PY_VER_TAG%）
    echo             offline_packages\ 里没有与 %PY_VER_TAG% 匹配的 Pillow 轮（随包只有 cp315）。
    echo             需要图片功能时请安装 Python 3.15 或自备对应版本的 Pillow 轮。
    echo             派车同步/轨迹导出/题库搜题/批量重命名等其余功能不受影响。
  ) else (
    echo       图片依赖 OK（%DEP_IMAGING%）
  )
) else (
  echo       警告：图片工具不可用（当前 Python 低于 3.11，Pillow 13 不支持）
  echo             派车同步/轨迹导出/题库搜题/批量重命名等其余功能不受影响。
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
