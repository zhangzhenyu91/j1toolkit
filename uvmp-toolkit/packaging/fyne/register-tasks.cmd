@echo off
rem Shade Yixia - Intranet toolkit (Fyne track) OS scheduled tasks register/cleanup.
rem ASCII-only on purpose: cmd.exe misparses UTF-8 multibyte text at its 512-byte
rem batch read boundary (verified 2026-10-29; same reason as desktop/build/installer.nsh).
rem Chinese docs: packaging/fyne/README.md (OS scheduling contract).
rem
rem Usage: register-tasks.cmd [install^|remove]
rem   install (default): register/replace the 5 UVMP-Toolkit-* tasks (every minute,
rem     wscript run-uvmp.vbs runs uvmp-core.exe hidden with same-name args;
rem     due-check gating lives in the core).
rem   remove: delete all 5 tasks (for uninstall; never restores old task definitions).
rem Task names match the legacy installers (Electron track / source mode) on purpose:
rem upgrade-replace semantics, last install wins, no double-run of exports.
rem If a legacy task was created elevated (high integrity label), replace/delete is
rem denied at medium integrity (exit 1) -> caller retries elevated (UAC) or fails loud.
setlocal EnableExtensions
set "DIR=%~dp0"
if /i "%~1"=="remove" goto :REMOVE

call :REG "UVMP-Toolkit-DailyExport" "daily-export --if-due" || exit /b 1
call :REG "UVMP-Toolkit-TreeSyncTick" "tree-sync --tick" || exit /b 1
call :REG "UVMP-Toolkit-TreeSyncNightly" "tree-sync --nightly --if-due" || exit /b 1
call :REG "UVMP-Toolkit-CrossingSyncTick" "crossing-sync --tick" || exit /b 1
call :REG "UVMP-Toolkit-CrossingSyncNightly" "crossing-sync --nightly --if-due" || exit /b 1
echo [OK] 5 tasks registered (same-name upgrade replace, no double-run)
exit /b 0

:REG
schtasks /query /tn "%~1" >nul 2>&1 && echo [REPLACE] %~1 (legacy task found, take over) || echo [CREATE] %~1
schtasks /create /tn "%~1" /f /sc minute /mo 1 /tr "wscript.exe \"%DIR%run-uvmp.vbs\" %~2" >nul
if errorlevel 1 (
  echo [FAIL] %~1 register denied - retry elevated if legacy task was created by an elevated install
  exit /b 1
)
exit /b 0

:REMOVE
set "FAILED=0"
call :DEL "UVMP-Toolkit-DailyExport" || set "FAILED=1"
call :DEL "UVMP-Toolkit-TreeSyncTick" || set "FAILED=1"
call :DEL "UVMP-Toolkit-TreeSyncNightly" || set "FAILED=1"
call :DEL "UVMP-Toolkit-CrossingSyncTick" || set "FAILED=1"
call :DEL "UVMP-Toolkit-CrossingSyncNightly" || set "FAILED=1"
if "%FAILED%"=="1" (
  echo [FAIL] some tasks could not be deleted - retry elevated if needed
  exit /b 1
)
echo [OK] task cleanup done (missing tasks skipped; old definitions never restored)
exit /b 0

:DEL
schtasks /query /tn "%~1" >nul 2>&1 || exit /b 0
schtasks /delete /tn "%~1" /f >nul 2>&1 && (echo [DELETE] %~1 & exit /b 0)
echo [FAIL] %~1 delete denied
exit /b 1
