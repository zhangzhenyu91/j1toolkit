@echo off
rem 内网工具箱 卸载脚本（Windows）：删除计划任务与桌面快捷方式；配置文件保留。
setlocal EnableExtensions
chcp 65001 >nul

schtasks /delete /tn "UVMP-Toolkit-DailyExport" /f >nul 2>&1
if errorlevel 1 (echo 计划任务不存在或已删除) else (echo 计划任务已删除)

rem 新旧两种快捷方式名一并清理（v2.5.7 起安装包显示名改为「Shade 壹匣 - 内网」）
powershell -NoProfile -ExecutionPolicy Bypass -Command ^
  "$d = [Environment]::GetFolderPath('Desktop');" ^
  "foreach ($n in @('内网工具箱.lnk', 'Shade 壹匣 - 内网.lnk')) { $p = Join-Path $d $n; if (Test-Path $p) { Remove-Item $p -Force } }" >nul 2>&1
echo 桌面快捷方式已删除
echo 卸载完成（项目文件与 toolkit\config.json 保留，可手动删除）
endlocal
