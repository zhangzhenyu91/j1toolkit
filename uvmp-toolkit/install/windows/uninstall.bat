@echo off
rem 内网工具箱 卸载脚本（Windows）：删除计划任务与桌面快捷方式；配置文件保留。
setlocal EnableExtensions
chcp 65001 >nul

rem 三条计划任务一并删除（与 install.bat 对齐；不存在的静默跳过）
for %%T in ("UVMP-Toolkit-DailyExport" "UVMP-Toolkit-TreeSyncTick" "UVMP-Toolkit-TreeSyncNightly" "UVMP-Toolkit-CrossingSyncTick" "UVMP-Toolkit-CrossingSyncNightly") do schtasks /delete /tn %%T /f >nul 2>&1
echo 计划任务已删除（不存在的跳过）

rem 新旧两种快捷方式名一并清理（v2.5.7 起安装包显示名改为「Shade 壹匣 - 内网」）
powershell -NoProfile -ExecutionPolicy Bypass -Command ^
  "$d = [Environment]::GetFolderPath('Desktop');" ^
  "foreach ($n in @('内网工具箱.lnk', 'Shade 壹匣 - 内网.lnk')) { $p = Join-Path $d $n; if (Test-Path $p) { Remove-Item $p -Force } }" >nul 2>&1
echo 桌面快捷方式已删除
echo 卸载完成（项目文件与 toolkit\config.json 保留，可手动删除）
endlocal
